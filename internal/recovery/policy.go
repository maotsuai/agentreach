package recovery

import (
	"context"
	"fmt"
	"time"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/orbstack"
)

// recoveryPolicy is private: its reduced readiness check cannot be exported to
// a normal session, Guest/bridge tool, service or browser executor.
func recoveryPolicy(r project.Record) (*policy.Store, error) {
	base, err := project.Home()
	if err != nil {
		return nil, err
	}
	check := func(ctx context.Context) error {
		current, err := project.Load(r.Spec.ID)
		if err != nil {
			return err
		}
		if project.Digest(current) != project.Digest(r) {
			return fmt.Errorf("plan_stale: recovery authorization changed")
		}
		if err := controlPaths(r); err != nil {
			return err
		}
		return orbstack.New(r).CheckRecoveryBase(ctx)
	}
	return policy.New(project.ControlBase(base, r), policy.Binding{ProjectID: r.Spec.ID, RecordDigest: project.Digest(r), Generation: r.Binding.Generation}, check, func() error { return controlPaths(r) }), nil
}

// UpdatePolicy can initialize an empty, deny-all policy or change only the
// recovery rule. It cannot grant Guest, service, browser or arbitrary Host access.
func (c *Controller) UpdatePolicy(ctx context.Context, id, decision string) error {
	if decision != "init" && decision != "approval" && decision != "deny" {
		return fmt.Errorf("policy_denied: runtime recovery requires exact approval")
	}
	r, err := project.Load(id)
	if err != nil {
		return err
	}
	p, err := c.policy(r)
	if err != nil {
		return err
	}
	if decision == "init" {
		return project.WithCAS(ctx, id, project.Digest(r), r.Binding.Revision, func(_ *project.Locked) error {
			return p.InitRecovery(ctx)
		})
	}
	return p.Set(ctx, "runtime_change", decision)
}

// Approve revalidates the originally persisted plan, and issues only a runtime
// approval. This entry cannot approve an unrelated Host pending request.
func (c *Controller) Approve(ctx context.Context, id, requestID, digest string, ttl time.Duration) (policy.Approval, error) {
	var out policy.Approval
	r, err := project.Load(id)
	if err != nil {
		return out, err
	}
	err = project.WithCAS(ctx, id, project.Digest(r), r.Binding.Revision, func(_ *project.Locked) error {
		f, e := journalFile(r)
		if e != nil {
			return e
		}
		j, e := readJournal(f, id)
		if e != nil {
			return e
		}
		for _, entry := range j.Entries {
			if entry.Pending != nil && entry.Pending.ID == requestID {
				if entry.Pending.Digest != digest || !matchesRuntimeRequest(entry.Pending.Request, entry.Plan) {
					return fmt.Errorf("request_mismatch")
				}
				if e = c.verify(ctx, r, entry.Plan, ""); e != nil {
					return e
				}
				p, e := c.policy(r)
				if e != nil {
					return e
				}
				out, e = p.Approve(ctx, requestID, digest, ttl)
				return e
			}
		}
		return fmt.Errorf("request_unknown: no runtime plan owns this request")
	})
	return out, err
}
