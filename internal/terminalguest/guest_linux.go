//go:build linux

// Package terminalguest implements the fixed Linux tmux/collector helper modes.
package terminalguest

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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bojieli/agentreach/internal/rooted"
	"github.com/bojieli/agentreach/internal/state"
	"github.com/bojieli/agentreach/internal/terminal"
	"github.com/bojieli/agentreach/internal/transport"
)

const tmuxPath = "/usr/bin/tmux"

type metadata struct {
	Schema   int               `json:"schema"`
	Identity terminal.Identity `json:"identity"`
}
type exitEvidence struct {
	Kind string `json:"kind"`
	Code *int   `json:"code,omitempty"`
}
type progress struct {
	Offset    int64  `json:"offset"`
	Log       string `json:"log"`
	Collector string `json:"collector"`
	Done      bool   `json:"done"`
	Gap       string `json:"gap,omitempty"`
}

func baseDir() string { return "/tmp/agentreach-terminals-" + strconv.Itoa(os.Geteuid()) }
func private(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && fi.Mode().Perm()&0077 == 0 && (fi.IsDir() || st.Nlink == 1)
}
func directory(p string, create bool) error {
	if create {
		if e := os.Mkdir(p, 0700); e != nil && !os.IsExist(e) {
			return e
		}
	}
	fi, e := os.Lstat(p)
	if e != nil {
		return e
	}
	if !fi.IsDir() || !private(fi) {
		return fmt.Errorf("identity_changed: unsafe terminal directory")
	}
	return nil
}
func openDir(id string, create bool) (*os.Root, error) {
	if !terminal.ValidID(id) {
		return nil, fmt.Errorf("policy_denied: invalid terminal identity")
	}
	if e := directory(baseDir(), create); e != nil {
		return nil, e
	}
	p := filepath.Join(baseDir(), id)
	if create {
		if e := os.Mkdir(p, 0700); e != nil {
			return nil, e
		}
	}
	if e := directory(p, false); e != nil {
		return nil, e
	}
	return os.OpenRoot(p)
}
func leaf(r *os.Root, name string, flags int) (*os.File, error) {
	before, e := r.Lstat(name)
	if e != nil {
		return nil, e
	}
	if !before.Mode().IsRegular() || !private(before) {
		return nil, fmt.Errorf("output_gap: unsafe terminal file")
	}
	f, e := r.OpenFile(name, flags, 0)
	if e != nil {
		return nil, e
	}
	after, e := f.Stat()
	if e != nil || !os.SameFile(before, after) {
		_ = f.Close()
		return nil, fmt.Errorf("output_gap: terminal file changed")
	}
	return f, nil
}
func load(r *os.Root, name string, v any) error {
	f, e := leaf(r, name, os.O_RDONLY)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	b, e := io.ReadAll(io.LimitReader(f, terminal.MaxFrame+1))
	if e != nil {
		return e
	}
	if len(b) > terminal.MaxFrame {
		return fmt.Errorf("request_budget")
	}
	return state.Decode(b, v)
}
func save(r *os.Root, name string, v any) error {
	if fi, err := r.Lstat(name); err == nil {
		if !fi.Mode().IsRegular() || !private(fi) {
			return fmt.Errorf("identity_changed: unsafe control leaf")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	id, e := terminal.NewID()
	if e != nil {
		return e
	}
	tmp := ".write-" + id
	f, e := r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer func() { _ = r.Remove(tmp) }()
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = r.Rename(tmp, name); e != nil {
		return e
	}
	d, e := r.Open(".")
	if e != nil {
		return e
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
func processIdentity(pid string) (string, error) {
	n, e := strconv.Atoi(pid)
	if e != nil || n <= 0 {
		return "", fmt.Errorf("identity_changed: invalid pid")
	}
	b, e := os.ReadFile("/proc/" + pid + "/stat")
	if e != nil {
		return "", e
	}
	end := bytes.LastIndexByte(b, ')')
	if end < 0 {
		return "", fmt.Errorf("identity_changed: invalid proc stat")
	}
	fields := strings.Fields(string(b[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" {
		return "", fmt.Errorf("resource_lost: process exited")
	}
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(string(boot)) + ":" + pid + ":" + fields[19], nil
}
func tmux(ctx context.Context, id string, input []byte, args ...string) (string, error) {
	argv := []string{"-S", filepath.Join(baseDir(), id, "socket"), "-f", "/dev/null"}
	argv = append(argv, args...)
	c := exec.CommandContext(ctx, tmuxPath, argv...)
	c.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LANG=C.UTF-8", "TERM=xterm-256color"}
	c.Dir = "/"
	c.Stdin = bytes.NewReader(input)
	buf := &boundedOutput{}
	c.Stdout = buf
	c.Stderr = buf
	e := c.Run()
	b := buf.Bytes()
	if buf.exceeded {
		return "", fmt.Errorf("request_budget: tmux response")
	}
	if e != nil {
		return "", fmt.Errorf("tmux: %w: %s", e, b)
	}
	return strings.TrimSpace(string(b)), nil
}
func actual(ctx context.Context, m metadata) error {
	out, e := tmux(ctx, m.Identity.ID, nil, "display-message", "-p", "-t", m.Identity.Pane, "#{pid}|#{session_id}|#{pane_id}|#{@reach_nonce}")
	if e != nil {
		return e
	}
	parts := strings.Split(out, "|")
	if len(parts) != 4 {
		return &terminal.BackendIdentityError{Cause: fmt.Errorf("backend format")}
	}
	p, e := processIdentity(parts[0])
	if e != nil {
		return e
	}
	if p != m.Identity.Server || parts[1] != m.Identity.Session || parts[2] != m.Identity.Pane || parts[3] != m.Identity.Nonce {
		return &terminal.BackendIdentityError{Cause: fmt.Errorf("backend instance")}
	}
	return nil
}
func same(a, b terminal.Identity) bool {
	return a.ID == b.ID && a.Epoch == b.Epoch && a.Nonce == b.Nonce && a.Binding == b.Binding && (b.Server == "" || a == b)
}
func waitFile(ctx context.Context, r *os.Root, name string, v any) error {
	for {
		e := load(r, name, v)
		if e == nil {
			return nil
		}
		if !os.IsNotExist(e) {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Call handles one bounded, typed terminal operation. It never accepts arbitrary control paths.
func Call(in io.Reader, out io.Writer) error {
	b, e := io.ReadAll(io.LimitReader(in, terminal.MaxFrame+1))
	if e != nil {
		return e
	}
	if len(b) > terminal.MaxFrame {
		return fmt.Errorf("request_budget")
	}
	var q terminal.GuestRequest
	if e = state.Decode(b, &q); e != nil {
		return e
	}
	if !terminal.ValidID(q.Identity.ID) || !terminal.ValidID(q.Identity.Epoch) || !terminal.ValidID(q.Identity.Nonce) || len(q.Identity.Binding) != 64 {
		return fmt.Errorf("policy_denied: invalid binding")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var obs terminal.Observation
	if q.Op == "create" {
		obs, e = create(ctx, q)
	} else {
		obs, e = operate(ctx, q)
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(out).Encode(obs)
}
func create(ctx context.Context, q terminal.GuestRequest) (terminal.Observation, error) {
	var obs terminal.Observation
	if q.Command == "" || len(q.Command) > terminal.MaxFrame || q.Cols < 1 || q.Cols > 500 || q.Rows < 1 || q.Rows > 300 {
		return obs, fmt.Errorf("policy_denied: terminal create bounds")
	}
	v, e := exec.CommandContext(ctx, tmuxPath, "-V").Output()
	if e != nil || strings.TrimSpace(string(v)) != "tmux 3.3a" {
		return obs, fmt.Errorf("unsupported: terminal requires tested tmux 3.3a")
	}
	r, e := openDir(q.Identity.ID, true)
	if e != nil {
		return obs, e
	}
	defer func() { _ = r.Close() }()
	m := metadata{Schema: 1, Identity: q.Identity}
	if e = save(r, "identity.json", m); e != nil {
		return obs, e
	}
	// The rooted launcher opens/holds cwd before this pane helper waits for go.json.
	out, e := tmux(ctx, q.Identity.ID, nil, "new-session", "-d", "-P", "-F", "#{pid}|#{session_id}|#{pane_id}", "-s", "t"+q.Identity.ID, "-x", strconv.Itoa(q.Cols), "-y", strconv.Itoa(q.Rows), q.Command)
	if e != nil {
		return obs, e
	}
	p := strings.Split(out, "|")
	if len(p) != 3 {
		return obs, fmt.Errorf("identity_changed: backend format")
	}
	m.Identity.Server, e = processIdentity(p[0])
	if e != nil {
		return obs, e
	}
	m.Identity.Session = p[1]
	m.Identity.Pane = p[2]
	if e = save(r, "identity.json", m); e != nil {
		return obs, e
	}
	if _, e = tmux(ctx, q.Identity.ID, nil, "set-option", "-p", "-t", p[2], "@reach_nonce", m.Identity.Nonce); e != nil {
		return obs, e
	}
	if _, e = tmux(ctx, q.Identity.ID, nil, "set-option", "-w", "-t", p[2], "remain-on-exit", "off"); e != nil {
		return obs, e
	}
	// A control pane keeps a valid hook context after the user pane disappears.
	// Unlike remain-on-exit, natural pane destruction does not wait for a possibly
	// missed SIGCHLD status notification. tmux drains its pipe buffer first.
	anchor := "c" + q.Identity.ID
	if _, e = tmux(ctx, q.Identity.ID, nil, "new-session", "-d", "-s", anchor, "/usr/bin/sleep infinity"); e != nil {
		return obs, e
	}
	action := "run-shell " + transport.ShellQuote(rooted.HelperPath+" terminal-exited "+q.Identity.ID+" drained") + " ; kill-session -t =" + anchor
	hook := "if-shell -F " + transport.ShellQuote("#{==:#{hook_pane},"+m.Identity.Pane+"}") + " " + transport.ShellQuote(action)
	if _, e = tmux(ctx, q.Identity.ID, nil, "set-hook", "-g", "pane-exited", hook); e != nil {
		return obs, e
	}

	if _, e = tmux(ctx, q.Identity.ID, nil, "pipe-pane", "-O", "-t", p[2], rooted.HelperPath+" terminal-collect "+q.Identity.ID); e != nil {
		return obs, e
	}
	var pr progress
	if e = waitFile(ctx, r, "progress.json", &pr); e != nil {
		return obs, e
	}
	var ready bool
	if e = waitFile(ctx, r, "pane-ready.json", &ready); e != nil {
		return obs, e
	}
	if !ready || pr.Done || pr.Gap != "" {
		return obs, fmt.Errorf("output_gap: startup barrier failed")
	}
	if e = actual(ctx, m); e != nil {
		return obs, e
	}
	if e = save(r, "go.json", true); e != nil {
		return obs, e
	}
	return terminal.Observation{Identity: m.Identity, State: "running"}, nil
}
func observe(ctx context.Context, r *os.Root, m metadata) (terminal.Observation, error) {
	obs := terminal.Observation{Identity: m.Identity, State: "running"}
	var pr progress
	if e := load(r, "progress.json", &pr); e != nil {
		obs.State = "unavailable"
		obs.Gap = "collector_state_missing"
		return obs, nil //nolint:nilerr // The failure is returned as an explicit unavailable/lost observation and gap.
	}
	if pr.Offset < 0 || pr.Offset > terminal.MaxLog || pr.Log == "" {
		return obs, fmt.Errorf("output_gap: invalid collector progress")
	}
	obs.Offset = pr.Offset
	obs.Gap = pr.Gap
	f, e := leaf(r, "output", os.O_RDONLY)
	if e != nil {
		obs.State = "lost"
		obs.Gap = "log_missing"
		return obs, nil //nolint:nilerr // The failure is returned as an explicit unavailable/lost observation and gap.
	}
	fi, e := f.Stat()
	_ = f.Close()
	if e != nil {
		return obs, e
	}
	if fileIdentity(fi) != pr.Log || fi.Size() < pr.Offset || fi.Size() > terminal.MaxLog {
		obs.State = "lost"
		obs.Gap = "log_changed"
		return obs, nil //nolint:nilerr // The failure is returned as an explicit unavailable/lost observation and gap.
	}
	var closing bool
	closeErr := load(r, "close-intent.json", &closing)
	if closeErr != nil && !os.IsNotExist(closeErr) {
		obs.State = "unavailable"
		obs.Gap = "close_state_invalid"
		return obs, nil
	}
	if closeErr == nil && closing {
		obs.State = "unavailable"
		obs.Gap = "closed_before_drain"
	}
	var closed bool
	closeErr = load(r, "closed.json", &closed)
	if closeErr != nil && !os.IsNotExist(closeErr) {
		obs.State = "unavailable"
		obs.Gap = "close_state_invalid"
		return obs, nil
	}
	if closeErr == nil && closed {
		obs.State = "closed"
		obs.Gap = "closed_before_drain"
		return obs, nil
	}
	if closing {
		return obs, nil
	}
	if pr.Done {
		var ended exitEvidence
		if e = load(r, "exit.json", &ended); e == nil && ended.Kind == "pane_exited" && obs.Gap == "" {
			obs.State = "exited"
			obs.Complete = true
			obs.ExitCode = ended.Code
		} else {
			obs.State = "unavailable"
			if obs.Gap == "" {
				obs.Gap = "exit_unconfirmed"
			}
		}
		return obs, nil //nolint:nilerr // The failure is returned as an explicit unavailable/lost observation and gap.
	}
	var ended exitEvidence
	if load(r, "exit.json", &ended) == nil && ended.Kind == "pane_exited" {
		obs.State = "exited"
		return obs, nil
	}
	// A collector identity is boot:pid:starttime, not a PID alone.
	parts := strings.Split(pr.Collector, ":")
	if len(parts) != 3 {
		return obs, fmt.Errorf("output_gap: collector identity")
	}
	cur, e := processIdentity(parts[1])
	if e != nil || cur != pr.Collector {
		obs.State = "unavailable"
		obs.Gap = "collector_failed"
		return obs, nil //nolint:nilerr // The failure is returned as an explicit unavailable/lost observation and gap.
	}
	if e = actual(ctx, m); e != nil {
		if load(r, "exit.json", &ended) == nil && ended.Kind == "pane_exited" {
			obs.State = "exited"
			return obs, nil
		} //nolint:nilerr // Natural exit evidence may appear during the backend check.
		fields := strings.Split(m.Identity.Server, ":")
		if len(fields) == 3 {
			if cur, err := processIdentity(fields[1]); err != nil || cur != m.Identity.Server {
				obs.State = "lost"
				obs.Gap = "backend_lost"
				return obs, nil //nolint:nilerr // The failure is returned as an explicit unavailable/lost observation and gap.
			}
		}
		var identityFailure *terminal.BackendIdentityError
		if errors.As(e, &identityFailure) {
			obs.State = "unavailable"
			obs.Gap = "backend_identity_changed"
			return obs, nil
		} //nolint:nilerr // The identity failure is a typed observation for the Host latch.
		obs.State = "unavailable"
		obs.Gap = "backend_unavailable"
	}
	return obs, nil //nolint:nilerr // The failure is returned as an explicit unavailable/lost observation and gap.
}
func operate(ctx context.Context, q terminal.GuestRequest) (terminal.Observation, error) {
	r, e := openDir(q.Identity.ID, false)
	if e != nil {
		return terminal.Observation{Identity: q.Identity, State: "lost", Gap: "backend_missing"}, nil
	}
	defer func() { _ = r.Close() }()
	var m metadata
	if e = load(r, "identity.json", &m); e != nil {
		return terminal.Observation{}, e
	}
	if m.Schema != 1 || !same(m.Identity, q.Identity) {
		return terminal.Observation{}, fmt.Errorf("identity_changed: terminal binding")
	}
	obs, e := observe(ctx, r, m)
	if e != nil {
		return obs, e
	}
	switch q.Op {
	case "inspect":
		return obs, nil
	case "read":
		if q.Offset < 0 || q.Offset > obs.Offset || q.Limit < 1 || q.Limit > terminal.MaxRead {
			return obs, fmt.Errorf("output_gap: invalid cursor or read budget")
		}
		if obs.Gap == "log_missing" || obs.Gap == "log_changed" {
			return obs, nil
		}
		f, e := leaf(r, "output", os.O_RDONLY)
		if e != nil {
			return obs, e
		}
		defer func() { _ = f.Close() }()
		n := min(int64(q.Limit), obs.Offset-q.Offset)
		obs.Data = make([]byte, n)
		count, readErr := f.ReadAt(obs.Data, q.Offset)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return obs, readErr
		}
		if count != len(obs.Data) {
			obs.Data = obs.Data[:count]
			obs.Gap = "log_truncated"
			obs.State = "lost"
			obs.Complete = false
		}
		obs.Offset = q.Offset + int64(count)
		return obs, nil
	case "write", "resize", "interrupt", "close":
		if obs.State == "exited" && q.Op == "close" {
			obs.State = "closed"
			obs.Descendants = "unconfirmed"
			return obs, nil
		}
		if q.Op != "close" && (obs.State != "running" || obs.Gap != "") {
			return obs, fmt.Errorf("resource_lost: terminal is not writable")
		}
		if e = actual(ctx, m); e != nil {
			return obs, e
		}
	default:
		return obs, fmt.Errorf("unsupported: terminal operation")
	}
	switch q.Op {
	case "write":
		if len(q.Data) > terminal.MaxInput || len(q.Data) == 0 && q.Key == "" || len(q.Data) > 0 && q.Key != "" {
			return obs, fmt.Errorf("request_budget: invalid input")
		}
		if q.Key != "" {
			if q.Key != "Enter" {
				return obs, fmt.Errorf("unsupported: terminal key")
			}
			_, e = tmux(ctx, m.Identity.ID, nil, "send-keys", "-t", m.Identity.Pane, "Enter")
		} else {
			name := "b" + m.Identity.Nonce
			if _, e = tmux(ctx, m.Identity.ID, q.Data, "load-buffer", "-b", name, "-"); e == nil {
				_, e = tmux(ctx, m.Identity.ID, nil, "paste-buffer", "-r", "-d", "-b", name, "-t", m.Identity.Pane)
			}
		}
	case "resize":
		if q.Cols < 1 || q.Cols > 500 || q.Rows < 1 || q.Rows > 300 {
			return obs, fmt.Errorf("request_budget: terminal dimensions")
		}
		_, e = tmux(ctx, m.Identity.ID, nil, "resize-window", "-t", m.Identity.Pane, "-x", strconv.Itoa(q.Cols), "-y", strconv.Itoa(q.Rows))
	case "interrupt":
		_, e = tmux(ctx, m.Identity.ID, nil, "send-keys", "-t", m.Identity.Pane, "C-c")
	case "close":
		// kill-pane is deliberately scoped to the exact verified backend. It cannot prove that detached descendants died.
		if e = save(r, "close-intent.json", true); e != nil {
			return obs, e
		}
		_, e = tmux(ctx, m.Identity.ID, nil, "kill-pane", "-t", m.Identity.Pane)
		if e == nil {
			_, cleanupErr := tmux(ctx, m.Identity.ID, nil, "kill-session", "-t", "=c"+m.Identity.ID)
			if cleanupErr != nil {
				fields := strings.Split(m.Identity.Server, ":")
				if len(fields) != 3 {
					return obs, cleanupErr
				}
				current, err := processIdentity(fields[1])
				if err == nil && current == m.Identity.Server {
					return obs, cleanupErr
				}
			}
			if e = save(r, "closed.json", true); e != nil {
				return obs, e
			}
			obs.State = "closed"
			obs.Complete = false
			obs.Gap = "closed_before_drain"
			obs.Descendants = "unconfirmed"
		}
	}
	return obs, e
}
func fileIdentity(fi os.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
}

// Collect is the sole output writer. Host polling is not needed for collection.
func Collect(id string) error {
	r, e := openDir(id, false)
	if e != nil {
		return e
	}
	defer func() { _ = r.Close() }()
	var m metadata
	if e = load(r, "identity.json", &m); e != nil {
		return e
	}
	f, e := r.OpenFile("output", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	fi, e := f.Stat()
	if e != nil {
		return e
	}
	pid, e := processIdentity(strconv.Itoa(os.Getpid()))
	if e != nil {
		return e
	}
	pr := progress{Log: fileIdentity(fi), Collector: pid}
	if e = save(r, "progress.json", pr); e != nil {
		return e
	}
	buf := make([]byte, 8192)
	for {
		n, re := os.Stdin.Read(buf)
		if n > 0 {
			count := min(int64(n), terminal.MaxLog-pr.Offset)
			if count > 0 {
				if _, e = f.Write(buf[:count]); e != nil {
					return e
				}
				if e = f.Sync(); e != nil {
					return e
				}
				pr.Offset += count
			}
			if count < int64(n) {
				pr.Gap = "output_limit"
				pr.Done = true
				if e = save(r, "progress.json", pr); e != nil {
					return e
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if e = actual(ctx, m); e == nil {
					_, e = tmux(ctx, id, nil, "kill-pane", "-t", m.Identity.Pane)
					_, _ = tmux(ctx, id, nil, "kill-session", "-t", "=c"+id)
				}
				return e
			}
			if e = save(r, "progress.json", pr); e != nil {
				return e
			}
		}
		if re != nil {
			if !errors.Is(re, io.EOF) {
				return re
			}
			break
		}
	}
	if e = f.Sync(); e != nil {
		return e
	}
	pr.Done = true
	return save(r, "progress.json", pr)
}

// Pane waits behind the collector-ready release barrier while retaining the rooted cwd.
func Pane(id, encoded string) error {
	r, e := openDir(id, false)
	if e != nil {
		return e
	}
	defer func() { _ = r.Close() }()
	b, e := base64.RawURLEncoding.DecodeString(encoded)
	if e != nil {
		return e
	}
	var argv []string
	if e = state.Decode(b, &argv); e != nil {
		return e
	}
	if len(argv) == 0 || len(argv) > 128 {
		return fmt.Errorf("request_budget: argv")
	}
	if e = save(r, "pane-ready.json", true); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var goFlag bool
	if e = waitFile(ctx, r, "go.json", &goFlag); e != nil {
		return e
	}
	if !goFlag {
		return fmt.Errorf("startup barrier")
	}
	name, e := exec.LookPath(argv[0])
	if e != nil {
		return e
	}
	_ = r.Close()
	return syscall.Exec(name, argv, os.Environ())
}

// Exited persists tmux's natural pane exit evidence independently of collector EOF.
func Exited(id, status string) error {
	ended := exitEvidence{Kind: "pane_exited"}
	if status != "drained" {
		parts := strings.Split(status, ":")
		if len(parts) != 2 {
			return fmt.Errorf("invalid pane exit status")
		}
		if code, e := strconv.Atoi(parts[0]); e == nil && code >= 0 && code <= 255 {
			ended.Code = &code
		} else {
			return fmt.Errorf("invalid pane exit status")
		}
	}
	r, e := openDir(id, false)
	if e != nil {
		return e
	}
	defer func() { _ = r.Close() }()
	return save(r, "exit.json", ended)
}

type boundedOutput struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > terminal.MaxFrame-b.Len() {
		b.exceeded = true
		return 0, fmt.Errorf("request_budget: tmux output")
	}
	return b.Buffer.Write(p)
}
