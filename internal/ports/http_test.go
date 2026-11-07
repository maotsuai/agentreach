package ports

import (
	"bufio"
	"context"
	"fmt"
	"github.com/bojieli/agentreach/internal/policy"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func regressionSerialHTTP(t *testing.T) (net.Listener, *atomic.Int32) {
	t.Helper()
	ln, e := net.Listen("tcp4", "0.0.0.0:0")
	if e != nil {
		t.Fatal(e)
	}
	applications := new(atomic.Int32)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, e := ln.Accept()
			if e != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
			req, e := http.ReadRequest(bufio.NewReader(conn))
			if e == nil {
				if req.URL.Path == "/application" {
					applications.Add(1)
				}
				_ = req.Body.Close()
				_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			}
			_ = conn.Close()
		}
	}()
	return ln, applications
}

func regressionHTTPFixture(t *testing.T) (*Client, *Server, *policy.Store, Target, *atomic.Int32) {
	t.Helper()
	ln, applications := regressionSerialHTTP(t)
	healthClient := &http.Client{Timeout: 250 * time.Millisecond, Transport: &http.Transport{DisableKeepAlives: true}}
	c, s, p, target := testServer(t, func(r Target) (Target, error) {
		response, e := healthClient.Get("http://" + net.JoinHostPort(r.Address, fmtPort(r.Port)) + "/health")
		if e != nil {
			return Target{}, e
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 200 {
			return Target{}, fmt.Errorf("health status %d", response.StatusCode)
		}
		return r, nil
	})
	target.Port = uint16(ln.Addr().(*net.TCPAddr).Port)
	return c, s, p, target, applications
}

func regressionInstallIdentity(s *Server, f func(context.Context, string, uint16) (Target, error)) {
	s.cfg.ResolveIdentity = f
}

func regressionExpose(t *testing.T, c *Client, p *policy.Store, target Target) Exposure {
	t.Helper()
	op := approve(t, p, policy.HostRequest{Capability: "port_expose", TargetID: target.ServiceID, Generation: target.Generation, Params: policy.HostParams{ServiceID: target.ServiceID, GuestPort: target.Port, Listen: "127.0.0.1"}})
	x, e := c.Expose(context.Background(), target, 0, op.ID)
	if e != nil {
		t.Fatal(e)
	}
	return x
}

func regressionGET(x Exposure) (string, error) {
	c := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	r, e := c.Get("http://" + net.JoinHostPort(x.Address, fmtPort(x.Port)) + "/application")
	if e != nil {
		return "", e
	}
	defer r.Body.Close()
	b, e := io.ReadAll(r.Body)
	if e == nil && r.StatusCode != 200 {
		e = fmt.Errorf("status %d", r.StatusCode)
	}
	return string(b), e
}

func TestProxySingleThreadedHTTPDoesNotDeadlock(t *testing.T) {
	c, s, p, target, applications := regressionHTTPFixture(t)
	regressionInstallIdentity(s, func(context.Context, string, uint16) (Target, error) { return target, nil })
	x := regressionExpose(t, c, p, target)
	body, e := regressionGET(x)
	t.Logf("application_requests=%d body=%q error=%v", applications.Load(), body, e)
	if e != nil || body != "ok" || applications.Load() != 1 {
		t.Fatal("proxy request blocked by its own post-dial HTTP health probe")
	}
}

func TestProxyPayloadWaitsForPostDialIdentity(t *testing.T) {
	c, s, p, target, applications := regressionHTTPFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	regressionInstallIdentity(s, func(ctx context.Context, _ string, _ uint16) (Target, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return target, nil
		case <-ctx.Done():
			return Target{}, ctx.Err()
		}
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	x := regressionExpose(t, c, p, target)
	done := make(chan error, 1)
	go func() {
		body, e := regressionGET(x)
		if e == nil && body != "ok" {
			e = fmt.Errorf("body %q", body)
		}
		done <- e
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("post-dial identity check did not run")
	}
	time.Sleep(100 * time.Millisecond)
	if applications.Load() != 0 {
		t.Fatal("application payload escaped before post-dial identity confirmation")
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if applications.Load() != 1 {
		t.Fatal("request not delivered after confirmation")
	}
}

func TestProxyPostDialIdentityMismatchSendsNoPayload(t *testing.T) {
	c, s, p, target, applications := regressionHTTPFixture(t)
	regressionInstallIdentity(s, func(context.Context, string, uint16) (Target, error) {
		changed := target
		changed.EndpointID = "foreign-endpoint"
		return changed, nil
	})
	x := regressionExpose(t, c, p, target)
	_, e := regressionGET(x)
	if e == nil || applications.Load() != 0 {
		t.Fatal("mismatched post-dial target admitted payload", e, applications.Load())
	}
}

func TestProxyMonitorIdentityDoesNotWaitForConcurrentHTTPHealth(t *testing.T) {
	ln, _ := regressionSerialHTTP(t)
	healthClient := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{DisableKeepAlives: true}}
	entered := make(chan struct{})
	var once sync.Once
	var armed atomic.Bool
	c, s, p, target := testServer(t, func(r Target) (Target, error) {
		if armed.Load() {
			once.Do(func() { close(entered) })
		}
		response, e := healthClient.Get("http://" + net.JoinHostPort(r.Address, fmtPort(r.Port)) + "/health")
		if e != nil {
			return Target{}, e
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return r, nil
	})
	target.Port = uint16(ln.Addr().(*net.TCPAddr).Port)
	regressionInstallIdentity(s, func(context.Context, string, uint16) (Target, error) { return target, nil })
	x := regressionExpose(t, c, p, target)
	primary, e := net.Dial("tcp4", net.JoinHostPort(target.Address, fmtPort(target.Port)))
	if e != nil {
		t.Fatal(e)
	}
	defer primary.Close()
	armed.Store(true)
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done <- s.valid(ctx, target)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("concurrent HTTP readiness did not enter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	s.recheck(ctx)
	cancel()
	s.mu.Lock()
	state, reason := s.d.Exposures[0].State, s.d.Exposures[0].Reason
	s.mu.Unlock()
	_ = primary.Close()
	<-done
	t.Logf("monitor state=%s reason=%s exposure=%s", state, reason, x.ID)
	if state != "open" {
		t.Fatal("ongoing readiness HTTP probe blocked identity monitor")
	}
}
