package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/bojieli/agentreach/internal/reach"
)

func TestManagedContainerDispatchAndRejection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed macOS/POSIX executable fixture")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "dispatch")
	cli := filepath.Join(root, "docker")
	// This runtime records the real subprocess argv and environment. It then
	// executes only the final, already constructed Guest shell payload.
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$DISPATCH\"\n" +
		"printf 'HOME=%s\\n' \"$HOME\" >> \"$DISPATCH\"\n" +
		"for arg do last=$arg; done\nexec /bin/sh -c \"$last\"\n"
	if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ready := true
	checks := 0
	tr, err := NewContainer(ContainerConfig{Runtime: cli, Container: "approved-id", Args: []string{"--config", "/trusted/config", "--host", "unix:///approved.sock"}, Env: []string{"PATH=/usr/bin:/bin", "HOME=/trusted/home", "DISPATCH=" + marker}, Check: func(context.Context) error {
		checks++
		if !ready {
			return errors.New("generation_mismatch")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	result, err := tr.Run(context.Background(), reach.ExecRequest{Command: "printf canary"})
	if err != nil || string(result.Stdout) != "canary" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"--config", "/trusted/config", "--host", "unix:///approved.sock", "exec", "approved-id", "/bin/sh", "-c"}
	if len(lines) < len(want)+2 || !reflect.DeepEqual(lines[:len(want)], want) || lines[len(lines)-1] != "HOME=/trusted/home" {
		t.Fatalf("uncontrolled dispatch: %s", data)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	ready = false
	if _, err := tr.Run(context.Background(), reach.ExecRequest{Command: "touch forbidden"}); err == nil {
		t.Fatal("Run bypassed guard")
	}
	if _, err := tr.Open(context.Background(), "touch forbidden"); err == nil {
		t.Fatal("Open bypassed guard")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) || checks != 3 {
		t.Fatalf("rejected request reached runtime: checks=%d err=%v", checks, err)
	}
}
