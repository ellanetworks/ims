package ua

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/sip"
)

// DefaultReferExpires is the duration of the implicit subscription of a REFER the application accepts without one of
// its own: longer than an INVITE takes to complete, Timer C (RFC 3515 §3.4, RFC 3261 §16.6).
const DefaultReferExpires = 4 * time.Minute

// Refer sends a REFER outside a dialog (RFC 3515), and returns the implicit subscription its NOTIFYs report the
// referred request on, from their message/sipfrag bodies (§2.4.4). RFC 7647 §4 makes such a REFER the way to refer
// within an existing dialog too: req names it with a Target-Dialog (RFC 4538), as TS 24.379 §6.2.4.2 does. req
// carries the Request-URI, To, Refer-To, the Route, and the header fields of the application: Refer-Sub: false with
// norefersub to ask for no subscription (RFC 4488), Require: explicitsub for none at all (RFC 7614). When the 2xx
// says there is none (Refer-Sub: false, or explicitsub), the subscription ends at once with EndNoSubscription, after
// Accepted. A REFER within a dialog that cannot create a subscription, with Require: explicitsub, goes with
// Session.Send.
func (a *Agent) Refer(req *sip.Request, h SubscriptionHandler, opts ...Option) (*Subscription, error) {
	if req.Method != "REFER" || !req.Header.Has("Refer-To") {
		return nil, errors.New("sip/ua: Refer of a REFER with a Refer-To")
	}

	tmpl := req.Clone()
	if err := a.completeInitial(tmpl); err != nil {
		return nil, err
	}

	if !tmpl.Header.Has("Contact") {
		if a.cfg.Contact.URI.Scheme == "" {
			return nil, errors.New("sip/ua: no Contact")
		}

		tmpl.Header.Set("Contact", a.cfg.Contact.String())
	}

	cseq, _ := tmpl.Header.CSeq()
	from, _ := tmpl.Header.From()

	// The SUBSCRIBE of the refreshes and of an unsubscribe of the implicit subscription.
	sub := tmpl.Clone()
	sub.Method = "SUBSCRIBE"

	for _, name := range []string{"Refer-To", "Refer-Sub", "Referred-By", "Target-Dialog", "Require", "Supported"} {
		sub.Header.Del(name)
	}

	sub.Header.Set("Accept", "message/sipfrag")

	event := sip.Event{Package: "refer", ID: strconv.FormatUint(uint64(cseq.Seq), 10)}

	st := a.options(opts).strand(a)
	att := &subscribing{
		a: a, st: st, key: subscribeKey{callID: tmpl.Header.CallID(), fromTag: from.Tag()},
		tmpl: tmpl, sub: sub, refer: true, subs: map[string]*Subscription{},
	}
	s := &Subscription{a: a, st: st, sh: h, att: att, event: event}
	att.first = s

	if a.isClosed() {
		return nil, ErrClosed
	}

	a.addSubscribing(att)

	if err := st.do(att.send); err != nil {
		a.removeSubscribing(att)
		return nil, err
	}

	return s, nil
}

// noSubscription reports whether the 2xx to a REFER says it created no subscription (RFC 4488 §4, RFC 7614 §4.2).
func noSubscription(refer *sip.Request, res *sip.Response) bool {
	return strings.EqualFold(strings.TrimSpace(res.Header.Get("Refer-Sub")), "false") ||
		refer.Header.HasOptionTag("Require", sip.OptionTagExplicitSub)
}

// Sipfrag is a message/sipfrag body of a status line (RFC 3420), for the NOTIFYs of a REFER (RFC 3515 §2.4.5).
func Sipfrag(code int, reason string) sip.BodyPart {
	if reason == "" {
		reason = sip.ReasonPhrase(code)
	}

	return sip.NewBodyPart("message/sipfrag", []byte(fmt.Sprintf("SIP/2.0 %d %s\r\n", code, reason)))
}

// ReferAcceptance is how the application accepts a REFER, with Request.AcceptRefer.
type ReferAcceptance struct {
	// NoSubscription accepts a REFER that asked for none, with Refer-Sub: false (RFC 4488 §4), without one: the
	// 200 says Refer-Sub: false. A REFER that did not ask gets its subscription.
	NoSubscription bool

	// EventsAt is the URI of the refer event state of a REFER that requires explicitsub (RFC 7614 §4.3), sent in
	// Refer-Events-At: the REFER creates no subscription, its sender subscribing there, through OnRequest. It must be
	// hard to guess, as a temporary GRUU.
	EventsAt string

	// Expires is the duration of the implicit subscription (RFC 3515 §3.4); DefaultReferExpires without one.
	Expires time.Duration

	Handler ServerSubscriptionHandler

	// Contact is the Contact of the agent in a dialog the REFER creates (Acceptance.Contact).
	Contact string
}

// AcceptRefer accepts a REFER with a 200 (never a 202, RFC 7647 §5). Unless o says otherwise, the REFER creates an
// implicit subscription to its refer event (RFC 3515 §2.4.4): in the dialog of the session for a REFER within it,
// with the id of its CSeq (§2.4.6), or in a dialog of its own; its first NOTIFY, at once, reports 100 Trying, and
// ServerSubscription.ReferStatus the progress of the referred request, its final status terminating the subscription
// (§2.4.7). s is nil without a subscription.
func (r *Request) AcceptRefer(o ReferAcceptance) (s *ServerSubscription, err error) {
	if r.req.Method != "REFER" {
		return nil, errors.New("sip/ua: AcceptRefer of a " + r.req.Method)
	}

	err = r.st.do(func() error {
		if r.final {
			return &Error{Op: "AcceptRefer", State: Terminated, Err: ErrState}
		}

		switch {
		case r.req.Header.HasOptionTag("Require", sip.OptionTagExplicitSub):
			if o.EventsAt == "" {
				return errors.New("sip/ua: a REFER requiring explicitsub needs ReferAcceptance.EventsAt")
			}

			return r.respond(Reply{Code: 200, Header: sip.Header{{Name: "Refer-Events-At", Value: "<" + o.EventsAt + ">"}}})
		case o.NoSubscription:
			if !strings.EqualFold(strings.TrimSpace(r.req.Header.Get("Refer-Sub")), "false") {
				return errors.New("sip/ua: the REFER asked for its subscription")
			}

			return r.respond(Reply{Code: 200, Header: sip.Header{{Name: "Refer-Sub", Value: "false"}}})
		}

		var err error

		s, err = r.referSubscription(o)

		return err
	})

	return s, err
}

func (r *Request) referSubscription(o ReferAcceptance) (*ServerSubscription, error) {
	cseq, _ := r.req.Header.CSeq()
	event := sip.Event{Package: "refer", ID: strconv.FormatUint(uint64(cseq.Seq), 10)}

	expires := o.Expires
	if expires == 0 {
		expires = DefaultReferExpires
	}

	var refer *ServerSubscription

	// The state of the refer event is the last status of the referred request, 100 Trying before any.
	acc := Acceptance{Expires: expires, State: func(uint32) (Notification, error) {
		status := Sipfrag(100, "")
		if refer != nil && refer.referStatus != nil {
			status = *refer.referStatus
		}

		return Notification{Bodies: []sip.BodyPart{status}, Full: true}, nil
	}, Handler: o.Handler, Contact: o.Contact}

	if r.s == nil {
		s, err := r.subscribe(acc, event, true)
		refer = s

		return s, err
	}

	// A REFER within the dialog of a session: the subscription is a usage of that dialog.
	sess := r.s

	if err := r.respond(Reply{Code: 200}); err != nil {
		return nil, err
	}

	refer = &ServerSubscription{
		a: r.a, st: r.st, h: o.Handler, event: event, d: sess.d, shared: true, contact: sess.contact,
		stateOf: acc.State, state: SubscriptionActive, limit: expires,
	}

	r.a.addServerSubscription(refer)
	refer.setExpiry(expires)
	refer.enqueue(&pendingNotify{build: refer.stateOf, full: true})

	return refer, nil
}

// ReferStatus reports the status of the request a REFER referred, in a NOTIFY of its sipfrag (RFC 3515 §2.4.5): a
// provisional one keeps the subscription, a final one terminates it, reason noresource (§2.4.7).
func (s *ServerSubscription) ReferStatus(code int, reason string) error {
	return s.st.do(func() error {
		if s.ended || s.ending != nil {
			return &Error{Op: "ReferStatus", State: Terminated, Err: ErrState}
		}

		body := Sipfrag(code, reason)
		s.referStatus = &body
		n := func(uint32) (Notification, error) { return Notification{Bodies: []sip.BodyPart{body}, Full: true}, nil }

		if code < 200 {
			s.enqueue(&pendingNotify{build: n, full: true})
			return nil
		}

		s.terminate(sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: sip.SubscriptionNoResource},
			End{Cause: EndTerminated}, n)

		return nil
	})
}
