package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStrictRecordsAndReadDoesNotCreate(t *testing.T) {
	var v struct {
		Schema int `json:"schema"`
	}
	for _, s := range []string{`{"schema":1,"unsafe":true}`, `{"schema":1,"schema":2}`, `{"schema":1,"Schema":2}`, `{"schema":1} {"schema":2}`} {
		if err := Decode([]byte(s), &v); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	root := filepath.Join(t.TempDir(), "missing")
	if err := Read(filepath.Join(root, "record"), &v); !os.IsNotExist(err) {
		t.Fatalf("missing read: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("read created directory")
	}
}
func TestLockNeverStealsAndAtomicRecordIsComplete(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "lock")
	unlock, err := Lock(context.Background(), lock)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx, lock); err == nil {
		t.Fatal("stole live lock")
	}
	p := filepath.Join(dir, "record")
	if err := Write(p, map[string]int{"schema": 1}); err != nil {
		t.Fatal(err)
	}
	var v map[string]int
	if err := Read(p, &v); err != nil || v["schema"] != 1 {
		t.Fatalf("record %v: %v", v, err)
	}
}
