package api

import (
	"strconv"
	"testing"
	"time"
)

// TestRateLimiterIsBounded checks that a limiter refuses new keys once it tracks maxRateKeys in their windows, and
// takes them again once the windows are over.
func TestRateLimiterIsBounded(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	l := newRateLimiter(1, time.Minute, func() time.Time { return now })

	for i := range maxRateKeys {
		if !l.allow(strconv.Itoa(i)) {
			t.Fatalf("key %d refused", i)
		}
	}

	if l.allow("one more") || !l.exceeded("one more") {
		t.Fatal("a key past the bound was allowed")
	}

	if l.allow("0") {
		t.Fatal("a tracked key past its limit was allowed")
	}

	now = now.Add(time.Minute)

	if !l.allow("one more") {
		t.Fatal("a new key was refused after the windows were over")
	}
}
