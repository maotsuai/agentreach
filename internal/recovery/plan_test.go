package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/orbstack"
	"github.com/bojieli/agentreach/internal/state"
)

func planFixture(t *testing.T) (*Controller, project.Record, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REACH_HOME", base)
	var spec project.Spec
	b, err := os.ReadFile("../../examples/orbstack/project.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = state.Decode(b, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Workspace.Mode, spec.Workspace.HostPath = "guest-owned", ""
	spec.BindMounts = nil
	r := project.Record{Spec: spec, Binding: project.Binding{Schema: 1, Revision: 1, Generation: 1, ContainerID: strings.Repeat("a", 64), ImageID: "sha256:image", StartedAt: "start", DaemonID: "daemon", SnapshotSHA256: strings.Repeat("b", 64), RegisteredAt: time.Unix(1, 0).UTC()}}
	if err = project.Register(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	c := New()
	c.template = func(context.Context, project.Record, string) (*orbstack.SnapshotTemplate, error) { return nil, nil }
	c.clock = func() (policy.Stamp, error) {
		return policy.Stamp{Boot: "boot", Tick: int64(time.Hour), Wall: int64(time.Hour)}, nil
	}
	c.observe = func(context.Context, project.Record, string) orbstack.Inventory {
		return orbstack.Inventory{Storage: "verified", Engine: "verified", DaemonID: "daemon", Bound: &orbstack.RuntimeObservation{ContainerID: r.Binding.ContainerID, State: "running", Matches: true, Approved: true}}
	}
	return c, r, base
}

func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		var data []byte
		if fi.Mode().IsRegular() {
			data, err = os.ReadFile(p)
			if err != nil {
				return err
			}
		}
		out[p] = fmt.Sprintf("%v:%d:%x", fi.Mode(), fi.ModTime().UnixNano(), sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReadOnlyPlanStableAndZeroMutationWithInvalidState(t *testing.T) {
	for _, scenario := range []string{"missing", "valid", "corrupt", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			c, r, base := planFixture(t)
			switch scenario {
			case "missing":
				base = filepath.Join(base, "not-created")
				t.Setenv("REACH_HOME", base)
			case "corrupt", "symlink":
				dir := filepath.Join(base, "security")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(dir, r.Spec.ID+".json")
				if scenario == "corrupt" {
					if err := os.WriteFile(p, []byte("{broken"), 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink("missing-target", p); err != nil {
					t.Fatal(err)
				}
			}
			parent := filepath.Dir(base)
			before := tree(t, parent)
			p, err := c.Plan(context.Background(), r.Spec.ID, Request{Action: "reconcile"})
			if scenario == "missing" {
				if !os.IsNotExist(err) {
					t.Fatalf("missing record: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				again, err := c.Plan(context.Background(), r.Spec.ID, Request{Action: "reconcile"})
				if err != nil || p.Digest != again.Digest {
					t.Fatalf("unstable plan: %v", err)
				}
				if p.Eligible || len(p.Effects) != 0 {
					t.Fatal("diagnosis authorized an effect")
				}
				if scenario != "valid" && len(p.Control.Problems) == 0 {
					t.Fatal("invalid policy ignored")
				}
				b, _ := json.Marshal(p)
				if strings.Contains(string(b), "hmac_key") {
					t.Fatal("private key exposed")
				}
			}
			if after := tree(t, parent); !reflect.DeepEqual(before, after) {
				t.Fatalf("read-only plan changed files\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestPlanBindsPolicyRecordObservationAndTime(t *testing.T) {
	c, r, _ := planFixture(t)
	m, me := projectSession(r).Services(projectSession(r).CheckManaged)
	if me == nil {
		_, me = m.Recorded()
	}
	if me != nil {
		t.Fatalf("service fixture: %v", me)
	}
	p, e := c.Plan(context.Background(), r.Spec.ID, Request{Action: "restart"})
	if e != nil || !p.Eligible {
		t.Fatalf("%+v %v", p, e)
	}
	c.control = func(context.Context, project.Record, orbstack.Inventory, string) Control {
		return Control{PolicyRevision: 9, RuntimePolicy: "approval"}
	}
	changed, e := c.Plan(context.Background(), r.Spec.ID, Request{Action: "restart"})
	if e != nil || changed.Digest == p.Digest {
		t.Fatal("policy revision not covered")
	}
	c.clock = func() (policy.Stamp, error) { return policy.Stamp{Boot: "boot", Tick: p.Deadline, Wall: 1}, nil }
	if _, e = c.plan(context.Background(), r, p.Request, "", p.WindowStart); e == nil {
		t.Fatal("expired window accepted")
	}
	for _, q := range []Request{{Action: "repair"}, {Action: "rebind", ContainerID: "name"}, {Action: "stop", ContainerID: strings.Repeat("a", 64)}} {
		if _, e = c.Plan(context.Background(), r.Spec.ID, q); e == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
}

func TestExhaustedRevisionCannotArchiveWrappedCandidate(t *testing.T) {
	c, r, base := planFixture(t)
	r.Spec.Schema, r.Binding.Schema = 2, 2
	r.Binding.Revision = math.MaxUint64
	if e := state.Write(filepath.Join(base, "projects", r.Spec.ID+".json"), r); e != nil {
		t.Fatal(e)
	}
	c.observe = func(context.Context, project.Record, string) orbstack.Inventory {
		return orbstack.Inventory{Storage: "verified", Engine: "verified", Bound: &orbstack.RuntimeObservation{ContainerID: r.Binding.ContainerID, Matches: true, Approved: true, State: "running"},
			Candidate: &orbstack.RuntimeObservation{ContainerID: strings.Repeat("c", 64), Snapshot: r.Binding.SnapshotSHA256, StartedAt: "next", Approved: true, State: "running"}}
	}
	p, e := c.Plan(context.Background(), r.Spec.ID, Request{Action: "rebind", ContainerID: strings.Repeat("c", 64)})
	if e != nil || p.Eligible || !slices.Contains(p.Differences, "authorization_counter_exhausted") {
		t.Fatalf("counter exhaustion %+v %v", p, e)
	}
	if _, e = c.Request(context.Background(), r.Spec.ID, p); e == nil {
		t.Fatal("archived wrapped revision")
	}
	if _, e = os.Stat(filepath.Join(base, "runtime")); !os.IsNotExist(e) {
		t.Fatal("created recovery state for exhausted counter", e)
	}
}
