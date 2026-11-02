package ua

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/sip"
	"go.opentelemetry.io/otel/trace"
)

// RFC 3515 section 3.4
const DefaultReferExpires = 4 * time.Minute

// RFC 3515 section 2.4.4
// RFC 7647 section 4
func (a *Agent) Refer(ctx context.Context, req *sip.Request, h SubscriptionHandler, opts ...Option) (*Subscription, error) {
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
		tmpl: tmpl, sub: sub, refer: true, subs: map[string]*Subscription{}, origin: trace.SpanContextFromContext(ctx),
	}
	s := &Subscription{a: a, st: st, sh: h, att: att, event: event}
	att.first = s

	if err := a.addSubscribing(att); err != nil {
		return nil, err
	}

	if err := st.do(func() error { return att.send(ctx) }); err != nil {
		a.removeSubscribing(att)
		return nil, err
	}

	return s, nil
}

// RFC 4488 section 4
// RFC 7614 section 4.2
func noSubscription(refer *sip.Request, res *sip.Response) bool {
	return strings.EqualFold(strings.TrimSpace(res.Header.Get("Refer-Sub")), "false") ||
		refer.Header.HasOptionTag("Require", sip.OptionTagExplicitSub)
}

// RFC 3420
// RFC 3515 section 2.4.5
func Sipfrag(code int, reason string) sip.BodyPart {
	if reason == "" {
		reason = sip.ReasonPhrase(code)
	}

	return sip.NewBodyPart("message/sipfrag", []byte(fmt.Sprintf("SIP/2.0 %d %s\r\n", code, reason)))
}

type ReferAcceptance struct {
	NoSubscription bool

	EventsAt string

	Expires time.Duration

	Handler ServerSubscriptionHandler

	Contact string
}

// RFC 7647 section 5
// RFC 3515 section 2.4.4
// RFC 3515 section 2.4.6
// RFC 3515 section 2.4.7
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
			if !r.a.cfg.Extensions.NoReferSub {
				return errors.New("sip/ua: no subscription needs Extensions.NoReferSub")
			}

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

	sess := r.s

	if err := r.respond(Reply{Code: 200}); err != nil {
		return nil, err
	}

	refer = &ServerSubscription{
		a: r.a, st: r.st, h: o.Handler, event: event, d: sess.d, shared: true, contact: sess.contact,
		stateOf: acc.State, state: SubscriptionActive, limit: expires,
	}

	r.a.addServerSubscription(refer, trace.SpanContextFromContext(r.ctx))
	refer.setExpiry(r.ctx, expires)
	refer.enqueue(&pendingNotify{ctx: r.ctx, build: refer.stateOf, full: true})

	return refer, nil
}

// RFC 3515 section 2.4.5
// RFC 3515 section 2.4.7
func (s *ServerSubscription) ReferStatus(ctx context.Context, code int, reason string) error {
	return s.st.do(func() error {
		if s.ended || s.ending != nil {
			return &Error{Op: "ReferStatus", State: Terminated, Err: ErrState}
		}

		body := Sipfrag(code, reason)
		s.referStatus = &body
		n := func(uint32) (Notification, error) { return Notification{Bodies: []sip.BodyPart{body}, Full: true}, nil }

		if code < 200 {
			s.enqueue(&pendingNotify{ctx: ctx, build: n, full: true})
			return nil
		}

		s.terminate(ctx, sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: sip.SubscriptionNoResource},
			End{Cause: EndTerminated}, n)

		return nil
	})
}
