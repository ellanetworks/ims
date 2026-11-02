package ua

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"go.opentelemetry.io/otel/trace"
)

var ErrResourceTerminated = errors.New("sip/ua: resource terminated")

type ResourceConfig[S, D any] struct {
	Full func(value any, seq uint32, state S) (Notification, error)

	Partial func(value any, seq uint32, state S, delta D) (Notification, error)

	MinInterval time.Duration
}

// RFC 6665 section 4.2.2
type Resource[S, D any] struct {
	cfg ResourceConfig[S, D]

	mu         sync.Mutex
	state      S
	version    uint64
	subs       map[*ServerSubscription]struct{}
	terminated bool
}

func NewResource[S, D any](state S, cfg ResourceConfig[S, D]) *Resource[S, D] {
	if cfg.Full == nil {
		panic("sip/ua: a ResourceConfig needs Full")
	}

	return &Resource[S, D]{cfg: cfg, state: state, subs: make(map[*ServerSubscription]struct{})}
}

func (r *Resource[S, D]) State() S {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.state
}

func (r *Resource[S, D]) Change(ctx context.Context, state S, delta D) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.terminated {
		return ErrResourceTerminated
	}

	r.state = state
	r.version++

	v := r.version

	for s := range r.subs {
		var partial func(uint32) (Notification, error)

		if r.cfg.Partial != nil {
			partial = func(seq uint32) (Notification, error) {
				n, err := r.cfg.Partial(s.value, seq, state, delta)
				n.Full = false

				return n, err
			}
		}

		s.st.post(func() { s.changed(ctx, v, partial) })
	}

	return nil
}

func (r *Resource[S, D]) Subscriptions() []*ServerSubscription {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]*ServerSubscription, 0, len(r.subs))
	for s := range r.subs {
		out = append(out, s)
	}

	return out
}

// RFC 6665 section 4.1.3
func (r *Resource[S, D]) Terminate(ctx context.Context, reason string, retryAfter time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.terminated {
		return
	}

	r.terminated = true

	ss := sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: reason}

	if retryAfter > 0 {
		v := uint32(min(retryAfter/time.Second, math.MaxUint32))
		ss.RetryAfter = &v
	}

	for s := range r.subs {
		s.st.post(func() { s.terminate(ctx, ss, End{Cause: EndTerminated}, nil) })
	}
}

type Source interface {
	bind(s *ServerSubscription) error
	detach(s *ServerSubscription)
	render(s *ServerSubscription, seq uint32) (Notification, error)
	latest() uint64
	minInterval() time.Duration
}

func (r *Resource[S, D]) bind(s *ServerSubscription) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.terminated {
		return ErrResourceTerminated
	}

	r.subs[s] = struct{}{}

	return nil
}

func (r *Resource[S, D]) detach(s *ServerSubscription) {
	r.mu.Lock()
	delete(r.subs, s)
	r.mu.Unlock()
}

func (r *Resource[S, D]) render(s *ServerSubscription, seq uint32) (Notification, error) {
	r.mu.Lock()
	state, v := r.state, r.version
	r.mu.Unlock()

	s.version = v

	n, err := r.cfg.Full(s.value, seq, state)
	n.Full = true

	return n, err
}

func (r *Resource[S, D]) latest() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.version
}

func (r *Resource[S, D]) minInterval() time.Duration {
	return r.cfg.MinInterval
}

func (s *ServerSubscription) changed(ctx context.Context, v uint64, partial func(uint32) (Notification, error)) {
	if s.ended || s.ending != nil || s.state != SubscriptionActive || v <= s.version {
		return
	}

	if s.held != nil {
		if len(s.heldCauses) < 128 {
			s.heldCauses = append(s.heldCauses, trace.SpanContextFromContext(ctx))
		}

		return
	}

	if wait := s.nextChange.Sub(s.a.clock.Now()); wait > 0 {
		s.heldCauses = []trace.SpanContext{trace.SpanContextFromContext(ctx)}
		s.held = s.st.after(wait, func() {
			ctx, span := s.a.startDetached(s.heldCauses, "subscription notify")
			defer span.End()

			s.held, s.heldCauses = nil, nil

			if !s.ended && s.ending == nil && s.src.latest() > s.version {
				s.notifyChange(ctx, nil)
			}
		})

		return
	}

	if partial != nil && v == s.version+1 && s.inflight == nil && len(s.queue) == 0 {
		s.version = v
		s.notifyChange(ctx, partial)

		return
	}

	s.notifyChange(ctx, nil)
}

func (s *ServerSubscription) notifyChange(ctx context.Context, partial func(uint32) (Notification, error)) {
	if d := s.src.minInterval(); d > 0 {
		s.nextChange = s.a.clock.Now().Add(d)
	}

	if partial != nil {
		s.enqueue(&pendingNotify{ctx: ctx, build: partial})
		return
	}

	s.enqueue(&pendingNotify{ctx: ctx, build: s.stateOf, full: true})
}
