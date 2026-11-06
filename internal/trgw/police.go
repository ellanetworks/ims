package trgw

import (
	"sync"
	"time"
)

// TS 26.114 clause K.2
const (
	ipv4Header = 20
	ipv6Header = 40
	udpHeader  = 8
)

// TS 29.162 clause 10.2.9
const burstPeriod = 2 * time.Second

// TS 29.235 clause A.7.1.2.2.8.2
type bucket struct {
	bps   uint64
	rate  float64
	depth float64

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func newBucket(bps uint64, now time.Time) *bucket {
	rate := float64(bps) / 8
	depth := rate * burstPeriod.Seconds()

	return &bucket{bps: bps, rate: rate, depth: depth, tokens: depth, last: now}
}

func (b *bucket) admit(size int, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = min(b.depth, b.tokens+elapsed*b.rate)
		b.last = now
	}

	if float64(size) > b.tokens {
		return false
	}

	b.tokens -= float64(size)

	return true
}
