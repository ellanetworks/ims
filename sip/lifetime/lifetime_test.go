package lifetime_test

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip/lifetime"
)

type key struct{}

func TestWithKeepsTheValuesOfTheCause(t *testing.T) {
	life := context.WithValue(context.Background(), key{}, "life")
	cause := context.WithValue(context.Background(), key{}, "cause")

	if got := lifetime.With(life, cause).Value(key{}); got != "cause" {
		t.Fatalf("value %v, want the cause's", got)
	}

	if got := lifetime.With(context.Background(), nil).Value(key{}); got != nil {
		t.Fatalf("value %v of a nil cause", got)
	}
}

func TestWithEndsWithTheLifetimeNotTheCause(t *testing.T) {
	life, stop := context.WithCancel(context.Background())
	cause, cancel := context.WithCancel(context.Background())

	ctx := lifetime.With(life, cause)

	cancel()

	select {
	case <-ctx.Done():
		t.Fatal("ended with its cause")
	default:
	}

	stop()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("did not end with its lifetime")
	}

	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("Err %v", ctx.Err())
	}
}

func TestWithHasTheDeadlineOfTheLifetime(t *testing.T) {
	at := time.Now().Add(time.Hour)

	life, stop := context.WithDeadline(context.Background(), at)
	defer stop()

	cause, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if d, ok := lifetime.With(life, cause).Deadline(); !ok || !d.Equal(at) {
		t.Fatalf("deadline %v %v, want the lifetime's", d, ok)
	}
}

func TestChildrenEndWithTheLifetime(t *testing.T) {
	life, stop := context.WithCancel(context.Background())
	child, cancel := context.WithCancelCause(lifetime.With(life, context.Background()))

	defer cancel(nil)

	stop()

	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("child did not end with the lifetime")
	}
}

func TestChildrenDoNotLeak(t *testing.T) {
	life, stop := context.WithCancel(context.Background())
	defer stop()

	before := runtime.NumGoroutine()

	for range 1000 {
		_, cancel := context.WithCancel(lifetime.With(life, context.Background()))
		cancel()
	}

	if after := runtime.NumGoroutine(); after > before+10 {
		t.Fatalf("%d goroutines after, %d before", after, before)
	}
}
