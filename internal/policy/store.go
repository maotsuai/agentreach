package policy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/state"
)

var errUninitialized = errors.New("policy_unavailable: operator must run policy init")

// Store owns one project ledger and its bounded cross-process lock.
type Store struct {
	path    string
	binding Binding
	clock   Clock
	check   func(context.Context) error
	paths   func() error
	// Failure seams are private to package tests, never reachable by CLI/env/MCP.
	fault func(string) error
	write func(*os.File, []byte) (int, error)
}

// New binds storage to the caller's immutable project and trusted checks.
func New(base string, b Binding, check func(context.Context) error, paths func() error) *Store {
	return &Store{path: filepath.Join(base, "security", b.ProjectID+".json"), binding: b, clock: NativeClock, check: check, paths: paths}
}

// CheckTarget runs the trusted runtime/identity guard, independently of policy
// decisions and ledger/clock failures. Connection adapters own its lifetime.
func (s *Store) CheckTarget(ctx context.Context) error { return s.check(ctx) }

// WithTargetCheck gives a trusted adapter its own guard while retaining the
// original immutable binding and authoritative store. It changes no state.
func (s *Store) WithTargetCheck(check func(context.Context) error) *Store {
	copyStore := *s
	copyStore.check = check
	return &copyStore
}

func (s *Store) safe() error {
	if !targetPattern.MatchString(s.binding.ProjectID) || !digestPattern.MatchString(s.binding.RecordDigest) || s.binding.Generation == 0 {
		return fail("policy_denied: invalid binding")
	}
	if s.paths != nil {
		if e := s.paths(); e != nil {
			return e
		}
	}
	if e := hostpath.Canonical(s.path, true); e != nil {
		return fmt.Errorf("audit_unavailable: %w", e)
	}
	if e := hostpath.PrivateFile(s.path); e != nil {
		return fmt.Errorf("audit_unavailable: %w", e)
	}
	if fi, e := os.Stat(filepath.Dir(s.path)); e == nil {
		if !fi.IsDir() || !private(fi) {
			return fail("audit_unavailable: unsafe ledger directory permissions/owner")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	return nil
}

type envelope struct {
	Schema   int    `json:"schema"`
	Ledger   Ledger `json:"ledger"`
	Checksum string `json:"checksum"`
}

func (s *Store) load() (*Ledger, error) {
	if e := s.safe(); e != nil {
		return nil, e
	}
	before, e := os.Lstat(s.path)
	if os.IsNotExist(e) {
		return nil, errUninitialized
	}
	if e != nil {
		return nil, e
	}
	if !private(before) || !before.Mode().IsRegular() || before.Size() > MaxLedger {
		return nil, fail("audit_unavailable: unsafe ledger file")
	}
	f, e := os.Open(s.path)
	if e != nil {
		return nil, e
	}
	defer func() { _ = f.Close() }()
	opened, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !os.SameFile(before, opened) || !private(opened) {
		return nil, fail("audit_unavailable: ledger identity changed")
	}
	b, e := io.ReadAll(io.LimitReader(f, MaxLedger+1))
	if e != nil {
		return nil, e
	}
	if len(b) > MaxLedger {
		return nil, fail("audit_unavailable: ledger budget")
	}
	after, e := os.Lstat(s.path)
	if e != nil || !os.SameFile(opened, after) {
		return nil, fail("audit_unavailable: ledger identity changed")
	}
	if e = s.safe(); e != nil {
		return nil, e
	}
	var env envelope
	if e = state.Decode(b, &env); e != nil {
		return nil, fmt.Errorf("audit_unavailable: corrupt ledger: %w", e)
	}
	l := &env.Ledger
	if env.Schema != 1 || env.Checksum != digest(l) {
		return nil, fail("audit_unavailable: ledger integrity")
	}
	if e = s.validate(l); e != nil {
		return nil, e
	}
	return l, nil
}

func (s *Store) validate(l *Ledger) error {
	if l.Schema != 1 || l.Binding != s.binding || l.Revision == 0 || l.Host == nil || len(l.Key) != 32 || len(l.Pending) > MaxPending || len(l.Approvals) > MaxApprovals || len(l.Operations) > MaxOperations || len(l.Events) > MaxEvents || l.Highwater.Boot == "" || l.Highwater.Tick < 0 || l.Highwater.Wall <= 0 {
		return fail("audit_unavailable: invalid ledger schema/binding/budget")
	}
	for k, v := range l.Host {
		if !hostCapability(k) || v != "deny" && v != "allow" && v != "approval" {
			return fail("audit_unavailable: invalid host policy")
		}
	}
	seen := map[string]bool{}
	for _, p := range l.Pending {
		if !idPattern.MatchString(p.ID) || seen[p.ID] || p.Request.Binding != l.Binding || p.Request.PolicyRevision == 0 || p.Request.PolicyRevision > l.Revision || p.Digest != digest(p.Request) {
			return fail("audit_unavailable: invalid pending request")
		}
		if _, _, e := normalise(p.Request.Host); e != nil {
			return fail("audit_unavailable: invalid pending parameters")
		}
		seen[p.ID] = true
	}
	for _, a := range l.Approvals {
		if !idPattern.MatchString(a.ID) || seen[a.ID] || !idPattern.MatchString(a.RequestID) || a.Revision == 0 || a.Revision > l.Revision || a.Issued.Boot == "" || a.Issued.Tick < 0 || a.Issued.Wall <= 0 || a.Deadline <= a.Issued.Tick || a.Deadline-a.Issued.Tick > int64(MaxTTL) {
			return fail("audit_unavailable: invalid approval")
		}
		p := findPending(l, a.RequestID)
		if p == nil || p.Digest != a.Digest || p.Request.PolicyRevision != a.Revision {
			return fail("audit_unavailable: detached approval")
		}
		seen[a.ID] = true
	}
	for _, o := range l.Operations {
		if !idPattern.MatchString(o.ID) || seen[o.ID] || len(o.Digest) != 64 || o.Revision == 0 || o.Revision > l.Revision || o.Capability == "" || o.Actor != "guest" && o.Actor != "model" || o.Outcome != "intent" && o.Outcome != "ok" && o.Outcome != "failed" && o.Outcome != "outcome_unknown" {
			return fail("audit_unavailable: invalid operation")
		}
		seen[o.ID] = true
	}
	for i, e := range l.Events {
		if e.Schema != 1 || e.Binding != l.Binding || e.Sequence != uint64(i+1) || !idPattern.MatchString(e.ID) || seen[e.ID] || e.Time.Boot == "" || e.Time.Tick < 0 || e.PolicyRevision == 0 || e.PolicyRevision > l.Revision {
			return fail("audit_unavailable: invalid event sequence")
		}
		seen[e.ID] = true
	}
	return nil
}

func (s *Store) inject(stage string) error {
	if s.fault != nil {
		return s.fault(stage)
	}
	return nil
}
func (s *Store) save(l *Ledger) error {
	if e := s.safe(); e != nil {
		return e
	}
	stamp, e := s.sample(l)
	if e != nil {
		return e
	}
	l.Highwater = stamp
	if e := s.validate(l); e != nil {
		return e
	}
	b, e := json.Marshal(envelope{1, *l, digest(l)})
	if e != nil {
		return e
	}
	if len(b)+1 > MaxLedger {
		return fail("audit_unavailable: ledger budget exhausted")
	}
	if e = s.inject("create"); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(s.path), ".ledger-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer func() { _ = f.Close() }()
	if e = f.Chmod(0o600); e != nil {
		return e
	}
	w := s.write
	if w == nil {
		w = func(f *os.File, b []byte) (int, error) { return f.Write(b) }
	}
	b = append(b, '\n')
	n, e := w(f, b)
	if e != nil {
		return e
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	if e = s.inject("file_sync"); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = s.safe(); e != nil {
		return e
	}
	if e = s.inject("rename"); e != nil {
		return e
	}
	if e = os.Rename(tmp, s.path); e != nil {
		return e
	}
	if e = s.inject("directory_sync"); e != nil {
		return e
	}
	return syncDir(filepath.Dir(s.path))
}

func (s *Store) locked(ctx context.Context, fn func() error) error {
	if e := s.safe(); e != nil {
		return e
	}
	if _, e := os.Stat(filepath.Dir(s.path)); os.IsNotExist(e) {
		return fail("policy_unavailable: operator must run policy init")
	} else if e != nil {
		return e
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	u, e := state.Lock(c, s.path+".lock")
	if e != nil {
		return fmt.Errorf("audit_unavailable: %w", e)
	}
	defer u()
	return fn()
}

func (s *Store) sample(l *Ledger) (Stamp, error) {
	n, e := s.clock()
	if e != nil {
		return n, fmt.Errorf("clock_unavailable: %w", e)
	}
	if n.Boot == "" || n.Tick < 0 || n.Wall <= 0 {
		return n, fail("clock_unavailable")
	}
	if l != nil && (n.Boot != l.Highwater.Boot || n.Tick < l.Highwater.Tick || n.Wall < l.Highwater.Wall) {
		return n, fail("clock_invalid: boot changed or observed clock rollback")
	}
	return n, nil
}

// Init is the explicit operator-only policy bootstrap.
func (s *Store) Init(ctx context.Context) error {
	return s.init(ctx, true)
}

// InitRecovery bootstraps an empty policy with Guest and Host effects denied.
func (s *Store) InitRecovery(ctx context.Context) error {
	return s.init(ctx, false)
}

func (s *Store) init(ctx context.Context, guestAllow bool) error {
	if e := s.safe(); e != nil {
		return e
	}
	if e := s.check(ctx); e != nil {
		return e
	}
	if e := os.MkdirAll(filepath.Dir(s.path), 0o700); e != nil {
		return e
	}
	return s.locked(ctx, func() error {
		if _, e := os.Lstat(s.path); e == nil {
			return fail("policy_exists")
		} else if !os.IsNotExist(e) {
			return e
		}
		n, e := s.sample(nil)
		if e != nil {
			return e
		}
		key := make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return e
		}
		l := &Ledger{Schema: 1, Binding: s.binding, Revision: 1, GuestAllow: guestAllow, Host: map[string]string{}, Key: key, Highwater: n}
		if e = event(l, n, Operation{}, "policy", "initialized"); e != nil {
			return e
		}
		return s.save(l)
	})
}

func (s *Store) transaction(ctx context.Context, fn func(*Ledger, Stamp) error) error {
	return s.locked(ctx, func() error {
		l, e := s.load()
		if e != nil {
			return e
		}
		n, e := s.sample(l)
		if e != nil {
			return e
		}
		l.Highwater = n
		if e = fn(l, n); e != nil {
			return e
		}
		if e = s.save(l); e != nil {
			return fmt.Errorf("audit_unavailable: %w", e)
		}
		return nil
	})
}

func event(l *Ledger, n Stamp, o Operation, phase, reason string) error {
	if o.Actor == "" {
		o.Actor = "operator"
	}
	if o.Capability == "" {
		o.Capability = "policy." + phase
	}
	if o.Revision == 0 {
		o.Revision = l.Revision
	}
	if o.TargetID == "" {
		o.TargetID = l.Binding.ProjectID
	}
	if o.Generation == 0 {
		o.Generation = l.Binding.Generation
	}
	reserved := 0
	for _, op := range l.Operations {
		if op.Outcome == "intent" {
			reserved++
		}
	}
	if len(l.Events)+reserved >= MaxEvents {
		return fail("audit_unavailable: event budget exhausted")
	}
	l.Events = append(l.Events, Event{1, newID(), uint64(len(l.Events) + 1), n, l.Binding, l.Revision, o, phase, reason})
	return nil
}

// Inspect reads existing state without creating paths or exposing its key.
func (s *Store) Inspect() (View, error) {
	l, e := s.load()
	if e != nil {
		return View{}, e
	}
	return view(l), nil
}

// InspectOptional distinguishes an uninitialized ledger from an invalid one.
// Like Inspect, it never bootstraps policy or checks the running workspace.
func (s *Store) InspectOptional() (View, error) {
	v, err := s.Inspect()
	if errors.Is(err, errUninitialized) {
		return View{}, nil
	}
	return v, err
}

// Set advances policy revision and invalidates all earlier plans.
func (s *Store) Set(ctx context.Context, capability, decision string) error {
	if capability != "guest" && !hostCapability(capability) || decision != "deny" && decision != "allow" && decision != "approval" || capability == "guest" && decision == "approval" {
		return fail("policy_denied: invalid policy rule")
	}
	return s.transaction(ctx, func(l *Ledger, n Stamp) error {
		if e := s.check(ctx); e != nil {
			return e
		}
		l.Revision++
		if capability == "guest" {
			l.GuestAllow = decision == "allow"
		} else {
			l.Host[capability] = decision
		}
		return event(l, n, Operation{}, "policy", "rule_changed")
	})
}

func findPending(l *Ledger, id string) *Pending {
	for i := range l.Pending {
		if l.Pending[i].ID == id {
			return &l.Pending[i]
		}
	}
	return nil
}
func findApproval(l *Ledger, id string) *Approval {
	for i := range l.Approvals {
		if l.Approvals[i].ID == id {
			return &l.Approvals[i]
		}
	}
	return nil
}

// VerifyEncoded provides strict, bounded request decoding for CLI and MCP.
func VerifyEncoded(b []byte, v any) error {
	if len(b) > MaxRequest {
		return fail("request_too_large")
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return fail("policy_denied: null request")
	}
	return state.Decode(b, v)
}
