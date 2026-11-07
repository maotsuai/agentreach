package docker

import (
	"context"
	"sort"
	"strings"
)

// ProjectContainers returns a bounded read-only inventory. Labels are search
// hints only; they do not authorize a lifecycle operation.
func (c *Client) ProjectContainers(ctx context.Context, projectID string) ([]string, error) {
	if !volumeNamePattern.MatchString(projectID) || len(projectID) > 64 {
		return nil, Fail("policy_denied", "invalid project inventory scope")
	}
	b, err := c.command(ctx, true, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=dev.agentreach.project="+projectID)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(b))
	if len(ids) > 64 {
		return nil, Fail("request_budget", "project container inventory")
	}
	sort.Strings(ids)
	for i, id := range ids {
		if !fullID(id) || i > 0 && ids[i-1] == id {
			return nil, Fail("engine_unavailable", "invalid project container inventory")
		}
	}
	return ids, nil
}

// CreateWorkspace is private to the typed recovery driver. It never starts,
// pulls or builds; the driver must validate full configuration before start.
func (c *Client) CreateWorkspace(ctx context.Context, args []string) (string, error) {
	out, err := c.command(ctx, true, append([]string{"container", "create", "--pull", "never"}, args...)...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if !fullID(id) {
		return "", Fail("outcome_unknown", "workspace create returned an invalid exact identity")
	}
	return id, nil
}
