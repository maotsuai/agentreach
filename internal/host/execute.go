// Package host implements typed Host capabilities. It has no shell/argv tool.
package host

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/ports"
	"github.com/bojieli/agentreach/internal/service"
	"github.com/bojieli/agentreach/internal/terminal"
)

// Exposure is a trusted registry observation, never a model-provided origin.
type Exposure struct {
	Binding    policy.Binding
	ID         string
	Generation uint64
	Healthy    bool
	Address    string
	Port       uint16
}

// Executor dispatches typed Host effects after durable authorisation.
type Executor struct {
	Store         *policy.Store
	Terminal      *terminal.Manager
	OpenTerminals func(func(context.Context) error) (*terminal.Manager, error)
	Services      *service.Manager
	OpenServices  func(func(context.Context) error) (*service.Manager, error)
	// Only trusted code may supply the production service registry.
	Resolve func(context.Context, string) (Exposure, error)
	launch  func(context.Context, string) error
}

func exposureURL(e Exposure, r policy.Request) (string, error) {
	if e.Binding != r.Binding || e.ID != r.Host.TargetID || e.Generation != r.Host.Generation || !e.Healthy || e.Port == 0 || e.Address != "127.0.0.1" && e.Address != "::1" {
		return "", fmt.Errorf("exposure_unavailable: stale, unhealthy or foreign exposure")
	}
	p, err := policy.RelativePath(r.Host.Params.Path)
	if err != nil {
		return "", err
	}
	u := url.URL{Scheme: "http", Host: net.JoinHostPort(e.Address, strconv.Itoa(int(e.Port))), Path: p}
	return u.String(), nil
}

// Execute verifies the resource before intent and again immediately before launch.
func (e *Executor) Execute(ctx context.Context, id, approval string) (policy.Operation, error) {
	v, err := e.Store.Inspect()
	if err != nil {
		return policy.Operation{}, err
	}
	for _, p := range v.Pending {
		if p.ID == id && p.Request.Host.Capability == "open_url" && e.Services != nil {
			var op policy.Operation
			err := e.Services.WithExposure(ctx, p.Request.Host.TargetID, func(resolve func() (ports.Exposure, error)) error {
				connection := *e
				connection.Services = nil
				connection.Resolve = func(_ context.Context, target string) (Exposure, error) {
					if target != p.Request.Host.TargetID {
						return Exposure{}, fmt.Errorf("request_mismatch: exposure scope")
					}
					x, err := resolve()
					return Exposure{Binding: x.Target.Binding, ID: x.ID, Generation: x.Target.Generation, Healthy: x.State == "open", Address: x.Address, Port: x.Port}, err
				}
				var err error
				op, err = connection.Execute(ctx, id, approval)
				return err
			})
			return op, err
		}
		if p.ID == id && p.Request.Host.Capability != "open_url" {
			if e.Services == nil {
				return policy.Operation{}, fmt.Errorf("unsupported: capability driver unavailable")
			}
			return e.Services.ExecuteHost(ctx, id, approval)
		}
	}
	var authorized Exposure
	verify := func(r policy.Request) error {
		if r.Host.Capability != "open_url" {
			return fmt.Errorf("unsupported: capability driver belongs to P4/P5")
		}
		if e.Resolve == nil {
			return fmt.Errorf("exposure_unavailable: P4 registry is not installed")
		}
		var err error
		authorized, err = e.Resolve(ctx, r.Host.TargetID)
		if err != nil {
			return err
		}
		_, err = exposureURL(authorized, r)
		return err
	}
	op, r, err := e.Store.BeginHost(ctx, id, approval, verify)
	if err != nil {
		return op, err
	}
	current, err := e.Resolve(ctx, r.Host.TargetID)
	if err == nil && current != authorized {
		err = fmt.Errorf("exposure_unavailable: generation changed")
	}
	var u string
	if err == nil {
		u, err = exposureURL(current, r)
	}
	outcome := "failed"
	if err == nil {
		launch := e.launch
		if launch == nil {
			launch = openBrowser
		}
		err = launch(ctx, u)
		outcome = "ok"
		if err != nil {
			outcome = "outcome_unknown"
		}
	}
	op.Outcome = outcome
	if resultErr := e.Store.Finish(context.WithoutCancel(ctx), op.ID, outcome); resultErr != nil {
		op.Outcome = "outcome_unknown"
		if err != nil {
			resultErr = fmt.Errorf("%w; dispatch: %w", resultErr, err)
		}
		return op, resultErr
	}
	if outcome == "outcome_unknown" {
		err = policy.UnknownOutcome(op.ID, err)
	}
	return op, err
}

func openBrowser(ctx context.Context, u string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("unsupported: browser executor requires macOS")
	}
	c := exec.CommandContext(ctx, "/usr/bin/open", "--", u)
	c.Env = []string{"PATH=/usr/bin:/bin"}
	return c.Run()
}
