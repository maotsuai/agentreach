package host

import (
	"context"
	"github.com/bojieli/agentreach/internal/service"
)

// ServiceResolver adapts the production registry into the browser authority boundary.
func ServiceResolver(m *service.Manager) func(context.Context, string) (Exposure, error) {
	return func(ctx context.Context, id string) (Exposure, error) {
		x, e := m.Exposure(ctx, id)
		return Exposure{Binding: x.Target.Binding, ID: x.ID, Generation: x.Target.Generation, Healthy: x.State == "open", Address: x.Address, Port: x.Port}, e
	}
}
