package recovery

import (
	"context"
	"fmt"
	"time"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/service"
)

func (c *Controller) inject(stage string) error {
	if c.fault != nil {
		return c.fault(stage)
	}
	return nil
}

func (c *Controller) verify(ctx context.Context, r project.Record, p Plan, ignore string) error {
	if p.Schema != 1 || p.ProjectID != r.Spec.ID || p.Digest != service.Digest(p.Report) || p.ExpectedDigest != project.Digest(r) || p.Revision != r.Binding.Revision || p.Request.Action == "reconcile" {
		return fmt.Errorf("plan_stale: recovery plan does not match the current authorization")
	}
	if err := validateRequest(p.Request); err != nil {
		return err
	}
	if err := c.verifyTime(p); err != nil {
		return err
	}
	fresh, err := c.plan(ctx, r, p.Request, ignore, p.WindowStart)
	if err != nil {
		return err
	}
	if fresh.Digest != p.Digest || !fresh.Eligible {
		return fmt.Errorf("plan_stale: recovery observations changed or action is ineligible")
	}
	if p.Control.PolicyRevision == 0 || p.Control.RuntimePolicy != "approval" {
		return fmt.Errorf("policy_denied: runtime_change must require exact operator approval")
	}
	if p.Request.Action != "migrate" && r.Spec.Schema != project.SchemaV2 {
		return fmt.Errorf("policy_denied: explicitly migrate the project to schema v2 first")
	}
	return nil
}

func (c *Controller) verifyTime(p Plan) error {
	n, err := c.clock()
	if err != nil || p.Boot != n.Boot || n.Tick < p.WindowStart || n.Tick >= p.Deadline || p.Deadline-p.WindowStart != int64(planWindow) {
		return fmt.Errorf("plan_stale: recovery plan expired or boot changed")
	}
	return nil
}

func nextRecord(r project.Record, p Plan) *project.Record {
	switch p.Request.Action {
	case "migrate":
		next := r
		next.Spec.Schema, next.Binding.Schema = project.SchemaV2, project.SchemaV2
		next.Binding.Revision++
		return &next
	case "rebind":
		next := r
		next.Binding.Revision++
		next.Binding.Generation++
		next.Binding.ContainerID = p.Observed.Candidate.ContainerID
		next.Binding.StartedAt = p.Observed.Candidate.StartedAt
		next.Binding.SnapshotSHA256 = p.Observed.Candidate.Snapshot
		next.Spec.Container = next.Binding.ContainerID
		return &next
	default:
		return nil
	}
}

func runtimeScope(p Plan) policy.HostRequest {
	return policy.HostRequest{Capability: "runtime_change", TargetID: p.ProjectID, Generation: p.Desired.Generation,
		Params: policy.HostParams{Operation: p.Request.Action, SpecDigest: p.Digest}}
}

func matchesRuntimeRequest(req policy.Request, p Plan) bool {
	if req.Binding != (policy.Binding{ProjectID: p.ProjectID, RecordDigest: p.ExpectedDigest, Generation: p.Desired.Generation}) ||
		req.PolicyRevision != p.Control.PolicyRevision || req.Scope != p.ProjectID {
		return false
	}
	want := runtimeScope(p)
	if p.Request.Action == "migrate" && req.Effect == "publish_project_schema_v2" {
		want.Params.Operation = "restart"
		return req.Host == want
	}
	return req.Effect == "explicit_runtime_recovery" && req.Host == want
}

// Request persists a previously read-only plan and creates a P2 pending request.
// Neither step dispatches a runtime effect or grants approval.
func (c *Controller) Request(ctx context.Context, id string, p Plan) (policy.Pending, error) {
	var out policy.Pending
	r, err := project.Load(id)
	if err != nil {
		return out, err
	}
	err = project.WithCAS(ctx, id, p.ExpectedDigest, p.Revision, func(_ *project.Locked) error {
		if e := c.verify(ctx, r, p, ""); e != nil {
			return e
		}
		f, e := journalFile(r)
		if e != nil {
			return e
		}
		unlock, e := f.Lock(ctx)
		if e != nil {
			return e
		}
		defer unlock()
		j, e := readJournal(f, id)
		if e != nil {
			return e
		}
		i := -1
		for n, entry := range j.Entries {
			if entry.Plan.Digest == p.Digest {
				i = n
				break
			}
		}
		if i < 0 {
			if len(j.Entries) >= maxEntries {
				return fmt.Errorf("request_budget: runtime journal exhausted")
			}
			j.Entries = append(j.Entries, Entry{Plan: p, Before: r, After: nextRecord(r, p), Phase: "planned"})
			i = len(j.Entries) - 1
			if e = f.Save(j); e != nil {
				return e
			}
		}
		if j.Entries[i].Pending != nil {
			out = *j.Entries[i].Pending
			return nil
		}
		store, e := c.policy(r)
		if e != nil {
			return e
		}
		view, e := store.Inspect()
		if e != nil {
			return e
		}
		for _, pending := range view.Pending {
			if matchesRuntimeRequest(pending.Request, p) {
				out = pending
				break
			}
		}
		if out.ID == "" {
			out, e = store.PlanRuntime(ctx, runtimeScope(p), p.Control.PolicyRevision)
			if e != nil {
				return e
			}
		}
		j.Entries[i].Pending = &out
		return f.Save(j)
	})
	return out, err
}

func (c *Controller) previous(r project.Record, requestID string) (policy.Operation, bool, error) {
	f, err := journalFile(r)
	if err != nil {
		return policy.Operation{}, false, err
	}
	j, err := readJournal(f, r.Spec.ID)
	if err != nil {
		return policy.Operation{}, false, err
	}
	for _, entry := range j.Entries {
		if entry.Pending == nil || entry.Pending.ID != requestID {
			continue
		}
		// A crash may occur between the durable P2 intent and the optional
		// resource receipt, or after P2 saves the result but before the
		// receipt is updated. The original ledger owns the operation.
		p, e := c.policy(entry.Before)
		if e != nil {
			return unconfirmedReceipt(entry), false, e
		}
		v, e := p.Inspect()
		if e != nil {
			return unconfirmedReceipt(entry), false, e
		}
		op, e := journalOperation(entry, v)
		if e != nil {
			return unconfirmedReceipt(entry), false, e
		}
		if op.ID != "" {
			return op, true, nil
		}
	}
	return policy.Operation{}, false, nil
}

func consumed(op policy.Operation) (policy.Operation, error) {
	if op.Outcome == "intent" || op.Outcome == "outcome_unknown" {
		op.Outcome = "outcome_unknown"
		return op, policy.UnknownOutcome(op.ID, fmt.Errorf("request_consumed: inspect original runtime operation; never replay"))
	}
	return op, fmt.Errorf("request_consumed: original operation %s", op.ID)
}

// Executor narrows the Host interface to one operator-selected project.
type Executor struct {
	Controller *Controller
	ProjectID  string
}

// ExecuteHost routes an exact pending request to the scoped runtime driver.
func (x Executor) ExecuteHost(ctx context.Context, requestID, approvalID string) (policy.Operation, error) {
	return x.Controller.Apply(ctx, x.ProjectID, requestID, approvalID)
}

// Apply rechecks the archived plan and consumes one original P2 approval.
func (c *Controller) Apply(ctx context.Context, id, requestID, approvalID string) (policy.Operation, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var op policy.Operation
	r, err := project.Load(id)
	if err != nil {
		return op, err
	}
	if old, ok, e := c.previous(r, requestID); e != nil {
		if old.ID != "" {
			return old, policy.UnknownOutcome(old.ID, e)
		}
		return op, e
	} else if ok {
		return consumed(old)
	}
	f, err := journalFile(r)
	if err != nil {
		return op, err
	}
	j, err := readJournal(f, id)
	if err != nil {
		return op, err
	}
	var planned *Entry
	for i := range j.Entries {
		if j.Entries[i].Pending != nil && j.Entries[i].Pending.ID == requestID {
			entry := j.Entries[i]
			planned = &entry
			break
		}
	}
	if planned == nil {
		return op, fmt.Errorf("request_unknown: no persisted runtime plan")
	}
	p := planned.Plan
	err = project.WithCAS(ctx, id, p.ExpectedDigest, p.Revision, func(l *project.Locked) error {
		unlock, e := f.Lock(ctx)
		if e != nil {
			return e
		}
		defer unlock()
		current, e := readJournal(f, id)
		if e != nil {
			return e
		}
		idx := -1
		for i, entry := range current.Entries {
			if entry.Pending != nil && entry.Pending.ID == requestID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("request_unknown")
		}
		entry := &current.Entries[idx]
		if old, ok, e := c.previous(r, requestID); e != nil {
			return e
		} else if ok {
			op = old
			return fmt.Errorf("request_consumed")
		}
		if e = c.verify(ctx, r, p, ""); e != nil {
			return e
		}
		store, e := c.policy(r)
		if e != nil {
			return e
		}
		op, _, e = store.BeginHost(ctx, requestID, approvalID, func(req policy.Request) error {
			if !matchesRuntimeRequest(req, p) {
				return fmt.Errorf("request_mismatch: runtime scope")
			}
			return c.verify(ctx, r, p, "")
		})
		if e != nil {
			return e
		}
		if e = c.inject("after_intent"); e != nil {
			return e
		}
		entry.Operation, entry.Phase = op, "intent"
		if e = f.Save(current); e != nil {
			return e
		}
		// The same plan is checked after intent and immediately before dispatch.
		if e = c.verify(ctx, r, p, op.ID); e != nil {
			op.Outcome = "failed"
			if result := store.Finish(context.WithoutCancel(ctx), op.ID, "failed"); result != nil {
				return result
			}
			entry.Operation, entry.Phase = op, "complete"
			if save := f.Save(current); save != nil {
				return save
			}
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if e = c.inject("before_dispatch"); e != nil {
			return e
		}
		authorizeEffect := func() error {
			if err := store.CheckRuntimeIntent(ctx, op.ID); err != nil {
				return err
			}
			return c.verifyTime(p)
		}
		if e = authorizeEffect(); e != nil {
			return e
		}
		outcome := "ok"
		if entry.After != nil {
			if e = l.Publish(*entry.After); e == nil {
				entry.Phase = "published"
			}
		} else if p.Request.Action == "manager-stop" {
			e = shutdownManager(ctx, r, p)
			entry.Phase = "dispatched"
		} else if p.Request.Action == "provision" {
			_, e = c.provision(ctx, r, *p.Request.Provision, op.ID, func(id string, verified bool) error {
				entry.ResourceID, entry.Phase, entry.Verified = id, "created", verified
				return f.Save(current)
			}, authorizeEffect, *p.Provision)
			entry.Phase = "dispatched"
		} else {
			if p.Request.Action == "restart" {
				e = c.change(ctx, r, r.Binding.ContainerID, "stop")
				if e == nil {
					if e = c.inject("between_restart"); e == nil {
						e = c.verifyRestartStart(ctx, r)
						if e == nil {
							e = authorizeEffect()
						}
						if e == nil {
							e = c.change(ctx, r, r.Binding.ContainerID, "start")
						}
					}
				}
			} else {
				id, action := r.Binding.ContainerID, p.Request.Action
				if action == "orphan-stop" {
					id, action = p.Request.ContainerID, "stop"
				}
				e = c.change(ctx, r, id, action)
			}
			entry.Phase = "dispatched"
		}
		if e != nil {
			outcome = "outcome_unknown"
		}
		if interrupted := c.inject("after_effect"); interrupted != nil {
			return interrupted
		}
		op.Outcome = outcome
		entry.Operation = op
		if result := store.Finish(context.WithoutCancel(ctx), op.ID, outcome); result != nil {
			return result
		}
		if receipt := c.inject("after_result"); receipt != nil {
			return receipt
		}
		entry.Phase = "complete"
		if outcome == "outcome_unknown" {
			entry.Phase = outcome
		}
		if save := f.Save(current); save != nil {
			return save
		}
		if e != nil {
			return policy.UnknownOutcome(op.ID, e)
		}
		return nil
	})
	if err != nil {
		old, ok, statusErr := c.previous(r, requestID)
		if ok {
			if old.Outcome == "intent" || old.Outcome == "outcome_unknown" {
				old.Outcome = "outcome_unknown"
				return old, policy.UnknownOutcome(old.ID, err)
			}
			return old, err
		}
		if old.ID != "" {
			return old, policy.UnknownOutcome(old.ID, err)
		}
		if statusErr != nil && op.ID != "" {
			op.Outcome = "outcome_unknown"
			return op, policy.UnknownOutcome(op.ID, err)
		}
		return policy.Operation{}, err
	}
	return op, err
}

func (c *Controller) verifyRestartStart(ctx context.Context, r project.Record) error {
	// stop changes only the running state. Recheck storage, daemon, full
	// configuration and the original StartedAt before the exact-ID start.
	o := c.observe(ctx, r, "")
	if o.Storage != "verified" || o.Engine != "verified" || o.Problem != "" || o.Bound == nil || !o.Bound.Matches || !o.Bound.Approved || o.Bound.State != "exited" {
		return fmt.Errorf("plan_stale: runtime changed between restart stop and start")
	}
	return nil
}
