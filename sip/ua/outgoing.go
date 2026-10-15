package ua

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/offeranswer"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/transaction"
)

// Outgoing is an initial INVITE of the agent, and the sessions its responses create: one for each dialog, early ones
// when it forks (RFC 3261 §13.2.2). Its state belongs to the loop of the agent.
type Outgoing struct {
	a *Agent
	h Handler

	// tmpl is the INVITE before Prepare, sent again after a 422 (RFC 4028 §7.3) or a challenge (RFC 3261 §22.2).
	tmpl  *sip.Request
	ci    *clientInvite
	sdp   offeranswer.Negotiation
	tx    *transaction.ClientTransaction
	tries int

	sessions  map[string]*Session
	chosen    *Session
	final     bool
	retried   bool
	cancelled func(*sip.Request)
}

// Invite sends an initial INVITE, and creates a session for each dialog its responses create, with handler h.
//
// req carries the Request-URI, To, the Route of an outbound proxy or a service route, the header fields of the
// application and its bodies: an SDP one is the offer; without one, the offer comes in a reliable 1xx or the 2xx.
// The agent adds what req leaves out: the From tag, a Call-ID, CSeq, Max-Forwards, Contact, Allow, and the option
// tags and Session-Expires of 100rel and the session timer.
func (a *Agent) Invite(req *sip.Request, h Handler) (*Outgoing, error) {
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

	for _, tag := range a.supported {
		if !tmpl.Header.HasOptionTag("Supported", tag) {
			tmpl.Header.Add("Supported", tag)
		}
	}

	if !tmpl.Header.Has("Allow") {
		tmpl.Header.Set("Allow", a.allowValue())
	}

	// RFC 4028 §7.1
	if iv := a.cfg.SessionTimer.Interval; iv > 0 && !tmpl.Header.Has("Session-Expires") {
		tmpl.Header.Set("Session-Expires", strconv.Itoa(int(iv/time.Second)))
	}

	o := &Outgoing{a: a, h: h, tmpl: tmpl, sessions: map[string]*Session{}}

	err := a.loop.call(func() error {
		if a.closed {
			return ErrClosed
		}

		if err := o.send(); err != nil {
			return err
		}

		a.outgoing[o] = struct{}{}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return o, nil
}

func (o *Outgoing) done() {
	delete(o.a.outgoing, o)
}

// send sends the INVITE of the template.
func (o *Outgoing) send() error {
	req := o.tmpl.Clone()

	o.ci = &clientInvite{out: o, req: req, key: keyOf(local, req)}
	o.sdp = offeranswer.Negotiation{}
	o.sdp.Request(o.ci.key, sessionSDP(req.Envelope))

	tx, err := o.a.prepareAndSend(req, o.a.client(o.response, o.error))
	if err != nil {
		return err
	}

	o.tx = tx

	if o.cancelled != nil {
		return tx.CancelWith(o.cancelled)
	}

	return nil
}

// Request is the INVITE as last sent.
func (o *Outgoing) Request() *sip.Request {
	return get(o.a.loop, func() *sip.Request { return o.ci.req })
}

// Cancel cancels the INVITE, the CANCEL carrying reasons (RFC 3261 §9.1, RFC 3326). Its sessions end on the 487.
func (o *Outgoing) Cancel(reasons ...sip.Reason) error {
	return o.CancelWith(func(cancel *sip.Request) {
		for _, r := range reasons {
			cancel.Header.Add("Reason", r.String())
		}
	})
}

// CancelWith cancels the INVITE, prepare completing the CANCEL: its Reason, or the session description of a
// precondition failure (RFC 3312 §8).
func (o *Outgoing) CancelWith(prepare func(cancel *sip.Request)) error {
	return o.a.loop.call(func() error { return o.cancel(prepare) })
}

func (o *Outgoing) cancel(prepare func(cancel *sip.Request)) error {
	if prepare == nil {
		prepare = func(*sip.Request) {}
	}

	o.cancelled = prepare

	if o.tx == nil || o.final {
		return nil
	}

	return o.tx.CancelWith(prepare)
}

func (o *Outgoing) emitFailed(res *sip.Response, err error) {
	o.a.loop.emit(func() {
		if f := o.h.Failed; f != nil {
			f(o, res, err)
		}
	})
}

// response handles a response to the INVITE.
func (o *Outgoing) response(res *sip.Response) {
	if res.StatusCode == 100 {
		return
	}

	to, err := res.Header.To()
	if err != nil {
		return
	}

	tag := to.Tag()

	if res.StatusCode >= 300 {
		o.failed(res)
		return
	}

	if tag == "" {
		if res.IsProvisional() {
			o.a.loop.emit(func() {
				if f := o.h.Progress; f != nil {
					f(nil, res)
				}
			})
		}

		return
	}

	s := o.session(res, tag)
	if s == nil {
		return
	}

	if res.IsSuccess() && o.chosen == nil {
		o.chosen = s
		o.final = true
		o.a.after(64*o.a.layer.T1(), o.endEarly)

		// Answered, the INVITE has nothing left for Close to cancel.
		o.done()
	}

	if res.IsSuccess() && o.chosen != s {
		s.notChosen(o.ci, res)
		return
	}

	s.clientResponse(o.ci, res)
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
		o.a.log.Debug("response creates no dialog", slog.Any("error", err))
		return nil
	}

	s := newSession(o.a, o.h, true, d.ID())
	s.d = d
	s.out = o
	s.sdp = o.sdp
	s.contact = o.ci.req.Header.Get("Contact")

	o.sessions[tag] = s
	o.a.addSession(s)

	return s
}

// failed handles a final response other than 2xx: a challenge is answered (RFC 3261 §22.2), a 422 retried with its
// Min-SE once (RFC 4028 §7.3); otherwise the sessions end.
func (o *Outgoing) failed(res *sip.Response) {
	if o.final {
		return
	}

	next := o.tmpl.Clone()
	nextCSeq(next)

	retry := false

	switch minSE, ok, err := res.Header.MinSE(); {
	case o.cancelled != nil:
	case res.StatusCode == 422 && ok && err == nil && !o.retried:
		o.retried = true
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
		o.endSessions(End{Cause: EndRejected, Remote: true, Response: res})
		o.tmpl = next

		if err := o.send(); err != nil {
			o.error(err)
		}

		return
	}

	o.final = true

	e := End{Cause: EndRejected, Remote: true, Response: res}
	if o.cancelled != nil && res.StatusCode == 487 {
		e = End{Cause: EndCancelled, Response: res}
	}

	o.endSessions(e)
	o.done()
	o.emitFailed(res, nil)
}

// error ends an outgoing INVITE that failed without a final response.
func (o *Outgoing) error(err error) {
	if o.final {
		return
	}

	o.final = true
	o.endSessions(End{Cause: EndTimeout, Err: err})
	o.done()
	o.emitFailed(nil, err)
}

// endSessions ends the sessions of the INVITE but the one chosen.
func (o *Outgoing) endSessions(e End) {
	for tag, s := range o.sessions {
		if s != o.chosen {
			delete(o.sessions, tag)
			s.end(e)
		}
	}
}

// endEarly ends, once the INVITE transaction is over, the early dialogs no 2xx confirmed (RFC 3261 §13.2.2.4).
func (o *Outgoing) endEarly() {
	o.endSessions(End{Cause: EndNotChosen})
	o.done()
}

// notChosen acknowledges the 2xx of another dialog of a forked INVITE than the one answered first, refusing its offer,
// and ends it with a BYE (RFC 3261 §13.2.2.4).
func (s *Session) notChosen(c *clientInvite, res *sip.Response) {
	if _, seen := s.acks[c.key.Seq]; seen {
		s.resendAck(c.key.Seq)
		return
	}

	_ = s.d.ReceiveResponse(res)

	var bodies []sip.BodyPart

	if offer := sessionSDP(res.Envelope); offer != nil && s.sdp.Response(c.key, res.StatusCode, offer) == offeranswer.Offer {
		if refusal, err := sdp.Refusal(offer); err == nil {
			bodies = []sip.BodyPart{SDP(refusal)}
		}
	}

	s.sendAck(c, bodies)
	s.byeEnd(End{Cause: EndNotChosen, Response: res})
}

// clientInvite is an INVITE or an UPDATE of the agent: the initial INVITE of an Outgoing, or a re-INVITE or an
// UPDATE of a session.
type clientInvite struct {
	s   *Session
	out *Outgoing
	req *sip.Request
	key offeranswer.Key
	tx  *transaction.ClientTransaction

	change Change
	done   func(*sip.Response, error)

	// refresh marks a session refresh of the agent; retried a 422 retried (RFC 4028 §7.3); tries the challenges
	// answered; final a final response received: an error of the transaction after it, as on closing, is no failure.
	refresh bool
	retried bool
	tries   int
	final   bool
}

func (c *clientInvite) send() error {
	s := c.s

	err := s.transmit(c.req, s.a.client(c.response, c.error), func(tx *transaction.ClientTransaction) { c.tx = tx })
	if err != nil {
		s.sdp.Fail(c.key)

		if s.invite == c {
			s.invite = nil
		}
	}

	return err
}

// response handles a response to a re-INVITE or an UPDATE.
func (c *clientInvite) response(res *sip.Response) {
	if res.StatusCode != 100 {
		c.s.clientResponse(c, res)
	}
}

// error handles the error of the transaction of a re-INVITE or an UPDATE.
func (c *clientInvite) error(err error) {
	s := c.s

	if c.final {
		return
	}

	c.final = true
	s.sdp.Fail(c.key)

	if s.invite == c {
		s.invite = nil
	}

	if errors.Is(err, transaction.ErrTimeout) {
		s.lost(nil, err)
	}

	s.emitDone(c.done, nil, err)
}

// clientResponse handles a response to an INVITE or an UPDATE of the agent, in the dialog of s.
func (s *Session) clientResponse(c *clientInvite, res *sip.Response) {
	if res.StatusCode >= 200 && c.out == nil {
		if c.final && !(res.IsSuccess() && c.key.Method == "INVITE") {
			return
		}

		c.final = true
	}

	if s.d != nil {
		_ = s.d.ReceiveResponse(res)
	}

	s.notePeer(res.Header)

	switch {
	case res.IsProvisional():
		s.clientProvisional(c, res)
	case res.IsSuccess():
		s.clientSuccess(c, res)
	default:
		s.clientFailure(c, res)
	}
}

// clientProvisional handles a provisional response to an INVITE: a reliable one in order gets its PRACK, with the
// answer to its offer (RFC 3262 §4, §5).
func (s *Session) clientProvisional(c *clientInvite, res *sip.Response) {
	if c.key.Method != "INVITE" || s.state == Terminated {
		return
	}

	if !res.Header.HasOptionTag("Require", sip.OptionTag100rel) {
		if c.out != nil {
			s.emitProgress(res)
		}

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

	switch s.sdp.Response(c.key, res.StatusCode, offer) {
	case offeranswer.Answer:
		s.emitAnswer(offer)
		s.prack(res, nil, nil)
	case offeranswer.Offer:
		s.emitOffer(&Offer{s: s, sdp: offer, res: res, c: c, prack: true})
	default:
		s.prack(res, nil, nil)
	}

	if c.out != nil {
		s.emitProgress(res)
	}
}

// prack sends the PRACK of a reliable 1xx, with bodies; then, if any, runs once it completes.
func (s *Session) prack(res *sip.Response, bodies []sip.BodyPart, then func()) {
	prack, err := s.d.NewPrack(res)
	if err != nil {
		return
	}

	prack.SetBodies(bodies...)
	s.sdp.Request(keyOf(local, prack), sessionSDPOf(bodies))

	var h transaction.ClientHandler

	if then != nil {
		done := false

		once := func() {
			if !done {
				done = true
				then()
			}
		}

		h = s.a.client(func(res *sip.Response) {
			if !res.IsProvisional() {
				once()
			}
		}, func(error) { once() })
	}

	s.sendRequest(prack, h)
}

// clientSuccess handles a 2xx to an INVITE or an UPDATE of the agent.
func (s *Session) clientSuccess(c *clientInvite, res *sip.Response) {
	if c.key.Method == "INVITE" {
		if _, seen := s.acks[c.key.Seq]; seen {
			s.resendAck(c.key.Seq)
			return
		}
	}

	offer := sessionSDP(res.Envelope)

	if s.state == Terminated {
		// The session ended while its INVITE was in progress: the 2xx is acknowledged, its offer refused, and the
		// dialog ended.
		if c.key.Method == "INVITE" {
			var bodies []sip.BodyPart

			if offer != nil && s.sdp.Response(c.key, res.StatusCode, offer) == offeranswer.Offer {
				if refusal, err := sdp.Refusal(offer); err == nil {
					bodies = []sip.BodyPart{SDP(refusal)}
				}
			}

			s.sendAck(c, bodies)

			if bye, err := s.d.NewRequest("BYE"); err == nil {
				s.sendRequest(bye, nil)
			}
		}

		return
	}

	role := s.sdp.Response(c.key, res.StatusCode, offer)

	s.timer.fromResponse(s.a, c.req, res)
	s.timer.start(s)

	if c.key.Method == "INVITE" && s.state == Early {
		s.state = Confirmed
		s.emitEstablished()
	}

	if role == offeranswer.Answer {
		s.emitAnswer(offer)
	}

	if c.key.Method == "INVITE" {
		if s.invite == c {
			s.invite = nil
		}

		if role == offeranswer.Offer {
			o := &Offer{s: s, sdp: offer, res: res, c: c}
			s.acks[c.key.Seq] = nil
			s.answering[c.key.Seq] = o
			s.emitOffer(o)
		} else {
			s.sendAck(c, nil)
		}
	}

	s.emitDone(c.done, res, nil)
}

// sendAck sends the ACK of the 2xx to an INVITE, with bodies, and keeps it for the retransmissions of the 2xx, until
// 64*T1 after it (RFC 3261 §13.2.2.4, RFC 6026).
func (s *Session) sendAck(c *clientInvite, bodies []sip.BodyPart) {
	ack, err := s.d.NewAck(c.req)
	if err != nil {
		return
	}

	ack.SetBodies(bodies...)

	if answer := sessionSDPOf(bodies); answer != nil {
		s.sdp.Request(offeranswer.Key{From: local, Seq: c.key.Seq, Method: "ACK"}, answer)
	}

	if err := s.a.cfg.Prepare(ack); err != nil {
		s.a.log.Debug("prepare ACK failed", slog.Any("error", err))
		return
	}

	seq := c.key.Seq
	s.acks[seq] = ack
	delete(s.answering, seq)
	s.resendAck(seq)

	s.a.after(64*s.a.layer.T1(), func() {
		delete(s.acks, seq)
		delete(s.rseq, seq)
	})
}

func (s *Session) resendAck(seq uint32) {
	if ack := s.acks[seq]; ack != nil {
		s.writeAck(ack.Clone())
	}
}

// clientFailure handles a final response other than 2xx to a re-INVITE or an UPDATE of the agent: a challenge is
// answered (RFC 3261 §22.2), a 491 retried after a random delay (RFC 3261 §14.1), a 422 with the Min-SE (RFC 4028
// §7.3); a 408 or 481 loses the dialog (RFC 3261 §12.2.1.2); a failed session refresh is retried before the session
// expires (RFC 4028 §10).
func (s *Session) clientFailure(c *clientInvite, res *sip.Response) {
	s.sdp.Response(c.key, res.StatusCode, nil)

	if c.out != nil {
		return
	}

	if s.invite == c {
		s.invite = nil
	}

	minSE, hasMinSE, _ := res.Header.MinSE()

	switch {
	case s.state == Terminated:
	case res.StatusCode == 401 || res.StatusCode == 407:
		if next, ok := s.challenged(c.req, res, c.tries); ok {
			ci := &clientInvite{s: s, req: next, key: keyOf(local, next), change: c.change, done: c.done,
				refresh: c.refresh, retried: c.retried, tries: c.tries + 1}
			s.sdp.Request(ci.key, sessionSDP(next.Envelope))

			if next.Method == "INVITE" {
				s.invite = ci
			}

			if err := ci.send(); err != nil {
				s.emitDone(ci.done, nil, err)
			}

			return
		}
	case res.StatusCode == 491:
		s.retryLater(c, glareDelay(s.uac))
		return
	case res.StatusCode == 422 && hasMinSE && !c.retried:
		s.timer.noteMinSE(minSE)
		c.retried = true
		s.retryLater(c, 0)

		return
	case res.StatusCode == 408 || res.StatusCode == 481:
		s.lost(res, nil)
	case c.refresh:
		s.timer.retry(s)
	}

	s.emitDone(c.done, res, nil)
}

// retryLater sends a re-INVITE or an UPDATE again after delay, with the same change.
func (s *Session) retryLater(c *clientInvite, delay time.Duration) {
	s.a.after(delay, func() {
		if s.state == Terminated {
			s.emitDone(c.done, nil, ErrState)
			return
		}

		if code, _ := s.conflict(c.key.Method, sessionSDPOf(c.change.Bodies) != nil, true); code != 0 {
			if c.refresh {
				s.timer.retry(s)
			}

			s.emitDone(c.done, nil, ErrPending)

			return
		}

		next, err := s.newClientInvite(c.key.Method, c.change, c.done)
		if err != nil {
			s.emitDone(c.done, nil, err)
			return
		}

		next.retried, next.refresh = c.retried, c.refresh

		if err := next.send(); err != nil {
			s.emitDone(c.done, nil, err)
		}
	})
}

// Offer is an offer of the peer in a response, to answer in the PRACK of a reliable 1xx or in the ACK of a 2xx.
type Offer struct {
	s     *Session
	sdp   []byte
	res   *sip.Response
	c     *clientInvite
	prack bool
	used  bool
}

func (o *Offer) SDP() []byte {
	return o.sdp
}

// Response is the response carrying the offer.
func (o *Offer) Response() *sip.Response {
	return o.res
}

// Answer sends the PRACK or the ACK with the answer, among bodies.
func (o *Offer) Answer(bodies ...sip.BodyPart) error {
	if sessionSDPOf(bodies) == nil {
		return errors.New("sip/ua: no session description in the answer")
	}

	return o.s.a.loop.call(func() error {
		if o.used {
			return nil
		}

		o.used = true
		s := o.s

		if o.prack {
			s.prack(o.res, bodies, nil)
		} else if _, waiting := s.answering[o.c.key.Seq]; waiting {
			s.sendAck(o.c, bodies)
		}

		return nil
	})
}

// Reject refuses the offer with an answer refusing its streams (RFC 3264 §6), which the PRACK or the ACK must carry
// (RFC 3262 §5, RFC 3261 §13.2.2.4); the INVITE is then cancelled, or the session ended with a BYE.
func (o *Offer) Reject() {
	_ = o.s.a.loop.call(func() error {
		o.reject()
		return nil
	})
}

func (o *Offer) reject() {
	if o.used {
		return
	}

	o.used = true
	s := o.s

	if o.prack {
		// RFC 3262 §5: the PRACK carries the answer; the CANCEL follows it.
		o.refuse(o.cancelInvite)
	} else if _, waiting := s.answering[o.c.key.Seq]; waiting {
		o.refuse(nil)
		s.byeEnd(End{Cause: EndBye})
	}
}

// refuse sends the PRACK or the ACK with an answer refusing every stream of the offer, then.
func (o *Offer) refuse(then func()) {
	var bodies []sip.BodyPart

	if refusal, err := sdp.Refusal(o.sdp); err == nil {
		bodies = []sip.BodyPart{SDP(refusal)}
	}

	if o.prack {
		o.s.prack(o.res, bodies, then)
		return
	}

	o.s.sendAck(o.c, bodies)
}

func (o *Offer) cancelInvite() {
	if o.c.out != nil {
		_ = o.c.out.cancel(nil)
		return
	}

	if o.c.tx != nil {
		_ = o.c.tx.Cancel()
	}
}
