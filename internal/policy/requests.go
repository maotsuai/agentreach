package policy

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func hostCapability(c string) bool {
	return c == "open_url" || c == "port_expose" || c == "service_change" || c == "runtime_change"
}

// RelativePath rejects encoded and literal authority/scheme/path traversal as
// well as query/fragment. P2 never stores URL queries or accepts arbitrary URLs.
func RelativePath(p string) (string, error) {
	if p == "" {
		p = "/"
	}
	if len(p) > 2048 {
		return "", fail("policy_denied: path too long")
	}
	u, e := url.PathUnescape(p)
	if e != nil {
		return "", fail("policy_denied: invalid path encoding")
	}
	for _, v := range []string{p, u} {
		if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.ContainsAny(v, "\\?#%") {
			return "", fail("policy_denied: only an origin-relative path is allowed")
		}
		for _, r := range v {
			if r < 32 || r == 127 {
				return "", fail("policy_denied: control character")
			}
		}
		for _, part := range strings.Split(v, "/") {
			if part == "." || part == ".." {
				return "", fail("policy_denied: path traversal")
			}
		}
	}
	return u, nil
}

func normalise(h HostRequest) (HostRequest, string, error) {
	if !hostCapability(h.Capability) {
		return h, "", fail("policy_denied: unknown/forbidden Host capability")
	}
	if !targetPattern.MatchString(h.TargetID) || h.Generation == 0 {
		return h, "", fail("policy_denied: invalid target identity")
	}
	p := h.Params
	switch h.Capability {
	case "open_url":
		if p.ServiceID != "" || p.GuestPort != 0 || p.HostPort != 0 || p.Listen != "" || p.Operation != "" || p.SpecDigest != "" {
			return h, "", fail("policy_denied: unexpected open_url parameters")
		}
		path, e := RelativePath(p.Path)
		if e != nil {
			return h, "", e
		}
		h.Params.Path = path
		return h, "open_loopback_browser", nil
	case "port_expose":
		if p.Operation == "close" {
			if p.Path != "" || p.SpecDigest != "" || !idPattern.MatchString(p.ServiceID) || !idPattern.MatchString(h.TargetID) || p.GuestPort != 0 || p.HostPort != 0 || p.Listen != "127.0.0.1" {
				return h, "", fail("policy_denied: invalid exposure close parameters")
			}
			return h, "close_loopback_exposure", nil
		}
		if p.Path != "" || p.Operation != "" || p.SpecDigest != "" || p.ServiceID != h.TargetID || p.GuestPort == 0 || p.Listen != "127.0.0.1" {
			return h, "", fail("policy_denied: invalid port exposure parameters")
		}
		return h, "create_loopback_exposure", nil
	case "runtime_change":
		if p.Path != "" || p.ServiceID != "" || p.GuestPort != 0 || p.HostPort != 0 || p.Listen != "" || !digestPattern.MatchString(p.SpecDigest) {
			return h, "", fail("policy_denied: invalid runtime plan parameters")
		}
		switch p.Operation {
		case "start", "stop", "restart", "migrate", "rebind", "provision", "orphan-stop", "manager-stop":
			return h, "explicit_runtime_recovery", nil
		default:
			return h, "", fail("policy_denied: unknown runtime operation")
		}
	default:
		if p.Path != "" || p.ServiceID != "" || p.GuestPort != 0 || p.HostPort != 0 || p.Listen != "" || !digestPattern.MatchString(p.SpecDigest) || p.Operation != "start" && p.Operation != "stop" && p.Operation != "restart" {
			return h, "", fail("policy_denied: invalid change parameters")
		}
		return h, "change_runtime_resource", nil
	}
}

// Plan persists an exact request without granting or executing it.
func (s *Store) Plan(ctx context.Context, h HostRequest) (Pending, error) {
	if h.Capability == "runtime_change" {
		return Pending{}, fail("policy_denied: use the operator runtime request entry")
	}
	return s.plan(ctx, h, 0)
}

// PlanRuntime records an operator's retained recovery request for one policy revision.
func (s *Store) PlanRuntime(ctx context.Context, h HostRequest, revision uint64) (Pending, error) {
	if h.Capability != "runtime_change" || h.TargetID != s.binding.ProjectID || h.Generation != s.binding.Generation || revision == 0 {
		return Pending{}, fail("policy_denied: invalid runtime request scope")
	}
	return s.plan(ctx, h, revision)
}

func (s *Store) plan(ctx context.Context, h HostRequest, revision uint64) (Pending, error) {
	var out Pending
	h, effect, e := normalise(h)
	if e != nil {
		return out, e
	}
	if revision != 0 && h.Params.Operation == "migrate" {
		// Schema 1 is still authoritative until publication. P4 decodes
		// only start/stop/restart in this slot. The explicit effect and
		// digest bind migration, and P4 cannot execute runtime_change.
		h.Params.Operation = "restart"
		effect = "publish_project_schema_v2"
	}
	e = s.transaction(ctx, func(l *Ledger, n Stamp) error {
		actor := "model"
		if revision != 0 {
			if l.Revision != revision || l.Host[h.Capability] != "approval" {
				return fail("plan_stale: runtime policy changed")
			}
			actor = "operator"
		}
		if l.Host[h.Capability] != "allow" && l.Host[h.Capability] != "approval" {
			return fail("policy_denied: Host capability defaults to deny")
		}
		if e := s.check(ctx); e != nil {
			return e
		}
		if len(l.Pending) >= MaxPending {
			return fail("audit_unavailable: pending budget exhausted")
		}
		r := Request{l.Binding, l.Revision, h, h.TargetID, effect}
		out = Pending{newID(), r, digest(r)}
		l.Pending = append(l.Pending, out)
		return event(l, n, Operation{RequestID: out.ID, Digest: out.Digest, Capability: h.Capability, Actor: actor, Revision: l.Revision, TargetID: h.TargetID, Generation: h.Generation}, "plan", "planned")
	})
	return out, e
}

// Approve grants one exact digest for a bounded monotonic lifetime.
func (s *Store) Approve(ctx context.Context, id, expectedDigest string, ttl time.Duration) (Approval, error) {
	var out Approval
	if !idPattern.MatchString(id) || len(expectedDigest) != 64 || ttl <= 0 || ttl > MaxTTL {
		return out, fail("policy_denied: invalid exact approval/TTL")
	}
	e := s.transaction(ctx, func(l *Ledger, n Stamp) error {
		p := findPending(l, id)
		if p == nil || p.Digest != expectedDigest {
			return fail("request_mismatch")
		}
		if p.Request.PolicyRevision != l.Revision {
			return fail("plan_stale")
		}
		if l.Host[p.Request.Host.Capability] != "approval" {
			return fail("policy_denied: request does not require approval")
		}
		if len(l.Approvals) >= MaxApprovals {
			return fail("audit_unavailable: approval budget exhausted")
		}
		if e := s.check(ctx); e != nil {
			return e
		}
		if n.Tick > int64(^uint64(0)>>1)-int64(ttl) {
			return fail("clock_unavailable")
		}
		out = Approval{ID: newID(), RequestID: id, Digest: p.Digest, Revision: l.Revision, Issued: n, Deadline: n.Tick + int64(ttl)}
		l.Approvals = append(l.Approvals, out)
		return event(l, n, Operation{RequestID: id, ApprovalID: out.ID, Digest: p.Digest, Capability: p.Request.Host.Capability, Actor: "operator", Revision: l.Revision}, "approval", "granted_once")
	})
	return out, e
}

// Revoke prevents future consumption; it does not undo dispatched effects.
func (s *Store) Revoke(ctx context.Context, id string) error {
	if !idPattern.MatchString(id) {
		return fail("policy_denied: invalid approval id")
	}
	return s.transaction(ctx, func(l *Ledger, n Stamp) error {
		a := findApproval(l, id)
		if a == nil {
			return fail("approval_unknown")
		}
		a.Revoked = true
		return event(l, n, Operation{RequestID: a.RequestID, ApprovalID: a.ID, Digest: a.Digest, Actor: "operator", Revision: l.Revision}, "approval", "revoked")
	})
}

// BeginHost consumes approval and commits intent under one project lock.
func (s *Store) BeginHost(ctx context.Context, id, approvalID string, verify ...func(Request) error) (Operation, Request, error) {
	var op Operation
	var req Request
	if !idPattern.MatchString(id) || approvalID != "" && !idPattern.MatchString(approvalID) {
		return op, req, fail("policy_denied: invalid request/approval id")
	}
	e := s.transaction(ctx, func(l *Ledger, n Stamp) error {
		p := findPending(l, id)
		if p == nil {
			return fail("request_unknown")
		}
		req = p.Request
		if req.PolicyRevision != l.Revision {
			return fail("plan_stale")
		}
		for _, o := range l.Operations {
			if o.RequestID == id {
				return fail("request_consumed: inspect operation; do not replay")
			}
		}
		decision := l.Host[req.Host.Capability]
		if req.Host.Capability == "runtime_change" {
			if decision != "approval" {
				return fail("policy_denied: runtime change requires approval")
			}
		}
		if decision != "allow" && decision != "approval" {
			return fail("policy_denied")
		}
		if decision == "allow" && approvalID != "" {
			return fail("request_mismatch")
		}
		if decision == "approval" {
			a := findApproval(l, approvalID)
			if a == nil {
				return fail("approval_required")
			}
			if a.RequestID != id || a.Digest != p.Digest || a.Revision != l.Revision {
				return fail("approval_mismatch")
			}
			if a.Revoked {
				return fail("approval_revoked")
			}
			if a.Used {
				return fail("approval_consumed")
			}
			if a.Issued.Boot != n.Boot || n.Tick < a.Issued.Tick || n.Wall < a.Issued.Wall || n.Tick >= a.Deadline {
				return fail("approval_expired")
			}
			a.Used = true
		}
		if e := s.check(ctx); e != nil {
			return e
		}
		for _, v := range verify {
			if e := v(req); e != nil {
				return e
			}
		}
		// Runtime/registry checks can take time. Consume only after a fresh
		// boot/clock/deadline check, rather than using the earlier load stamp.
		fresh, e := s.sample(l)
		if e != nil {
			return e
		}
		n = fresh
		l.Highwater = n
		if decision == "approval" {
			a := findApproval(l, approvalID)
			if n.Tick >= a.Deadline {
				return fail("approval_expired")
			}
		}
		op = Operation{ID: newID(), RequestID: id, ApprovalID: approvalID, Digest: p.Digest, Capability: req.Host.Capability, Actor: "model", Revision: l.Revision, TargetID: req.Host.TargetID, Generation: req.Host.Generation, Outcome: "intent"}
		return intent(l, n, op)
	})
	return op, req, e
}

func intent(l *Ledger, n Stamp, op Operation) error {
	if len(l.Operations) >= MaxOperations {
		return fail("audit_unavailable: operation budget exhausted")
	}
	l.Operations = append(l.Operations, op)
	return event(l, n, op, "intent", "authorized")
}

// BeginGuest stores only a keyed digest of the complete execution parameters.
// The caller retains exactly that immutable payload through dispatch.
func (s *Store) BeginGuest(ctx context.Context, capability string, payload any) (Operation, error) {
	return s.beginGuest(ctx, capability, payload, "", 0)
}

// BeginGuestResource journals a service identity as typed metadata while keeping
// its complete parameters private. Only fixed P4 Guest capabilities use it.
func (s *Store) BeginGuestResource(ctx context.Context, capability string, payload any, target string, generation uint64) (Operation, error) {
	if capability != "guest.service_start" && capability != "guest.service_stop" || !idPattern.MatchString(target) || generation == 0 {
		return Operation{}, fail("policy_denied: invalid Guest service scope")
	}
	return s.beginGuest(ctx, capability, struct {
		Target     string
		Generation uint64
		Parameters any
	}{target, generation, payload}, target, generation)
}
func (s *Store) beginGuest(ctx context.Context, capability string, payload any, target string, generation uint64) (Operation, error) {
	var op Operation
	if capability != "guest.exec" && capability != "guest.process_start" && capability != "guest.stdin" && capability != "guest.write" && capability != "guest.mkdir" && capability != "guest.remove" && capability != "guest.rename" && capability != "guest.copy" && capability != "guest.terminal_create" && capability != "guest.terminal_write" && capability != "guest.terminal_resize" && capability != "guest.terminal_interrupt" && capability != "guest.terminal_close" && capability != "guest.service_start" && capability != "guest.service_stop" {
		return op, fail("policy_denied: unknown Guest effect")
	}
	e := s.transaction(ctx, func(l *Ledger, n Stamp) error {
		if !l.GuestAllow {
			return fail("policy_denied: Guest effects disabled")
		}
		d, e := privateDigest(l.Key, struct {
			Binding    Binding
			Revision   uint64
			Capability string
			Payload    any
		}{l.Binding, l.Revision, capability, payload})
		if e != nil {
			return e
		}
		if e = s.check(ctx); e != nil {
			return e
		}
		if target == "" {
			target = l.Binding.ProjectID
			generation = l.Binding.Generation
		}
		op = Operation{ID: newID(), Digest: d, Capability: capability, Actor: "guest", Revision: l.Revision, TargetID: target, Generation: generation, Outcome: "intent"}
		return intent(l, n, op)
	})
	return op, e
}

// Finish does not persist raw external errors, outputs or secrets. A result
// write failure means the dispatched action may have happened.
func (s *Store) Finish(ctx context.Context, id, outcome string) error {
	if outcome != "ok" && outcome != "failed" && outcome != "outcome_unknown" {
		return fail("policy_denied: invalid outcome")
	}
	e := s.transaction(ctx, func(l *Ledger, n Stamp) error {
		for i := range l.Operations {
			o := &l.Operations[i]
			if o.ID == id {
				if o.Outcome != "intent" {
					return fail("operation_finished")
				}
				o.Outcome = outcome
				return event(l, n, *o, "result", outcome)
			}
		}
		return fail("operation_unknown")
	})
	if e != nil {
		return UnknownOutcome(id, fmt.Errorf("result durability unavailable: %w", e))
	}
	return nil
}
