package project

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"

	"github.com/bojieli/agentreach/internal/state"
)

func migration(r Record) Record {
	r.Spec.Schema, r.Binding.Schema = SchemaV2, SchemaV2
	r.Binding.Revision++
	return r
}

func TestCASAcrossProcesses(t *testing.T) {
	if os.Getenv("REACH_CAS_TEST") == "child" {
		id, digest, revision := os.Getenv("REACH_CAS_ID"), os.Getenv("REACH_CAS_DIGEST"), os.Getenv("REACH_CAS_REVISION")
		rev, e := strconv.ParseUint(revision, 10, 64)
		if e == nil {
			e = WithCAS(context.Background(), id, digest, rev, func(l *Locked) error { return l.Publish(migration(l.Current())) })
		}
		if e != nil {
			os.Exit(9)
		}
		os.Exit(0)
	}
	t.Setenv("REACH_HOME", canonicalTempDir(t))
	r := exampleRecord(t)
	if e := Register(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	bin, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(bin, "-test.run=^TestCASAcrossProcesses$")
			cmd.Env = append(os.Environ(), "REACH_CAS_TEST=child", "REACH_CAS_ID="+r.Spec.ID, "REACH_CAS_DIGEST="+Digest(r), "REACH_CAS_REVISION=1")
			results <- cmd.Run()
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for e := range results {
		if e == nil {
			ok++
		}
	}
	got, e := Load(r.Spec.ID)
	if e != nil || ok != 1 || Digest(got) != Digest(migration(r)) {
		t.Fatalf("successes=%d record=%+v err=%v", ok, got, e)
	}
	if e = WithCAS(context.Background(), r.Spec.ID, Digest(r), 1, func(*Locked) error { t.Fatal("stale callback invoked"); return nil }); e == nil {
		t.Fatal("stale CAS accepted")
	}
}

func TestAtomicPublicationFailureHasOneAuthority(t *testing.T) {
	for _, stage := range []string{"before", "after"} {
		t.Run(stage, func(t *testing.T) {
			t.Setenv("REACH_HOME", canonicalTempDir(t))
			r := exampleRecord(t)
			if e := Register(context.Background(), r); e != nil {
				t.Fatal(e)
			}
			want := r
			e := WithCAS(context.Background(), r.Spec.ID, Digest(r), 1, func(l *Locked) error {
				l.write = func(p string, v any) error {
					if stage == "after" {
						if e := state.Write(p, v); e != nil {
							return e
						}
					}
					return errors.New("injected publication boundary")
				}
				return l.Publish(migration(r))
			})
			if e == nil {
				t.Fatal("injection did not fail")
			}
			if stage == "after" {
				want = migration(r)
			}
			got, e := Load(r.Spec.ID)
			if e != nil || Digest(got) != Digest(want) {
				t.Fatalf("ambiguous authority: %+v %v", got, e)
			}
		})
	}
}

func TestCASCannotChangeConfigurationOrDowngrade(t *testing.T) {
	t.Setenv("REACH_HOME", canonicalTempDir(t))
	r := exampleRecord(t)
	if e := Register(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	if e := WithCAS(context.Background(), r.Spec.ID, Digest(r), 1, func(l *Locked) error {
		bad := migration(r)
		bad.Spec.EngineDataPath = "/unexpected"
		if e := l.Publish(bad); e == nil {
			t.Fatal("accepted changed configuration")
		}
		return l.Publish(migration(r))
	}); e != nil {
		t.Fatal(e)
	}
	r, _ = Load(r.Spec.ID)
	if e := WithCAS(context.Background(), r.Spec.ID, Digest(r), 2, func(l *Locked) error {
		old := r
		old.Spec.Schema, old.Binding.Schema = 1, 1
		old.Binding.Revision = 1
		return l.Publish(old)
	}); e == nil {
		t.Fatal("accepted downgrade")
	}
}
