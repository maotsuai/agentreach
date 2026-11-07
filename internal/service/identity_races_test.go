package service

import (
	"context"
	"encoding/json"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"strings"
	"testing"
)

func identityRecordFixture(t *testing.T) (*Manager, Record) {
	t.Helper()
	m, _ := fixture(t)
	spec := validSpec()
	id := strings.Repeat("a", 32)
	cid := strings.Repeat("e", 64)
	ep := docker.Endpoint{NetworkID: spec.NetworkID, EndpointID: "fixed-endpoint", IPAddress: "127.0.0.1"}
	r := Record{ID: id, Binding: m.cfg.Binding, RequestID: strings.Repeat("b", 32), RequestDigest: Digest(spec), Spec: spec, Digest: Digest(spec), Generation: 1, State: "running", Ready: true, ContainerID: cid, StartedAt: "old-start", Endpoint: ep, OperationID: strings.Repeat("d", 32)}
	unlock, e := m.file().Lock(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	e = m.save(saved{Binding: m.cfg.Binding, Records: []Record{r}})
	unlock()
	if e != nil {
		t.Fatal(e)
	}
	return m, r
}
func identityContainerFixture(r Record, running bool, started string) map[string]any {
	spec := r.Spec
	return map[string]any{
		"Id": r.ContainerID, "Image": spec.Image, "State": map[string]any{"Running": running, "StartedAt": started},
		"Config":          map[string]any{"User": spec.User, "WorkingDir": spec.Cwd, "Cmd": spec.Argv, "Entrypoint": []string{}, "Env": []string{}, "Labels": map[string]string{"dev.agentreach.service": r.ID, "dev.agentreach.spec": r.Digest, "dev.agentreach.project": "p4"}, "Healthcheck": map[string]any{"Test": []string{"NONE"}}},
		"HostConfig":      map[string]any{"NetworkMode": "p4-network", "IpcMode": "private", "CgroupnsMode": "private", "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "Memory": spec.Resources.Memory, "MemorySwap": spec.Resources.Memory, "NanoCpus": spec.Resources.NanoCPUs, "PidsLimit": spec.Resources.PIDs, "Init": true, "RestartPolicy": map[string]string{"Name": "no"}, "Runtime": "runc", "ShmSize": 16 << 20, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "1048576", "max-file": "1"}}},
		"NetworkSettings": map[string]any{"Networks": map[string]any{"p4-network": r.Endpoint}},
	}
}
func holdIdentityObservation(t *testing.T, m *Manager, r Record, running bool, started string) (func(), <-chan error) {
	t.Helper()
	entered := make(chan struct{})
	old := m.cfg.Docker.Client.Run
	m.cfg.Docker.Client.Run = func(ctx context.Context, cli string, args, env []string) ([]byte, error) {
		if slicesContain(args, "inspect") && args[len(args)-1] == r.ContainerID {
			close(entered)
			return json.Marshal([]any{identityContainerFixture(r, running, started)})
		}
		return old(ctx, cli, args, env)
	}
	unlock, e := m.file().Lock(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := m.IdentityTarget(context.Background(), r.ID, r.Spec.Health.Port); done <- e }()
	<-entered
	return unlock, done
}
func TestIdentityReadOnlyFailurePreservesNewStopOperation(t *testing.T) {
	m, r := identityRecordFixture(t)
	unlock, done := holdIdentityObservation(t, m, r, false, r.StartedAt)
	newer := r
	newer.State = "stopped"
	newer.Ready = false
	newer.OperationID = strings.Repeat("c", 32)
	e := m.save(saved{Binding: m.cfg.Binding, Records: []Record{newer}})
	unlock()
	if e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("stopped observation unexpectedly succeeded")
	}
	current, e := m.load()
	if e != nil {
		t.Fatal(e)
	}
	got := current.Records[0]
	t.Logf("retained operation=%s state=%s", got.OperationID, got.State)
	if got.OperationID != newer.OperationID || got.State != "stopped" {
		t.Fatal("read-only failure overwrote newer same-generation stop receipt", got)
	}
}
func TestIdentityReadOnlyFailureCannotLoseNewGeneration(t *testing.T) {
	m, r := identityRecordFixture(t)
	unlock, done := holdIdentityObservation(t, m, r, true, "foreign-start")
	newer := r
	newer.Generation = 2
	newer.StartedAt = "new-approved-start"
	newer.Endpoint.EndpointID = "new-approved-endpoint"
	newer.OperationID = strings.Repeat("c", 32)
	e := m.save(saved{Binding: m.cfg.Binding, Records: []Record{newer}})
	unlock()
	if e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("old identity mismatch unexpectedly succeeded")
	}
	current, e := m.load()
	if e != nil {
		t.Fatal(e)
	}
	got := current.Records[0]
	t.Logf("new generation=%d started=%s state=%s ready=%v", got.Generation, got.StartedAt, got.State, got.Ready)
	if got.Generation != 2 || got.StartedAt != newer.StartedAt || got.Endpoint != newer.Endpoint || got.OperationID != newer.OperationID || got.State != "running" || !got.Ready {
		t.Fatal("old failure retired or overwrote newer approved generation", got)
	}
}
