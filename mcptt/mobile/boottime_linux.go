package mobile

import (
	"time"

	"golang.org/x/sys/unix"
)

func bootTime() time.Duration {
	var ts unix.Timespec

	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return time.Duration(time.Now().UnixNano())
	}

	return time.Duration(ts.Nano())
}
