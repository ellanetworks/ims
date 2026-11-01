package ua

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/offeranswer"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/transaction"
)

// maxMinSERetries bounds the retries of an initial INVITE after a 422 whose Min-SE rises (RFC 4028 §7.3): one per
// proxy on the path that asks for more.
const maxMinSERetries = 4

// Outgoing is an initial INVITE of the agent, and the sessions its responses create: one for each dialog, early ones
// when it forks (RFC 3261 §13.2.2). Its state belongs to its strand.
type Outgoing struct {
	a      *Agent
	st     *Strand
	h      Handler
	policy Policy

	// tmpl is the INVITE before Prepare, sent again after a 422 (RFC 4028 §7.3) or a challenge (RFC 3261 §22.2).
	tmpl  *sip.Request
	ci    *clientInvite
	sdp   offeranswer.Negotiation
	tx    *transaction.ClientTransaction
	tries int

	sessions map[string]*Session
	chosen   *Session
	final    bool

	// minSE is the Min-SE of the last 422 retried, retries their number.
	minSE   uint32
	retries int

	// cancel is the change of the CANCEL of the application, sent once the transaction exists.
	cancel *Change

	// closing: Shutdown waits for the final response, until closeBy.
	closing bool
	closeBy *timer
}

// awaitFinal makes Shutdown wait for the final response of the INVITE, or 64*T1.
func (o *Outgoing) awaitFinal() {
	if o.final || o.closing {
		return
	}

	o.closing = true
	o.a.await(1)
	o.closeBy = o.st.after(64*o.a.layer.T1(), o.release)
}

// release ends the wait of Shutdown for the INVITE.
func (o *Outgoing) release() {
	if o.closing {
		o.closing = false
		o.closeBy.stop()
		o.a.await(-1)
	}
}

// Invite sends an initial INVITE, and creates a session for each dialog its responses create, with handler h.
//
// req carries the Request-URI, To, the Route of an outbound proxy or a service route, the header fields of the
// application and its bodies: an SDP one is the offer; without one, the offer comes in a reliable 1xx or the 2xx.
// The agent adds what req leaves out: the From tag, a Call-ID, CSeq, Max-Forwards, Contact, Allow, and the option
// tags, Session-Expires and Min-SE of 100rel, 199 and the session timer.
//
// The INVITE, its retries and what its responses cause are part of the trace of ctx.
func (a *Agent) Invite(ctx context.Context, req *sip.Request, h Handler, opts ...Option) (*Outgoing, error) {
	if req.Method != "INVITE" {
		return nil, fmt.Errorf("sip/ua: Invite of a %s request", req.Method)
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

	op := a.options(opts)

	// RFC 6228 §8: the agent takes the 199 of the early dialogs a forking proxy or the UAS ends.
	tags := slices.DeleteFunc(append(slices.Clone(a.supported), sip.OptionTag199), func(tag string) bool {
		return containsFold(op.without, tag)
	})
	mergeSupported(&tmpl.Header, tags)

	if !tmpl.Header.Has("Allow") {
		tmpl.Header.Set("Allow", a.allowValue())
	}

	// RFC 4028 §7.1: the session interval asked for, and the smallest one the agent accepts, above the default.
	if iv := a.cfg.SessionTimer.Interval; iv > 0 && !tmpl.Header.Has("Session-Expires") {
		tmpl.Header.Set("Session-Expires", strconv.Itoa(int(iv/time.Second)))
	}

	if minSE := seconds(a.cfg.SessionTimer.MinSE); minSE > minSessionExpires && !tmpl.Header.Has("Min-SE") {
		tmpl.Header.Set("Min-SE", strconv.FormatUint(uint64(minSE), 10))
	}

	o := &Outgoing{a: a, st: op.strand(a), h: h, policy: op.policyOr(a.cfg.Policy), tmpl: tmpl, sessions: map[string]*Session{}}

	if err := a.addOutgoing(o); err != nil {
		return nil, err
	}

	if err := o.st.do(func() error { return o.send(ctx) }); err != nil {
		a.removeOutgoing(o)
		return nil, err
	}

	return o, nil
}

// Strand is the strand of the INVITE and of its sessions.
func (o *Outgoing) Strand() *Strand {
	return o.st
}

// SetPolicy sets the policy of the sessions the INVITE creates from now on; Session.SetPolicy sets the one of a
// session it created.
func (o *Outgoing) SetPolicy(p Policy) {
	_ = o.st.do(func() error {
		o.policy = p
		return nil
	})
}

func (o *Outgoing) done() {
	o.a.removeOutgoing(o)
}

// send sends the INVITE of the template.
func (o *Outgoing) send(ctx context.Context) error {
	req := o.tmpl.Clone()

	o.ci = &clientInvite{out: o, req: req, key: keyOf(local, req)}
	o.sdp = offeranswer.Negotiation{KeepFirstAnswer: true}
	o.sdp.Request(o.ci.key, sessionSDP(req.Envelope))

	tx, err := o.a.prepareAndSend(ctx, req, o.st.client(o.response, o.error))
	if err != nil {
		return err
	}

	o.tx = tx

	if o.cancel != nil {
		return tx.CancelWith(o.cancel.apply)
	}

	return nil
}

// Request is the INVITE as last sent.
func (o *Outgoing) Request() *sip.Request {
	return get(o.st, func() *sip.Request {
		if o.tx != nil {
			return o.tx.Request()
		}

		return o.ci.req
	})
}

// Cancel cancels the INVITE, the CANCEL carrying c: its Reason header fields (RFC 3326, Reasons), or the session
// description of a precondition failure (RFC 3312 §8). Its sessions end on the 487. It returns ErrState once a final
// response came.
func (o *Outgoing) Cancel(c Change) error {
	return o.st.do(func() error {
		if o.final {
			return &Error{Op: "Cancel", State: Confirmed, Err: ErrState}
		}

		return o.cancelWith(c)
	})
}

func (o *Outgoing) cancelWith(c Change) error {
	if o.cancel != nil {
		return nil
	}

	o.cancel = &c

	if o.tx == nil || o.final {
		return nil
	}

	return o.tx.CancelWith(c.apply)
}

func (o *Outgoing) emitFailed(ctx context.Context, res *sip.Response, err error) {
	o.st.emit(func() {
		if f := o.h.Failed; f != nil {
			f(ctx, o, res, err)
		}
	})
}

// response handles a response to the INVITE.
func (o *Outgoing) response(ctx context.Context, res *sip.Response) {
	// A 100 is hop by hop (RFC 3261 §8.2.6.1): reported, without a session.
	if res.StatusCode == 100 {
		o.report(ctx, nil, res)
		return
	}

	// Once final, the INVITE no longer holds Shutdown, its BYE, if any, counted by then.
	defer func() {
		if o.final {
			o.release()
		}
	}()

	to, err := res.Header.To()
	if err != nil {
		return
	}

	tag := to.Tag()

	if res.StatusCode >= 300 || res.StatusCode == 199 {
		o.report(ctx, o.sessions[tag], res)
	}

	if res.StatusCode >= 300 {
		o.failed(ctx, res)
		return
	}

	if res.StatusCode == 199 {
		o.earlyTerminated(ctx, res, tag)
		return
	}

	if tag == "" {
		o.report(ctx, nil, res)

		if res.IsProvisional() {
			o.st.emit(func() {
				if f := o.h.Progress; f != nil {
					f(ctx, nil, res)
				}
			})
		}

		return
	}

	s := o.session(res, tag)
	if s == nil {
		o.report(ctx, nil, res)
		return
	}

	// A 2xx acknowledged already is a retransmission.
	if _, seen := s.acks[o.ci.key.Seq]; !seen || !res.IsSuccess() {
		s.emitReceived(ctx, res)
	}

	if res.IsSuccess() && o.chosen == nil {
		o.chosen = s
		o.final = true
		o.st.later(ctx, 64*o.a.layer.T1(), o.endEarly)

		// Answered, the INVITE has nothing left for Shutdown to cancel.
		o.done()
	}

	if res.IsSuccess() && o.chosen != s {
		o.forked(ctx, s, res)
		return
	}

	// RFC 3261 §9.1, §15: a 2xx crossing the CANCEL confirms the session, which ends with a BYE carrying the Reason
	// of the CANCEL.
	if res.IsSuccess() && o.cancel != nil && s.state == Early {
		s.clientResponse(ctx, o.ci, res)
		s.byeEnd(ctx, End{Cause: EndCancelled, Response: res}, Change{Header: reasonFields(o.cancel.Header)}, nil)

		return
	}

	s.clientResponse(ctx, o.ci, res)
}

// report reports a response to the INVITE to the session of its dialog, or without one (Handler.Received).
func (o *Outgoing) report(ctx context.Context, s *Session, res *sip.Response) {
	if s != nil {
		s.emitReceived(ctx, res)
		return
	}

	o.st.emit(func() {
		if f := o.h.Received; f != nil {
			f(ctx, nil, res)
		}
	})
}

// reasonFields are the Reason header fields of h.
func reasonFields(h sip.Header) sip.Header {
	var out sip.Header

	for _, f := range h {
		if sip.LongName(f.Name) == "Reason" {
			out = append(out, f)
		}
	}

	return out
}

// earlyTerminated handles a 199 (RFC 6228 §4): the early dialog it names ends, once its PRACK, if reliable, is sent;
// a reliable 199 of a dialog the agent did not know yet creates it for the PRACK, even without a Contact, as one a
// forking proxy sends: the PRACK then goes to the Request-URI of the INVITE. An offer in it, which can only have no
// streams (RFC 6228 §8), is answered refused in the PRACK.
func (o *Outgoing) earlyTerminated(ctx context.Context, res *sip.Response, tag string) {
	if tag == "" {
		return
	}

	reliable := res.Header.HasOptionTag("Require", sip.OptionTag100rel)

	s := o.sessions[tag]
	if s == nil && reliable && o.chosen == nil {
		if !res.Header.Has("Contact") {
			res = res.Clone()
			res.Header.Set("Contact", "<"+o.ci.req.URI.String()+">")
		}

		s = o.session(res, tag)
	}

	if s == nil || s.state != Early {
		return
	}

	_ = s.d.ReceiveResponse(res)

	if reliable {
		rseq, err := res.Header.RSeq()
		if last, seen := s.rseq[o.ci.key.Seq]; err != nil || seen && rseq != last+1 {
			return
		}

		s.rseq[o.ci.key.Seq] = rseq

		var bodies []sip.BodyPart

		if offer := sessionSDP(res.Envelope); offer != nil && s.offerResponse(o.ci.key, res.StatusCode, offer) == offeranswer.Offer {
			if refusal, err := sdp.Refusal(offer); err == nil {
				bodies = []sip.BodyPart{SDP(refusal)}
			}
		}

		s.prack(ctx, res, Change{Bodies: bodies}, nil)
	}

	delete(o.sessions, tag)
	s.emitProgress(ctx, res)
	s.end(ctx, End{Cause: EndNotChosen, Remote: true, Response: res})
}

// forked handles a 2xx of another dialog than the one answered first: Handler.Forked keeps its session, confirmed;
// otherwise it is acknowledged and ended with a BYE (RFC 3261 §13.2.2.4).
func (o *Outgoing) forked(ctx context.Context, s *Session, res *sip.Response) {
	if s.state != Early || o.h.Forked == nil {
		if s.state == Confirmed {
			s.clientResponse(ctx, o.ci, res)
		} else {
			s.notChosen(ctx, o.ci, res)
		}

		return
	}

	o.st.emit(func() {
		keep := o.h.Forked(ctx, s, res)

		_ = o.st.do(func() error {
			switch {
			case s.state == Early && keep, s.state == Terminated:
				s.clientResponse(ctx, o.ci, res)
			default:
				s.notChosen(ctx, o.ci, res)
			}

			return nil
		})
	})
}

// session is the session of the dialog of a response, created by its first response (RFC 3261 §12.1.2).
func (o *Outgoing) session(res *sip.Response, tag string) *Session {
	if s := o.sessions[tag]; s != nil {
		return s
	}

	if o.final && res.IsProvisional() {
		return nil
	}

	d, err := dialog.NewUAC(o.ci.req, res)
	if err != nil {
		o.a.log.Debug("response creates no dialog", callAttr(o.ci.req), slog.Any("error", err))
		return nil
	}

	s := newSession(o.a, o.st, o.h, true, d.ID(), o.policy)
	s.d = d
	s.out = o
	s.sdp = o.sdp
	s.contact = o.ci.req.Header.Get("Contact")

	o.sessions[tag] = s
	o.a.addSession(s)

	return s
}

// failed handles a final response other than 2xx: a challenge is answered (RFC 3261 §22.2), a 422 retried with its
// Min-SE while it rises (RFC 4028 §7.3); otherwise the INVITE fails, then its sessions end.
func (o *Outgoing) failed(ctx context.Context, res *sip.Response) {
	if o.final {
		return
	}

	next := o.tmpl.Clone()
	nextCSeq(next)

	retry := false

	switch minSE, ok, err := res.Header.MinSE(); {
	case o.cancel != nil:
	case res.StatusCode == 422 && ok && err == nil && minSE > o.minSE && o.retries < maxMinSERetries:
		o.minSE = minSE
		o.retries++
		retry = true

		next.Header.Set("Min-SE", strconv.FormatUint(uint64(minSE), 10))

		if se, ok, err := next.Header.SessionExpires(); !ok || err != nil || se.Seconds < minSE {
			next.Header.Set("Session-Expires", strconv.FormatUint(uint64(minSE), 10))
		}
	case o.a.authenticate(next, res, o.tries):
		o.tries++
		retry = true
	}

	if retry {
		o.endSessions(ctx, End{Cause: EndRejected, Remote: true, Response: res})
		o.tmpl = next

		if err := o.send(ctx); err != nil {
			o.error(ctx, err)
		}

		return
	}

	o.final = true

	e := End{Cause: EndRejected, Remote: true, Response: res}
	if o.cancel != nil && res.StatusCode == 487 {
		e = End{Cause: EndCancelled, Response: res}
	}

	o.done()
	o.emitFailed(ctx, res, nil)
	o.endSessions(ctx, e)
}

// closed ends an outgoing INVITE the agent closed before its final response, ctx being the trace of the shutdown.
func (o *Outgoing) closed(ctx context.Context) {
	if o.final {
		return
	}

	o.final = true
	o.done()
	o.emitFailed(ctx, nil, ErrClosed)
	o.endSessions(ctx, End{Cause: EndClosed})
	o.release()
}

// dropped ends the INVITE at once, without a CANCEL, its sessions with EndDropped (Agent.Drop).
func (o *Outgoing) dropped(ctx context.Context) {
	if o.final {
		return
	}

	o.final = true
	o.done()
	o.emitFailed(ctx, nil, transaction.ErrDropped)
	o.endSessions(ctx, End{Cause: EndDropped})
	o.release()
}

// error ends an outgoing INVITE that failed without a final response.
func (o *Outgoing) error(ctx context.Context, err error) {
	if o.final {
		return
	}

	o.final = true
	o.done()
	o.emitFailed(ctx, nil, err)
	o.endSessions(ctx, failureEnd(err))
	o.release()
}

// endSessions ends the early sessions of the INVITE: all but the one chosen, and those Handler.Forked kept.
func (o *Outgoing) endSessions(ctx context.Context, e End) {
	for tag, s := range o.sessions {
		if s != o.chosen && s.state == Early {
			delete(o.sessions, tag)
			s.end(ctx, e)
		}
	}
}

// endEarly ends, once the INVITE transaction is over, the early dialogs no 2xx confirmed (RFC 3261 §13.2.2.4).
func (o *Outgoing) endEarly(ctx context.Context) {
	o.endSessions(ctx, End{Cause: EndNotChosen})
	o.done()
}

// notChosen acknowledges the 2xx of another dialog of a forked INVITE than the one answered first, refusing its offer,
// and ends it with a BYE (RFC 3261 §13.2.2.4).
func (s *Session) notChosen(ctx context.Context, c *clientInvite, res *sip.Response) {
	if _, seen := s.acks[c.key.Seq]; seen {
		s.resendAck(ctx, c.key.Seq)
		return
	}

	_ = s.d.ReceiveResponse(res)

	var bodies []sip.BodyPart

	if offer := sessionSDP(res.Envelope); offer != nil && s.offerResponse(c.key, res.StatusCode, offer) == offeranswer.Offer {
		if refusal, err := sdp.Refusal(offer); err == nil {
			bodies = []sip.BodyPart{SDP(refusal)}
		}
	}

	s.sendAck(ctx, c, Change{Bodies: bodies})
	s.byeEnd(ctx, End{Cause: EndNotChosen, Response: res}, Change{}, nil)
}

// clientInvite is an INVITE or an UPDATE of the agent: the initial INVITE of an Outgoing, or a re-INVITE or an
// UPDATE of a session, for a modification m.
type clientInvite struct {
	s   *Session
	out *Outgoing
	m   *modification
	req *sip.Request
	key offeranswer.Key
	tx  *transaction.ClientTransaction

	// tries are the challenges answered; final marks a final response received: an error of the transaction after
	// it, as on closing, is no failure.
	tries int
	final bool

	// cancelled completes the CANCEL of a re-INVITE the application cancelled, sent once the transaction exists.
	cancelled func(*sip.Request)
}

func (c *clientInvite) send(ctx context.Context) error {
	s := c.s

	err := s.transmit(ctx, c.req, s.st.client(c.response, c.error), func(tx *transaction.ClientTransaction) {
		c.tx = tx

		if c.cancelled != nil {
			_ = tx.CancelWith(c.cancelled)
		}
	})
	if err != nil {
		c.final = true
		s.sdp.Fail(c.key)
		s.finished(c)
		s.emitDone(c.m.done, nil, err)
	}

	return err
}

// cancel cancels a re-INVITE of the agent with the change of its CANCEL; the transaction layer sends the CANCEL once
// a provisional response came (RFC 3261 §9.1).
func (c *clientInvite) cancel(ch Change) {
	if c.cancelled != nil {
		return
	}

	c.cancelled = func(cancel *sip.Request) {
		ch.apply(cancel)
		c.s.decorate(cancel)
	}

	if c.tx != nil {
		_ = c.tx.CancelWith(c.cancelled)
	}
}

// response handles a response to a re-INVITE or an UPDATE.
func (c *clientInvite) response(ctx context.Context, res *sip.Response) {
	if res.StatusCode == 100 {
		c.s.emitReceived(ctx, res)
		return
	}

	c.s.clientResponse(ctx, c, res)
}

// error handles the error of the transaction of a re-INVITE or an UPDATE.
func (c *clientInvite) error(ctx context.Context, err error) {
	s := c.s

	if c.final {
		return
	}

	c.final = true
	s.sdp.Fail(c.key)

	s.fail(ctx, c.key.Method, nil, err)
	s.finished(c)
	s.emitDone(c.m.done, nil, err)
}

// clientResponse handles a response to an INVITE or an UPDATE of the agent, in the dialog of s.
func (s *Session) clientResponse(ctx context.Context, c *clientInvite, res *sip.Response) {
	if res.StatusCode >= 200 && c.out == nil {
		if c.final && (!res.IsSuccess() || c.key.Method != "INVITE") {
			return
		}

		c.final = true
	}

	// The responses to a re-INVITE or an UPDATE, those of the initial INVITE reported by its Outgoing, and a
	// retransmitted 2xx not.
	if _, seen := s.acks[c.key.Seq]; c.out == nil && (!seen || !res.IsSuccess() || c.key.Method != "INVITE") {
		s.emitReceived(ctx, res)
	}

	if s.d != nil {
		_ = s.d.ReceiveResponse(res)
	}

	s.notePeer(res.Header)

	switch {
	case res.IsProvisional():
		s.clientProvisional(ctx, c, res)
	case res.IsSuccess():
		s.clientSuccess(ctx, c, res)
	default:
		s.offerResponse(c.key, res.StatusCode, nil)

		if c.out == nil {
			s.modificationFailed(ctx, c, res)
		}
	}
}

// clientProvisional handles a provisional response to an INVITE: a reliable one in order gets its PRACK, with the
// answer to its offer (RFC 3262 §4, §5).
func (s *Session) clientProvisional(ctx context.Context, c *clientInvite, res *sip.Response) {
	if c.key.Method != "INVITE" || s.state == Terminated {
		return
	}

	if !res.Header.HasOptionTag("Require", sip.OptionTag100rel) {
		s.emitProgress(ctx, res)
		return
	}

	rseq, err := res.Header.RSeq()
	if err != nil {
		return
	}

	// A retransmission, or a response out of order, is discarded.
	if last, seen := s.rseq[c.key.Seq]; seen && rseq != last+1 {
		return
	}

	s.rseq[c.key.Seq] = rseq

	offer := sessionSDP(res.Envelope)

	// The reliable 1xx is reported once its PRACK completed: a request of the application, such as an UPDATE of
	// preconditions (RFC 3312 §5), follows it. A 2xx that comes first reports it before itself.
	s.unreported = append(s.unreported, res)
	progress := func(ctx context.Context) { s.reportProgress(ctx, res) }

	switch s.offerResponse(c.key, res.StatusCode, offer) {
	case offeranswer.Answer:
		s.emitAnswer(ctx, offer)
		s.prack(ctx, res, Change{}, progress)
	case offeranswer.Offer:
		s.emitOffer(ctx, &Offer{s: s, sdp: offer, res: res, c: c, prack: true, progress: progress})
	default:
		s.prack(ctx, res, Change{}, progress)
	}
}

// reportProgress reports the reliable 1xx not reported yet, up to res, or all of them when res is nil.
func (s *Session) reportProgress(ctx context.Context, res *sip.Response) {
	if s.state == Terminated {
		s.unreported = nil
		return
	}

	i := len(s.unreported) - 1
	if res != nil {
		i = slices.Index(s.unreported, res)
	}

	for _, r := range s.unreported[:i+1] {
		s.emitProgress(ctx, r)
	}

	s.unreported = s.unreported[i+1:]
}

// prack sends the PRACK of a reliable 1xx, with c; then, if any, runs once it completes. A challenge is answered
// (RFC 3261 §22.2); a 481 or a 408 to it, or its timeout, loses the dialog (RFC 3261 §12.2.1.2).
func (s *Session) prack(ctx context.Context, res *sip.Response, c Change, then func(context.Context)) {
	prack, err := s.d.NewPrack(res)
	if err != nil {
		return
	}

	c.apply(prack)
	s.offerRequest(keyOf(local, prack), sessionSDPOf(c.Bodies))

	done, tries := false, 0

	var handler transaction.ClientHandler

	finish := func(ctx context.Context, res *sip.Response, err error) {
		if done {
			return
		}

		done = true

		if res == nil || !res.IsSuccess() {
			s.fail(ctx, "PRACK", res, err)
		}

		if then != nil {
			then(ctx)
		}

		s.pump()
	}

	handler = s.st.client(func(ctx context.Context, res *sip.Response) {
		if res.IsProvisional() || done {
			return
		}

		s.emitReceived(ctx, res)

		if next, ok := s.challenged(prack, res, tries, false); ok {
			tries++
			prack = next
			s.sendRequest(ctx, next, handler)

			return
		}

		finish(ctx, res, nil)
	}, func(ctx context.Context, err error) { finish(ctx, nil, err) })

	s.sendRequest(ctx, prack, handler)
}

// clientSuccess handles a 2xx to an INVITE or an UPDATE of the agent.
func (s *Session) clientSuccess(ctx context.Context, c *clientInvite, res *sip.Response) {
	if c.key.Method == "INVITE" {
		if _, seen := s.acks[c.key.Seq]; seen {
			s.resendAck(ctx, c.key.Seq)
			return
		}
	}

	offer := sessionSDP(res.Envelope)

	if s.state == Terminated {
		// The session ended while its INVITE was in progress: the 2xx is acknowledged, its offer refused, and the
		// dialog, kept for it, ended with a BYE unless one ended the session already.
		if c.key.Method == "INVITE" {
			var bodies []sip.BodyPart

			if offer != nil && s.sdp.Response(c.key, res.StatusCode, offer) == offeranswer.Offer {
				if refusal, err := sdp.Refusal(offer); err == nil {
					bodies = []sip.BodyPart{SDP(refusal)}
				}
			}

			s.sendAck(ctx, c, Change{Bodies: bodies})

			if !s.byeSent {
				if bye, err := s.d.NewRequest("BYE"); err == nil {
					s.byeSent = true
					s.sendRequest(ctx, bye, nil)
				}
			}
		}

		return
	}

	role := s.offerResponse(c.key, res.StatusCode, offer)

	s.timer.fromResponse(c.req, res)

	if c.key.Method == "INVITE" {
		s.reportProgress(ctx, nil)
	}

	if c.key.Method == "INVITE" && s.state == Early {
		s.confirm(ctx, res)
	} else {
		s.timer.start(ctx, s)
	}

	if role == offeranswer.Answer {
		s.emitAnswer(ctx, offer)
	}

	// The ACK goes before the modifications that waited for this one.
	var done func(*sip.Response, error)
	if c.m != nil {
		done = c.m.done
		s.finishedQuietly(c)
	}

	if c.key.Method == "INVITE" {
		if role == offeranswer.Offer {
			o := &Offer{s: s, sdp: offer, res: res, c: c, initial: c.out != nil}
			s.acks[c.key.Seq] = nil
			s.answering[c.key.Seq] = o
			o.deadline = s.st.later(ctx, answerDeadline*s.a.layer.T1(), o.expire)
			s.emitOffer(ctx, o)
		} else {
			s.sendAck(ctx, c, Change{})
		}
	}

	s.emitDone(done, res, nil)
	s.modified()
	s.pump()
}

// answerDeadline is the time, in T1, the application has to answer the offer of a 2xx: half the time the peer
// retransmits it for (RFC 3261 §13.3.1.4).
const answerDeadline = 32

// sendAck sends the ACK of the 2xx to an INVITE, with c, and keeps it for the retransmissions of the 2xx, until 64*T1
// after it (RFC 3261 §13.2.2.4, RFC 6026).
func (s *Session) sendAck(ctx context.Context, c *clientInvite, ch Change) {
	ack, err := s.d.NewAck(c.req)
	if err != nil {
		s.ackFailed(ctx, err)
		return
	}

	ch.apply(ack)

	if answer := sessionSDPOf(ch.Bodies); answer != nil {
		s.offerRequest(offeranswer.Key{From: local, Seq: c.key.Seq, Method: "ACK"}, answer)
	}

	s.decorate(ack)

	if err := s.a.cfg.Prepare(ack); err != nil {
		s.ackFailed(ctx, err)
		return
	}

	seq := c.key.Seq
	s.acks[seq] = ack
	delete(s.answering, seq)
	s.resendAck(ctx, seq)

	s.st.after(64*s.a.layer.T1(), func() {
		delete(s.acks, seq)
		delete(s.rseq, seq)
	})

	s.pump()
}

// ackFailed ends a session whose 2xx the agent cannot acknowledge: the peer would end it once its 2xx goes
// unacknowledged (RFC 3261 §13.3.1.4), and the session is lost (RFC 3261 §13.2.2.4). It ends, with a BYE, once the
// handling of the 2xx is over.
func (s *Session) ackFailed(ctx context.Context, err error) {
	s.logger().Warn("ACK not sent", slog.Any("error", err))

	s.st.post(func() {
		if s.state != Terminated {
			s.byeEnd(ctx, End{Cause: EndFailed, Err: fmt.Errorf("sip/ua: ACK not sent: %w", err)}, Change{}, nil)
		}
	})
}

// resendAck sends the ACK of a 2xx again, in the trace of what makes it go: the 2xx it acknowledges.
func (s *Session) resendAck(ctx context.Context, seq uint32) {
	if ack := s.acks[seq]; ack != nil {
		s.queueAck(ctx, ack.Clone())
	}
}

// Offer is an offer of the peer in a response, to answer in the PRACK of a reliable 1xx or in the ACK of a 2xx.
type Offer struct {
	s       *Session
	sdp     []byte
	res     *sip.Response
	c       *clientInvite
	prack   bool
	initial bool
	used    bool

	// progress reports the reliable 1xx of the offer once its PRACK completed.
	progress func(context.Context)

	// deadline ends the session whose application leaves the offer of a 2xx unanswered.
	deadline *timer
}

func (o *Offer) SDP() []byte {
	return o.sdp
}

// Response is the response carrying the offer.
func (o *Offer) Response() *sip.Response {
	return o.res
}

// Answer sends the PRACK or the ACK with c, whose bodies carry the answer, in the trace of ctx. The offer of a 2xx is to
// be answered within 32*T1: past it, the agent refuses it and ends the session (ErrNoAnswer).
func (o *Offer) Answer(ctx context.Context, c Change) error {
	if sessionSDPOf(c.Bodies) == nil {
		return errors.New("sip/ua: no session description in the answer")
	}

	return o.s.st.do(func() error {
		if o.used {
			return nil
		}

		o.used = true
		o.deadline.stop()

		s := o.s

		if o.prack {
			s.prack(ctx, o.res, c, o.progress)
		} else if _, waiting := s.answering[o.c.key.Seq]; waiting {
			s.sendAck(ctx, o.c, c)
		}

		return nil
	})
}

// Reject refuses the offer with an answer refusing its streams (RFC 3264 §6), which the PRACK or the ACK must carry
// (RFC 3262 §5, RFC 3261 §13.2.2.4). The initial INVITE is then cancelled, or its session ended with a BYE
// (RFC 3261 §13.2.2.4); the offer of a 2xx to a re-INVITE refused, the session goes on without the refused streams,
// for the application to offer anew or to end. ctx is the trace of the refusal.
func (o *Offer) Reject(ctx context.Context) {
	_ = o.s.st.do(func() error {
		o.reject(ctx)
		return nil
	})
}

func (o *Offer) reject(ctx context.Context) {
	if o.used {
		return
	}

	o.used = true
	o.deadline.stop()

	s := o.s

	switch _, waiting := s.answering[o.c.key.Seq]; {
	case o.prack:
		// RFC 3262 §5: the PRACK carries the answer; the CANCEL follows it.
		o.refuse(ctx, func(context.Context) { o.cancelInvite() })
	case waiting && o.initial:
		o.refuse(ctx, nil)
		s.byeEnd(ctx, End{Cause: EndBye}, Change{}, nil)
	case waiting:
		o.refuse(ctx, nil)
	}
}

// expire refuses an offer of a 2xx the application did not answer in time, and ends the session, in the trace of the
// 2xx.
func (o *Offer) expire(ctx context.Context) {
	if o.used {
		return
	}

	o.used = true

	if _, waiting := o.s.answering[o.c.key.Seq]; waiting {
		o.refuse(ctx, nil)
		o.s.byeEnd(ctx, End{Cause: EndLost, Err: ErrNoAnswer}, Change{}, nil)
	}
}

// refuse sends the PRACK or the ACK with an answer refusing every stream of the offer, then.
func (o *Offer) refuse(ctx context.Context, then func(context.Context)) {
	var bodies []sip.BodyPart

	if refusal, err := sdp.Refusal(o.sdp); err == nil {
		bodies = []sip.BodyPart{SDP(refusal)}
	}

	if o.prack {
		o.s.prack(ctx, o.res, Change{Bodies: bodies}, then)
		return
	}

	o.s.sendAck(ctx, o.c, Change{Bodies: bodies})
}

func (o *Offer) cancelInvite() {
	if o.c.out != nil {
		_ = o.c.out.cancelWith(Change{})
		return
	}

	o.c.cancel(Change{})
}
