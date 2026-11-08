package api

import (
	"strconv"
	"testing"
	"time"
)

func TestRateLimiterIsBounded(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	l := newRateLimiter(1, time.Minute, func() time.Time { return now })

	for i := range maxRateKeys {
		if !l.allow(strconv.Itoa(i)) {
			t.Fatalf("key %d refused", i)
		}
	}

	if l.allow("1") {
		t.Fatal("a tracked key past its limit was allowed")
	}

	if !l.allow("one more") {
		t.Fatal("a new key was refused when the limiter was full")
	}

	if len(l.windows) != maxRateKeys || l.order.Len() != maxRateKeys {
		t.Fatalf("limiter holds %d keys, want %d", len(l.windows), maxRateKeys)
	}

	if _, ok := l.windows["0"]; ok {
		t.Fatal("the oldest key was not evicted")
	}

	if l.allow("1") {
		t.Fatal("a tracked key was reset by the eviction of another")
	}
}

func TestRateLimiterSweepsExpiredKeys(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	l := newRateLimiter(1, time.Minute, func() time.Time { return now })

	l.allow("a")

	now = now.Add(30 * time.Second)

	l.allow("b")

	now = now.Add(30 * time.Second)

	if !l.allow("a") {
		t.Fatal("a key was refused after its window was over")
	}

	if _, ok := l.windows["b"]; !ok || len(l.windows) != 2 {
		t.Fatalf("limiter holds %d keys, want a and b", len(l.windows))
	}

	if l.allow("b") {
		t.Fatal("a key was allowed past its limit within its window")
	}
}

func TestExceededDoesNotCreateState(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	l := newRateLimiter(2, time.Hour, func() time.Time { return now })

	for i := range 2 * maxRateKeys {
		if l.exceeded(strconv.Itoa(i)) {
			t.Fatalf("key %d with no failures is exceeded", i)
		}
	}

	if len(l.windows) != 0 {
		t.Fatalf("exceeded created %d keys", len(l.windows))
	}

	l.allow("a")

	if l.exceeded("a") {
		t.Fatal("exceeded below the limit")
	}

	l.allow("a")

	if !l.exceeded("a") {
		t.Fatal("not exceeded at the limit")
	}

	now = now.Add(time.Hour)

	if l.exceeded("a") {
		t.Fatal("exceeded after the window")
	}
}

func TestFullLimiterAdmitsNewKeys(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	l := newRateLimiter(100, time.Hour, func() time.Time { return now })

	for i := range 3 * maxRateKeys {
		if !l.allow("junk" + strconv.Itoa(i)) {
			t.Fatalf("junk key %d refused", i)
		}

		if i%maxRateKeys == 0 {
			now = now.Add(time.Minute)
		}
	}

	if l.exceeded("admin@example.com") || !l.allow("admin@example.com") {
		t.Fatal("a new key was refused when the limiter was full")
	}
}
