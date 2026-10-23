package mobile

import (
	"time"

	"golang.org/x/sys/unix"
)

// bootTime is the time since boot, the time asleep included (CLOCK_BOOTTIME, which Android's elapsedRealtime reads).
func bootTime() time.Duration {
	var ts unix.Timespec

	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return time.Duration(time.Now().UnixNano())
	}

	return time.Duration(ts.Nano())
}
