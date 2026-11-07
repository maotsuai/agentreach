package ports

import (
	"context"
	"fmt"

	"github.com/bojieli/agentreach/internal/policy"
)

// RetiredManager is a private identity observation, not readiness or authority
// to route traffic. It exists only for explicit operator recovery.
type RetiredManager struct {
	Binding   policy.Binding `json:"binding"`
	Instance  string         `json:"instance"`
	Exposures []Exposure     `json:"exposures"`
	Reachable bool           `json:"private_manager_reachable"`
	Legacy    bool           `json:"p4_retained_listener_records,omitempty"`
}

func retiredClient(base, socketBase string, b policy.Binding, paths func() error) *Client {
	return &Client{Config: Config{Base: base, SocketBase: socketBase, Binding: b, Paths: paths}}
}

// ObserveRetired reads control state and asks the original nonce-bound manager
// for its listener identities. It performs no runtime action or repair.
func ObserveRetired(ctx context.Context, base, socketBase string, b policy.Binding, paths func() error) (RetiredManager, error) {
	c := retiredClient(base, socketBase, b, paths)
	d, e := c.load()
	out := RetiredManager{Binding: b, Instance: d.Instance, Exposures: d.Exposures}
	if e != nil {
		return out, e
	}
	if d.Instance == "" {
		return out, nil
	}
	reply, e := c.call(ctx, request{Op: "recovery_status", Instance: d.Instance})
	if e != nil && e.Error() == "policy_denied: unknown Host manager request" && b.Generation == 1 {
		// P4 validates nonce/binding before its list guard loads the project.
		// Its exact schema rejection proves the old manager answered with the
		// expected identity. Only the retained listener records are available;
		// P4 shutdown still refuses if any actual listener remains live.
		_, e = c.call(ctx, request{Op: "list", Instance: d.Instance})
		if e != nil && e.Error() == "policy_denied: unsupported project schema/provider, invalid id or missing container" {
			out.Reachable, out.Legacy = true, true
			return out, nil
		}
		return out, fmt.Errorf("identity_changed: could not verify the retired P4 manager")
	}
	if e != nil {
		return out, e
	}
	out.Exposures, out.Reachable = reply.Exposures, true
	return out, nil
}

// ObserveRecoveryExposures reads the current control ledger and probes only
// the private manager. A missing or stale runtime does not veto recovery.
func ObserveRecoveryExposures(ctx context.Context, base, socketBase string, b policy.Binding, paths func() error) ([]Exposure, error) {
	c := retiredClient(base, socketBase, b, paths)
	d, err := c.load()
	if err != nil || d.Instance == "" {
		return d.Exposures, err
	}
	reply, err := c.call(ctx, request{Op: "recovery_status", Instance: d.Instance})
	if err == nil {
		return reply.Exposures, nil
	}
	for i := range d.Exposures {
		if d.Exposures[i].State == "open" || d.Exposures[i].State == "creating" {
			d.Exposures[i].State = "unavailable"
		}
	}
	return d.Exposures, nil
}

// ShutdownRetired can only exit an exact original manager after its listeners
// and streams have been closed. It never signals a PID or restores a listener.
func ShutdownRetired(ctx context.Context, base, socketBase string, b policy.Binding, instance string, paths func() error) error {
	c := retiredClient(base, socketBase, b, paths)
	d, e := c.load()
	if e != nil {
		return e
	}
	if d.Instance != instance || instance == "" {
		return fmt.Errorf("identity_changed: retired manager instance")
	}
	_, e = c.call(ctx, request{Op: "shutdown", Instance: instance})
	return e
}
