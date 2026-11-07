package recovery

import (
	"context"
	"fmt"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/runtime/orbstack"
	"github.com/bojieli/agentreach/internal/service"
)

// Ownership is proof tied to a retained exact object and original operation.
// A matching label, name, PID or current port alone never makes Verified true.
type Ownership struct {
	ContainerID  string         `json:"container_id"`
	Binding      policy.Binding `json:"original_binding"`
	OperationID  string         `json:"original_operation_id"`
	RecordDigest string         `json:"original_resource_digest"`
	Kind         string         `json:"kind"`
	Verified     bool           `json:"ownership_verified"`
	Problem      string         `json:"problem,omitempty"`
}

func historicalBindings(r project.Record) ([]project.Record, journal, error) {
	f, e := journalFile(r)
	if e != nil {
		return nil, journal{}, e
	}
	j, e := readJournal(f, r.Spec.ID)
	if e != nil {
		return nil, j, e
	}
	out := []project.Record{r}
	seen := map[string]bool{project.Digest(r): true}
	for _, entry := range j.Entries {
		for _, record := range []*project.Record{&entry.Before, entry.After} {
			if record != nil && !seen[project.Digest(*record)] {
				out = append(out, *record)
				seen[project.Digest(*record)] = true
			}
		}
	}
	return out, j, nil
}

func observeOwnership(ctx context.Context, r project.Record, id string) (*Ownership, error) {
	if !docker.FullID(id) || id == r.Binding.ContainerID {
		return nil, fmt.Errorf("policy_denied: current workspace is not an orphan")
	}
	bindings, j, e := historicalBindings(r)
	if e != nil {
		return nil, e
	}
	base, e := project.Home()
	if e != nil {
		return nil, e
	}
	for _, old := range bindings {
		if project.Digest(old) == project.Digest(r) {
			continue
		}
		b := policy.Binding{ProjectID: old.Spec.ID, RecordDigest: project.Digest(old), Generation: old.Binding.Generation}
		records, e := service.RecordedAt(project.ControlBase(base, old), b, func() error { return controlPaths(old) })
		if e != nil {
			return nil, e
		}
		for _, record := range records {
			if record.ContainerID != id || record.Spec.Driver != "sidecar" {
				continue
			}
			proof := &Ownership{ContainerID: id, Binding: b, OperationID: record.OperationID, RecordDigest: service.Digest(record), Kind: "retained_sidecar"}
			p, e := recoveryPolicy(old)
			if e != nil {
				return nil, e
			}
			v, e := p.Inspect()
			if e != nil {
				return nil, e
			}
			owned := false
			for _, op := range v.Operations {
				if op.ID == record.OperationID && op.Capability == "service_change" && op.TargetID == record.ID && op.Generation == record.Generation &&
					(op.Outcome == "intent" || op.Outcome == "outcome_unknown" || op.Outcome == "ok") {
					owned = true
					break
				}
			}
			if !owned {
				proof.Problem = "original_intent_unconfirmed"
				return proof, nil
			}
			driver := &service.DockerDriver{Client: &docker.Client{Connection: r.Spec.Docker}, Project: old, Base: project.ControlBase(base, old), Paths: func() error { return controlPaths(old) }}
			if e = driver.RecoveryIdentity(ctx, record); e != nil {
				proof.Problem = reportProblem(e)
				return proof, nil
			}
			proof.Verified = true
			return proof, nil
		}
	}
	for _, entry := range j.Entries {
		if entry.ResourceID != id || entry.Plan.Request.Action != "provision" || entry.Operation.ID == "" || entry.Plan.Request.Provision == nil {
			continue
		}
		op, e := provisionIntent(entry)
		if e != nil {
			return nil, e
		}
		if op.ID == "" {
			return nil, fmt.Errorf("ownership_unconfirmed: provisioning intent")
		}
		b := policy.Binding{ProjectID: entry.Before.Spec.ID, RecordDigest: project.Digest(entry.Before), Generation: entry.Before.Binding.Generation}
		proof := &Ownership{ContainerID: id, Binding: b, OperationID: entry.Operation.ID, RecordDigest: entry.Plan.Digest, Kind: "provisioned_workspace"}
		g := orbstack.New(entry.Before)
		o := g.Observe(ctx, id)
		if o.Problem != "" || o.Candidate == nil || !o.Candidate.ConfigSafe || entry.Plan.Provision == nil || !entry.Plan.Provision.matchesSnapshot(o.Candidate.Snapshot) || o.Candidate.Name != "reach-runtime-"+entry.Operation.ID || o.Candidate.ContainerID != id {
			proof.Problem = "provisioned_identity_unconfirmed"
			return proof, nil
		}
		proof.Verified = true
		return proof, nil
	}
	return &Ownership{ContainerID: id, Kind: "unknown", Problem: "unknown_ownership"}, nil
}

// provisionAdoption permits only the exact full snapshot approved before a
// recorded provisioning operation. It does not normalise an existing binding
// or accept arbitrary differences from a same-labelled replacement.
func provisionAdoption(r project.Record, candidate *orbstack.RuntimeObservation) (*Ownership, error) {
	if candidate == nil {
		return nil, nil
	}
	_, j, e := historicalBindings(r)
	if e != nil {
		return nil, e
	}
	for _, entry := range j.Entries {
		if entry.ResourceID != candidate.ContainerID || entry.Plan.Request.Action != "provision" {
			continue
		}
		b := policy.Binding{ProjectID: entry.Before.Spec.ID, RecordDigest: project.Digest(entry.Before), Generation: entry.Before.Binding.Generation}
		proof := &Ownership{ContainerID: candidate.ContainerID, Binding: b, OperationID: entry.Operation.ID, RecordDigest: entry.Plan.Digest, Kind: "approved_provisioned_workspace"}
		if !candidate.ConfigSafe || entry.Plan.Provision == nil || !entry.Plan.Provision.matchesSnapshot(candidate.Snapshot) || candidate.Name != "reach-runtime-"+entry.Operation.ID {
			proof.Problem = "provisioned_identity_unconfirmed"
			return proof, nil
		}
		op, e := provisionIntent(entry)
		if e != nil {
			return nil, e
		}
		// A create receipt alone does not prove verification of argv and env.
		// Older P5 receipts recorded ok only after verification and start.
		if op.ID == "" || !entry.Verified && op.Outcome != "ok" {
			proof.Problem = "created_configuration_unverified"
			return proof, nil
		}
		proof.Verified = true
		return proof, nil
	}
	return nil, nil
}

func provisionIntent(entry Entry) (policy.Operation, error) {
	if entry.Pending == nil || entry.Operation.ID == "" {
		return policy.Operation{}, nil
	}
	p, e := recoveryPolicy(entry.Before)
	if e != nil {
		return policy.Operation{}, e
	}
	v, e := p.Inspect()
	if e != nil {
		return policy.Operation{}, e
	}
	op, e := journalOperation(entry, v)
	if e != nil || op.Outcome == "intent" || op.Outcome == "outcome_unknown" || op.Outcome == "ok" {
		return op, e
	}
	return policy.Operation{}, nil
}
