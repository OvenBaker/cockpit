package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const Protocol = "1.0"
const MaxFrame = 1 << 20

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}
type domainError struct {
	Code    string
	Message string
}
type standardRPCError struct {
	code    int
	message string
}

func (e *standardRPCError) Error() string { return e.message }
func rpcStandard(code int, message string) error {
	return &standardRPCError{code: code, message: message}
}

func (e *domainError) Error() string  { return e.Code + ": " + e.Message }
func derr(code, message string) error { return &domainError{code, message} }
func id(prefix string) string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b)
}

type pane struct {
	Ref, WorkspaceRef, WindowID, PaneID, Badge string
	Generation, Version                        int64
	Fenced                                     bool
}
type workspace struct {
	Ref, WindowID, Name string
	Generation, Version int64
}
type paneExpectation struct {
	Kind            string `json:"kind"`
	PaneRef         string `json:"paneRef"`
	Generation      int64  `json:"generation"`
	ResourceVersion int64  `json:"resourceVersion"`
	Material        struct {
		Lifecycle string `json:"lifecycle"`
	} `json:"material"`
}
type badgeParams struct {
	Protocol       string            `json:"protocol"`
	Deadline       string            `json:"deadline"`
	IdempotencyKey string            `json:"idempotencyKey"`
	PaneRef        string            `json:"paneRef"`
	Badge          string            `json:"badge"`
	Expectations   []paneExpectation `json:"expectations"`
}
type sessionParams struct {
	Protocol       string `json:"protocol"`
	ClientID       string `json:"clientId"`
	ClaimedProfile string `json:"claimedProfile"`
	Credential     string `json:"credential"`
}

var idemRE = regexp.MustCompile(`^ik_([0-9]{1,12})_([a-f0-9]{32})$`)

func validateBadge(s string) error {
	if utf8.RuneCountInString(s) > 48 {
		return derr("INVALID_REQUEST", "badge exceeds 48 code points")
	}
	if strings.ContainsAny(s, "\x00\x1b\r\n\t") {
		return derr("INVALID_REQUEST", "badge contains control characters")
	}
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return derr("INVALID_REQUEST", "badge contains control characters")
		}
	}
	return nil
}

// strictJSON accepts exactly one JSON value. Decoder.More only applies inside
// arrays/objects, so a second decode is required to reject trailing values.
func strictJSON(raw []byte, dst any) error {
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
func validateIdempotency(k string, now time.Time) error {
	m := idemRE.FindStringSubmatch(k)
	if m == nil {
		return derr("INVALID_REQUEST", "invalid idempotency key")
	}
	var stamp int64
	_, _ = fmt.Sscan(m[1], &stamp)
	t := time.Unix(stamp, 0)
	if t.After(now.Add(5 * time.Minute)) {
		return derr("INVALID_REQUEST", "idempotency key is in the future")
	}
	if t.Before(now.Add(-30 * 24 * time.Hour)) {
		return derr("IDEMPOTENCY_EXPIRED", "idempotency key is older than 30 days")
	}
	return nil
}
