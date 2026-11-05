package orbstack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/transport"
)

type fixture struct {
	g                         *Guard
	c                         docker.Container
	daemon, ambient, endpoint string
	calls                     [][]string
	volume                    docker.Volume
}

func setup(t *testing.T) *fixture {
	if runtime.GOOS == "windows" {
		t.Skip("managed control plane is macOS; injected POSIX fixture")
	}
	t.Helper()
	root := canonicalTempDir(t)
	source := filepath.Join(root, "source")
	engine := filepath.Join(root, "engine")
	configDir := filepath.Join(root, "docker")
	for _, p := range []string{source, engine, configDir} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cli := filepath.Join(root, "docker-cli")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "sentinel")
	if err := os.WriteFile(sentinel, []byte("test-volume-identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("test-volume-identity"))
	s := project.Spec{Schema: 1, ID: "demo", Provider: "orbstack-docker", Docker: docker.Connection{CLI: cli, Context: "orbstack", Endpoint: "unix:///fixture.sock", ConfigDir: configDir}, Container: "name", Workspace: project.Workspace{Mode: "shared-project", GuestPath: "/workspace/demo", HostPath: source}, EngineDataPath: engine, OrbStackConfig: filepath.Join(root, ".orbstack", "vmconfig.json"), Volumes: []project.VolumeClaim{{Path: root, UUID: "volume-uuid", Name: "external", Sentinel: sentinel, SentinelSHA256: hex.EncodeToString(sum[:])}}, BindMounts: []project.MountApproval{{Source: source, Target: "/workspace/demo"}}}
	s.Docker.ControlHome = root
	f := &fixture{daemon: "daemon-one", ambient: "orbstack", endpoint: s.Docker.Endpoint}
	f.c.ID = strings.Repeat("a", 64)
	f.c.Image = "sha256:image"
	f.c.State.Running = true
	f.c.State.StartedAt = "2026-10-31T00:00:00Z"
	f.c.Config.User = "10001:10001"
	f.c.Config.WorkingDir = s.Workspace.GuestPath
	f.c.Config.Labels = map[string]string{"dev.agentreach.managed": "true", "dev.agentreach.project": "demo"}
	f.c.Mounts = []docker.Mount{{Type: "bind", Source: source, Destination: s.Workspace.GuestPath, RW: true, Propagation: "rprivate"}}
	f.c.HostConfig = json.RawMessage(`{"CapDrop":["ALL"],"SecurityOpt":["no-new-privileges=true"],"NetworkMode":"isolated","IpcMode":"private","CgroupnsMode":"private"}`)
	f.g = New(project.Record{Spec: s})
	f.g.CheckVolume = func(context.Context, project.VolumeClaim) error { return nil }
	f.g.CheckPathVolume = func(context.Context, string, project.VolumeClaim) error { return nil }
	f.g.CheckEngineData = func(context.Context, string, string) error { return nil }
	f.g.CheckControlHome = func(context.Context, string) error { return nil }
	f.g.Docker.Run = func(_ context.Context, path string, args, env []string) ([]byte, error) {
		if path != cli {
			t.Fatalf("wrong CLI: %s", path)
		}
		f.calls = append(f.calls, slices.Clone(args))
		if !slices.Contains(env, "PATH=/usr/bin:/bin:/usr/sbin:/sbin") {
			t.Fatalf("uncontrolled env %v", env)
		}
		for _, e := range env {
			if strings.HasPrefix(e, "DOCKER_") {
				t.Fatalf("ambient Docker env leaked: %s", e)
			}
		}
		if slices.Contains(args, "context") {
			if slices.Contains(args, "show") {
				return []byte(f.ambient + "\n"), nil
			}
			return json.Marshal([]any{map[string]any{"Endpoints": map[string]any{"docker": map[string]string{"Host": f.endpoint}}}})
		}
		if len(args) < 4 || args[2] != "--host" || args[3] != s.Docker.Endpoint {
			t.Fatalf("operation is not pinned to endpoint: %v", args)
		}
		if slices.Contains(args, "info") {
			return json.Marshal(map[string]string{"ID": f.daemon, "OperatingSystem": "OrbStack"})
		}
		if slices.Contains(args, "volume") {
			return json.Marshal([]docker.Volume{f.volume})
		}
		if slices.Contains(args, "inspect") {
			return json.Marshal([]docker.Container{f.c})
		}
		t.Fatalf("unexpected Docker mutation: %v", args)
		return nil, nil
	}
	b, err := f.g.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.g.Record.Binding = b
	f.calls = nil
	return f
}
func TestGuardRejectsChangedRuntimeBeforeAnyExec(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture)
	}{
		{"context", func(f *fixture) { f.ambient = "desktop-linux" }},
		{"endpoint", func(f *fixture) { f.endpoint = "unix:///other.sock" }},
		{"daemon", func(f *fixture) { f.daemon = "replacement" }},
		{"same-name-new-ID", func(f *fixture) { f.c.ID = strings.Repeat("b", 64) }},
		{"restart", func(f *fixture) { f.c.State.StartedAt = "later" }},
		{"stopped", func(f *fixture) { f.c.State.Running = false }},
		{"root", func(f *fixture) { f.c.Config.User = "0:0" }},
		{"named-user", func(f *fixture) { f.c.Config.User = "developer" }},
		{"UID-leading-zero", func(f *fixture) { f.c.Config.User = "010001" }},
		{"extra-mount", func(f *fixture) {
			f.c.Mounts = append(f.c.Mounts, docker.Mount{Type: "bind", Source: "/", Destination: "/host", RW: true})
		}},
		{"security", func(f *fixture) { f.c.HostConfig = json.RawMessage(`{"Privileged":true}`) }},
		{"host-network", func(f *fixture) {
			f.c.HostConfig = json.RawMessage(`{"NetworkMode":"host","CapDrop":["ALL"],"SecurityOpt":["no-new-privileges=true"]}`)
		}},
		{"host-pid", func(f *fixture) {
			f.c.HostConfig = json.RawMessage(`{"PidMode":"host","CapDrop":["ALL"],"SecurityOpt":["no-new-privileges=true"]}`)
		}},
		{"host-ports", func(f *fixture) {
			f.c.HostConfig = json.RawMessage(`{"PortBindings":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}]},"CapDrop":["ALL"],"SecurityOpt":["no-new-privileges=true"]}`)
		}},
		{"sentinel", func(f *fixture) { _ = os.WriteFile(f.g.Record.Spec.Volumes[0].Sentinel, []byte("replaced"), 0o600) }},
		{"volume", func(f *fixture) {
			f.g.CheckVolume = func(context.Context, project.VolumeClaim) error { return errors.New("storage_unavailable") }
		}},
		{"storage-location", func(f *fixture) {
			f.g.CheckEngineData = func(context.Context, string, string) error { return errors.New("storage_unavailable") }
		}},
		{"host-override", func(_ *fixture) { t.Setenv("DOCKER_HOST", "tcp://hostile:2375") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			marker := filepath.Join(f.g.Record.Spec.Docker.ControlHome, "unexpected-exec")
			if err := os.WriteFile(f.g.Record.Spec.Docker.CLI, []byte("#!/bin/sh\nprintf exec > "+transport.ShellQuote(marker)+"\nexit 99\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			tr, err := transport.NewContainer(transport.ContainerConfig{Runtime: f.g.Record.Spec.Docker.CLI, Argv0: "docker", Container: f.g.Record.Binding.ContainerID, Args: f.g.Record.Spec.Docker.ExecArgs(), Check: f.g.Check})
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close()
			tc.change(f)
			if _, err := tr.Run(context.Background(), reach.ExecRequest{Command: "touch forbidden"}); err == nil {
				t.Fatal("unsafe runtime accepted")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("rejected runtime reached the real exec subprocess")
			}
			for _, a := range f.calls {
				if slices.Contains(a, "exec") {
					t.Fatal("dispatched execution after failed check")
				}
			}
		})
	}
}
func TestGuardChecksRealApprovedSnapshot(t *testing.T) {
	f := setup(t)
	if err := f.g.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.c.Config.Labels["new"] = "unexpected"
	if err := f.g.Check(context.Background()); err == nil {
		t.Fatal("changed labels accepted")
	}
}
func TestGuardRejectsEngineDataExposure(t *testing.T) {
	f := setup(t)
	f.g.Record.Spec.BindMounts[0].Source = filepath.Dir(f.g.Record.Spec.EngineDataPath)
	if _, err := f.g.Capture(context.Background()); err == nil {
		t.Fatal("parent of engine data accepted as bind")
	}
}
func TestNamedVolumeCannotHideHostBind(t *testing.T) {
	f := setup(t)
	s := &f.g.Record.Spec
	s.Workspace.Mode = "guest-owned"
	s.Workspace.HostPath = ""
	s.BindMounts = nil
	s.NamedVolumes = []project.NamedVolumeApproval{{Name: "data", Target: s.Workspace.GuestPath}}
	f.c.Mounts = []docker.Mount{{Type: "volume", Name: "data", Driver: "local", Destination: s.Workspace.GuestPath, RW: true}}
	f.volume = docker.Volume{Name: "data", Driver: "local", Options: map[string]string{"type": "none", "o": "bind", "device": "/"}}
	if _, err := f.g.Capture(context.Background()); err == nil {
		t.Fatal("volume driver bind accepted")
	}
}
func TestDiskutilStructureWithoutMounted(t *testing.T) {
	// Field shape follows actual diskutil -plist on the verified Host; personal
	// identifiers and unrelated disk fields are omitted.
	v, err := parsePlist([]byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>APFSPhysicalStores</key><array><dict><key>APFSPhysicalStore</key><string>disk9s2</string></dict></array><key>MountPoint</key><string>/Volumes/External</string><key>VolumeUUID</key><string>FIXTURE-UUID</string><key>VolumeName</key><string>External</string><key>Internal</key><false/><key>Writable</key><true/></dict></plist>`))
	if err != nil || v["MountPoint"] != "/Volumes/External" || v["Internal"] != "false" || v["Writable"] != "true" || v["Mounted"] != "" {
		t.Fatalf("invalid diskutil values: %v %v", v, err)
	}
}

func TestNestedDiskutilIdentityDoesNotOverrideRoot(t *testing.T) {
	v, err := parsePlist([]byte(`<plist><dict><key>Internal</key><true/><key>Nested</key><dict><key>Internal</key><false/><key>MountPoint</key><string>/spoof</string></dict><key>MountPoint</key><string>/actual</string></dict></plist>`))
	if err != nil || v["Internal"] != "true" || v["MountPoint"] != "/actual" {
		t.Fatalf("nested identity overrode root: %v %v", v, err)
	}
}

func TestFixedControlHomeSurvivesTemporaryHarnessHome(t *testing.T) {
	f := setup(t)
	t.Setenv("HOME", canonicalTempDir(t))
	if err := f.g.Check(context.Background()); err != nil {
		t.Fatalf("probe HOME changed runtime identity: %v", err)
	}
	env, err := f.g.Record.Spec.Docker.Environment()
	if err != nil || !slices.Contains(env, "HOME="+f.g.Record.Spec.Docker.ControlHome) {
		t.Fatalf("Docker inherited harness HOME: %v %v", env, err)
	}
}

func TestBindCannotExposeControlOrSentinel(t *testing.T) {
	for _, which := range []string{"control", "cli", "docker-config", "sentinel", "engine-child", "engine-dotdot"} {
		t.Run(which, func(t *testing.T) {
			f := setup(t)
			s := &f.g.Record.Spec
			switch which {
			case "control":
				t.Setenv("REACH_HOME", s.BindMounts[0].Source)
			case "cli":
				s.Docker.CLI = filepath.Join(s.BindMounts[0].Source, "cli")
			case "docker-config":
				s.Docker.ConfigDir = s.BindMounts[0].Source
			case "sentinel":
				s.BindMounts[0].Source = s.Volumes[0].Path
			case "engine-child":
				s.BindMounts[0].Source = filepath.Join(s.EngineDataPath, "child")
			case "engine-dotdot":
				s.BindMounts[0].Source += "/../engine"
			}
			if _, err := f.g.Capture(context.Background()); err == nil {
				t.Fatal("unsafe exposure accepted")
			}
		})
	}
}

func TestBindRejectsSocketRegardlessOfItsFilename(t *testing.T) {
	f := setup(t)
	name := filepath.Join(f.g.Record.Spec.BindMounts[0].Source, "innocent")
	listener, err := net.Listen("unix", name)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	f.g.Record.Spec.BindMounts[0].Source = name
	if _, err := f.g.Capture(context.Background()); err == nil {
		t.Fatal("socket with an innocuous filename was approved as a bind")
	}
}

func TestUsedPathsCannotCrossUnapprovedNestedMounts(t *testing.T) {
	for _, which := range []string{"sentinel", "engine", "bind"} {
		t.Run(which, func(t *testing.T) {
			f := setup(t)
			bad := f.g.Record.Spec.Volumes[0].Sentinel
			if which == "engine" {
				bad = f.g.Record.Spec.EngineDataPath
			}
			if which == "bind" {
				bad = f.g.Record.Spec.BindMounts[0].Source
			}
			f.g.CheckPathVolume = func(_ context.Context, p string, _ project.VolumeClaim) error {
				if p == bad {
					return docker.Fail("storage_unavailable", "unapproved nested mount")
				}
				return nil
			}
			if err := f.g.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "nested mount") {
				t.Fatalf("accepted nested filesystem: %v", err)
			}
			if len(f.calls) != 0 {
				t.Fatalf("storage failure reached Docker: %v", f.calls)
			}
		})
	}
}

func TestBindHardlinkCannotExposeTrustedCLI(t *testing.T) {
	f := setup(t)
	alias := filepath.Join(f.g.Record.Spec.BindMounts[0].Source, "cli-link")
	if err := os.Link(f.g.Record.Spec.Docker.CLI, alias); err != nil {
		t.Skipf("hardlink unsupported: %v", err)
	}
	f.g.Record.Spec.BindMounts[0].Source = alias
	if err := f.g.Check(context.Background()); err == nil {
		t.Fatal("hard-linked CLI accepted as bind")
	}
	if len(f.calls) != 0 {
		t.Fatal("unsafe bind reached Docker")
	}
}

func TestReservedCredentialComponentsUseActualIdentity(t *testing.T) {
	root := canonicalTempDir(t)
	t.Setenv("REACH_HOME", filepath.Join(canonicalTempDir(t), "control"))
	credential, alias := filepath.Join(root, ".ssh"), filepath.Join(root, ".SSH")
	if err := os.WriteFile(credential, []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(credential, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if safeSource(alias, filepath.Join(root, "home")) {
		t.Fatal("credential hard-link alias accepted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alias, []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, _ := os.Stat(credential)
	b, _ := os.Stat(alias)
	if !os.SameFile(a, b) && !safeSource(alias, filepath.Join(root, "home")) {
		t.Fatal("case-sensitive distinct objects were globally folded")
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
