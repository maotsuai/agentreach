package ports

import (
	"context"
	"errors"
	"fmt"
	"github.com/bojieli/agentreach/internal/policy"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testServer(t *testing.T, resolvers ...func(Target) (Target, error)) (*Client, *Server, *policy.Store, Target) {
	t.Helper()
	base, e := os.MkdirTemp("/tmp", "reach-p4-ports-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	b := policy.Binding{ProjectID: "p4", RecordDigest: strings.Repeat("a", 64), Generation: 1}
	check := func(context.Context) error { return nil }
	paths := func() error { return nil }
	p := policy.New(base, b, check, paths)
	if e = p.Init(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = p.Set(context.Background(), "port_expose", "approval"); e != nil {
		t.Fatal(e)
	}
	addresses, e := net.InterfaceAddrs()
	if e != nil {
		t.Fatal(e)
	}
	ip := ""
	for _, a := range addresses {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			ip = n.IP.String()
			break
		}
	}
	if ip == "" {
		t.Fatal("test requires its Linux Guest network interface")
	}
	target := Target{Binding: b, ServiceID: strings.Repeat("b", 32), Generation: 1, ContainerID: strings.Repeat("c", 64), StartedAt: "test-generation", EndpointID: "fixed-endpoint", Address: ip, Port: 1}
	cfg := Config{Base: base, Binding: b, Policy: p, Check: check, Paths: paths, Resolve: func(_ context.Context, _ string, port uint16) (Target, error) {
		r := target
		r.Port = port
		if len(resolvers) > 0 {
			return resolvers[0](r)
		}
		return r, nil
	}}
	s := New(cfg)
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	c := &Client{Config: cfg}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if e = c.Ping(context.Background()); e == nil {
			break
		}
		select {
		case e = <-done:
			t.Fatal(e)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("manager not ready", e)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		s.Close()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(3 * time.Second):
			t.Error("manager did not stop")
		}
	})
	return c, s, p, target
}
func TestSharedTargetUsesOneObservationPerRound(t *testing.T) {
	b := policy.Binding{ProjectID: "p4", RecordDigest: strings.Repeat("a", 64), Generation: 1}
	target := Target{Binding: b, ServiceID: strings.Repeat("b", 32), Address: "192.0.2.2", Port: 80}
	calls := 0
	s := New(Config{Binding: b, Check: func(context.Context) error { return nil }, Resolve: func(context.Context, string, uint16) (Target, error) { calls++; return target, nil }})
	s.live["one"] = &live{Exposure: Exposure{Target: target}}
	s.live["two"] = &live{Exposure: Exposure{Target: target}}
	s.recheck(context.Background())
	if calls != 1 {
		t.Fatal("duplicate target checks contend for the same resource", calls)
	}
}

func TestFreshObservationRoundAndWaiterCancellation(t *testing.T) {
	b := policy.Binding{ProjectID: "p4", RecordDigest: strings.Repeat("a", 64), Generation: 1}
	target := Target{Binding: b, ServiceID: strings.Repeat("b", 32), Address: "192.0.2.2", Port: 80}
	entered := make(chan int, 4)
	releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var calls atomic.Int32
	s := New(Config{Binding: b, Check: func(context.Context) error { return nil }, Resolve: func(ctx context.Context, _ string, _ uint16) (Target, error) {
		n := int(calls.Add(1))
		entered <- n
		if n > len(releases) {
			return Target{}, fmt.Errorf("unexpected observation round")
		}
		select {
		case <-releases[n-1]:
			return target, nil
		case <-ctx.Done():
			return Target{}, ctx.Err()
		}
	}})
	defer func() {
		for _, c := range releases {
			select {
			case <-c:
			default:
				close(c)
			}
		}
		s.Close()
	}()
	first := make(chan error, 1)
	go func() { first <- s.valid(context.Background(), target) }()
	if <-entered != 1 {
		t.Fatal("initial round")
	}
	second := make(chan error, 1)
	go func() { second <- s.valid(context.Background(), target) }()
	time.Sleep(50 * time.Millisecond)
	close(releases[0])
	if e := <-first; e != nil {
		t.Fatal(e)
	}
	select {
	case n := <-entered:
		if n != 2 {
			t.Fatal(n)
		}
	case e := <-second:
		t.Fatal("post-dial observation reused an earlier snapshot", e)
	case <-time.After(time.Second):
		t.Fatal("fresh round missing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := s.valid(ctx, target); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	close(releases[1])
	if e := <-second; e != nil {
		t.Fatal("cancelled waiter poisoned shared observation", e)
	}
}

func TestQueuedHealthyChecksDoNotRetireListener(t *testing.T) {
	base, e := os.MkdirTemp("/tmp", "reach-p4-observer-queue-")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = os.RemoveAll(base) }()
	b := policy.Binding{ProjectID: "p4", RecordDigest: strings.Repeat("a", 64), Generation: 1}
	p := policy.New(base, b, func(context.Context) error { return nil }, func() error { return nil })
	if e = p.Init(context.Background()); e != nil {
		t.Fatal(e)
	}
	target := Target{Binding: b, ServiceID: strings.Repeat("b", 32), Generation: 1, ContainerID: strings.Repeat("c", 64), StartedAt: "unchanged", EndpointID: "unchanged", Address: "192.0.2.2", Port: 8080}
	var completed atomic.Int32
	entered := make(chan struct{}, 16)
	cfg := Config{Base: base, Binding: b, Policy: p, Paths: func() error { return nil }, Check: func(context.Context) error { return nil }, Resolve: func(ctx context.Context, _ string, _ uint16) (Target, error) {
		entered <- struct{}{}
		select {
		case <-time.After(2800 * time.Millisecond):
			completed.Add(1)
			return target, nil
		case <-ctx.Done():
			return Target{}, ctx.Err()
		}
	}}
	var identityChecks atomic.Int32
	cfg.ResolveIdentity = func(context.Context, string, uint16) (Target, error) { identityChecks.Add(1); return target, nil }
	s := New(cfg)
	defer s.Close()
	ln, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	x := Exposure{ID: strings.Repeat("d", 32), Instance: strings.Repeat("e", 32), Target: target, Address: "127.0.0.1", Port: uint16(ln.Addr().(*net.TCPAddr).Port), OperationID: strings.Repeat("f", 32), State: "open"}
	s.d = disk{Binding: b, Instance: x.Instance, Exposures: []Exposure{x}}
	s.live[x.ID] = &live{Exposure: x, listener: ln, conns: map[net.Conn]bool{}}
	unlock, e := cfg.file().Lock(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	e = cfg.file().Save(s.d)
	unlock()
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 4)
	for i := range 4 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done <- s.valid(ctx, target)
		}()
		if i == 0 {
			<-entered
		}
	}
	time.Sleep(100 * time.Millisecond)
	s.recheck(context.Background())
	for range 4 {
		if cause := <-done; cause != nil {
			t.Fatal(cause)
		}
	}
	if completed.Load() > 2 || identityChecks.Load() != 1 || s.live[x.ID] == nil || s.d.Exposures[0].State != "open" {
		t.Fatal("healthy listener retired by duplicate queue", completed.Load(), s.d.Exposures)
	}
}
func TestRevocationDoesNotWaitForSlowTargetCheck(t *testing.T) {
	var blocked atomic.Bool
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	c, _, p, target := testServer(t, func(r Target) (Target, error) {
		if blocked.Load() {
			once.Do(func() { close(entered) })
			<-release
		}
		return r, nil
	})
	defer close(release)
	backend, e := net.Listen("tcp4", "0.0.0.0:0")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = backend.Close() }()
	target.Port = uint16(backend.Addr().(*net.TCPAddr).Port)
	go func() {
		conn, e := backend.Accept()
		if e == nil {
			defer func() { _ = conn.Close() }()
			_, _ = io.Copy(conn, conn)
		}
	}()
	op := approve(t, p, policy.HostRequest{Capability: "port_expose", TargetID: target.ServiceID, Generation: 1, Params: policy.HostParams{ServiceID: target.ServiceID, GuestPort: target.Port, Listen: "127.0.0.1"}})
	x, e := c.Expose(context.Background(), target, 0, op.ID)
	if e != nil {
		t.Fatal(e)
	}
	conn, e := net.Dial("tcp4", net.JoinHostPort(x.Address, fmtPort(x.Port)))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write([]byte("ok"))
	buf := make([]byte, 2)
	if _, e = io.ReadFull(conn, buf); e != nil {
		t.Fatal(e)
	}
	blocked.Store(true)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("target check did not enter")
	}
	if e = p.Set(context.Background(), "port_expose", "deny"); e != nil {
		t.Fatal(e)
	}
	if _, e = conn.Read(buf); e == nil {
		t.Fatal("revoked stream stayed open behind slow target check")
	}
	var timed net.Error
	if errors.As(e, &timed) && timed.Timeout() {
		t.Fatal("revocation was blocked by target observation", e)
	}
}
func TestResolverDoesNotBlockControlReads(t *testing.T) {
	c, s, p, target := testServer(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	original := s.cfg.Resolve
	s.cfg.Resolve = func(ctx context.Context, id string, port uint16) (Target, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return original(ctx, id, port)
		case <-ctx.Done():
			return Target{}, ctx.Err()
		}
	}
	op := approve(t, p, policy.HostRequest{Capability: "port_expose", TargetID: target.ServiceID, Generation: 1, Params: policy.HostParams{ServiceID: target.ServiceID, GuestPort: target.Port, Listen: "127.0.0.1"}})
	done := make(chan error, 1)
	go func() { _, e := c.Expose(context.Background(), target, 0, op.ID); done <- e }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("resolver not entered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	e := c.Ping(ctx)
	cancel()
	close(release)
	if e != nil {
		t.Fatal("service lookup blocked manager reads", e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
func TestOldManagerCannotOverwriteReplacementRegistry(t *testing.T) {
	c, s, _, _ := testServer(t)
	d, e := c.load()
	if e != nil {
		t.Fatal(e)
	}
	d.Instance = strings.Repeat("f", 32)
	unlock, e := c.file().Lock(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	e = c.file().Save(d)
	unlock()
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	after, e := c.load()
	if e != nil || after.Instance != d.Instance {
		t.Fatal("old close overwrote new owner", after, e)
	}
}

func TestShutdownCancelsAndJoinsObservers(t *testing.T) {
	c, s, p, target := testServer(t)
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	cleaned := make(chan struct{})
	s.cfg.Resolve = func(ctx context.Context, _ string, _ uint16) (Target, error) {
		close(entered)
		defer close(cleaned)
		<-ctx.Done()
		close(cancelled)
		<-release // deferred resource/lock cleanup may outlive cancellation
		return Target{}, ctx.Err()
	}
	op := approve(t, p, policy.HostRequest{Capability: "port_expose", TargetID: target.ServiceID, Generation: 1, Params: policy.HostParams{ServiceID: target.ServiceID, GuestPort: target.Port, Listen: "127.0.0.1"}})
	dispatched := make(chan error, 1)
	go func() { _, e := c.Expose(context.Background(), target, 0, op.ID); dispatched <- e }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("observer did not enter")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("shutdown did not cancel observation")
	}
	select {
	case <-closed:
		close(release)
		t.Fatal("manager exited before observer cleanup")
	default:
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not join observer")
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("observer cleanup missing")
	}
	<-dispatched
}
func TestStrictControlShapes(t *testing.T) {
	for _, q := range []request{{Op: "list", OperationID: strings.Repeat("a", 32)}, {Op: "shutdown", Port: 1234}, {Op: "close", ExposureID: strings.Repeat("a", 32), OperationID: strings.Repeat("b", 32), Target: Target{Port: 1}}, {Op: "expose", OperationID: "bad", Target: Target{ServiceID: strings.Repeat("a", 32), Port: 80}}} {
		if e := validateRequest(q); e == nil {
			t.Fatal("ignored control fields", q)
		}
	}
	if e := New(Config{}).Serve(context.Background()); e == nil {
		t.Fatal("invalid manager config accepted")
	}
}
func approve(t *testing.T, p *policy.Store, h policy.HostRequest) policy.Operation {
	t.Helper()
	ctx := context.Background()
	plan, e := p.Plan(ctx, h)
	if e != nil {
		t.Fatal(e)
	}
	a, e := p.Approve(ctx, plan.ID, plan.Digest, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	op, _, e := p.BeginHost(ctx, plan.ID, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	return op
}
func TestRealListenerHalfCloseReplayAndRelease(t *testing.T) {
	c, _, p, target := testServer(t)
	backend, e := net.Listen("tcp4", "0.0.0.0:0")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = backend.Close() }()
	target.Port = uint16(backend.Addr().(*net.TCPAddr).Port)
	go func() {
		conn, e := backend.Accept()
		if e != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		b, _ := io.ReadAll(conn)
		_, _ = conn.Write(append([]byte("reply:"), b...))
	}()
	op := approve(t, p, policy.HostRequest{Capability: "port_expose", TargetID: target.ServiceID, Generation: 1, Params: policy.HostParams{ServiceID: target.ServiceID, GuestPort: target.Port, Listen: "127.0.0.1"}})
	x, e := c.Expose(context.Background(), target, 0, op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if x.Address != "127.0.0.1" || x.Port == 0 {
		t.Fatal(x)
	}
	conn, e := net.DialTimeout("tcp4", net.JoinHostPort(x.Address, fmtPort(x.Port)), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	payload := strings.Repeat("half-close-stream", 16000)
	_, e = io.WriteString(conn, payload)
	if e != nil {
		t.Fatal(e)
	}
	_ = conn.(*net.TCPConn).CloseWrite()
	got, e := io.ReadAll(conn)
	_ = conn.Close()
	if e != nil || string(got) != "reply:"+payload {
		t.Fatal(len(got), e)
	}
	if _, e = c.Expose(context.Background(), target, 0, op.ID); e == nil {
		t.Fatal("operation replayed")
	}
	closeOp := approve(t, p, policy.HostRequest{Capability: "port_expose", TargetID: x.ID, Generation: 1, Params: policy.HostParams{Operation: "close", ServiceID: target.ServiceID, Listen: "127.0.0.1"}})
	if _, e = c.Close(context.Background(), x.ID, closeOp.ID); e != nil {
		t.Fatal(e)
	}
	rebound, e := net.Listen("tcp4", net.JoinHostPort(x.Address, fmtPort(x.Port)))
	if e != nil {
		t.Fatal("listener leaked", e)
	}
	_ = rebound.Close()
}
func fmtPort(p uint16) string { return fmt.Sprint(p) }
func TestConcurrentAutoConflictAndRevocation(t *testing.T) {
	c, _, p, target := testServer(t)
	backend, e := net.Listen("tcp4", "0.0.0.0:0")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = backend.Close() }()
	target.Port = uint16(backend.Addr().(*net.TCPAddr).Port)
	go func() {
		for {
			conn, e := backend.Accept()
			if e != nil {
				return
			}
			go func() { defer func() { _ = conn.Close() }(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	create := func(port uint16) policy.Operation {
		return approve(t, p, policy.HostRequest{Capability: "port_expose", TargetID: target.ServiceID, Generation: 1, Params: policy.HostParams{ServiceID: target.ServiceID, GuestPort: target.Port, HostPort: port, Listen: "127.0.0.1"}})
	}
	a, b := create(0), create(0)
	type result struct {
		x Exposure
		e error
	}
	ch := make(chan result, 2)
	for _, op := range []policy.Operation{a, b} {
		go func(op policy.Operation) {
			x, e := c.Expose(context.Background(), target, 0, op.ID)
			ch <- result{x, e}
		}(op)
	}
	x, y := <-ch, <-ch
	if x.e != nil || y.e != nil || x.x.Port == y.x.Port {
		t.Fatal(x, y)
	}
	conflict := create(x.x.Port)
	if _, e = c.Expose(context.Background(), target, x.x.Port, conflict.ID); e == nil {
		t.Fatal("conflict accepted")
	}
	conn, e := net.Dial("tcp4", net.JoinHostPort(x.x.Address, fmtPort(x.x.Port)))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write([]byte("identity"))
	buf := make([]byte, 8)
	if _, e = io.ReadFull(conn, buf); e != nil {
		t.Fatal(e)
	}
	if e = p.Set(context.Background(), "port_expose", "deny"); e != nil {
		t.Fatal(e)
	}
	if _, e = conn.Read(buf); e == nil {
		t.Fatal("established stream survived revocation")
	}
}
