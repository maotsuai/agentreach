package session

import (
	"context"
	"fmt"
	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
)

// Policy retains the original binding, shared runtime latch and control paths.
// Ordinary managed calls never initialize or grant a missing policy.
func (s *Session) Policy(check func(context.Context) error) (*policy.Store, error) {
	r, e := s.ManagedProject()
	if e != nil {
		return nil, e
	}
	base, e := project.Home()
	if e != nil {
		return nil, e
	}
	return policy.New(base, policy.Binding{ProjectID: s.Managed.ProjectID, RecordDigest: s.Managed.RecordSHA256, Generation: r.Binding.Generation}, check, s.CheckControlPaths), nil
}

// BeginGuest journals a managed effect and returns its mandatory result writer.
func (s *Session) BeginGuest(ctx context.Context, check func(context.Context) error, capability string, payload any) (func(string, error) error, error) {
	if s.Managed == nil {
		return func(string, error) error { return nil }, nil
	}
	p, e := s.Policy(check)
	if e != nil {
		return nil, e
	}
	op, e := p.BeginGuest(ctx, capability, payload)
	if e != nil {
		return nil, e
	}
	return func(outcome string, cause error) error {
		if e := p.Finish(context.WithoutCancel(ctx), op.ID, outcome); e != nil {
			if cause != nil {
				return fmt.Errorf("%w; dispatch: %w", e, cause)
			}
			return e
		}
		if outcome == "outcome_unknown" {
			return policy.UnknownOutcome(op.ID, cause)
		}
		return nil
	}, nil
}

func (s *Session) guestEffect(check func(context.Context) error) func(context.Context, string, any, func() error) error {
	return func(ctx context.Context, capability string, payload any, dispatch func() error) error {
		finish, e := s.BeginGuest(ctx, check, "guest."+capability, payload)
		if e != nil {
			return e
		}
		e = dispatch()
		outcome := "ok"
		if e != nil {
			outcome = "outcome_unknown"
		}
		if resultErr := finish(outcome, e); resultErr != nil {
			return resultErr
		}
		return e
	}
}
