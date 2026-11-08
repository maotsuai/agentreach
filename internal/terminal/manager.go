package terminal

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/state"
)

type input struct {
	ID        string `json:"id"`
	Digest    string `json:"digest"`
	State     string `json:"state"`
	Operation string `json:"operation"`
}
type record struct {
	Identity  Identity `json:"identity"`
	RequestID string   `json:"request_id"`
	Digest    string   `json:"digest"`
	State     string   `json:"state"`
	Operation string   `json:"operation"`
	Inputs    []input  `json:"inputs"`
	Highwater int64    `json:"highwater"`
	Gap       string   `json:"gap,omitempty"`
}
type store struct {
	Schema  int            `json:"schema"`
	Binding policy.Binding `json:"binding"`
	Key     []byte         `json:"key"`
	Records []record       `json:"records"`
}
type envelope struct {
	Store    store  `json:"store"`
	Checksum string `json:"checksum"`
}

// RecordedTerminal is the retained identity, not a live tmux observation.
type RecordedTerminal struct {
	ID          string `json:"terminal_id"`
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
}

// Recorded returns the validated retained terminals without locks, writes,
// Guest commands or the current workspace guard.
func (m *Manager) Recorded() ([]RecordedTerminal, error) {
	s, err := m.load()
	if err != nil {
		return nil, err
	}
	x := make([]RecordedTerminal, 0, len(s.Records))
	for _, r := range s.Records {
		x = append(x, RecordedTerminal{r.Identity.ID, r.Operation, r.State})
	}
	return x, nil
}

// RetainedLoss returns diagnostic metadata for a lost backend without dispatching
// to it. Callers are responsible for checking their own binding before use.
func (m *Manager) RetainedLoss(id string) (Result, error) {
	s, err := m.load()
	if err != nil {
		return Result{}, err
	}
	for _, r := range s.Records {
		if r.Identity.ID == id && r.State == "lost" {
			return result(r, saved(r)), nil
		}
	}
	return Result{}, fmt.Errorf("resource_lost: no retained terminal loss")
}

// Config contains only trusted session callbacks. Callers cannot override these through a request.
type Config struct {
	Base     string
	Binding  policy.Binding
	Runtime  string // immutable daemon/container/StartedAt digest
	Policy   *policy.Store
	Check    func(context.Context) error
	Paths    func() error
	Dispatch func(context.Context, GuestRequest) (Observation, error)
	Launch   func(context.Context, string, []string, string) (string, error)
}

// Manager serialises the complete object lifecycle and permanently latches a failed runtime guard.
type Manager struct {
	cfg     Config
	mu      sync.Mutex
	failure atomic.Pointer[connectionFailure]
	write   func(string, any) error
}

// New creates a new connection to existing terminal identities, without creating storage.
func New(cfg Config) *Manager { return &Manager{cfg: cfg, write: state.Write} }

// Ready shares one permanent failure across checks before calls and under the policy lock.
type connectionFailure struct{ cause error }

// Failure is a nonblocking snapshot for the enclosing MCP connection's shared latch.
func (m *Manager) Failure() error {
	if f := m.failure.Load(); f != nil {
		return f.cause
	}
	return nil
}
func (m *Manager) retire(e error) {
	if e != nil {
		m.failure.CompareAndSwap(nil, &connectionFailure{e})
	}
}

// Ready shares one permanent failure across checks before calls and under the policy lock.
func (m *Manager) Ready(ctx context.Context) error {
	if e := m.Failure(); e != nil {
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Failure() == nil {
		m.retire(m.cfg.Check(ctx))
	}
	return m.Failure()
}
func (m *Manager) dispatch(ctx context.Context, q GuestRequest) (Observation, error) {
	o, e := m.cfg.Dispatch(ctx, q)
	if (e == nil || o.Identity.ID != "") && !sameInstance(q.Identity, o.Identity) {
		e = &BackendIdentityError{errors.Join(e, fmt.Errorf("observation identity mismatch during %s", q.Op))}
	}
	if e == nil {
		e = ValidateObservation(o)
	}
	if e == nil && o.Gap == "backend_identity_changed" {
		e = &BackendIdentityError{fmt.Errorf("Guest backend identity check failed")}
	}
	if e != nil && m.Failure() == nil {
		var backend *BackendIdentityError
		var runtime *docker.Error
		if errors.As(e, &backend) {
			m.retire(e)
		} else if errors.As(e, &runtime) && (runtime.Kind == "identity_changed" || runtime.Kind == "generation_mismatch") {
			e = &BackendIdentityError{e}
			m.retire(e)
		}
	}
	return o, e
}
func (m *Manager) observeError(r *record, cause error) Observation {
	status, gap := failureState(cause)
	if m.Failure() == nil {
		status = "unavailable"
		gap = "backend_unavailable"
	}
	if r.State != "closed" && r.State != "lost" && (r.State != "creating" || status == "lost") {
		r.State = status
	}
	r.Gap = gap
	return Observation{State: status, Offset: r.Highwater, Gap: gap}
}
func saved(r record) Observation {
	gap := r.Gap
	if r.State == "lost" && gap == "" {
		gap = "resource_lost"
	}
	return Observation{State: r.State, Offset: r.Highwater, Gap: gap}
}
func remember(r *record, o Observation) {
	if o.Offset > r.Highwater {
		r.Highwater = o.Offset
	}
	if o.Gap != "" {
		r.Gap = o.Gap
	} else if r.State != "lost" && r.State != "closed" {
		r.Gap = ""
	}
	if r.State != "lost" && r.State != "closed" && (r.State != "creating" || o.State == "lost") {
		r.State = o.State
	}
}
func failureState(cause error) (string, string) {
	var backend *BackendIdentityError
	var runtime *docker.Error
	if errors.As(cause, &backend) {
		return "unavailable", "backend_identity_changed"
	}
	if errors.As(cause, &runtime) && (runtime.Kind == "identity_changed" || runtime.Kind == "generation_mismatch") {
		return "lost", "runtime_lost"
	}
	return "unavailable", "runtime_unavailable"
}

func (m *Manager) path() string {
	return filepath.Join(m.cfg.Base, "terminals", m.cfg.Binding.ProjectID+".json")
}
func (m *Manager) safe() error {
	if !project.ValidID(m.cfg.Binding.ProjectID) || len(m.cfg.Binding.RecordDigest) != 64 || m.cfg.Runtime == "" {
		return fmt.Errorf("policy_denied: terminal binding")
	}
	if e := m.cfg.Paths(); e != nil {
		return e
	}
	for _, p := range []string{filepath.Dir(m.path()), m.path(), m.path() + ".lock"} {
		if e := hostpath.Canonical(p, true); e != nil {
			return e
		}
	}
	if e := hostpath.PrivateFile(m.path()); e != nil {
		return e
	}
	for _, p := range []string{filepath.Dir(m.path()), m.path()} {
		fi, e := os.Lstat(p)
		if e == nil {
			if !private(fi) {
				return fmt.Errorf("state_corrupt: unsafe terminal storage")
			}
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	return nil
}
func checksum(s store) string {
	b, _ := json.Marshal(s)
	v := sha256.Sum256(b)
	return hex.EncodeToString(v[:])
}
func (m *Manager) load() (store, error) {
	var s store
	if e := m.safe(); e != nil {
		return s, e
	}
	f, e := os.Open(m.path())
	if os.IsNotExist(e) {
		return store{Schema: 1, Binding: m.cfg.Binding, Records: []record{}}, nil
	}
	if e != nil {
		return s, e
	}
	defer func() { _ = f.Close() }()
	fi, e := f.Stat()
	if e != nil {
		return s, e
	}
	before, e := os.Lstat(m.path())
	if e != nil || !os.SameFile(fi, before) || !private(fi) || !fi.Mode().IsRegular() || fi.Size() > MaxStore {
		return s, fmt.Errorf("state_corrupt: terminal storage identity/budget")
	}
	b, e := io.ReadAll(io.LimitReader(f, MaxStore+1))
	if e != nil {
		return s, e
	}
	if len(b) > MaxStore {
		return s, fmt.Errorf("request_budget: terminal storage")
	}
	var env envelope
	if e = state.Decode(b, &env); e != nil {
		return s, e
	}
	s = env.Store
	if s.Schema != 1 || s.Binding != m.cfg.Binding || len(s.Key) != 32 || env.Checksum != checksum(s) || len(s.Records) > MaxTerminals {
		return s, fmt.Errorf("state_corrupt: terminal schema/binding/checksum")
	}
	seen := map[string]bool{}
	seenRequests := map[string]bool{}
	for _, r := range s.Records {
		if !ValidID(r.Identity.ID) || !ValidID(r.Identity.Epoch) || !ValidID(r.Identity.Nonce) || r.Identity.Binding != m.cfg.Runtime || !ValidID(r.RequestID) || !ValidID(r.Operation) || !knownGap(r.Gap) || len(r.Digest) != 64 || seen[r.Identity.ID] || seenRequests[r.RequestID] || r.Highwater < 0 || r.Highwater > MaxLog || len(r.Inputs) > MaxInputs {
			return s, fmt.Errorf("state_corrupt: terminal record")
		}
		switch r.State {
		case "creating", "running", "exited", "unavailable", "lost", "closing", "closed", "outcome_unknown":
		default:
			return s, fmt.Errorf("state_corrupt: terminal state")
		}
		seen[r.Identity.ID] = true
		seenRequests[r.RequestID] = true
		inputs := map[string]bool{}
		for _, i := range r.Inputs {
			if !ValidID(i.ID) || inputs[i.ID] || len(i.Digest) != 64 || !ValidID(i.Operation) || i.State != "accepted" && i.State != "sending" && i.State != "sent" && i.State != "outcome_unknown" {
				return s, fmt.Errorf("state_corrupt: input identity")
			}
			inputs[i.ID] = true
		}
	}
	return s, m.safe()
}
func (m *Manager) save(s store) error {
	if e := m.safe(); e != nil {
		return e
	}
	env := envelope{s, checksum(s)}
	b, e := json.Marshal(env)
	if e != nil {
		return e
	}
	if len(b) > MaxStore {
		return fmt.Errorf("request_budget: terminal storage exhausted")
	}
	return m.write(m.path(), env)
}
func (m *Manager) lock(ctx context.Context) (func(), error) {
	if e := m.safe(); e != nil {
		return nil, e
	}
	if e := os.MkdirAll(filepath.Dir(m.path()), 0700); e != nil {
		return nil, e
	}
	if e := m.safe(); e != nil {
		return nil, e
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return state.Lock(c, m.path()+".lock")
}
func fingerprint(key []byte, v any) string {
	b, _ := json.Marshal(v)
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
func result(r record, o Observation) Result {
	return Result{TerminalID: r.Identity.ID, Epoch: r.Identity.Epoch, State: o.State, OperationID: r.Operation, Orphan: r.State == "creating", Complete: o.Complete, Gap: o.Gap, ExitCode: o.ExitCode, Descendants: o.Descendants, EarliestCursor: cursor(r.Identity, 0), NextCursor: cursor(r.Identity, o.Offset), Data: o.Data}
}
func parseCursor(s string, id Identity) (int64, error) {
	if s == "" {
		return 0, nil
	}
	p := strings.Split(s, ":")
	if len(p) != 3 || p[0] != id.ID || p[1] != id.Epoch {
		return 0, fmt.Errorf("output_gap: cursor instance/epoch")
	}
	n, e := strconv.ParseInt(p[2], 10, 64)
	if e != nil || n < 0 || strconv.FormatInt(n, 10) != p[2] {
		return 0, fmt.Errorf("output_gap: invalid cursor")
	}
	return n, nil
}
func validate(op string, q Request) error {
	// Strict per-operation shapes prevent ignored fields from conveying false semantics.
	copyQ := q
	switch op {
	case "create":
		if !ValidID(q.RequestID) || len(q.Argv) == 0 || len(q.Argv) > 128 || q.Cols < 1 || q.Cols > 500 || q.Rows < 1 || q.Rows > 300 {
			return fmt.Errorf("request_budget: create identity/argv/dimensions")
		}
		for _, s := range q.Argv {
			if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
				return fmt.Errorf("unsupported: argv must be UTF-8 without NUL")
			}
		}
		if q.Argv[0] == "" {
			return fmt.Errorf("policy_denied: empty executable")
		}
		copyQ.RequestID = ""
		copyQ.Cwd = ""
		copyQ.Argv = nil
		copyQ.Cols = 0
		copyQ.Rows = 0
	case "write":
		if !ValidID(q.InputID) || len(q.Data) > MaxInput || len(q.Data) == 0 && q.Key == "" || len(q.Data) > 0 && q.Key != "" || q.Key != "" && q.Key != "Enter" {
			return fmt.Errorf("request_budget: input identity/data/key")
		}
		copyQ.InputID = ""
		copyQ.Data = nil
		copyQ.Key = ""
	case "read":
		if q.MaxBytes < 1 || q.MaxBytes > MaxRead {
			return fmt.Errorf("request_budget: read limit")
		}
		copyQ.Cursor = ""
		copyQ.MaxBytes = 0
	case "resize":
		if q.Cols < 1 || q.Cols > 500 || q.Rows < 1 || q.Rows > 300 {
			return fmt.Errorf("request_budget: terminal dimensions")
		}
		copyQ.Cols = 0
		copyQ.Rows = 0
	case "interrupt":
		if q.Signal != "" && q.Signal != "Ctrl-C" {
			return fmt.Errorf("unsupported: only Ctrl-C is supported")
		}
		copyQ.Signal = ""
	case "inspect", "attach", "close", "list":
	default:
		return fmt.Errorf("unsupported: terminal operation")
	}
	if op != "create" && op != "list" {
		if !ValidID(q.TerminalID) {
			return fmt.Errorf("policy_denied: invalid terminal_id")
		}
		copyQ.TerminalID = ""
	}
	b, _ := json.Marshal(copyQ)
	if string(b) != "{}" {
		return fmt.Errorf("policy_denied: unexpected terminal parameters")
	}
	b, _ = json.Marshal(q)
	if len(b) > MaxFrame {
		return fmt.Errorf("request_budget: terminal frame")
	}
	return nil
}

// Call is the sole public operation router. Each mutation is preceded by mandatory durable policy intent.
func (m *Manager) Call(ctx context.Context, op string, q Request) (any, error) {
	if e := validate(op, q); e != nil {
		return nil, e
	}
	if e := m.Ready(ctx); e != nil {
		return m.guardResult(ctx, op, q, e)
	}
	if op == "list" {
		s, e := m.load()
		if e != nil {
			return nil, e
		}
		out := []Result{}
		for _, r := range s.Records {
			out = append(out, result(r, saved(r)))
		}
		return out, nil
	}
	if op != "create" {
		if _, err := os.Lstat(m.path()); err != nil {
			return nil, err
		}
	}
	unlock, e := m.lock(ctx)
	if e != nil {
		return nil, e
	}
	defer unlock()
	if e = m.Ready(ctx); e != nil {
		return nil, e
	}
	s, e := m.load()
	if e != nil {
		return nil, e
	}
	if op == "create" {
		return m.create(ctx, &s, q)
	}
	var r *record
	for i := range s.Records {
		if s.Records[i].Identity.ID == q.TerminalID {
			r = &s.Records[i]
			break
		}
	}
	if r == nil {
		return nil, fmt.Errorf("resource_lost: unknown terminal")
	}
	var readOffset int64
	if op == "read" {
		readOffset, e = parseCursor(q.Cursor, r.Identity)
		if e != nil {
			return result(*r, saved(*r)), e
		}
	}
	if op == "write" {
		for _, i := range r.Inputs {
			if i.ID == q.InputID {
				v := result(*r, saved(*r))
				v.InputID = i.ID
				v.OperationID = i.Operation
				if fingerprint(s.Key, q) != i.Digest {
					return v, fmt.Errorf("policy_denied: input_id payload changed")
				}
				v.Accepted = i.State == "sent"
				if !v.Accepted {
					return v, policy.UnknownOutcome(i.Operation, fmt.Errorf("input may have been dispatched; replay refused"))
				}
				return v, nil
			}
		}
	}
	if r.State == "lost" || r.State == "closed" && op != "read" && op != "inspect" && op != "attach" {
		v := result(*r, saved(*r))
		if op == "inspect" || op == "attach" || op == "read" || op == "close" {
			return v, nil
		}
		return v, fmt.Errorf("resource_lost: terminal is final")
	}
	g := GuestRequest{Op: "inspect", Identity: r.Identity}
	o, e := m.dispatch(ctx, g)
	if e != nil {
		failed := m.observeError(r, e)
		return result(*r, failed), errors.Join(e, m.save(s))
	}
	if o.Offset < r.Highwater && o.Gap == "" {
		o.State = "lost"
		o.Gap = "log_truncated"
	}
	wasCreating := r.State == "creating"
	wasClosed := r.State == "closed"
	remember(r, o)
	if wasCreating && op == "attach" && o.State != "lost" {
		r.Identity = o.Identity
		r.State = o.State
	}
	if wasClosed {
		o.State = "closed"
	}
	v := result(*r, o)
	v.Orphan = wasCreating
	if e = m.save(s); e != nil {
		return v, e
	}
	if wasCreating && op != "inspect" && op != "read" && op != "attach" && op != "close" {
		return v, policy.UnknownOutcome(r.Operation, fmt.Errorf("attach the exact orphan before sending new effects"))
	}
	if wasCreating && op == "close" {
		r.Identity = o.Identity
	}
	if op == "inspect" || op == "attach" {
		return v, nil
	}
	if op == "read" {
		if o.State == "lost" || o.Gap == "log_missing" || o.Gap == "log_changed" || o.Gap == "log_truncated" {
			return v, nil
		}
		o, e = m.dispatch(ctx, GuestRequest{Op: "read", Identity: r.Identity, Offset: readOffset, Limit: q.MaxBytes})
		if e != nil {
			failed := m.observeError(r, e)
			return result(*r, failed), errors.Join(e, m.save(s))
		}
		remember(r, o)
		if wasClosed {
			o.State = "closed"
		}
		return result(*r, o), m.save(s)
	}
	if (o.State != "running" || o.Gap != "") && op != "close" {
		return v, fmt.Errorf("resource_lost: terminal is not writable (%s)", o.State)
	}
	if len(r.Inputs) >= MaxInputs && op == "write" {
		return v, fmt.Errorf("request_budget: input retention exhausted")
	}
	p := m.cfg.Policy.WithTargetCheck(m.Ready)
	intent, e := p.BeginGuest(ctx, "guest.terminal_"+op, struct {
		Identity Identity
		Request  Request
	}{r.Identity, q})
	if e != nil {
		return v, e
	}
	previous := r.State
	r.Operation = intent.ID
	if op == "write" {
		r.Inputs = append(r.Inputs, input{q.InputID, fingerprint(s.Key, q), "accepted", intent.ID})
	}
	if op == "close" {
		r.State = "closing"
	}
	if e = m.save(s); e != nil {
		_ = p.Finish(context.WithoutCancel(ctx), intent.ID, "failed")
		return v, e
	}
	if op == "write" {
		r.Inputs[len(r.Inputs)-1].State = "sending"
		if e = m.save(s); e != nil {
			_ = p.Finish(context.WithoutCancel(ctx), intent.ID, "failed")
			return v, e
		}
	}
	o, e = m.dispatch(ctx, GuestRequest{Op: op, Identity: r.Identity, Data: q.Data, Key: q.Key, Cols: q.Cols, Rows: q.Rows})
	outcome := "ok"
	if e != nil {
		outcome = "outcome_unknown"
	}
	finishErr := p.Finish(context.WithoutCancel(ctx), intent.ID, outcome)
	if finishErr != nil {
		outcome = "outcome_unknown"
		e = errors.Join(e, finishErr)
	}
	r.State = previous
	if op == "write" {
		r.Inputs[len(r.Inputs)-1].State = "sent"
		if outcome != "ok" {
			r.Inputs[len(r.Inputs)-1].State = "outcome_unknown"
		}
	}
	if op == "close" {
		r.Gap = o.Gap
		r.State = "closed"
		if outcome != "ok" {
			r.State = "outcome_unknown"
		}
	}
	if e != nil {
		failed := m.observeError(r, e)
		o.Gap = failed.Gap
	} else if op != "close" {
		remember(r, o)
	}
	v = result(*r, o)
	v.OperationID = intent.ID
	v.InputID = q.InputID
	v.Accepted = op == "write" && outcome == "ok"
	if se := m.save(s); se != nil {
		outcome = "outcome_unknown"
		e = errors.Join(e, se)
		v.Accepted = false
	}
	if outcome != "ok" {
		v.State = "outcome_unknown"
		return v, policy.UnknownOutcome(intent.ID, e)
	}
	return v, nil
}
func sameInstance(a, b Identity) bool {
	return a.ID == b.ID && a.Epoch == b.Epoch && a.Binding == b.Binding && a.Nonce == b.Nonce && (a.Server == "" || a == b)
}
func (m *Manager) create(ctx context.Context, s *store, q Request) (Result, error) {
	for _, r := range s.Records {
		if r.RequestID == q.RequestID {
			v := result(r, saved(r))
			if fingerprint(s.Key, q) != r.Digest {
				return v, fmt.Errorf("policy_denied: request_id payload changed")
			}
			if r.State == "creating" || r.State == "outcome_unknown" {
				return v, policy.UnknownOutcome(r.Operation, fmt.Errorf("create may have been dispatched; inspect exact terminal_id"))
			}
			return v, nil
		}
	}
	if len(s.Records) >= MaxTerminals {
		return Result{}, fmt.Errorf("request_budget: terminal retention exhausted")
	}
	if len(s.Key) == 0 {
		s.Key = make([]byte, 32)
		if _, e := rand.Read(s.Key); e != nil {
			return Result{}, e
		}
	}
	id, e := NewID()
	if e != nil {
		return Result{}, e
	}
	epoch, e := NewID()
	if e != nil {
		return Result{}, e
	}
	nonce, e := NewID()
	if e != nil {
		return Result{}, e
	}
	command, e := m.cfg.Launch(ctx, q.Cwd, q.Argv, id)
	if e != nil {
		return Result{}, e
	}
	r := record{Identity: Identity{ID: id, Epoch: epoch, Nonce: nonce, Binding: m.cfg.Runtime}, RequestID: q.RequestID, Digest: fingerprint(s.Key, q), State: "creating", Inputs: []input{}}
	frame, _ := json.Marshal(GuestRequest{Op: "create", Identity: r.Identity, Command: command, Cols: q.Cols, Rows: q.Rows})
	if len(frame) > MaxFrame {
		return Result{}, fmt.Errorf("request_budget: terminal launch frame")
	}
	p := m.cfg.Policy.WithTargetCheck(m.Ready)
	intent, e := p.BeginGuest(ctx, "guest.terminal_create", struct {
		Identity Identity
		Request  Request
	}{r.Identity, q})
	if e != nil {
		return Result{}, e
	}
	r.Operation = intent.ID
	s.Records = append(s.Records, r)
	if e = m.save(*s); e != nil {
		_ = p.Finish(context.WithoutCancel(ctx), intent.ID, "failed")
		return Result{}, e
	}
	o, e := m.dispatch(ctx, GuestRequest{Op: "create", Identity: r.Identity, Command: command, Cols: q.Cols, Rows: q.Rows})
	outcome := "ok"
	if e != nil {
		outcome = "outcome_unknown"
	} else if !sameInstance(r.Identity, o.Identity) {
		e = fmt.Errorf("identity_changed: created terminal")
		outcome = "outcome_unknown"
	}
	if fe := p.Finish(context.WithoutCancel(ctx), intent.ID, outcome); fe != nil {
		outcome = "outcome_unknown"
		e = errors.Join(e, fe)
	}
	if outcome == "ok" {
		r.Identity = o.Identity
		r.State = o.State
		s.Records[len(s.Records)-1] = r
		if se := m.save(*s); se != nil {
			outcome = "outcome_unknown"
			e = errors.Join(e, se)
		}
	}
	v := result(r, o)
	v.OperationID = intent.ID
	if outcome != "ok" {
		v.State = "outcome_unknown"
		return v, policy.UnknownOutcome(intent.ID, e)
	}
	return v, nil
}

// A confirmed immutable binding change loses the terminal; a transient check failure is unavailable.
func (m *Manager) guardResult(ctx context.Context, op string, q Request, cause error) (any, error) {
	if op == "create" || op == "list" || !ValidID(q.TerminalID) {
		return nil, cause
	}
	s, e := m.load()
	if e != nil {
		return nil, cause
	}
	for i := range s.Records {
		r := &s.Records[i]
		if r.Identity.ID != q.TerminalID {
			continue
		}
		status, gap := failureState(cause)
		v := result(*r, Observation{State: status, Offset: r.Highwater, Gap: gap})
		if status == "lost" {
			unlock, err := m.lock(ctx)
			if err == nil {
				defer unlock()
				current, err := m.load()
				if err == nil {
					for j := range current.Records {
						if current.Records[j].Identity.ID == q.TerminalID {
							current.Records[j].State = "lost"
							current.Records[j].Gap = gap
						}
					}
					_ = m.save(current)
				}
			}
		}
		if op == "write" {
			for _, i := range r.Inputs {
				if i.ID == q.InputID && fingerprint(s.Key, q) == i.Digest {
					v.InputID = i.ID
					v.OperationID = i.Operation
					v.Accepted = i.State == "sent"
					if !v.Accepted {
						return v, policy.UnknownOutcome(i.Operation, cause)
					}
					break
				}
			}
		}
		return v, cause
	}
	return nil, cause
}
