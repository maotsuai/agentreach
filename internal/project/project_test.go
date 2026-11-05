package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bojieli/agentreach/internal/state"
)

func exampleRecord(t *testing.T) Record {
	t.Helper()
	var s Spec
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "orbstack", "project.example.json"))
	if err == nil {
		err = state.Decode(data, &s)
	}
	if err != nil {
		t.Fatal(err)
	}
	root := canonicalTempDir(t)
	s.Docker.CLI = filepath.Join(root, "docker")
	s.Docker.ConfigDir = filepath.Join(root, "config")
	s.Docker.ControlHome = root
	s.OrbStackConfig = filepath.Join(root, ".orbstack", "vmconfig.json")
	s.EngineDataPath = filepath.Join(root, "engine")
	if s.Workspace.Mode == "shared-project" {
		s.Workspace.HostPath = filepath.Join(root, "project")
	}
	return Record{Spec: s, Binding: Binding{Schema: Schema, Revision: 1, Generation: 1, ContainerID: strings.Repeat("a", 64), ImageID: "sha256:image", DaemonID: "approved-daemon", StartedAt: "2026-10-31T00:00:00Z", SnapshotSHA256: strings.Repeat("b", 64), RegisteredAt: time.Now()}}
}

func TestRegistrationCannotOverwriteBinding(t *testing.T) {
	t.Setenv("REACH_HOME", canonicalTempDir(t))
	r := exampleRecord(t)
	if err := Register(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.Binding.ContainerID = strings.Repeat("c", 64)
	if err := Register(context.Background(), r); err == nil {
		t.Fatal("registration rebound an existing project")
	}
	loaded, err := Load(r.Spec.ID)
	if err != nil || loaded.Binding.ContainerID != strings.Repeat("a", 64) {
		t.Fatalf("binding replaced: %v %v", loaded.Binding, err)
	}
}

func TestProjectLookupNeverCreatesStateAndRefusesUnknownSchema(t *testing.T) {
	base := filepath.Join(canonicalTempDir(t), "absent")
	t.Setenv("REACH_HOME", base)
	if _, err := Load("missing"); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatal("read-only lookup created state")
	}
	r := exampleRecord(t)
	if err := Register(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.Binding.Schema++
	p, _ := recordPath(r.Spec.ID)
	if err := state.Write(p, r); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(r.Spec.ID); err == nil {
		t.Fatal("accepted unknown binding schema")
	}
}

func TestProjectRejectsUnknownFieldsAndFakeNativeConfig(t *testing.T) {
	r := exampleRecord(t)
	r.Spec.OrbStackConfig = "/untrusted/.orbstack/vmconfig.json"
	if err := r.Spec.Validate(); err == nil {
		t.Fatal("accepted config from outside fixed Host identity")
	}
	p := filepath.Join(canonicalTempDir(t), "project.json")
	if err := os.WriteFile(p, []byte(`{"schema":1,"id":"x","unknown_authorization":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSpec(p); err == nil {
		t.Fatal("accepted unknown authorization fields")
	}
}

func TestRegisteredProjectCannotBeReadThroughSharedHardlink(t *testing.T) {
	t.Setenv("REACH_HOME", canonicalTempDir(t))
	r := exampleRecord(t)
	if err := Register(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	p, err := recordPath(r.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(canonicalTempDir(t), "guest-link")
	if err := os.Link(p, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if _, err := Load(r.Spec.ID); err == nil {
		t.Fatal("shared project record was treated as Host authorization")
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
