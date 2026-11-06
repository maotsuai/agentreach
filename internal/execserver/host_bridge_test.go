package execserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/session"
	"github.com/bojieli/agentreach/internal/transport"
)

func TestManagedRPCFrameBudgetRejectsBeforeDispatch(t *testing.T) {
	s := &Server{sess: &session.Session{Managed: &session.Managed{}}, processes: map[string]*process{}, order: newSequencer()}
	e := s.Serve(context.Background(), strings.NewReader(strings.Repeat("x", policy.MaxRequest+1)), io.Discard)
	if e == nil || !strings.Contains(e.Error(), "request_too_large") {
		t.Fatal(e)
	}
}

// Called by the two real rooted-helper layouts, after the RPC connection is
// initialized. The Host callback must not send even one command to its Guest.
func testFixedHostCallback(t *testing.T, srv *Server, sess *session.Session, dispatchFile string) {
	t.Helper()
	self, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	self, e = filepath.EvalSymlinks(self)
	if e != nil {
		t.Fatal(e)
	}
	base, e := project.Home()
	if e != nil {
		t.Fatal(e)
	}
	p := execParams{ProcessID: "fixed-mcp", Argv: []string{self, "host-bridge", "--session", sess.Name}, Cwd: pathToURI(base), PipeStdin: true, Env: map[string]string{"REACH_HOME": base, "REACH_SESSION": sess.Name, "REACH_MANAGED": "1", "REACH_MANAGED_PROJECT": sess.Managed.ProjectID, "REACH_MANAGED_RECORD": sess.Managed.RecordSHA256, "PATH": "/unused"}}
	before, e := os.ReadFile(dispatchFile)
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"argv", "cwd", "binding", "env", "tty", "stdin"} {
		bad := p
		bad.Argv = append([]string{}, p.Argv...)
		bad.Env = map[string]string{}
		for k, v := range p.Env {
			bad.Env[k] = v
		}
		switch kind {
		case "argv":
			bad.Argv = append(bad.Argv, "--shell")
		case "cwd":
			bad.Cwd = pathToURI("/tmp")
		case "binding":
			bad.Env["REACH_MANAGED_RECORD"] = "changed"
		case "env":
			bad.Env["NODE_OPTIONS"] = "malicious"
		case "tty":
			bad.Tty = true
		case "stdin":
			bad.PipeStdin = false
		}
		if _, matched, e := srv.hostBridgeStream(bad); !matched || e == nil {
			t.Fatalf("%s matched=%t err=%v", kind, matched, e)
		}
	}
	st, matched, e := srv.hostBridgeStream(p)
	if e != nil || !matched {
		t.Fatalf("matched=%t err=%v", matched, e)
	}
	defer st.Close()
	if _, matched, e := srv.hostBridgeStream(p); !matched || e == nil || !strings.Contains(e.Error(), "request_budget") {
		t.Fatal("unbounded duplicate Host MCP stream", e)
	}
	go func() { _, _ = io.Copy(io.Discard, st.Stderr) }()
	reader := bufio.NewReader(st.Stdout)
	frames := []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"host_status","arguments":{},"_meta":{"progressToken":1}}}`}
	for _, frame := range frames {
		if _, e = io.WriteString(st.Stdin, frame+"\n"); e != nil {
			t.Fatal(e)
		}
		line, e := reader.ReadBytes('\n')
		if e != nil {
			t.Fatal(e)
		}
		var r map[string]any
		if e = json.Unmarshal(line, &r); e != nil || r["error"] != nil {
			t.Fatalf("%s %v", line, e)
		}
	}
	st.Stdin.Close()
	io.Copy(io.Discard, reader)
	_, e = st.Wait()
	if e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(dispatchFile)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("fixed Host handshake reached Guest transport")
	}
	// Exercise the real process pump, not just the inner pipe. With the Shell
	// sentinel filter this response's tail would wait forever for the next RPC.
	raw, _ := json.Marshal(p)
	if _, e := srv.handleProcessStart(context.Background(), raw); e != nil {
		t.Fatal(e)
	}
	write, _ := json.Marshal(writeParams{ProcessID: p.ProcessID, WriteID: "init", Chunk: base64.StdEncoding.EncodeToString([]byte(frames[0] + "\n"))})
	if _, e := srv.handleProcessWrite(write); e != nil {
		t.Fatal(e)
	}
	srv.mu.Lock()
	proc := srv.processes[p.ProcessID]
	srv.mu.Unlock()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		proc.mu.Lock()
		var output []byte
		for _, c := range proc.chunks {
			if c.stream == "stdout" {
				output = append(output, c.data...)
			}
		}
		next := proc.notifyCh
		proc.mu.Unlock()
		if bytes.HasSuffix(output, []byte("\n")) {
			var r map[string]any
			if e := json.Unmarshal(output, &r); e != nil || r["result"] == nil {
				t.Fatalf("MCP response=%q %v", output, e)
			}
			break
		}
		select {
		case <-next:
		case <-deadline.C:
			t.Fatal("MCP response was held in the Shell sentinel tail")
		}
	}
	proc.stdin.Close()
	srv.waitForProcesses()
	p.Argv = []string{"/bin/sh", "-c", self + " host-bridge --session " + sess.Name}
	if _, matched, e = srv.hostBridgeStream(p); matched || e != nil {
		t.Fatal("ordinary Guest shell became a Host callback")
	}
	if e = srv.runtimeReady(context.Background()); e != nil {
		t.Fatal(e)
	}
	testUnknownProcessResponses(t, srv, sess)
}

type uncertainOpen struct {
	transport.Transport
	calls int
}

func (f *uncertainOpen) Open(context.Context, string) (transport.Stream, error) {
	f.calls++
	return transport.Stream{}, io.ErrClosedPipe
}

type partialInput struct {
	calls int
	seen  []byte
}

func (f *partialInput) Write(b []byte) (int, error) {
	f.calls++
	n := len(b) - 1
	f.seen = append(f.seen, b[:n]...)
	return n, nil
}
func (*partialInput) Close() error { return nil }

func testUnknownProcessResponses(t *testing.T, srv *Server, sess *session.Session) {
	t.Helper()
	ctx := context.Background()
	store, e := sess.Policy(srv.runtimeReady)
	if e != nil {
		t.Fatal(e)
	}
	old := srv.t
	failing := &uncertainOpen{Transport: old}
	srv.t = failing
	raw, _ := json.Marshal(execParams{ProcessID: "uncertain-start", Argv: []string{"/bin/sh", "-c", "dummy-command"}, Cwd: pathToURI(srv.root)})
	_, startErr := srv.handleProcessStart(ctx, raw)
	srv.t = old
	v, e := store.Inspect()
	if e != nil {
		t.Fatal(e)
	}
	op := v.Operations[len(v.Operations)-1]
	if failing.calls != 1 || startErr == nil || !strings.Contains(startErr.Message, "outcome_unknown") || !strings.Contains(startErr.Message, op.ID) || op.Outcome != "outcome_unknown" {
		t.Fatalf("start: %v op=%+v", startErr, op)
	}
	w := &partialInput{}
	p := &process{id: "uncertain-stdin", stdin: w, stdinOpen: true, managedWrites: map[string]managedWrite{}, writeIDs: map[string]bool{}}
	srv.mu.Lock()
	srv.processes[p.id] = p
	srv.mu.Unlock()
	defer func() { srv.mu.Lock(); delete(srv.processes, p.id); srv.mu.Unlock() }()
	raw, _ = json.Marshal(writeParams{ProcessID: p.id, WriteID: "partial", Chunk: base64.StdEncoding.EncodeToString([]byte("line\n"))})
	_, writeErr := srv.handleProcessWrite(raw)
	v, e = store.Inspect()
	if e != nil {
		t.Fatal(e)
	}
	op = v.Operations[len(v.Operations)-1]
	if w.calls != 1 || string(w.seen) != "line" || writeErr == nil || !strings.Contains(writeErr.Message, "outcome_unknown") || !strings.Contains(writeErr.Message, op.ID) || !strings.Contains(writeErr.Message, io.ErrShortWrite.Error()) || op.Outcome != "outcome_unknown" {
		t.Fatalf("stdin: %v op=%+v", writeErr, op)
	}
	_, retryErr := srv.handleProcessWrite(raw)
	if w.calls != 1 || retryErr == nil || !strings.Contains(retryErr.Message, op.ID) {
		t.Fatal("unknown stdin replay lost operation identity", retryErr)
	}
	if e = store.Set(ctx, "guest", "deny"); e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(execParams{ProcessID: "denied-start", Argv: []string{"/bin/sh", "-c", "dummy-command"}, Cwd: pathToURI(srv.root)})
	_, denied := srv.handleProcessStart(ctx, raw)
	if denied == nil || !strings.Contains(denied.Message, "policy_denied") || strings.Contains(denied.Message, "outcome_unknown") {
		t.Fatal(denied)
	}
	if e = store.Set(ctx, "guest", "allow"); e != nil {
		t.Fatal(e)
	}
}
