package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/runtime/docker"
)

type backend struct {
	mu      sync.Mutex
	objects map[string]Observation
	writes  int
	fail    bool
}

func fixture(t *testing.T) (*Manager, *backend) {
	t.Helper()
	base, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	b := policy.Binding{ProjectID: "terminal-test", RecordDigest: strings.Repeat("a", 64), Generation: 1}
	check := func(context.Context) error { return nil }
	paths := func() error { return nil }
	p := policy.New(base, b, check, paths)
	if e = p.Init(context.Background()); e != nil {
		t.Fatal(e)
	}
	back := &backend{objects: map[string]Observation{}}
	cfg := Config{Base: base, Binding: b, Runtime: strings.Repeat("b", 64), Policy: p, Check: check, Paths: paths, Launch: func(context.Context, string, []string, string) (string, error) { return "held-rooted-cwd", nil }}
	cfg.Dispatch = func(_ context.Context, q GuestRequest) (Observation, error) {
		back.mu.Lock()
		defer back.mu.Unlock()
		if q.Op == "create" {
			q.Identity.Server = "boot:123:456"
			q.Identity.Session = "$0"
			q.Identity.Pane = "%0"
			back.objects[q.Identity.ID] = Observation{Identity: q.Identity, State: "running"}
		}
		o := back.objects[q.Identity.ID]
		if q.Op == "write" {
			back.writes++
			if back.fail {
				return o, fmt.Errorf("response lost after dispatch")
			}
		}
		return o, nil
	}
	return New(cfg), back
}
func rid() string {
	s, e := NewID()
	if e != nil {
		panic(e)
	}
	return s
}
func createQ() Request {
	return Request{RequestID: rid(), Argv: []string{"python3", "-q"}, Cols: 80, Rows: 24}
}
func createTerminal(t *testing.T, m *Manager) Result {
	t.Helper()
	v, e := m.Call(context.Background(), "create", createQ())
	if e != nil {
		t.Fatal(e)
	}
	return v.(Result)
}
func TestPersistentInputAndConcurrentSerialization(t *testing.T) {
	m, b := fixture(t)
	r := createTerminal(t, m)
	q := Request{TerminalID: r.TerminalID, InputID: rid(), Data: []byte("private-terminal-input\x00\xff")}
	v, e := m.Call(context.Background(), "write", q)
	if e != nil || !v.(Result).Accepted {
		t.Fatal(v, e)
	}
	m = New(m.cfg) // complete Host manager restart, retaining only durable state
	v, e = m.Call(context.Background(), "write", q)
	if e != nil || !v.(Result).Accepted || b.writes != 1 {
		t.Fatal(v, e, b.writes)
	}
	q.Data = []byte("changed")
	if _, e = m.Call(context.Background(), "write", q); e == nil {
		t.Fatal("changed input accepted")
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := New(m.cfg).Call(context.Background(), "write", Request{TerminalID: r.TerminalID, InputID: rid(), Key: "Enter"})
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if b.writes != 11 {
		t.Fatal(b.writes)
	}
	for _, name := range []string{m.path(), filepath.Join(m.cfg.Base, "security", m.cfg.Binding.ProjectID+".json")} {
		data, e := os.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(data), "private-terminal-input") {
			t.Fatal("raw input persisted", name)
		}
	}
}
func TestDispatchedUnknownAndResultWriteFailureNeverReplay(t *testing.T) {
	for _, kind := range []string{"transport", "record-result"} {
		t.Run(kind, func(t *testing.T) {
			m, b := fixture(t)
			r := createTerminal(t, m)
			q := Request{TerminalID: r.TerminalID, InputID: rid(), Data: []byte("side-effect")}
			if kind == "transport" {
				b.fail = true
			} else {
				original := m.write
				m.write = func(p string, v any) error {
					b.mu.Lock()
					dispatched := b.writes > 0
					b.mu.Unlock()
					if dispatched {
						return errors.New("ENOSPC result")
					}
					return original(p, v)
				}
			}
			v, e := m.Call(context.Background(), "write", q)
			if e == nil || !strings.Contains(e.Error(), "outcome_unknown") || v.(Result).OperationID == "" {
				t.Fatal(v, e)
			}
			v, e = New(m.cfg).Call(context.Background(), "write", q)
			if e == nil || !strings.Contains(e.Error(), "outcome_unknown") || b.writes != 1 {
				t.Fatal(v, e, b.writes)
			}
		})
	}
}
func TestIntentRefusalAndCreatingOrphan(t *testing.T) {
	m, b := fixture(t)
	r := createTerminal(t, m)
	if e := m.cfg.Policy.Set(context.Background(), "guest", "deny"); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Call(context.Background(), "write", Request{TerminalID: r.TerminalID, InputID: rid(), Key: "Enter"}); e == nil || b.writes != 0 {
		t.Fatal(e, b.writes)
	}
	m, b = fixture(t)
	original := m.write
	count := 0
	m.write = func(p string, v any) error {
		count++
		if count == 2 {
			return errors.New("result lost")
		}
		return original(p, v)
	}
	q := createQ()
	v, e := m.Call(context.Background(), "create", q)
	if e == nil || v.(Result).TerminalID == "" {
		t.Fatal(v, e)
	}
	id := v.(Result).TerminalID
	m = New(m.cfg)
	if _, e = m.Call(context.Background(), "create", q); e == nil {
		t.Fatal("unknown create retried")
	}
	if len(b.objects) != 1 {
		t.Fatal("duplicate backend", b.objects)
	}
	v, e = m.Call(context.Background(), "attach", Request{TerminalID: id})
	if e != nil || !v.(Result).Orphan || v.(Result).State != "running" {
		t.Fatal(v, e)
	}
}
func TestGuardFailurePersistsForConnectionAndReportsLost(t *testing.T) {
	m, b := fixture(t)
	r := createTerminal(t, m)
	failed := false
	m.cfg.Check = func(context.Context) error {
		if failed {
			return docker.Fail("identity_changed", "runtime generation")
		}
		return nil
	}
	failed = true
	if _, e := m.Call(context.Background(), "inspect", Request{TerminalID: r.TerminalID}); e == nil {
		t.Fatal("guard accepted")
	}
	failed = false
	if _, e := m.Call(context.Background(), "write", Request{TerminalID: r.TerminalID, InputID: rid(), Key: "Enter"}); e == nil || b.writes != 0 {
		t.Fatal(e, b.writes)
	}
}
func TestStrictBudgetsCursorAndStorage(t *testing.T) {
	m, _ := fixture(t)
	r := createTerminal(t, m)
	for _, q := range []Request{{TerminalID: r.TerminalID, InputID: rid(), Data: make([]byte, MaxInput+1)}, {TerminalID: r.TerminalID, InputID: rid(), Data: []byte("x"), Key: "Enter"}, {TerminalID: r.TerminalID, InputID: rid(), Key: "C-c"}} {
		if _, e := m.Call(context.Background(), "write", q); e == nil {
			t.Fatal("invalid input accepted")
		}
	}
	for _, c := range []string{r.TerminalID + ":" + rid() + ":0", r.TerminalID + ":" + r.Epoch + ":-1", r.TerminalID + ":" + r.Epoch + ":01"} {
		if _, e := m.Call(context.Background(), "read", Request{TerminalID: r.TerminalID, Cursor: c, MaxBytes: 1}); e == nil {
			t.Fatal("invalid cursor", c)
		}
	}
	if e := os.Chmod(m.path(), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Call(context.Background(), "list", Request{}); e == nil {
		t.Fatal("public terminal record accepted")
	}
}

func TestRequestAndTerminalIDNamespacesDoNotCorruptState(t *testing.T) {
	m, _ := fixture(t)
	r := createTerminal(t, m)
	q := createQ()
	q.RequestID = r.TerminalID
	if _, e := m.Call(context.Background(), "create", q); e != nil {
		t.Fatal(e)
	}
	if _, e := New(m.cfg).Call(context.Background(), "list", Request{}); e != nil {
		t.Fatal(e)
	}
}

func TestStrictWireRequest(t *testing.T) {
	q, e := DecodeRequest([]byte(`{"terminal_id":"0123456789abcdef0123456789abcdef","max_bytes":123}`))
	if e != nil || q.MaxBytes != 123 || q.TerminalID == "" {
		t.Fatal(q, e)
	}
	for _, b := range [][]byte{[]byte("null"), []byte(`{"data":"YQ==","data":"Yg=="}`), []byte(`{"terminal_id":"x","command":"host-shell"}`), append([]byte(`{"cwd":"`), []byte{0xff, '"', '}'}...)} {
		if _, e := DecodeRequest(b); e == nil {
			t.Fatalf("accepted invalid request %q", b)
		}
	}
}

// Kill the actual writer process at both sides of dispatch. A stale lock is
// preserved; only this test's verified-dead child lock is explicitly removed.
func TestProcessCrashRetainsSendingAndNeverReplays(t *testing.T) {
	if base := os.Getenv("AGENTREACH_TERMINAL_CRASH_BASE"); base != "" {
		var q Request
		if e := json.Unmarshal([]byte(os.Getenv("AGENTREACH_TERMINAL_CRASH_REQUEST")), &q); e != nil {
			os.Exit(2)
		}
		b := policy.Binding{ProjectID: "terminal-test", RecordDigest: strings.Repeat("a", 64), Generation: 1}
		check := func(context.Context) error { return nil }
		p := policy.New(base, b, check, nil)
		var child *Manager
		cfg := Config{Base: base, Binding: b, Runtime: strings.Repeat("b", 64), Policy: p, Check: check, Paths: func() error { return nil }}
		cfg.Dispatch = func(_ context.Context, g GuestRequest) (Observation, error) {
			s, e := child.load()
			if e != nil {
				return Observation{}, e
			}
			o := Observation{Identity: s.Records[0].Identity, State: "running"}
			if g.Op == "write" {
				if os.Getenv("AGENTREACH_TERMINAL_CRASH_STAGE") == "after" {
					f, e := os.OpenFile(filepath.Join(base, "effect"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
					if e != nil {
						os.Exit(3)
					}
					if _, e = f.Write([]byte("x")); e != nil {
						os.Exit(3)
					}
					if e = f.Sync(); e != nil {
						os.Exit(3)
					}
					f.Close()
				}
				os.Exit(0)
			}
			return o, nil
		}
		child = New(cfg)
		_, _ = child.Call(context.Background(), "write", q)
		os.Exit(4)
	}
	for _, stage := range []string{"before", "after"} {
		t.Run(stage, func(t *testing.T) {
			m, b := fixture(t)
			r := createTerminal(t, m)
			q := Request{TerminalID: r.TerminalID, InputID: rid(), Data: []byte("crash-write")}
			encoded, _ := json.Marshal(q)
			c := exec.Command(os.Args[0], "-test.run=^TestProcessCrashRetainsSendingAndNeverReplays$")
			c.Env = append(os.Environ(), "AGENTREACH_TERMINAL_CRASH_BASE="+m.cfg.Base, "AGENTREACH_TERMINAL_CRASH_REQUEST="+string(encoded), "AGENTREACH_TERMINAL_CRASH_STAGE="+stage)
			if out, e := c.CombinedOutput(); e != nil {
				t.Fatal(e, string(out))
			}
			s, e := m.load()
			if e != nil || len(s.Records[0].Inputs) != 1 || s.Records[0].Inputs[0].State != "sending" {
				t.Fatal(s, e)
			}
			op := s.Records[0].Inputs[0].Operation
			ledger, e := m.cfg.Policy.Inspect()
			if e != nil {
				t.Fatal(e)
			}
			if ledger.Operations[len(ledger.Operations)-1].ID != op || ledger.Operations[len(ledger.Operations)-1].Outcome != "intent" {
				t.Fatal("lost durable intent")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if _, e = New(m.cfg).Call(ctx, "write", q); e == nil {
				t.Fatal("stale lock stolen")
			}
			if e = os.Remove(m.path() + ".lock"); e != nil {
				t.Fatal(e)
			} // exact isolated test lock; child Wait confirmed termination
			if _, e = New(m.cfg).Call(context.Background(), "write", q); e == nil || !strings.Contains(e.Error(), op) || b.writes != 0 {
				t.Fatal("crashed input replayed", e, b.writes)
			}
			actual, e := os.ReadFile(filepath.Join(m.cfg.Base, "effect"))
			if stage == "before" {
				if !os.IsNotExist(e) {
					t.Fatal(actual, e)
				}
			} else if e != nil || string(actual) != "x" {
				t.Fatal(actual, e)
			}
		})
	}
}

func TestAuditConfirmedDuplicateAfterClose(t *testing.T) {
	m, b := fixture(t)
	r := createTerminal(t, m)
	q := Request{TerminalID: r.TerminalID, InputID: rid(), Data: []byte("once")}
	first, e := m.Call(context.Background(), "write", q)
	if e != nil {
		t.Fatal(e)
	}
	original := m.cfg.Dispatch
	m.cfg.Dispatch = func(ctx context.Context, g GuestRequest) (Observation, error) {
		o, e := original(ctx, g)
		if g.Op == "close" {
			o.State = "closed"
		}
		return o, e
	}
	if _, e = m.Call(context.Background(), "close", Request{TerminalID: r.TerminalID}); e != nil {
		t.Fatal(e)
	}
	v, e := New(m.cfg).Call(context.Background(), "write", q)
	if e != nil || !v.(Result).Accepted || v.(Result).OperationID != first.(Result).OperationID || b.writes != 1 {
		t.Fatalf("confirmed duplicate after close: result=%+v err=%v writes=%d original_op=%s", v, e, b.writes, first.(Result).OperationID)
	}
}
func TestAuditBackendIdentityErrorLatchesConnection(t *testing.T) {
	m, _ := fixture(t)
	r := createTerminal(t, m)
	original := m.cfg.Dispatch
	failOnce := true
	m.cfg.Dispatch = func(ctx context.Context, g GuestRequest) (Observation, error) {
		if g.Op == "inspect" && failOnce {
			failOnce = false
			o, e := original(ctx, g)
			o.Identity.Nonce = rid()
			return o, e
		}
		return original(ctx, g)
	}
	if _, e := m.Call(context.Background(), "inspect", Request{TerminalID: r.TerminalID}); e == nil {
		t.Fatal("missing initial failure")
	}
	v, e := m.Call(context.Background(), "inspect", Request{TerminalID: r.TerminalID})
	if e == nil {
		t.Fatalf("same connection resumed after backend identity mismatch: %+v", v)
	}
}
func TestAuditLossGapPersistsAcrossInspection(t *testing.T) {
	m, b := fixture(t)
	r := createTerminal(t, m)
	b.mu.Lock()
	o := b.objects[r.TerminalID]
	o.State = "lost"
	o.Offset = 3
	o.Gap = "log_missing"
	b.objects[r.TerminalID] = o
	b.mu.Unlock()
	first, e := m.Call(context.Background(), "inspect", Request{TerminalID: r.TerminalID})
	if e != nil {
		t.Fatal(e)
	}
	v, e := New(m.cfg).Call(context.Background(), "read", Request{TerminalID: r.TerminalID, MaxBytes: 16})
	if e != nil || v.(Result).Gap != "log_missing" {
		t.Fatalf("lost read dropped original gap: first=%+v read=%+v err=%v", first, v, e)
	}
}

func TestAuditReadConfirmedLossCannotResume(t *testing.T) {
	m, b := fixture(t)
	r := createTerminal(t, m)
	b.mu.Lock()
	original := b.objects[r.TerminalID]
	lost := original
	lost.State = "lost"
	lost.Gap = "log_missing"
	b.objects[r.TerminalID] = lost
	b.mu.Unlock()
	v, e := m.Call(context.Background(), "read", Request{TerminalID: r.TerminalID, MaxBytes: 16})
	if e != nil || v.(Result).State != "lost" {
		t.Fatal(v, e)
	}
	b.mu.Lock()
	b.objects[r.TerminalID] = original
	b.mu.Unlock()
	resumed, e := New(m.cfg).Call(context.Background(), "write", Request{TerminalID: r.TerminalID, InputID: rid(), Key: "Enter"})
	if e == nil || b.writes != 0 {
		t.Fatalf("confirmed lost terminal resumed: lost=%+v resumed=%+v err=%v writes=%d", v, resumed, e, b.writes)
	}
}

func TestEveryObservationEntryPersistsLossBeforeRejecting(t *testing.T) {
	for _, op := range []string{"read", "write", "resize", "interrupt"} {
		t.Run(op, func(t *testing.T) {
			m, b := fixture(t)
			r := createTerminal(t, m)
			b.mu.Lock()
			healthy := b.objects[r.TerminalID]
			lost := healthy
			lost.State = "lost"
			lost.Gap = "log_missing"
			b.objects[r.TerminalID] = lost
			b.mu.Unlock()
			q := Request{TerminalID: r.TerminalID}
			switch op {
			case "read":
				q.MaxBytes = 16
			case "write":
				q.InputID = rid()
				q.Key = "Enter"
			case "resize":
				q.Cols = 80
				q.Rows = 24
			}
			v, _ := m.Call(context.Background(), op, q)
			if v.(Result).State != "lost" {
				t.Fatal(v)
			}
			b.mu.Lock()
			b.objects[r.TerminalID] = healthy
			b.mu.Unlock()
			if _, e := New(m.cfg).Call(context.Background(), "write", Request{TerminalID: r.TerminalID, InputID: rid(), Key: "Enter"}); e == nil || b.writes != 0 {
				t.Fatal("lost terminal revived", e, b.writes)
			}
			v, e := New(m.cfg).Call(context.Background(), "inspect", Request{TerminalID: r.TerminalID})
			if e != nil || v.(Result).Gap != "log_missing" {
				t.Fatal(v, e)
			}
			if _, e = New(m.cfg).Call(context.Background(), "read", Request{TerminalID: r.TerminalID, Cursor: r.TerminalID + ":" + rid() + ":0", MaxBytes: 1}); e == nil {
				t.Fatal("lost bypassed epoch validation")
			}
		})
	}
}
func TestUnknownAndChangedInputResultsSurviveClose(t *testing.T) {
	m, b := fixture(t)
	r := createTerminal(t, m)
	q := Request{TerminalID: r.TerminalID, InputID: rid(), Key: "Enter"}
	b.fail = true
	first, e := m.Call(context.Background(), "write", q)
	if e == nil {
		t.Fatal("expected unknown")
	}
	original := m.cfg.Dispatch
	m.cfg.Dispatch = func(ctx context.Context, g GuestRequest) (Observation, error) {
		o, e := original(ctx, g)
		if g.Op == "close" {
			o.State = "closed"
		}
		return o, e
	}
	if _, e = m.Call(context.Background(), "close", Request{TerminalID: r.TerminalID}); e != nil {
		t.Fatal(e)
	}
	v, e := New(m.cfg).Call(context.Background(), "write", q)
	if e == nil || !strings.Contains(e.Error(), "outcome_unknown") || v.(Result).OperationID != first.(Result).OperationID || b.writes != 1 {
		t.Fatal(v, e, b.writes)
	}
	q.Key = ""
	q.Data = []byte("changed")
	if _, e = New(m.cfg).Call(context.Background(), "write", q); e == nil || !strings.Contains(e.Error(), "payload changed") {
		t.Fatal(e)
	}
}

func TestDispatchedIdentityMismatchRetainsUnknownInputAcrossRetiredConnection(t *testing.T) {
	m, b := fixture(t)
	r := createTerminal(t, m)
	q := Request{TerminalID: r.TerminalID, InputID: rid(), Key: "Enter"}
	original := m.cfg.Dispatch
	bad := true
	m.cfg.Dispatch = func(ctx context.Context, g GuestRequest) (Observation, error) {
		o, e := original(ctx, g)
		if g.Op == "write" && bad {
			bad = false
			o.Identity.Nonce = rid()
		}
		return o, e
	}
	first, e := m.Call(context.Background(), "write", q)
	if e == nil || !strings.Contains(e.Error(), "outcome_unknown") || m.Failure() == nil {
		t.Fatal(first, e)
	}
	for _, connection := range []*Manager{m, New(m.cfg)} {
		v, e := connection.Call(context.Background(), "write", q)
		if e == nil || !strings.Contains(e.Error(), "outcome_unknown") || v.(Result).OperationID != first.(Result).OperationID || v.(Result).InputID != q.InputID || b.writes != 1 {
			t.Fatal(v, e, b.writes)
		}
	}
}
