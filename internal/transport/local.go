package transport

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/bojieli/agentreach/internal/reach"
)

// LocalTransport runs commands on the machine reach itself runs on.
//
// It exists so the whole stack above it — file-operation tiers, the daemon,
// every harness adapter — can be exercised in tests without a network, and so
// `reach up local:///path` is a working degenerate case rather than a special
// path through the code.
type LocalTransport struct {
	Shell string
}

// NewLocal builds a local transport.
//
// It resolves a POSIX shell rather than assuming /bin/sh, because on Windows
// there is not one. A local:// target means "the machine reach is running on is
// the target", and every file-operation tier below the agent speaks to a target
// through a POSIX shell — so on Windows this is only usable when Git for
// Windows or MSYS2 has supplied one, and says so plainly when it has not.
func NewLocal() (*LocalTransport, error) {
	shell, err := localShell()
	if err != nil {
		return nil, err
	}
	return &LocalTransport{Shell: shell}, nil
}

// Run implements Transport.
func (t *LocalTransport) Run(ctx context.Context, req reach.ExecRequest) (reach.ExecResult, error) {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	sentinel := newSentinel()
	wrapped := wrapWithSentinel(BuildCommand(req), sentinel)

	start := time.Now()
	so, se, code, trunc, err := runLocalProcess(ctx, []string{t.Shell, "-c", wrapped}, req.Stdin, req.MaxOutput)
	return finishExec(start, so, se, code, trunc, sentinel, err, "local")
}

// Open implements Transport.
func (t *LocalTransport) Open(ctx context.Context, command string) (Stream, error) {
	cmd := exec.CommandContext(ctx, t.Shell, "-c", command)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Stream{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Stream{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Stream{}, err
	}
	if err := cmd.Start(); err != nil {
		return Stream{}, err
	}
	var once sync.Once
	var waitErr error
	var waitCode int
	wait := func() (int, error) {
		once.Do(func() {
			if e := cmd.Wait(); e != nil {
				var ee *exec.ExitError
				if errors.As(e, &ee) {
					waitCode = ee.ExitCode()
				} else {
					waitErr = e
				}
			}
		})
		return waitCode, waitErr
	}
	return Stream{
		Stdin: stdin, Stdout: stdout, Stderr: stderr, Wait: wait,
		Close: func() error {
			_ = stdin.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_, _ = wait()
			return nil
		},
	}, nil
}

// Describe implements Transport.
func (t *LocalTransport) Describe() string { return "local://" }

// Close implements Transport.
func (t *LocalTransport) Close() error { return nil }

var _ Transport = (*LocalTransport)(nil)

// ContainerConfig configures a container transport.
type ContainerConfig struct {
	// Runtime is the CLI to drive: docker, podman, or nerdctl.
	Runtime string
	// Container is the container name or id.
	Container string
	// User optionally overrides the exec user.
	User string
	// Shell is the shell inside the container.
	Shell string
	// Managed callers pin both connection arguments and the child environment.
	Args  []string
	Env   []string
	Check func(context.Context) error
	// Argv0 selects a fixed multicall entrypoint while Runtime stays resolved.
	// Empty preserves the ordinary Docker/Podman invocation name.
	Argv0 string
}

// ContainerTransport runs commands inside a container via the runtime CLI.
//
// This is the transport to develop against: it exercises the identical code
// path as ssh with no network in between, which makes it the honest way to
// prove transparency claims before blaming a flaky link for a bug.
type ContainerTransport struct {
	cfg     ContainerConfig
	mu      sync.Mutex
	streams map[*int]func() error
}

// NewContainer builds a container transport.
func NewContainer(cfg ContainerConfig) (*ContainerTransport, error) {
	if cfg.Container == "" {
		return nil, fmt.Errorf("container transport: container is required")
	}
	if cfg.Runtime == "" {
		cfg.Runtime = "docker"
	}
	if cfg.Shell == "" {
		cfg.Shell = "/bin/sh"
	}
	return &ContainerTransport{cfg: cfg, streams: map[*int]func() error{}}, nil
}

func (t *ContainerTransport) check(ctx context.Context) error {
	if t.cfg.Check == nil {
		return nil
	}
	if err := t.cfg.Check(ctx); err != nil {
		t.mu.Lock()
		var closeAll []func() error
		for _, close := range t.streams {
			closeAll = append(closeAll, close)
		}
		t.mu.Unlock()
		for _, close := range closeAll {
			_ = close()
		}
		return err
	}
	return nil
}

func (t *ContainerTransport) execArgs(interactive bool) []string {
	args := append([]string{t.cfg.Runtime}, t.cfg.Args...)
	args = append(args, "exec")
	if interactive {
		args = append(args, "-i")
	}
	if t.cfg.User != "" {
		args = append(args, "-u", t.cfg.User)
	}
	return append(args, t.cfg.Container)
}

// Run implements Transport.
func (t *ContainerTransport) Run(ctx context.Context, req reach.ExecRequest) (reach.ExecResult, error) {
	if err := t.check(ctx); err != nil {
		return reach.ExecResult{}, err
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	sentinel := newSentinel()
	wrapped := wrapWithSentinel(BuildCommand(req), sentinel)

	argv := append(t.execArgs(len(req.Stdin) > 0), t.cfg.Shell, "-c", wrapped)
	start := time.Now()
	so, se, code, trunc, err := runLocalProcessAsEnv(ctx, argv, req.Stdin, req.MaxOutput, t.cfg.Env, t.cfg.Argv0)
	return finishExec(start, so, se, code, trunc, sentinel, err, t.cfg.Runtime+" "+t.cfg.Container)
}

// Open implements Transport.
func (t *ContainerTransport) Open(ctx context.Context, command string) (Stream, error) {
	if err := t.check(ctx); err != nil {
		return Stream{}, err
	}
	argv := append(t.execArgs(true), t.cfg.Shell, "-c", command)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if t.cfg.Argv0 != "" {
		cmd.Args[0] = t.cfg.Argv0
	}
	cmd.Env = t.cfg.Env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Stream{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Stream{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Stream{}, err
	}
	if err := cmd.Start(); err != nil {
		return Stream{}, err
	}
	var once sync.Once
	var waitErr error
	var waitCode int
	wait := func() (int, error) {
		once.Do(func() {
			if e := cmd.Wait(); e != nil {
				var ee *exec.ExitError
				if errors.As(e, &ee) {
					waitCode = ee.ExitCode()
				} else {
					waitErr = e
				}
			}
		})
		return waitCode, waitErr
	}
	key := new(int)
	closeStream := func() error {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_, _ = wait()
		t.mu.Lock()
		delete(t.streams, key)
		t.mu.Unlock()
		return nil
	}
	if t.cfg.Check != nil {
		t.mu.Lock()
		t.streams[key] = closeStream
		t.mu.Unlock()
	}
	return Stream{
		Stdin: stdin, Stdout: stdout, Stderr: stderr, Wait: wait,
		Close: closeStream,
	}, nil
}

// Describe implements Transport.
func (t *ContainerTransport) Describe() string {
	return t.cfg.Runtime + "://" + t.cfg.Container
}

// Close implements Transport.
func (t *ContainerTransport) Close() error {
	t.mu.Lock()
	var all []func() error
	for _, close := range t.streams {
		all = append(all, close)
	}
	t.mu.Unlock()
	for _, close := range all {
		_ = close()
	}
	return nil
}

var _ Transport = (*ContainerTransport)(nil)
