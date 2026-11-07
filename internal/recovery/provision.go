package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/runtime/orbstack"
	"github.com/bojieli/agentreach/internal/service"
)

// ProvisionSpec is deliberately narrower than Docker create. It reuses only
// approved mounts, existing project network and immutable local image. No
// implicit bind creation, image pull, build, restart policy or volume removal.
type ProvisionSpec struct {
	Schema    int               `json:"schema"`
	Image     string            `json:"image"`
	NetworkID string            `json:"network_id"`
	User      string            `json:"user"`
	Argv      []string          `json:"argv"`
	Env       map[string]string `json:"env"`
	Labels    map[string]string `json:"labels"`
	Memory    int64             `json:"memory"`
	NanoCPUs  int64             `json:"nano_cpus"`
	PIDs      int64             `json:"pids"`
	LogSize   string            `json:"log_size,omitempty"`
	LogBytes  int64             `json:"log_bytes"`
	Retention string            `json:"retention"`
}

var logSizePattern = regexp.MustCompile(`^[1-9][0-9]{0,8}[km]?$`)

var envKey = regexp.MustCompile(`^[A-Z_][A-Z_0-9]{0,63}$`)
var provisionUser = regexp.MustCompile(`^[1-9][0-9]{0,8}:[1-9][0-9]{0,8}$`)

func (s ProvisionSpec) validate(r project.Record) error {
	if !logSizePattern.MatchString(s.LogSize) {
		return fmt.Errorf("policy_denied: exact approved log_size is required")
	}
	size := s.LogSize
	unit := int64(1)
	if strings.HasSuffix(size, "m") {
		unit = 1 << 20
		size = strings.TrimSuffix(size, "m")
	}
	if strings.HasSuffix(size, "k") {
		unit = 1 << 10
		size = strings.TrimSuffix(size, "k")
	}
	n, e := strconv.ParseInt(size, 10, 64)
	if e != nil || n*unit != s.LogBytes {
		return fmt.Errorf("policy_denied: log_size does not match log_bytes")
	}
	if s.Schema != 1 || s.Image != r.Binding.ImageID || !docker.FullID(s.NetworkID) || !provisionUser.MatchString(s.User) ||
		len(s.Argv) == 0 || len(s.Argv) > 128 || s.Retention != "preserve" ||
		s.Memory < 32<<20 || s.Memory > 16<<30 || s.NanoCPUs < 100000000 || s.NanoCPUs > 16000000000 || s.PIDs < 16 || s.PIDs > 4096 || s.LogBytes < 1<<20 || s.LogBytes > 8<<20 || len(s.Env) > 64 || len(s.Labels) > 8 {
		return fmt.Errorf("policy_denied: invalid bounded runtime provisioning specification")
	}
	for _, arg := range s.Argv {
		if strings.ContainsRune(arg, 0) || len(arg) > 4096 {
			return fmt.Errorf("policy_denied: invalid provision argv")
		}
	}
	for k, v := range s.Env {
		if !envKey.MatchString(k) || strings.ContainsRune(v, 0) || len(v) > 4096 {
			return fmt.Errorf("policy_denied: invalid provision environment")
		}
	}
	for k, v := range s.Labels {
		if k != "dev.agentreach.managed" && k != "dev.agentreach.project" && k != "agentreach.task" || len(v) > 128 || strings.ContainsRune(v, 0) {
			return fmt.Errorf("policy_denied: unsupported workspace label")
		}
	}
	if s.Labels["dev.agentreach.managed"] != "true" || s.Labels["dev.agentreach.project"] != r.Spec.ID {
		return fmt.Errorf("policy_denied: provisioning ownership labels")
	}
	return nil
}

// ProvisionObservation binds local prerequisites and the exact created snapshot.
type ProvisionObservation struct {
	Template            *orbstack.SnapshotTemplate `json:"approved_source_template,omitempty"`
	ConfigurationSource string                     `json:"configuration_source,omitempty"`
	ImageID             string                     `json:"image_id"`
	NetworkID           string                     `json:"network_id"`
	ExpectedSnapshot    string                     `json:"expected_created_snapshot,omitempty"`
	ExpectedRunning     string                     `json:"expected_running_snapshot,omitempty"`
	Changes             []string                   `json:"approved_create_changes,omitempty"`
	Ready               bool                       `json:"prerequisites_verified"`
	Problem             string                     `json:"problem,omitempty"`
}

func (o ProvisionObservation) matchesSnapshot(snapshot string) bool {
	if snapshot == "" {
		return false
	}
	return snapshot == o.ExpectedSnapshot || snapshot == o.ExpectedRunning ||
		o.ExpectedRunning == "" && o.Template != nil && snapshot == o.Template.Digest()
}

func observeProvision(ctx context.Context, r project.Record, s ProvisionSpec) ProvisionObservation {
	out := ProvisionObservation{ImageID: s.Image, NetworkID: s.NetworkID}
	if err := s.validate(r); err != nil {
		out.Problem = "invalid_provision_spec"
		return out
	}
	g := orbstack.New(r)
	if err := g.CheckRecoveryBase(ctx); err != nil {
		out.Problem = reportProblem(err)
		return out
	}
	image, err := g.Docker.Image(ctx, s.Image)
	if err != nil || image.ID != s.Image || len(image.Config.Volumes) != 0 {
		out.Problem = "image_unavailable_or_implicit_volume"
		return out
	}
	network, err := g.Docker.Network(ctx, s.NetworkID)
	if err != nil || network.ID != s.NetworkID || network.Driver != "bridge" || network.Labels["dev.agentreach.project"] != r.Spec.ID {
		out.Problem = "network_unavailable_or_foreign"
		return out
	}
	for _, v := range r.Spec.NamedVolumes {
		actual, e := g.Docker.Volume(ctx, v.Name)
		want, ok := r.Binding.NamedVolumes[v.Name]
		if e != nil || !ok || serviceDigest(actual) != serviceDigest(want) {
			out.Problem = "approved_volume_changed"
			return out
		}
	}
	out.Template, out.ConfigurationSource, err = provisionTemplate(ctx, r)
	if err != nil {
		out.Problem = "source_configuration_unavailable"
		return out
	}
	if out.Template.User != s.User || serviceDigest(out.Template.Labels) != serviceDigest(s.Labels) {
		out.Problem = "provision_user_or_labels_differ"
		return out
	}
	var approved struct {
		Memory, MemorySwap, NanoCpus, PidsLimit int64
		Init                                    bool
		LogConfig                               struct {
			Type   string
			Config map[string]string
		}
	}
	encoded, _ := json.Marshal(out.Template.Host)
	if err = json.Unmarshal(encoded, &approved); err != nil || approved.Memory != s.Memory || approved.MemorySwap != s.Memory || approved.NanoCpus != s.NanoCPUs || approved.PidsLimit != s.PIDs || !approved.Init ||
		approved.LogConfig.Type != "json-file" || len(approved.LogConfig.Config) != 2 || approved.LogConfig.Config["max-size"] != s.LogSize || approved.LogConfig.Config["max-file"] != "1" {
		out.Problem = "provision_resources_or_logging_differ"
		return out
	}
	out.ExpectedSnapshot, err = out.Template.ForCreate()
	if err != nil {
		out.Problem = "unsupported_create_snapshot"
		return out
	}
	out.ExpectedRunning, err = out.Template.ForRunning()
	if err != nil {
		out.Problem = "unsupported_running_snapshot"
		return out
	}
	out.Changes = []string{"HostConfig.OomKillDisable is false on create; after start only the exact false and null snapshots are approved"}
	out.Ready = true
	return out
}

func serviceDigest(v any) string { // canonical JSON through the existing digest implementation
	return service.Digest(v)
}

func (c *Controller) provision(ctx context.Context, r project.Record, s ProvisionSpec, opID string, save func(string, bool) error, authorized func() error, expected ProvisionObservation) (string, error) {
	if err := s.validate(r); err != nil {
		return "", err
	}
	name := "reach-runtime-" + opID
	args := []string{"--name", name, "--network", s.NetworkID, "--user", s.User, "--workdir", r.Spec.Workspace.GuestPath,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--init", "--restart", "no",
		"--memory", fmt.Sprint(s.Memory), "--memory-swap", fmt.Sprint(s.Memory), "--cpus", fmt.Sprintf("%.9f", float64(s.NanoCPUs)/1e9), "--pids-limit", fmt.Sprint(s.PIDs),
		"--log-opt", "max-size=" + s.LogSize, "--log-opt", "max-file=1", "--entrypoint", s.Argv[0]}
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		args = append(args, "--label", k+"="+s.Labels[k])
	}
	keys = keys[:0]
	for k := range s.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+s.Env[k])
	}
	for _, m := range r.Spec.BindMounts {
		if strings.ContainsAny(m.Source+m.Target, ",\n\r") {
			return "", fmt.Errorf("policy_denied: unsupported bind punctuation")
		}
		v := "type=bind,src=" + m.Source + ",dst=" + m.Target
		if m.ReadOnly {
			v += ",readonly"
		}
		args = append(args, "--mount", v)
	}
	for _, m := range r.Spec.NamedVolumes {
		if strings.ContainsAny(m.Name+m.Target, ",\n\r") {
			return "", fmt.Errorf("policy_denied: unsupported volume punctuation")
		}
		v := "type=volume,src=" + m.Name + ",dst=" + m.Target + ",volume-nocopy"
		if m.ReadOnly {
			v += ",readonly"
		}
		args = append(args, "--mount", v)
	}
	args = append(args, s.Image)
	args = append(args, s.Argv[1:]...)
	client := &docker.Client{Connection: r.Spec.Docker}
	id, err := client.CreateWorkspace(ctx, args)
	if err != nil {
		return "", err
	}
	// Record the exact created ID before any start. A failure here leaves a
	// created resource and an unresolved original operation, never a retry.
	if err = save(id, false); err != nil {
		return id, err
	}
	full, err := client.InspectService(ctx, id)
	if err != nil {
		return id, err
	}
	var cfg struct {
		User, WorkingDir     string
		Cmd, Entrypoint, Env []string
		Labels               map[string]string
	}
	if err = json.Unmarshal(full.Config, &cfg); err != nil {
		return id, err
	}
	if full.ID != id || full.Image != s.Image || cfg.User != s.User || cfg.WorkingDir != r.Spec.Workspace.GuestPath ||
		!slices.Equal(cfg.Entrypoint, []string{s.Argv[0]}) || !slices.Equal(cfg.Cmd, s.Argv[1:]) || serviceDigest(cfg.Labels) != serviceDigest(s.Labels) {
		return id, fmt.Errorf("identity_changed: provisioned command/configuration changed")
	}
	image, err := client.Image(ctx, s.Image)
	if err != nil {
		return id, err
	}
	expectedEnv := map[string]string{}
	for _, v := range image.Config.Env {
		k, value, ok := strings.Cut(v, "=")
		if !ok {
			return id, fmt.Errorf("identity_changed: malformed image environment")
		}
		expectedEnv[k] = value
	}
	for k, v := range s.Env {
		expectedEnv[k] = v
	}
	actualEnv := map[string]string{}
	for _, v := range cfg.Env {
		k, value, ok := strings.Cut(v, "=")
		if !ok {
			return id, fmt.Errorf("identity_changed: malformed created environment")
		}
		if _, dup := actualEnv[k]; dup {
			return id, fmt.Errorf("identity_changed: duplicate created environment")
		}
		actualEnv[k] = value
	}
	if serviceDigest(actualEnv) != serviceDigest(expectedEnv) {
		return id, fmt.Errorf("identity_changed: provisioned environment changed")
	}
	// Existing P0 snapshot approval remains mandatory. Provisioning cannot
	// broaden mounts, namespaces, resources or execution privilege.
	o := c.observe(ctx, r, id)
	if o.Problem != "" || o.Candidate == nil || !o.Candidate.ConfigSafe || o.Candidate.Snapshot != expected.ExpectedSnapshot || o.Candidate.Problem != "" || o.Candidate.State != "created" {
		return id, fmt.Errorf("identity_changed: created runtime does not match approved workspace snapshot")
	}
	current := c.provisionCheck(ctx, r, s)
	if !current.Ready || serviceDigest(current) != serviceDigest(expected) {
		return id, fmt.Errorf("plan_stale: provision prerequisites changed")
	}
	if expected.ConfigurationSource == "exact_source_configuration" && (o.Bound == nil || !o.Bound.Matches || o.Bound.State != "exited" && o.Bound.State != "created") {
		return id, fmt.Errorf("plan_stale: original runtime changed during provisioning")
	}
	if err = save(id, true); err != nil {
		return id, err
	}
	if err = authorized(); err != nil {
		return id, err
	}
	if _, err = client.ChangeService(ctx, id, "start"); err != nil {
		return id, err
	}
	started := c.observe(ctx, r, id)
	if started.Problem != "" || started.Candidate == nil || !started.Candidate.ConfigSafe ||
		started.Candidate.ContainerID != id || started.Candidate.Name != name || started.Candidate.Problem != "" ||
		started.Candidate.State != "running" || started.Candidate.StartedAt == "" || !expected.matchesSnapshot(started.Candidate.Snapshot) {
		return id, fmt.Errorf("identity_changed: started runtime does not match either approved snapshot")
	}
	return id, nil
}
