package policy

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// NativeClock uses the platform public clock and native boot identity.
func NativeClock() (Stamp, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return Stamp{}, err
	}
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return Stamp{}, err
	}
	b := strings.TrimSpace(string(boot))
	if len(b) != 36 {
		return Stamp{}, fmt.Errorf("clock_unavailable: invalid boot identity")
	}
	return Stamp{Boot: b, Tick: ts.Nano(), Wall: wallNow()}, nil
}
