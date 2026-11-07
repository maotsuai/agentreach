package orbstack

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/bojieli/agentreach/internal/runtime/docker"
)

// RuntimeObservation separates execution identity from the running state, so
// an exact stopped container can be inspected without weakening Check.
type RuntimeObservation struct {
	ContainerID string                   `json:"container_id"`
	Name        string                   `json:"name,omitempty"`
	ImageID     string                   `json:"image_id"`
	StartedAt   string                   `json:"started_at"`
	State       string                   `json:"state"`
	Snapshot    string                   `json:"snapshot_sha256"`
	Volumes     map[string]docker.Volume `json:"named_volumes,omitempty"`
	Matches     bool                     `json:"matches_registered_identity"`
	Approved    bool                     `json:"matches_approved_config"`
	ConfigSafe  bool                     `json:"configuration_safe,omitempty"`
	Problem     string                   `json:"problem,omitempty"`
	ServiceID   string                   `json:"service_id,omitempty"`
	OperationID string                   `json:"operation_id,omitempty"`
}

// Inventory identifies the approved storage/engine and the objects seen on it.
type Inventory struct {
	Storage        string               `json:"storage"`
	EngineDataPath string               `json:"engine_data_path"`
	Engine         string               `json:"engine"`
	DaemonID       string               `json:"daemon_id,omitempty"`
	Bound          *RuntimeObservation  `json:"bound,omitempty"`
	Candidate      *RuntimeObservation  `json:"candidate,omitempty"`
	Containers     []RuntimeObservation `json:"containers,omitempty"`
	Problem        string               `json:"problem,omitempty"`
}

// CheckRecoveryBase validates only the explicitly approved control/storage and
// daemon identity. It grants no Guest capability and never adopts a container.
// Only the operator recovery controller may use this while a workspace is down.
func (g *Guard) CheckRecoveryBase(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := g.storage(ctx); err != nil {
		return err
	}
	if err := g.Docker.CheckConnection(ctx); err != nil {
		return err
	}
	id, err := g.Docker.DaemonID(ctx)
	if err != nil {
		return err
	}
	if id != g.Record.Binding.DaemonID {
		return docker.Fail("identity_changed", "recovery refuses a different daemon")
	}
	return nil
}

func errorKind(err error, fallback string) string {
	var e *docker.Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return fallback
}

func containerObservation(c docker.Container) RuntimeObservation {
	return RuntimeObservation{
		ContainerID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), ImageID: c.Image,
		StartedAt: c.State.StartedAt, State: c.State.Status, Snapshot: snapshot(c),
		ServiceID: c.Config.Labels["dev.agentreach.service"], OperationID: c.Config.Labels["dev.agentreach.operation"],
	}
}

// Observe is a recovery-only read path. A successful observation of another
// instance never authorises use of it for normal Guest operations.
func (g *Guard) Observe(ctx context.Context, candidate string) Inventory {
	b, s := g.Record.Binding, g.Record.Spec
	o := Inventory{Storage: "unavailable", Engine: "not_checked", EngineDataPath: s.EngineDataPath}
	if e := g.storage(ctx); e != nil {
		o.Problem = errorKind(e, "storage_unavailable")
		return o
	}
	o.Storage, o.Engine = "verified", "unavailable"
	if e := g.Docker.CheckConnection(ctx); e != nil {
		o.Problem = errorKind(e, "engine_unavailable")
		return o
	}
	var e error
	o.DaemonID, e = g.Docker.DaemonID(ctx)
	if e != nil {
		o.Problem = errorKind(e, "engine_unavailable")
		return o
	}
	if o.DaemonID != b.DaemonID {
		o.Problem = "daemon_changed"
		return o
	}
	o.Engine = "verified"
	inspect := func(id string, workspace bool) *RuntimeObservation {
		if !docker.FullID(id) {
			return &RuntimeObservation{ContainerID: id, Problem: "invalid_exact_id"}
		}
		c, e := g.Docker.Inspect(ctx, id)
		if e != nil {
			return &RuntimeObservation{ContainerID: id, Problem: errorKind(e, "engine_unavailable")}
		}
		if c.ID != id {
			return &RuntimeObservation{ContainerID: id, Problem: "inspect_identity_mismatch"}
		}
		r := containerObservation(c)
		if workspace {
			r.Volumes, e = g.safeConfig(ctx, c)
			if e != nil {
				r.Problem = errorKind(e, "configuration_unavailable")
				return &r
			}
			r.Approved = c.Image == b.ImageID && r.Snapshot == b.SnapshotSHA256 && reflect.DeepEqual(r.Volumes, b.NamedVolumes)
			r.ConfigSafe = c.Image == b.ImageID && reflect.DeepEqual(r.Volumes, b.NamedVolumes)
			r.Matches = r.Approved && c.ID == b.ContainerID && c.State.StartedAt == b.StartedAt
		}
		return &r
	}
	o.Bound = inspect(b.ContainerID, true)
	if candidate != "" {
		if candidate == b.ContainerID {
			o.Candidate = o.Bound
		} else {
			o.Candidate = inspect(candidate, true)
		}
	}
	ids, e := g.Docker.ProjectContainers(ctx, s.ID)
	if e != nil {
		o.Problem = errorKind(e, "inventory_unavailable")
		return o
	}
	for _, id := range ids {
		switch {
		case id == b.ContainerID:
			o.Containers = append(o.Containers, *o.Bound)
		case o.Candidate != nil && id == o.Candidate.ContainerID:
			o.Containers = append(o.Containers, *o.Candidate)
		default:
			o.Containers = append(o.Containers, *inspect(id, false))
		}
	}
	return o
}

// ProvisionSnapshot approves Docker create's explicit default representation.
func (g *Guard) ProvisionSnapshot(ctx context.Context) (string, error) {
	t, e := g.CaptureTemplate(ctx, g.Record.Binding.ContainerID)
	if e != nil {
		return "", e
	}
	if t.Digest() != g.Record.Binding.SnapshotSHA256 {
		return "", docker.Fail("identity_changed", "provision source snapshot changed")
	}
	return t.ForCreate()
}
