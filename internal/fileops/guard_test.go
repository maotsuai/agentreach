package fileops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExistingPipeAndPoolCannotBypassGuard(t *testing.T) {
	tr, base := localPipeTransport(t)
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "canary")
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops, err := NewPipe(ctx, tr, base)
	if err != nil {
		t.Fatal(err)
	}
	pool := NewPool(ops, 1, func(context.Context) (FileOps, error) { t.Fatal("unexpected reopen"); return nil, nil })
	ready := true
	checks := 0
	g := Guard(pool, func(context.Context) error {
		checks++
		if !ready {
			return errors.New("storage_unavailable")
		}
		return nil
	})
	t.Cleanup(func() { _ = g.Close() })
	if got, err := g.Read(ctx, file, 0, 0); err != nil || string(got) != "before" {
		t.Fatalf("first read %q: %v", got, err)
	}
	if err := g.Write(ctx, file, []byte("approved"), 0o600); err != nil {
		t.Fatal(err)
	}
	ready = false
	if _, err := g.Read(ctx, file, 0, 0); err == nil {
		t.Fatal("second read bypassed guard")
	}
	if err := g.Write(ctx, file, []byte("unsafe"), 0o600); err == nil {
		t.Fatal("write bypassed guard")
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "approved" {
		t.Fatalf("rejected write reached target: %q %v", got, err)
	}
	if checks != 4 || tr.Opens() != 1 {
		t.Fatalf("checks=%d opens=%d", checks, tr.Opens())
	}
}
