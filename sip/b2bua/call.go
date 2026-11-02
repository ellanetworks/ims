package b2bua

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

var (
	ErrStrand = errors.New("sip/b2bua: the leg is on another strand")

	ErrEnded = errors.New("sip/b2bua: ended")

	ErrState = errors.New("sip/b2bua: not allowed in the state of the leg")
)

type CallHandler struct {
	Ended func(ctx context.Context, c *Call)

	Error func(c *Call, err error)
}

type Call struct {
	a     *ua.Agent
	st    *ua.Strand
	h     CallHandler
	legs  []*Leg
	ended bool
	done  chan struct{}
	value any
}

func NewCall(a *ua.Agent, st *ua.Strand, h CallHandler) *Call {
	return &Call{a: a, st: st, h: h, done: make(chan struct{})}
}

func (c *Call) Strand() *ua.Strand { return c.st }

func (c *Call) Agent() *ua.Agent { return c.a }

func (c *Call) Legs() []*Leg {
	c.check()

	return append([]*Leg(nil), c.legs...)
}

func (c *Call) Ended() bool {
	c.check()

	return c.ended
}

func (c *Call) Done() <-chan struct{} { return c.done }

func (c *Call) Value() any {
	c.check()

	return c.value
}

func (c *Call) SetValue(v any) {
	c.check()

	c.value = v
}

func (c *Call) End(ctx context.Context, rp ua.Reply) {
	c.check()

	for _, l := range c.Legs() {
		c.report(l.End(ctx, rp))
	}
}

func (c *Call) check() {
	if !c.st.Confined() {
		panic("sip/b2bua: a call used off its strand")
	}
}

var legs sync.Map

func LegOf(s *ua.Session) *Leg {
	if l, ok := legs.Load(s); ok {
		return l.(*Leg)
	}

	return nil
}

func (l *Leg) own(s *ua.Session) {
	if s != nil && !slices.Contains(l.owned, s) {
		l.owned = append(l.owned, s)
		legs.Store(s, l)
	}
}

func (c *Call) report(err error) {
	if err != nil && c.h.Error != nil {
		c.h.Error(c, err)
	}
}

func (c *Call) Incoming(r *ua.Request, h LegHandler) (*Leg, error) {
	c.check()

	if r.Strand() != c.st {
		return nil, ErrStrand
	}

	if c.ended {
		return nil, ErrEnded
	}

	s := r.Session()
	l := &Leg{
		call: c, h: h, in: r, s: s, state: ua.Early,
		early: map[*ua.Session]bool{s: true}, members: map[*ua.Session]*ua.Request{s: r},
	}
	c.legs = append(c.legs, l)
	l.own(s)

	s.SetHandler(l.handler())

	return l, nil
}

func (c *Call) Dial(ctx context.Context, req *sip.Request, h LegHandler, opts ...ua.Option) (*Leg, error) {
	c.check()

	l, err := c.leg(h)
	if err != nil {
		return nil, err
	}

	return l, l.dial(ctx, req, opts...)
}

func (c *Call) leg(h LegHandler) (*Leg, error) {
	if c.ended {
		return nil, ErrEnded
	}

	l := &Leg{call: c, h: h, outgoing: true, state: ua.Early, early: map[*ua.Session]bool{}}
	c.legs = append(c.legs, l)

	return l, nil
}

func (l *Leg) dial(ctx context.Context, req *sip.Request, opts ...ua.Option) error {
	c := l.call

	o, err := c.a.Invite(ctx, req, l.handler(), append([]ua.Option{ua.On(c.st)}, opts...)...)
	if err != nil {
		l.state = ua.Terminated
		l.removed(ctx)

		return err
	}

	l.out = o

	return nil
}

func (c *Call) Attach(ctx context.Context, l *Leg, h LegHandler) error {
	c.check()

	switch {
	case l.call.st != c.st:
		return ErrStrand
	case c.ended, l.state == ua.Terminated:
		return ErrEnded
	}

	if l.relay != nil {
		l.relay.Stop()
	}

	l.h = h

	if l.call == c {
		return nil
	}

	l.removed(ctx)
	l.call = c
	c.legs = append(c.legs, l)

	return nil
}

func (c *Call) remove(ctx context.Context, l *Leg) {
	for i, x := range c.legs {
		if x == l {
			c.legs = append(c.legs[:i], c.legs[i+1:]...)
			break
		}
	}

	if len(c.legs) > 0 || c.ended {
		return
	}

	c.ended = true
	close(c.done)

	if f := c.h.Ended; f != nil {
		c.st.Post(func() { f(ctx, c) })
	}
}

type LegHandler struct {
	Progress func(ctx context.Context, l *Leg, s *ua.Session, res *sip.Response)

	Established func(ctx context.Context, l *Leg, m sip.Message)

	Answer  func(ctx context.Context, l *Leg, s *ua.Session, sdp []byte)
	Offer   func(ctx context.Context, l *Leg, s *ua.Session, o *ua.Offer)
	Pracked func(ctx context.Context, l *Leg, s *ua.Session, res *sip.Response)

	Request   func(ctx context.Context, l *Leg, r *ua.Request)
	Bye       func(ctx context.Context, l *Leg, r *ua.Request)
	Cancelled func(ctx context.Context, l *Leg, r *ua.Request, cancel *sip.Request)

	Forked func(ctx context.Context, l *Leg, s *ua.Session, res *sip.Response) bool

	Decorate func(l *Leg, m sip.Message)

	Failed func(ctx context.Context, l *Leg, res *sip.Response, err error)

	EarlyEnded func(ctx context.Context, l *Leg, s *ua.Session, e ua.End)

	Ended func(ctx context.Context, l *Leg, e ua.End)
}

type Leg struct {
	call     *Call
	h        LegHandler
	relay    *Relay
	outgoing bool

	in  *ua.Request
	out *ua.Outgoing
	s   *ua.Session

	early   map[*ua.Session]bool
	members map[*ua.Session]*ua.Request
	owned   []*ua.Session

	state ua.State
	end   ua.End
	value any
}

func (l *Leg) Call() *Call {
	l.call.check()

	return l.call
}

func (l *Leg) Outgoing() bool { return l.outgoing }

func (l *Leg) Request() *ua.Request { return l.in }

func (l *Leg) Member(s *ua.Session) *ua.Request {
	l.call.check()

	return l.members[s]
}

func (l *Leg) Invite() *ua.Outgoing { return l.out }

func (l *Leg) Session() *ua.Session {
	l.call.check()

	if !l.outgoing && l.state == ua.Early {
		if s := l.in.Chosen(); s != nil {
			return s
		}
	}

	return l.s
}

func (l *Leg) Early() []*ua.Session {
	l.call.check()

	out := make([]*ua.Session, 0, len(l.early))
	for s := range l.early {
		out = append(out, s)
	}

	return out
}

func (l *Leg) State() ua.State {
	l.call.check()

	return l.state
}

func (l *Leg) EndedBy() ua.End {
	l.call.check()

	return l.end
}

func (l *Leg) Relay() *Relay {
	l.call.check()

	return l.relay
}

func (l *Leg) Value() any {
	l.call.check()

	return l.value
}

func (l *Leg) SetValue(v any) {
	l.call.check()

	l.value = v
}

func (l *Leg) SetHandler(h LegHandler) {
	l.call.check()

	l.h = h
}

func (l *Leg) Fork() (*ua.Request, error) {
	l.call.check()

	if l.outgoing || l.state != ua.Early {
		return nil, ErrState
	}

	failed := ErrState

	for s, m := range l.members {
		if !l.early[s] {
			continue
		}

		f, err := m.Fork(l.handler())
		if err != nil {
			failed = err
			continue
		}

		l.early[f.Session()] = true
		l.members[f.Session()] = f
		l.own(f.Session())

		return f, nil
	}

	return nil, failed
}

func (l *Leg) Respond(rp ua.Reply) error {
	l.call.check()

	if l.outgoing {
		return ErrState
	}

	err := l.in.Respond(rp)
	if !errors.Is(err, transaction.ErrFinalResponseSent) {
		return err
	}

	for s, m := range l.members {
		if m == l.in || !l.early[s] {
			continue
		}

		if err = m.Respond(rp); !errors.Is(err, transaction.ErrFinalResponseSent) {
			return err
		}
	}

	return err
}

func (l *Leg) End(ctx context.Context, rp ua.Reply) error {
	l.call.check()

	if l.state == ua.Terminated {
		return nil
	}

	c := ua.Change{Header: rp.Header}

	switch s := l.Session(); {
	case s != nil && s.State() == ua.Confirmed:
		return s.Bye(ctx, c, nil)
	case !l.outgoing && l.in.Chosen() != nil:
		return s.Bye(ctx, c, nil)
	case !l.outgoing:
		if rp.Code == 0 {
			rp.Code = 480
		}

		return l.Respond(rp)
	case l.out != nil:
		return l.out.Cancel(ctx, c)
	}

	return nil
}

func (l *Leg) handler() ua.Handler {
	return ua.Handler{
		Progress: func(ctx context.Context, s *ua.Session, res *sip.Response) {
			if l.state != ua.Early {
				return
			}

			if s != nil && l.outgoing && !l.early[s] && res.StatusCode != 199 {
				l.early[s] = true
				l.own(s)

				if l.s == nil {
					l.s = s
				}
			}

			if l.relay != nil {
				l.relay.progress(ctx, l, s, res)
			}

			if f := l.h.Progress; f != nil {
				f(ctx, l, s, res)
			}
		},
		Established: func(ctx context.Context, s *ua.Session, m sip.Message) {
			if l.state != ua.Early {
				return
			}

			l.s = s
			l.state = ua.Confirmed
			l.own(s)

			if l.relay != nil {
				l.relay.established(ctx, l, m)
			}

			if f := l.h.Established; f != nil {
				f(ctx, l, m)
			}
		},
		Answer: func(ctx context.Context, s *ua.Session, sdp []byte) {
			if !l.mine(s) {
				return
			}

			if l.relay != nil {
				l.relay.answer(ctx, l, s, sdp)
			}

			if f := l.h.Answer; f != nil {
				f(ctx, l, s, sdp)
			}
		},
		Offer: func(ctx context.Context, s *ua.Session, o *ua.Offer) {
			switch {
			case !l.mine(s):
				o.Reject(ctx)
			case l.relay != nil && l.relay.offer(ctx, l, s, o):
			case l.h.Offer != nil:
				l.h.Offer(ctx, l, s, o)
			default:
				o.Reject(ctx)
			}
		},
		Pracked: func(ctx context.Context, s *ua.Session, res *sip.Response) {
			if f := l.h.Pracked; f != nil && l.mine(s) {
				f(ctx, l, s, res)
			}
		},
		Request: func(ctx context.Context, s *ua.Session, r *ua.Request) {
			switch {
			case !l.mine(s):
				_ = r.Reject(481)
			case l.h.Request != nil:
				l.h.Request(ctx, l, r)
			case l.relay != nil:
				l.relay.Forward(ctx, l, r)
			default:
				l.call.report(r.RespondDefault())
			}
		},
		Bye: func(ctx context.Context, s *ua.Session, r *ua.Request) {
			switch {
			case l.h.Bye != nil && l.mine(s):
				l.h.Bye(ctx, l, r)
			case l.relay != nil && l.mine(s):
				l.relay.Forward(ctx, l, r)
			default:
				_ = r.Respond(ua.Reply{Code: 200})
			}
		},
		Cancelled: func(ctx context.Context, s *ua.Session, r *ua.Request, cancel *sip.Request) {
			if !l.mine(s) {
				return
			}

			if l.relay != nil {
				l.relay.cancelled(ctx, l, r, cancel)
			}

			if f := l.h.Cancelled; f != nil {
				f(ctx, l, r, cancel)
			}
		},
		Decorate: func(_ *ua.Session, m sip.Message) {
			if l.relay != nil {
				l.relay.decorate(l, m)
			}

			if f := l.h.Decorate; f != nil {
				f(l, m)
			}
		},
		Forked: func(ctx context.Context, s *ua.Session, res *sip.Response) bool {
			if f := l.h.Forked; f != nil && l.state != ua.Terminated {
				return f(ctx, l, s, res)
			}

			return false
		},
		Failed: func(ctx context.Context, _ *ua.Outgoing, res *sip.Response, err error) {
			if l.state == ua.Terminated {
				return
			}

			if l.relay != nil {
				l.relay.failed(ctx, l, res, err)
			}

			if f := l.h.Failed; f != nil {
				f(ctx, l, res, err)
			}

			l.ended(ctx, failure(res, err))
		},
		Ended: func(ctx context.Context, s *ua.Session, e ua.End) {
			if l.state == ua.Terminated {
				return
			}

			if l.state == ua.Confirmed {
				if s == l.s {
					l.ended(ctx, e)
				}

				return
			}

			if !l.early[s] {
				return
			}

			delete(l.early, s)

			if l.outgoing || len(l.early) > 0 {
				if l.relay != nil {
					l.relay.earlyEnded(ctx, l, s, e)
				}

				if f := l.h.EarlyEnded; f != nil {
					f(ctx, l, s, e)
				}

				return
			}

			l.ended(ctx, e)
		},
	}
}

func (l *Leg) mine(s *ua.Session) bool {
	return l.state != ua.Terminated && (s == l.s || l.early[s] || l.s == nil)
}

func (l *Leg) ended(ctx context.Context, e ua.End) {
	if l.state == ua.Terminated {
		return
	}

	l.state = ua.Terminated
	l.end = e

	l.disown()

	if l.relay != nil {
		l.relay.ended(ctx, l, e)
	}

	if f := l.h.Ended; f != nil {
		f(ctx, l, e)
	}

	l.removed(ctx)
}

func (l *Leg) removed(ctx context.Context) {
	l.call.remove(ctx, l)
}

func (l *Leg) disown() {
	for _, s := range l.owned {
		legs.CompareAndDelete(s, l)
	}

	l.owned = nil
}

func failure(res *sip.Response, err error) ua.End {
	switch {
	case res != nil && res.StatusCode == 487:
		return ua.End{Cause: ua.EndCancelled, Remote: true, Response: res}
	case res != nil:
		return ua.End{Cause: ua.EndRejected, Remote: true, Response: res}
	case errors.Is(err, ua.ErrClosed), errors.Is(err, transaction.ErrClosed):
		return ua.End{Cause: ua.EndClosed, Err: err}
	case errors.Is(err, transaction.ErrTimeout):
		return ua.End{Cause: ua.EndTimeout, Err: err}
	}

	return ua.End{Cause: ua.EndLost, Err: err}
}
