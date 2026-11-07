package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/orbstack"
)

func effectController(t *testing.T) (*Controller, project.Record, *atomic.Int32) {
	t.Helper()
	c, r, base := planFixture(t)
	c.policy = func(binding project.Record) (*policy.Store, error) {
		return policy.New(project.ControlBase(base, binding), policy.Binding{ProjectID: binding.Spec.ID, RecordDigest: project.Digest(binding), Generation: binding.Binding.Generation}, func(context.Context) error {
			got, e := project.Load(binding.Spec.ID)
			if e != nil {
				return e
			}
			if project.Digest(got) != project.Digest(binding) {
				return errors.New("plan_stale")
			}
			return nil
		}, func() error { return controlPaths(binding) }), nil
	}
	p, _ := c.policy(r)
	if e := p.Init(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := p.Set(context.Background(), "runtime_change", "approval"); e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	c.change = func(context.Context, project.Record, string, string) error { calls.Add(1); return nil }
	return c, r, &calls
}

func authorize(t *testing.T, c *Controller, r project.Record, q Request) (Plan, policy.Pending, policy.Approval) {
	t.Helper()
	ctx := context.Background()
	p, e := c.Plan(ctx, r.Spec.ID, q)
	if e != nil {
		t.Fatal(e)
	}
	pending, e := c.Request(ctx, r.Spec.ID, p)
	if e != nil {
		t.Fatalf("request %+v: %v", p, e)
	}
	a, e := c.Approve(ctx, r.Spec.ID, pending.ID, pending.Digest, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	return p, pending, a
}

func TestMigrationCrashBoundariesNeverReplay(t *testing.T) {
	for _, stage := range []string{"after_intent", "before_dispatch", "after_effect"} {
		t.Run(stage, func(t *testing.T) {
			c, r, calls := effectController(t)
			_, pending, a := authorize(t, c, r, Request{Action: "migrate"})
			c.fault = func(at string) error {
				if at == stage {
					return errors.New("injected crash boundary")
				}
				return nil
			}
			op, e := c.Apply(context.Background(), r.Spec.ID, pending.ID, a.ID)
			if e == nil || op.ID == "" || op.Outcome != "outcome_unknown" {
				t.Fatalf("lost intent %v %v", op, e)
			}
			got, e := project.Load(r.Spec.ID)
			if e != nil {
				t.Fatal(e)
			}
			want := r
			if stage == "after_effect" {
				want = *nextRecord(r, Plan{Report: Report{Request: Request{Action: "migrate"}}})
			}
			if project.Digest(got) != project.Digest(want) {
				t.Fatalf("wrong authoritative record %+v", got)
			}
			c.fault = nil
			repeat, e := c.Apply(context.Background(), r.Spec.ID, pending.ID, a.ID)
			if e == nil || repeat.ID != op.ID || calls.Load() != 0 {
				t.Fatalf("replayed/lost ID %+v %v calls=%d", repeat, e, calls.Load())
			}
			observed, e := c.observeJournal(got, "")
			if e != nil {
				t.Fatal(e)
			}
			if stage == "after_effect" && (len(observed) != 1 || observed[0].Authority != "candidate_record") {
				t.Fatalf("commit not identifiable: %+v", observed)
			}
			// Original ledger and key remain in their old private namespace.
			old, _ := c.policy(r)
			v, e := old.Inspect()
			if e != nil || len(v.Operations) != 1 || v.Operations[0].ID != op.ID {
				t.Fatalf("original intent lost %v %v", v, e)
			}
		})
	}
}

func TestConcurrentMigrationOneIntentOnePublication(t *testing.T) {
	c, r, calls := effectController(t)
	_, pending, a := authorize(t, c, r, Request{Action: "migrate"})
	var wg sync.WaitGroup
	type result struct {
		op policy.Operation
		e  error
	}
	ch := make(chan result, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			op, e := c.Apply(context.Background(), r.Spec.ID, pending.ID, a.ID)
			ch <- result{op, e}
		}()
	}
	wg.Wait()
	close(ch)
	var id string
	ok := 0
	for x := range ch {
		if x.e == nil {
			ok++
		}
		if x.op.ID != "" {
			if id != "" && id != x.op.ID {
				t.Fatal("multiple operation IDs")
			}
			id = x.op.ID
		}
	}
	got, e := project.Load(r.Spec.ID)
	if e != nil || ok != 1 || got.Binding.Revision != 2 || got.Binding.Generation != 1 || calls.Load() != 0 {
		t.Fatalf("CAS: successes=%d record=%+v calls=%d err=%v", ok, got, calls.Load(), e)
	}
	old, _ := c.policy(r)
	v, e := old.Inspect()
	if e != nil || len(v.Operations) != 1 {
		t.Fatalf("multiple intents %+v %v", v, e)
	}
}

func TestStalePlanAndApprovalProduceZeroDispatch(t *testing.T) {
	for _, scenario := range []string{"revision", "observation", "expiry", "boot"} {
		t.Run(scenario, func(t *testing.T) {
			c, r, calls := effectController(t)
			_, pending, a := authorize(t, c, r, Request{Action: "migrate"})
			switch scenario {
			case "revision":
				p, _ := c.policy(r)
				if e := p.Set(context.Background(), "runtime_change", "deny"); e != nil {
					t.Fatal(e)
				}
			case "observation":
				c.observe = func(context.Context, project.Record, string) orbstack.Inventory {
					return orbstack.Inventory{Problem: "unavailable"}
				}
			case "expiry":
				c.clock = func() (policy.Stamp, error) {
					return policy.Stamp{Boot: "boot", Tick: int64(2 * time.Hour), Wall: 1}, nil
				}
			case "boot":
				c.clock = func() (policy.Stamp, error) { return policy.Stamp{Boot: "new", Tick: int64(time.Hour), Wall: 1}, nil }
			}
			op, e := c.Apply(context.Background(), r.Spec.ID, pending.ID, a.ID)
			if e == nil || op.ID != "" || calls.Load() != 0 {
				t.Fatalf("stale action dispatched %+v %v", op, e)
			}
			got, _ := project.Load(r.Spec.ID)
			if project.Digest(got) != project.Digest(r) {
				t.Fatal("stale plan changed authority")
			}
		})
	}
}

func TestNewBindingDoesNotRedirectOldControlState(t *testing.T) {
	c, r, _ := effectController(t)
	oldSession := projectSession(r)
	if e := oldSession.Save(); e != nil {
		t.Fatal(e)
	}
	_, pending, a := authorize(t, c, r, Request{Action: "migrate"})
	if _, e := c.Apply(context.Background(), r.Spec.ID, pending.ID, a.ID); e != nil {
		t.Fatal(e)
	}
	next, e := project.Load(r.Spec.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = oldSession.ManagedProject(); e == nil {
		t.Fatal("old session adopted new record")
	}
	if oldSession.Name == projectSession(next).Name {
		t.Fatal("session overwritten")
	}
	p, _ := c.policy(next)
	if _, e = p.Inspect(); e == nil {
		t.Fatal("old policy/approvals inherited")
	}
	old, _ := c.policy(r)
	if _, e = old.Inspect(); e != nil {
		t.Fatal("old ledger not retained", e)
	}
	if !strings.Contains(project.ControlBase("/control", next), project.Digest(next)) {
		t.Fatal("new authorization not namespaced")
	}
}

func TestFilesystemWriteFailuresPreserveIntentAndAuthority(t *testing.T) {
	for _, boundary := range []string{"runtime_intent_write", "result_write"} {
		t.Run(boundary, func(t *testing.T) {
			c, r, calls := effectController(t)
			_, pending, a := authorize(t, c, r, Request{Action: "migrate"})
			base, _ := project.Home()
			target := filepath.Join(base, "runtime")
			at := "after_intent"
			if boundary == "result_write" {
				target = filepath.Join(base, "security")
				at = "after_effect"
			}
			c.fault = func(stage string) error {
				if stage == at {
					return os.Chmod(target, 0500)
				}
				return nil
			}
			defer func() { _ = os.Chmod(target, 0700) }()
			op, e := c.Apply(context.Background(), r.Spec.ID, pending.ID, a.ID)
			if e == nil || op.ID == "" || op.Outcome != "outcome_unknown" || calls.Load() != 0 {
				t.Fatalf("lost write failure %+v %v", op, e)
			}
			if e = os.Chmod(target, 0700); e != nil {
				t.Fatal(e)
			}
			c.fault = nil
			got, e := project.Load(r.Spec.ID)
			if e != nil {
				t.Fatal(e)
			}
			if boundary == "runtime_intent_write" && got.Spec.Schema != 1 {
				t.Fatal("published without runtime journal")
			}
			if boundary == "result_write" && got.Spec.Schema != 2 {
				t.Fatal("lost committed authority after result failure")
			}
			again, e := c.Apply(context.Background(), r.Spec.ID, pending.ID, a.ID)
			if e == nil || again.ID != op.ID || calls.Load() != 0 {
				t.Fatal("write failure replayed or lost identity", again, e)
			}
		})
	}
}

func TestCancellationAfterIntentDoesNotPublish(t *testing.T) {
	c, r, calls := effectController(t)
	_, pending, a := authorize(t, c, r, Request{Action: "migrate"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.fault = func(stage string) error {
		if stage == "after_intent" {
			cancel()
		}
		return nil
	}
	op, e := c.Apply(ctx, r.Spec.ID, pending.ID, a.ID)
	if e == nil || op.ID == "" || calls.Load() != 0 {
		t.Fatalf("cancellation %+v %v", op, e)
	}
	got, e := project.Load(r.Spec.ID)
	if e != nil || project.Digest(got) != project.Digest(r) {
		t.Fatal("published after cancellation")
	}
}

func TestPlanExpiresAfterIntentBeforeDispatch(t *testing.T) {
	c, r, calls := effectController(t)
	p, pending, a := authorize(t, c, r, Request{Action: "migrate"})
	c.fault = func(stage string) error {
		if stage == "before_dispatch" {
			c.clock = func() (policy.Stamp, error) { return policy.Stamp{Boot: p.Boot, Tick: p.Deadline, Wall: 1}, nil }
		}
		return nil
	}
	op, e := c.Apply(context.Background(), r.Spec.ID, pending.ID, a.ID)
	if e == nil || op.ID == "" || calls.Load() != 0 {
		t.Fatalf("expired dispatch %+v %v", op, e)
	}
	got, e := project.Load(r.Spec.ID)
	if e != nil || project.Digest(got) != project.Digest(r) {
		t.Fatal("expired plan published")
	}
}

func TestRestartRechecksGrantAfterPostStopObservation(t *testing.T) {
	c, r, _ := effectController(t)
	_, migration, approval := authorize(t, c, r, Request{Action: "migrate"})
	if _, err := c.Apply(context.Background(), r.Spec.ID, migration.ID, approval.ID); err != nil {
		t.Fatal(err)
	}
	r, err := project.Load(r.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	store, err := c.policy(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Set(ctx, "runtime_change", "approval"); err != nil {
		t.Fatal(err)
	}
	_, pending, grant := authorize(t, c, r, Request{Action: "restart"})
	observe := c.observe
	var changes []string
	c.change = func(_ context.Context, _ project.Record, id, action string) error {
		if id != r.Binding.ContainerID {
			t.Fatalf("unexpected container %q", id)
		}
		changes = append(changes, action)
		return nil
	}
	c.observe = func(ctx context.Context, current project.Record, id string) orbstack.Inventory {
		o := observe(ctx, current, id)
		if len(changes) == 1 {
			o.Bound.State = "exited"
			if err := store.Revoke(ctx, grant.ID); err != nil {
				t.Fatal(err)
			}
		}
		return o
	}
	op, err := c.Apply(ctx, r.Spec.ID, pending.ID, grant.ID)
	if err == nil || !strings.Contains(err.Error(), "approval_expired_or_revoked") || op.ID == "" || op.Outcome != "outcome_unknown" || len(changes) != 1 || changes[0] != "stop" {
		t.Fatalf("post-stop revocation: op=%+v err=%v changes=%v", op, err, changes)
	}
	again, err := c.Apply(ctx, r.Spec.ID, pending.ID, grant.ID)
	if err == nil || again.ID != op.ID || len(changes) != 1 {
		t.Fatalf("restart replay: op=%+v err=%v changes=%v", again, err, changes)
	}
}

func TestRecoveryBootstrapOnUnavailableNewBinding(t *testing.T) {
	c, r, _ := effectController(t)
	ctx := context.Background()
	_, migration, approval := authorize(t, c, r, Request{Action: "migrate"})
	if _, err := c.Apply(ctx, r.Spec.ID, migration.ID, approval.ID); err != nil {
		t.Fatal(err)
	}
	next, err := project.Load(r.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	observe := c.observe
	c.observe = func(ctx context.Context, r project.Record, id string) orbstack.Inventory {
		o := observe(ctx, r, id)
		o.Bound.State = "exited"
		return o
	}
	if err = c.UpdatePolicy(ctx, next.Spec.ID, "init"); err != nil {
		t.Fatal(err)
	}
	store, err := c.policy(next)
	if err != nil {
		t.Fatal(err)
	}
	view, err := store.Inspect()
	if err != nil || view.GuestAllow || len(view.Host) != 0 || len(view.Approvals) != 0 {
		t.Fatalf("recovery initialized with inherited or default grants: %+v %v", view, err)
	}
	if err = c.UpdatePolicy(ctx, next.Spec.ID, "init"); err == nil {
		t.Fatal("recovery reset an existing policy")
	}
	if err = c.UpdatePolicy(ctx, next.Spec.ID, "approval"); err != nil {
		t.Fatal(err)
	}
	_, pending, grant := authorize(t, c, next, Request{Action: "start"})
	if op, err := c.Apply(ctx, next.Spec.ID, pending.ID, grant.ID); err != nil || op.Outcome != "ok" {
		t.Fatalf("denied exact recovery after bootstrap: %+v %v", op, err)
	}
}

func TestDurablePolicyResultWinsOverUnwrittenRecoveryReceipt(t *testing.T) {
	c, r, _ := effectController(t)
	ctx := context.Background()
	_, pending, grant := authorize(t, c, r, Request{Action: "migrate"})
	c.fault = func(stage string) error {
		if stage == "after_result" {
			return errors.New("interrupted before recovery receipt")
		}
		return nil
	}
	op, err := c.Apply(ctx, r.Spec.ID, pending.ID, grant.ID)
	if err == nil || op.ID == "" || op.Outcome != "ok" {
		t.Fatalf("lost the durable result: %+v %v", op, err)
	}
	again, err := c.Apply(ctx, r.Spec.ID, pending.ID, grant.ID)
	if err == nil || again != op {
		t.Fatalf("duplicate lost the original result: %+v %v", again, err)
	}
	next, err := project.Load(r.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := c.observeJournal(next, "")
	if err != nil || len(journal) != 1 || journal[0].OperationID != op.ID || journal[0].Outcome != "ok" || journal[0].Authority != "candidate_record" {
		t.Fatalf("reconciliation lost the durable result: %+v %v", journal, err)
	}
}
