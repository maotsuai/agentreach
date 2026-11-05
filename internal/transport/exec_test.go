package transport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bojieli/agentreach/internal/reach"
)

// TestExitIsNotMistakenForTransportFailure guards the subshell decision in
// wrapWithSentinel. Agents write `exit N` routinely; if that killed the
// wrapper, reach would report a transport failure for an ordinary command.
func TestExitIsNotMistakenForTransportFailure(t *testing.T) {
	tr := localTransport(t)
	for _, code := range []int{0, 1, 3, 66, 127, 255} {
		res, err := tr.Run(context.Background(), reach.ExecRequest{
			Command: "echo before; exit " + itoa(code),
		})
		if err != nil {
			t.Fatalf("exit %d reported as transport failure: %v", code, err)
		}
		if res.Code != code {
			t.Errorf("exit %d: got code %d", code, res.Code)
		}
		if !strings.Contains(string(res.Stdout), "before") {
			t.Errorf("exit %d: lost stdout produced before exit", code)
		}
	}
}

func TestStdoutIsByteExact(t *testing.T) {
	tr := localTransport(t)
	for _, tc := range []struct{ cmd, want string }{
		{"printf 'no newline'", "no newline"},
		{"printf 'with newline\\n'", "with newline\n"},
		{"printf ''", ""},
		{"printf 'two\\n\\n'", "two\n\n"},
	} {
		res, err := tr.Run(context.Background(), reach.ExecRequest{Command: tc.cmd})
		if err != nil {
			t.Fatalf("%s: %v", tc.cmd, err)
		}
		if string(res.Stdout) != tc.want {
			t.Errorf("%s: got %q want %q", tc.cmd, res.Stdout, tc.want)
		}
	}
}

// TestOutputCapPreservesHeadTailAndExitCode is the regression test for a bug
// where capping stdout discarded the trailing status marker, making every
// high-output command look like a transport failure.
func TestOutputCapPreservesHeadTailAndExitCode(t *testing.T) {
	tr := localTransport(t)
	const outputCap = 4096
	res, err := tr.Run(context.Background(), reach.ExecRequest{
		Command:   "echo FIRST_LINE; yes abcdefghij | head -n 200000; echo LAST_LINE; exit 7",
		MaxOutput: outputCap,
	})
	if err != nil {
		t.Fatalf("flooding command reported as transport failure: %v", err)
	}
	// The cap bounds retained content; the truncation notice is metadata and
	// may push the total slightly past it.
	if len(res.Stdout) > outputCap+256 {
		t.Errorf("cap not enforced: %d bytes (cap %d)", len(res.Stdout), outputCap)
	}
	if !res.Truncated {
		t.Error("truncation not reported")
	}
	if res.Code != 7 {
		t.Errorf("exit code lost to truncation: got %d want 7", res.Code)
	}
	out := string(res.Stdout)
	if !strings.Contains(out, "FIRST_LINE") {
		t.Error("head of output not preserved")
	}
	// The end is where failures live; losing it is the expensive mistake.
	if !strings.Contains(out, "LAST_LINE") {
		t.Error("tail of output not preserved")
	}
	if !strings.Contains(out, "truncated") {
		t.Error("no truncation notice between head and tail")
	}
}

func TestShellQuoteSurvivesHostileInput(t *testing.T) {
	tr := localTransport(t)
	for _, s := range []string{
		"plain", "with space", "it's", `"double"`, "$(whoami)", "`id`",
		"back\\slash", "semi;colon", "pipe|char", "new\nline", "{brace}", "*glob*",
	} {
		res, err := tr.Run(context.Background(), reach.ExecRequest{
			Command: "printf '%s' " + ShellQuote(s),
		})
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if string(res.Stdout) != s {
			t.Errorf("quote round trip: got %q want %q", res.Stdout, s)
		}
	}
}

func TestBuildCommandChainsDirectorySafely(t *testing.T) {
	got := BuildCommand(reach.ExecRequest{Command: "ls", Dir: "/tmp/a b"})
	if !strings.HasPrefix(got, "cd '/tmp/a b' || exit $?\n") {
		t.Errorf("directory failure does not exit before the body: %q", got)
	}
}

func TestMissingDirectoryPreventsEntireCommandBody(t *testing.T) {
	tr := localTransport(t)
	for _, env := range []map[string]string{nil, {"REACH_A": "one"}, {"REACH_A": "one", "REACH_B": "two"}} {
		dir := t.TempDir()
		marker := filepath.Join(dir, "must-not-exist")
		res, err := tr.Run(context.Background(), reach.ExecRequest{
			Dir: filepath.Join(dir, "missing"), Env: env,
			Command: "true; printf bad > " + ShellQuote(marker) + "\n{ printf also-bad >> " + ShellQuote(marker) + "; }",
		})
		if err != nil || res.Code == 0 {
			t.Fatalf("missing cwd must be a nonzero command result, result=%+v err=%v", res, err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("body ran despite missing cwd: %v", err)
		}
	}
}

func TestDirectoryAndEnvironmentPreserveHeredocBody(t *testing.T) {
	tr := localTransport(t)
	dir := t.TempDir()
	res, err := tr.Run(context.Background(), reach.ExecRequest{
		Dir: dir, Env: map[string]string{"REACH_VALUE": "one two"},
		Command: "cat > relative <<'EOF'\nfirst\nEOF\nprintf '%s' \"$REACH_VALUE\" >> relative\ncat relative\nexit 7",
	})
	if err != nil || res.Code != 7 || string(res.Stdout) != "first\none two" {
		t.Fatalf("heredoc/env/exit changed: result=%+v err=%v", res, err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// localTransport builds a local:// transport, skipping when this machine cannot
// be a target. A local target needs a POSIX shell, which Windows only has when
// Git for Windows or MSYS2 supplied one — and a Windows machine is never a
// supported target, so skipping is the honest outcome rather than a failure.
func localTransport(t *testing.T) *LocalTransport {
	t.Helper()
	tr, err := NewLocal()
	if err != nil {
		t.Skipf("this machine cannot host a local:// target: %v", err)
	}
	return tr
}

// TestEnvSurvivesCompoundCommands is the regression test for a branch that was
// wrong for as long as it existed and never ran.
//
// BuildCommand rendered an environment as `env K=V <command>`. env takes a
// *command*, and several of reach's are shell constructs — the tier-0 write is
// `{ ...; } || { ...; }` — so the shell failed at the brace with a syntax
// error. Nothing set Env until reach began carrying the target's login PATH, so
// the defect shipped undetected.
func TestEnvSurvivesCompoundCommands(t *testing.T) {
	tr := localTransport(t)
	ctx := context.Background()

	for _, tc := range []struct{ name, command, want string }{
		{"simple", `printf %s "$REACH_T"`, "value"},
		{"brace group", `{ printf %s "$REACH_T"; }`, "value"},
		{"or list", `{ printf %s "$REACH_T"; } || { printf nope; }`, "value"},
		{"subshell", `( printf %s "$REACH_T" )`, "value"},
		{"pipeline", `printf %s "$REACH_T" | cat`, "value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tr.Run(ctx, reach.ExecRequest{
				Command: tc.command,
				Env:     map[string]string{"REACH_T": "value"},
			})
			if err != nil {
				t.Fatalf("%s: %v", tc.command, err)
			}
			if string(res.Stdout) != tc.want {
				t.Errorf("%s: got %q want %q", tc.command, res.Stdout, tc.want)
			}
		})
	}

	// A value containing shell metacharacters must reach the command intact.
	res, err := tr.Run(ctx, reach.ExecRequest{
		Command: `printf %s "$REACH_T"`,
		Env:     map[string]string{"REACH_T": `a b'c"d$e;f|g`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != `a b'c"d$e;f|g` {
		t.Errorf("metacharacters mangled: got %q", res.Stdout)
	}
}
