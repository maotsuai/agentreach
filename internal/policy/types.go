// Package policy owns the mandatory managed security ledger. Approval consumption
// and dispatch intent are one durable commit; unresolved intent is never replayed.
package policy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// Managed control requests and retained state have hard, non-rotating budgets.
const (
	MaxRequest    = 64 << 10
	MaxLedger     = 16 << 20
	MaxPending    = 256
	MaxApprovals  = 256
	MaxOperations = 4096
	MaxEvents     = 8192
	DefaultTTL    = 5 * time.Minute
	MaxTTL        = 15 * time.Minute
)

// Binding pins all authority to one immutable project generation.
type Binding struct {
	ProjectID    string `json:"project_id"`
	RecordDigest string `json:"record_digest"`
	Generation   uint64 `json:"generation"`
}

// HostParams is a closed set of supported capability arguments.
type HostParams struct {
	Path       string `json:"path,omitempty"`
	ServiceID  string `json:"service_id,omitempty"`
	GuestPort  uint16 `json:"guest_port,omitempty"`
	HostPort   uint16 `json:"host_port,omitempty"`
	Listen     string `json:"listen,omitempty"`
	Operation  string `json:"operation,omitempty"`
	SpecDigest string `json:"spec_digest,omitempty"`
}

// HostRequest contains no executor argv, URL origin, project or policy override.
type HostRequest struct {
	Capability string     `json:"capability"`
	TargetID   string     `json:"target_id"`
	Generation uint64     `json:"generation"`
	Params     HostParams `json:"params"`
}

// Request is the complete canonical authorisation payload.
type Request struct {
	Binding        Binding     `json:"binding"`
	PolicyRevision uint64      `json:"policy_revision"`
	Host           HostRequest `json:"host"`
	Scope          string      `json:"scope"`
	Effect         string      `json:"effect"`
}

// Pending is a private, reviewable plan.
type Pending struct {
	ID      string  `json:"request_id"`
	Request Request `json:"request"`
	Digest  string  `json:"request_digest"`
}

// Approval is an exact, expiring, single-use operator grant.
type Approval struct {
	ID        string `json:"approval_id"`
	RequestID string `json:"request_id"`
	Digest    string `json:"request_digest"`
	Revision  uint64 `json:"policy_revision"`
	Issued    Stamp  `json:"issued"`
	Deadline  int64  `json:"deadline"`
	Used      bool   `json:"used"`
	Revoked   bool   `json:"revoked"`
}

// Operation preserves dispatch intent and the last durable outcome.
type Operation struct {
	ID         string `json:"operation_id"`
	RequestID  string `json:"request_id,omitempty"`
	ApprovalID string `json:"approval_id,omitempty"`
	Digest     string `json:"request_digest"`
	Capability string `json:"capability"`
	Actor      string `json:"actor"`
	Revision   uint64 `json:"policy_revision"`
	TargetID   string `json:"target_id"`
	Generation uint64 `json:"generation"`
	Outcome    string `json:"outcome"` // intent means outcome unknown, never replay
}

// Event is one ordered security transition.
type Event struct {
	Schema         int       `json:"schema"`
	ID             string    `json:"event_id"`
	Sequence       uint64    `json:"sequence"`
	Time           Stamp     `json:"time"`
	Binding        Binding   `json:"binding"`
	PolicyRevision uint64    `json:"policy_revision"`
	Operation      Operation `json:"operation"`
	Phase          string    `json:"phase"`
	Reason         string    `json:"reason"`
}

// Ledger is the single authoritative policy, approval and intent record.
type Ledger struct {
	Schema     int               `json:"schema"`
	Binding    Binding           `json:"binding"`
	Revision   uint64            `json:"policy_revision"`
	GuestAllow bool              `json:"guest_allow"`
	Host       map[string]string `json:"host"`
	Key        []byte            `json:"hmac_key"`
	Highwater  Stamp             `json:"highwater"`
	Pending    []Pending         `json:"pending"`
	Approvals  []Approval        `json:"approvals"`
	Operations []Operation       `json:"operations"`
	Events     []Event           `json:"events"`
}

// View intentionally omits the private HMAC key and Guest payloads.
type View struct {
	Binding    Binding           `json:"binding"`
	Revision   uint64            `json:"policy_revision"`
	GuestAllow bool              `json:"guest_allow"`
	Host       map[string]string `json:"host"`
	Pending    []Pending         `json:"pending"`
	Approvals  []Approval        `json:"approvals"`
	Operations []Operation       `json:"operations"`
	Events     []Event           `json:"events"`
}

func view(l *Ledger) View {
	return View{l.Binding, l.Revision, l.GuestAllow, l.Host, l.Pending, l.Approvals, l.Operations, l.Events}
}
func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var targetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func privateDigest(key []byte, v any) (string, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	if len(b) > MaxRequest {
		return "", fmt.Errorf("request_too_large")
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil)), nil
}
func fail(code string) error { return fmt.Errorf("%s", code) }
