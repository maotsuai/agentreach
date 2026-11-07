package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Store, *Stamp) {
	t.Helper()
	base, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	s := New(base, Binding{"test", strings.Repeat("a", 64), 1}, func(context.Context) error { return nil }, nil)
	n := &Stamp{"boot-test", int64(time.Hour), time.Now().UnixNano()}
	s.clock = func() (Stamp, error) { return *n, nil }
	if e = s.Init(context.Background()); e != nil {
		t.Fatal(e)
	}
	return s, n
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func TestGuestServiceScopeRetainsIdentityWithoutPayload(t *testing.T) {
	s, _ := fixture(t)
	id := strings.Repeat("b", 32)
	op, e := s.BeginGuestResource(context.Background(), "guest.service_start", map[string]string{"argv": "private-service-command"}, id, 2)
	must(t, e)
	if op.TargetID != id || op.Generation != 2 {
		t.Fatal(op)
	}
	v, e := s.Inspect()
	must(t, e)
	b, e := json.Marshal(v)
	must(t, e)
	if strings.Contains(string(b), "private-service-command") {
		t.Fatal("Guest service payload leaked")
	}
	_, e = s.BeginGuestResource(context.Background(), "guest.exec", "x", id, 1)
	want(t, e, "policy_denied")
}
func want(t *testing.T, e error, code string) {
	t.Helper()
	if e == nil || !strings.Contains(e.Error(), code) {
		t.Fatalf("want %s, got %v", code, e)
	}
}
func planned(t *testing.T, s *Store) (Pending, Approval) {
	t.Helper()
	must(t, s.Set(context.Background(), "open_url", "approval"))
	p, e := s.Plan(context.Background(), HostRequest{"open_url", "exposure", 1, HostParams{Path: "/index"}})
	must(t, e)
	a, e := s.Approve(context.Background(), p.ID, p.Digest, DefaultTTL)
	must(t, e)
	return p, a
}

func TestExactApprovalAndDurableConsumption(t *testing.T) {
	s, _ := fixture(t)
	p, a := planned(t, s)
	other, e := s.Plan(context.Background(), HostRequest{"open_url", "exposure", 2, HostParams{Path: "/other"}})
	must(t, e)
	_, _, e = s.BeginHost(context.Background(), other.ID, a.ID)
	want(t, e, "approval_mismatch")
	_, e = s.Approve(context.Background(), p.ID, other.Digest, DefaultTTL)
	want(t, e, "request_mismatch")
	op, r, e := s.BeginHost(context.Background(), p.ID, a.ID)
	must(t, e)
	if r != p.Request {
		t.Fatal("authorized payload changed")
	}
	v, e := s.Inspect()
	must(t, e)
	if !v.Approvals[0].Used || v.Operations[0].Outcome != "intent" {
		t.Fatal("consumption/intent not one authoritative record")
	}
	_, _, e = s.BeginHost(context.Background(), p.ID, a.ID)
	want(t, e, "request_consumed")
	must(t, s.Finish(context.Background(), op.ID, "ok"))
	_, _, e = s.BeginHost(context.Background(), p.ID, a.ID)
	want(t, e, "request_consumed")
	changed := *s
	changed.binding.Generation++
	_, e = changed.Inspect()
	want(t, e, "binding")
}

func TestClockRevocationAndPolicyRevision(t *testing.T) {
	for _, mode := range []string{"deadline", "tick_rollback", "wall_rollback", "boot", "clock_error", "revoked", "revision", "ttl"} {
		t.Run(mode, func(t *testing.T) {
			s, n := fixture(t)
			p, a := planned(t, s)
			code := "clock_invalid"
			switch mode {
			case "deadline":
				n.Tick = a.Deadline
				n.Wall++
				code = "approval_expired"
			case "tick_rollback":
				n.Tick--
			case "wall_rollback":
				n.Wall--
			case "boot":
				n.Boot = "new-boot"
			case "clock_error":
				s.clock = func() (Stamp, error) { return Stamp{}, io.ErrUnexpectedEOF }
				code = "clock_unavailable"
			case "revoked":
				must(t, s.Revoke(context.Background(), a.ID))
				code = "approval_revoked"
			case "revision":
				must(t, s.Set(context.Background(), "guest", "deny"))
				code = "plan_stale"
			case "ttl":
				_, e := s.Approve(context.Background(), p.ID, p.Digest, MaxTTL+1)
				want(t, e, "TTL")
				return
			}
			_, _, e := s.BeginHost(context.Background(), p.ID, a.ID)
			want(t, e, code)
		})
	}
}

func TestConcurrentSingleUse(t *testing.T) {
	s, _ := fixture(t)
	p, a := planned(t, s)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			clone := *s
			_, _, e := clone.BeginHost(context.Background(), p.ID, a.ID)
			if e == nil {
				wins.Add(1)
			} else if !strings.Contains(e.Error(), "request_consumed") {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("dispatch winners=%d", wins.Load())
	}
}

func TestIntentFailureAndResultUnknown(t *testing.T) {
	for _, stage := range []string{"create", "short_write", "file_sync", "rename", "directory_sync"} {
		t.Run(stage, func(t *testing.T) {
			s, _ := fixture(t)
			p, a := planned(t, s)
			if stage == "short_write" {
				s.write = func(f *os.File, b []byte) (int, error) { return f.Write(b[:len(b)/2]) }
			} else {
				s.fault = func(x string) error {
					if x == stage {
						return errors.New("injected ENOSPC/fsync")
					}
					return nil
				}
			}
			_, _, e := s.BeginHost(context.Background(), p.ID, a.ID)
			want(t, e, "audit_unavailable")
			s.write = nil
			s.fault = nil
			v, e := s.Inspect()
			must(t, e)
			if stage == "directory_sync" {
				if len(v.Operations) != 1 || !v.Approvals[0].Used {
					t.Fatal("post-rename intent missing")
				}
				_, _, e = s.BeginHost(context.Background(), p.ID, a.ID)
				want(t, e, "request_consumed")
			} else if len(v.Operations) != 0 || v.Approvals[0].Used {
				t.Fatal("precommit failure consumed approval")
			}
		})
	}
	s, _ := fixture(t)
	op, e := s.BeginGuest(context.Background(), "guest.exec", map[string]string{"command": "secret-canary", "env": "private-value"})
	must(t, e)
	s.fault = func(string) error { return io.ErrShortWrite }
	want(t, s.Finish(context.Background(), op.ID, "ok"), "outcome_unknown")
	s.fault = nil
	v, e := s.Inspect()
	must(t, e)
	if v.Operations[0].Outcome != "intent" {
		t.Fatal("unknown result lost")
	}
	b, e := os.ReadFile(s.path)
	must(t, e)
	if strings.Contains(string(b), "secret-canary") || strings.Contains(string(b), "private-value") {
		t.Fatal("raw Guest secret in ledger")
	}
}

func TestStrictStorageAndBudgets(t *testing.T) {
	for _, mode := range []string{"tail", "duplicate", "unknown", "symlink", "hardlink", "permissions", "directory_permissions", "new_schema", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := fixture(t)
			b, e := os.ReadFile(s.path)
			must(t, e)
			switch mode {
			case "tail":
				b = append(b, []byte("broken")...)
			case "duplicate":
				b = append([]byte(`{"schema":1,`), b[1:]...)
			case "unknown":
				b = append([]byte(`{"unexpected":true,`), b[1:]...)
			case "new_schema":
				b = []byte(strings.Replace(string(b), `"schema":1`, `"schema":2`, 1))
			case "symlink":
				must(t, os.Rename(s.path, s.path+".original"))
				must(t, os.Symlink(s.path+".original", s.path))
			case "hardlink":
				must(t, os.Link(s.path, s.path+".link"))
			case "permissions":
				must(t, os.Chmod(s.path, 0644))
			case "directory_permissions":
				must(t, os.Chmod(filepath.Dir(s.path), 0755))
			case "oversize":
				b = make([]byte, MaxLedger+1)
			}
			if mode != "symlink" && mode != "hardlink" && mode != "permissions" && mode != "directory_permissions" {
				must(t, os.WriteFile(s.path, b, 0600))
			}
			_, e = s.BeginGuest(context.Background(), "guest.exec", "body")
			want(t, e, "audit_unavailable")
		})
	}
	s, _ := fixture(t)
	_, e := s.BeginGuest(context.Background(), "guest.exec", strings.Repeat("x", MaxRequest))
	want(t, e, "request_too_large")
	l, e := s.load()
	must(t, e)
	for len(l.Operations) < MaxOperations {
		l.Operations = append(l.Operations, Operation{ID: newID(), Digest: strings.Repeat("b", 64), Capability: "guest.exec", Actor: "guest", Revision: 1, Outcome: "ok"})
	}
	must(t, s.save(l))
	_, e = s.BeginGuest(context.Background(), "guest.exec", "body")
	want(t, e, "budget")
}

func TestUnknownFieldsAndForbiddenCapabilities(t *testing.T) {
	for _, b := range []string{`null`, `{"capability":"open_url","argv":["sh"]}`, `{"capability":"a","Capability":"b"}`, `{} {}`} {
		var h HostRequest
		if VerifyEncoded([]byte(b), &h) == nil {
			t.Fatal(b)
		}
	}
	s, _ := fixture(t)
	for _, cap := range []string{"shell", "arbitrary_host_shell", "xcode", "open_url"} {
		_, e := s.Plan(context.Background(), HostRequest{cap, "e", 1, HostParams{}})
		want(t, e, "policy_denied")
	}
	for _, p := range []string{"https://x", "//x", "/%2fexample", "/x?secret=1", "/x#f", "/\\x", "/../x", "/%252f", "/a\nb"} {
		if _, e := RelativePath(p); e == nil {
			t.Fatal(p)
		}
	}
}

// The parent cross-built Darwin test also runs this executable in a new process,
// proving that public clock stamps and boot identity are process independent.
func TestNativeClockAcrossProcesses(t *testing.T) {
	if os.Getenv("REACH_CLOCK_CHILD") == "1" {
		n, e := NativeClock()
		if e != nil {
			t.Fatal(e)
		}
		b, _ := json.Marshal(n)
		fmt.Printf("STAMP:%s\n", b)
		return
	}
	a, e := NativeClock()
	if e != nil {
		t.Fatal(e)
	}
	self, e := os.Executable()
	must(t, e)
	c := exec.Command(self, "-test.run=^TestNativeClockAcrossProcesses$")
	c.Env = append(os.Environ(), "REACH_CLOCK_CHILD=1")
	b, e := c.CombinedOutput()
	must(t, e)
	var middle Stamp
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "STAMP:") {
			must(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "STAMP:")), &middle))
		}
	}
	z, e := NativeClock()
	must(t, e)
	if a.Boot == "" || middle.Boot != a.Boot || z.Boot != a.Boot || middle.Tick < a.Tick || z.Tick < middle.Tick {
		t.Fatal("clock did not span processes monotonically")
	}
}

func TestProcessCrashDoesNotReplayIntent(t *testing.T) {
	if base := os.Getenv("REACH_LEDGER_CRASH_CHILD"); base != "" {
		s := New(base, Binding{"test", strings.Repeat("a", 64), 1}, func(context.Context) error { return nil }, nil)
		_, e := s.BeginGuest(context.Background(), "guest.exec", "crash-body")
		if e != nil {
			t.Fatal(e)
		}
		os.Exit(71)
	}
	base, e := filepath.EvalSymlinks(t.TempDir())
	must(t, e)
	s := New(base, Binding{"test", strings.Repeat("a", 64), 1}, func(context.Context) error { return nil }, nil)
	must(t, s.Init(context.Background()))
	self, e := os.Executable()
	must(t, e)
	c := exec.Command(self, "-test.run=^TestProcessCrashDoesNotReplayIntent$")
	c.Env = append(os.Environ(), "REACH_LEDGER_CRASH_CHILD="+base)
	e = c.Run()
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.ExitCode() != 71 {
		t.Fatalf("crash child: %v", e)
	}
	v, e := s.Inspect()
	must(t, e)
	if len(v.Operations) != 1 || v.Operations[0].Outcome != "intent" {
		t.Fatal("crash intent not durable")
	}
}

func TestCrossProcessApprovalSingleDispatch(t *testing.T) {
	if base := os.Getenv("REACH_APPROVAL_CHILD"); base != "" {
		s := New(base, Binding{"test", strings.Repeat("a", 64), 1}, func(context.Context) error { return nil }, nil)
		_, _, e := s.BeginHost(context.Background(), os.Getenv("REACH_REQUEST_ID"), os.Getenv("REACH_APPROVAL_ID"))
		if e != nil {
			if strings.Contains(e.Error(), "request_consumed") {
				os.Exit(72)
			}
			t.Fatal(e)
		}
		f, e := os.OpenFile(filepath.Join(base, "dispatch-canary"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if e != nil {
			t.Fatal(e)
		}
		_, e = f.WriteString("dispatch\n")
		must(t, e)
		must(t, f.Sync())
		must(t, f.Close())
		return
	}
	base, e := filepath.EvalSymlinks(t.TempDir())
	must(t, e)
	s := New(base, Binding{"test", strings.Repeat("a", 64), 1}, func(context.Context) error { return nil }, nil)
	must(t, s.Init(context.Background()))
	p, a := planned(t, s)
	self, e := os.Executable()
	must(t, e)
	var wg sync.WaitGroup
	var wins atomic.Int32
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := exec.Command(self, "-test.run=^TestCrossProcessApprovalSingleDispatch$")
			c.Env = append(os.Environ(), "REACH_APPROVAL_CHILD="+base, "REACH_REQUEST_ID="+p.ID, "REACH_APPROVAL_ID="+a.ID)
			out, e := c.CombinedOutput()
			if e == nil {
				wins.Add(1)
				return
			}
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() != 72 {
				t.Errorf("child: %v %s", e, out)
			}
		}()
	}
	wg.Wait()
	b, e := os.ReadFile(filepath.Join(base, "dispatch-canary"))
	must(t, e)
	if wins.Load() != 1 || string(b) != "dispatch\n" {
		t.Fatalf("wins=%d bytes=%q", wins.Load(), b)
	}
	v, e := s.Inspect()
	must(t, e)
	if len(v.Operations) != 1 || v.Operations[0].Outcome != "intent" || !v.Approvals[0].Used {
		t.Fatal("cross-process unknown intent lost")
	}
}

func TestDeadlineRecheckedAfterRuntimeGuard(t *testing.T) {
	s, n := fixture(t)
	p, a := planned(t, s)
	s.check = func(context.Context) error { n.Tick = a.Deadline; return nil }
	_, _, e := s.BeginHost(context.Background(), p.ID, a.ID)
	want(t, e, "approval_expired")
	v, e := s.Inspect()
	must(t, e)
	if v.Approvals[0].Used || len(v.Operations) != 0 {
		t.Fatal("expired during guard was committed")
	}
}

func TestAllRetentionBudgetsAndUnconditionalAudit(t *testing.T) {
	for _, kind := range []string{"pending", "approval", "event"} {
		t.Run(kind, func(t *testing.T) {
			s, n := fixture(t)
			p, _ := planned(t, s)
			l, e := s.load()
			must(t, e)
			switch kind {
			case "pending":
				for len(l.Pending) < MaxPending {
					other := p
					other.ID = newID()
					l.Pending = append(l.Pending, other)
				}
			case "approval":
				for len(l.Approvals) < MaxApprovals {
					other := l.Approvals[0]
					other.ID = newID()
					l.Approvals = append(l.Approvals, other)
				}
			case "event":
				for len(l.Events) < MaxEvents {
					must(t, event(l, *n, Operation{}, "policy", "budget_fixture"))
				}
			}
			must(t, s.save(l))
			switch kind {
			case "pending":
				_, e = s.Plan(context.Background(), p.Request.Host)
			case "approval":
				_, e = s.Approve(context.Background(), p.ID, p.Digest, DefaultTTL)
			case "event":
				_, e = s.BeginGuest(context.Background(), "guest.exec", "body")
			}
			want(t, e, "budget")
		})
	}
	s, _ := fixture(t)
	t.Setenv("REACH_NO_AUDIT", "1")
	op, e := s.BeginGuest(context.Background(), "guest.exec", "still-journalled")
	must(t, e)
	must(t, s.Finish(context.Background(), op.ID, "ok"))
	v, e := s.Inspect()
	must(t, e)
	if len(v.Operations) != 1 || len(v.Events) != 3 {
		t.Fatal("security audit was disabled")
	}
}
