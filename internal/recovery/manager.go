package recovery

import (
	"context"
	"fmt"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/ports"
	"github.com/bojieli/agentreach/internal/project"
)

func retiredBinding(r project.Record, digest string) (project.Record, error) {
	bindings, _, e := historicalBindings(r)
	if e != nil {
		return project.Record{}, e
	}
	for _, old := range bindings {
		if project.Digest(old) == digest && digest != project.Digest(r) {
			return old, nil
		}
	}
	return project.Record{}, fmt.Errorf("policy_denied: no retired binding owns this manager")
}

func managerObservation(ctx context.Context, r project.Record, digest string) (*ports.RetiredManager, error) {
	old, e := retiredBinding(r, digest)
	if e != nil {
		return nil, e
	}
	base, e := project.Home()
	if e != nil {
		return nil, e
	}
	b := policy.Binding{ProjectID: old.Spec.ID, RecordDigest: digest, Generation: old.Binding.Generation}
	out, e := ports.ObserveRetired(ctx, project.ControlBase(base, old), base, b, func() error { return controlPaths(old) })
	return &out, e
}

func shutdownManager(ctx context.Context, r project.Record, p Plan) error {
	old, e := retiredBinding(r, p.Request.RecordDigest)
	if e != nil {
		return e
	}
	base, e := project.Home()
	if e != nil {
		return e
	}
	b := policy.Binding{ProjectID: old.Spec.ID, RecordDigest: project.Digest(old), Generation: old.Binding.Generation}
	return ports.ShutdownRetired(ctx, project.ControlBase(base, old), base, b, p.Manager.Instance, func() error { return controlPaths(old) })
}
