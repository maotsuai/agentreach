package execserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bojieli/agentreach/internal/audit"
	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/transport"
)

// maxRememberedWrites is how many process/write ids one process remembers for
// deduplication. A retry follows its original within moments, so this covers
// any realistic retry window without growing for the life of the process.
const maxRememberedWrites = 256

// retainedOutputBytes caps one process's replay buffer, matching codex's own
// server (local_process.rs). Output older than the cap is evicted; an agent
// that needs everything streams it through process/output notifications or
// polls promptly, exactly as against codex's own server.
const retainedOutputBytes = 1 << 20

// outChunk is one retained output frame.
type outChunk struct {
	seq    uint64
	stream string
	data   []byte
}

// process is one running (or finished) remote command. Codex holds it by the
// processId it chose in process/start, streams its output through
// process/output notifications, and replays it through process/read.
type process struct {
	id      string
	command string // the script the agent asked for, for the audit log
	dir     string

	mu        sync.Mutex
	chunks    []outChunk
	retained  int
	nextSeq   uint64 // next seq to assign; starts at 1, as in codex's server
	exited    bool
	exitCode  int
	closed    bool
	failure   string
	stdin     io.WriteCloser
	stdinOpen bool
	// writeIDs remembers recent writes so a retried one is acknowledged rather
	// than applied twice, and writeOrder bounds how many are remembered. An
	// interactive process can be written to for as long as the agent keeps
	// talking to it, so remembering every id it ever saw grows without limit.
	writeIDs      map[string]bool
	writeOrder    []string
	managedWrites map[string]managedWrite
	hostBridge    bool
	terminated    bool
	stream        transport.Stream
	// notify is closed and replaced on every state change, broadcasting to
	// blocked process/read callers.
	notifyCh chan struct{}
}

type managedWrite struct{ chunk, outcome, failure string }

// broadcast wakes every process/read waiting on this process. Callers hold mu.
func (p *process) broadcast() {
	close(p.notifyCh)
	p.notifyCh = make(chan struct{})
}

// --- process/start ---

type execParams struct {
	ProcessID string            `json:"processId"`
	Argv      []string          `json:"argv"`
	Cwd       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	Tty       bool              `json:"tty"`
	PipeStdin bool              `json:"pipeStdin"`
}

func (s *Server) handleProcessStart(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var p execParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, invalidParams("process/start: %v", err)
	}
	if p.ProcessID == "" {
		return nil, invalidParams("process/start: processId must not be empty")
	}
	if len(p.Argv) == 0 {
		return nil, invalidParams("process/start: argv must not be empty")
	}
	if s.sess.Managed != nil && p.Tty {
		return nil, invalidParams("process/start: tty is unsupported; managed commands use piped stdio")
	}
	if err := s.runtimeReady(ctx); err != nil {
		return nil, internalError("%v", err)
	}

	s.mu.Lock()
	if _, dup := s.processes[p.ProcessID]; dup || s.starting[p.ProcessID] {
		s.mu.Unlock()
		return nil, invalidParams("process/start: processId %q is already in use", p.ProcessID)
	}
	if s.starting == nil {
		s.starting = map[string]bool{}
	}
	s.starting[p.ProcessID] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.starting, p.ProcessID); s.mu.Unlock() }()

	sentinel := transport.NewSentinel()
	st, isBridge, err := s.hostBridgeStream(p)
	if err != nil {
		return nil, internalError("%v", err)
	}
	dir := s.root
	command := "[fixed Host MCP bridge]"
	if !isBridge {
		var rerr *rpcError
		dir, rerr = s.mapURI(p.Cwd)
		if rerr != nil {
			return nil, rerr
		}
		command = argvToCommand(p.Argv)
		env := map[string]string{}
		if s.sess.Caps != nil {
			for k, v := range s.sess.Caps.Env() {
				env[k] = v
			}
		}
		for k, v := range p.Env {
			env[k] = v
		}
		built, buildErr := s.sess.Command(reach.ExecRequest{Command: command, Dir: dir, Env: env})
		if buildErr != nil {
			return nil, invalidParams("process/start: %v", buildErr)
		}
		wrapped := transport.WrapWithSentinel(built, sentinel)
		finish, beginErr := s.sess.BeginGuest(ctx, s.runtimeReady, "guest.process_start", struct {
			ID, Nonce string
			Params    execParams
			Command   string
		}{p.ProcessID, s.sessionID, p, wrapped})
		if beginErr != nil {
			return nil, internalError("%v", beginErr)
		}
		st, err = s.t.Open(context.Background(), wrapped)
		outcome := "ok"
		if err != nil {
			outcome = "outcome_unknown"
		}
		if resultErr := finish(outcome, err); resultErr != nil {
			if err == nil {
				_ = st.Close()
			}
			return nil, internalError("%v", resultErr)
		}
		if err != nil {
			s.record(audit.Entry{Action: "exec", Command: command, Dir: dir, Error: err.Error()})
			return nil, internalError("start on %s: %v", s.sess.Target.Describe(), err)
		}
	}

	if err := s.runtimeReady(ctx); err != nil {
		_ = st.Close()
		return nil, internalError("%v", err)
	}
	proc := &process{
		id:            p.ProcessID,
		command:       command,
		dir:           dir,
		nextSeq:       1,
		stdin:         st.Stdin,
		stdinOpen:     p.PipeStdin || p.Tty,
		writeIDs:      map[string]bool{},
		managedWrites: map[string]managedWrite{},
		hostBridge:    isBridge,
		notifyCh:      make(chan struct{}),
		stream:        st,
	}
	s.mu.Lock()
	s.processes[p.ProcessID] = proc
	s.mu.Unlock()

	if !proc.stdinOpen {
		// A command with no stdin writer must still see EOF, never a pipe that
		// stays open forever.
		_ = st.Stdin.Close()
	}

	s.running.Add(1)
	go s.runProcess(proc, st, sentinel)

	return map[string]any{
		"processId":   p.ProcessID,
		"sandboxType": "none",
	}, nil
}

// argvToCommand reconstructs the shell command line codex wants run. Codex
// derives argv from the environment-reported shell, so the normal shape is
// [shell, -lc|-c, script]; anything else is quoted word-by-word, which keeps
// an unusual argv honest rather than guessed at.
func argvToCommand(argv []string) string {
	if len(argv) == 3 && (argv[1] == "-lc" || argv[1] == "-c") {
		return argv[2]
	}
	if len(argv) == 4 && argv[1] == "-l" && argv[2] == "-c" {
		return argv[3]
	}
	words := make([]string, len(argv))
	for i, w := range argv {
		words[i] = transport.ShellQuote(w)
	}
	return strings.Join(words, " ")
}

// runProcess pumps a started process's output to the client and settles its
// exit status. It owns the audit record for the command.
func (s *Server) runProcess(p *process, st transport.Stream, sentinel string) {
	defer s.running.Done()
	started := time.Now()

	stdoutChunks := &chunkWriter{proc: p, stream: "stdout", server: s}
	filter := transport.NewSentinelFilter(stdoutChunks, sentinel)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if p.hostBridge {
			// MCP needs each complete JSON line immediately. A shell sentinel
			// filter withholds a tail and would deadlock the next request.
			_, _ = io.Copy(stdoutChunks, st.Stdout)
		} else {
			_, _ = io.Copy(filter, st.Stdout)
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&chunkWriter{proc: p, stream: "stderr", server: s}, st.Stderr)
	}()
	wg.Wait()
	filter.Flush()

	code, ok := filter.ExitCode()
	waitCode, waitErr := st.Wait()
	if p.hostBridge {
		code = waitCode
		ok = waitErr == nil
	}

	p.mu.Lock()
	p.exited = true
	switch {
	case ok:
		p.exitCode = code
	case p.terminated:
		// reach asked for the exit; a missing marker is expected, not a
		// transport failure.
		p.exitCode = -1
	default:
		// No marker: the transport, not the command, failed. That distinction
		// is reach's core invariant, so it is reported as a failure the agent
		// can reason about rather than as an exit status it would trust.
		p.exitCode = -1
		p.failure = fmt.Sprintf("%s: the connection closed before the command completed", s.t.Describe())
		if waitErr != nil {
			p.failure = fmt.Sprintf("%s: %v", s.t.Describe(), waitErr)
		}
	}
	seq := p.nextSeq
	p.nextSeq++
	exitCode := p.exitCode
	failure := p.failure
	p.broadcast()
	p.mu.Unlock()

	s.notify("process/exited", map[string]any{
		"processId": p.id,
		"seq":       seq,
		"exitCode":  exitCode,
	})

	p.mu.Lock()
	p.closed = true
	closeSeq := p.nextSeq
	p.nextSeq++
	p.broadcast()
	p.mu.Unlock()
	s.notify("process/closed", map[string]any{
		"processId": p.id,
		"seq":       closeSeq,
	})
	// The record stays addressable for a while so its output can still be read,
	// but not forever: nothing removed it, so every command an agent ran was
	// held with its retained output until the agent quit.
	s.retire(p.id)

	entry := audit.Entry{Action: "exec", Command: p.command, Dir: p.dir, Code: exitCode, Millis: time.Since(started).Milliseconds()}
	if failure != "" {
		entry.Error = failure
	}
	s.record(entry)
}

// chunkWriter turns a remote output stream into retained chunks and
// process/output notifications.
type chunkWriter struct {
	proc   *process
	stream string
	server *Server
}

func (w *chunkWriter) Write(data []byte) (int, error) {
	buf := make([]byte, len(data))
	copy(buf, data)

	p := w.proc
	p.mu.Lock()
	seq := p.nextSeq
	p.nextSeq++
	p.chunks = append(p.chunks, outChunk{seq: seq, stream: w.stream, data: buf})
	p.retained += len(buf)
	for p.retained > retainedOutputBytes && len(p.chunks) > 1 {
		p.retained -= len(p.chunks[0].data)
		p.chunks = p.chunks[1:]
	}
	p.broadcast()
	p.mu.Unlock()

	w.server.notify("process/output", map[string]any{
		"processId": p.id,
		"seq":       seq,
		"stream":    w.stream,
		"chunk":     base64.StdEncoding.EncodeToString(buf),
	})
	return len(data), nil
}

// --- process/read ---

type readParams struct {
	ProcessID string  `json:"processId"`
	AfterSeq  *uint64 `json:"afterSeq"`
	MaxBytes  *int64  `json:"maxBytes"`
	WaitMs    *uint64 `json:"waitMs"`
}

func (s *Server) handleProcessRead(raw json.RawMessage) (any, *rpcError) {
	var params readParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, invalidParams("process/read: %v", err)
	}
	s.mu.Lock()
	p, ok := s.processes[params.ProcessID]
	s.mu.Unlock()
	if !ok {
		return nil, invalidRequest("unknown process id %s", params.ProcessID)
	}

	var afterSeq uint64
	if params.AfterSeq != nil {
		afterSeq = *params.AfterSeq
	}
	maxBytes := int64(1) << 62
	bounded := params.MaxBytes != nil
	if bounded {
		maxBytes = *params.MaxBytes
	}
	deadline := time.Now()
	if params.WaitMs != nil {
		deadline = deadline.Add(time.Duration(*params.WaitMs) * time.Millisecond)
	}

	// Mirrors codex's own server (local_process.rs exec_read): return as soon
	// as there is anything new or the process is closed, otherwise wait for a
	// state change until the deadline.
	for {
		p.mu.Lock()
		chunks := []map[string]any{}
		var total int64
		nextSeq := p.nextSeq
		for _, c := range p.chunks {
			if c.seq <= afterSeq {
				continue
			}
			if len(chunks) > 0 && total+int64(len(c.data)) > maxBytes {
				break
			}
			total += int64(len(c.data))
			chunks = append(chunks, map[string]any{
				"seq":    c.seq,
				"stream": c.stream,
				"chunk":  base64.StdEncoding.EncodeToString(c.data),
			})
			nextSeq = c.seq + 1
			if total >= maxBytes {
				break
			}
		}
		if !bounded {
			nextSeq = p.nextSeq
		}
		done := len(chunks) > 0 || p.closed || !time.Now().Before(deadline)
		notify := p.notifyCh
		if done {
			resp := map[string]any{
				"chunks":        chunks,
				"nextSeq":       nextSeq,
				"exited":        p.exited,
				"closed":        p.closed,
				"sandboxDenied": false,
			}
			if p.exited {
				resp["exitCode"] = p.exitCode
			} else {
				resp["exitCode"] = nil
			}
			if p.failure != "" {
				resp["failure"] = p.failure
			} else {
				resp["failure"] = nil
			}
			p.mu.Unlock()
			return resp, nil
		}
		p.mu.Unlock()

		remaining := time.Until(deadline)
		timer := time.NewTimer(remaining)
		select {
		case <-notify:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

// --- process/write ---

type writeParams struct {
	ProcessID string `json:"processId"`
	Chunk     string `json:"chunk"`
	WriteID   string `json:"writeId"`
}

func (s *Server) handleProcessWrite(raw json.RawMessage) (any, *rpcError) {
	if err := s.runtimeReady(context.Background()); err != nil {
		return nil, internalError("%v", err)
	}
	var params writeParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, invalidParams("process/write: %v", err)
	}
	if params.WriteID == "" {
		return nil, invalidParams("process/write: writeId must not be empty")
	}
	data, err := base64.StdEncoding.DecodeString(params.Chunk)
	if err != nil {
		return nil, invalidParams("process/write: chunk is not valid base64: %v", err)
	}
	s.mu.Lock()
	p, ok := s.processes[params.ProcessID]
	s.mu.Unlock()
	if !ok {
		return map[string]any{"status": "unknownProcess"}, nil
	}

	p.mu.Lock()
	if !p.stdinOpen || p.exited {
		p.mu.Unlock()
		return map[string]any{"status": "stdinClosed"}, nil
	}
	if s.sess.Managed != nil {
		if previous, ok := p.managedWrites[params.WriteID]; ok {
			p.mu.Unlock()
			if previous.chunk != params.Chunk {
				return nil, invalidParams("request_mismatch: writeId parameters changed")
			}
			if previous.outcome != "accepted" {
				if previous.failure != "" {
					return nil, internalError("%s", previous.failure)
				}
				return nil, internalError("outcome_unknown: stdin write must not be replayed")
			}
			return map[string]any{"status": "accepted"}, nil
		}
		if len(p.managedWrites) >= maxRememberedWrites {
			p.mu.Unlock()
			return nil, internalError("request_budget: managed stdin write IDs exhausted")
		}
		// Reserve before dispatch. A partial/unknown write is never retried with
		// this ID, even if its result could not be persisted.
		p.managedWrites[params.WriteID] = managedWrite{chunk: params.Chunk, outcome: "outcome_unknown"}
	}
	if p.writeIDs[params.WriteID] {
		// A retried write is acknowledged without writing the bytes twice.
		p.mu.Unlock()
		return map[string]any{"status": "accepted"}, nil
	}
	p.mu.Unlock()

	finish := func(string, error) error { return nil }
	if !p.hostBridge {
		var beginErr error
		finish, beginErr = s.sess.BeginGuest(context.Background(), s.runtimeReady, "guest.stdin", struct {
			Nonce  string
			Params writeParams
		}{s.sessionID, params})
		if beginErr != nil {
			p.mu.Lock()
			delete(p.managedWrites, params.WriteID)
			p.mu.Unlock()
			return nil, internalError("%v", beginErr)
		}
	}

	n, err := p.stdin.Write(data)
	if n != len(data) && err == nil {
		err = io.ErrShortWrite
	}
	outcome := "ok"
	if err != nil || n != len(data) {
		outcome = "outcome_unknown"
	}
	if resultErr := finish(outcome, err); resultErr != nil {
		p.mu.Lock()
		if s.sess.Managed != nil {
			p.managedWrites[params.WriteID] = managedWrite{chunk: params.Chunk, outcome: "outcome_unknown", failure: resultErr.Error()}
		}
		p.mu.Unlock()
		return nil, internalError("%v", resultErr)
	}
	if err != nil || n != len(data) {
		return nil, internalError("write to process stdin: %v", err)
	}
	p.mu.Lock()
	if s.sess.Managed != nil {
		p.managedWrites[params.WriteID] = managedWrite{chunk: params.Chunk, outcome: "accepted"}
	}
	p.writeIDs[params.WriteID] = true
	p.writeOrder = append(p.writeOrder, params.WriteID)
	for len(p.writeOrder) > maxRememberedWrites {
		delete(p.writeIDs, p.writeOrder[0])
		p.writeOrder = p.writeOrder[1:]
	}
	p.mu.Unlock()
	return map[string]any{"status": "accepted"}, nil
}

// --- process/signal, process/terminate ---

// killStream terminates a remote process best-effort. reach's transports know
// how to tear down the channel the command runs on (Stream.Close); remote
// process-group signalling is not something a stock sshd offers, and a
// protocol error here would leave codex's Esc-key path worse off than a kill.
func (s *Server) killStream(p *process) {
	p.mu.Lock()
	p.terminated = true
	st := p.stream
	p.mu.Unlock()
	_ = st.Close()
}

type signalParams struct {
	ProcessID string `json:"processId"`
	Signal    string `json:"signal"`
}

func (s *Server) handleProcessSignal(raw json.RawMessage) (any, *rpcError) {
	if s.sess.Managed != nil {
		return nil, invalidParams("process/signal: Guest signal delivery is unsupported; process/terminate closes the transport best effort and does not guarantee Guest termination")
	}
	var params signalParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, invalidParams("process/signal: %v", err)
	}
	s.mu.Lock()
	p, ok := s.processes[params.ProcessID]
	s.mu.Unlock()
	if !ok {
		// Codex's own server treats an unknown id as a no-op; matching that
		// keeps an Esc pressed as the process exits from becoming an error.
		return map[string]any{}, nil
	}
	p.mu.Lock()
	exited := p.exited
	p.mu.Unlock()
	if !exited {
		s.killStream(p)
	}
	return map[string]any{}, nil
}

func (s *Server) handleProcessTerminate(raw json.RawMessage) (any, *rpcError) {
	var params signalParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, invalidParams("process/terminate: %v", err)
	}
	s.mu.Lock()
	p, ok := s.processes[params.ProcessID]
	s.mu.Unlock()
	if !ok {
		return map[string]any{"running": false}, nil
	}
	p.mu.Lock()
	exited := p.exited
	p.mu.Unlock()
	if exited {
		return map[string]any{"running": false}, nil
	}
	s.killStream(p)
	return map[string]any{"running": true}, nil
}
