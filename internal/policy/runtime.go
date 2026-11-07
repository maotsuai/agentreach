package policy

import (
	"context"
	"fmt"
)

// CheckRuntimeIntent rechecks a consumed, still-unresolved runtime grant before
// each fixed subeffect. It does not create an intent or authorize any replay.
func (s *Store) CheckRuntimeIntent(ctx context.Context, id string) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	l, err := s.load()
	if err != nil {
		return err
	}
	n, err := s.sample(l)
	if err != nil {
		return err
	}
	for _, op := range l.Operations {
		if op.ID != id {
			continue
		}
		if op.Capability != "runtime_change" || op.Outcome != "intent" || op.Revision != l.Revision || l.Host["runtime_change"] != "approval" {
			return fmt.Errorf("plan_stale: runtime intent no longer authorized")
		}
		a := findApproval(l, op.ApprovalID)
		if a == nil || !a.Used || a.Revoked || a.RequestID != op.RequestID || a.Digest != op.Digest || a.Revision != l.Revision || a.Issued.Boot != n.Boot || n.Tick < a.Issued.Tick || n.Tick >= a.Deadline || n.Wall < a.Issued.Wall {
			return fmt.Errorf("approval_expired_or_revoked")
		}
		return ctx.Err()
	}
	return fmt.Errorf("operation_unknown")
}
