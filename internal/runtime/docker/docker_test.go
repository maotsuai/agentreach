package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestOnlyExactMissingPinnedContainerChangesIdentity(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, tc := range []struct{ name, id, out, want string }{
		{"missing", id, "[]\nError response from daemon: No such container: " + id, "identity_changed"},
		{"connection", id, "Cannot connect to the Docker daemon", "engine_unavailable"},
		{"other-ID", id, "[]\nError response from daemon: No such container: " + strings.Repeat("b", 64), "engine_unavailable"},
		{"name", "name", "[]\nError response from daemon: No such container: name", "engine_unavailable"},
		{"mixed-error", id, "[]\nError response from daemon: No such container: " + id + "\nother error", "engine_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := &Client{Run: func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
				calls++
				if strings.Join(args, " ") == "exec" {
					t.Fatal("unexpected execution")
				}
				return []byte(tc.out), Fail("engine_unavailable", "original inspect cause")
			}}
			_, err := c.Inspect(context.Background(), tc.id)
			var failure *Error
			if !errors.As(err, &failure) || failure.Kind != tc.want || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			if tc.want == "identity_changed" && !strings.Contains(err.Error(), "register") {
				t.Fatal("missing recovery guidance")
			}
		})
	}
}

func TestPinnedConnectionUsesFixedHomeAndEndpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed macOS/POSIX executable fixture")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "docker")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	c := Connection{CLI: cli, ConfigDir: root, ControlHome: root, Context: "orbstack", Endpoint: "unix:///approved.sock"}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DOCKER_HOST", c.Endpoint)
	t.Setenv("DOCKER_CONTEXT", c.Context)
	t.Setenv("DOCKER_CONFIG", c.ConfigDir)
	wantEnv := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + root, "LANG=C", "LC_ALL=C"}
	calls := 0
	client := Client{Connection: c, Run: func(_ context.Context, binary string, args, env []string) ([]byte, error) {
		calls++
		if binary != cli || !reflect.DeepEqual(env, wantEnv) {
			t.Fatalf("binary=%q env=%v", binary, env)
		}
		want := []string{"--config", root, "--host", c.Endpoint, "inspect", "--type", "container", strings.Repeat("a", 64)}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("argv=%q, want %q", args, want)
		}
		return []byte(`[{"Id":"approved"}]`), nil
	}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Inspect(context.Background(), strings.Repeat("a", 64)); err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	for _, key := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "DOCKER_API_VERSION"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "hostile")
			before := calls
			if _, err := client.Inspect(context.Background(), "ignored"); err == nil || calls != before {
				t.Fatalf("connection override dispatched: calls=%d err=%v", calls, err)
			}
		})
	}
}
