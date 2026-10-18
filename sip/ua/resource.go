package ua

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
)

// ErrResourceTerminated is the error of binding a subscription to a resource terminated, or of changing one.
var ErrResourceTerminated = errors.New("sip/ua: resource terminated")

// ResourceConfig renders the state of a Resource for each of its subscriptions.
type ResourceConfig[S, D any] struct {
	// Full renders the full state for a subscription: value is the Value of the subscription, as its filter
	// (RFC 4661, TS 24.379 §9.3.2) or its subscriber; seq is the number of NOTIFYs sent before it in the subscription,
	// for the versions of the packages that count them (RFC 4575 §4.3). It runs on the strand of the subscription,
	// with the strand held: it must not call the agent.
	Full func(value any, seq uint32, state S) (Notification, error)

	// Partial, when not nil, renders the change delta that made state, as a partial notification (RFC 4575 §4.4),
	// for a subscription whose last NOTIFY had the state before it. Without Partial, each change sends the full state.
	Partial func(value any, seq uint32, state S, delta D) (Notification, error)

	// MinInterval is the shortest time between two NOTIFYs of changes to one subscription, as the event package
	// recommends (RFC 4575 §3.9, RFC 3856 §6.10: 5 s): the changes within it come together, in one NOTIFY of the full
	// state at its end. The NOTIFYs of a new subscription, of a refresh and of the end are sent at once. Zero sends
	// each change at once.
	MinInterval time.Duration
}

// Resource is the state of one resource for an event package at the notifier, and the subscriptions to it: those
// bound with Acceptance.Resource, of one agent or several. Each change reaches each subscription on its strand, in
// order, rendered for it (RFC 6665 §4.2.2). A subscriber slower than the changes holds back no other, nor more than
// one NOTIFY of its own: the changes made while its NOTIFY is in flight come together, in one NOTIFY of the full state
// once that one completes. A partial notification only goes to a subscription that has every change before it.
//
// The state is a value the application does not change once given: the subscriptions render it on their strands.
type Resource[S, D any] struct {
	cfg ResourceConfig[S, D]

	mu         sync.Mutex
	state      S
	version    uint64
	subs       map[*ServerSubscription]struct{}
	terminated bool
}

// NewResource makes a resource of the state given.
func NewResource[S, D any](state S, cfg ResourceConfig[S, D]) *Resource[S, D] {
	if cfg.Full == nil {
		panic("sip/ua: a ResourceConfig needs Full")
	}

	return &Resource[S, D]{cfg: cfg, state: state, subs: make(map[*ServerSubscription]struct{})}
}

// State is the current state of the resource.
func (r *Resource[S, D]) State() S {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.state
}

// Change makes state the state of the resource, delta the change from the previous one, and notifies the
// subscriptions.
func (r *Resource[S, D]) Change(state S, delta D) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.terminated {
		return ErrResourceTerminated
	}

	r.state = state
	r.version++

	v := r.version

	// Posted under the lock, the changes reach each subscription in their order.
	for s := range r.subs {
		var partial func(uint32) (Notification, error)

		if r.cfg.Partial != nil {
			partial = func(seq uint32) (Notification, error) {
				n, err := r.cfg.Partial(s.value, seq, state, delta)
				n.Full = false

				return n, err
			}
		}

		s.st.post(func() { s.changed(v, partial) })
	}

	return nil
}

// Subscriptions are the subscriptions to the resource.
func (r *Resource[S, D]) Subscriptions() []*ServerSubscription {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]*ServerSubscription, 0, len(r.subs))
	for s := range r.subs {
		out = append(out, s)
	}

	return out
}

// Terminate ends the subscriptions with a NOTIFY terminated with reason, without state (RFC 6665 §4.1.3: noresource
// for a resource gone, deactivated for a migration), and retryAfter when not zero; the resource takes no more.
func (r *Resource[S, D]) Terminate(reason string, retryAfter time.Duration) {
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
		s.st.post(func() { s.terminate(ss, End{Cause: EndTerminated}, nil) })
	}
}

// Source is what a subscription notifies the state of: a Resource.
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

// render renders the full state for s, which then has its version.
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

// changed notifies the change to version v of the resource of s, partial rendering it as a partial notification
// when the package has them. Called with the strand held.
func (s *ServerSubscription) changed(v uint64, partial func(uint32) (Notification, error)) {
	if s.ended || s.ending != nil || s.state != SubscriptionActive || v <= s.version || s.held != nil {
		return
	}

	// Within the interval since the last change notified, the changes wait for its end, together.
	if wait := s.nextChange.Sub(s.a.clock.Now()); wait > 0 {
		s.held = s.st.after(wait, func() {
			s.held = nil

			if !s.ended && s.ending == nil && s.src.latest() > s.version {
				s.notifyChange(nil)
			}
		})

		return
	}

	if partial != nil && v == s.version+1 && s.inflight == nil && len(s.queue) == 0 {
		s.version = v
		s.notifyChange(partial)

		return
	}

	s.notifyChange(nil)
}

// notifyChange queues the NOTIFY of a change: partial, or the full state without it.
func (s *ServerSubscription) notifyChange(partial func(uint32) (Notification, error)) {
	if d := s.src.minInterval(); d > 0 {
		s.nextChange = s.a.clock.Now().Add(d)
	}

	if partial != nil {
		s.enqueue(&pendingNotify{build: partial})
		return
	}

	s.enqueue(&pendingNotify{build: s.stateOf, full: true})
}
