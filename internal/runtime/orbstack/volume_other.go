//go:build !darwin

package orbstack

import (
	"context"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/docker"
)

// CheckNativeVolume fails closed on platforms without native macOS metadata.
func CheckNativeVolume(context.Context, project.VolumeClaim) error {
	return docker.Fail("storage_unavailable", "managed OrbStack control requires macOS volume identity checks")
}

// CheckNativeEngineData does not infer macOS storage from another platform.
func CheckNativeEngineData(context.Context, string, string) error {
	return docker.Fail("storage_unavailable", "managed OrbStack storage inspection requires macOS")
}

// CheckNativeControlHome never substitutes the probe's HOME for macOS identity.
func CheckNativeControlHome(context.Context, string) error {
	return docker.Fail("storage_unavailable", "managed OrbStack account inspection requires macOS")
}

// CheckNativePathVolume fails closed without native macOS statfs metadata.
func CheckNativePathVolume(context.Context, string, project.VolumeClaim) error {
	return docker.Fail("storage_unavailable", "managed OrbStack path volume checks require macOS")
}
