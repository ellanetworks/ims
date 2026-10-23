package client

import (
	"time"
)

// The AMR-WB speech of the client's source (TS 26.179 §4.1.1) in its RTP payload format (RFC 4867): one frame-block
// of one channel per packet, every 20 ms (TS 26.179 §4.1.3: ptime 20).
const (
	// frameDuration and frameSamples are the duration of a frame-block, and its samples at the 16 kHz clock of the
	// timestamp (RFC 4867 §4.1).
	frameDuration = 20 * time.Millisecond
	clockRate     = 16000
	frameSamples  = clockRate / 50

	// defaultMode is the mode of the frames: 12.65 kbit/s.
	defaultMode = 2
)
