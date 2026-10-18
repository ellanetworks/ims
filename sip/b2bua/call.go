// Package b2bua is the core of a back-to-back user agent (RFC 7092 §3.1) on sip/ua: calls made of legs, each an
// INVITE session of the agent, and the rules of relaying one leg to another written once (Relay), for the
// application servers of Ella IMS and the MCPTT functions.
//
// A Call and its legs run on one strand of the agent: their events come in order, and the application acts on any
// leg of the call from them without locks. Their methods are called on that strand: from the handlers, or from a job
// posted to it (Strand.Post).
package b2bua

import (
	"errors"
	"slices"
	"sync"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

var (
	// ErrStrand is the error of a leg joining a call, or a relay, on another strand.
	ErrStrand = errors.New("sip/b2bua: the leg is on another strand")

	// ErrEnded is the error of an operation on a leg, or a call, that ended.
	ErrEnded = errors.New("sip/b2bua: ended")

	// ErrState is the error of an operation the leg does not allow as it stands: answering an outgoing leg, forking
	// one answered.
	ErrState = errors.New("sip/b2bua: not allowed in the state of the leg")
)

// CallHandler receives the events of a call. Each function is optional.
type CallHandler struct {
	// Ended reports the end of the call: its last leg ended, or left it.
	Ended func(c *Call)

	// Error reports an error of the agent the call met while relaying, which it could not report otherwise: a
	// response or a request of a leg that failed.
	Error func(c *Call, err error)
}

// Call is a call of the B2BUA: legs attached and detached over its life, on one strand. The pairings of its legs are
// the application's: a relay of two (Relay), one leg to many, as a controlling function and its members (TS 24.379
// §6.3.3), or a long-lived leg serving a series of calls, as a pre-established session (TS 24.379 §10.1.1.3.1.2).
type Call struct {
	a     *ua.Agent
	st    *ua.Strand
	h     CallHandler
	legs  []*Leg
	ended bool
	done  chan struct{}
	value any
}

// NewCall makes a call on st, a strand of a: the strand of the incoming INVITE that starts it, of a long-lived leg,
// or a new one.
func NewCall(a *ua.Agent, st *ua.Strand, h CallHandler) *Call {
	return &Call{a: a, st: st, h: h, done: make(chan struct{})}
}

// Strand is the strand of the call.
func (c *Call) Strand() *ua.Strand { return c.st }

// Agent is the agent of the call.
func (c *Call) Agent() *ua.Agent { return c.a }

// Legs are the legs of the call, in the order they joined it.
func (c *Call) Legs() []*Leg {
	c.check()

	return append([]*Leg(nil), c.legs...)
}

// Ended reports whether the call ended.
func (c *Call) Ended() bool {
	c.check()

	return c.ended
}

// Done is closed once the call ended; it may be waited for from any goroutine.
func (c *Call) Done() <-chan struct{} { return c.done }

func (c *Call) Value() any {
	c.check()

	return c.value
}

func (c *Call) SetValue(v any) {
	c.check()

	c.value = v
}

// End ends every leg of the call (Leg.End).
func (c *Call) End(rp ua.Reply) {
	c.check()

	for _, l := range c.Legs() {
		c.report(l.End(rp))
	}
}

// check panics unless the caller runs on the strand of the call, when strands are checked (ua.CheckStrands).
func (c *Call) check() {
	if !c.st.Confined() {
		panic("sip/b2bua: a call used off its strand")
	}
}

// legs are the legs of the sessions, for LegOf.
var legs sync.Map

// LegOf is the leg whose dialog s is, if any: an early dialog of it, or its session.
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

// report reports an error of the agent to the application.
func (c *Call) report(err error) {
	if err != nil && c.h.Error != nil {
		c.h.Error(c, err)
	}
}

// Incoming makes the leg of an initial INVITE of the agent, r of Config.OnInvite, whose session it handles from now
// on: OnInvite returns what it likes, ua.Handler{} for one. The call is on the strand of r.
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

// Dial makes the leg of an initial INVITE the agent sends, req as Agent.Invite takes it, on the strand of the call.
func (c *Call) Dial(req *sip.Request, h LegHandler, opts ...ua.Option) (*Leg, error) {
	c.check()

	l, err := c.leg(h)
	if err != nil {
		return nil, err
	}

	return l, l.dial(req, opts...)
}

// leg makes an outgoing leg, to dial.
func (c *Call) leg(h LegHandler) (*Leg, error) {
	if c.ended {
		return nil, ErrEnded
	}

	l := &Leg{call: c, h: h, outgoing: true, state: ua.Early, early: map[*ua.Session]bool{}}
	c.legs = append(c.legs, l)

	return l, nil
}

func (l *Leg) dial(req *sip.Request, opts ...ua.Option) error {
	c := l.call

	o, err := c.a.Invite(req, l.handler(), append([]ua.Option{ua.On(c.st)}, opts...)...)
	if err != nil {
		l.state = ua.Terminated
		l.removed()

		return err
	}

	l.out = o

	return nil
}

// Attach moves l, a leg of another call on the same strand, to c, its events to h: the long-lived leg of a
// pre-established session joining the call it serves next. Its former call ends if it was its last leg. A relay of
// the leg stops.
func (c *Call) Attach(l *Leg, h LegHandler) error {
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

	l.removed()
	l.call = c
	c.legs = append(c.legs, l)

	return nil
}

// remove takes l out of the call, which ends with its last leg: Ended runs as an event of the strand, after what
// removed the leg.
func (c *Call) remove(l *Leg) {
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
		c.st.Post(func() { f(c) })
	}
}

// LegHandler receives the events of a leg: those of ua.Handler for its sessions, with the leg, and the early dialog
// they come from when it has several. Each function is optional. On a leg a Relay relays, the relay acts on each
// event first, then the application's function runs; the decisions are the application's when it has a function for
// them (Request, Bye, Forked), the relay's otherwise (Relay.Forward).
type LegHandler struct {
	// Progress reports a provisional response to an INVITE of the leg, initial or re-INVITE, of the early dialog s:
	// one of several for an initial INVITE that forked.
	Progress func(l *Leg, s *ua.Session, res *sip.Response)

	// Established reports that the session of the leg is confirmed by m: the 2xx (outgoing leg), the ACK (incoming).
	Established func(l *Leg, m sip.Message)

	// Answer reports the answer of the peer to an offer of the leg in s; Offer an offer of the peer in a response,
	// to answer in the PRACK or the ACK (ua.Handler), one a relay does not take; Pracked the PRACK of a reliable 1xx
	// of the leg in s.
	Answer  func(l *Leg, s *ua.Session, sdp []byte)
	Offer   func(l *Leg, s *ua.Session, o *ua.Offer)
	Pracked func(l *Leg, s *ua.Session, res *sip.Response)

	// Request receives a request of the peer, BYE aside, in one of the dialogs of the leg (Request.Session); Bye
	// its BYE, for the application to respond to once the other legs ended (ua.Handler.Bye); Cancelled a re-INVITE
	// of the peer cancelled. Without Request, the agent answers as it does on its own (ua.Request.RespondDefault),
	// or the relay relays it.
	Request   func(l *Leg, r *ua.Request)
	Bye       func(l *Leg, r *ua.Request)
	Cancelled func(l *Leg, r *ua.Request, cancel *sip.Request)

	// Forked decides on a 2xx of another dialog than the one answered first (ua.Handler.Forked): true keeps s, a
	// session of the leg beside its own, which the application ends. Without it, s is ended with a BYE.
	Forked func(l *Leg, s *ua.Session, res *sip.Response) bool

	// Decorate completes each message of the leg, as ua.Handler.Decorate: with the strand held, not calling the agent.
	Decorate func(l *Leg, m sip.Message)

	// Failed reports the failure of the initial INVITE of an outgoing leg: its final response, or its error.
	Failed func(l *Leg, res *sip.Response, err error)

	// EarlyEnded reports the end of one early dialog of the leg while the INVITE goes on in others.
	EarlyEnded func(l *Leg, s *ua.Session, e ua.End)

	// Ended reports the end of the leg, once: its session ended, its initial INVITE failed (after Failed), or, for an
	// incoming leg, its last early dialog ended.
	Ended func(l *Leg, e ua.End)
}

// Leg is one leg of a call: an INVITE session of the agent, with its peer. An incoming leg is made by an initial
// INVITE of the peer, which it may answer in several early dialogs (Fork); an outgoing one by an INVITE of the agent,
// which may fork. Its session is its first early dialog, then the one a 2xx answered or confirmed.
type Leg struct {
	call     *Call
	h        LegHandler
	relay    *Relay
	outgoing bool

	in  *ua.Request
	out *ua.Outgoing
	s   *ua.Session

	// early are the early dialogs of the leg alive; members the request answering each of an incoming leg; owned
	// the sessions of the leg in LegOf.
	early   map[*ua.Session]bool
	members map[*ua.Session]*ua.Request
	owned   []*ua.Session

	state ua.State
	end   ua.End
	value any
}

// Call is the call of the leg.
func (l *Leg) Call() *Call {
	l.call.check()

	return l.call
}

// Outgoing reports whether the agent sent the initial INVITE of the leg.
func (l *Leg) Outgoing() bool { return l.outgoing }

// Request is the initial INVITE of an incoming leg, for the application to respond to, as answered in its first
// early dialog; Member is the one of another.
func (l *Leg) Request() *ua.Request { return l.in }

// Member is the request answering the initial INVITE of an incoming leg in its early dialog s.
func (l *Leg) Member(s *ua.Session) *ua.Request {
	l.call.check()

	return l.members[s]
}

// Invite is the initial INVITE of an outgoing leg.
func (l *Leg) Invite() *ua.Outgoing { return l.out }

// Session is the session of the leg: the dialog a 2xx answered or confirmed, otherwise its first early dialog; nil
// for an outgoing leg before a response created a dialog.
func (l *Leg) Session() *ua.Session {
	l.call.check()

	if !l.outgoing && l.state == ua.Early {
		if s := l.in.Chosen(); s != nil {
			return s
		}
	}

	return l.s
}

// Early are the early dialogs of the leg alive.
func (l *Leg) Early() []*ua.Session {
	l.call.check()

	out := make([]*ua.Session, 0, len(l.early))
	for s := range l.early {
		out = append(out, s)
	}

	return out
}

// State is the state of the leg: Early until its session is confirmed, Terminated once it ended.
func (l *Leg) State() ua.State {
	l.call.check()

	return l.state
}

// EndedBy is how the leg ended, once it did.
func (l *Leg) EndedBy() ua.End {
	l.call.check()

	return l.end
}

// Relay is the relay of the leg, if any.
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

// SetHandler sets the application's handler of the leg's events.
func (l *Leg) SetHandler(h LegHandler) {
	l.call.check()

	l.h = h
}

// Fork answers the initial INVITE of an incoming leg in another early dialog (ua.Request.Fork): to mirror one of the
// early dialogs of another leg.
func (l *Leg) Fork() (*ua.Request, error) {
	l.call.check()

	if l.outgoing || l.state != ua.Early {
		return nil, ErrState
	}

	// Any member of the INVITE still in its early dialog forks it.
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

// Respond answers the initial INVITE of an incoming leg with a final response other than 2xx, in whichever of its
// early dialogs is left; a 2xx goes in one, with Member(s).Respond.
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

// End ends the leg however it stands: an incoming INVITE not answered gets rp (480 without a code), an outgoing one
// is cancelled, a session, answered or confirmed, ends with a BYE; the header fields of rp, as a Reason (RFC 3326),
// go in the CANCEL or the BYE.
func (l *Leg) End(rp ua.Reply) error {
	l.call.check()

	if l.state == ua.Terminated {
		return nil
	}

	c := ua.Change{Header: rp.Header}

	switch s := l.Session(); {
	case s != nil && s.State() == ua.Confirmed:
		return s.Bye(c, nil)
	case !l.outgoing && l.in.Chosen() != nil:
		// Answered, its ACK or PRACK not in yet: the BYE waits for the ACK, a 2xx held for a PRACK gives way.
		return s.Bye(c, nil)
	case !l.outgoing:
		if rp.Code == 0 {
			rp.Code = 480
		}

		return l.Respond(rp)
	case l.out != nil:
		return l.out.Cancel(c)
	}

	return nil
}

// handler is the ua.Handler of the leg's sessions.
func (l *Leg) handler() ua.Handler {
	return ua.Handler{
		Progress: func(s *ua.Session, res *sip.Response) {
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
				l.relay.progress(l, s, res)
			}

			if f := l.h.Progress; f != nil {
				f(l, s, res)
			}
		},
		Established: func(s *ua.Session, m sip.Message) {
			if l.state != ua.Early {
				return
			}

			l.s = s
			l.state = ua.Confirmed
			l.own(s)

			if l.relay != nil {
				l.relay.established(l, m)
			}

			if f := l.h.Established; f != nil {
				f(l, m)
			}
		},
		Answer: func(s *ua.Session, sdp []byte) {
			if !l.mine(s) {
				return
			}

			if l.relay != nil {
				l.relay.answer(l, s, sdp)
			}

			if f := l.h.Answer; f != nil {
				f(l, s, sdp)
			}
		},
		Offer: func(s *ua.Session, o *ua.Offer) {
			switch {
			case !l.mine(s):
				o.Reject()
			case l.relay != nil && l.relay.offer(l, s, o):
			case l.h.Offer != nil:
				l.h.Offer(l, s, o)
			default:
				o.Reject()
			}
		},
		Pracked: func(s *ua.Session, res *sip.Response) {
			if f := l.h.Pracked; f != nil && l.mine(s) {
				f(l, s, res)
			}
		},
		Request: func(s *ua.Session, r *ua.Request) {
			switch {
			case !l.mine(s):
				_ = r.Reject(481)
			case l.h.Request != nil:
				l.h.Request(l, r)
			case l.relay != nil:
				l.relay.Forward(l, r)
			default:
				l.call.report(r.RespondDefault())
			}
		},
		Bye: func(s *ua.Session, r *ua.Request) {
			switch {
			case l.h.Bye != nil && l.mine(s):
				l.h.Bye(l, r)
			case l.relay != nil && l.mine(s):
				l.relay.Forward(l, r)
			default:
				_ = r.Respond(ua.Reply{Code: 200})
			}
		},
		Cancelled: func(s *ua.Session, r *ua.Request, cancel *sip.Request) {
			if !l.mine(s) {
				return
			}

			if l.relay != nil {
				l.relay.cancelled(l, r, cancel)
			}

			if f := l.h.Cancelled; f != nil {
				f(l, r, cancel)
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
		Forked: func(s *ua.Session, res *sip.Response) bool {
			if f := l.h.Forked; f != nil && l.state != ua.Terminated {
				return f(l, s, res)
			}

			return false
		},
		Failed: func(_ *ua.Outgoing, res *sip.Response, err error) {
			if l.state == ua.Terminated {
				return
			}

			if l.relay != nil {
				l.relay.failed(l, res, err)
			}

			if f := l.h.Failed; f != nil {
				f(l, res, err)
			}

			l.ended(failure(res, err))
		},
		Ended: func(s *ua.Session, e ua.End) {
			if l.state == ua.Terminated {
				return
			}

			// Confirmed, the leg ends with its session; a forked session kept, or a late early dialog, without it.
			if l.state == ua.Confirmed {
				if s == l.s {
					l.ended(e)
				}

				return
			}

			if !l.early[s] {
				return
			}

			delete(l.early, s)

			// Early, an outgoing leg ends with its INVITE (Failed), an incoming one with its last early dialog.
			if l.outgoing || len(l.early) > 0 {
				if l.relay != nil {
					l.relay.earlyEnded(l, s, e)
				}

				if f := l.h.EarlyEnded; f != nil {
					f(l, s, e)
				}

				return
			}

			l.ended(e)
		},
	}
}

// mine reports whether s is a session of the leg.
func (l *Leg) mine(s *ua.Session) bool {
	return l.state != ua.Terminated && (s == l.s || l.early[s] || l.s == nil)
}

func (l *Leg) ended(e ua.End) {
	if l.state == ua.Terminated {
		return
	}

	l.state = ua.Terminated
	l.end = e

	l.disown()

	if l.relay != nil {
		l.relay.ended(l, e)
	}

	if f := l.h.Ended; f != nil {
		f(l, e)
	}

	l.removed()
}

func (l *Leg) removed() {
	l.call.remove(l)
}

// disown takes the sessions of the leg out of LegOf, as it ends.
func (l *Leg) disown() {
	for _, s := range l.owned {
		legs.CompareAndDelete(s, l)
	}

	l.owned = nil
}

// failure is the end of an outgoing leg whose INVITE failed.
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
