package recovery

import (
	"context"
	"fmt"
	"slices"

	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/ports"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/orbstack"
	"github.com/bojieli/agentreach/internal/session"
	"github.com/bojieli/agentreach/internal/terminal"
)

func projectSession(r project.Record) *session.Session {
	return &session.Session{
		Name: project.SessionNameFor(r), Managed: session.Bind(r), Mode: session.ModeExec,
		Target: &session.Target{Kind: session.KindDocker, Container: r.Binding.ContainerID, Workspace: r.Spec.Workspace.GuestPath},
	}
}

// controlPaths remains usable for diagnosis when Docker or the workspace is
// down, but cannot bless control files exposed by a workspace mount.
func controlPaths(r project.Record) error {
	if err := projectSession(r).CheckControlPaths(); err != nil {
		return err
	}
	base, err := project.Home()
	if err != nil {
		return err
	}
	for _, mount := range r.Spec.BindMounts {
		overlap, err := hostpath.Overlaps(mount.Source, base)
		if err != nil || overlap {
			return fmt.Errorf("policy_denied: recovery control root overlaps or cannot verify a Guest bind")
		}
	}
	return nil
}

func observeControl(ctx context.Context, r project.Record, o orbstack.Inventory, ignore string) Control {
	out := Control{RuntimePolicy: "deny"}
	if err := controlPaths(r); err != nil {
		out.Problems = append(out.Problems, "control_paths:"+reportProblem(err))
		return out
	}
	s := projectSession(r)
	p, err := s.Policy(s.CheckManaged)
	if err == nil {
		var view policy.View
		view, err = p.InspectOptional()
		out.PolicyRevision = view.Revision
		if v := view.Host["runtime_change"]; v != "" {
			out.RuntimePolicy = v
		}
		for _, op := range view.Operations {
			if op.ID != ignore && (op.Outcome == "intent" || op.Outcome == "outcome_unknown") {
				out.Unresolved = append(out.Unresolved, op)
			}
		}
	}
	if err != nil {
		out.Problems = append(out.Problems, "policy:"+reportProblem(err))
	}
	term, err := s.Terminals(s.CheckManaged)
	if err == nil {
		var records []terminal.RecordedTerminal
		records, err = term.Recorded()
		for _, t := range records {
			out.Objects = append(out.Objects, Object{Kind: "terminal", ID: t.ID, OperationID: t.OperationID, Desired: t.State, Observed: "not_probed"})
		}
	}
	if err != nil {
		out.Problems = append(out.Problems, "terminal:"+reportProblem(err))
	}
	m, err := s.Services(s.CheckManaged)
	if err != nil {
		out.Problems = append(out.Problems, "service:"+reportProblem(err))
		return out
	}
	retained := map[string]bool{r.Binding.ContainerID: true}
	records, err := m.Recorded()
	if err != nil {
		out.Problems = append(out.Problems, "service:"+reportProblem(err))
	}
	for _, svc := range records {
		obj := Object{Kind: "service", ID: svc.ID, OperationID: svc.OperationID, ContainerID: svc.ContainerID, Desired: svc.State, Observed: "not_probed"}
		if svc.ContainerID != "" {
			retained[svc.ContainerID] = true
			if o.Engine == "verified" && o.Problem == "" {
				i := slices.IndexFunc(o.Containers, func(c orbstack.RuntimeObservation) bool { return c.ContainerID == svc.ContainerID })
				switch {
				case i < 0:
					obj.Observed, obj.Difference = "not_in_project_inventory", "service_missing_or_unlabelled"
				case o.Containers[i].Problem != "":
					obj.Observed, obj.Difference = "unconfirmed", "service_unconfirmed"
				case o.Containers[i].ServiceID != svc.ID || o.Containers[i].StartedAt != svc.StartedAt:
					obj.Observed, obj.Difference = o.Containers[i].State, "service_identity_changed"
				default:
					obj.Observed = o.Containers[i].State
				}
			}
		}
		out.Objects = append(out.Objects, obj)
		for _, id := range svc.FixedIDs {
			out.Objects = append(out.Objects, Object{Kind: "fixed_exposure", ID: id, OperationID: svc.OperationID, ContainerID: svc.ContainerID, Desired: "published", Observed: "not_probed"})
		}
	}
	for _, c := range o.Containers {
		if !retained[c.ContainerID] {
			out.Objects = append(out.Objects, Object{Kind: "unreferenced_container", ID: c.ContainerID, OperationID: c.OperationID, Desired: "unknown_ownership", Observed: c.State, Difference: "unreferenced_container"})
		}
	}
	home, err := project.Home()
	if err != nil {
		out.Problems = append(out.Problems, "exposure:"+reportProblem(err))
		return out
	}
	binding := policy.Binding{ProjectID: r.Spec.ID, RecordDigest: project.Digest(r), Generation: r.Binding.Generation}
	exposures, err := ports.ObserveRecoveryExposures(ctx, project.ControlBase(home, r), home, binding, func() error { return controlPaths(r) })
	if err != nil {
		out.Problems = append(out.Problems, "exposure:"+reportProblem(err))
	}
	for _, x := range exposures {
		obj := Object{Kind: "dynamic_exposure", ID: x.ID, OperationID: x.OperationID, ContainerID: x.Target.ContainerID, Desired: "retained_listener", Observed: x.State}
		if x.State == "unavailable" {
			obj.Difference = "listener_unavailable"
		}
		out.Objects = append(out.Objects, obj)
	}
	return out
}
