// Package terminal owns persistent managed text terminals and their durable input identities.
package terminal

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/bojieli/agentreach/internal/policy"
	"regexp"
	"unicode/utf8"
)

// Protocol budgets are hard ceilings; records and input IDs are never evicted.
const (
	MaxInput     = 16 << 10
	MaxRead      = 16 << 10
	MaxFrame     = 64 << 10
	MaxLog       = 8 << 20
	MaxTerminals = 64
	MaxInputs    = 256
	MaxStore     = 8 << 20
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidID recognises opaque, path-independent terminal/request/input identities.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// NewID returns a cryptographically random instance identity.
func NewID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}

// Request is the public CLI/MCP request. Data is JSON base64, not shell text.
type Request struct {
	RequestID  string   `json:"request_id,omitempty"`
	TerminalID string   `json:"terminal_id,omitempty"`
	InputID    string   `json:"input_id,omitempty"`
	Cwd        string   `json:"cwd,omitempty"`
	Argv       []string `json:"argv,omitempty"`
	Cols       int      `json:"cols,omitempty"`
	Rows       int      `json:"rows,omitempty"`
	Data       []byte   `json:"data,omitempty"`
	Key        string   `json:"key,omitempty"`
	Cursor     string   `json:"cursor,omitempty"`
	MaxBytes   int      `json:"max_bytes,omitempty"`
	Signal     string   `json:"signal,omitempty"`
}

// Identity pins the backend, independent of a tmux/PID name's possible reuse.
type Identity struct {
	ID      string `json:"id"`
	Epoch   string `json:"epoch"`
	Nonce   string `json:"nonce"`
	Binding string `json:"binding"`
	Server  string `json:"server,omitempty"`
	Session string `json:"session,omitempty"`
	Pane    string `json:"pane,omitempty"`
}

// GuestRequest is internal control traffic; no caller may provide backend paths/commands.
type GuestRequest struct {
	Op       string   `json:"op"`
	Identity Identity `json:"identity"`
	Command  string   `json:"command,omitempty"`
	Data     []byte   `json:"data,omitempty"`
	Key      string   `json:"key,omitempty"`
	Cols     int      `json:"cols,omitempty"`
	Rows     int      `json:"rows,omitempty"`
	Offset   int64    `json:"offset,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

// Observation contains untrusted Guest output, kept separate from the security ledger.
type Observation struct {
	Identity    Identity `json:"identity"`
	State       string   `json:"state"`
	Offset      int64    `json:"offset"`
	Data        []byte   `json:"data,omitempty"`
	Complete    bool     `json:"complete"`
	Gap         string   `json:"gap,omitempty"`
	ExitCode    *int     `json:"exit_code,omitempty"`
	Descendants string   `json:"descendants,omitempty"`
}

// Result is returned even on an unknown outcome, preserving its operation identity.
type Result struct {
	TerminalID     string `json:"terminal_id,omitempty"`
	Epoch          string `json:"epoch,omitempty"`
	State          string `json:"state"`
	OperationID    string `json:"operation_id,omitempty"`
	InputID        string `json:"input_id,omitempty"`
	Accepted       bool   `json:"accepted,omitempty"`
	Orphan         bool   `json:"orphan,omitempty"`
	Data           []byte `json:"data,omitempty"`
	NextCursor     string `json:"next_cursor,omitempty"`
	EarliestCursor string `json:"earliest_available_cursor,omitempty"`
	Complete       bool   `json:"complete"`
	Gap            string `json:"gap,omitempty"`
	ExitCode       *int   `json:"exit_code,omitempty"`
	Descendants    string `json:"descendants,omitempty"`
}

func cursor(id Identity, offset int64) string {
	return fmt.Sprintf("%s:%s:%d", id.ID, id.Epoch, offset)
}

// ValidateObservation rejects malformed Guest control data before it can alter Host identity/state.
func ValidateObservation(o Observation) error {
	if !knownGap(o.Gap) {
		return fmt.Errorf("output_gap: invalid gap reason")
	}
	if !ValidID(o.Identity.ID) || !ValidID(o.Identity.Epoch) || !ValidID(o.Identity.Nonce) || !digestPattern.MatchString(o.Identity.Binding) || o.Offset < 0 || o.Offset > MaxLog || len(o.Data) > MaxRead {
		return fmt.Errorf("output_gap: invalid Guest observation")
	}
	if o.Identity.Server != "" && (!sessionPattern.MatchString(o.Identity.Session) || !panePattern.MatchString(o.Identity.Pane) || len(o.Identity.Server) > 128) {
		return &BackendIdentityError{Cause: fmt.Errorf("malformed backend identity")}
	}
	switch o.State {
	case "running", "exited", "unavailable", "lost", "closed":
	default:
		return fmt.Errorf("output_gap: invalid backend state")
	}
	if o.Complete && (o.State != "exited" && o.State != "closed" || o.Gap != "") {
		return fmt.Errorf("output_gap: invalid completion")
	}
	if o.ExitCode != nil && (*o.ExitCode < 0 || *o.ExitCode > 255) {
		return fmt.Errorf("output_gap: invalid exit status")
	}
	return nil
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var sessionPattern = regexp.MustCompile(`^\$[0-9]+$`)
var panePattern = regexp.MustCompile(`^%[0-9]+$`)

// DecodeRequest preserves the byte contract by refusing invalid JSON UTF-8 before decoding.
func DecodeRequest(b []byte) (Request, error) {
	var q Request
	if len(b) > MaxFrame {
		return q, fmt.Errorf("request_budget: terminal frame")
	}
	if !utf8.Valid(b) {
		return q, fmt.Errorf("policy_denied: invalid JSON UTF-8")
	}
	e := policy.VerifyEncoded(b, &q)
	return q, e
}

// BackendIdentityError distinguishes a terminal backend guard from a project runtime loss.
// It retires the current connection; a new connection must verify the unchanged backend.
type BackendIdentityError struct{ Cause error }

func (e *BackendIdentityError) Error() string {
	return fmt.Sprintf("identity_changed: terminal backend: %v", e.Cause)
}
func (e *BackendIdentityError) Unwrap() error { return e.Cause }

func knownGap(s string) bool {
	switch s {
	case "", "collector_state_missing", "collector_failed", "log_missing", "log_changed", "log_truncated", "output_limit", "exit_unconfirmed", "backend_missing", "backend_lost", "backend_unavailable", "closed_before_drain", "close_state_invalid", "runtime_unavailable", "runtime_lost", "backend_identity_changed", "resource_lost":
		return true
	}
	return false
}
