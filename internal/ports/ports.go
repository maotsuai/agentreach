// Package ports owns bounded IPv4 loopback listeners in an independent Host
// process. Targets are verified service identities, never arbitrary addresses.
package ports

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/privatecontrol"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/state"
	"github.com/bojieli/agentreach/internal/terminal"
)

// Resource budgets limit listeners, connections, frames and idle streams.
const (
	MaxExposures   = 16
	MaxConnections = 64
	MaxFrame       = 64 << 10
	IdleTimeout    = 30 * time.Second
	RevokeInterval = time.Second
)

// Target pins the complete verified service endpoint.
type Target struct {
	Binding     policy.Binding `json:"binding"`
	ServiceID   string         `json:"service_id"`
	Generation  uint64         `json:"generation"`
	ContainerID string         `json:"container_id"`
	StartedAt   string         `json:"started_at"`
	EndpointID  string         `json:"endpoint_id"`
	Address     string         `json:"address"`
	Port        uint16         `json:"port"`
}

// Exposure binds an actual listener to a service and manager instance.
type Exposure struct {
	ID            string `json:"exposure_id"`
	Instance      string `json:"instance"`
	Target        Target `json:"target"`
	Address       string `json:"address"`
	Port          uint16 `json:"port"`
	RequestedPort uint16 `json:"requested_port"`
	OperationID   string `json:"operation_id"`
	State         string `json:"state"`
	Reason        string `json:"reason,omitempty"`
}

// IdentityError retires a caller connection after a manager authority mismatch.
type IdentityError struct{ Cause error }

func (e *IdentityError) Error() string {
	return fmt.Sprintf("identity_changed: private manager: %v", e.Cause)
}
func (e *IdentityError) Unwrap() error { return e.Cause }

type disk struct {
	Binding   policy.Binding `json:"binding"`
	Instance  string         `json:"instance"`
	Exposures []Exposure     `json:"exposures"`
}
type request struct {
	Op          string         `json:"op"`
	Instance    string         `json:"instance"`
	Binding     policy.Binding `json:"binding"`
	Target      Target         `json:"target"`
	OperationID string         `json:"operation_id"`
	ExposureID  string         `json:"exposure_id"`
	Port        uint16         `json:"port"`
}
type response struct {
	Exposure  Exposure   `json:"exposure"`
	Exposures []Exposure `json:"exposures,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// Config supplies only trusted binding, authorization and identity callbacks.
type Config struct {
	Base string
	// SocketBase keeps Unix addresses short while v2 disk state is namespaced.
	// Only trusted session construction supplies this canonical Host directory.
	SocketBase string
	Binding    policy.Binding
	Check      func(context.Context) error
	Paths      func() error
	Policy     *policy.Store
	Resolve    func(context.Context, string, uint16) (Target, error)
	// ResolveIdentity verifies the same complete identity without application
	// readiness. Drivers providing only Resolve retain their stronger callback.
	ResolveIdentity func(context.Context, string, uint16) (Target, error)
}

func (cfg Config) file() privatecontrol.File {
	return privatecontrol.File{Root: cfg.Base, Path: filepath.Join(cfg.Base, "ports", cfg.Binding.ProjectID+".json"), Max: 1 << 20, Check: func() error {
		if e := cfg.validateStorage(); e != nil {
			return e
		}
		return cfg.Paths()
	}}
}
func (cfg Config) validate() error {
	if e := cfg.validateStorage(); e != nil {
		return e
	}
	if cfg.Check == nil || cfg.Policy == nil || cfg.Resolve == nil {
		return fmt.Errorf("policy_denied: missing manager authority callbacks")
	}
	return nil
}

func (cfg Config) validateStorage() error {
	_, e := hex.DecodeString(cfg.Binding.RecordDigest)
	if !project.ValidID(cfg.Binding.ProjectID) || len(cfg.Binding.RecordDigest) != 64 || e != nil || cfg.Binding.Generation == 0 || cfg.Paths == nil {
		return fmt.Errorf("policy_denied: invalid private manager configuration")
	}
	if cfg.SocketBase != "" {
		if e := privatecontrol.Dir(cfg.SocketBase); e != nil {
			return e
		}
		if e := privatecontrol.Dir(filepath.Dir(cfg.socket())); e != nil {
			return e
		}
	}
	return nil
}
func (cfg Config) socket() string {
	base := cfg.SocketBase
	if base == "" {
		base = cfg.Base
	}
	return filepath.Join(base, "ports", cfg.Binding.RecordDigest[:16]+".sock")
}
func (cfg Config) load() (disk, error) {
	var d disk
	e := cfg.file().Read(&d)
	if os.IsNotExist(e) {
		return disk{Binding: cfg.Binding, Exposures: []Exposure{}}, nil
	}
	if e != nil {
		return d, e
	}
	if d.Binding != cfg.Binding || !terminal.ValidID(d.Instance) || len(d.Exposures) > MaxExposures {
		return d, fmt.Errorf("state_corrupt: ports binding/budget")
	}
	seen := map[string]bool{}
	ops := map[string]bool{}
	for _, x := range d.Exposures {
		if !terminal.ValidID(x.ID) || !terminal.ValidID(x.Instance) || !terminal.ValidID(x.OperationID) || x.Target.Binding != cfg.Binding || seen[x.ID] || ops[x.OperationID] || x.Address != "127.0.0.1" {
			return d, fmt.Errorf("state_corrupt: exposure identity")
		}
		seen[x.ID] = true
		ops[x.OperationID] = true
		switch x.State {
		case "creating", "open", "closed", "unavailable":
		default:
			return d, fmt.Errorf("state_corrupt: exposure state")
		}
	}
	return d, nil
}

// Client never starts a manager for reads or ordinary Guest commands.
type Client struct {
	Config
	Start func() error
}

func (c *Client) call(ctx context.Context, q request) (response, error) {
	var out response
	if e := c.file().Safe(); e != nil {
		return out, e
	}
	d, e := c.load()
	if e != nil {
		return out, e
	}
	if d.Instance == "" {
		return out, fmt.Errorf("exposure_unavailable: Host manager absent")
	}
	if q.Instance != "" && q.Instance != d.Instance {
		return out, &IdentityError{fmt.Errorf("recorded manager instance changed")}
	}
	q.Instance = d.Instance
	q.Binding = c.Binding
	conn, e := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", c.socket())
	if e != nil {
		return out, fmt.Errorf("exposure_unavailable: Host manager unavailable: %w", e)
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(20 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if e = json.NewEncoder(conn).Encode(q); e != nil {
		return out, e
	}
	b, e := bufio.NewReader(io.LimitReader(conn, MaxFrame+1)).ReadBytes('\n')
	if e != nil {
		return out, e
	}
	if len(b) > MaxFrame || !utf8.Valid(b) {
		return out, fmt.Errorf("request_budget: manager response")
	}
	if e = state.Decode(b, &out); e != nil {
		return out, e
	}
	if out.Error != "" {
		err := fmt.Errorf("%s", out.Error)
		if strings.HasPrefix(out.Error, "identity_changed:") {
			return out, &IdentityError{err}
		}
		return out, err
	}
	return out, nil
}

// Expose dispatches one durable intent and never retries a possibly sent request.
func (c *Client) Expose(ctx context.Context, t Target, port uint16, op string) (Exposure, error) {
	if e := c.validate(); e != nil {
		return Exposure{}, e
	}
	// Start only after the service layer has durably consumed this exact plan.
	if c.Start != nil {
		if probe, e := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "unix", c.socket()); e != nil {
			if e = c.Start(); e != nil {
				return Exposure{}, e
			}
		} else {
			_ = probe.Close()
		}
	}
	var r response
	var e error
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, e = c.call(ctx, request{Op: "expose", Target: t, Port: port, OperationID: op})
		if e == nil || time.Now().After(deadline) {
			break
		}
		// Only a manager that has not yet published its control identity may be
		// awaited. Never retry a request that might have reached its listener.
		d, err := c.load()
		if err != nil || d.Instance != "" {
			break
		}
		select {
		case <-ctx.Done():
			return r.Exposure, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return r.Exposure, e
}

// List reports unavailable when the owning manager cannot be observed.
func (c *Client) List(ctx context.Context) ([]Exposure, error) {
	d, e := c.load()
	if e != nil {
		return nil, e
	}
	if d.Instance == "" {
		return []Exposure{}, nil
	}
	r, e := c.call(ctx, request{Op: "list"})
	if e != nil {
		var identity *IdentityError
		if errors.As(e, &identity) {
			return nil, e
		}
		for i := range d.Exposures {
			if d.Exposures[i].State == "open" || d.Exposures[i].State == "creating" {
				d.Exposures[i].State = "unavailable"
			}
		}
		return d.Exposures, nil
	}
	return r.Exposures, nil
}

// Inspect finds only retained same-project exposure identities.
func (c *Client) Inspect(ctx context.Context, id string) (Exposure, error) {
	if !terminal.ValidID(id) {
		return Exposure{}, fmt.Errorf("policy_denied: exposure ID")
	}
	xs, e := c.List(ctx)
	if e != nil {
		return Exposure{}, e
	}
	for _, x := range xs {
		if x.ID == id {
			return x, nil
		}
	}
	return Exposure{}, fmt.Errorf("exposure_unknown")
}

// Close revokes the exact approved listener and all its established streams.
func (c *Client) Close(ctx context.Context, id, op string) (Exposure, error) {
	r, e := c.call(ctx, request{Op: "close", ExposureID: id, OperationID: op})
	return r.Exposure, e
}

// Shutdown requests private manager exit after all live listeners are closed.
func (c *Client) Shutdown(ctx context.Context) error {
	_, e := c.call(ctx, request{Op: "shutdown"})
	return e
}

// Ping verifies the currently recorded manager instance.
func (c *Client) Ping(ctx context.Context) error { _, e := c.call(ctx, request{Op: "list"}); return e }

type live struct {
	Exposure
	listener net.Listener
	conns    map[net.Conn]bool
}
type observation struct {
	started time.Time
	done    chan struct{}
	err     error
}
type observationKey struct {
	Target    Target
	Readiness bool
}

// Server owns listeners and established streams independently of clients.
type Server struct {
	cfg            Config
	mu             sync.Mutex
	d              disk
	live           map[string]*live
	control        net.Listener
	active         chan struct{}
	controls       chan struct{}
	stop           chan struct{}
	once           sync.Once
	published      bool
	socketInfo     os.FileInfo
	verify         chan struct{}
	identityVerify chan struct{}
	workers        sync.WaitGroup
	background     sync.WaitGroup
	runContext     context.Context
	cancel         context.CancelFunc
	observations   map[observationKey]*observation
}

// New constructs an unstarted manager without creating state.
func New(cfg Config) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	if cfg.ResolveIdentity == nil {
		cfg.ResolveIdentity = cfg.Resolve
	}
	return &Server{cfg: cfg, live: map[string]*live{}, active: make(chan struct{}, MaxConnections), controls: make(chan struct{}, 8), stop: make(chan struct{}), verify: make(chan struct{}, 1), identityVerify: make(chan struct{}, 1), runContext: ctx, cancel: cancel, observations: map[observationKey]*observation{}}
}
func (s *Server) save() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	unlock, e := s.cfg.file().Lock(ctx)
	if e != nil {
		return e
	}
	defer unlock()
	current, e := s.cfg.load()
	if e != nil {
		return e
	}
	if current.Instance != s.d.Instance {
		return fmt.Errorf("identity_changed: Host manager registry replaced")
	}
	return s.cfg.file().Save(s.d)
}
func (s *Server) replace(x Exposure) {
	for i := range s.d.Exposures {
		if s.d.Exposures[i].ID == x.ID {
			s.d.Exposures[i] = x
			return
		}
	}
	s.d.Exposures = append(s.d.Exposures, x)
}
func (s *Server) valid(ctx context.Context, t Target) error {
	return s.verifyTarget(ctx, t, true)
}
func (s *Server) identity(ctx context.Context, t Target) error { return s.verifyTarget(ctx, t, false) }
func (s *Server) verifyTarget(ctx context.Context, t Target, readiness bool) error {
	started := time.Now()
	key := observationKey{t, readiness}
	for {
		s.mu.Lock()
		select {
		case <-s.stop:
			s.mu.Unlock()
			return fmt.Errorf("exposure_unavailable: manager stopped")
		default:
		}
		flight := s.observations[key]
		if flight == nil {
			flight = &observation{started: time.Now(), done: make(chan struct{})}
			s.observations[key] = flight
			// Registration shares the stop/Close synchronisation domain.
			s.workers.Add(1)
			go s.observeTarget(key, flight)
		}
		fresh := !flight.started.Before(started)
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.stop:
			return fmt.Errorf("exposure_unavailable: manager stopped")
		case <-flight.done:
			if fresh {
				return flight.err
			}
			// This observation started before our request (or before dial).
			// Join the next round rather than reusing a pre-existing snapshot.
		}
	}
}
func (s *Server) observeTarget(key observationKey, flight *observation) {
	defer s.workers.Done()
	// A caller's cancellation only abandons its wait. It cannot cancel a
	// shared observation or poison other connections using the same target.
	ctx, cancel := context.WithTimeout(s.runContext, 10*time.Second)
	defer cancel()
	err := s.checkTarget(ctx, key.Target, key.Readiness)
	s.mu.Lock()
	flight.err = err
	delete(s.observations, key)
	close(flight.done)
	s.mu.Unlock()
}
func (s *Server) checkTarget(ctx context.Context, t Target, readiness bool) error {
	gate := s.verify
	if !readiness {
		gate = s.identityVerify
	}
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stop:
		return fmt.Errorf("exposure_unavailable: manager stopped")
	}
	if e := s.cfg.Check(ctx); e != nil {
		return e
	}
	resolve := s.cfg.Resolve
	if !readiness {
		resolve = s.cfg.ResolveIdentity
	}
	current, e := resolve(ctx, t.ServiceID, t.Port)
	if e != nil {
		return e
	}
	if current != t {
		return fmt.Errorf("identity_changed: proxy target changed")
	}
	ip := net.ParseIP(t.Address)
	if t.Binding != s.cfg.Binding || ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || t.Port == 0 {
		return fmt.Errorf("policy_denied: invalid verified service endpoint")
	}
	return nil
}
func (s *Server) authorize(q request, closing bool) error {
	v, e := s.cfg.Policy.Inspect()
	if e != nil {
		return e
	}
	return authorizeView(v, q, closing)
}
func authorizeView(v policy.View, q request, closing bool) error {
	var op *policy.Operation
	for i := range v.Operations {
		if v.Operations[i].ID == q.OperationID {
			op = &v.Operations[i]
		}
	}
	if op == nil || op.Capability != "port_expose" || op.Outcome != "intent" && op.Outcome != "ok" || op.Revision != v.Revision || v.Host["port_expose"] == "deny" {
		return fmt.Errorf("policy_denied: durable exposure authorization unavailable")
	}
	for _, a := range v.Approvals {
		if a.ID == op.ApprovalID && (a.Revoked || !a.Used) {
			return fmt.Errorf("approval_revoked")
		}
	}
	for _, p := range v.Pending {
		if p.ID == op.RequestID {
			h := p.Request.Host
			if closing {
				if h.Params.Operation == "close" && h.TargetID == q.ExposureID {
					return nil
				}
			} else if h.TargetID == q.Target.ServiceID && h.Generation == q.Target.Generation && h.Params.ServiceID == q.Target.ServiceID && h.Params.GuestPort == q.Target.Port && h.Params.HostPort == q.Port && h.Params.Listen == "127.0.0.1" && h.Params.Operation != "close" {
				return nil
			}
		}
	}
	return fmt.Errorf("request_mismatch: exposure operation")
}
func (s *Server) closeLive(x *live, state string) {
	_ = x.listener.Close()
	for c := range x.conns {
		_ = c.Close()
	}
	x.State = state
	if state == "closed" {
		x.Reason = "closed"
	} else if x.Reason == "" {
		x.Reason = "manager_closed"
	}
	s.replace(x.Exposure)
	delete(s.live, x.ID)
}
func (s *Server) handle(ctx context.Context, q request) response {
	out := response{}
	fail := func(e error) response { out.Error = e.Error(); return out }
	if e := validateRequest(q); e != nil {
		return fail(e)
	}
	if q.Instance != s.d.Instance || q.Binding != s.cfg.Binding {
		return fail(fmt.Errorf("identity_changed: Host manager instance/binding"))
	}
	// Never hold the listener mutex while invoking a service resolver. A
	// browser/close operation may hold the service lock while querying us.
	if q.Op != "shutdown" && q.Op != "recovery_status" {
		if e := s.cfg.Check(ctx); e != nil {
			return fail(e)
		}
	}
	if q.Op == "expose" {
		if e := s.authorize(q, false); e != nil {
			return fail(e)
		}
		if e := s.valid(ctx, q.Target); e != nil {
			return fail(e)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stop:
		return fail(fmt.Errorf("exposure_unavailable: manager stopping"))
	default:
	}
	if q.Op == "shutdown" {
		if len(s.live) != 0 {
			return fail(fmt.Errorf("policy_denied: close exposures before shutdown"))
		}
		return out
	}
	switch q.Op {
	case "list", "recovery_status":
		out.Exposures = append([]Exposure{}, s.d.Exposures...)
	case "expose":
		for _, x := range s.d.Exposures {
			if x.OperationID == q.OperationID {
				out.Exposure = x
				return fail(fmt.Errorf("request_consumed: exposure intent is never replayed"))
			}
		}
		if len(s.d.Exposures) >= MaxExposures {
			return fail(fmt.Errorf("request_budget: exposures"))
		}
		if e := s.authorize(q, false); e != nil {
			return fail(e)
		}
		id, e := terminal.NewID()
		if e != nil {
			return fail(e)
		}
		x := Exposure{ID: id, Instance: s.d.Instance, Target: q.Target, Address: "127.0.0.1", RequestedPort: q.Port, OperationID: q.OperationID, State: "creating"}
		s.replace(x)
		out.Exposure = x
		if e = s.save(); e != nil {
			return fail(e)
		}
		ln, e := (&net.ListenConfig{}).Listen(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(q.Port))))
		if e != nil {
			x.State = "unavailable"
			x.Reason = "bind_unavailable"
			s.replace(x)
			_ = s.save()
			return fail(e)
		}
		address, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			_ = ln.Close()
			return fail(fmt.Errorf("identity_changed: unexpected listener family"))
		}
		x.Port = uint16(address.Port)
		x.State = "open"
		s.replace(x)
		out.Exposure = x
		if e = s.save(); e != nil {
			_ = ln.Close()
			x.State = "unavailable"
			x.Reason = "state_unavailable"
			s.replace(x)
			out.Exposure = x
			return fail(e)
		}
		l := &live{Exposure: x, listener: ln, conns: map[net.Conn]bool{}}
		s.live[id] = l
		s.workers.Add(1)
		go func() { defer s.workers.Done(); s.accept(l) }()
	case "close":
		if e := s.authorize(q, true); e != nil {
			return fail(e)
		}
		found := false
		for _, x := range s.d.Exposures {
			if x.ID == q.ExposureID {
				found = true
				out.Exposure = x
				if l := s.live[x.ID]; l != nil {
					s.closeLive(l, "closed")
					out.Exposure = l.Exposure
				} else {
					x.State = "closed"
					x.Reason = "closed"
					s.replace(x)
					out.Exposure = x
				}
				break
			}
		}
		if !found {
			return fail(fmt.Errorf("exposure_unknown"))
		}
		if e := s.save(); e != nil {
			return fail(e)
		}
	default:
		return fail(fmt.Errorf("policy_denied: unknown Host manager request"))
	}
	return out
}

// Serve publishes one private control socket and never restores old listeners.
func (s *Server) Serve(ctx context.Context) error {
	if e := s.cfg.validate(); e != nil {
		return e
	}
	if len(s.cfg.socket()) > 100 {
		return fmt.Errorf("unsupported: private Unix socket path too long")
	}
	if e := s.cfg.Check(ctx); e != nil {
		return e
	}
	defer s.Close()
	unlock, e := s.cfg.file().Lock(ctx)
	if e != nil {
		return e
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	d, e := s.cfg.load()
	if e != nil {
		return e
	}
	if e = privatecontrol.Dir(filepath.Dir(s.cfg.socket())); e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(s.cfg.socket()), 0700); e != nil {
		return e
	}
	s.mu.Lock()
	setupLocked := true
	defer func() {
		if setupLocked {
			s.mu.Unlock()
		}
	}()
	select {
	case <-s.stop:
		return fmt.Errorf("exposure_unavailable: manager already stopped")
	default:
	}
	s.d = d
	// A refused connection plus the exact owned socket inode permits replacing
	// a dead control endpoint. No PID signalling or approval replay is involved.
	if fi, e := os.Lstat(s.cfg.socket()); e == nil {
		if fi.Mode()&os.ModeSocket == 0 || !privatecontrol.Private(fi) {
			return fmt.Errorf("state_corrupt: unsafe control socket")
		}
		conn, e := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", s.cfg.socket())
		if e == nil {
			_ = conn.Close()
			return fmt.Errorf("state_locked: Host manager already running")
		}
		if !errors.Is(e, syscall.ECONNREFUSED) {
			return fmt.Errorf("exposure_unavailable: cannot prove stale control socket: %w", e)
		}
		now, e := os.Lstat(s.cfg.socket())
		if e != nil || !os.SameFile(fi, now) {
			return fmt.Errorf("identity_changed: stale control socket changed")
		}
		if e = os.Remove(s.cfg.socket()); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	ln, e := (&net.ListenConfig{}).Listen(ctx, "unix", s.cfg.socket())
	if e != nil {
		return e
	}
	s.control = ln
	if unix, ok := ln.(*net.UnixListener); ok {
		unix.SetUnlinkOnClose(false)
	}
	s.socketInfo, e = os.Lstat(s.cfg.socket())
	if e != nil {
		_ = ln.Close()
		return e
	}
	if e = os.Chmod(s.cfg.socket(), 0600); e != nil {
		return e
	}
	s.d.Instance, e = terminal.NewID()
	if e != nil {
		return e
	}
	for i := range s.d.Exposures {
		if s.d.Exposures[i].State != "closed" {
			s.d.Exposures[i].State = "unavailable"
			s.d.Exposures[i].Reason = "manager_restarted"
		}
	}
	if e = s.cfg.file().Save(s.d); e != nil {
		return e
	}
	s.published = true
	runCtx := s.runContext
	stopContext := context.AfterFunc(ctx, s.cancel)
	defer stopContext()
	s.background.Add(2)
	unlock()
	unlock = nil
	setupLocked = false
	s.mu.Unlock()
	go func() { defer s.background.Done(); s.monitor(runCtx) }()
	go func() { defer s.background.Done(); s.monitorAuthorization(runCtx) }()
	go func() { <-runCtx.Done(); s.Close() }()
	for {
		conn, e := ln.Accept()
		if e != nil {
			select {
			case <-s.stop:
				return nil
			default:
				return e
			}
		}
		s.mu.Lock()
		select {
		case <-s.stop:
			s.mu.Unlock()
			_ = conn.Close()
			return nil
		default:
		}
		select {
		case s.controls <- struct{}{}:
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				defer func() { <-s.controls; _ = conn.Close() }()
				stop := context.AfterFunc(runCtx, func() { _ = conn.Close() })
				defer stop()
				_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
				b, e := bufio.NewReader(io.LimitReader(conn, MaxFrame+1)).ReadBytes('\n')
				var q request
				out := response{}
				if e != nil || len(b) > MaxFrame || !utf8.Valid(b) {
					out.Error = "request_budget: Host manager frame"
				} else if e = state.Decode(b, &q); e != nil {
					out.Error = e.Error()
				} else {
					c, cancel := context.WithTimeout(runCtx, 15*time.Second)
					out = s.handle(c, q)
					cancel()
				}
				_ = json.NewEncoder(conn).Encode(out)
				if q.Op == "shutdown" && out.Error == "" {
					go s.Close()
				}
			}()
		default:
			_ = conn.Close()
		}
		s.mu.Unlock()
	}
}

// Close revokes all established streams before releasing listeners.
func (s *Server) Close() {
	s.once.Do(func() {
		s.mu.Lock()
		close(s.stop)
		if s.cancel != nil {
			s.cancel()
		}
		for _, x := range s.live {
			s.closeLive(x, "unavailable")
		}
		if s.control != nil {
			_ = s.control.Close()
		}
		if s.published {
			_ = s.save()
		}
		s.mu.Unlock()
		// No worker may be registered after stop closes under mu. Wait before
		// returning to the process entrypoint so observers release private locks.
		s.background.Wait()
		s.workers.Wait()
		if s.socketInfo != nil {
			if now, e := os.Lstat(s.cfg.socket()); e == nil && os.SameFile(now, s.socketInfo) {
				_ = os.Remove(s.cfg.socket())
			}
		}
	})
}
func (s *Server) monitor(ctx context.Context) {
	ticker := time.NewTicker(RevokeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
			s.recheck(ctx)
		}
	}
}
func (s *Server) recheck(ctx context.Context) {
	s.mu.Lock()
	groups := map[Target][]*live{}
	for _, x := range s.live {
		groups[x.Target] = append(groups[x.Target], x)
	}
	s.mu.Unlock()
	// Shared targets are observed once per round, rather than contending for
	// the same service record lock with duplicate expensive observations.
	for target, xs := range groups {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		e := s.identity(c, target)
		reason := "target_unavailable"
		if c.Err() != nil {
			reason = "target_check_timeout"
		} else if e != nil && strings.HasPrefix(e.Error(), "state_locked:") {
			reason = "target_busy"
		}
		cancel()
		if e != nil {
			s.mu.Lock()
			changed := false
			for _, x := range xs {
				if s.live[x.ID] == x {
					x.Reason = reason
					s.closeLive(x, "unavailable")
					changed = true
				}
			}
			if changed {
				_ = s.save()
			}
			s.mu.Unlock()
		}
	}
}
func (s *Server) monitorAuthorization(ctx context.Context) {
	ticker := time.NewTicker(RevokeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
			// Revocation never waits behind Docker/readiness target observations.
			s.mu.Lock()
			xs := make([]*live, 0, len(s.live))
			for _, x := range s.live {
				xs = append(xs, x)
			}
			s.mu.Unlock()
			if len(xs) == 0 {
				continue
			}
			view, e := s.cfg.Policy.Inspect()
			s.mu.Lock()
			changed := false
			for _, x := range xs {
				cause := e
				if cause == nil {
					cause = authorizeView(view, request{Target: x.Target, Port: x.RequestedPort, OperationID: x.OperationID}, false)
				}
				if cause != nil && s.live[x.ID] == x {
					x.Reason = "approval_unavailable"
					s.closeLive(x, "unavailable")
					changed = true
				}
			}
			if changed {
				_ = s.save()
			}
			s.mu.Unlock()
		}
	}
}
func (s *Server) accept(l *live) {
	for {
		client, e := l.listener.Accept()
		if e != nil {
			return
		}
		s.mu.Lock()
		if s.live[l.ID] != l {
			s.mu.Unlock()
			_ = client.Close()
			return
		}
		select {
		case s.active <- struct{}{}:
			s.workers.Add(1)
			go func() { defer s.workers.Done(); s.forward(l, client) }()
		default:
			_ = client.Close()
		}
		s.mu.Unlock()
	}
}

type idleConn struct{ net.Conn }

func (c idleConn) Read(b []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(IdleTimeout))
	return c.Conn.Read(b)
}
func (c idleConn) Write(b []byte) (int, error) {
	_ = c.SetWriteDeadline(time.Now().Add(IdleTimeout))
	return c.Conn.Write(b)
}
func (s *Server) forward(l *live, client net.Conn) {
	defer func() { <-s.active; _ = client.Close() }()
	ctx, cancel := context.WithTimeout(s.runContext, 10*time.Second)
	defer cancel()
	s.mu.Lock()
	if s.live[l.ID] != l {
		s.mu.Unlock()
		return
	}
	l.conns[client] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(l.conns, client); s.mu.Unlock() }()
	if e := s.authorize(request{Target: l.Target, Port: l.RequestedPort, OperationID: l.OperationID}, false); e != nil {
		return
	}
	if e := s.valid(ctx, l.Target); e != nil {
		return
	}
	target, e := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp4", net.JoinHostPort(l.Target.Address, strconv.Itoa(int(l.Target.Port))))
	if e != nil {
		return
	}
	defer func() { _ = target.Close() }()
	if e = s.identity(ctx, l.Target); e != nil {
		return
	}
	s.mu.Lock()
	if s.live[l.ID] != l {
		s.mu.Unlock()
		return
	}
	l.conns[target] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(l.conns, target); s.mu.Unlock() }()
	done := make(chan struct{})
	go func() {
		_, _ = io.CopyBuffer(idleConn{target}, idleConn{client}, make([]byte, 16<<10))
		if t, ok := target.(*net.TCPConn); ok {
			_ = t.CloseWrite()
		}
		close(done)
	}()
	_, _ = io.CopyBuffer(idleConn{client}, idleConn{target}, make([]byte, 16<<10))
	if c, ok := client.(*net.TCPConn); ok {
		_ = c.CloseWrite()
	}
	<-done
}

func validateRequest(q request) error {
	switch q.Op {
	case "list", "shutdown", "recovery_status":
		if q.Target != (Target{}) || q.OperationID != "" || q.ExposureID != "" || q.Port != 0 {
			return fmt.Errorf("policy_denied: unexpected manager parameters")
		}
	case "expose":
		if !terminal.ValidID(q.OperationID) || !terminal.ValidID(q.Target.ServiceID) || q.Target.Port == 0 || q.ExposureID != "" {
			return fmt.Errorf("policy_denied: exposure request identity")
		}
	case "close":
		if !terminal.ValidID(q.OperationID) || !terminal.ValidID(q.ExposureID) || q.Target != (Target{}) || q.Port != 0 {
			return fmt.Errorf("policy_denied: close request identity")
		}
	default:
		return fmt.Errorf("policy_denied: unknown Host manager request")
	}
	return nil
}
