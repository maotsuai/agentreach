package recovery

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/privatecontrol"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/service"
	"github.com/bojieli/agentreach/internal/terminal"
)

const maxEntries = 64

// Entry retains the original plan, binding backup and operation identity.
// Projects/<id>.json alone decides the current binding, including after a crash.
type Entry struct {
	Plan       Plan             `json:"plan"`
	Before     project.Record   `json:"before"`
	After      *project.Record  `json:"after,omitempty"`
	Pending    *policy.Pending  `json:"pending,omitempty"`
	Operation  policy.Operation `json:"operation"`
	Phase      string           `json:"phase"`
	ResourceID string           `json:"created_container_id,omitempty"`
	Verified   bool             `json:"created_configuration_verified,omitempty"`
}

type journal struct {
	ProjectID string  `json:"project_id"`
	Entries   []Entry `json:"entries"`
}

func journalFile(r project.Record) (privatecontrol.File, error) {
	base, err := project.Home()
	if err != nil {
		return privatecontrol.File{}, err
	}
	return privatecontrol.File{Root: base, Path: filepath.Join(base, "runtime", r.Spec.ID+".json"), Max: 16 << 20, Check: func() error { return controlPaths(r) }}, nil
}

func readJournal(f privatecontrol.File, id string) (journal, error) {
	var j journal
	err := f.Read(&j)
	if os.IsNotExist(err) {
		return journal{ProjectID: id, Entries: []Entry{}}, nil
	}
	if err != nil {
		return j, err
	}
	if j.ProjectID != id || len(j.Entries) > maxEntries {
		return j, fmt.Errorf("state_corrupt: runtime journal scope/budget")
	}
	seen := map[string]bool{}
	for _, e := range j.Entries {
		if e.ResourceID != "" && !docker.FullID(e.ResourceID) {
			return j, fmt.Errorf("state_corrupt: provisioned exact identity")
		}
		if e.Verified && (e.ResourceID == "" || e.Operation.ID == "" || e.Plan.Request.Action != "provision") {
			return j, fmt.Errorf("state_corrupt: verified provision lacks an exact identity")
		}
		if e.Before.Spec.ID != id || e.Before.Validate() != nil || e.Plan.ProjectID != id ||
			e.Plan.Digest != service.Digest(e.Plan.Report) || e.Plan.ExpectedDigest != project.Digest(e.Before) ||
			e.Plan.Revision != e.Before.Binding.Revision || seen[e.Plan.Digest] || e.Operation.ID != "" && !terminal.ValidID(e.Operation.ID) {
			return j, fmt.Errorf("state_corrupt: runtime journal identity")
		}
		if e.After != nil && (e.After.Validate() != nil || e.After.Spec.ID != id || e.After.Binding.Revision != e.Before.Binding.Revision+1) {
			return j, fmt.Errorf("state_corrupt: runtime journal candidate")
		}
		if e.Pending != nil && (e.Pending.Request.Binding.RecordDigest != project.Digest(e.Before) || e.Pending.Request.Host.Params.SpecDigest != e.Plan.Digest) {
			return j, fmt.Errorf("state_corrupt: runtime journal request")
		}
		switch e.Phase {
		case "planned", "intent", "created", "dispatched", "published", "complete", "outcome_unknown":
		default:
			return j, fmt.Errorf("state_corrupt: runtime journal phase")
		}
		seen[e.Plan.Digest] = true
	}
	return j, nil
}

// JournalObservation relates an original operation to the current authority.
type JournalObservation struct {
	PlanDigest  string `json:"plan_digest"`
	OperationID string `json:"operation_id,omitempty"`
	Action      string `json:"action"`
	Phase       string `json:"phase"`
	Authority   string `json:"binding_authority"`
	Outcome     string `json:"outcome"`
}

func journalOperation(e Entry, view policy.View) (policy.Operation, error) {
	if e.Pending != nil {
		for _, op := range view.Operations {
			if op.RequestID == e.Pending.ID {
				return matchJournalOperation(e, op)
			}
		}
	}
	return matchJournalOperation(e, policy.Operation{})
}

func matchJournalOperation(e Entry, op policy.Operation) (policy.Operation, error) {
	if op.ID != "" && e.Pending != nil && op.RequestID == e.Pending.ID && op.Digest == e.Pending.Digest &&
		op.Capability == "runtime_change" && op.TargetID == e.Before.Spec.ID && op.Generation == e.Before.Binding.Generation &&
		op.Revision == e.Pending.Request.PolicyRevision && (e.Operation.ID == "" || e.Operation.ID == op.ID) {
		return op, nil
	}
	if e.Operation.ID != "" {
		return policy.Operation{}, fmt.Errorf("state_corrupt: runtime receipt has no matching original intent")
	}
	return policy.Operation{}, nil
}

func unconfirmedReceipt(e Entry) policy.Operation {
	op, err := matchJournalOperation(e, e.Operation)
	if err != nil || op.ID == "" {
		return policy.Operation{}
	}
	op.Outcome = "outcome_unknown"
	return op
}

func (c *Controller) observeJournal(r project.Record, ignore string) ([]JournalObservation, error) {
	f, err := journalFile(r)
	if err != nil {
		return nil, err
	}
	j, err := readJournal(f, r.Spec.ID)
	if err != nil {
		return nil, err
	}
	var out []JournalObservation
	var problem error
	operations := map[string]map[string]policy.Operation{}
	failures := map[string]error{}
	current := project.Digest(r)
	for _, e := range j.Entries {
		if e.Pending == nil || e.Operation.ID != "" && e.Operation.ID == ignore {
			continue
		}
		before := project.Digest(e.Before)
		byRequest, ok := operations[before]
		if !ok {
			byRequest = map[string]policy.Operation{}
			failures[before] = c.loadJournalOperations(e.Before, byRequest)
			operations[before] = byRequest
		}
		op, err := matchJournalOperation(e, byRequest[e.Pending.ID])
		if failures[before] != nil {
			err = failures[before]
		}
		if err != nil {
			if problem == nil {
				problem = err
			}
			op = unconfirmedReceipt(e)
		}
		if op.ID == "" || op.ID == ignore {
			continue
		}
		authority := "another_authorization"
		if current == before {
			authority = "original_record"
		} else if e.After != nil && current == project.Digest(*e.After) {
			authority = "candidate_record"
		}
		out = append(out, JournalObservation{e.Plan.Digest, op.ID, e.Plan.Request.Action, e.Phase, authority, op.Outcome})
	}
	return out, problem
}

func (c *Controller) loadJournalOperations(r project.Record, byRequest map[string]policy.Operation) error {
	store, err := c.policy(r)
	if err != nil {
		return err
	}
	view, err := store.Inspect()
	if err != nil {
		return err
	}
	for _, op := range view.Operations {
		if op.Capability == "runtime_change" {
			byRequest[op.RequestID] = op
		}
	}
	return nil
}
