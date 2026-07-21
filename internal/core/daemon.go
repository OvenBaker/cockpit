package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type daemon struct {
	root, socket, epoch  string
	st                   *store
	tm                   tmux
	lock                 *os.File
	serverLock           *os.File
	listener             net.Listener
	socketDev, socketIno uint64
	lifeMu               sync.Mutex
	closeMu              sync.Mutex
	closed               bool
	mu                   sync.Mutex
	paneLocks            map[string]*sync.Mutex
	watchers             map[string]*watcher
	watchMu              sync.Mutex
	subs                 map[string]*subscription
	subMu                sync.Mutex
	eventSeq             uint64
	eventRing            []eventSummary
}
type watcher struct {
	paneRef      string
	operationRef string
	after        int64
	ch           chan map[string]any
	done         chan struct{}
}
type subscription struct {
	ch                    chan map[string]any
	paneRef, operationRef string
	writer                *frameWriter
}
type eventSummary struct {
	Seq                         uint64
	Kind, PaneRef, OperationRef string
	Version                     int64
	At                          time.Time
}
type frameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *frameWriter) write(v any) error { w.mu.Lock(); defer w.mu.Unlock(); return writeFrame(w.w, v) }

func NewDaemon(root, socket, tmuxSocket string) (*daemon, error) {
	if tmuxSocket == "cockpit" {
		return nil, errors.New("refusing live tmux socket cockpit")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(tmuxSocket) {
		return nil, errors.New("invalid tmux socket scalar")
	}
	clean := filepath.Clean(root)
	if !filepath.IsAbs(clean) || clean == "/tmp" || strings.Contains(clean, "..") {
		return nil, errors.New("test root must be a dedicated absolute directory")
	}
	if !strings.HasPrefix(filepath.Clean(socket), clean+string(os.PathSeparator)) {
		return nil, errors.New("socket must be inside test root")
	}
	if err := os.MkdirAll(clean, 0700); err != nil {
		return nil, err
	}
	t := tmux{socket: tmuxSocket, trace: tracePath(clean)}
	serverSocket, e := (tmux{socket: tmuxSocket}).serverSocketPath()
	if e != nil {
		return nil, e
	}
	slf, e := os.OpenFile(serverSocket+".cockpit.lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(slf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		_ = slf.Close()
		return nil, fmt.Errorf("tmux server lease unavailable: %w", e)
	}
	leaseTransferred := false
	defer func() {
		if !leaseTransferred {
			_ = syscall.Flock(int(slf.Fd()), syscall.LOCK_UN)
			_ = slf.Close()
		}
	}()
	// A root may only use an existing server stamp if its own already-durable
	// fingerprint agrees. This read-only preflight happens before migration,
	// keeping an empty precreated control.db and a losing root untouched.
	serverFP, fpErr := (tmux{socket: tmuxSocket}).globalOption("@cockpit_server_fingerprint")
	if fpErr != nil {
		return nil, fpErr
	}
	if _, dbErr := os.Stat(filepath.Join(clean, "control.db")); os.IsNotExist(dbErr) {
		if serverFP != "" {
			return nil, derr("CONTROLLER_NOT_READY", "tmux server is already controller-stamped")
		}
	} else if dbErr != nil {
		return nil, dbErr
	} else if serverFP != "" {
		rootFP, err := readExistingFingerprint(filepath.Join(clean, "control.db"))
		if err != nil || rootFP != serverFP {
			return nil, derr("CONTROLLER_NOT_READY", "tmux server is already controller-stamped")
		}
	}
	if fi, err := os.Lstat(socket); err == nil && fi.Mode()&os.ModeSocket == 0 {
		return nil, errors.New("socket path is not a socket")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	} else if err == nil {
		if c, dialErr := net.DialTimeout("unix", socket, 100*time.Millisecond); dialErr == nil {
			_ = c.Close()
			return nil, errors.New("socket path belongs to a live controller")
		}
	}
	lf, e := os.OpenFile(filepath.Join(clean, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lf.Close()
		return nil, fmt.Errorf("controller lease unavailable: %w", e)
	}
	s, e := openStore(clean)
	if e != nil {
		lf.Close()
		return nil, e
	}
	d := &daemon{root: clean, socket: socket, epoch: id("cpe_"), st: s, tm: t, lock: lf, serverLock: slf, paneLocks: map[string]*sync.Mutex{}, watchers: map[string]*watcher{}, subs: map[string]*subscription{}}
	if e = d.reconcile(); e != nil {
		leaseTransferred = true
		d.Close()
		return nil, e
	}
	leaseTransferred = true
	return d, nil
}
func readExistingFingerprint(path string) (string, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return "", err
	}
	defer db.Close()
	var value string
	err = db.QueryRow("SELECT v FROM meta WHERE k='fingerprint'").Scan(&value)
	if err == sql.ErrNoRows || (err != nil && strings.Contains(err.Error(), "no such table")) {
		return "", nil
	}
	return value, err
}
func (d *daemon) Close() {
	d.closeMu.Lock()
	defer d.closeMu.Unlock()
	d.lifeMu.Lock()
	d.closed = true
	l := d.listener
	d.listener = nil
	dev, ino := d.socketDev, d.socketIno
	d.lifeMu.Unlock()
	if l != nil {
		_ = l.Close()
	}
	if fi, err := os.Lstat(d.socket); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && uint64(st.Dev) == dev && uint64(st.Ino) == ino {
			_ = os.Remove(d.socket)
		}
	}
	if d.st != nil {
		_ = d.st.close()
		d.st = nil
	}
	if d.lock != nil {
		_ = syscall.Flock(int(d.lock.Fd()), syscall.LOCK_UN)
		_ = d.lock.Close()
		d.lock = nil
	}
	if d.serverLock != nil {
		_ = syscall.Flock(int(d.serverLock.Fd()), syscall.LOCK_UN)
		_ = d.serverLock.Close()
		d.serverLock = nil
	}
}
func (d *daemon) Serve() error {
	if fi, e := os.Lstat(d.socket); e == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return errors.New("socket path is not a socket")
		}
		// Never unlink a live foreign controller. A stale pathname fails a dial;
		// only then is it safe for the lease owner to replace it.
		c, dialErr := net.DialTimeout("unix", d.socket, 100*time.Millisecond)
		if dialErr == nil {
			_ = c.Close()
			return errors.New("socket path belongs to a live controller")
		}
		_ = os.Remove(d.socket)
	}
	l, e := net.Listen("unix", d.socket)
	if e != nil {
		return e
	}
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	_ = os.Chmod(d.socket, 0600)
	fi, e := os.Lstat(d.socket)
	if e != nil {
		_ = l.Close()
		return e
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		_ = l.Close()
		return errors.New("socket lacks inode identity")
	}
	d.lifeMu.Lock()
	if d.closed {
		d.lifeMu.Unlock()
		_ = l.Close()
		return net.ErrClosed
	}
	d.listener, d.socketDev, d.socketIno = l, uint64(st.Dev), uint64(st.Ino)
	d.lifeMu.Unlock()
	for {
		c, e := l.Accept()
		if e != nil {
			return e
		}
		go d.handle(c)
	}
}
func (d *daemon) reconcile() error {
	fp, e := d.st.meta("fingerprint")
	if e == sql.ErrNoRows {
		if existing, ge := d.tm.globalOption("@cockpit_server_fingerprint"); ge != nil || existing != "" {
			if ge != nil {
				return ge
			}
			return derr("CONTROLLER_NOT_READY", "tmux server fingerprint exists without durable owner")
		}
		fp = id("cpf_")
		if e = d.st.setMeta("fingerprint", fp); e != nil {
			return e
		}
		if e = d.tm.setGlobal("@cockpit_server_fingerprint", fp); e != nil {
			return e
		}
	} else if e != nil {
		return e
	} else if got, ge := d.tm.globalOption("@cockpit_server_fingerprint"); ge != nil || got != fp {
		return derr("CONTROLLER_NOT_READY", "tmux server fingerprint does not match durable controller")
	}
	b, e := d.tm.run("list-panes", "-a", "-F", "#{window_id}\t#{window_name}\t#{pane_id}\t#{@cockpit_workspace_ref}\t#{@cockpit_pane_ref}\t#{@cockpit_pane_generation}\t#{@cockpit_pane_version}\t#{@cockpit_badge}\t#{@cockpit_provider}\t#{@cockpit_state}")
	if e != nil {
		return e
	}
	// A window stamp is shared by every captured pane. Resolve every missing
	// workspace ref before writing any pane so the first multi-pane inventory
	// cannot create one logical workspace per pane.
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	windowRefs := map[string]string{}
	workspaceOwners := map[string]string{}
	paneOwners := map[string]string{}
	for _, line := range lines {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 10 {
			return fmt.Errorf("unexpected tmux inventory")
		}
		wid, pid, wref, pref := f[0], f[2], f[3], f[4]
		if prior, ok := windowRefs[wid]; ok && wref != "" && prior != wref {
			return derr("CONTROLLER_NOT_READY", "conflicting workspace stamps")
		}
		if wref != "" {
			windowRefs[wid] = wref
			if owner, ok := workspaceOwners[wref]; ok && owner != wid {
				return derr("CONTROLLER_NOT_READY", "workspaceRef appears on multiple windows")
			}
			workspaceOwners[wref] = wid
		}
		if pref != "" {
			var gen, ver int64
			if _, err := fmt.Sscan(f[5], &gen); err != nil || gen < 1 {
				return derr("CONTROLLER_NOT_READY", "pane identity has invalid generation")
			}
			if _, err := fmt.Sscan(f[6], &ver); err != nil || ver < 1 {
				return derr("CONTROLLER_NOT_READY", "pane identity has invalid version")
			}
			if owner, ok := paneOwners[pref]; ok && owner != pid {
				return derr("CONTROLLER_NOT_READY", "paneRef appears on multiple panes")
			}
			paneOwners[pref] = pid
		} else if f[5] != "" || f[6] != "" {
			return derr("CONTROLLER_NOT_READY", "partial pane identity stamp")
		}
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 10 {
			return fmt.Errorf("unexpected tmux inventory")
		}
		wid, name, pid, wref, pref := f[0], f[1], f[2], f[3], f[4]
		if pref != "" && wref == "" {
			return derr("CONTROLLER_NOT_READY", "pane identity is missing workspaceRef")
		}
		if wref == "" {
			wref = windowRefs[wid]
			if wref == "" {
				wref = id("cpw_")
				windowRefs[wid] = wref
				if e = d.tm.setWindow(wid, "@cockpit_workspace_ref", wref); e != nil {
					return e
				}
			}
		}
		if pref == "" {
			pref = id("cpp_")
			if e = d.tm.setPane(pid, "@cockpit_pane_ref", pref); e != nil {
				return e
			}
			if e = d.tm.setPane(pid, "@cockpit_pane_generation", "1"); e != nil {
				return e
			}
			if e = d.tm.setPane(pid, "@cockpit_pane_version", "1"); e != nil {
				return e
			}
		}
		_, e = d.st.db.Exec("INSERT INTO workspaces(ref,window_id,name,generation,version) VALUES(?,?,?,1,1) ON CONFLICT(ref) DO UPDATE SET window_id=excluded.window_id,name=excluded.name", wref, wid, name)
		if e != nil {
			return e
		}
		var gen, ver int64
		_, _ = fmt.Sscan(f[5], &gen)
		_, _ = fmt.Sscan(f[6], &ver)
		if gen < 1 {
			gen = 1
		}
		if ver < 1 {
			ver = 1
		}
		var dbGen, dbVer int64
		var dbPane string
		err := d.st.db.QueryRow("SELECT pane_id,generation,version FROM panes WHERE ref=?", pref).Scan(&dbPane, &dbGen, &dbVer)
		if err == nil && (dbGen != gen || dbVer != ver || dbPane != pid) {
			// The only permitted one-version divergence is a durable prepared
			// badge intent whose two tmux effects landed before process death.
			var n int
			pe := d.st.db.QueryRow("SELECT count(*) FROM operations WHERE status='prepared' AND pane_ref=? AND badge=?", pref, f[7]).Scan(&n)
			if dbPane != pid || dbGen != gen || dbVer+1 != ver || pe != nil || n != 1 {
				return derr("CONTROLLER_NOT_READY", "tmux pane stamp does not match durable projection")
			}
		}
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		_, e = d.st.db.Exec("INSERT INTO panes(ref,workspace_ref,window_id,pane_id,generation,version,badge) VALUES(?,?,?,?,?,?,?) ON CONFLICT(ref) DO UPDATE SET workspace_ref=excluded.workspace_ref,window_id=excluded.window_id,pane_id=excluded.pane_id", pref, wref, wid, pid, gen, ver, f[7])
		if e != nil {
			return e
		}
	}
	return d.recoverPrepared()
}
func (d *daemon) recoverPrepared() error {
	rows, e := d.st.db.Query("SELECT ref,caller,pane_ref,badge,target_version FROM operations WHERE status='prepared'")
	if e != nil {
		return e
	}
	type prepared struct {
		ref, caller, paneRef, badge string
		targetVersion               int64
	}
	var pending []prepared
	for rows.Next() {
		var ref, caller, pr, badge string
		var target int64
		if e = rows.Scan(&ref, &caller, &pr, &badge, &target); e != nil {
			return e
		}
		pending = append(pending, prepared{ref: ref, caller: caller, paneRef: pr, badge: badge, targetVersion: target})
	}
	if e = rows.Err(); e != nil {
		_ = rows.Close()
		return e
	}
	if e = rows.Close(); e != nil {
		return e
	}
	for _, item := range pending {
		ref, pr, badge := item.ref, item.paneRef, item.badge
		p, e := d.st.pane(pr)
		if e != nil {
			return e
		}
		got, e := d.tm.paneOption(p.PaneID, "@cockpit_badge")
		if e != nil {
			return e
		}
		stamp, se := d.tm.paneOption(p.PaneID, "@cockpit_pane_version")
		refStamp, re := d.tm.paneOption(p.PaneID, "@cockpit_pane_ref")
		genStamp, ge := d.tm.paneOption(p.PaneID, "@cockpit_pane_generation")
		if got != badge || se != nil || stamp != fmt.Sprint(item.targetVersion) || re != nil || ge != nil || refStamp != p.Ref || genStamp != fmt.Sprint(p.Generation) {
			e = d.markRecoveryRequired(ref, pr)
			if e != nil {
				return e
			}
			continue
		}
		v := item.targetVersion
		r := map[string]any{"operationRef": ref, "status": "completed", "paneRef": pr, "generation": p.Generation, "resourceVersion": v, "recovered": true}
		rb, _ := json.Marshal(r)
		tx, e := d.st.db.Begin()
		if e != nil {
			return e
		}
		if e = mustTx(tx, "UPDATE panes SET badge=?,version=? WHERE ref=?", badge, v, pr); e == nil {
			e = mustTx(tx, "UPDATE operations SET status='completed',result=? WHERE ref=?", string(rb), ref)
		}
		if e == nil {
			e = mustTx(tx, "INSERT INTO audit(at,caller,method,pane_ref,before_digest,after_digest) VALUES(?,?,?,?,?,?)", time.Now().Unix(), item.caller, "metadata.set_display", pr, digest(p.Badge), digest(badge))
		}
		if e != nil {
			_ = tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		d.publishEvent(pr, v, "operation.completed", ref)
	}
	return nil
}
func (d *daemon) markRecoveryRequired(operationRef, paneRef string) error {
	tx, err := d.st.db.Begin()
	if err != nil {
		return err
	}
	if err = mustTx(tx, "UPDATE operations SET status='recovery-required' WHERE ref=?", operationRef); err == nil {
		err = mustTx(tx, "UPDATE panes SET fenced=1 WHERE ref=?", paneRef)
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (d *daemon) handle(c net.Conn) {
	defer c.Close()
	writer := &frameWriter{w: c}
	owned := []string{}
	var ownedMu, pendingMu sync.Mutex
	pending := map[string]context.CancelFunc{}
	waits := 0
	defer func() {
		pendingMu.Lock()
		for _, cancel := range pending {
			cancel()
		}
		pendingMu.Unlock()
		ownedMu.Lock()
		for _, ref := range owned {
			d.removeSubscription(ref)
		}
		ownedMu.Unlock()
	}()
	profile, caller, ok := d.open(c, writer)
	if !ok {
		return
	}
	for {
		raw, err := readFrame(c)
		if err != nil {
			return
		}
		var req rpcRequest
		if !utf8.Valid(raw) {
			_ = writer.write(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		if !json.Valid(raw) {
			_ = writer.write(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		if err := strictJSON(raw, &req); err != nil {
			_ = writer.write(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32600, Message: "invalid request"}})
			continue
		}
		key, valid := requestIDFromRaw(req.ID)
		if !valid {
			_ = writer.write(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32600, Message: "invalid request"}})
			continue
		}
		if req.JSONRPC != "2.0" || req.Method == "" {
			_ = writer.write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "invalid request"}})
			continue
		}
		if req.Method == "rpc.cancel" {
			var p struct {
				RequestID any `json:"requestId"`
			}
			if strict(req.Params, &p) != nil {
				_ = writer.write(d.errorResponse(req.ID, rpcStandard(-32602, "invalid params")))
				continue
			}
			key, valid := requestID(p.RequestID)
			if !valid {
				_ = writer.write(d.errorResponse(req.ID, rpcStandard(-32602, "invalid params")))
				continue
			}
			pendingMu.Lock()
			cancel := pending[key]
			pendingMu.Unlock()
			if cancel != nil {
				cancel()
			}
			_ = writer.write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"cancelled": cancel != nil}})
			continue
		}
		isWait := req.Method == "wait.for_change"
		if isWait {
			pendingMu.Lock()
			if _, exists := pending[key]; exists {
				pendingMu.Unlock()
				_ = writer.write(d.errorResponse(req.ID, rpcStandard(-32600, "invalid request")))
				continue
			}
			if waits >= 64 {
				pendingMu.Unlock()
				_ = writer.write(d.errorResponse(req.ID, derr("CAPABILITY_ABSENT", "per-connection watcher limit reached")))
				continue
			}
			waits++
			pendingMu.Unlock()
		}
		ctx, cancel := context.WithCancel(context.Background())
		pendingMu.Lock()
		if _, exists := pending[key]; exists {
			pendingMu.Unlock()
			cancel()
			_ = writer.write(d.errorResponse(req.ID, rpcStandard(-32600, "invalid request")))
			continue
		}
		if len(pending) >= 64 {
			pendingMu.Unlock()
			cancel()
			_ = writer.write(d.errorResponse(req.ID, derr("CAPABILITY_ABSENT", "outstanding request limit reached")))
			continue
		}
		pending[key] = cancel
		pendingMu.Unlock()
		go func(req rpcRequest, key string, isWait bool) {
			defer func() {
				pendingMu.Lock()
				delete(pending, key)
				if isWait {
					waits--
				}
				pendingMu.Unlock()
				cancel()
			}()
			result, err := d.dispatch(ctx, profile, caller, req)
			if err != nil {
				_ = writer.write(d.errorResponse(req.ID, err))
				return
			}
			if req.Method == "events.subscribe" {
				if m, ok := result.(map[string]any); ok {
					if ref, ok := m["subscriptionRef"].(string); ok {
						// Ownership is recorded before the response is visible. A peer
						// that closes immediately after receiving it cannot leak a sub.
						d.attachSubscription(ref, writer)
						ownedMu.Lock()
						owned = append(owned, ref)
						ownedMu.Unlock()
					}
				}
			}
			if req.Method == "events.unsubscribe" {
				var p struct {
					SubscriptionRef string `json:"subscriptionRef"`
				}
				if strict(req.Params, &p) == nil {
					ownedMu.Lock()
					allowed := false
					for i, ref := range owned {
						if ref == p.SubscriptionRef {
							owned = append(owned[:i], owned[i+1:]...)
							allowed = true
							break
						}
					}
					ownedMu.Unlock()
					if !allowed {
						_ = writer.write(d.errorResponse(req.ID, derr("PERMISSION_DENIED", "subscription belongs to another connection")))
						return
					}
					d.removeSubscription(p.SubscriptionRef)
				}
			}
			_ = writer.write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
			if req.Method == "events.subscribe" {
				if m, ok := result.(map[string]any); ok {
					if ref, ok := m["subscriptionRef"].(string); ok {
						d.startSubscription(ref)
					}
				}
			}
		}(req, key, isWait)
	}
}
func (d *daemon) open(r io.Reader, w *frameWriter) (string, string, bool) {
	raw, e := readFrame(r)
	if e != nil {
		return "", "", false
	}
	if !utf8.Valid(raw) || !json.Valid(raw) {
		_ = w.write(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
		return "", "", false
	}
	var req rpcRequest
	if strictJSON(raw, &req) != nil || req.JSONRPC != "2.0" || req.Method != "session.open" {
		_ = w.write(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32600, Message: "invalid request"}})
		return "", "", false
	}
	if _, ok := requestIDFromRaw(req.ID); !ok {
		_ = w.write(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32600, Message: "invalid request"}})
		return "", "", false
	}
	var p sessionParams
	if strict(req.Params, &p) != nil || p.Protocol == "" || p.ClientID == "" || utf8.RuneCountInString(p.ClientID) > 128 || p.ClaimedProfile == "" || !validClaimedProfile(p.ClaimedProfile) || p.Credential == "" || utf8.RuneCountInString(p.Credential) > 512 {
		_ = w.write(d.errorResponse(req.ID, rpcStandard(-32602, "invalid params")))
		return "", "", false
	}
	if p.Protocol != Protocol {
		_ = w.write(d.errorResponse(req.ID, derr("UNSUPPORTED_PROTOCOL", "protocol 1.0 required")))
		return "", "", false
	}
	profile := ""
	switch p.Credential {
	case "test-local":
		profile = "local-operator"
	case "test-read":
		profile = "read-only"
	}
	if profile == "" {
		_ = w.write(d.errorResponse(req.ID, derr("UNAUTHENTICATED", "invalid credential")))
		return "", "", false
	}
	caller := "test-local-operator"
	if profile == "read-only" {
		caller = "test-read-only"
	}
	_ = w.write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"protocol": Protocol, "controllerEpoch": d.epoch, "clientId": caller, "profile": profile, "ready": true, "capabilities": caps(profile)}})
	return profile, caller, true
}
func validClaimedProfile(profile string) bool {
	switch profile {
	case "local-operator", "read-only", "tmux-binding", "mcp-local", "web-gateway", "orbital", "hook-producer":
		return true
	}
	return false
}
func caps(profile string) []string {
	if profile == "local-operator" {
		return []string{"state:read", "operations:read", "events:wait", "capture:sanitized", "metadata:write", "interaction:nudge", "interaction:pause", "interaction:compact", "interaction:resume"}
	}
	return []string{"state:read", "operations:read", "events:wait"}
}
func requestID(v any) (string, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	var raw json.RawMessage = b
	return requestIDFromRaw(raw)
}
func requestIDFromRaw(raw json.RawMessage) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		if x != "" {
			return "s:" + x, true
		}
	case json.Number:
		i, e := x.Int64()
		if e == nil && i >= -9007199254740991 && i <= 9007199254740991 {
			return fmt.Sprintf("n:%d", i), true
		}
	}
	return "", false
}

func (d *daemon) dispatch(ctx context.Context, profile, caller string, r rpcRequest) (any, error) {
	switch r.Method {
	case "controller.health":
		var p struct{}
		if e := strict(r.Params, &p); e != nil {
			return nil, rpcStandard(-32602, "invalid params")
		}
		return map[string]any{"ready": true, "controllerEpoch": d.epoch, "schemaVersion": 1, "watcherCount": d.watcherCount(), "subscriptionCount": d.subscriptionCount(), "eventSeq": d.currentEventSeq()}, nil
	case "capabilities.get":
		var p struct{}
		if e := strict(r.Params, &p); e != nil {
			return nil, rpcStandard(-32602, "invalid params")
		}
		return map[string]any{"capabilities": caps(profile)}, nil
	case "state.snapshot":
		var p struct{}
		if e := strict(r.Params, &p); e != nil {
			return nil, rpcStandard(-32602, "invalid params")
		}
		ps, e := d.st.panes()
		if e != nil {
			return nil, e
		}
		out := make([]any, 0, len(ps))
		for _, p := range ps {
			out = append(out, p.view())
		}
		return map[string]any{"controllerEpoch": d.epoch, "eventSeq": d.currentEventSeq(), "panes": out}, nil
	case "pane.inspect":
		var p struct {
			PaneRef string `json:"paneRef"`
		}
		if e := strict(r.Params, &p); e != nil {
			return nil, rpcStandard(-32602, "invalid params")
		}
		if p.PaneRef == "" {
			return nil, rpcStandard(-32602, "invalid params")
		}
		x, e := d.st.pane(p.PaneRef)
		if e == sql.ErrNoRows {
			return nil, derr("TARGET_NOT_FOUND", "pane not found")
		}
		if e != nil {
			return nil, e
		}
		return x.view(), nil
	case "pane.resolve":
		var p struct {
			Canonical string `json:"canonical"`
		}
		if e := strict(r.Params, &p); e != nil || !regexp.MustCompile(`^[A-Za-z0-9_.-]+:[0-9]+\.[0-9]+$`).MatchString(p.Canonical) {
			return nil, rpcStandard(-32602, "invalid params")
		}
		return d.resolveCanonical(p.Canonical)
	case "pane.capture":
		var p struct {
			PaneRef string `json:"paneRef"`
			Lines   int    `json:"lines"`
		}
		if e := strict(r.Params, &p); e != nil || p.PaneRef == "" || p.Lines < 1 || p.Lines > 200 {
			return nil, rpcStandard(-32602, "invalid params")
		}
		if !has(caps(profile), "capture:sanitized") {
			return nil, derr("PERMISSION_DENIED", "capture capability absent")
		}
		return d.capture(ctx, p.PaneRef, p.Lines)
	case "operation.get":
		var p struct {
			OperationRef string `json:"operationRef"`
		}
		if e := strict(r.Params, &p); e != nil {
			return nil, rpcStandard(-32602, "invalid params")
		}
		if p.OperationRef == "" {
			return nil, rpcStandard(-32602, "invalid params")
		}
		x, e := d.st.operation(p.OperationRef)
		if e == sql.ErrNoRows {
			return nil, derr("TARGET_NOT_FOUND", "operation not found")
		}
		return x, e
	case "metadata.set_display":
		if profile != "local-operator" {
			return nil, derr("PERMISSION_DENIED", "metadata capability absent")
		}
		var p badgeParams
		if e := strict(r.Params, &p); e != nil || p.Protocol == "" || p.PaneRef == "" || p.IdempotencyKey == "" || p.Deadline == "" {
			return nil, rpcStandard(-32602, "invalid params")
		}
		return d.setBadge(caller, p)
	case "interaction.nudge", "interaction.pause", "interaction.compact", "interaction.resume":
		spec, _ := specForMethod(r.Method)
		if !has(caps(profile), spec.Capability) {
			return nil, derr("PERMISSION_DENIED", "interaction capability absent")
		}
		var p interactionParams
		if e := strict(r.Params, &p); e != nil || p.Protocol == "" || p.PaneRef == "" || p.IdempotencyKey == "" || p.Deadline == "" {
			return nil, rpcStandard(-32602, "invalid params")
		}
		return d.interact(caller, r.Method, p)
	case "events.subscribe":
		if !has(caps(profile), "events:wait") {
			return nil, derr("PERMISSION_DENIED", "events capability absent")
		}
		var p struct {
			ControllerEpoch string `json:"controllerEpoch"`
			AfterEventSeq   uint64 `json:"afterEventSeq"`
			PaneRef         string `json:"paneRef"`
			OperationRef    string `json:"operationRef"`
		}
		if e := strict(r.Params, &p); e != nil {
			return nil, rpcStandard(-32602, "invalid params")
		}
		if p.ControllerEpoch != "" && p.ControllerEpoch != d.epoch {
			return d.resync(), nil
		}
		return d.newSubscription(p.AfterEventSeq, p.PaneRef, p.OperationRef)
	case "events.unsubscribe":
		var p struct {
			SubscriptionRef string `json:"subscriptionRef"`
		}
		if e := strict(r.Params, &p); e != nil || p.SubscriptionRef == "" {
			return nil, rpcStandard(-32602, "invalid params")
		}
		return map[string]any{"released": true}, nil
	case "wait.for_change":
		if !has(caps(profile), "events:wait") {
			return nil, derr("PERMISSION_DENIED", "events capability absent")
		}
		var p struct {
			PaneRef      string `json:"paneRef"`
			OperationRef string `json:"operationRef"`
			AfterVersion int64  `json:"afterVersion"`
			Deadline     string `json:"deadline"`
		}
		if e := strict(r.Params, &p); e != nil {
			return nil, rpcStandard(-32602, "invalid params")
		}
		if (p.PaneRef == "") == (p.OperationRef == "") || p.Deadline == "" {
			return nil, rpcStandard(-32602, "invalid params")
		}
		return d.wait(ctx, p)
	default:
		return nil, rpcStandard(-32601, "method not found")
	}
}
func (p pane) view() map[string]any {
	if p.Provider == "" {
		p.Provider = "unknown"
	}
	if p.State == "" {
		p.State = "waiting"
	}
	lifecycle, capabilities := "active", []string{"metadata:write"}
	if p.Fenced {
		lifecycle, capabilities = "recovery-required", []string{}
	}
	if p.Provider == "claude" || p.Provider == "codex" {
		if p.State == "waiting" {
			capabilities = append(capabilities, "interaction:nudge", "interaction:compact")
		}
		if p.State == "working" {
			capabilities = append(capabilities, "interaction:pause")
		}
		if p.State == "paused" {
			capabilities = append(capabilities, "interaction:resume")
		}
	}
	return map[string]any{"paneRef": p.Ref, "workspaceRef": p.WorkspaceRef, "generation": p.Generation, "resourceVersion": p.Version, "lifecycle": lifecycle, "provider": p.Provider, "observedState": p.State, "locator": map[string]any{"paneId": p.PaneID, "windowId": p.WindowID}, "display": map[string]any{"badge": p.Badge}, "capabilities": capabilities}
}

// Provider and observed state are deliberately not client writable and are not
// persisted from terminal output in this slice. They are controller-read tmux
// stamps; unavailable/invalid values fail closed as unsupported.
func (d *daemon) observePane(p pane) pane {
	provider, pe := d.tm.paneOption(p.PaneID, "@cockpit_provider")
	state, se := d.tm.paneOption(p.PaneID, "@cockpit_state")
	if pe == nil && (provider == "claude" || provider == "codex") {
		p.Provider = provider
	} else {
		p.Provider = "unknown"
	}
	if se == nil && (state == "waiting" || state == "working" || state == "paused") {
		p.State = state
	} else {
		p.State = "waiting"
	}
	return p
}

func (d *daemon) resolveCanonical(canonical string) (any, error) {
	b, err := d.tm.run("list-panes", "-a", "-F", "#{session_name}:#{window_index}.#{pane_index}\t#{@cockpit_pane_ref}")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 2 && f[0] == canonical && f[1] != "" {
			return d.dispatch(context.Background(), "local-operator", "resolver", rpcRequest{Method: "pane.inspect", Params: json.RawMessage(fmt.Sprintf(`{"paneRef":%q}`, f[1]))})
		}
	}
	return nil, derr("TARGET_NOT_FOUND", "canonical pane locator not found")
}
func has(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func strict(raw json.RawMessage, dst any) error {
	return strictJSON(raw, dst)
}
func (d *daemon) errorResponse(id json.RawMessage, e error) rpcResponse {
	se := &standardRPCError{}
	if errors.As(e, &se) {
		return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: se.code, Message: se.message}}
	}
	de := &domainError{}
	if errors.As(e, &de) {
		return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32001, Message: de.Code, Data: map[string]string{"code": de.Code, "message": de.Message}}}
	}
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32001, Message: "INTERNAL", Data: map[string]string{"code": "INTERNAL"}}}
}
func (d *daemon) lockFor(ref string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.paneLocks[ref]
	if m == nil {
		m = &sync.Mutex{}
		d.paneLocks[ref] = m
	}
	return m
}
func (d *daemon) setBadge(caller string, p badgeParams) (any, error) {
	if testMetadataDisabled(d) {
		return nil, derr("CAPABILITY_ABSENT", "metadata capability removed by policy")
	}
	if p.Protocol != Protocol {
		return nil, derr("UNSUPPORTED_PROTOCOL", "protocol 1.0 required")
	}
	if e := validateBadge(p.Badge); e != nil {
		return nil, e
	}
	if e := validateIdempotency(p.IdempotencyKey, time.Now()); e != nil {
		return nil, e
	}
	if len(p.Expectations) != 1 || p.Expectations[0].Kind != "pane" || p.Expectations[0].PaneRef != p.PaneRef {
		return nil, derr("INVALID_REQUEST", "one exact pane expectation is required")
	}
	deadline, e := time.Parse(time.RFC3339, p.Deadline)
	if e != nil || !deadline.After(time.Now()) {
		return nil, derr("DEADLINE_EXCEEDED", "deadline expired")
	}
	m := d.lockFor(p.PaneRef)
	m.Lock()
	defer m.Unlock()
	testBarrier(d, "badge_at_queue_head")
	return d.setBadgeLocked(caller, p, deadline)
}
func (d *daemon) setBadgeLocked(caller string, p badgeParams, deadline time.Time) (any, error) {
	in := intent(p)
	var ref, oldIntent, status, result string
	e := d.st.db.QueryRow("SELECT ref,intent,status,result FROM operations WHERE caller=? AND method='metadata.set_display' AND idem_key=?", caller, p.IdempotencyKey).Scan(&ref, &oldIntent, &status, &result)
	if e == nil {
		if oldIntent != in {
			return nil, derr("IDEMPOTENCY_CONFLICT", "idempotency key has a different intent")
		}
		var v any
		_ = json.Unmarshal([]byte(result), &v)
		if v == nil {
			v = map[string]any{"operationRef": ref, "status": status}
		}
		return map[string]any{"replayed": true, "operation": v}, nil
	}
	if e != sql.ErrNoRows {
		return nil, e
	}
	if time.Now().After(deadline) {
		return nil, derr("DEADLINE_EXCEEDED", "deadline expired")
	}
	x, e := d.st.pane(p.PaneRef)
	if e == sql.ErrNoRows {
		return nil, derr("TARGET_NOT_FOUND", "pane not found")
	}
	if e != nil {
		return nil, e
	}
	if x.Fenced {
		return nil, derr("CONTROLLER_NOT_READY", "pane is fenced pending recovery")
	}
	ex := p.Expectations[0]
	if ex.Generation != x.Generation {
		return nil, derr("CONFLICT_GENERATION", "pane generation changed")
	}
	if ex.ResourceVersion != x.Version {
		return nil, derr("CONFLICT_VERSION", "pane version changed")
	}
	if ex.Material.Lifecycle != "active" {
		return nil, derr("CONFLICT_MATERIAL_STATE", "pane lifecycle changed")
	}
	// Pane ids are reusable. Re-resolve the stable stamps at queue head before
	// issuing the effect, including the durable version expected by this CAS.
	refStamp, re := d.tm.paneOption(x.PaneID, "@cockpit_pane_ref")
	genStamp, ge := d.tm.paneOption(x.PaneID, "@cockpit_pane_generation")
	verStamp, ve := d.tm.paneOption(x.PaneID, "@cockpit_pane_version")
	if re != nil || ge != nil || ve != nil || refStamp != x.Ref || genStamp != fmt.Sprint(x.Generation) || verStamp != fmt.Sprint(x.Version) {
		return nil, derr("CONFLICT_GENERATION", "pane locator or stable stamp changed")
	}
	ref = id("cpo_")
	tx, e := d.st.db.Begin()
	if e != nil {
		return nil, e
	}
	if e = mustTx(tx, "INSERT INTO operations(ref,caller,method,idem_key,intent,status,pane_ref,badge,target_version,result,created_at) VALUES(?,?,?, ?,?,'prepared',?,?,?,?,?)", ref, caller, "metadata.set_display", p.IdempotencyKey, in, p.PaneRef, p.Badge, x.Version+1, "null", time.Now().Unix()); e != nil {
		_ = tx.Rollback()
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	testBarrier(d, "after_prepared_commit")
	if testMetadataDisabled(d) {
		if e = d.markFailed(ref); e != nil {
			return nil, e
		}
		d.publishEvent(x.Ref, x.Version, "operation.failed", ref)
		return nil, derr("CAPABILITY_ABSENT", "metadata capability removed by policy")
	}
	if testDriverAmbiguous(d) {
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, fmt.Errorf("tmux driver outcome ambiguous"))
	}
	if e = d.tm.setPaneBadgeVersion(x.PaneID, p.Badge, x.Version+1); e != nil {
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, e)
	}
	testBarrier(d, "after_tmux_effect")
	if testCrashAfterEffect() {
		os.Exit(86)
	}
	if testReadbackAmbiguous(d) {
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, fmt.Errorf("badge readback outcome ambiguous"))
	}
	got, e := d.tm.paneOption(x.PaneID, "@cockpit_badge")
	if e != nil {
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, e)
	}
	versionStamp, se := d.tm.paneOption(x.PaneID, "@cockpit_pane_version")
	if got != p.Badge || se != nil || versionStamp != fmt.Sprint(x.Version+1) {
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, derr("INTERNAL", "badge confirmation failed"))
	}
	v := x.Version + 1
	op := map[string]any{"operationRef": ref, "status": "completed", "paneRef": x.Ref, "generation": x.Generation, "resourceVersion": v}
	rb, _ := json.Marshal(op)
	tx, e = d.st.db.Begin()
	if e != nil {
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, e)
	}
	if e = mustTx(tx, "UPDATE panes SET badge=?,version=? WHERE ref=?", p.Badge, v, x.Ref); e == nil {
		e = mustTx(tx, "UPDATE operations SET status='completed',result=? WHERE ref=?", string(rb), ref)
	}
	if e == nil {
		e = mustTx(tx, "INSERT INTO audit(at,caller,method,pane_ref,before_digest,after_digest) VALUES(?,?,?,?,?,?)", time.Now().Unix(), caller, "metadata.set_display", x.Ref, digest(x.Badge), digest(p.Badge))
	}
	if e != nil {
		_ = tx.Rollback()
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, e)
	}
	if e = tx.Commit(); e != nil {
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, e)
	}
	d.publishEvent(x.Ref, v, "operation.completed", ref)
	return op, nil
}

func (d *daemon) capture(ctx context.Context, ref string, lines int) (any, error) {
	p, err := d.st.pane(ref)
	if err == sql.ErrNoRows {
		return nil, derr("TARGET_NOT_FOUND", "pane not found")
	}
	if err != nil {
		return nil, err
	}
	if p.Fenced {
		return nil, derr("CONTROLLER_NOT_READY", "pane is fenced pending recovery")
	}
	// Recheck stable identity immediately before the privacy-sensitive driver
	// read. A reused tmux pane id must never disclose another pane's output.
	stamp, err := d.tm.paneOption(p.PaneID, "@cockpit_pane_ref")
	if err != nil || stamp != p.Ref {
		return nil, derr("CONFLICT_GENERATION", "pane locator or stable stamp changed")
	}
	b, err := d.tm.capturePane(ctx, p.PaneID, lines)
	if err != nil {
		return nil, err
	}
	text, truncated := sanitizeCapture(b, 64*1024)
	return map[string]any{"paneRef": p.Ref, "generation": p.Generation, "resourceVersion": p.Version, "lines": lines, "text": text, "redacted": true, "truncated": truncated, "private": true}, nil
}

func sanitizeCapture(b []byte, max int) (string, bool) {
	// Strip terminal controls first. Redaction is intentionally conservative:
	// common bearer/token assignments are replaced, and captures remain marked
	// private/untrusted rather than being treated as secret-free.
	s := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r >= 0x20 && r != 0x7f {
			return r
		}
		return -1
	}, string(b))
	secret := regexp.MustCompile(`(?i)(api[_-]?key|token|password|secret)\s*([=:])\s*[^\s]+`)
	s = secret.ReplaceAllString(s, "$1$2[REDACTED]")
	truncated := len(s) > max
	if truncated {
		s = s[:max]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s, truncated
}

func (d *daemon) interact(caller, method string, p interactionParams) (any, error) {
	if p.Protocol != Protocol {
		return nil, derr("UNSUPPORTED_PROTOCOL", "protocol 1.0 required")
	}
	if err := validateIdempotency(p.IdempotencyKey, time.Now()); err != nil {
		return nil, err
	}
	dl, err := time.Parse(time.RFC3339, p.Deadline)
	if err != nil || !dl.After(time.Now()) || time.Until(dl) > 30*time.Minute {
		return nil, derr("DEADLINE_EXCEEDED", "deadline expired")
	}
	if len(p.Expectations) != 1 || p.Expectations[0].Kind != "pane" || p.Expectations[0].PaneRef != p.PaneRef {
		return nil, derr("INVALID_REQUEST", "one exact pane expectation is required")
	}
	action := strings.TrimPrefix(method, "interaction.")
	if (action == "nudge" || action == "resume") && (len(p.Text) < 1 || len(p.Text) > 16384 || !utf8.ValidString(p.Text) || strings.ContainsAny(p.Text, "\x00\x1b\r\n")) {
		return nil, derr("INVALID_REQUEST", "interaction text is not bounded literal text")
	}
	if (action == "pause" || action == "compact") && p.Text != "" {
		return nil, derr("INVALID_REQUEST", "this interaction does not accept text")
	}
	m := d.lockFor(p.PaneRef)
	m.Lock()
	defer m.Unlock()
	if time.Now().After(dl) {
		return nil, derr("DEADLINE_EXCEEDED", "deadline expired")
	}
	x, err := d.st.pane(p.PaneRef)
	if err == sql.ErrNoRows {
		return nil, derr("TARGET_NOT_FOUND", "pane not found")
	}
	if err != nil {
		return nil, err
	}
	x = d.observePane(x)
	ex := p.Expectations[0]
	if x.Fenced {
		return nil, derr("CONTROLLER_NOT_READY", "pane is fenced pending recovery")
	}
	if ex.Generation != x.Generation {
		return nil, derr("CONFLICT_GENERATION", "pane generation changed")
	}
	if ex.ResourceVersion != x.Version {
		return nil, derr("CONFLICT_VERSION", "pane version changed")
	}
	if ex.Material.Lifecycle != "active" {
		return nil, derr("CONFLICT_MATERIAL_STATE", "pane lifecycle changed")
	}
	if ex.Material.ObservedState != x.State {
		return nil, derr("CONFLICT_MATERIAL_STATE", "pane observed state changed")
	}
	if x.Provider != "claude" && x.Provider != "codex" {
		return nil, derr("CAPABILITY_ABSENT", "unsupported provider")
	}
	want := map[string]string{"nudge": "waiting", "compact": "waiting", "pause": "working", "resume": "paused"}[action]
	if x.State != want {
		return nil, derr("CONFLICT_MATERIAL_STATE", "pane is not in the required observed state")
	}
	if !has(x.view()["capabilities"].([]string), "interaction:"+action) {
		return nil, derr("CAPABILITY_ABSENT", "interaction capability absent")
	}
	refStamp, re := d.tm.paneOption(x.PaneID, "@cockpit_pane_ref")
	genStamp, ge := d.tm.paneOption(x.PaneID, "@cockpit_pane_generation")
	verStamp, ve := d.tm.paneOption(x.PaneID, "@cockpit_pane_version")
	if re != nil || ge != nil || ve != nil || refStamp != x.Ref || genStamp != fmt.Sprint(x.Generation) || verStamp != fmt.Sprint(x.Version) {
		return nil, derr("CONFLICT_GENERATION", "pane locator or stable stamp changed")
	}
	digestText := digest(p.Text)
	intent := fmt.Sprintf("%s:%s:%d:%d:%s", action, p.PaneRef, ex.Generation, ex.ResourceVersion, digestText)
	var oldRef, oldIntent, status, result string
	err = d.st.db.QueryRow("SELECT ref,intent,status,result FROM operations WHERE caller=? AND method=? AND idem_key=?", caller, method, p.IdempotencyKey).Scan(&oldRef, &oldIntent, &status, &result)
	if err == nil {
		if oldIntent != intent {
			return nil, derr("IDEMPOTENCY_CONFLICT", "idempotency key has a different intent")
		}
		var prior any
		_ = json.Unmarshal([]byte(result), &prior)
		if prior == nil {
			prior = map[string]any{"operationRef": oldRef, "status": status}
		}
		return map[string]any{"replayed": true, "operation": prior}, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	ref := id("cpo_")
	if _, err = d.st.db.Exec("INSERT INTO operations(ref,caller,method,idem_key,intent,status,pane_ref,badge,target_version,result,created_at) VALUES(?,?,?,?,?,'prepared',?,'',?,'null',?)", ref, caller, method, p.IdempotencyKey, intent, x.Ref, x.Version, time.Now().Unix()); err != nil {
		return nil, err
	}
	if err = d.tm.interact(x.PaneID, action, p.Text); err != nil {
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, err)
	}
	if err = d.tm.setPaneVersion(x.PaneID, x.Version+1); err != nil {
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, err)
	}
	// Delivery is not completion.  In this accelerated slice there is no
	// provider observer yet, so pause/compact cannot claim success merely from
	// elapsed time. The durable outcome is explicitly unconfirmed and callers
	// use wait/status after an independent provider observation.
	v := x.Version + 1
	op := map[string]any{"operationRef": ref, "status": "effect-delivered-unconfirmed", "paneRef": x.Ref, "generation": x.Generation, "resourceVersion": v, "provider": x.Provider}
	rb, _ := json.Marshal(op)
	tx, err := d.st.db.Begin()
	if err != nil {
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, err)
	}
	if err = mustTx(tx, "UPDATE panes SET version=? WHERE ref=?", v, x.Ref); err == nil {
		err = mustTx(tx, "UPDATE operations SET status='effect-delivered-unconfirmed',result=? WHERE ref=?", string(rb), ref)
	}
	if err == nil {
		err = mustTx(tx, "INSERT INTO audit(at,caller,method,pane_ref,before_digest,after_digest) VALUES(?,?,?,?,?,?)", time.Now().Unix(), caller, method, x.Ref, digestText, fmt.Sprintf("bytes:%d", len(p.Text)))
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, err)
	}
	if err = tx.Commit(); err != nil {
		return nil, d.recoveryAfterEffect(ref, x.Ref, x.Version, err)
	}
	d.publishEvent(x.Ref, v, "operation.effect-delivered-unconfirmed", ref)
	return op, nil
}
func (d *daemon) markFailed(operationRef string) error {
	tx, err := d.st.db.Begin()
	if err != nil {
		return err
	}
	if err = mustTx(tx, "UPDATE operations SET status='failed' WHERE ref=?", operationRef); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// recoveryAfterEffect emits the terminal notification only after the durable
// operation transition and pane fence commit together.
func (d *daemon) recoveryAfterEffect(operationRef, paneRef string, version int64, cause error) error {
	if err := d.markRecoveryRequired(operationRef, paneRef); err != nil {
		return fmt.Errorf("%w; recovery-required transition failed: %v", cause, err)
	}
	d.publishEvent(paneRef, version, "operation.recovery-required", operationRef)
	return cause
}
func digest(x string) string { sum := sha256.Sum256([]byte(x)); return hex.EncodeToString(sum[:8]) }
func (d *daemon) publish(ref string, v int64, kind string) {
	d.publishEvent(ref, v, kind, "")
}
func (d *daemon) publishEvent(ref string, v int64, kind, operationRef string) {
	d.watchMu.Lock()
	defer d.watchMu.Unlock()
	d.eventSeq++
	seq := d.eventSeq
	e := eventSummary{Seq: seq, Kind: kind, PaneRef: ref, OperationRef: operationRef, Version: v, At: time.Now()}
	d.eventRing = append(d.eventRing, e)
	cutoff := time.Now().Add(-24 * time.Hour)
	for len(d.eventRing) > 0 && d.eventRing[0].At.Before(cutoff) {
		d.eventRing = d.eventRing[1:]
	}
	if len(d.eventRing) > 10000 {
		d.eventRing = d.eventRing[len(d.eventRing)-10000:]
	}
	for key, w := range d.watchers {
		if (w.paneRef == ref && v > w.after) || (w.operationRef != "" && w.operationRef == operationRef && strings.HasPrefix(kind, "operation.")) {
			select {
			case w.ch <- map[string]any{"event": kind, "paneRef": ref, "operationRef": operationRef, "resourceVersion": v}:
			default:
			}
			delete(d.watchers, key)
			close(w.done)
		}
	}
	d.subMu.Lock()
	defer d.subMu.Unlock()
	for _, sub := range d.subs {
		if (sub.paneRef == "" || sub.paneRef == ref) && (sub.operationRef == "" || sub.operationRef == e.OperationRef) {
			d.enqueueSubscription(sub, e, seq)
		}
	}
}
func (d *daemon) currentEventSeq() uint64 {
	d.watchMu.Lock()
	defer d.watchMu.Unlock()
	return d.eventSeq
}
func (d *daemon) resync() map[string]any {
	d.watchMu.Lock()
	seq := d.eventSeq
	d.watchMu.Unlock()
	return d.resyncAt(seq)
}
func (d *daemon) resyncAt(seq uint64) map[string]any {
	return map[string]any{"resyncRequired": true, "controllerEpoch": d.epoch, "eventSeq": seq}
}
func (d *daemon) newSubscription(after uint64, paneRef, operationRef string) (any, error) {
	d.watchMu.Lock()
	defer d.watchMu.Unlock()
	cutoff := time.Now().Add(-24 * time.Hour)
	for len(d.eventRing) > 0 && d.eventRing[0].At.Before(cutoff) {
		d.eventRing = d.eventRing[1:]
	}
	seq := d.eventSeq
	if after > seq || (len(d.eventRing) == 0 && after < seq) || (len(d.eventRing) > 0 && after+1 < d.eventRing[0].Seq) {
		return d.resyncAt(seq), nil
	}
	replay := make([]eventSummary, 0)
	for _, e := range d.eventRing {
		if e.Seq > after && (paneRef == "" || e.PaneRef == paneRef) && (operationRef == "" || e.OperationRef == operationRef) {
			replay = append(replay, e)
		}
	}
	testBarrier(d, "subscribe_after_snapshot_before_register")
	ref := id("cps_")
	// Lock order is watchMu -> subMu. Publish uses the same order, making
	// cursor snapshot, replay and registration one atomic bus transition.
	d.subMu.Lock()
	if len(d.subs) >= 256 {
		d.subMu.Unlock()
		return nil, derr("CAPABILITY_ABSENT", "global subscription limit reached")
	}
	s := &subscription{ch: make(chan map[string]any, 64), paneRef: paneRef, operationRef: operationRef}
	d.subs[ref] = s
	for _, e := range replay {
		d.enqueueSubscription(s, e, seq)
	}
	d.subMu.Unlock()
	return map[string]any{"subscriptionRef": ref, "controllerEpoch": d.epoch, "eventSeq": seq}, nil
}
func (d *daemon) enqueueSubscription(sub *subscription, e eventSummary, currentSeq uint64) {
	v := map[string]any{"jsonrpc": "2.0", "method": "controller.event", "params": map[string]any{"event": e.Kind, "paneRef": e.PaneRef, "operationRef": e.OperationRef, "resourceVersion": e.Version, "eventSeq": e.Seq}}
	select {
	case sub.ch <- v:
		return
	default:
	}
	// The client fell behind. Replace a queued summary with an explicit resync
	// notification; loss is never represented as a quiet timeout.
	select {
	case <-sub.ch:
	default:
	}
	select {
	case sub.ch <- map[string]any{"jsonrpc": "2.0", "method": "controller.event", "params": d.resyncAt(currentSeq)}:
	default:
	}
}
func (d *daemon) attachSubscription(ref string, writer *frameWriter) {
	d.subMu.Lock()
	sub := d.subs[ref]
	if sub != nil {
		sub.writer = writer
	}
	d.subMu.Unlock()
}
func (d *daemon) startSubscription(ref string) {
	d.subMu.Lock()
	sub := d.subs[ref]
	if sub == nil || sub.writer == nil {
		d.subMu.Unlock()
		return
	}
	w := sub.writer
	d.subMu.Unlock()
	go func() {
		for event := range sub.ch {
			_ = w.write(event)
		}
	}()
}
func (d *daemon) removeSubscription(ref string) {
	d.subMu.Lock()
	sub := d.subs[ref]
	delete(d.subs, ref)
	d.subMu.Unlock()
	if sub != nil {
		close(sub.ch)
	}
}
func (d *daemon) watcherCount() int {
	d.watchMu.Lock()
	defer d.watchMu.Unlock()
	return len(d.watchers)
}
func (d *daemon) subscriptionCount() int {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	return len(d.subs)
}
func (d *daemon) wait(ctx context.Context, p struct {
	PaneRef      string `json:"paneRef"`
	OperationRef string `json:"operationRef"`
	AfterVersion int64  `json:"afterVersion"`
	Deadline     string `json:"deadline"`
}) (any, error) {
	dl, e := time.Parse(time.RFC3339, p.Deadline)
	if e != nil || time.Until(dl) > 30*time.Minute {
		return nil, derr("INVALID_REQUEST", "invalid wait deadline")
	}
	w := &watcher{paneRef: p.PaneRef, operationRef: p.OperationRef, after: p.AfterVersion, ch: make(chan map[string]any, 1), done: make(chan struct{})}
	key := id("cpwait_")
	d.watchMu.Lock()
	x, e := d.st.pane(p.PaneRef)
	if p.OperationRef != "" {
		op, oe := d.st.operation(p.OperationRef)
		if oe == sql.ErrNoRows {
			d.watchMu.Unlock()
			return nil, derr("TARGET_NOT_FOUND", "operation not found")
		}
		if oe != nil {
			d.watchMu.Unlock()
			return nil, oe
		}
		if terminalOperationStatus(fmt.Sprint(op["status"])) {
			d.watchMu.Unlock()
			return map[string]any{"matched": true, "operation": op}, nil
		}
	}
	if p.PaneRef == "" && p.OperationRef != "" {
		e = nil
	}
	if e != nil {
		d.watchMu.Unlock()
		return nil, derr("TARGET_NOT_FOUND", "pane not found")
	}
	if p.PaneRef != "" && x.Version > p.AfterVersion {
		d.watchMu.Unlock()
		return map[string]any{"matched": true, "paneRef": p.PaneRef, "resourceVersion": x.Version}, nil
	}
	testBarrier(d, "wait_after_snapshot_before_register")
	if len(d.watchers) >= 256 {
		d.watchMu.Unlock()
		return nil, derr("CAPABILITY_ABSENT", "watcher limit reached")
	}
	d.watchers[key] = w
	d.watchMu.Unlock()
	timer := time.NewTimer(time.Until(dl))
	defer timer.Stop()
	select {
	case r := <-w.ch:
		return r, nil
	case <-timer.C:
		d.removeWatcher(key)
		return nil, derr("DEADLINE_EXCEEDED", "wait deadline exceeded")
	case <-ctx.Done():
		d.removeWatcher(key)
		return nil, derr("CANCELLED", "wait cancelled")
	}
}
func terminalOperationStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "recovery-required" || status == "cancelled"
}
func (d *daemon) removeWatcher(key string) {
	d.watchMu.Lock()
	delete(d.watchers, key)
	d.watchMu.Unlock()
}
func readFrame(r io.Reader) ([]byte, error) {
	var h [4]byte
	if _, e := io.ReadFull(r, h[:]); e != nil {
		return nil, e
	}
	n := binary.BigEndian.Uint32(h[:])
	if n > MaxFrame {
		return nil, derr("FRAME_TOO_LARGE", "frame exceeds 1 MiB")
	}
	b := make([]byte, n)
	_, e := io.ReadFull(r, b)
	return b, e
}
func writeFrame(w io.Writer, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > MaxFrame {
		return derr("FRAME_TOO_LARGE", "frame exceeds 1 MiB")
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if _, e = w.Write(h[:]); e != nil {
		return e
	}
	_, e = w.Write(b)
	return e
}
