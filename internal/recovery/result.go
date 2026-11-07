package recovery

import (
	"context"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
)

// Result reports a new plan; it never applies a new binding implicitly.
type Result struct {
	policy.Operation
	CreatedID      string `json:"created_container_id,omitempty"`
	RebindRequired bool   `json:"rebind_required,omitempty"`
	RebindPlan     *Plan  `json:"rebind_plan,omitempty"`
}

// Result returns a new read-only binding plan after a successful lifecycle action.
func (c *Controller) Result(ctx context.Context, id string, op policy.Operation) (Result, error) {
	result := Result{Operation: op}
	r, err := project.Load(id)
	if err != nil {
		return result, err
	}
	f, err := journalFile(r)
	if err != nil {
		return result, err
	}
	j, err := readJournal(f, id)
	if err != nil {
		return result, err
	}
	for _, entry := range j.Entries {
		if entry.Operation.ID != op.ID {
			continue
		}
		result.CreatedID = entry.ResourceID
		switch entry.Plan.Request.Action {
		case "start", "restart", "provision":
			result.RebindRequired = true
			target := entry.ResourceID
			if target == "" {
				target = entry.Before.Binding.ContainerID
			}
			p, e := c.Plan(ctx, id, Request{Action: "rebind", ContainerID: target})
			if e != nil {
				return result, e
			}
			result.RebindPlan = &p
		}
		break
	}
	return result, nil
}
