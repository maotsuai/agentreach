package execserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bojieli/agentreach/internal/fileops"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/session"
	"github.com/bojieli/agentreach/internal/transport"
	workspacepath "github.com/bojieli/agentreach/internal/workspace"
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
	helper := filepath.Join(root, "reach-helper")
	build := exec.Command("go", "build", "-o", helper, "../../cmd/reach-helper")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build rooted test helper: %v: %s", e, out)
	}
	script := fmt.Sprintf("#!/usr/bin/python3\nimport os,sys\nwith open(%q,\"a\") as f: f.write(\"dispatch\\n\")\ncommand=sys.argv[-1].replace(\"/usr/local/bin/reach-helper\",%q)\nos.execl(\"/bin/sh\",\"sh\",\"-c\",command)\n", dispatches, helper)
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

func TestManagedRPCUsesGuestNamespaceAfterProtocolMapping(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed Guest fixture requires Linux")
	}
	helper := filepath.Join(t.TempDir(), "reach-helper")
	build := exec.Command("go", "build", "-o", helper, "../../cmd/reach-helper")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build helper: %v: %s", e, out)
	}
	for _, direction := range []string{"host-parent", "host-child", "equal", "sibling"} {
		t.Run(direction, func(t *testing.T) {
			base := canonicalTempDir(t)
			t.Setenv("REACH_HOME", filepath.Join(base, "control"))
			guestRoot := filepath.Join(base, "project", "guest")
			hostAlias := filepath.Dir(guestRoot)
			prefix := ""
			wrongPrefix := "guest/"
			if direction == "host-child" {
				guestRoot = filepath.Join(base, "project")
				hostAlias = filepath.Join(guestRoot, "host")
				prefix = "host/"
				wrongPrefix = ""
			}
			if direction == "equal" {
				hostAlias = guestRoot
			}
			if direction == "sibling" {
				guestRoot = filepath.Join(base, "project")
				hostAlias = guestRoot + "-other"
			}
			for _, p := range []string{guestRoot, hostAlias} {
				if e := os.MkdirAll(p, 0700); e != nil {
					t.Fatal(e)
				}
			}
			cli := filepath.Join(base, "docker")
			dispatches := filepath.Join(base, "dispatches")
			script := fmt.Sprintf("#!/usr/bin/python3\nimport os,sys\nwith open(%q,\"a\") as f: f.write(sys.argv[-1]+\"\\n\")\ncommand=sys.argv[-1].replace(\"/usr/local/bin/reach-helper\",%q)\nos.execl(\"/bin/sh\",\"sh\",\"-c\",command)\n", dispatches, helper)
			if e := os.WriteFile(cli, []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			spec, e := project.ReadSpec(filepath.Join("..", "..", "examples", "orbstack", "project.example.json"))
			if e != nil {
				t.Fatal(e)
			}
			spec.Docker.CLI, spec.Docker.ConfigDir, spec.Docker.ControlHome = cli, base, base
			spec.OrbStackConfig = filepath.Join(base, ".orbstack", "vmconfig.json")
			spec.Workspace.Mode = "shared-project"
			spec.Workspace.GuestPath = guestRoot
			spec.Workspace.HostPath = hostAlias
			record := project.Record{Spec: spec, Binding: project.Binding{Schema: 1, Revision: 1, Generation: 1, ContainerID: strings.Repeat("a", 64), ImageID: "image", DaemonID: "daemon", StartedAt: "now", SnapshotSHA256: strings.Repeat("b", 64)}}
			if e = project.Register(context.Background(), record); e != nil {
				t.Fatal(e)
			}
			local, e := transport.NewLocal()
			if e != nil {
				t.Fatal(e)
			}
			caps, e := fileops.Probe(context.Background(), local)
			if e != nil {
				t.Fatal(e)
			}
			sess := &session.Session{Name: "overlap", Target: &session.Target{Kind: session.KindDocker, Container: record.Binding.ContainerID, Workspace: guestRoot}, Mode: session.ModeExec, Tier: reach.TierHelper, Caps: caps, Managed: session.Bind(record)}
			if e = sess.Save(); e != nil {
				t.Fatal(e)
			}
			hostCwd := filepath.Join(base, "harness-cwd")
			if e = os.Mkdir(hostCwd, 0700); e != nil {
				t.Fatal(e)
			}

			if direction == "host-parent" || direction == "host-child" {
				intended := filepath.Join(guestRoot, prefix+"note.txt")
				unrelated := filepath.Join(guestRoot, wrongPrefix+"note.txt")
				for p, value := range map[string]string{intended: "intended-old", unrelated: "unrelated-old"} {
					if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(p, []byte(value), 0600); e != nil {
						t.Fatal(e)
					}
				}
				recordPath := filepath.Join(base, "control", "projects", spec.ID+".json")
				before, e := os.ReadFile(recordPath)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = newServer(context.Background(), sess, hostCwd, func(context.Context) error { return nil }); e == nil || !strings.Contains(e.Error(), "path_denied") {
					t.Fatalf("overlap server did not fail before dispatch: %v", e)
				}
				if _, e = sess.Transport(); e == nil || !strings.Contains(e.Error(), "path_denied") {
					t.Fatalf("old session transport bypassed alias contract: %v", e)
				}
				if _, e = sess.FileOpsGuard(context.Background(), local, workspacepath.Guest, func(context.Context) error { return nil }); e == nil || !strings.Contains(e.Error(), "path_denied") {
					t.Fatalf("fixed Guest FileOps bypassed registered alias: %v", e)
				}
				if _, e = sess.Command(reach.ExecRequest{Command: "touch command-body", Dir: guestRoot}); e == nil || !strings.Contains(e.Error(), "path_denied") {
					t.Fatalf("cwd command bypassed registered alias: %v", e)
				}
				if e = sess.SetCwd(guestRoot); e == nil || !strings.Contains(e.Error(), "path_denied") {
					t.Fatalf("cwd persisted ambiguous binding: %v", e)
				}
				bootstrap := &session.Session{Name: "bootstrap", Target: sess.Target, Mode: sess.Mode, Tier: sess.Tier, Managed: sess.Managed}
				if e = bootstrap.ProbeManaged(context.Background(), record); e == nil || !strings.Contains(e.Error(), "path_denied") {
					t.Fatalf("bootstrap dispatched ambiguous alias: %v", e)
				}
				if _, e = os.Stat(dispatches); !os.IsNotExist(e) {
					t.Fatalf("overlap dispatched file/helper/shell probe: %v", e)
				}
				for p, value := range map[string]string{intended: "intended-old", unrelated: "unrelated-old"} {
					data, e := os.ReadFile(p)
					if e != nil || string(data) != value {
						t.Fatalf("rejected binding changed %s: %q %v", p, data, e)
					}
				}
				if _, e = project.Load(spec.ID); e != nil {
					t.Fatalf("read-only project load marked old binding corrupt: %v", e)
				}
				if _, e = session.Load(sess.Name); e != nil {
					t.Fatalf("read-only session load marked old binding corrupt: %v", e)
				}
				after, e := os.ReadFile(recordPath)
				if e != nil || !bytes.Equal(before, after) {
					t.Fatal("rejection rewrote immutable record")
				}
				return
			}
			srv, e := newServer(context.Background(), sess, hostCwd, func(context.Context) error { return nil })
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { srv.terminateAll(); srv.waitForProcesses(); _ = srv.Close() })
			client := newTestClient(t, srv)
			call := func(method string, p any) frame {
				t.Helper()
				f := client.call(t, method, p)
				if f.Error != nil {
					t.Fatalf("%s: %s", method, f.Error.Message)
				}
				return f
			}
			call("initialize", map[string]any{"clientName": "overlap"})
			intended := func(name string) string { return filepath.Join(guestRoot, prefix+name) }
			unrelated := func(name string) string { return filepath.Join(guestRoot, wrongPrefix+name) }
			uri := func(name string) string { return pathToURI(filepath.Join(hostAlias, prefix+name)) }
			seed := func(p, value string) {
				t.Helper()
				if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(p, []byte(value), 0600); e != nil {
					t.Fatal(e)
				}
			}
			check := func(p, value string) {
				t.Helper()
				b, e := os.ReadFile(p)
				if e != nil || string(b) != value {
					t.Fatalf("disk %s: %q %v, want %q", p, b, e, value)
				}
			}
			for _, name := range []string{"note.txt", "copy.txt", "delete.txt"} {
				seed(intended(name), "intended-old")
				seed(unrelated(name), "unrelated-old")
			}
			call("fs/writeFile", fsWriteFileParams{Path: uri("note.txt"), DataBase64: base64.StdEncoding.EncodeToString([]byte("requested-new"))})
			check(intended("note.txt"), "requested-new")
			check(unrelated("note.txt"), "unrelated-old")
			f := call("fs/readFile", fsPathParams{Path: uri("note.txt")})
			var read struct {
				DataBase64 string `json:"dataBase64"`
			}
			if e = json.Unmarshal(f.Result, &read); e != nil {
				t.Fatal(e)
			}
			if read.DataBase64 != base64.StdEncoding.EncodeToString([]byte("requested-new")) {
				t.Fatal("read selected unrelated file")
			}
			call("fs/open", fsOpenParams{HandleID: "held", Path: uri("note.txt")})
			f = call("fs/readBlock", fsReadBlockParams{HandleID: "held", Len: 13})
			var block struct {
				Chunk string `json:"chunk"`
			}
			if e = json.Unmarshal(f.Result, &block); e != nil {
				t.Fatal(e)
			}
			if block.Chunk != read.DataBase64 {
				t.Fatal("handle reinterpreted stored Guest path")
			}
			call("fs/copy", fsCopyParams{SourcePath: uri("note.txt"), DestinationPath: uri("copy.txt")})
			check(intended("copy.txt"), "requested-new")
			check(unrelated("copy.txt"), "unrelated-old")
			f = call("fs/canonicalize", fsPathParams{Path: uri("note.txt")})
			var canon struct {
				Path string `json:"path"`
			}
			if e = json.Unmarshal(f.Result, &canon); e != nil {
				t.Fatal(e)
			}
			if canon.Path != pathToURI(intended("note.txt")) {
				t.Fatalf("canonical display changed: %s", canon.Path)
			}

			roundtrip := call("fs/readFile", fsPathParams{Path: canon.Path})
			var reread struct {
				DataBase64 string `json:"dataBase64"`
			}
			if e = json.Unmarshal(roundtrip.Result, &reread); e != nil || reread.DataBase64 != read.DataBase64 {
				t.Fatalf("canonical Guest URI roundtrip changed object: %s %v", roundtrip.Result, e)
			}
			// Helper-returned display paths and relative symlink targets are already Guest paths.
			if data, e := srv.ops.Read(context.Background(), intended("note.txt"), 0, 0); e != nil || string(data) != "requested-new" {
				t.Fatalf("Guest display was reinterpreted inside FileOps: %q %v", data, e)
			}
			if e = os.Symlink("note.txt", intended("link")); e != nil {
				t.Fatal(e)
			}
			f = call("fs/getMetadata", fsPathParams{Path: uri("link")})
			var meta struct {
				IsFile bool `json:"isFile"`
			}
			if e = json.Unmarshal(f.Result, &meta); e != nil || !meta.IsFile {
				t.Fatalf("metadata derived Guest target: %s %v", f.Result, e)
			}
			f = call("fs/walk", fsWalkParams{Path: pathToURI(filepath.Dir(filepath.Join(hostAlias, prefix+"note.txt"))), Options: walkOptions{MaxDepth: 1}})
			var walk struct {
				Entries []struct {
					Path string `json:"path"`
				} `json:"entries"`
			}
			if e = json.Unmarshal(f.Result, &walk); e != nil {
				t.Fatal(e)
			}
			found := false
			for _, entry := range walk.Entries {
				if entry.Path == pathToURI(intended("note.txt")) {
					found = true
				}
			}
			if !found {
				t.Fatalf("walk omitted intended Guest path: %s", f.Result)
			}
			recursive := false
			call("fs/remove", fsRemoveParams{Path: uri("delete.txt"), Recursive: &recursive})
			if _, e = os.Stat(intended("delete.txt")); !os.IsNotExist(e) {
				t.Fatalf("remove left intended file: %v", e)
			}
			check(unrelated("delete.txt"), "unrelated-old")

			safeTarget := filepath.Join(base, "trusted-target")
			if e = os.Mkdir(safeTarget, 0700); e != nil {
				t.Fatal(e)
			}
			canonicalInside := filepath.Join(guestRoot, "inside-control")
			if e = os.Mkdir(canonicalInside, 0700); e != nil {
				t.Fatal(e)
			}
			canonicalAlias := filepath.Join(base, "canonical-overlap")
			if e = os.Symlink(canonicalInside, canonicalAlias); e != nil {
				t.Fatal(e)
			}
			rawAlias := filepath.Join(guestRoot, "raw-overlap")
			if e = os.Symlink(safeTarget, rawAlias); e != nil {
				t.Fatal(e)
			}
			for _, cwd := range []string{filepath.Dir(guestRoot), canonicalInside, canonicalAlias, rawAlias} {
				before, e := os.ReadFile(dispatches)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = newServer(context.Background(), sess, cwd, func(context.Context) error { return nil }); e == nil || !strings.Contains(e.Error(), "path_denied") {
					t.Fatalf("trusted cwd %s bypassed alias contract: %v", cwd, e)
				}
				after, e := os.ReadFile(dispatches)
				if e != nil || !bytes.Equal(before, after) {
					t.Fatalf("rejected cwd dispatched helper/body: %s", cwd)
				}
			}
			safeAlias := filepath.Join(base, "safe-control-alias")
			if e = os.Symlink(safeTarget, safeAlias); e != nil {
				t.Fatal(e)
			}
			safeSrv, e := newServer(context.Background(), sess, safeAlias, func(context.Context) error { return nil })
			if e != nil {
				t.Fatalf("disjoint original/canonical control alias rejected: %v", e)
			}
			_ = safeSrv.Close()
			equalSrv, e := newServer(context.Background(), sess, guestRoot, func(context.Context) error { return nil })
			if e != nil {
				t.Fatalf("equal control cwd rejected: %v", e)
			}
			_ = equalSrv.Close()
			// Hold the first real request at its member guard so that the next
			// RPC must grow the pool. Both then perform actual rooted writes.
			held := make(chan struct{})
			gate := make(chan struct{})
			released := false
			release := func() {
				if !released {
					close(gate)
					released = true
				}
			}
			t.Cleanup(release)
			var checkCount atomic.Int32
			srv.checkRuntime = func(ctx context.Context) error {
				if checkCount.Add(1) == 2 {
					close(held)
					select {
					case <-gate:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			}
			seed(unrelated("parallel-0"), "unrelated-old")
			seed(unrelated("parallel-1"), "unrelated-old")
			payload := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), 128<<10))
			first := client.send(t, "fs/writeFile", fsWriteFileParams{Path: uri("parallel-0"), DataBase64: payload})
			select {
			case <-held:
			case <-time.After(3 * time.Second):
				t.Fatal("first request did not acquire its pool member")
			}
			second := client.send(t, "fs/writeFile", fsWriteFileParams{Path: uri("parallel-1"), DataBase64: payload})
			if f := client.await(t, second); f.Error != nil {
				t.Fatal(f.Error.Message)
			}
			release()
			if f := client.await(t, first); f.Error != nil {
				t.Fatal(f.Error.Message)
			}
			for n := 0; n < 2; n++ {
				name := fmt.Sprintf("parallel-%d", n)
				data, e := os.ReadFile(intended(name))
				if e != nil || len(data) != 128<<10 {
					t.Fatalf("pooled write target %s: %d %v", name, len(data), e)
				}
				check(unrelated(name), "unrelated-old")
			}

			logs, e := os.ReadFile(dispatches)
			if e != nil || strings.Count(string(logs), " serve-rooted ") < 2 {
				t.Fatalf("pool did not grow in regression: %v", e)
			}
		})
	}
}
