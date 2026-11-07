package orbstack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bojieli/agentreach/internal/project"
)

func TestRecoveryObservationNeverDispatchesOrRepairs(t *testing.T) {
	for _, scenario := range []string{"running", "stopped", "restart", "daemon", "storage"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			switch scenario {
			case "stopped":
				f.c.State.Running, f.c.State.Status = false, "exited"
			case "restart":
				f.c.State.StartedAt = "different-start"
			case "daemon":
				f.daemon = "different-daemon"
			case "storage":
				f.g.CheckVolume = func(context.Context, project.VolumeClaim) error { return os.ErrNotExist }
			}
			o := f.g.Observe(context.Background(), f.c.ID)
			for _, args := range f.calls {
				for _, bad := range []string{"start", "stop", "restart", "create", "run", "pull", "build", "exec", "rm", "mkdir"} {
					if slices.Contains(args, bad) {
						t.Fatalf("observation dispatched %v", args)
					}
				}
			}
			switch scenario {
			case "running", "stopped":
				if o.Bound == nil || !o.Bound.Matches || !o.Bound.Approved || o.Problem != "" {
					t.Fatalf("configuration/identity conflated with running state: %+v", o)
				}
			case "restart":
				if o.Bound == nil || o.Bound.Matches || !o.Bound.Approved {
					t.Fatalf("adopted restart or rejected unchanged config: %+v", o.Bound)
				}
			case "daemon":
				if o.Engine == "verified" || o.Bound != nil {
					t.Fatalf("read a changed daemon: %+v", o)
				}
			case "storage":
				if len(f.calls) != 0 || o.Storage == "verified" {
					t.Fatal("Docker called after storage failure")
				}
			}
		})
	}
}

func TestTemplatePreservesLegacyRawSnapshot(t *testing.T) {
	f := setup(t)
	// This is the legacy wire shape used before exporting SnapshotTemplate.
	var host any
	if e := json.Unmarshal(f.c.HostConfig, &host); e != nil {
		t.Fatal(e)
	}
	mounts := slices.Clone(f.c.Mounts)
	slices.SortFunc(mounts, func(a, b docker.Mount) int { return strings.Compare(a.Destination, b.Destination) })
	raw, _ := json.Marshal(struct {
		User, Dir string
		Labels    map[string]string
		Mounts    []docker.Mount
		Host      any
	}{f.c.Config.User, f.c.Config.WorkingDir, f.c.Config.Labels, mounts, host})
	h := sha256.Sum256(raw)
	if got := template(f.c).Digest(); got != hex.EncodeToString(h[:]) {
		t.Fatal("changed legacy snapshot", got)
	}
	before := snapshot(f.c)
	created, e := template(f.c).ForCreate()
	if e != nil || created == "" || snapshot(f.c) != before {
		t.Fatalf("rewrote existing snapshot %s %v", created, e)
	}
	var source map[string]any
	_ = json.Unmarshal(f.c.HostConfig, &source)
	if _, ok := source["OomKillDisable"]; ok {
		t.Fatal("mutated source configuration")
	}
}
