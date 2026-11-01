// Package lifetime binds work to a lifetime other than that of its cause.
//
// Work that a request causes often outlives the request: a registrar notifies its watchers after answering a
// REGISTER, a proxy relays responses after its handler returned. Such work belongs to the trace of its cause, and to
// the lifetime of what does it. context.WithoutCancel keeps the first and drops the second; With keeps both.
package lifetime

import (
	"context"
	"time"
)

// With returns a context with the values of cause, and so its trace, and the deadline and cancellation of life. The
// values of life are not kept: life is a lifetime, such as that of a server, not a request.
func With(life, cause context.Context) context.Context {
	if cause == nil {
		cause = context.Background()
	}

	return bound{life: life, values: context.WithoutCancel(cause)}
}

type bound struct {
	life   context.Context
	values context.Context
}

func (b bound) Deadline() (time.Time, bool) { return b.life.Deadline() }

func (b bound) Done() <-chan struct{} { return b.life.Done() }

func (b bound) Err() error { return b.life.Err() }

func (b bound) Value(key any) any { return b.values.Value(key) }

// AfterFunc lets the contexts derived from b be cancelled with life, as context.WithCancel does for its own, without a
// goroutine each.
func (b bound) AfterFunc(f func()) func() bool { return context.AfterFunc(b.life, f) }

func (b bound) String() string {
	return "lifetime.With(" + contextName(b.life) + ", " + contextName(b.values) + ")"
}

func contextName(c context.Context) string {
	if s, ok := c.(interface{ String() string }); ok {
		return s.String()
	}

	return "context"
}
