//go:build !linux

package mobile

import "time"

var start = time.Now()

// bootTime is the time since the package started, on a platform without a boot clock.
func bootTime() time.Duration { return time.Since(start) }
