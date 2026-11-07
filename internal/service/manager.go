package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/ports"
	"github.com/bojieli/agentreach/internal/privatecontrol"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/terminal"
)

// Config supplies trusted runtime, policy and non-interactive drivers.
type Config struct {
	Base     string
	Binding  policy.Binding
	Policy   *policy.Store
	Check    func(context.Context) error
	Paths    func() error
	Docker   *DockerDriver
	Terminal *terminal.Manager
	Ports    *ports.Client
}
type saved struct {
	Binding policy.Binding `json:"binding"`
	Records []Record       `json:"records"`
}
type failure struct{ err error }

// Manager serialises service transitions and latches connection identity failures.
type Manager struct {
	cfg     Config
	failure atomic.Pointer[failure]
	write   func(string, any) error
}

// New opens a connection without creating or repairing any resource.
func New(cfg Config) *Manager {
	m := &Manager{cfg: cfg}
	m.cfg.Policy = cfg.Policy.WithTargetCheck(m.Ready)
	return m
}

// Failure exposes the connection latch without acquiring lifecycle locks.
func (m *Manager) Failure() error {
	if f := m.failure.Load(); f != nil {
		return f.err
	}
	if m.cfg.Terminal != nil {
		return m.cfg.Terminal.Failure()
	}
	return nil
}
func (m *Manager) retire(e error) {
	if e != nil {
		m.failure.CompareAndSwap(nil, &failure{e})
	}
}

// Ready permanently retains a failed runtime guard for this connection.
func (m *Manager) Ready(ctx context.Context) error {
	if e := m.Failure(); e != nil {
		return e
	}
	e := m.cfg.Check(ctx)
	m.retire(e)
	return e
}
func (m *Manager) file() privatecontrol.File {
	return privatecontrol.File{Root: m.cfg.Base, Path: filepath.Join(m.cfg.Base, "services", m.cfg.Binding.ProjectID, "state.json"), Max: MaxStore, Check: m.cfg.Paths, Write: m.write}
}
func (m *Manager) load() (saved, error) {
	var s saved
	e := m.file().Read(&s)
	if os.IsNotExist(e) {
		return saved{Binding: m.cfg.Binding, Records: []Record{}}, nil
	}
	if e != nil {
		return s, e
	}
	if s.Binding != m.cfg.Binding || len(s.Records) > MaxServices {
		return s, fmt.Errorf("state_corrupt: service binding/budget")
	}
	ids := map[string]bool{}
	reqs := map[string]bool{}
	for _, r := range s.Records {
		if !terminal.ValidID(r.ID) || !terminal.ValidID(r.RequestID) || ids[r.ID] || reqs[r.RequestID] || r.Binding != m.cfg.Binding || r.Generation == 0 || r.Digest != Digest(r.Spec) {
			return s, fmt.Errorf("state_corrupt: service identity/digest")
		}
		if e = r.Spec.Validate(m.cfg.Binding.ProjectID); e != nil {
			return s, e
		}
		if r.ExecutionDigest != "" && r.ExecutionDigest != executionDigest(r) {
			return s, fmt.Errorf("state_corrupt: service execution digest")
		}
		switch r.State {
		case "planned", "created", "starting", "running", "unhealthy", "stopped", "lost", "unavailable", "outcome_unknown":
		default:
			return s, fmt.Errorf("state_corrupt: service state")
		}
		ids[r.ID] = true
		reqs[r.RequestID] = true
	}
	return s, nil
}
func find(s *saved, id string) *Record {
	for i := range s.Records {
		if s.Records[i].ID == id {
			return &s.Records[i]
		}
	}
	return nil
}
func (m *Manager) save(s saved) error { return m.file().Save(s) }
func (m *Manager) identity(e error) bool {
	var d *docker.Error
	var t *terminal.BackendIdentityError
	var p *ports.IdentityError
	return errors.As(e, &p) || errors.As(e, &t) || errors.As(e, &d) && (d.Kind == "identity_changed" || d.Kind == "generation_mismatch")
}
func (m *Manager) failed(r *Record, e error) {
	r.Ready = false
	if r.State == "lost" {
		return
	}
	if m.identity(e) {
		r.State = "lost"
		r.Reason = "identity_changed"
		m.retire(&BackendIdentityError{r.ID, e})
	} else {
		r.State = "unavailable"
		r.Reason = "observation_unavailable"
	}
}
func (m *Manager) observe(ctx context.Context, r *Record) error {
	return m.observeTarget(ctx, r, true)
}
func (m *Manager) observeTarget(ctx context.Context, r *Record, readiness bool) error {
	if r.State == "lost" {
		return fmt.Errorf("resource_lost: service identity retired")
	}
	if e := m.Ready(ctx); e != nil {
		retiredRecord(r, e)
		return e
	}
	if r.State == "planned" && r.ContainerID == "" && r.TerminalID == "" {
		return nil
	}
	if r.Spec.Driver == "workspace" {
		if r.TerminalID == "" {
			return fmt.Errorf("outcome_unknown: workspace backend unavailable")
		}
		v, e := m.cfg.Terminal.Call(ctx, "inspect", terminal.Request{TerminalID: r.TerminalID})
		if e != nil {
			m.failed(r, e)
			return e
		}
		o, ok := v.(terminal.Result)
		if !ok {
			return fmt.Errorf("state_corrupt: service backend observation")
		}
		switch o.State {
		case "running":
			r.State = "running"
		case "exited", "closed":
			r.State = "stopped"
			r.Descendants = "unconfirmed"
		case "lost":
			r.State = "lost"
			r.Reason = "backend_lost"
		default:
			r.State = "unavailable"
		}
		r.Ready = false
		c, e := m.cfg.Docker.Client.InspectService(ctx, r.ContainerID)
		if e != nil {
			m.failed(r, e)
			return e
		}
		ep, e := m.cfg.Docker.endpoint(ctx, *r, c)
		if e != nil {
			m.failed(r, e)
			return e
		}
		if ep != r.Endpoint || c.State.StartedAt != r.StartedAt {
			e = docker.Fail("identity_changed", "workspace service endpoint changed")
			m.failed(r, e)
			return e
		}
	} else {
		if r.ContainerID == "" {
			return fmt.Errorf("outcome_unknown: created sidecar ID is not confirmed")
		}
		c, e := m.cfg.Docker.Client.InspectService(ctx, r.ContainerID)
		if e == nil {
			e = m.cfg.Docker.verify(ctx, *r, c)
		}
		if e != nil {
			m.failed(r, e)
			return e
		}
		if r.StartedAt != "" && r.StartedAt != c.State.StartedAt {
			e = docker.Fail("generation_mismatch", "sidecar restarted outside approved lifecycle")
			m.failed(r, e)
			return e
		}
		if r.StartedAt == "" && c.State.Running {
			r.State = "outcome_unknown"
			r.Ready = false
			r.Reason = "start_generation_unconfirmed"
			return policy.UnknownOutcome(r.OperationID, nil)
		}
		r.Ready = false
		if !c.State.Running {
			if r.StartedAt == "" {
				r.State = "created"
			} else {
				r.State = "stopped"
			}
			return nil
		}
		if c.State.Paused || c.State.Restarting {
			r.State = "unavailable"
			return nil
		}
		ep, e := m.cfg.Docker.endpoint(ctx, *r, c)
		if e != nil {
			m.failed(r, e)
			return e
		}
		if r.Endpoint.EndpointID != "" && r.Endpoint != ep {
			e = docker.Fail("identity_changed", "sidecar endpoint changed")
			m.failed(r, e)
			return e
		}
		r.Endpoint = ep
		r.State = "running"
	}
	if r.State == "running" && readiness {
		r.Ready = probe(ctx, *r)
		if !r.Ready {
			r.State = "unhealthy"
		}
		r.Reason = ""
	}
	return nil
}

// Call implements the bounded strict CLI and MCP service operations.
func (m *Manager) Call(ctx context.Context, op string, q Request) (any, error) {
	if e := validateRequest(op, q); e != nil {
		return nil, e
	}
	if q.Spec != nil {
		if e := q.Spec.Validate(m.cfg.Binding.ProjectID); e != nil {
			return nil, e
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if cause := m.Ready(ctx); cause != nil {
		return m.guardResult(ctx, op, q, cause)
	}
	if op != "plan" && op != "start" {
		initial, e := m.load()
		if e != nil {
			return nil, e
		}
		if len(initial.Records) == 0 {
			if e = m.Ready(ctx); e != nil {
				return nil, e
			}
			if op == "list" {
				return []Summary{}, nil
			}
			return nil, fmt.Errorf("service_unknown")
		}
	}
	unlock, e := m.file().Lock(ctx)
	if e != nil {
		return nil, e
	}
	defer unlock()
	s, e := m.load()
	if e != nil {
		return nil, e
	}
	if op == "plan" || op == "start" {
		return m.create(ctx, &s, op, q)
	}
	if op == "list" {
		if e = m.Ready(ctx); e != nil {
			return nil, e
		}
		out := make([]Summary, 0, len(s.Records))
		for _, r := range s.Records {
			out = append(out, Summary{r.ID, r.Spec.Driver, r.Digest, r.ExecutionDigest, r.Generation, r.State, r.Ready, r.OperationID, r.ContainerID})
		}
		return out, nil
	}
	r := find(&s, q.ServiceID)
	if r == nil {
		return nil, fmt.Errorf("service_unknown")
	}
	if op == "stop" && r.Spec.Driver == "workspace" {
		view, e := m.cfg.Policy.Inspect()
		if e != nil {
			return Result{Service: r}, e
		}
		for _, old := range view.Operations {
			if old.Capability == "guest.service_stop" && old.TargetID == r.ID && old.Generation == r.Generation && old.Outcome != "failed" {
				r.OperationID = old.ID
				if old.Outcome != "ok" {
					return Result{Service: r}, policy.UnknownOutcome(old.ID, nil)
				}
				return Result{Service: r}, nil
			}
		}
	}
	e = m.observe(ctx, r)
	if saveErr := m.save(s); saveErr != nil {
		return Result{Service: r}, saveErr
	}
	if e != nil {
		return Result{Service: r}, e
	}
	switch op {
	case "inspect":
		return Result{Service: r}, nil
	case "stop":
		if r.Spec.Driver != "workspace" {
			return Result{Service: r}, fmt.Errorf("approval_required: plan exact service_change stop")
		}
		if r.State == "stopped" {
			return Result{Service: r}, nil
		}
		p, e := m.cfg.Policy.BeginGuestResource(ctx, "guest.service_stop", struct {
			ID         string
			Generation uint64
		}{r.ID, r.Generation}, r.ID, r.Generation)
		if e != nil {
			return Result{Service: r}, e
		}
		r.OperationID = p.ID
		r.State = "outcome_unknown"
		r.Ready = false
		if e = m.save(s); e != nil {
			return Result{Service: r}, policy.UnknownOutcome(p.ID, e)
		}
		_, e = m.cfg.Terminal.Call(ctx, "close", terminal.Request{TerminalID: r.TerminalID})
		if e == nil {
			r.State = "stopped"
			r.Ready = false
			r.Descendants = "unconfirmed"
		}
		return m.finish(ctx, s, r, p, e)
	case "logs":
		if r.Spec.Driver == "workspace" {
			v, e := m.cfg.Terminal.Call(ctx, "read", terminal.Request{TerminalID: r.TerminalID, MaxBytes: q.MaxBytes, Cursor: q.Cursor})
			if e != nil {
				return Result{Service: r}, e
			}
			o, ok := v.(terminal.Result)
			if !ok {
				return Result{Service: r}, fmt.Errorf("state_corrupt: backend log response")
			}
			return Result{Service: r, Data: o.Data, Cursor: o.NextCursor, Complete: o.Complete}, nil
		}
		if q.Cursor != "" {
			return nil, fmt.Errorf("unsupported: Docker log cursor; use bounded tail")
		}
		b, e := m.cfg.Docker.Client.ServiceLogs(ctx, r.ContainerID, 100)
		if e != nil {
			return Result{Service: r}, e
		}
		if len(b) > q.MaxBytes {
			return Result{Service: r}, fmt.Errorf("request_budget: log tail exceeds max_bytes")
		}
		return Result{Service: r, Data: b}, nil
	}
	return nil, fmt.Errorf("unsupported: service operation")
}

// A retired connection may recover identifiers from private receipts, but it
// must never turn that recovery into a successful call or a new dispatch.
func (m *Manager) guardResult(ctx context.Context, op string, q Request, cause error) (any, error) {
	s, e := m.load()
	if e != nil {
		return nil, errors.Join(cause, e)
	}
	for _, retained := range s.Records {
		r := retained
		if q.ServiceID != r.ID && (q.RequestID == "" || q.RequestID != r.RequestID) {
			continue
		}
		if q.Spec != nil && Digest(q.Spec) != r.RequestDigest {
			return Result{Service: &r}, errors.Join(cause, fmt.Errorf("request_mismatch: service request_id"))
		}
		unlock, e := m.file().Lock(ctx)
		if e != nil {
			return Result{Service: &r}, errors.Join(cause, e)
		}
		defer unlock()
		current, e := m.load()
		if e != nil {
			return Result{Service: &r}, errors.Join(cause, e)
		}
		actual := find(&current, r.ID)
		if actual == nil {
			return Result{Service: &r}, errors.Join(cause, fmt.Errorf("state_corrupt: retained service disappeared"))
		}
		r = *actual
		if r.Spec.Driver == "workspace" && (op == "start" || op == "stop") {
			view, e := m.cfg.Policy.Inspect()
			if e != nil {
				return Result{Service: &r}, errors.Join(cause, e)
			}
			for _, receipt := range view.Operations {
				if receipt.Capability == "guest.service_"+op && receipt.TargetID == r.ID && receipt.Generation == r.Generation {
					r.OperationID = receipt.ID
				}
			}
		}
		retiredRecord(&r, cause)
		*actual = r
		return Result{Service: &r}, errors.Join(cause, m.save(current))
	}
	return nil, cause
}

func retiredRecord(r *Record, cause error) {
	r.Ready = false
	if r.State == "lost" {
		return
	}
	var own *BackendIdentityError
	var port *ports.IdentityError
	var term *terminal.BackendIdentityError
	var runtime *docker.Error
	if errors.As(cause, &own) && own.ServiceID == r.ID {
		r.State = "lost"
		r.Reason = "identity_changed"
	} else if errors.As(cause, &runtime) && (runtime.Kind == "identity_changed" || runtime.Kind == "generation_mismatch") && !errors.As(cause, &own) && !errors.As(cause, &port) && !errors.As(cause, &term) {
		r.State = "lost"
		r.Reason = "runtime_lost"
	} else {
		r.State = "unavailable"
		r.Reason = "connection_retired"
	}
}
func (m *Manager) create(ctx context.Context, s *saved, op string, q Request) (any, error) {
	if op == "start" && q.Spec.Driver != "workspace" || op == "plan" && q.Spec.Driver != "sidecar" {
		return nil, fmt.Errorf("policy_denied: sidecar requires plan/approval; workspace requires Guest start")
	}
	for i := range s.Records {
		r := &s.Records[i]
		if r.RequestID == q.RequestID {
			if r.RequestDigest == "" || Digest(q.Spec) != r.RequestDigest {
				return Result{Service: r}, fmt.Errorf("request_mismatch: service request_id")
			}
			if r.Spec.Driver == "workspace" {
				view, e := m.cfg.Policy.Inspect()
				if e != nil {
					return Result{Service: r}, e
				}
				for _, old := range view.Operations {
					if old.Capability == "guest.service_start" && old.TargetID == r.ID && old.Generation == r.Generation {
						r.OperationID = old.ID
						if old.Outcome == "intent" || old.Outcome == "outcome_unknown" {
							return Result{Service: r}, policy.UnknownOutcome(old.ID, nil)
						}
					}
				}
				if r.OperationID == "" {
					return Result{Service: r}, fmt.Errorf("preparation_unavailable: no durable start result; inspect before a new request")
				}
			}
			if r.State == "outcome_unknown" || r.State == "starting" {
				return Result{Service: r}, policy.UnknownOutcome(r.OperationID, nil)
			}
			if op == "plan" && r.Pending == nil {
				return Result{Service: r}, fmt.Errorf("plan_unavailable: inspect retained preparation; use a new request only after review")
			}
			return Result{Service: r}, nil
		}
	}
	if e := m.Ready(ctx); e != nil {
		return nil, e
	}
	fixed := len(q.Spec.Publish)
	for _, r := range s.Records {
		fixed += len(r.Spec.Publish)
	}
	if fixed > MaxPublications {
		return nil, fmt.Errorf("request_budget: retained fixed publications")
	}
	if len(s.Records) >= MaxServices {
		return nil, fmt.Errorf("request_budget: retained services")
	}
	view, e := m.cfg.Policy.Inspect()
	if e != nil {
		return nil, e
	}
	if op == "start" && !view.GuestAllow || op == "plan" && view.Host["service_change"] != "allow" && view.Host["service_change"] != "approval" {
		return nil, fmt.Errorf("policy_denied: service policy")
	}
	spec, tool, e := m.cfg.Docker.prepare(ctx, *q.Spec)
	if e != nil {
		return nil, e
	}
	id, e := terminal.NewID()
	if e != nil {
		return nil, e
	}
	r := Record{ID: id, Binding: m.cfg.Binding, RequestID: q.RequestID, RequestDigest: Digest(q.Spec), Spec: spec, Digest: Digest(spec), Generation: 1, State: "planned", Tool: tool}
	s.Records = append(s.Records, r)
	rptr := &s.Records[len(s.Records)-1]
	for range spec.Publish {
		fixed, e := terminal.NewID()
		if e != nil {
			return nil, e
		}
		rptr.FixedIDs = append(rptr.FixedIDs, fixed)
	}
	if e = m.save(*s); e != nil {
		return nil, e
	}
	if op == "plan" {
		render, e := m.cfg.Docker.render(ctx, r, true)
		if e != nil {
			return Result{Service: rptr}, e
		}
		rptr.RenderDigest = render
		rptr.ExecutionDigest = executionDigest(*rptr)
		p, e := m.cfg.Policy.Plan(ctx, policy.HostRequest{Capability: "service_change", TargetID: r.ID, Generation: 1, Params: policy.HostParams{Operation: "start", SpecDigest: rptr.ExecutionDigest}})
		if e != nil {
			return Result{Service: rptr}, e
		}
		rptr.Pending = &p
		if e = m.save(*s); e != nil {
			return Result{Service: rptr}, e
		}
		return Result{Service: rptr}, nil
	}
	p, e := m.cfg.Policy.BeginGuestResource(ctx, "guest.service_start", spec, rptr.ID, rptr.Generation)
	if e != nil {
		return Result{Service: rptr}, e
	}
	rptr.OperationID = p.ID
	rptr.State = "outcome_unknown"
	if e = m.save(*s); e != nil {
		return Result{Service: rptr}, policy.UnknownOutcome(p.ID, e)
	}
	argv := []string{"/usr/bin/env"}
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		argv = append(argv, k+"="+spec.Env[k])
	}
	argv = append(argv, spec.Argv...)
	v, e := m.cfg.Terminal.Call(ctx, "create", terminal.Request{RequestID: q.RequestID, Argv: argv, Cwd: spec.Cwd, Cols: 80, Rows: 24})
	if result, ok := v.(terminal.Result); ok {
		rptr.TerminalID = result.TerminalID
	}
	rptr.ContainerID = m.cfg.Docker.Project.Binding.ContainerID
	rptr.StartedAt = m.cfg.Docker.Project.Binding.StartedAt
	if e == nil {
		c, cause := m.cfg.Docker.Client.InspectService(ctx, rptr.ContainerID)
		e = cause
		if e == nil {
			rptr.Endpoint, e = m.cfg.Docker.endpoint(ctx, *rptr, c)
		}
		if e == nil {
			rptr.State = "starting"
			e = m.observe(ctx, rptr)
		}
	}
	return m.finish(ctx, *s, rptr, p, e)
}
func (m *Manager) finish(ctx context.Context, s saved, r *Record, p policy.Operation, cause error) (Result, error) {
	outcome := "ok"
	if cause != nil && m.identity(cause) {
		m.failed(r, cause)
	}
	if cause != nil {
		outcome = "outcome_unknown"
		if r.State != "lost" {
			r.State = "outcome_unknown"
		}
		r.Ready = false
	}
	e := m.save(s)
	if e != nil {
		outcome = "outcome_unknown"
		cause = errors.Join(cause, e)
	}
	if e = m.cfg.Policy.Finish(context.WithoutCancel(ctx), p.ID, outcome); e != nil {
		cause = errors.Join(cause, e)
	}
	if cause != nil {
		return Result{Service: r}, policy.UnknownOutcome(p.ID, cause)
	}
	return Result{Service: r}, nil
}

// ExecuteHost runs only plans whose exact target/spec/generation are retained.
func (m *Manager) ExecuteHost(ctx context.Context, id, approval string) (policy.Operation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var zero policy.Operation
	v, e := m.cfg.Policy.Inspect()
	if e != nil {
		return zero, e
	}
	var h policy.HostRequest
	found := false
	for _, p := range v.Pending {
		if p.ID == id {
			h = p.Request.Host
			found = true
		}
	}
	if !found {
		return zero, fmt.Errorf("request_unknown")
	}
	if h.Capability != "service_change" && h.Capability != "port_expose" {
		return zero, fmt.Errorf("unsupported: capability driver unavailable")
	}
	for _, op := range v.Operations {
		if op.RequestID == id {
			if op.Outcome == "intent" || op.Outcome == "outcome_unknown" {
				return op, policy.UnknownOutcome(op.ID, nil)
			}
			return op, fmt.Errorf("request_consumed: operation_id=%s", op.ID)
		}
	}
	// Keep the approval critical section bounded to the retained specification,
	// generation and actual container/endpoint identity. The independent manager
	// verifies the complete process and readiness before binding any listener.
	unlock, e := m.file().Lock(ctx)
	if e != nil {
		// A concurrent dispatcher may have committed intent while this caller
		// waited for the resource lock. Preserve its receipt even on timeout.
		if current, cause := m.cfg.Policy.Inspect(); cause == nil {
			if op, found := hostReceipt(current, id); found {
				return consumedHost(op)
			}
		}
		return zero, e
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	// Deduplication must also occur under the service lock: both callers may
	// have inspected the ledger before either one committed its intent.
	v, e = m.cfg.Policy.Inspect()
	if e != nil {
		return zero, e
	}
	if op, found := hostReceipt(v, id); found {
		return consumedHost(op)
	}
	s, e := m.load()
	if e != nil {
		return zero, e
	}
	var r *Record
	var target ports.Target
	verify := func(req policy.Request) error {
		if req.Host != h {
			return fmt.Errorf("request_mismatch")
		}
		if e := m.Ready(ctx); e != nil {
			return e
		}
		if h.Capability == "port_expose" && h.Params.Operation == "close" {
			x, e := m.cfg.Ports.Inspect(ctx, h.TargetID)
			if e != nil {
				return e
			}
			if x.Target.ServiceID != h.Params.ServiceID || x.Target.Generation != h.Generation {
				return fmt.Errorf("request_mismatch: exposure generation")
			}
			return nil
		}
		r = find(&s, h.TargetID)
		if r == nil || r.Generation != h.Generation {
			return fmt.Errorf("resource_lost: service generation")
		}
		if h.Capability == "service_change" {
			if r.Generation == ^uint64(0) && (h.Params.Operation == "restart" || h.Params.Operation == "start" && r.StartedAt != "") {
				return fmt.Errorf("request_budget: service generation exhausted")
			}
			if r.Spec.Driver != "sidecar" || r.ExecutionDigest == "" || h.Params.SpecDigest != r.ExecutionDigest {
				return fmt.Errorf("request_mismatch: service specification")
			}
			digest, e := m.cfg.Docker.render(ctx, *r, false)
			if e != nil {
				return e
			}
			if digest != r.RenderDigest {
				return docker.Fail("identity_changed", "Compose rendered digest changed")
			}
			if r.State == "lost" {
				return fmt.Errorf("resource_lost")
			}
			if r.ContainerID != "" {
				if h.Params.Operation == "restart" && r.State == "outcome_unknown" && r.StartedAt == "" {
					c, e := m.cfg.Docker.Client.InspectService(ctx, r.ContainerID)
					if e != nil {
						return e
					}
					return m.cfg.Docker.verify(ctx, *r, c)
				}
				e = m.observe(ctx, r)
				if saveErr := m.save(s); saveErr != nil {
					return saveErr
				}
				if e == nil && h.Params.Operation == "start" && (r.State == "running" || r.State == "unhealthy") {
					return fmt.Errorf("policy_denied: service already running")
				}
				return e
			}
			if h.Params.Operation != "start" {
				return fmt.Errorf("policy_denied: service not created")
			}
			return nil
		}
		if !r.Ready || r.State != "running" || !r.Port(h.Params.GuestPort) {
			return fmt.Errorf("exposure_unavailable: inspect a ready service and approved port before planning exposure")
		}
		target = m.target(*r, h.Params.GuestPort)
		if e := m.checkTarget(ctx, *r, target); e != nil {
			return e
		}
		return nil
	}
	p, _, e := m.cfg.Policy.BeginHost(ctx, id, approval, verify)
	if e != nil {
		if m.identity(e) {
			if r != nil {
				m.failed(r, e)
				if saveErr := m.save(s); saveErr != nil {
					return p, errors.Join(e, saveErr)
				}
			} else {
				m.retire(e)
			}
		}
		return p, e
	}
	if h.Capability == "port_expose" {
		unlock()
		unlock = nil
		if h.Params.Operation == "close" {
			_, e = m.cfg.Ports.Close(ctx, h.TargetID, p.ID)
		} else {
			_, e = m.cfg.Ports.Expose(ctx, target, h.Params.HostPort, p.ID)
		}
		if m.identity(e) {
			m.retire(e)
		}
		outcome := "ok"
		if e != nil {
			outcome = "outcome_unknown"
		}
		p.Outcome = outcome
		if cause := m.cfg.Policy.Finish(context.WithoutCancel(ctx), p.ID, outcome); cause != nil {
			p.Outcome = "outcome_unknown"
			return p, errors.Join(e, cause)
		}
		if e != nil {
			return p, policy.UnknownOutcome(p.ID, e)
		}
		return p, nil
	}
	r.OperationID = p.ID
	r.State = "outcome_unknown"
	r.Ready = false
	if e = m.save(s); e != nil {
		return p, policy.UnknownOutcome(p.ID, e)
	}
	e = m.change(ctx, &s, r, h.Params.Operation)
	_, e = m.finish(ctx, s, r, p, e)
	if e != nil {
		p.Outcome = "outcome_unknown"
	} else {
		p.Outcome = "ok"
	}
	return p, e
}

func hostReceipt(v policy.View, id string) (policy.Operation, bool) {
	for _, op := range v.Operations {
		if op.RequestID == id {
			return op, true
		}
	}
	return policy.Operation{}, false
}
func consumedHost(op policy.Operation) (policy.Operation, error) {
	if op.Outcome == "intent" || op.Outcome == "outcome_unknown" {
		return op, policy.UnknownOutcome(op.ID, nil)
	}
	return op, fmt.Errorf("request_consumed: operation_id=%s", op.ID)
}
func (m *Manager) change(ctx context.Context, s *saved, r *Record, op string) error {
	d := m.cfg.Docker
	if r.ContainerID == "" {
		if _, e := d.Client.Compose(ctx, r.Tool, "reach-"+r.ID, d.file(*r), "create"); e != nil {
			return e
		}
		b, e := d.Client.Compose(ctx, r.Tool, "reach-"+r.ID, d.file(*r), "id")
		if e != nil {
			return e
		}
		r.ContainerID = strings.TrimSpace(string(b))
		c, e := d.Client.InspectService(ctx, r.ContainerID)
		if e != nil {
			return e
		}
		if c.State.Running {
			return docker.Fail("identity_changed", "newly created sidecar is already running")
		}
		if e = d.verify(ctx, *r, c); e != nil {
			return e
		}
		r.Snapshot = snapshot(c)
		r.State = "created"
		if e = m.save(*s); e != nil {
			return e
		}
	}
	c, e := d.Client.InspectService(ctx, r.ContainerID)
	if e != nil {
		return e
	}
	if e = d.verify(ctx, *r, c); e != nil {
		return e
	}
	if op == "stop" || op == "restart" {
		if _, e = d.Client.ChangeService(ctx, r.ContainerID, "stop"); e != nil {
			return e
		}
		c, e = d.Client.InspectService(ctx, r.ContainerID)
		if e != nil {
			return e
		}
		if c.State.Running {
			return fmt.Errorf("outcome_unknown: service still running")
		}
		r.State = "stopped"
		r.Ready = false
		if op == "stop" {
			return nil
		}
	}
	if r.StartedAt != "" || op == "restart" {
		if op != "restart" && c.State.Running {
			return fmt.Errorf("policy_denied: service already started")
		}
		r.Generation++
		r.Endpoint = docker.Endpoint{}
		r.StartedAt = ""
		if e = m.save(*s); e != nil {
			return e
		}
	}
	if _, e = d.Client.ChangeService(ctx, r.ContainerID, "start"); e != nil {
		return e
	}
	c, e = d.Client.InspectService(ctx, r.ContainerID)
	if e != nil {
		return e
	}
	if e = d.verify(ctx, *r, c); e != nil {
		return e
	}
	r.StartedAt = c.State.StartedAt
	r.State = "starting"
	return m.observe(ctx, r)
}
func (m *Manager) target(r Record, port uint16) ports.Target {
	return ports.Target{Binding: r.Binding, ServiceID: r.ID, Generation: r.Generation, ContainerID: r.ContainerID, StartedAt: r.StartedAt, EndpointID: r.Endpoint.EndpointID, Address: r.Endpoint.IPAddress, Port: port}
}

func (m *Manager) checkTarget(ctx context.Context, r Record, t ports.Target) error {
	c, e := m.cfg.Docker.Client.InspectService(ctx, t.ContainerID)
	if e != nil {
		return e
	}
	if !c.State.Running || c.State.Paused || c.State.Restarting || c.State.StartedAt != t.StartedAt {
		return docker.Fail("generation_mismatch", "service target runtime changed")
	}
	if r.Spec.Driver == "sidecar" {
		if e = m.cfg.Docker.verify(ctx, r, c); e != nil {
			return e
		}
	}
	ep, e := m.cfg.Docker.endpoint(ctx, r, c)
	if e != nil {
		return e
	}
	if ep.EndpointID != t.EndpointID || ep.IPAddress != t.Address {
		return docker.Fail("identity_changed", "service target endpoint changed")
	}
	return nil
}

// Target is a fresh identity/readiness observation for the Host manager.
func (m *Manager) Target(ctx context.Context, id string, port uint16) (ports.Target, error) {
	return m.resolveTarget(ctx, id, port)
}

// IdentityTarget rechecks process/runtime/endpoint identity without making an
// application request on a server awaiting the proxied payload. Success leaves
// last readiness metadata intact; failure and stopped identities are retained.
func (m *Manager) IdentityTarget(ctx context.Context, id string, port uint16) (ports.Target, error) {
	if !terminal.ValidID(id) {
		return ports.Target{}, fmt.Errorf("policy_denied: service ID")
	}
	s, e := m.load()
	if e != nil {
		return ports.Target{}, e
	}
	retained := find(&s, id)
	if retained == nil {
		return ports.Target{}, fmt.Errorf("service_unknown")
	}
	before := *retained
	if !before.Port(port) || before.State != "running" && before.State != "unhealthy" {
		return ports.Target{}, fmt.Errorf("exposure_unavailable: service identity not running")
	}
	// App health probes can hold the lifecycle lock while an ordinary server
	// waits for a request. Identity-only observation must not wait behind them.
	observed := before
	e = m.observeTarget(ctx, &observed, false)
	if e != nil || observed.State != "running" {
		if e == nil {
			e = fmt.Errorf("exposure_unavailable: service process not running")
		}
		unlock, cause := m.file().Lock(ctx)
		if cause != nil {
			return ports.Target{}, errors.Join(e, cause)
		}
		defer unlock()
		current, cause := m.load()
		if cause != nil {
			return ports.Target{}, errors.Join(e, cause)
		}
		r := find(&current, id)
		if r != nil && sameServiceIdentity(before, *r) {
			if r.State == "lost" {
				return ports.Target{}, e
			}
			// Observation may have raced a same-generation stop receipt. Keep
			// its authoritative operation/approval metadata while retiring only
			// the observed runtime state.
			r.State = observed.State
			r.Ready = false
			r.Reason = observed.Reason
			if observed.Descendants != "" {
				r.Descendants = observed.Descendants
			}
			return ports.Target{}, errors.Join(e, m.save(current))
		}
		return ports.Target{}, e
	}
	// A lifecycle change may have committed during the read-only observation.
	// Re-read the authoritative generation; never overwrite a newer identity.
	current, e := m.load()
	if e != nil {
		return ports.Target{}, e
	}
	after := find(&current, id)
	if after == nil || !sameServiceIdentity(before, *after) || after.State != "running" && after.State != "unhealthy" {
		return ports.Target{}, fmt.Errorf("exposure_unavailable: service changed during identity observation")
	}
	return m.target(observed, port), nil
}
func sameServiceIdentity(a, b Record) bool {
	return a.ID == b.ID && a.Binding == b.Binding && a.Generation == b.Generation && a.Digest == b.Digest && a.ExecutionDigest == b.ExecutionDigest && a.ContainerID == b.ContainerID && a.StartedAt == b.StartedAt && a.Endpoint == b.Endpoint && a.TerminalID == b.TerminalID && a.Snapshot == b.Snapshot
}
func (m *Manager) resolveTarget(ctx context.Context, id string, port uint16) (ports.Target, error) {
	if !terminal.ValidID(id) {
		return ports.Target{}, fmt.Errorf("policy_denied: service ID")
	}
	initial, e := m.load()
	if e != nil {
		return ports.Target{}, e
	}
	if len(initial.Records) == 0 {
		return ports.Target{}, fmt.Errorf("service_unknown")
	}
	unlock, e := m.file().Lock(ctx)
	if e != nil {
		return ports.Target{}, e
	}
	defer unlock()
	s, e := m.load()
	if e != nil {
		return ports.Target{}, e
	}
	r := find(&s, id)
	if r == nil {
		return ports.Target{}, fmt.Errorf("service_unknown")
	}
	if !r.Port(port) {
		return ports.Target{}, fmt.Errorf("exposure_unavailable: unapproved service port")
	}
	e = m.observe(ctx, r)
	if cause := m.save(s); cause != nil {
		return ports.Target{}, cause
	}
	if e != nil {
		return ports.Target{}, e
	}
	if !r.Ready || !r.Port(port) {
		return ports.Target{}, fmt.Errorf("exposure_unavailable: service readiness/port")
	}
	if !portReady(ctx, *r, port) {
		return ports.Target{}, fmt.Errorf("exposure_unavailable: approved port not ready")
	}
	return m.target(*r, port), nil
}

// PortClient returns the trusted private manager adapter.
func (m *Manager) PortClient() *ports.Client { return m.cfg.Ports }
func executionDigest(r Record) string {
	return Digest(struct {
		Binding                  policy.Binding
		SpecDigest, RenderDigest string
		Tool                     docker.ComposeTool
	}{r.Binding, r.Digest, r.RenderDigest, r.Tool})
}

// Exposures combines verified fixed Docker publications and live proxy state.
func (m *Manager) Exposures(ctx context.Context) ([]ports.Exposure, error) {
	if e := m.Ready(ctx); e != nil {
		return nil, e
	}
	s, e := m.load()
	if e != nil {
		return nil, e
	}
	out := []ports.Exposure{}
	for _, r := range s.Records {
		if len(r.FixedIDs) != len(r.Spec.Publish) {
			return nil, fmt.Errorf("state_corrupt: fixed exposure identities")
		}
		if len(r.FixedIDs) == 0 {
			continue
		}
		observed, e := m.Call(ctx, "inspect", Request{ServiceID: r.ID})
		if observed != nil {
			if result, ok := observed.(Result); ok && result.Service != nil {
				r = *result.Service
			}
		}
		if e != nil && r.State != "lost" && r.State != "unavailable" {
			return nil, e
		}
		for i, p := range r.Spec.Publish {
			state := "unavailable"
			if r.Ready && portReady(ctx, r, p.GuestPort) {
				state = "open"
			}
			out = append(out, ports.Exposure{ID: r.FixedIDs[i], Instance: r.ID, Target: m.target(r, p.GuestPort), Address: "127.0.0.1", Port: p.HostPort, RequestedPort: p.HostPort, State: state, OperationID: r.OperationID})
		}
	}
	dynamic, e := m.cfg.Ports.List(ctx)
	if e != nil {
		if m.identity(e) {
			m.retire(e)
		}
		return nil, e
	}
	return append(out, dynamic...), nil
}

// WithExposure holds the service lock before the caller takes the policy lock.
// The resolver re-observes the exact resource on every invocation, including
// immediately before browser dispatch. Lock order stays service -> policy.
func (m *Manager) WithExposure(ctx context.Context, id string, visit func(func() (ports.Exposure, error)) error) error {
	if !terminal.ValidID(id) {
		return fmt.Errorf("policy_denied: exposure ID")
	}
	initial, e := m.load()
	if e != nil {
		return e
	}
	if len(initial.Records) == 0 {
		return fmt.Errorf("exposure_unavailable: service registry absent")
	}
	unlock, e := m.file().Lock(ctx)
	if e != nil {
		return e
	}
	defer unlock()
	s, e := m.load()
	if e != nil {
		return e
	}
	resolve := func() (ports.Exposure, error) {
		for i := range s.Records {
			r := &s.Records[i]
			for j, fixed := range r.FixedIDs {
				if fixed == id {
					if j >= len(r.Spec.Publish) {
						return ports.Exposure{}, fmt.Errorf("state_corrupt: fixed publication")
					}
					e := m.observe(ctx, r)
					if cause := m.save(s); cause != nil {
						return ports.Exposure{}, cause
					}
					if e != nil {
						return ports.Exposure{}, e
					}
					p := r.Spec.Publish[j]
					state := "unavailable"
					if r.Ready && portReady(ctx, *r, p.GuestPort) {
						state = "open"
					}
					return ports.Exposure{ID: id, Instance: r.ID, Target: m.target(*r, p.GuestPort), Address: "127.0.0.1", Port: p.HostPort, RequestedPort: p.HostPort, State: state, OperationID: r.OperationID}, nil
				}
			}
		}
		x, e := m.cfg.Ports.Inspect(ctx, id)
		if e != nil {
			if m.identity(e) {
				m.retire(e)
			}
			return x, e
		}
		if x.State != "open" {
			return x, nil
		}
		r := find(&s, x.Target.ServiceID)
		if r == nil {
			return x, fmt.Errorf("exposure_unavailable: service missing")
		}
		e = m.observe(ctx, r)
		if cause := m.save(s); cause != nil {
			return x, cause
		}
		if e != nil {
			return x, e
		}
		if !r.Ready || !r.Port(x.Target.Port) || m.target(*r, x.Target.Port) != x.Target {
			return x, fmt.Errorf("exposure_unavailable: target generation/endpoint changed")
		}
		if !portReady(ctx, *r, x.Target.Port) {
			return x, fmt.Errorf("exposure_unavailable: approved port not ready")
		}
		return x, nil
	}
	return visit(resolve)
}

// Exposure rechecks only the requested resource before providing an origin.
func (m *Manager) Exposure(ctx context.Context, id string) (ports.Exposure, error) {
	var out ports.Exposure
	e := m.WithExposure(ctx, id, func(resolve func() (ports.Exposure, error)) error { var e error; out, e = resolve(); return e })
	return out, e
}
