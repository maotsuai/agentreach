//go:build !linux && !darwin

package policy

import "fmt"

// NativeClock uses the platform public clock and native boot identity.
func NativeClock() (Stamp, error) {
	return Stamp{}, fmt.Errorf("clock_unavailable: unsupported platform")
}
