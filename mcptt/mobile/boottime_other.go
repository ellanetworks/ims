//go:build !linux

package mobile

import "time"

var start = time.Now()

func bootTime() time.Duration { return time.Since(start) }
