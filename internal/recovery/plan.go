// Package recovery builds reviewable, boot-bound plans for explicit project
// recovery. Observing state never grants authority or repairs a resource.
package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/ports"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/runtime/orbstack"
	"github.com/bojieli/agentreach/internal/service"
)

const planWindow = 10 * time.Minute

// Request names a closed action and, when required, an exact Docker object.
type Request struct {
	Action       string         `json:"action"`
	ContainerID  string         `json:"container_id,omitempty"`
	RecordDigest string         `json:"retired_record_digest,omitempty"`
	Provision    *ProvisionSpec `json:"provision,omitempty"`
}

// Object compares a retained control identity with the observation available
// without dispatching into the workspace or doing an application health probe.
type Object struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	OperationID string `json:"operation_id,omitempty"`
	ContainerID string `json:"container_id,omitempty"`
	Desired     string `json:"desired"`
	Observed    string `json:"observed"`
	Difference  string `json:"difference,omitempty"`
}

// Control is a bounded, redacted projection of the project's retained state.
type Control struct {
	PolicyRevision uint64             `json:"policy_revision"`
	RuntimePolicy  string             `json:"runtime_change_policy"`
	Unresolved     []policy.Operation `json:"unresolved_operations,omitempty"`
	Objects        []Object           `json:"objects,omitempty"`
	Problems       []string           `json:"problems,omitempty"`
}

// Report is the complete material covered by the plan digest.
type Report struct {
	Schema         int                        `json:"schema"`
	ProjectID      string                     `json:"project_id"`
	Request        Request                    `json:"request"`
	ExpectedDigest string                     `json:"expected_record_digest"`
	Revision       uint64                     `json:"expected_revision"`
	Desired        project.Binding            `json:"desired"`
	VolumeClaims   []project.VolumeClaim      `json:"volume_claims"`
	Observed       orbstack.Inventory         `json:"observed"`
	Control        Control                    `json:"control"`
	Differences    []string                   `json:"differences"`
	Risks          []string                   `json:"risks"`
	Recommendation string                     `json:"recommendation"`
	Effects        []string                   `json:"effects"`
	Eligible       bool                       `json:"eligible"`
	Boot           string                     `json:"boot"`
	WindowStart    int64                      `json:"window_start_tick"`
	Journal        []JournalObservation       `json:"runtime_journal,omitempty"`
	Template       *orbstack.SnapshotTemplate `json:"approved_runtime_template,omitempty"`
	Deadline       int64                      `json:"deadline_tick"`
	Manager        *ports.RetiredManager      `json:"retired_manager,omitempty"`
	Adoption       *Ownership                 `json:"approved_provision_adoption,omitempty"`
	Ownership      *Ownership                 `json:"ownership,omitempty"`
	Provision      *ProvisionObservation      `json:"provision,omitempty"`
}

// Plan is a reviewable report with its complete canonical digest.
type Plan struct {
	Report
	Digest string `json:"plan_digest"`
}

// Controller owns read-only observation seams. The command uses only native
// observers; unit tests can count calls and inject unavailable dependencies.
type Controller struct {
	observe        func(context.Context, project.Record, string) orbstack.Inventory
	control        func(context.Context, project.Record, orbstack.Inventory, string) Control
	clock          policy.Clock
	policy         func(project.Record) (*policy.Store, error)
	fault          func(string) error
	change         func(context.Context, project.Record, string, string) error
	provisionCheck func(context.Context, project.Record, ProvisionSpec) ProvisionObservation
	template       func(context.Context, project.Record, string) (*orbstack.SnapshotTemplate, error)
	owner          func(context.Context, project.Record, string) (*Ownership, error)
}

// New installs native read-only observers and the closed production driver.
func New() *Controller {
	return &Controller{observe: func(ctx context.Context, r project.Record, candidate string) orbstack.Inventory {
		return orbstack.New(r).Observe(ctx, candidate)
	}, control: observeControl, clock: policy.NativeClock, policy: recoveryPolicy, provisionCheck: observeProvision, owner: observeOwnership, template: func(ctx context.Context, r project.Record, id string) (*orbstack.SnapshotTemplate, error) {
		return orbstack.New(r).CaptureTemplate(ctx, id)
	},
		change: func(ctx context.Context, r project.Record, id, action string) error {
			_, err := (&docker.Client{Connection: r.Spec.Docker}).ChangeService(ctx, id, action)
			return err
		}}
}

func validateRequest(q Request) error {
	if q.Action == "manager-stop" {
		if len(q.RecordDigest) == 64 && q.ContainerID == "" && q.Provision == nil {
			return nil
		}
		return docker.Fail("policy_denied", "manager-stop requires a retired record digest")
	}
	if q.RecordDigest != "" {
		return docker.Fail("policy_denied", "unexpected retired record digest")
	}
	if q.Action == "provision" {
		if q.Provision != nil && q.ContainerID == "" {
			return nil
		}
		return docker.Fail("policy_denied", "provision requires a closed specification")
	}
	if q.Provision != nil {
		return docker.Fail("policy_denied", "unexpected provision specification")
	}
	switch q.Action {
	case "reconcile", "migrate", "start", "stop", "restart":
		if q.ContainerID == "" {
			return nil
		}
	case "rebind", "orphan-stop":
		if docker.FullID(q.ContainerID) {
			return nil
		}
	}
	return docker.Fail("policy_denied", "invalid recovery action or exact container ID")
}

// Plan reads existing project/control state without creating or repairing it.
func (c *Controller) Plan(ctx context.Context, id string, q Request) (Plan, error) {
	if err := validateRequest(q); err != nil {
		return Plan{}, err
	}
	r, err := project.Load(id)
	if err != nil {
		return Plan{}, err
	}
	return c.plan(ctx, r, q, "", 0)
}

func (c *Controller) plan(ctx context.Context, r project.Record, q Request, ignore string, window int64) (Plan, error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	o := c.observe(ctx, r, q.ContainerID)
	v := c.control(ctx, r, o, ignore)
	now, err := c.clock()
	if err != nil || now.Boot == "" || now.Tick < 0 || now.Tick > int64(^uint64(0)>>1)-int64(planWindow) {
		return Plan{}, fmt.Errorf("clock_unavailable: recovery plan requires a native monotonic clock")
	}
	start := now.Tick - now.Tick%int64(planWindow)
	if window != 0 && window != start {
		return Plan{}, fmt.Errorf("plan_stale: deadline expired")
	}
	p := Report{
		Schema: 1, ProjectID: r.Spec.ID, Request: q, ExpectedDigest: project.Digest(r), Revision: r.Binding.Revision,
		Desired: r.Binding, VolumeClaims: r.Spec.Volumes, Observed: o, Control: v,
		Differences: []string{}, Risks: []string{}, Effects: []string{}, Boot: now.Boot, WindowStart: start, Deadline: start + int64(planWindow),
	}
	p.Journal, err = c.observeJournal(r, ignore)
	if err != nil {
		p.Control.Problems = append(p.Control.Problems, "runtime_journal:"+reportProblem(err))
	}
	if q.Provision != nil {
		provision := c.provisionCheck(ctx, r, *q.Provision)
		p.Provision = &provision
	}
	if q.Action == "orphan-stop" {
		p.Ownership, err = c.owner(ctx, r, q.ContainerID)
		if err != nil {
			p.Control.Problems = append(p.Control.Problems, "ownership:"+reportProblem(err))
		}
	}
	if q.Action == "manager-stop" {
		p.Manager, err = managerObservation(ctx, r, q.RecordDigest)
		if err != nil {
			p.Control.Problems = append(p.Control.Problems, "retired_manager:"+reportProblem(err))
		}
	}
	if q.Action == "rebind" && o.Candidate != nil {
		p.Adoption, err = provisionAdoption(r, o.Candidate)
		if err != nil {
			p.Control.Problems = append(p.Control.Problems, "provision_adoption:"+reportProblem(err))
		}
		if p.Adoption != nil {
			p.Observed.Candidate.Approved = p.Adoption.Verified
		}
	}
	if q.Action == "migrate" || q.Action == "rebind" {
		target := r.Binding.ContainerID
		if q.Action == "rebind" {
			target = q.ContainerID
		}
		p.Template, err = c.template(ctx, r, target)
		if err != nil {
			p.Risks = append(p.Risks, "an approved provisioning template is unavailable; this binding can only be recovered using a separately reviewed source")
		}
	}
	p.describe()
	out := Plan{Report: p, Digest: service.Digest(p)}
	encoded, err := json.Marshal(out)
	if err != nil {
		return Plan{}, err
	}
	if len(encoded) > policy.MaxRequest {
		return Plan{}, fmt.Errorf("request_budget: recovery plan exceeds 64 KiB")
	}
	return out, nil
}

func (p *Report) describe() {
	o := p.Observed
	p.Recommendation = "no runtime mutation"
	switch {
	case o.Problem != "":
		p.Differences = append(p.Differences, o.Problem)
		p.Recommendation = "verify approved storage, connection and daemon before planning an effect"
	case o.Bound == nil || o.Bound.Problem != "":
		p.Differences = append(p.Differences, "bound_runtime_unconfirmed")
		p.Recommendation = "inspect the exact runtime or explicitly plan a verified replacement"
	case !o.Bound.Approved:
		p.Differences = append(p.Differences, "runtime_configuration_changed")
		p.Recommendation = "review the runtime configuration against its approved snapshot"
	case !o.Bound.Matches:
		p.Differences = append(p.Differences, "execution_identity_changed")
		p.Recommendation = "plan an explicit rebind to a verified full container ID"
	case o.Bound.State != "running":
		p.Differences = append(p.Differences, "runtime_not_running")
		p.Recommendation = "plan start for the exact registered container"
	}
	if len(p.Control.Unresolved) != 0 {
		p.Differences = append(p.Differences, "unresolved_operations")
		p.Risks = append(p.Risks, "an unresolved operation may already have taken effect; do not replay it")
	}
	if len(p.Control.Problems) != 0 {
		p.Differences = append(p.Differences, "control_observation_unavailable")
	}
	for _, x := range p.Control.Objects {
		if x.Difference != "" && !slices.Contains(p.Differences, x.Difference) {
			p.Differences = append(p.Differences, x.Difference)
		}
	}
	verified := o.Engine == "verified" && o.Problem == "" && len(p.Control.Problems) == 0
	exact := o.Bound != nil && o.Bound.Matches && o.Bound.Problem == ""
	switch p.Request.Action {
	case "migrate":
		p.Eligible = verified && p.Desired.Schema == 1
		p.Effects = []string{"record_intent", "retain_original_record", "publish_schema_v2", "record_result"}
		p.Risks = append(p.Risks, "new authorization revision; old handles and approvals become stale")
	case "rebind":
		p.Eligible = verified && o.Candidate != nil && o.Candidate.Approved && o.Candidate.Problem == "" && o.Candidate.State == "running" && !o.Candidate.Matches
		p.Effects = []string{"record_intent", "publish_new_binding", "invalidate_previous_binding", "record_result"}
		p.Risks = append(p.Risks, "new sessions and approvals are required; existing processes are not migrated")
	case "start", "stop", "restart":
		p.Eligible = verified && exact && (p.Request.Action == "start" && (o.Bound.State == "created" || o.Bound.State == "exited") || p.Request.Action != "start" && o.Bound.State == "running")
		p.Effects = []string{"record_intent", "docker_" + p.Request.Action + "_exact_id", "record_result"}
		if p.Request.Action != "start" {
			p.Risks = append(p.Risks, "running workspace processes and terminal memory will be lost")
		}
		if p.Request.Action != "stop" {
			p.Risks = append(p.Risks, "new StartedAt requires a separately approved rebind")
		}
	case "orphan-stop":
		p.Eligible = verified && p.Ownership != nil && p.Ownership.Verified && o.Candidate != nil && o.Candidate.State == "running"
		p.Effects = []string{"record_intent", "stop_exact_owned_orphan", "record_result"}
		p.Risks = append(p.Risks, "unknown ownership cannot authorize cleanup; volumes are retained")
	case "manager-stop":
		p.Eligible = verified && p.Manager != nil && p.Manager.Reachable
		if p.Manager != nil {
			for _, x := range p.Manager.Exposures {
				if x.State == "open" || x.State == "creating" {
					p.Eligible = false
				}
			}
		}
		p.Effects = []string{"record_intent", "shutdown_original_private_manager", "record_result"}
		p.Risks = append(p.Risks, "only the original nonce-bound manager may exit; active listeners must already be retired")
	case "provision":
		inactive := o.Bound != nil && (o.Bound.Problem == "identity_changed" || o.Bound.Matches && (o.Bound.State == "exited" || o.Bound.State == "created"))
		p.Eligible = verified && inactive && p.Provision != nil && p.Provision.Ready
		p.Effects = []string{"record_intent", "create_one_workspace_from_local_image", "verify_created_exact_id", "record_created_identity", "start_created_exact_id", "record_result"}
		p.Risks = append(p.Risks, "approved existing network and data volumes are reused and preserved; application data must already be quiescent", "new binding, policy and sessions require separate explicit actions")
	}
	switch p.Request.Action {
	case "migrate", "rebind", "start", "restart", "provision":
		if p.Desired.Revision == math.MaxUint64 || p.Request.Action != "migrate" && p.Desired.Generation == math.MaxUint64 {
			p.Eligible = false
			p.Differences = append(p.Differences, "authorization_counter_exhausted")
		}
	}

}

func reportProblem(err error) string {
	var d *docker.Error
	if errors.As(err, &d) {
		return d.Kind
	}
	return "state_unavailable"
}
