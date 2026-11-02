package client

import (
	"time"
)

// TS 26.179 clause 4.1.1
// TS 26.179 clause 4.1.3
// RFC 4867
const (
	// RFC 4867 section 4.1
	frameDuration = 20 * time.Millisecond
	clockRate     = 16000
	frameSamples  = clockRate / 50

	defaultMode = 2
)
