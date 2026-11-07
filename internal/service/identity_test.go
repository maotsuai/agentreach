package service

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestIdentityNotBlockedByApplicationHealthLock(t *testing.T) {
	m, _ := fixture(t)
	ctx := context.Background()
	ln, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 8)
	go func() {
		for {
			conn, e := ln.Accept()
			if e != nil {
				return
			}
			accepted <- struct{}{}
			_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
			req, e := http.ReadRequest(bufio.NewReader(conn))
			if e == nil {
				_ = req.Body.Close()
				_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			}
			_ = conn.Close()
		}
	}()
	// The real single-threaded backend now owns the main proxy TCP connection
	// and is awaiting payload; an HTTP health probe will queue behind it.
	primary, e := net.Dial("tcp4", ln.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer primary.Close()
	<-accepted
	spec := validSpec()
	spec.Health = Health{Protocol: "http", Port: uint16(ln.Addr().(*net.TCPAddr).Port), Path: "/", TimeoutMS: 1000}
	spec.GuestPorts = []uint16{spec.Health.Port}
	id := strings.Repeat("a", 32)
	cid := strings.Repeat("e", 64)
	ep := docker.Endpoint{NetworkID: spec.NetworkID, EndpointID: "fixed-endpoint", IPAddress: "127.0.0.1"}
	r := Record{ID: id, Binding: m.cfg.Binding, RequestID: strings.Repeat("b", 32), RequestDigest: Digest(spec), Spec: spec, Digest: Digest(spec), Generation: 1, State: "running", Ready: true, ContainerID: cid, StartedAt: "fixed-start", Endpoint: ep}
	container := map[string]any{
		"Id": cid, "Image": spec.Image, "State": map[string]any{"Running": true, "StartedAt": r.StartedAt},
		"Config":          map[string]any{"User": spec.User, "WorkingDir": spec.Cwd, "Cmd": spec.Argv, "Entrypoint": []string{}, "Env": []string{}, "Labels": map[string]string{"dev.agentreach.service": id, "dev.agentreach.spec": r.Digest, "dev.agentreach.project": "p4"}, "Healthcheck": map[string]any{"Test": []string{"NONE"}}},
		"HostConfig":      map[string]any{"NetworkMode": "p4-network", "IpcMode": "private", "CgroupnsMode": "private", "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "Memory": spec.Resources.Memory, "MemorySwap": spec.Resources.Memory, "NanoCpus": spec.Resources.NanoCPUs, "PidsLimit": spec.Resources.PIDs, "Init": true, "RestartPolicy": map[string]string{"Name": "no"}, "Runtime": "runc", "ShmSize": 16 << 20, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "1048576", "max-file": "1"}}},
		"NetworkSettings": map[string]any{"Networks": map[string]any{"p4-network": ep}},
	}
	backendInspect := make(chan struct{}, 8)
	old := m.cfg.Docker.Client.Run
	m.cfg.Docker.Client.Run = func(c context.Context, cli string, args, env []string) ([]byte, error) {
		if slicesContain(args, "inspect") && args[len(args)-1] == cid {
			backendInspect <- struct{}{}
			return json.Marshal([]any{container})
		}
		return old(c, cli, args, env)
	}
	unlock, e := m.file().Lock(ctx)
	if e != nil {
		t.Fatal(e)
	}
	e = m.save(saved{Binding: m.cfg.Binding, Records: []Record{r}})
	unlock()
	if e != nil {
		t.Fatal(e)
	}
	healthDone := make(chan error, 1)
	go func() { _, e := m.Call(ctx, "inspect", Request{ServiceID: id}); healthDone <- e }()
	<-backendInspect
	time.Sleep(50 * time.Millisecond)
	deadline, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	observed, e := m.IdentityTarget(deadline, id, spec.Health.Port)
	cancel()
	t.Logf("identity result=%+v error=%v while full health awaits the occupied backend", observed, e)
	// Release and join the full observer before any t.Fatal, preserving cleanup.
	_ = primary.Close()
	<-healthDone
	if e != nil || observed.ServiceID != id || observed.EndpointID != ep.EndpointID {
		t.Fatal("application readiness probe blocked post-dial identity")
	}
}
