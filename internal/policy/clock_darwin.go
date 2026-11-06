package policy

import (
	"fmt"
	"golang.org/x/sys/unix"
)

// NativeClock uses the platform public clock and native boot identity.
func NativeClock() (Stamp, error) {
	boot, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return Stamp{}, err
	}
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &ts); err != nil {
		return Stamp{}, err
	}
	if len(boot) != 36 {
		return Stamp{}, fmt.Errorf("clock_unavailable: invalid boot identity")
	}
	return Stamp{Boot: boot, Tick: ts.Nano(), Wall: wallNow()}, nil
}
