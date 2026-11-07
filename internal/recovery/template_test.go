package recovery

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/orbstack"
	"github.com/bojieli/agentreach/internal/service"
	"github.com/bojieli/agentreach/internal/state"
)

func TestMissingRuntimeRequiresExactRetainedTemplate(t *testing.T) {
	c, r, base := planFixture(t)
	template := &orbstack.SnapshotTemplate{User: "10001:10001", Dir: r.Spec.Workspace.GuestPath, Labels: map[string]string{"dev.agentreach.managed": "true", "dev.agentreach.project": r.Spec.ID}, Host: map[string]any{"OomKillDisable": nil}}
	r.Binding.SnapshotSHA256 = template.Digest()
	if e := state.Write(filepath.Join(base, "projects", r.Spec.ID+".json"), r); e != nil {
		t.Fatal(e)
	}
	p, e := c.Plan(context.Background(), r.Spec.ID, Request{Action: "migrate"})
	if e != nil {
		t.Fatal(e)
	}
	p.Template = template
	p.Digest = service.Digest(p.Report)
	f, e := journalFile(r)
	if e != nil {
		t.Fatal(e)
	}
	u, e := f.Lock(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer u()
	j := journal{ProjectID: r.Spec.ID, Entries: []Entry{{Plan: p, Before: r, After: nextRecord(r, p), Phase: "planned"}}}
	if e = f.Save(j); e != nil {
		t.Fatal(e)
	}
	recovered, source, e := provisionTemplate(context.Background(), r)
	if e != nil || recovered.Digest() != r.Binding.SnapshotSHA256 || source != "retained_approved_binding_template" {
		t.Fatalf("retained source unavailable %v %v", source, e)
	}
	changed := r
	changed.Binding.SnapshotSHA256 = strings.Repeat("f", 64)
	if _, _, e = provisionTemplate(context.Background(), changed); e == nil {
		t.Fatal("adopted a different template")
	}
}

func TestProvisionCertificateCannotAdoptAnotherConfiguration(t *testing.T) {
	c, r, _ := effectController(t)
	candidate := &orbstack.RuntimeObservation{ContainerID: strings.Repeat("c", 64), Snapshot: strings.Repeat("e", 64), ConfigSafe: true, State: "running"}
	p, e := c.Plan(context.Background(), r.Spec.ID, Request{Action: "migrate"})
	if e != nil {
		t.Fatal(e)
	}
	p.Request = Request{Action: "provision", Provision: &ProvisionSpec{Schema: 1}}
	p.Provision = &ProvisionObservation{ExpectedSnapshot: candidate.Snapshot}
	p.Digest = service.Digest(p.Report)
	store, _ := c.policy(r)
	pending, e := store.PlanRuntime(context.Background(), policy.HostRequest{Capability: "runtime_change", TargetID: r.Spec.ID, Generation: 1, Params: policy.HostParams{Operation: "provision", SpecDigest: p.Digest}}, p.Control.PolicyRevision)
	if e != nil {
		t.Fatal(e)
	}
	approval, e := store.Approve(context.Background(), pending.ID, pending.Digest, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	op, _, e := store.BeginHost(context.Background(), pending.ID, approval.ID)
	if e != nil {
		t.Fatal(e)
	}
	candidate.Name = "reach-runtime-" + op.ID
	f, e := journalFile(r)
	if e != nil {
		t.Fatal(e)
	}
	u, e := f.Lock(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer u()
	entry := Entry{Plan: p, Before: r, Pending: &pending, Operation: op, ResourceID: candidate.ContainerID, Phase: "created"}
	if e = f.Save(journal{ProjectID: r.Spec.ID, Entries: []Entry{entry}}); e != nil {
		t.Fatal(e)
	}
	proof, e := provisionAdoption(r, candidate)
	if e != nil || proof == nil || proof.Verified {
		t.Fatalf("adopted an unverified create receipt %+v %v", proof, e)
	}
	candidate.Approved = true
	c.observe = func(context.Context, project.Record, string) orbstack.Inventory {
		observed := *candidate
		return orbstack.Inventory{Storage: "verified", Engine: "verified", Candidate: &observed}
	}
	rebind, e := c.Plan(context.Background(), r.Spec.ID, Request{Action: "rebind", ContainerID: candidate.ContainerID})
	if e != nil || rebind.Eligible || rebind.Observed.Candidate.Approved {
		t.Fatalf("equal P0 snapshot bypassed the provision certificate: %+v %v", rebind, e)
	}
	entry.Verified = true
	if e = f.Save(journal{ProjectID: r.Spec.ID, Entries: []Entry{entry}}); e != nil {
		t.Fatal(e)
	}
	proof, e = provisionAdoption(r, candidate)
	if e != nil || proof == nil || !proof.Verified {
		t.Fatalf("original certificate rejected %+v %v", proof, e)
	}
	bad := *candidate
	bad.Snapshot = strings.Repeat("f", 64)
	if proof, e = provisionAdoption(r, &bad); e != nil || proof == nil || proof.Verified {
		t.Fatal("adopted unapproved snapshot")
	}
	bad = *candidate
	bad.Name = "same-label-replacement"
	if proof, e = provisionAdoption(r, &bad); e != nil || proof == nil || proof.Verified {
		t.Fatal("adopted replacement name")
	}
	entry.Operation.ID = strings.Repeat("f", 32)
	candidate.Name = "reach-runtime-" + entry.Operation.ID
	if e = f.Save(journal{ProjectID: r.Spec.ID, Entries: []Entry{entry}}); e != nil {
		t.Fatal(e)
	}
	if proof, e = provisionAdoption(r, candidate); e == nil || proof != nil {
		t.Fatal("adopted missing original P2 intent")
	}
}

func TestProvisionSnapshotsPinOnlyObservedOOMDefaults(t *testing.T) {
	source := &orbstack.SnapshotTemplate{Host: map[string]any{"OomKillDisable": nil, "Memory": float64(512 << 20)}}
	created, err := source.ForCreate()
	if err != nil {
		t.Fatal(err)
	}
	running, err := source.ForRunning()
	if err != nil || running != source.Digest() || created == running {
		t.Fatalf("invalid transition: create=%s run=%s err=%v", created, running, err)
	}
	changed := orbstack.SnapshotTemplate{Host: map[string]any{"OomKillDisable": nil, "Memory": float64(256 << 20)}}
	for _, o := range []ProvisionObservation{
		{Template: source, ExpectedSnapshot: created},
		{Template: source, ExpectedSnapshot: created, ExpectedRunning: running},
	} {
		if !o.matchesSnapshot(created) || !o.matchesSnapshot(running) || o.matchesSnapshot(changed.Digest()) || o.matchesSnapshot("") {
			t.Fatalf("unexpected exact snapshot comparison: %+v", o)
		}
	}
}
