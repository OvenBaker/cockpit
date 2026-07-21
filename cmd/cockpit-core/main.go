package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/gareth/cockpit-core/internal/core"
)

const maxFrame = 1 << 20

func main() {
	if len(os.Args) < 2 {
		fatal("usage: cockpit-core daemon|ctl|mcp-stdio")
	}
	switch os.Args[1] {
	case "daemon":
		daemon(os.Args[2:])
	case "ctl":
		ctl(os.Args[2:])
	case "mcp-stdio":
		mcpStdio(os.Args[2:])
	default:
		fatal("usage: cockpit-core daemon|ctl|mcp-stdio")
	}
}

// mcp-stdio deliberately contains no tmux logic or controller policy. It is
// one-client stdio JSON-RPC translation onto the resident controller's framed
// socket; disconnecting it cannot abandon controller waits or operations.
func mcpStdio(args []string) {
	fs := flag.NewFlagSet("mcp-stdio", flag.ExitOnError)
	socket := fs.String("socket", "", "control socket")
	fs.Parse(args)
	if *socket == "" {
		fatal("mcp-stdio requires --socket")
	}
	credential, err := privateMCPCredential()
	if err != nil {
		fatal("mcp-stdio credential unavailable")
	}
	c, err := net.Dial("unix", *socket)
	if err != nil {
		fatal(err.Error())
	}
	defer c.Close()
	if err = writeFrame(c, map[string]any{"jsonrpc": "2.0", "id": "open", "method": "session.open", "params": map[string]any{"protocol": "1.0", "clientId": "cockpit-mcp", "claimedProfile": "mcp-local", "credential": credential}}); err != nil {
		fatal(err.Error())
	}
	opened, err := readFrame(c)
	if err != nil {
		fatal(err.Error())
	}
	var openedResponse struct {
		Result struct {
			Ready bool `json:"ready"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if json.Unmarshal(opened, &openedResponse) != nil || openedResponse.Error != nil || !openedResponse.Result.Ready {
		fatal("mcp-stdio controller session denied")
	}
	out := bufio.NewWriter(os.Stdout)
	var outMu, connMu, pendingMu sync.Mutex
	write := func(v any) {
		outMu.Lock()
		defer outMu.Unlock()
		b, _ := json.Marshal(v)
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}
	pending, byMCP := map[string]json.RawMessage{}, map[string]string{}
	seq := 0
	// A dedicated reader keeps a long wait from blocking stdin. Cancellation is
	// consequently sent on the same controller connection and reaches its
	// request context, rather than being a best-effort process kill.
	go func() {
		for {
			b, e := readFrame(c)
			if e != nil {
				return
			}
			var response struct {
				ID     json.RawMessage `json:"id"`
				Result any             `json:"result"`
				Error  any             `json:"error"`
			}
			if json.Unmarshal(b, &response) != nil {
				continue
			}
			var controllerID string
			_ = json.Unmarshal(response.ID, &controllerID)
			pendingMu.Lock()
			mcpID, ok := pending[controllerID]
			if ok {
				delete(pending, controllerID)
				delete(byMCP, string(mcpID))
			}
			pendingMu.Unlock()
			if !ok {
				continue
			} // cancellation acknowledgements are transport-internal.
			payload, _ := json.Marshal(response.Result)
			if response.Error != nil {
				payload, _ = json.Marshal(response.Error)
			}
			write(map[string]any{"jsonrpc": "2.0", "id": mcpID, "result": map[string]any{"content": []map[string]string{{"type": "text", "text": string(payload)}}, "structuredContent": response.Result, "isError": response.Error != nil}})
		}
	}()
	resolve := func(args map[string]any) (map[string]any, error) {
		if ref, ok := args["paneRef"].(string); ok && ref != "" {
			return map[string]any{"paneRef": ref}, nil
		}
		loc, ok := args["locator"].(string)
		if !ok || loc == "" {
			return nil, errors.New("paneRef or canonical locator is required")
		}
		return resolvePane(*socket, credential, loc)
	}
	s := bufio.NewScanner(os.Stdin)
	s.Buffer(make([]byte, 4096), maxFrame)
	for s.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if json.Unmarshal(s.Bytes(), &req) != nil || req.JSONRPC != "2.0" {
			write(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error"}})
			continue
		}
		if req.Method == "notifications/initialized" {
			continue
		}
		if req.Method == "notifications/cancelled" {
			var cancel struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			if json.Unmarshal(req.Params, &cancel) == nil {
				pendingMu.Lock()
				target := byMCP[string(cancel.RequestID)]
				pendingMu.Unlock()
				if target != "" {
					connMu.Lock()
					_ = writeFrame(c, map[string]any{"jsonrpc": "2.0", "id": "cancel-" + target, "method": "rpc.cancel", "params": map[string]any{"requestId": target}})
					connMu.Unlock()
				}
			}
			continue
		}
		if req.Method == "initialize" {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"protocolVersion": "2025-03-26", "serverInfo": map[string]any{"name": "cockpit-core", "version": "1.0"}, "capabilities": map[string]any{"tools": map[string]any{}}}})
			continue
		}
		if req.Method == "tools/list" {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"tools": core.MCPTools()}})
			continue
		}
		if req.Method != "tools/call" {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "error": map[string]any{"code": -32601, "message": "method not found"}})
			continue
		}
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &call) != nil || call.Arguments == nil {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "error": map[string]any{"code": -32602, "message": "invalid params"}})
			continue
		}
		method, ok := core.MCPMethod(call.Name)
		if !ok {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"content": []map[string]string{{"type": "text", "text": "unknown tool"}}, "isError": true}})
			continue
		}
		operationOnlyWait := method == "wait.for_change" && call.Arguments["operationRef"] != nil && call.Arguments["paneRef"] == nil && call.Arguments["locator"] == nil
		if method != "state.snapshot" && method != "capabilities.get" && !operationOnlyWait {
			if target, e := resolve(call.Arguments); e != nil {
				write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"content": []map[string]string{{"type": "text", "text": e.Error()}}, "isError": true}})
				continue
			} else {
				call.Arguments["paneRef"] = target["paneRef"]
				delete(call.Arguments, "locator")
			}
		}
		params, _ := json.Marshal(call.Arguments)
		seq++
		controllerID := "mcp-" + strconv.Itoa(seq)
		pendingMu.Lock()
		pending[controllerID] = append(json.RawMessage(nil), req.ID...)
		byMCP[string(req.ID)] = controllerID
		pendingMu.Unlock()
		connMu.Lock()
		err := writeFrame(c, map[string]any{"jsonrpc": "2.0", "id": controllerID, "method": method, "params": json.RawMessage(params)})
		connMu.Unlock()
		if err != nil {
			pendingMu.Lock()
			delete(pending, controllerID)
			delete(byMCP, string(req.ID))
			pendingMu.Unlock()
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "error": map[string]any{"code": -32001, "message": "controller unavailable"}})
			continue
		}
	}
}

func privateMCPCredential() (string, error) {
	path := os.Getenv("COCKPIT_MCP_CREDENTIAL_FILE")
	return privateCredentialFile(path)
}
func privateCredentialFile(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("credential file is required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("credential file must be private")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	credential := strings.TrimSpace(string(b))
	if credential == "" || len(credential) > 512 || strings.ContainsAny(credential, "\x00\r\n") {
		return "", errors.New("credential file is invalid")
	}
	return credential, nil
}

func resolvePane(socket, credential, locator string) (map[string]any, error) {
	c, err := net.Dial("unix", socket)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	open := map[string]any{"jsonrpc": "2.0", "id": "open", "method": "session.open", "params": map[string]any{"protocol": "1.0", "clientId": "cockpit-mcp", "claimedProfile": "mcp-local", "credential": credential}}
	if err = writeFrame(c, open); err != nil {
		return nil, err
	}
	opened, err := readFrame(c)
	if err != nil {
		return nil, err
	}
	var openedResponse struct {
		Result struct {
			Ready bool `json:"ready"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if json.Unmarshal(opened, &openedResponse) != nil || openedResponse.Error != nil || !openedResponse.Result.Ready {
		return nil, errors.New("controller session denied")
	}
	if err = writeFrame(c, map[string]any{"jsonrpc": "2.0", "id": "resolve", "method": "pane.resolve", "params": map[string]any{"canonical": locator}}); err != nil {
		return nil, err
	}
	b, err := readFrame(c)
	if err != nil {
		return nil, err
	}
	var r struct {
		Result map[string]any `json:"result"`
		Error  any            `json:"error"`
	}
	if json.Unmarshal(b, &r) != nil || r.Error != nil {
		return nil, errors.New("canonical pane locator was not found")
	}
	ref, _ := r.Result["paneRef"].(string)
	if ref == "" {
		return nil, errors.New("canonical pane locator was not found")
	}
	return map[string]any{"paneRef": ref}, nil
}

func daemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	root := fs.String("test-root", "", "isolated test root")
	runtimeRoot := fs.String("runtime-root", "", "private controller runtime root for --live-cockpit")
	socket := fs.String("socket", "", "control socket")
	tmuxSocket := fs.String("tmux-socket", "", "throwaway tmux socket")
	credentials := fs.String("credentials-file", "", "private controller credential registry")
	liveCockpit := fs.Bool("live-cockpit", false, "admit only the named Cockpit tmux socket")
	fs.Parse(args)
	if *liveCockpit {
		if *runtimeRoot == "" || *socket == "" || *credentials == "" || *root != "" || *tmuxSocket != "" {
			fatal("--live-cockpit requires --runtime-root --socket --credentials-file and rejects --test-root/--tmux-socket")
		}
		d, err := core.NewLiveCockpitDaemon(*runtimeRoot, *socket, *credentials)
		if err != nil {
			fatal(err.Error())
		}
		defer d.Close()
		if err := d.Serve(); err != nil {
			fatal(err.Error())
		}
		return
	}
	if *root == "" || *socket == "" || *tmuxSocket == "" || *credentials == "" || *runtimeRoot != "" {
		fatal("daemon requires --test-root --socket --tmux-socket --credentials-file (or --live-cockpit with --runtime-root)")
	}
	d, err := core.NewDaemonWithCredentials(*root, *socket, *tmuxSocket, *credentials)
	if err != nil {
		fatal(err.Error())
	}
	defer d.Close()
	if err := d.Serve(); err != nil {
		fatal(err.Error())
	}
}

func ctl(args []string) {
	fs := flag.NewFlagSet("ctl", flag.ExitOnError)
	socket := fs.String("socket", "", "control socket")
	credentialFile := fs.String("credential-file", "", "private controller credential file")
	fs.Parse(args)
	if *socket == "" || *credentialFile == "" || fs.NArg() < 1 {
		fatal("ctl requires --socket --credential-file METHOD [JSON params]")
	}
	credential, err := privateCredentialFile(*credentialFile)
	if err != nil {
		fatal("ctl credential unavailable")
	}
	params := json.RawMessage(`{}`)
	if fs.NArg() > 1 {
		params = json.RawMessage(fs.Arg(1))
	}
	c, err := net.Dial("unix", *socket)
	if err != nil {
		fatal(err.Error())
	}
	defer c.Close()
	if err := writeFrame(c, map[string]any{"jsonrpc": "2.0", "id": "open", "method": "session.open", "params": map[string]any{"protocol": "1.0", "clientId": "cockpitctl", "claimedProfile": "local-operator", "credential": credential}}); err != nil {
		fatal(err.Error())
	}
	if _, err := readFrame(c); err != nil {
		fatal(err.Error())
	}
	if err := writeFrame(c, map[string]any{"jsonrpc": "2.0", "id": "ctl", "method": fs.Arg(0), "params": params}); err != nil {
		fatal(err.Error())
	}
	r, err := readFrame(c)
	if err != nil {
		fatal(err.Error())
	}
	os.Stdout.Write(append(r, '\n'))
}

func writeFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > maxFrame {
		return errors.New("frame too large")
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if _, err = w.Write(h[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}
func readFrame(r io.Reader) ([]byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(h[:])
	if n > maxFrame {
		return nil, errors.New("frame too large")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}
func fatal(s string) { fmt.Fprintln(os.Stderr, s); os.Exit(1) }
