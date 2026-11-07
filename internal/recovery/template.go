package recovery

import (
	"context"
	"fmt"

	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/orbstack"
)

// provisionTemplate uses an exact current snapshot or an authenticated copy of
// that same approved snapshot retained before the runtime disappeared.
func provisionTemplate(ctx context.Context, r project.Record) (*orbstack.SnapshotTemplate, string, error) {
	t, e := orbstack.New(r).CaptureTemplate(ctx, r.Binding.ContainerID)
	if e == nil && t.Digest() == r.Binding.SnapshotSHA256 {
		return t, "exact_source_configuration", nil
	}
	_, j, err := historicalBindings(r)
	if err != nil {
		return nil, "", err
	}
	for i := len(j.Entries) - 1; i >= 0; i-- {
		entry := j.Entries[i]
		if entry.Before.Binding.ImageID != r.Binding.ImageID {
			continue
		}
		candidates := []*orbstack.SnapshotTemplate{entry.Plan.Template}
		if entry.Plan.Provision != nil {
			candidates = append(candidates, entry.Plan.Provision.Template)
		}
		for _, candidate := range candidates {
			if candidate != nil && candidate.Digest() == r.Binding.SnapshotSHA256 {
				return candidate, "retained_approved_binding_template", nil
			}
		}
	}
	return nil, "", fmt.Errorf("configuration_unavailable: no exact approved runtime template")
}
