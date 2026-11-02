package lifetime

import (
	"context"
	"time"
)

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
