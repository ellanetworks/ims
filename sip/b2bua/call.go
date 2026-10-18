// Package b2bua is the core of a back-to-back user agent (RFC 7092 §3.1) on sip/ua: calls made of legs, each an
// INVITE session of the agent, and the rules of relaying one leg to another written once (Relay, Bridge), for the
// application servers of Ella IMS and the MCPTT functions.
//
// A Call and its legs run on one strand of the agent: their events come in order, and the application acts on any
// leg of the call from them without locks. Their methods are called on that strand: from the handlers, or from a job
// posted to it (Strand.Post).
package b2bua

import (
	"errors"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// CallHandler receives the events of a call.
type CallHandler struct {
	// Ended reports the end of the call: its last leg ended, or left it.
	Ended func(c *Call)
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
	value any
}

// NewCall makes a call on st, a strand of a: the strand of the incoming INVITE that starts it, of a long-lived leg,
// or a new one.
func NewCall(a *ua.Agent, st *ua.Strand, h CallHandler) *Call {
	return &Call{a: a, st: st, h: h}
}

// Strand is the strand of the call.
func (c *Call) Strand() *ua.Strand { return c.st }

// Agent is the agent of the call.
func (c *Call) Agent() *ua.Agent { return c.a }

// Legs are the legs of the call, in the order they joined it.
func (c *Call) Legs() []*Leg { return append([]*Leg(nil), c.legs...) }

// Ended reports whether the call ended.
func (c *Call) Ended() bool { return c.ended }

func (c *Call) Value() any     { return c.value }
func (c *Call) SetValue(v any) { c.value = v }

// End ends every leg of the call (Leg.End).
func (c *Call) End(rp ua.Reply) {
	for _, l := range c.Legs() {
		_ = l.End(rp)
	}
}

// ErrStrand is the error of a leg joining a call on another strand.
var ErrStrand = errors.New("sip/b2bua: the leg is on another strand")

// ErrEnded is the error of an operation on a leg, or a call, that ended.
var ErrEnded = errors.New("sip/b2bua: ended")

// Incoming makes the leg of an initial INVITE of the agent, r of Config.OnInvite, which returns l.Handler(). The
// call is on the strand of r.
func (c *Call) Incoming(r *ua.Request, h LegHandler) (*Leg, error) {
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

	return l, nil
}

// Dial makes the leg of an initial INVITE the agent sends, req as Agent.Invite takes it, on the strand of the call.
func (c *Call) Dial(req *sip.Request, h LegHandler, opts ...ua.Option) (*Leg, error) {
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

	o, err := c.a.Invite(req, l.Handler(), append([]ua.Option{ua.On(c.st)}, opts...)...)
	if err != nil {
		l.state = ua.Terminated
		l.removed()

		return err
	}

	l.out = o

	return nil
}

// Attach moves l, a leg of another call on the same strand, to c, its events to h: the long-lived leg of a
// pre-established session joining the call it serves next. Its former call ends if it was its last leg.
func (c *Call) Attach(l *Leg, h LegHandler) error {
	switch {
	case l.call.st != c.st:
		return ErrStrand
	case c.ended, l.state == ua.Terminated:
		return ErrEnded
	case l.call == c:
		l.h = h
		return nil
	}

	l.removed()
	l.call, l.h = c, h
	c.legs = append(c.legs, l)

	return nil
}

func (c *Call) remove(l *Leg) {
	for i, x := range c.legs {
		if x == l {
			c.legs = append(c.legs[:i], c.legs[i+1:]...)
			break
		}
	}

	if len(c.legs) == 0 && !c.ended {
		c.ended = true

		if c.h.Ended != nil {
			c.h.Ended(c)
		}
	}
}

// LegHandler receives the events of a leg: those of ua.Handler for its sessions, with the leg, and the early dialog
// they come from when it has several. Each function is optional.
type LegHandler struct {
	// Progress reports a provisional response to an INVITE of the leg, initial or re-INVITE, of the early dialog s:
	// one of several for an initial INVITE that forked.
	Progress func(l *Leg, s *ua.Session, res *sip.Response)

	// Established reports that the session of the leg is confirmed by m: the 2xx (outgoing leg), the ACK (incoming).
	Established func(l *Leg, m sip.Message)

	// Answer reports the answer of the peer to an offer of the leg in s; Offer an offer of the peer in a response,
	// to answer in the PRACK or the ACK (ua.Handler); Pracked the PRACK of a reliable 1xx of the leg in s.
	Answer  func(l *Leg, s *ua.Session, sdp []byte)
	Offer   func(l *Leg, s *ua.Session, o *ua.Offer)
	Pracked func(l *Leg, s *ua.Session, res *sip.Response)

	// Request receives a request of the peer, BYE aside, in one of the dialogs of the leg (Request.Session); Bye
	// its BYE, for the application to respond to once the other legs ended (ua.Handler.Bye); Cancelled a re-INVITE
	// of the peer cancelled.
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
// which may fork. Its session is its first early dialog, then the one a 2xx confirmed.
type Leg struct {
	call     *Call
	h        LegHandler
	outgoing bool

	in  *ua.Request
	out *ua.Outgoing
	s   *ua.Session

	// early are the early dialogs of the leg alive; members the request answering each of an incoming leg.
	early   map[*ua.Session]bool
	members map[*ua.Session]*ua.Request

	state ua.State
	end   ua.End
	value any
}

// Call is the call of the leg.
func (l *Leg) Call() *Call { return l.call }

// Outgoing reports whether the agent sent the initial INVITE of the leg.
func (l *Leg) Outgoing() bool { return l.outgoing }

// Request is the initial INVITE of an incoming leg, for the application to respond to: as answered in its first
// early dialog. Member is the one of another.
func (l *Leg) Request() *ua.Request { return l.in }

// Member is the request answering the initial INVITE of an incoming leg in its early dialog s.
func (l *Leg) Member(s *ua.Session) *ua.Request { return l.members[s] }

// Invite is the initial INVITE of an outgoing leg.
func (l *Leg) Invite() *ua.Outgoing { return l.out }

// Session is the session of the leg: nil for an outgoing leg before a response created a dialog.
func (l *Leg) Session() *ua.Session { return l.s }

// Early are the early dialogs of the leg alive.
func (l *Leg) Early() []*ua.Session {
	out := make([]*ua.Session, 0, len(l.early))
	for s := range l.early {
		out = append(out, s)
	}

	return out
}

// State is the state of the leg: Early until its session is confirmed, Terminated once it ended.
func (l *Leg) State() ua.State { return l.state }

// EndedBy is how the leg ended, once it did.
func (l *Leg) EndedBy() ua.End { return l.end }

func (l *Leg) Value() any     { return l.value }
func (l *Leg) SetValue(v any) { l.value = v }

// SetHandler sets the handler of the leg's events.
func (l *Leg) SetHandler(h LegHandler) { l.h = h }

// Fork answers the initial INVITE of an incoming leg in another early dialog (ua.Request.Fork): to mirror one of the
// early dialogs of another leg.
func (l *Leg) Fork() (*ua.Request, error) {
	if l.outgoing || l.state != ua.Early {
		return nil, ErrEnded
	}

	f, err := l.in.Fork(l.Handler())
	if err != nil {
		return nil, err
	}

	l.early[f.Session()] = true
	l.members[f.Session()] = f

	return f, nil
}

// Respond answers the initial INVITE of an incoming leg with a final response other than 2xx, in whichever of its
// early dialogs is left; a 2xx goes in one, with Member(s).Respond.
func (l *Leg) Respond(rp ua.Reply) error {
	if l.outgoing {
		return ErrEnded
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
// is cancelled, a session is ended with a BYE; the header fields of rp, as a Reason (RFC 3326), go in the CANCEL or
// the BYE.
func (l *Leg) End(rp ua.Reply) error {
	if l.state == ua.Terminated {
		return nil
	}

	c := ua.Change{Header: rp.Header}

	switch {
	case l.s != nil && l.s.State() == ua.Confirmed:
		return l.s.Bye(c, nil)
	case !l.outgoing:
		if rp.Code == 0 {
			rp.Code = 480
		}

		err := l.Respond(rp)
		if errors.Is(err, transaction.ErrFinalResponseSent) && l.s != nil {
			// Answered, the ACK not yet in: the BYE waits for it.
			return l.s.Bye(c, nil)
		}

		return err
	case l.out != nil:
		return l.out.Cancel(c)
	}

	return nil
}

// Handler is the ua.Handler of the leg's sessions: the one Config.OnInvite returns for an incoming leg.
func (l *Leg) Handler() ua.Handler {
	return ua.Handler{
		Progress: func(s *ua.Session, res *sip.Response) {
			if l.state != ua.Early {
				return
			}

			if s != nil && l.outgoing && !l.early[s] && res.StatusCode != 199 {
				l.early[s] = true

				if l.s == nil {
					l.s = s
				}
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

			if f := l.h.Established; f != nil {
				f(l, m)
			}
		},
		Answer: func(s *ua.Session, sdp []byte) {
			if f := l.h.Answer; f != nil && l.mine(s) {
				f(l, s, sdp)
			}
		},
		Offer: func(s *ua.Session, o *ua.Offer) {
			if f := l.h.Offer; f != nil && l.mine(s) {
				f(l, s, o)
				return
			}

			o.Reject()
		},
		Pracked: func(s *ua.Session, res *sip.Response) {
			if f := l.h.Pracked; f != nil && l.mine(s) {
				f(l, s, res)
			}
		},
		Request: func(s *ua.Session, r *ua.Request) {
			if f := l.h.Request; f != nil && l.mine(s) {
				f(l, r)
				return
			}

			_ = r.Reject(500)
		},
		Bye: func(s *ua.Session, r *ua.Request) {
			if f := l.h.Bye; f != nil && l.mine(s) {
				f(l, r)
				return
			}

			_ = r.Respond(ua.Reply{Code: 200})
		},
		Cancelled: func(s *ua.Session, r *ua.Request, cancel *sip.Request) {
			if f := l.h.Cancelled; f != nil && l.mine(s) {
				f(l, r, cancel)
			}
		},
		Decorate: func(_ *ua.Session, m sip.Message) {
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

	if f := l.h.Ended; f != nil {
		f(l, e)
	}

	l.removed()
}

func (l *Leg) removed() {
	l.call.remove(l)
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
