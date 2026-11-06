//go:build linux

package rooted

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/transport"
)

// TestLauncherChild is a one-shot real launcher process, never a fake success response.
func TestLauncherChild(_ *testing.T) {
	if os.Getenv("REACH_ROOTED_TEST_CHILD") != "1" {
		return
	}
	for i, a := range os.Args {
		if a == "--" {
			args := os.Args[i+1:]
			if len(args) == 2 && args[0] == "report-cwd" {
				if e := ReportCwd(args[1], os.Stderr); e != nil {
					fmt.Fprintln(os.Stderr, e)
					os.Exit(1)
				}
				os.Exit(0)
			}
			if len(args) != 4 || args[0] != "rooted-exec" {
				os.Exit(2)
			}
			if e := Exec(args[1], args[2], args[3]); e != nil {
				fmt.Fprintln(os.Stderr, e)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}
func launcherTransport(t *testing.T) (transport.Transport, string) {
	t.Helper()
	self, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	script := filepath.Join(t.TempDir(), "helper")
	body := "#!/bin/sh\nREACH_ROOTED_TEST_CHILD=1 exec " + transport.ShellQuote(self) + " -test.run=TestLauncherChild -- \"$@\"\n"
	if e = os.WriteFile(script, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	tr, e := transport.NewLocal()
	if e != nil {
		t.Fatal(e)
	}
	return tr, script
}
func TestLauncherActualTransportRejectsCwdAndPreservesCommand(t *testing.T) {
	_, root, out := fixture(t)
	tr, helper := launcherTransport(t)
	if e := os.Mkdir(root+"/子 dir", 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink("../outside", root+"/escape"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(root+"/file", nil, 0600); e != nil {
		t.Fatal(e)
	}
	for _, cwd := range []string{"子 dir", "escape", "escape/no", "../outside", "file"} {
		command := "printf %s \"$VALUE\"; pwd -P; touch " + transport.ShellQuote(out+"/body") + "\nprintf after"
		cmd, e := Command(root, cwd, reach.ExecRequest{Command: command, Env: map[string]string{"VALUE": "literal /Volumes/stays"}}, "")
		if e != nil {
			t.Fatal(e)
		}
		cmd = strings.Replace(cmd, HelperPath, helper, 1)
		res, e := tr.Run(context.Background(), reach.ExecRequest{Command: cmd})
		if e != nil {
			t.Fatal(e)
		}
		if cwd == "子 dir" {
			if res.Code != 0 || !strings.Contains(string(res.Stdout), "literal /Volumes/stays"+root+"/子 dir") {
				t.Fatalf("valid cwd: %+v", res)
			}
			_ = os.Remove(out + "/body")
		} else {
			if res.Code == 0 || len(res.Stdout) > 0 {
				t.Fatalf("bad cwd ran body: %s %+v", cwd, res)
			}
			if _, e = os.Stat(out + "/body"); !os.IsNotExist(e) {
				t.Fatal("bad cwd side effect")
			}
		}
	}
}
func TestLauncherActualTransportSymlinkSwap(t *testing.T) {
	_, root, out := fixture(t)
	tr, helper := launcherTransport(t)
	for _, p := range []string{root + "/safe", root + "/safe/inner", out + "/inner"} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	for _, cwd := range []string{"flip", "flip/inner"} {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = os.Symlink("safe", root+"/next")
				_ = os.Rename(root+"/next", root+"/flip")
				_ = os.Symlink("../outside", root+"/next")
				_ = os.Rename(root+"/next", root+"/flip")
			}
		}()
		command := "p=$(pwd -P); case \"$p\" in " + transport.ShellQuote(root) + "/*) printf %s \"$p\";; *) touch " + transport.ShellQuote(out+"/violation") + "; exit 99;; esac"
		cmd, e := Command(root, cwd, reach.ExecRequest{Command: command}, "")
		if e != nil {
			t.Fatal(e)
		}
		cmd = strings.Replace(cmd, HelperPath, helper, 1)
		for n := 0; n < 50; n++ {
			var stdout, stderr bytes.Buffer
			code, e := transport.RunStream(context.Background(), tr, reach.ExecRequest{Command: cmd}, &stdout, &stderr)
			if e != nil {
				close(stop)
				wg.Wait()
				t.Fatal(e)
			}
			if code != 0 && code != 1 {
				close(stop)
				wg.Wait()
				t.Fatalf("unexpected exit %d: %s", code, stderr.String())
			}
			if code == 0 && !strings.HasPrefix(stdout.String(), root+"/") {
				close(stop)
				wg.Wait()
				t.Fatalf("outside cwd: %s", stdout.String())
			}
		}
		close(stop)
		wg.Wait()
		if _, e := os.Stat(out + "/violation"); !os.IsNotExist(e) {
			t.Fatal("body started outside root")
		}
	}
}

func TestLauncherRejectsReplacedNegotiatedRoot(t *testing.T) {
	f, root, out := fixture(t)
	id := f.Identity
	tr, helper := launcherTransport(t)
	if e := os.Rename(root, root+"-old"); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	cmd, e := Command(root, ".", reach.ExecRequest{Command: "touch " + transport.ShellQuote(out+"/body")}, id)
	if e != nil {
		t.Fatal(e)
	}
	res, e := tr.Run(context.Background(), reach.ExecRequest{Command: strings.Replace(cmd, HelperPath, helper, 1)})
	if e != nil || res.Code == 0 || !strings.Contains(string(res.Stderr), "identity_changed") {
		t.Fatalf("replaced root accepted: %+v %v", res, e)
	}
	if _, e = os.Stat(out + "/body"); !os.IsNotExist(e) {
		t.Fatal("replaced root body ran")
	}
}

func TestRootedFIFOReadAndCwdDoNotBlock(t *testing.T) {
	f, root, out := fixture(t)
	if e := syscall.Mkfifo(root+"/fifo", 0600); e != nil {
		t.Fatal(e)
	}
	for _, op := range []string{"read", "hash", "list"} {
		done := make(chan error, 1)
		go func() { _, _, e := f.Dispatch(Request{Op: op, Path: "fifo"}, nil); done <- e }()
		select {
		case e := <-done:
			if e == nil {
				t.Fatalf("%s accepted FIFO", op)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s blocked opening FIFO", op)
		}
	}
	tr, helper := launcherTransport(t)
	cmd, e := Command(root, "fifo", reach.ExecRequest{Command: "touch " + transport.ShellQuote(out+"/body")}, "")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, e := tr.Run(ctx, reach.ExecRequest{Command: strings.Replace(cmd, HelperPath, helper, 1)})
	if e != nil || res.Code == 0 || ctx.Err() != nil {
		t.Fatalf("FIFO cwd did not fail promptly: %+v %v", res, e)
	}
	if _, e = os.Stat(out + "/body"); !os.IsNotExist(e) {
		t.Fatal("FIFO cwd body ran")
	}
}

func TestPhysicalCwdReportPreservesNewlinesAndIgnoresPWD(t *testing.T) {
	_, root, _ := fixture(t)
	tr, helper := launcherTransport(t)
	cwd := root + "/line\nbreak\n"
	if e := os.Mkdir(cwd, 0700); e != nil {
		t.Fatal(e)
	}
	command := "PWD=/untrusted\n" + transport.ShellQuote(helper) + " report-cwd marker"
	res, e := tr.Run(context.Background(), reach.ExecRequest{Command: command, Dir: cwd})
	if e != nil || res.Code != 0 {
		t.Fatalf("report: %+v %v", res, e)
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(string(res.Stderr), "marker"), "\n")
	b, e := base64.StdEncoding.DecodeString(raw)
	if e != nil || string(b) != cwd {
		t.Fatalf("physical cwd changed %q %v", b, e)
	}
}
