// Package coord implements the structured workstream coordination domain:
// immutable typed records, controller-owned durable state, role and lease
// enforcement, exact-SHA review binding, and provider-native task pointer
// delivery. It deliberately contains no tmux, pane, or terminal semantics;
// terminal text is never a semantic input to any transition in this package.
package coord

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Error is the coordination domain error. The controller transport maps it to
// the same wire shape as its own domain errors; codes reuse the existing
// protocol error vocabulary so CLI and MCP surface identical semantics.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func cerr(code, message string) error {
	return &Error{Code: code, Message: message}
}

// Bounds are explicit and fail closed. A record that exceeds them is rejected
// before any mutation.
const (
	MaxRecordBytes  = 128 * 1024
	maxListItems    = 64
	maxShortString  = 256
	maxLongString   = 4096
	maxTextString   = 16384
	maxEventsPage   = 200
	maxWaiters      = 128
	maxWaitDuration = 10 * time.Minute
	maxTasksPerWS   = 32
	maxRolesPerWS   = 8
	maxCorrections  = 1
)

var (
	workstreamIDRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	taskIDRE        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
	requestIDRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,119}$`)
	leaseIDRE       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,119}$`)
	gitShaRE        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256RE        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	idemRE          = regexp.MustCompile(`^ik_([0-9]{1,12})_([a-f0-9]{32})$`)
	policyVersionRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)
)

// Roles are fixed server-side assignments. A mutation payload can never claim
// or escalate one; the service resolves the caller's role from the durable
// role table keyed by the authenticated client identity.
const (
	RoleOrchestrator     = "orchestrator"
	RoleBuilder          = "builder"
	RoleReviewer         = "reviewer"
	RoleReleaseConductor = "release-conductor"
	RoleOperator         = "operator"
)

func validRole(r string) bool {
	switch r {
	case RoleOrchestrator, RoleBuilder, RoleReviewer, RoleReleaseConductor:
		return true
	}
	return false
}

// Task statuses. Transitions are enforced by the service; there is no path
// from terminal text into any of these.
const (
	StatusPublished        = "published"
	StatusAcknowledged     = "acknowledged"
	StatusClaimed          = "claimed"
	StatusHandoffSubmitted = "handoff-submitted"
	StatusReviewRequested  = "review-requested"
	StatusReviewedPass     = "reviewed-pass"
	StatusReviewedChanges  = "reviewed-changes-requested"
	StatusAccepted         = "accepted"
	StatusReleased         = "released"
)

// Verdicts for a review result.
const (
	VerdictPass             = "PASS"
	VerdictPassWithNotes    = "PASS_WITH_NONBLOCKING_NOTES"
	VerdictChangesRequested = "CHANGES_REQUESTED"
)

func passingVerdict(v string) bool {
	return v == VerdictPass || v == VerdictPassWithNotes
}

func newRef(prefix string) string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// strictDecode accepts exactly one JSON value and rejects unknown fields and
// trailing values.
func strictDecode(raw []byte, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON")
		}
		return err
	}
	return nil
}

func validIdempotencyKey(k string, now time.Time) error {
	m := idemRE.FindStringSubmatch(k)
	if m == nil {
		return cerr("INVALID_REQUEST", "invalid idempotency key")
	}
	var stamp int64
	_, _ = fmt.Sscan(m[1], &stamp)
	t := time.Unix(stamp, 0)
	if t.After(now.Add(5 * time.Minute)) {
		return cerr("INVALID_REQUEST", "idempotency key is in the future")
	}
	if t.Before(now.Add(-30 * 24 * time.Hour)) {
		return cerr("IDEMPOTENCY_EXPIRED", "idempotency key is older than 30 days")
	}
	return nil
}

func boundedString(s string, max int, field string) error {
	if !utf8.ValidString(s) {
		return cerr("INVALID_REQUEST", field+" is not valid UTF-8")
	}
	if len(s) > max {
		return cerr("INVALID_REQUEST", fmt.Sprintf("%s exceeds %d bytes", field, max))
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			return cerr("INVALID_REQUEST", field+" contains control characters")
		}
		if r == 0x7f {
			return cerr("INVALID_REQUEST", field+" contains control characters")
		}
	}
	return nil
}

func boundedList(n int, field string) error {
	if n > maxListItems {
		return cerr("INVALID_REQUEST", fmt.Sprintf("%s exceeds %d items", field, maxListItems))
	}
	return nil
}

func validRFC3339(s, field string) error {
	if s == "" {
		return cerr("INVALID_REQUEST", field+" is required")
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		return cerr("INVALID_REQUEST", field+" is not RFC3339")
	}
	return nil
}
