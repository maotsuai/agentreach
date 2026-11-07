package ports

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/bojieli/agentreach/internal/policy"
)

func TestRetiredManagerAcceptsOnlyProvenLegacyResponse(t *testing.T) {
	for _, tc := range []struct {
		name, listError string
		ok              bool
	}{
		{"old-schema", "policy_denied: unsupported project schema/provider, invalid id or missing container", true},
		{"wrong-nonce", "identity_changed: Host manager instance/binding", false},
		{"unrelated", "policy_denied: no matching Host session", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := os.MkdirTemp("/tmp", "reach-p5-ports-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(base) })
			binding := policy.Binding{ProjectID: "test", RecordDigest: strings.Repeat("a", 64), Generation: 1}
			paths := func() error { return nil }
			c := retiredClient(base, base, binding, paths)
			nonce := strings.Repeat("b", 32)
			ctx := context.Background()
			unlock, err := c.file().Lock(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = c.file().Save(disk{Binding: binding, Instance: nonce})
			unlock()
			if err != nil {
				t.Fatal(err)
			}
			l, err := net.Listen("unix", c.socket())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = l.Close() }()
			seen := make(chan request, 2)
			go func() {
				for _, reply := range []string{"policy_denied: unknown Host manager request", tc.listError} {
					conn, err := l.Accept()
					if err != nil {
						return
					}
					var q request
					_ = json.NewDecoder(conn).Decode(&q)
					seen <- q
					_ = json.NewEncoder(conn).Encode(response{Error: reply})
					_ = conn.Close()
				}
			}()
			if _, err = c.call(ctx, request{Op: "shutdown", Instance: strings.Repeat("c", 32)}); err == nil || !strings.Contains(err.Error(), "recorded manager instance changed") {
				t.Fatalf("rewrote a stale request nonce: %v", err)
			}
			view, err := ObserveRetired(ctx, base, base, binding, paths)
			if (err == nil) != tc.ok || view.Reachable != tc.ok || view.Legacy != tc.ok {
				t.Fatalf("legacy observation: %+v %v", view, err)
			}
			for _, op := range []string{"recovery_status", "list"} {
				select {
				case q := <-seen:
					if q.Op != op || q.Instance != nonce || q.Binding != binding {
						t.Fatalf("unexpected request: %+v", q)
					}
				default:
					t.Fatalf("missing %s", op)
				}
			}
		})
	}
}
