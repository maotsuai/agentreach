package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/terminal"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validSpec() Spec {
	return Spec{Schema: 1, ProjectID: "p4", Driver: "sidecar", Image: "sha256:" + strings.Repeat("a", 64), Argv: []string{"/usr/bin/server", "literal$VALUE", ";touch /unwanted"}, Cwd: "/tmp", Env: map[string]string{}, NetworkID: strings.Repeat("b", 64), User: "10001:10001", Resources: Resources{128 << 20, 500000000, 64}, Health: Health{Protocol: "tcp", Port: 8080, TimeoutMS: 100}, Restart: "no", LogBytes: 1 << 20, GuestPorts: []uint16{8080}}
}

func TestOtherApprovedPortNeedsActualListener(t *testing.T) {
	ln, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	spec := validSpec()
	spec.Health.Port = 1
	spec.GuestPorts = []uint16{1, port}
	r := Record{Spec: spec, Ready: true, Endpoint: docker.Endpoint{IPAddress: "127.0.0.1"}}
	if !portReady(context.Background(), r, port) {
		_ = ln.Close()
		t.Fatal("approved listening port rejected")
	}
	_ = ln.Close()
	if portReady(context.Background(), r, port) {
		t.Fatal("primary readiness substituted for closed secondary port")
	}
	if portReady(context.Background(), r, 2) {
		t.Fatal("unapproved port accepted")
	}
}
func TestStrictSpecAndShapes(t *testing.T) {
	s := validSpec()
	if e := s.Validate("p4"); e != nil {
		t.Fatal(e)
	}
	tests := []func(*Spec){func(s *Spec) { s.ProjectID = "other" }, func(s *Spec) { s.Driver = "compose" }, func(s *Spec) { s.User = "0:0" }, func(s *Spec) { s.Env = map[string]string{"API_TOKEN": "secret"} }, func(s *Spec) { s.Publish = []Publication{{8080, 8080, "0.0.0.0"}} }, func(s *Spec) { s.Health.Port = 8081 }, func(s *Spec) { s.Argv = []string{"x\x00"} }, func(s *Spec) { s.Resources.PIDs = 0 }}
	for i, change := range tests {
		v := validSpec()
		change(&v)
		if e := v.Validate("p4"); e == nil {
			t.Fatal("accepted", i)
		}
	}
	for _, b := range []string{`{"service_id":"a","service_id":"b"}`, `{"compose":"arbitrary"}`, `{"spec":{"privileged":true}}`, `{} {}`} {
		if _, e := Decode([]byte(b)); e == nil {
			t.Fatal("accepted", b)
		}
	}
	if e := validateRequest("inspect", Request{ServiceID: strings.Repeat("a", 32), RequestID: strings.Repeat("b", 32)}); e == nil {
		t.Fatal("ignored request field")
	}
}
func fixture(t *testing.T) (*Manager, *int) {
	t.Helper()
	base := t.TempDir()
	if e := os.Chmod(base, 0700); e != nil {
		t.Fatal(e)
	}
	config := filepath.Join(base, "docker")
	if e := os.MkdirAll(filepath.Join(config, "cli-plugins"), 0700); e != nil {
		t.Fatal(e)
	}
	for p, v := range map[string]string{filepath.Join(config, "config.json"): "{}", filepath.Join(config, "cli-plugins", "docker-compose"): "trusted-fixture"} {
		if e := os.WriteFile(p, []byte(v), 0700); e != nil {
			t.Fatal(e)
		}
	}
	spec := validSpec()
	binding := policy.Binding{ProjectID: "p4", RecordDigest: strings.Repeat("c", 64), Generation: 1}
	check := func(context.Context) error { return nil }
	paths := func() error { return nil }
	p := policy.New(base, binding, check, paths)
	if e := p.Init(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := p.Set(context.Background(), "service_change", "allow"); e != nil {
		t.Fatal(e)
	}
	workspace := strings.Repeat("d", 64)
	cid := strings.Repeat("e", 64)
	mutations := 0
	var created Record
	running := false
	client := &docker.Client{Connection: docker.Connection{ConfigDir: config}, Run: func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
		a := args[4:]
		encode := func(v any) ([]byte, error) { return json.Marshal(v) }
		if a[0] == "network" {
			return encode([]any{map[string]any{"Id": spec.NetworkID, "Name": "p4-network", "Driver": "bridge", "Labels": map[string]string{"dev.agentreach.project": "p4"}}})
		}
		if a[0] == "image" {
			return encode([]any{map[string]any{"Id": spec.Image, "Config": map[string]any{"Env": []string{"PATH=/usr/bin:/bin"}}}})
		}
		if a[0] == "compose" {
			if slicesContain(a, "version") {
				return []byte("5.5.0"), nil
			}
			file := ""
			for i, v := range a {
				if v == "-f" {
					file = a[i+1]
				}
			}
			if slicesContain(a, "config") {
				return os.ReadFile(file)
			}
			if slicesContain(a, "create") {
				mutations++
				return nil, fmt.Errorf("injected transport failure after create dispatch")
			}
			if slicesContain(a, "ps") {
				return []byte(cid), nil
			}
		}
		if a[0] == "inspect" {
			if a[len(a)-1] == workspace {
				return encode([]any{map[string]any{"Id": workspace, "Config": map[string]string{"User": "10001:10001"}, "NetworkSettings": map[string]any{"Networks": map[string]any{"p4-network": map[string]string{"NetworkID": spec.NetworkID, "EndpointID": "workspace-endpoint", "IPAddress": "192.0.2.1"}}}}})
			}
			return encode([]any{map[string]any{"Id": cid, "Image": created.Spec.Image, "State": map[string]any{"Running": running}}})
		}
		if a[0] == "start" {
			mutations++
			running = true
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected Docker %v", a)
	}}
	driver := &DockerDriver{Client: client, Project: project.Record{Spec: project.Spec{ID: "p4"}, Binding: project.Binding{ContainerID: workspace}}, Base: base, Paths: paths}
	return New(Config{Base: base, Binding: binding, Policy: p, Check: check, Paths: paths, Docker: driver}), &mutations
}
func TestWorkspaceLogsPreserveTerminalDiagnostics(t *testing.T) {
	zero := 0
	for _, tt := range []struct {
		name, state, gap, cursor, failOp, wantErr string
		data, descendants                         string
		exit                                      *int
		complete                                  bool
	}{
		{name: "empty", state: "running"},
		{name: "exited", state: "exited", data: "ok", complete: true, exit: &zero, descendants: "unconfirmed"},
		{name: "closed before drain", state: "closed", gap: "closed_before_drain"},
		{name: "output limit", state: "exited", gap: "output_limit", data: "ok"},
		{name: "missing log", state: "lost", gap: "log_missing"},
		{name: "old cursor", state: "exited", cursor: "old", wantErr: "output_gap: cursor instance/epoch"},
		{name: "inspect failure", state: "running", gap: "backend_unavailable", failOp: "inspect", wantErr: "transport failure"},
		{name: "read failure", state: "running", gap: "backend_unavailable", failOp: "read", wantErr: "transport failure"},
		{name: "inspect save failure", state: "running", failOp: "save-inspect", wantErr: "terminal save failure"},
		{name: "read save failure", state: "running", failOp: "save-read", wantErr: "terminal save failure"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := fixture(t)
			ctx := context.Background()
			calls := 0
			failSave := false
			m.cfg.Terminal = terminal.New(terminal.Config{
				Base: m.cfg.Base, Binding: m.cfg.Binding, Runtime: strings.Repeat("f", 64),
				Policy: m.cfg.Policy, Check: m.cfg.Check,
				Paths: func() error {
					if failSave {
						return fmt.Errorf("terminal save failure")
					}
					return m.cfg.Paths()
				},
				Launch: func(context.Context, string, []string, string) (string, error) { return "fixture", nil },
				Dispatch: func(_ context.Context, q terminal.GuestRequest) (terminal.Observation, error) {
					calls++
					o := terminal.Observation{Identity: q.Identity, State: "running"}
					if q.Op == "create" {
						o.Identity.Server, o.Identity.Session, o.Identity.Pane = "boot:123:456", "$0", "%0"
						return o, nil
					}
					if q.Op == tt.failOp {
						return terminal.Observation{}, fmt.Errorf("transport failure")
					}
					o.State, o.Complete, o.ExitCode, o.Descendants = tt.state, tt.complete, tt.exit, tt.descendants
					if tt.failOp == "" {
						o.Gap = tt.gap
					}
					o.Offset = int64(len(tt.data))
					if q.Op == "read" {
						o.Data = []byte(tt.data)
					}
					if q.Op == "inspect" && tt.failOp == "save-inspect" {
						failSave = true
						o.Data = []byte("unexpected inspect data")
					}
					if q.Op == "read" && tt.failOp == "save-read" {
						failSave = true
						o.Data, o.Offset = []byte("x"), 1
					}
					return o, nil
				},
			})
			v, err := m.cfg.Terminal.Call(ctx, "create", terminal.Request{RequestID: strings.Repeat("1", 32), Argv: []string{"fixture"}, Cols: 80, Rows: 24})
			if err != nil {
				t.Fatal(err)
			}
			backend := v.(terminal.Result)
			spec := validSpec()
			spec.Driver, spec.Image, spec.LogBytes = "workspace", "", terminal.MaxLog
			r := Record{
				ID: strings.Repeat("2", 32), RequestID: strings.Repeat("3", 32), Binding: m.cfg.Binding,
				Spec: spec, Digest: Digest(spec), Generation: 1, State: "running", TerminalID: backend.TerminalID,
				ContainerID: m.cfg.Docker.Project.Binding.ContainerID,
				Endpoint:    docker.Endpoint{NetworkID: spec.NetworkID, EndpointID: "workspace-endpoint", IPAddress: "192.0.2.1"},
			}
			unlock, err := m.file().Lock(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = m.save(saved{Binding: m.cfg.Binding, Records: []Record{r}})
			unlock()
			if err != nil {
				t.Fatal(err)
			}
			v, err = m.Call(ctx, "logs", Request{ServiceID: r.ID, MaxBytes: max(1, len(tt.data)), Cursor: tt.cursor})
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("logs error = %v, want %q", err, tt.wantErr)
			}
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Service     Record `json:"service"`
				Data        []byte `json:"data"`
				Next        string `json:"next_cursor"`
				Earliest    string `json:"earliest_available_cursor"`
				Gap         string `json:"gap"`
				Complete    bool   `json:"complete"`
				ExitCode    *int   `json:"exit_code"`
				Descendants string `json:"descendants"`
			}
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			prefix := backend.TerminalID + ":" + backend.Epoch + ":"
			var next string
			if tt.wantErr == "" {
				next = fmt.Sprintf("%s%d", prefix, len(tt.data))
			}
			if got.Service.ID != r.ID || got.Earliest != prefix+"0" || got.Next != next ||
				string(got.Data) != tt.data || got.Gap != tt.gap || got.Complete != tt.complete || got.Descendants != tt.descendants ||
				(got.ExitCode == nil) != (tt.exit == nil) || got.ExitCode != nil && *got.ExitCode != *tt.exit {
				t.Fatalf("unexpected log response: %s", b)
			}
			if tt.state == "lost" {
				previousCalls := calls
				v, err := m.Call(ctx, "logs", Request{ServiceID: r.ID, MaxBytes: MaxRead})
				retained := v.(Result)
				if err == nil || calls != previousCalls || retained.Service.ID != r.ID || retained.Gap != got.Gap || retained.EarliestCursor != got.Earliest || len(retained.Data) != 0 || retained.Cursor != "" {
					t.Fatalf("unexpected repeated read after loss: %+v, %v", retained, err)
				}
			}
		})
	}
}

func slicesContain(a []string, v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}
func TestCreateUnknownNeverAutomaticallyReplayed(t *testing.T) {
	m, counter := fixture(t)
	ctx := context.Background()
	s := validSpec()
	q := Request{RequestID: strings.Repeat("f", 32), Spec: &s}
	v, e := m.Call(ctx, "plan", q)
	if e != nil {
		t.Fatal(e)
	}
	r := v.(Result).Service
	if r.Pending == nil {
		t.Fatal(r)
	}
	op, e := m.ExecuteHost(ctx, r.Pending.ID, "")
	if e == nil || op.ID == "" || !strings.Contains(e.Error(), "outcome_unknown") || *counter != 1 {
		t.Fatal(op, e, *counter)
	}
	fresh := New(m.cfg)
	if _, e = fresh.ExecuteHost(ctx, r.Pending.ID, ""); e == nil || *counter != 1 {
		t.Fatal("replayed unknown", e, *counter)
	}
	state, e := fresh.load()
	if e != nil {
		t.Fatal(e)
	}
	if state.Records[0].OperationID != op.ID || state.Records[0].State != "outcome_unknown" {
		t.Fatal(state)
	}
}

func TestConcurrentHostDuplicatePreservesReceipt(t *testing.T) {
	for _, outcome := range []string{"outcome_unknown", "ok"} {
		t.Run(outcome, func(t *testing.T) {
			m, counter := fixture(t)
			ctx := context.Background()
			spec := validSpec()
			v, e := m.Call(ctx, "plan", Request{RequestID: strings.Repeat("f", 32), Spec: &spec})
			if e != nil {
				t.Fatal(e)
			}
			r := v.(Result).Service
			unlock, e := m.file().Lock(ctx)
			if e != nil {
				t.Fatal(e)
			}
			type reply struct {
				op  policy.Operation
				err error
			}
			results := make(chan reply, 2)
			for range 2 {
				go func() { op, e := m.ExecuteHost(ctx, r.Pending.ID, ""); results <- reply{op, e} }()
			}
			time.Sleep(300 * time.Millisecond)
			if outcome == "ok" {
				// Simulate another already-authorized dispatcher finishing while
				// both calls are queued. The authority is the actual ledger.
				op, _, cause := m.cfg.Policy.BeginHost(ctx, r.Pending.ID, "")
				if cause != nil {
					unlock()
					t.Fatal(cause)
				}
				if cause = m.cfg.Policy.Finish(ctx, op.ID, "ok"); cause != nil {
					unlock()
					t.Fatal(cause)
				}
			}
			unlock()
			a, b := <-results, <-results
			if a.op.ID == "" || a.op.ID != b.op.ID || a.err == nil || b.err == nil || a.op.Outcome != outcome || b.op.Outcome != outcome {
				t.Fatal(a, b)
			}
			want := 1
			if outcome == "ok" {
				want = 0
			}
			if *counter != want {
				t.Fatal("replayed consumed operation", *counter)
			}
		})
	}
}
func TestTamperedGeneratedComposeHasNoMutation(t *testing.T) {
	m, counter := fixture(t)
	ctx := context.Background()
	s := validSpec()
	v, e := m.Call(ctx, "plan", Request{RequestID: strings.Repeat("f", 32), Spec: &s})
	if e != nil {
		t.Fatal(e)
	}
	r := v.(Result).Service
	file := m.cfg.Docker.file(*r)
	if e = os.WriteFile(file, []byte(`{"services":{"app":{"privileged":true}}}`), 0600); e != nil {
		t.Fatal(e)
	}
	op, e := m.ExecuteHost(ctx, r.Pending.ID, "")
	if e == nil || op.ID != "" || *counter != 0 {
		t.Fatal(op, e, *counter)
	}
	ledger, e := m.cfg.Policy.Inspect()
	if e != nil || len(ledger.Operations) != 0 {
		t.Fatal(ledger.Operations, e)
	}
}
func TestSnapshotNormalisesOnlyDisabledOOMDefault(t *testing.T) {
	c := docker.ServiceContainer{Config: json.RawMessage(`{"Env":["A=1"]}`)}
	c.HostConfig = json.RawMessage(`{"OomKillDisable":false,"Memory":134217728}`)
	before := snapshot(c)
	c.HostConfig = json.RawMessage(`{"OomKillDisable":null,"Memory":134217728}`)
	if snapshot(c) != before {
		t.Fatal("engine false/null default changed service identity")
	}
	c.HostConfig = json.RawMessage(`{"OomKillDisable":true,"Memory":134217728}`)
	if snapshot(c) == before {
		t.Fatal("unsafe OOM setting collapsed")
	}
	c.HostConfig = json.RawMessage(`{"OomKillDisable":false,"Memory":268435456}`)
	if snapshot(c) == before {
		t.Fatal("memory limit change collapsed")
	}
}
func TestIntentStateFailureNeverDispatchesAndKeepsOperation(t *testing.T) {
	m, counter := fixture(t)
	ctx := context.Background()
	s := validSpec()
	v, e := m.Call(ctx, "plan", Request{RequestID: strings.Repeat("f", 32), Spec: &s})
	if e != nil {
		t.Fatal(e)
	}
	r := v.(Result).Service
	m.write = func(string, any) error { return fmt.Errorf("injected ENOSPC") }
	op, e := m.ExecuteHost(ctx, r.Pending.ID, "")
	if e == nil || op.ID == "" || *counter != 0 {
		t.Fatal(op, e, *counter)
	}
	again, e := New(m.cfg).ExecuteHost(ctx, r.Pending.ID, "")
	if e == nil || again.ID != op.ID || *counter != 0 {
		t.Fatal(again, e, *counter)
	}
}
func TestAbsentReadDoesNotCreateServiceStorage(t *testing.T) {
	m, _ := fixture(t)
	if _, e := m.Call(context.Background(), "list", Request{}); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Lstat(filepath.Join(m.cfg.Base, "services")); !os.IsNotExist(e) {
		t.Fatal("read created service storage", e)
	}
	if _, e := m.Call(context.Background(), "inspect", Request{ServiceID: strings.Repeat("a", 32)}); e == nil {
		t.Fatal("foreign service accepted")
	}
	if _, e := os.Lstat(filepath.Join(m.cfg.Base, "services")); !os.IsNotExist(e) {
		t.Fatal("unknown inspect created service storage", e)
	}
}
func TestMutationIdentityFailureLatchesConnection(t *testing.T) {
	m, counter := fixture(t)
	ctx := context.Background()
	s := validSpec()
	v, e := m.Call(ctx, "plan", Request{RequestID: strings.Repeat("f", 32), Spec: &s})
	if e != nil {
		t.Fatal(e)
	}
	r := v.(Result).Service
	run := m.cfg.Docker.Client.Run
	m.cfg.Docker.Client.Run = func(ctx context.Context, cli string, args, env []string) ([]byte, error) {
		b, e := run(ctx, cli, args, env)
		if slicesContain(args, "create") {
			return b, docker.Fail("identity_changed", "injected backend mutation identity mismatch")
		}
		return b, e
	}
	op, e := m.ExecuteHost(ctx, r.Pending.ID, "")
	if e == nil || op.ID == "" || *counter != 1 || m.Failure() == nil {
		t.Fatal(op, e, *counter, m.Failure())
	}
	if e = m.Ready(ctx); e == nil {
		t.Fatal("identity failure did not latch")
	}
	stored, e := m.load()
	if e != nil {
		t.Fatal(e)
	}
	if stored.Records[0].State != "lost" {
		t.Fatal(stored.Records[0])
	}
}
func TestBackendLossDoesNotRetireUnrelatedObject(t *testing.T) {
	m, _ := fixture(t)
	a := Record{ID: strings.Repeat("a", 32), State: "running", Ready: true}
	b := Record{ID: strings.Repeat("b", 32), State: "running", Ready: true}
	m.failed(&a, docker.Fail("identity_changed", "specific backend changed"))
	if e := m.observe(context.Background(), &b); e == nil || a.State != "lost" || b.State != "unavailable" || b.Ready {
		t.Fatal(a, b, e)
	}
}
func TestUnknownWorkspaceStopUsesDurableResourceReceipt(t *testing.T) {
	m, counter := fixture(t)
	ctx := context.Background()
	spec := validSpec()
	spec.Driver = "workspace"
	spec.Image = ""
	spec.Publish = nil
	id := strings.Repeat("a", 32)
	r := Record{ID: id, Binding: m.cfg.Binding, RequestID: strings.Repeat("b", 32), RequestDigest: Digest(spec), Spec: spec, Digest: Digest(spec), Generation: 1, State: "running"}
	unlock, e := m.file().Lock(ctx)
	if e != nil {
		t.Fatal(e)
	}
	e = m.save(saved{Binding: m.cfg.Binding, Records: []Record{r}})
	unlock()
	if e != nil {
		t.Fatal(e)
	}
	op, e := m.cfg.Policy.BeginGuestResource(ctx, "guest.service_stop", "private stop payload", id, 1)
	if e != nil {
		t.Fatal(e)
	}
	// The object record deliberately lacks the operation: simulate failure of
	// its write after durable ledger intent and before any backend dispatch.
	value, e := New(m.cfg).Call(ctx, "stop", Request{ServiceID: id})
	result, ok := value.(Result)
	if e == nil || !ok || result.Service.OperationID != op.ID || *counter != 0 {
		t.Fatal(value, e, *counter)
	}
	ledger, e := m.cfg.Policy.Inspect()
	if e != nil || len(ledger.Operations) != 1 || ledger.Operations[0].TargetID != id {
		t.Fatal(ledger.Operations, e)
	}
}

func TestRetiredConnectionRejectsRepeatedServiceReceipts(t *testing.T) {
	for _, op := range []string{"plan", "start", "stop"} {
		t.Run(op, func(t *testing.T) {
			m, counter := fixture(t)
			ctx := context.Background()
			spec := validSpec()
			id := strings.Repeat("a", 32)
			r := Record{ID: id, Binding: m.cfg.Binding, RequestID: strings.Repeat("b", 32), RequestDigest: Digest(spec), Spec: spec, Digest: Digest(spec), Generation: 1, State: "planned", OperationID: strings.Repeat("c", 32)}
			q := Request{ServiceID: id}
			if op == "plan" || op == "start" {
				q = Request{RequestID: r.RequestID, Spec: &spec}
			}
			if op != "plan" {
				spec.Driver = "workspace"
				spec.Image = ""
				r.Spec = spec
				r.Digest = Digest(spec)
				r.RequestDigest = Digest(spec)
				r.State = "stopped"
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
			m.retire(&BackendIdentityError{ServiceID: id, Cause: fmt.Errorf("lost backend")})
			value, e := m.Call(ctx, op, q)
			result, ok := value.(Result)
			if e == nil || !ok || result.Service.ID != id || result.Service.OperationID != r.OperationID || *counter != 0 {
				t.Fatal("retired connection accepted duplicate or lost receipt", value, e, *counter)
			}
			stored, e := m.load()
			if e != nil || stored.Records[0].State != "lost" || stored.Records[0].Ready {
				t.Fatal("guard failure was not persisted", stored, e)
			}
		})
	}
}

func TestGuardRuntimeLossPersistsAcrossNewManager(t *testing.T) {
	m, _ := fixture(t)
	ctx := context.Background()
	spec := validSpec()
	id := strings.Repeat("a", 32)
	r := Record{ID: id, Binding: m.cfg.Binding, RequestID: strings.Repeat("b", 32), RequestDigest: Digest(spec), Spec: spec, Digest: Digest(spec), Generation: 1, State: "running", Ready: true}
	unlock, e := m.file().Lock(ctx)
	if e != nil {
		t.Fatal(e)
	}
	e = m.save(saved{Binding: m.cfg.Binding, Records: []Record{r}})
	unlock()
	if e != nil {
		t.Fatal(e)
	}
	m.cfg.Check = func(context.Context) error { return docker.Fail("generation_mismatch", "workspace restarted") }
	value, e := m.Call(ctx, "inspect", Request{ServiceID: id})
	if e == nil || value.(Result).Service.State != "lost" {
		t.Fatal(value, e)
	}
	m.cfg.Check = func(context.Context) error { return nil }
	value, e = New(m.cfg).Call(ctx, "inspect", Request{ServiceID: id})
	if e == nil || value.(Result).Service.State != "lost" || value.(Result).Service.Ready {
		t.Fatal("runtime identity revived", value, e)
	}
}
func TestToolSchemasNeverEmitNullRequired(t *testing.T) {
	b, e := json.Marshal(Tools())
	if e != nil {
		t.Fatal(e)
	}
	var root any
	if e = json.Unmarshal(b, &root); e != nil {
		t.Fatal(e)
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if r, yes := x["required"]; yes {
				if _, ok := r.([]any); !ok {
					t.Fatalf("invalid required: %v", r)
				}
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(root)
}
func TestUnknownWorkspaceStartRecoversOperationBeforeDispatch(t *testing.T) {
	m, counter := fixture(t)
	ctx := context.Background()
	spec := validSpec()
	spec.Driver = "workspace"
	spec.Image = ""
	spec.Publish = nil
	id := strings.Repeat("a", 32)
	req := strings.Repeat("b", 32)
	r := Record{ID: id, Binding: m.cfg.Binding, RequestID: req, RequestDigest: Digest(spec), Spec: spec, Digest: Digest(spec), Generation: 1, State: "planned"}
	unlock, e := m.file().Lock(ctx)
	if e != nil {
		t.Fatal(e)
	}
	e = m.save(saved{Binding: m.cfg.Binding, Records: []Record{r}})
	unlock()
	if e != nil {
		t.Fatal(e)
	}
	op, e := m.cfg.Policy.BeginGuestResource(ctx, "guest.service_start", spec, id, 1)
	if e != nil {
		t.Fatal(e)
	}
	v, e := New(m.cfg).Call(ctx, "start", Request{RequestID: req, Spec: &spec})
	out, ok := v.(Result)
	if e == nil || !ok || out.Service.OperationID != op.ID || *counter != 0 {
		t.Fatal(v, e, *counter)
	}
}
