package execserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bojieli/agentreach/internal/fileops"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/session"
	"github.com/bojieli/agentreach/internal/transport"
)

func TestTransportFirstGuardFailureRetiresExistingServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX runtime fixture")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("REACH_HOME", filepath.Join(root, "control"))
	cli := filepath.Join(root, "docker")
	dispatches := filepath.Join(root, "dispatches")
	script := "#!/bin/sh\nprintf 'dispatch\\n' >> " + transport.ShellQuote(dispatches) + "\nfor arg do last=$arg; done\nexec /bin/sh -c \"$last\"\n"
	if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	spec, err := project.ReadSpec(filepath.Join("..", "..", "examples", "orbstack", "project.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec.Docker.CLI, spec.Docker.ConfigDir, spec.Docker.ControlHome = cli, root, root
	spec.OrbStackConfig = filepath.Join(root, ".orbstack", "vmconfig.json")
	spec.Workspace.GuestPath = root
	record := project.Record{Spec: spec, Binding: project.Binding{Schema: 1, Revision: 1, Generation: 1, ContainerID: strings.Repeat("a", 64), ImageID: "image", DaemonID: "daemon", StartedAt: "now", SnapshotSHA256: strings.Repeat("b", 64)}}
	if err := project.Register(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	local, err := transport.NewLocal()
	if err != nil {
		t.Skipf("POSIX shell unavailable: %v", err)
	}
	caps, err := fileops.Probe(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	if ok, why := caps.Qualifies(reach.TierPipe); !ok {
		t.Skipf("pipe fixture: %s", why)
	}
	sess := &session.Session{Name: "transport-failure", Target: &session.Target{Kind: session.KindDocker, Container: record.Binding.ContainerID, Workspace: root}, Mode: session.ModeExec, Tier: reach.TierPipe, Pinned: true, Caps: caps, Managed: session.Bind(record)}
	if err := sess.Save(); err != nil {
		t.Fatal(err)
	}
	var remaining, checks atomic.Int32
	check := func(context.Context) error {
		checks.Add(1)
		if remaining.Load() > 0 && remaining.Add(-1) == 0 {
			return errors.New("storage_unavailable: transport-only failure")
		}
		return nil
	}
	srv, err := newServer(context.Background(), sess, root, check)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.terminateAll(); srv.waitForProcesses(); _ = srv.Close() })
	client := newTestClient(t, srv)
	if f := client.call(t, "initialize", map[string]any{"clientName": "transport-guard"}); f.Error != nil {
		t.Fatal(f.Error.Message)
	}
	seed := filepath.Join(root, "seed")
	if err := os.WriteFile(seed, []byte("warm"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f := client.call(t, "fs/readFile", map[string]any{"path": pathToURI(seed)}); f.Error != nil {
		t.Fatal(f.Error.Message)
	}
	if f := client.call(t, "fs/open", fsOpenParams{HandleID: "old", Path: pathToURI(seed)}); f.Error != nil {
		t.Fatal(f.Error.Message)
	}
	stdinCanary := filepath.Join(root, "stale-stdin")
	if f := client.call(t, "process/start", execParams{ProcessID: "held", Argv: []string{"sh", "-c", "read -r line && printf %s \"$line\" > " + transport.ShellQuote(stdinCanary)}, Cwd: pathToURI(root), PipeStdin: true}); f.Error != nil {
		t.Fatal(f.Error.Message)
	}
	before, err := os.ReadFile(dispatches)
	if err != nil {
		t.Fatal(err)
	}
	startCanary := filepath.Join(root, "forbidden-start")
	beforeChecks := checks.Load()
	remaining.Store(2)
	f := client.call(t, "process/start", execParams{ProcessID: "rejected", Argv: []string{"sh", "-c", "touch " + transport.ShellQuote(startCanary)}, Cwd: pathToURI(root)})
	if f.Error == nil || !strings.Contains(f.Error.Message, "transport-only failure") || checks.Load()-beforeChecks != 2 {
		t.Fatalf("inner check did not fail: %+v checks=%d", f.Error, checks.Load()-beforeChecks)
	}
	// The injected condition has recovered. No old server entry may reach it.
	for _, method := range []string{"environment/status", "environment/info", "fs/readFile", "process/start"} {
		var params any = map[string]any{}
		if method == "process/start" {
			params = execParams{ProcessID: "after-recovery", Argv: []string{"sh", "-c", "true"}, Cwd: pathToURI(root)}
		}
		if f := client.call(t, method, params); f.Error == nil || !strings.Contains(f.Error.Message, "transport-only failure") {
			t.Fatalf("%s revived the connection: %+v", method, f.Error)
		}
	}
	if f := client.call(t, "fs/readBlock", fsReadBlockParams{HandleID: "old", Len: 4}); f.Error == nil {
		t.Fatal("old file handle revived")
	}
	if f := client.call(t, "process/write", writeParams{ProcessID: "held", WriteID: "after-recovery", Chunk: base64.StdEncoding.EncodeToString([]byte("forbidden\n"))}); f.Error == nil {
		t.Fatal("old stdin revived")
	}
	if _, err := srv.ops.Read(context.Background(), seed, 0, 0); err == nil {
		t.Fatal("old pool remained reusable")
	}
	if _, err := srv.t.Open(context.Background(), "touch "+transport.ShellQuote(startCanary)); err == nil {
		t.Fatal("direct transport Open revived")
	}
	after, err := os.ReadFile(dispatches)
	if err != nil || string(before) != string(after) {
		t.Fatalf("rejected calls reached runtime: %v", err)
	}
	for _, p := range []string{startCanary, stdinCanary} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("rejected side effect %s: %v", p, err)
		}
	}
	if f := client.call(t, "fs/close", fsCloseParams{HandleID: "old"}); f.Error != nil {
		t.Fatal("cleanup was blocked")
	}
	fresh, err := newServer(context.Background(), sess, root, check)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if b, err := fresh.ops.Read(context.Background(), seed, 0, 0); err != nil || string(b) != "warm" {
		t.Fatalf("new server could not recover: %q %v", b, err)
	}
}

func TestAnyFailedRuntimeEntryPermanentlyRetiresConnection(t *testing.T) {
	for _, first := range []string{"environment/status", "environment/info", "process/start", "fs/readFile"} {
		t.Run(first, func(t *testing.T) {
			e := newTestEnv(t)
			e.initialize(t)
			var ready atomic.Bool
			ready.Store(true)
			e.srv.checkRuntime = func(context.Context) error {
				if !ready.Load() {
					return errors.New("storage_unavailable")
				}
				return nil
			}
			marker := filepath.Join(e.root, "stale-stdin")
			seed := filepath.Join(e.root, "seed")
			if err := os.WriteFile(seed, []byte("warm"), 0o600); err != nil {
				t.Fatal(err)
			}
			if f := e.client.call(t, "fs/readFile", map[string]any{"path": pathToURI(seed)}); f.Error != nil {
				t.Fatal(f.Error.Message)
			}
			if f := e.client.call(t, "process/start", execParams{ProcessID: "held", Argv: []string{"sh", "-c", "read -r value && printf '%s' \"$value\" > " + marker}, Cwd: pathToURI(e.root), PipeStdin: true}); f.Error != nil {
				t.Fatal(f.Error.Message)
			}
			ready.Store(false)
			var params any
			if first == "process/start" {
				params = execParams{ProcessID: "new", Argv: []string{"sh", "-c", "true"}, Cwd: pathToURI(e.root)}
			}
			if first == "fs/readFile" {
				params = map[string]any{"path": pathToURI(seed)}
			}
			if f := e.client.call(t, first, params); f.Error == nil {
				t.Fatal("failed runtime reported success")
			}
			ready.Store(true)
			if f := e.client.call(t, "process/write", writeParams{ProcessID: "held", WriteID: "stale", Chunk: base64.StdEncoding.EncodeToString([]byte("forbidden\n"))}); f.Error == nil {
				t.Fatal("restored guard revived old stdin")
			}
			if f := e.client.call(t, "fs/readFile", map[string]any{"path": pathToURI(seed)}); f.Error == nil {
				t.Fatal("restored guard revived old file pool")
			}
			if f := e.client.call(t, "process/terminate", signalParams{ProcessID: "held"}); f.Error != nil {
				t.Fatal("cleanup was blocked")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("stale stdin side effect: %v", err)
			}
			fresh, err := New(context.Background(), e.srv.sess, e.workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			if _, err := fresh.ops.Read(context.Background(), seed, 0, 0); err != nil {
				t.Fatalf("new connection could not read: %v", err)
			}
		})
	}
}

type writeSpy struct{ writes int }

func (w *writeSpy) Write(b []byte) (int, error) { w.writes++; return len(b), nil }
func (w *writeSpy) Close() error                { return nil }
func TestProcessWriteAndStatusRecheckRuntime(t *testing.T) {
	e := newTestEnv(t)
	e.initialize(t)
	ready := true
	e.srv.checkRuntime = func(context.Context) error {
		if !ready {
			return errors.New("generation_mismatch")
		}
		return nil
	}
	w := &writeSpy{}
	e.srv.processes["held"] = &process{stdin: w, stdinOpen: true, writeIDs: map[string]bool{}}
	raw, _ := json.Marshal(writeParams{ProcessID: "held", WriteID: "one", Chunk: base64.StdEncoding.EncodeToString([]byte("one"))})
	if _, err := e.srv.handleProcessWrite(raw); err != nil {
		t.Fatal(err)
	}
	ready = false
	raw, _ = json.Marshal(writeParams{ProcessID: "held", WriteID: "two", Chunk: base64.StdEncoding.EncodeToString([]byte("two"))})
	if _, err := e.srv.handleProcessWrite(raw); err == nil {
		t.Fatal("old stdin bypassed runtime check")
	}
	if w.writes != 1 {
		t.Fatal("rejected bytes reached target")
	}
	if _, err := e.srv.handle(context.Background(), "environment/status", nil); err == nil {
		t.Fatal("reported ready after generation changed")
	}
	if _, err := e.srv.handle(context.Background(), "environment/info", nil); err == nil {
		t.Fatal("reported stale environment after generation changed")
	}
	// No live transport process is held by this synthetic process.
	e.srv.processes = map[string]*process{}
}
func TestManagedTTYAndSignalsAreExplicitlyUnsupported(t *testing.T) {
	e := newTestEnv(t)
	e.srv.sess.Managed = &session.Managed{ProjectID: "test"}
	e.srv.checkRuntime = func(context.Context) error { return nil }
	raw, _ := json.Marshal(execParams{ProcessID: "tty", Argv: []string{"bash"}, Cwd: pathToURI(e.root), Tty: true})
	if _, err := e.srv.handleProcessStart(context.Background(), raw); err == nil || !strings.Contains(err.Message, "tty") {
		t.Fatal("pretended to provide a PTY")
	}
	if _, err := e.srv.handleProcessSignal([]byte(`{"processId":"x","signal":"SIGINT"}`)); err == nil {
		t.Fatal("pretended to deliver a Guest signal")
	}
	e.srv.sess.Managed = nil
}

var _ io.WriteCloser = (*writeSpy)(nil)

func TestWorkspaceProtocolMappingAcceptsCanonicalHostAlias(t *testing.T) {
	e := newTestEnv(t)
	e.initialize(t)
	alias := filepath.Join(t.TempDir(), "host-workspace-alias")
	if err := os.Symlink(e.workspace, alias); err != nil {
		t.Fatal(err)
	}
	// Exercise New's resolution rather than pre-populating a synthetic alias.
	srv, err := New(context.Background(), e.srv.sess, alias)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	for _, p := range []string{alias, e.workspace, alias + "/sub/file", e.workspace + "/sub/file"} {
		want := e.root
		if strings.HasSuffix(p, "/sub/file") {
			want = filepath.Join(e.root, "sub", "file")
		}
		if got := srv.mapPath(p); got != want {
			t.Fatalf("mapPath(%q)=%q want %q", p, got, want)
		}
	}
	if got := srv.mapPath(e.workspace + "-other/file"); got != e.workspace+"-other/file" {
		t.Fatalf("mapped unrelated same-prefix path: %q", got)
	}
}
