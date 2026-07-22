package coord

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// The artifact store is a controller-private content-addressed area. Bodies
// are immutable: publication stages a private temp file, fsyncs it, and
// hard-links it into place with no-replace semantics before the metadata
// transaction commits. A hash collision with different bytes fails closed.

func (s *Service) artifactDir() string { return filepath.Join(s.root, "coord", "artifacts") }
func (s *Service) seedDir() string     { return filepath.Join(s.root, "coord", "seeds") }

func (s *Service) artifactPath(sha string) string {
	return filepath.Join(s.artifactDir(), sha[:2], sha)
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return cerr("CONTROLLER_NOT_READY", "coordination directory is not private")
	}
	return nil
}

// validateCanonicalRecord bounds and shape-checks the canonical bytes shared
// by every stored record.
func validateCanonicalRecord(raw []byte) error {
	if len(raw) == 0 {
		return cerr("INVALID_REQUEST", "record is required")
	}
	if len(raw) > MaxRecordBytes {
		return cerr("INVALID_REQUEST", fmt.Sprintf("record exceeds %d bytes", MaxRecordBytes))
	}
	if !utf8.Valid(raw) {
		return cerr("INVALID_REQUEST", "record is not valid UTF-8")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return cerr("INVALID_REQUEST", "record must be a JSON object")
	}
	if !json.Valid(trimmed) {
		return cerr("INVALID_REQUEST", "record is not valid JSON")
	}
	return nil
}

// canonicalBytes trims surrounding whitespace: the stored canonical bytes are
// exactly the JSON value, so the recorded digest is reproducible by hashing
// the stored file.
func canonicalBytes(raw []byte) []byte { return bytes.TrimSpace(raw) }

// stageArtifact writes the canonical bytes durably into the content store
// and returns their digest. It is idempotent: re-staging identical bytes is a
// no-op; a digest that already exists with different bytes fails closed.
// Callers must follow with a metadata transaction; an orphaned body from a
// crashed publication is pruned by Reconcile and is never served, because
// reads require committed metadata.
func (s *Service) stageArtifact(canon []byte) (string, error) {
	sha := sha256Hex(canon)
	dir := filepath.Dir(s.artifactPath(sha))
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	final := s.artifactPath(sha)
	if info, err := os.Lstat(final); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", cerr("CONTROLLER_NOT_READY", "artifact body is not a regular file")
		}
		existing, rerr := os.ReadFile(final)
		if rerr != nil || !bytes.Equal(existing, canon) {
			return "", cerr("CONTROLLER_NOT_READY", "artifact body digest collision")
		}
		return sha, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".stage-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = os.Chmod(tmpName, 0600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err = tmp.Write(canon); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}
	// Hard link gives no-replace semantics: it fails if the final name exists.
	if err = os.Link(tmpName, final); err != nil {
		if os.IsExist(err) {
			existing, rerr := os.ReadFile(final)
			if rerr == nil && bytes.Equal(existing, canon) {
				return sha, nil
			}
			return "", cerr("CONTROLLER_NOT_READY", "artifact body digest collision")
		}
		return "", err
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return sha, nil
}

// rowQuerier lets artifact verification run against either the pooled
// connection or an open mutation transaction (the store allows exactly one
// connection, so a transactional caller must pass its own tx).
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// readArtifact returns the committed canonical bytes for a digest, verifying
// length and digest against the metadata row before serving them.
func (s *Service) readArtifact(q rowQuerier, sha string) ([]byte, error) {
	if !sha256RE.MatchString(sha) {
		return nil, cerr("INVALID_REQUEST", "sha256 must be 64 lowercase hex")
	}
	var storedBytes int64
	err := q.QueryRow("SELECT bytes FROM coord_artifacts WHERE sha256=?", sha).Scan(&storedBytes)
	if err != nil {
		return nil, cerr("TARGET_NOT_FOUND", "artifact not found")
	}
	path := s.artifactPath(sha)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, cerr("CONTROLLER_NOT_READY", "artifact body is missing")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != storedBytes || sha256Hex(b) != sha {
		return nil, cerr("CONTROLLER_NOT_READY", "artifact body failed digest verification")
	}
	return b, nil
}

// Reconcile verifies every committed artifact body and removes staging
// leftovers and unreferenced bodies from crashed publications. It runs before
// the controller serves traffic.
func (s *Service) Reconcile() error {
	for _, dir := range []string{filepath.Join(s.root, "coord"), s.artifactDir(), s.seedDir()} {
		if err := ensurePrivateDir(dir); err != nil {
			return err
		}
	}
	rows, err := s.db.Query("SELECT sha256, bytes FROM coord_artifacts")
	if err != nil {
		return err
	}
	committed := map[string]int64{}
	for rows.Next() {
		var sha string
		var n int64
		if err = rows.Scan(&sha, &n); err != nil {
			_ = rows.Close()
			return err
		}
		committed[sha] = n
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for sha, n := range committed {
		b, err := os.ReadFile(s.artifactPath(sha))
		if err != nil || int64(len(b)) != n || sha256Hex(b) != sha {
			return cerr("CONTROLLER_NOT_READY", "committed artifact body is missing or corrupt: "+sha)
		}
	}
	entries, err := os.ReadDir(s.artifactDir())
	if err != nil {
		return err
	}
	for _, sub := range entries {
		if !sub.IsDir() {
			_ = os.Remove(filepath.Join(s.artifactDir(), sub.Name()))
			continue
		}
		subdir := filepath.Join(s.artifactDir(), sub.Name())
		files, err := os.ReadDir(subdir)
		if err != nil {
			return err
		}
		for _, f := range files {
			name := f.Name()
			if strings.HasPrefix(name, ".stage-") {
				_ = os.Remove(filepath.Join(subdir, name))
				continue
			}
			if _, ok := committed[name]; !ok {
				// Body staged by a publication whose transaction never
				// committed: prune it; it was never observable.
				_ = os.Remove(filepath.Join(subdir, name))
			}
		}
	}
	return nil
}
