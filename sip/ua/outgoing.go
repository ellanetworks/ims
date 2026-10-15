package ua

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/offeranswer"
	"github.com/ellanetworks/ims/sip/transaction"
)

// Outgoing is an initial INVITE of the agent, and the sessions its responses create: one for each dialog, early ones
// when it forks (RFC 3261 §13.2.2).
type Outgoing struct {
	a  *Agent
	h  Handler
	ev serial

	mu sync.Mutex

	// tmpl is the INVITE before Prepare, sent again after a 422 (RFC 4028 §7.3).
	tmpl *sip.Request
	ci   *clientInvite
	sdp  offeranswer.Negotiation
	tx   *transaction.ClientTransaction

	sessions map[string]*Session
	chosen   *Session
	final    bool
	retried  bool
	cancel   bool
}

// Invite sends an initial INVITE, and creates a session for each dialog its responses create, with handler h.
//
// req carries the Request-URI, To, the header fields of the application and its bodies: an SDP one is the offer;
// without one, the offer comes in a reliable 1xx or the 2xx. The agent adds what req leaves out: the From tag, a
// Call-ID, CSeq, Max-Forwards, Contact, and the header fields of 100rel and the session timer.
func (a *Agent) Invite(req *sip.Request, h Handler) (*Outgoing, error) {
	if req.Method != "INVITE" {
		return nil, fmt.Errorf("sip/ua: Invite of a %s request", req.Method)
	}

	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()

	if closed {
		return nil, ErrClosed
	}

	tmpl := req.Clone()
	if err := a.complete(tmpl); err != nil {
		return nil, err
	}

	o := &Outgoing{a: a, h: h, tmpl: tmpl, sessions: map[string]*Session{}}

	return o, o.send()
}

// complete adds to an initial INVITE the header fields the application left out.
func (a *Agent) complete(req *sip.Request) error {
	from, err := req.Header.From()
	if err != nil {
		return fmt.Errorf("sip/ua: From: %w", err)
	}

	if from.Tag() == "" {
		from.Params.Set("tag", sip.NewTag())
		req.Header.Set("From", from.String())
	}

	if !req.Header.Has("Call-ID") {
		req.Header.Set("Call-ID", sip.NewTag()+"@"+req.URI.Host)
	}

	if !req.Header.Has("CSeq") {
		req.Header.Set("CSeq", sip.CSeq{Seq: 1, Method: "INVITE"}.String())
	}

	if !req.Header.Has("Max-Forwards") {
		req.Header.Set("Max-Forwards", "70")
	}

	if !req.Header.Has("Contact") {
		if a.cfg.Contact.URI.Scheme == "" {
			return errors.New("sip/ua: no Contact")
		}

		req.Header.Set("Contact", a.cfg.Contact.String())
	}

	for _, tag := range a.supported() {
		if !req.Header.HasOptionTag("Supported", tag) {
			req.Header.Add("Supported", tag)
		}
	}

	if !req.Header.Has("Allow") {
		req.Header.Set("Allow", a.allowValue())
	}

	// RFC 4028 §7.1
	if iv := a.cfg.SessionTimer.Interval; iv > 0 && !req.Header.Has("Session-Expires") {
		req.Header.Set("Session-Expires", strconv.Itoa(int(iv/time.Second)))
	}

	return nil
}

// send sends the INVITE of the template.
func (o *Outgoing) send() error {
	req := o.tmpl.Clone()

	o.mu.Lock()
	o.ci = &clientInvite{out: o, req: req, key: keyOf(local, req)}
	o.sdp = offeranswer.Negotiation{}
	o.sdp.Request(o.ci.key, sessionSDP(req.Envelope))
	o.mu.Unlock()

	tx, err := o.a.send(req, o)
	if err != nil {
		return err
	}

	o.mu.Lock()
	o.tx = tx
	cancel := o.cancel
	o.mu.Unlock()

	if cancel {
		return tx.Cancel()
	}

	return nil
}

// Request is the INVITE as last sent.
func (o *Outgoing) Request() *sip.Request {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.ci.req
}

// Cancel cancels the INVITE, the CANCEL carrying reasons (RFC 3261 §9.1, RFC 3326). Its sessions end on the 487.
func (o *Outgoing) Cancel(reasons ...sip.Reason) error {
	o.mu.Lock()
	o.cancel = true
	tx, final := o.tx, o.final
	o.mu.Unlock()

	if tx == nil || final {
		return nil
	}

	var extra []sip.Field
	for _, r := range reasons {
		extra = append(extra, sip.Field{Name: "Reason", Value: r.String()})
	}

	return tx.Cancel(extra...)
}

// HandleResponse implements transaction.ClientHandler.
func (o *Outgoing) HandleResponse(res *sip.Response) {
	if res.StatusCode == 100 {
		return
	}

	to, err := res.Header.To()
	if err != nil {
		return
	}

	tag := to.Tag()

	o.mu.Lock()

	if res.StatusCode >= 300 {
		o.failed(res)
		return
	}

	if tag == "" {
		o.mu.Unlock()

		if f := o.h.Progress; f != nil && res.IsProvisional() {
			o.ev.do(func() { f(nil, res) })
		}

		return
	}

	s := o.sessionLocked(res, tag)
	if s == nil {
		o.mu.Unlock()
		return
	}

	c := o.ci
	chosen := true

	if res.IsSuccess() {
		if o.chosen == nil {
			o.chosen = s
			o.final = true
			o.a.clock.AfterFunc(64*o.a.layer.T1(), o.endEarly)
		}

		chosen = o.chosen == s
	}

	o.mu.Unlock()

	if chosen {
		s.clientResponse(c, res)
		return
	}

	s.notChosen(c, res)
}

// sessionLocked is the session of the dialog of a response, created by its first response (RFC 3261 §12.1.2).
func (o *Outgoing) sessionLocked(res *sip.Response, tag string) *Session {
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

	o.sessions[tag] = s
	o.a.add(s)

	return s
}

// failed handles a final response other than 2xx, under the lock of o: a 422 is retried with its Min-SE once
// (RFC 4028 §7.3); otherwise the sessions end.
func (o *Outgoing) failed(res *sip.Response) {
	if o.final {
		o.mu.Unlock()
		return
	}

	if minSE, ok, err := res.Header.MinSE(); res.StatusCode == 422 && ok && err == nil && !o.retried && !o.cancel {
		o.retried = true
		o.retryLocked(minSE)

		sessions := o.takeSessionsLocked()
		o.mu.Unlock()

		for _, s := range sessions {
			s.end(End{Cause: EndRejected, Remote: true, Response: res})
		}

		if err := o.send(); err != nil {
			o.ev.do(func() {
				if f := o.h.Failed; f != nil {
					f(nil, err)
				}
			})
		}

		return
	}

	o.final = true
	sessions := o.takeSessionsLocked()
	cancelled := o.cancel && res.StatusCode == 487
	o.mu.Unlock()

	e := End{Cause: EndRejected, Remote: true, Response: res}
	if cancelled {
		e = End{Cause: EndCancelled, Response: res}
	}

	for _, s := range sessions {
		s.end(e)
	}

	o.ev.do(func() {
		if f := o.h.Failed; f != nil {
			f(res, nil)
		}
	})
}

// retryLocked prepares the INVITE sent again after a 422: the next CSeq, the Min-SE, and a session interval at
// least as long (RFC 4028 §7.4).
func (o *Outgoing) retryLocked(minSE uint32) {
	cseq, _ := o.tmpl.Header.CSeq()
	o.tmpl.Header.Set("CSeq", sip.CSeq{Seq: cseq.Seq + 1, Method: "INVITE"}.String())
	o.tmpl.Header.Set("Min-SE", strconv.FormatUint(uint64(minSE), 10))

	if se, ok, err := o.tmpl.Header.SessionExpires(); !ok || err != nil || se.Seconds < minSE {
		o.tmpl.Header.Set("Session-Expires", strconv.FormatUint(uint64(minSE), 10))
	}
}

func (o *Outgoing) takeSessionsLocked() []*Session {
	var out []*Session

	for tag, s := range o.sessions {
		if s != o.chosen {
			out = append(out, s)
			delete(o.sessions, tag)
		}
	}

	return out
}

// endEarly ends, once the INVITE transaction is over, the early dialogs no 2xx confirmed (RFC 3261 §13.2.2.4).
func (o *Outgoing) endEarly() {
	o.mu.Lock()
	sessions := o.takeSessionsLocked()
	o.mu.Unlock()

	for _, s := range sessions {
		s.end(End{Cause: EndNotChosen})
	}
}

// HandleError implements transaction.ClientHandler.
func (o *Outgoing) HandleError(err error) {
	o.mu.Lock()

	if o.final {
		o.mu.Unlock()
		return
	}

	o.final = true
	sessions := o.takeSessionsLocked()
	o.mu.Unlock()

	for _, s := range sessions {
		s.end(End{Cause: EndTimeout, Err: err})
	}

	o.ev.do(func() {
		if f := o.h.Failed; f != nil {
			f(nil, err)
		}
	})
}

func (s *Session) end(e End) {
	s.mu.Lock()
	s.endLocked(e)
	s.unlock()
}

// notChosen acknowledges the 2xx of another dialog of a forked INVITE than the one answered first, and ends it
// (RFC 3261 §13.2.2.4).
func (s *Session) notChosen(c *clientInvite, res *sip.Response) {
	s.mu.Lock()

	if _, seen := s.acks[c.key.Seq]; seen {
		s.resendAckLocked(c.key.Seq)
		s.unlock()

		return
	}

	_ = s.d.ReceiveResponse(res)
	s.ackLocked(c, nil)
	s.byeLocked(End{Cause: EndNotChosen, Response: res})
	s.unlock()
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

	// retried is set once a 422 was retried (RFC 4028 §7.3), final once a final response came: an error of the
	// transaction after it, as on closing, is no failure of the request.
	retried bool
	final   bool
}

func (c *clientInvite) send() error {
	tx, err := c.s.a.send(c.req, c)
	if err != nil {
		c.s.mu.Lock()
		c.s.sdp.Fail(c.key)

		if c.s.invite == c {
			c.s.invite = nil
		}

		c.s.unlock()

		return err
	}

	c.s.mu.Lock()
	c.tx = tx
	c.s.unlock()

	return nil
}

// HandleResponse implements transaction.ClientHandler, for a re-INVITE or an UPDATE.
func (c *clientInvite) HandleResponse(res *sip.Response) {
	if res.StatusCode == 100 {
		return
	}

	c.s.clientResponse(c, res)
}

// HandleError implements transaction.ClientHandler, for a re-INVITE or an UPDATE.
func (c *clientInvite) HandleError(err error) {
	s := c.s
	s.mu.Lock()

	if c.final {
		s.unlock()
		return
	}

	c.final = true
	s.sdp.Fail(c.key)

	if s.invite == c {
		s.invite = nil
	}

	if errors.Is(err, transaction.ErrTimeout) {
		s.byeLocked(End{Cause: EndLost, Err: err})
	}

	if c.done != nil {
		done := c.done
		s.emit(func() { done(nil, err) })
	}

	s.unlock()
}

// clientResponse handles a response to an INVITE or an UPDATE of the agent, in the dialog of s.
func (s *Session) clientResponse(c *clientInvite, res *sip.Response) {
	s.mu.Lock()

	if s.d != nil {
		_ = s.d.ReceiveResponse(res)
	}

	s.notePeerLocked(res.Header)

	if res.StatusCode >= 200 && c.out == nil {
		if c.final && !(res.IsSuccess() && c.key.Method == "INVITE") {
			s.unlock()
			return
		}

		c.final = true
	}

	switch {
	case res.IsProvisional():
		s.provisionalLocked(c, res)
	case res.IsSuccess():
		s.successLocked(c, res)
	default:
		s.failureLocked(c, res)
	}

	s.unlock()
}

// provisionalLocked handles a provisional response to an INVITE: a reliable one in order gets its PRACK, with the
// answer to its offer (RFC 3262 §4, §5).
func (s *Session) provisionalLocked(c *clientInvite, res *sip.Response) {
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

	sdp := sessionSDP(res.Envelope)

	switch s.sdp.Response(c.key, res.StatusCode, sdp) {
	case offeranswer.Answer:
		s.emitAnswer(sdp)
		s.prackLocked(res, nil)
	case offeranswer.Offer:
		s.emitOffer(&Offer{s: s, sdp: sdp, res: res, c: c, prack: true})
	default:
		s.prackLocked(res, nil)
	}

	if c.out != nil {
		s.emitProgress(res)
	}
}

// prackLocked sends the PRACK of a reliable 1xx, with bodies.
func (s *Session) prackLocked(res *sip.Response, bodies []sip.BodyPart) {
	prack, err := s.d.NewPrack(res)
	if err != nil {
		return
	}

	prack.SetBodies(bodies...)
	s.sdp.Request(keyOf(local, prack), sessionSDPOf(bodies))
	s.sendLater(prack, nil)
}

// successLocked handles a 2xx to an INVITE or an UPDATE of the agent.
func (s *Session) successLocked(c *clientInvite, res *sip.Response) {
	if c.key.Method == "INVITE" {
		if _, seen := s.acks[c.key.Seq]; seen {
			s.resendAckLocked(c.key.Seq)
			return
		}
	}

	if s.state == Terminated {
		// The session ended while its INVITE was in progress: the 2xx is acknowledged, and the dialog ended.
		if c.key.Method == "INVITE" {
			s.ackLocked(c, nil)

			if bye, err := s.d.NewRequest("BYE"); err == nil {
				s.sendLater(bye, nil)
			}
		}

		return
	}

	sdp := sessionSDP(res.Envelope)
	role := s.sdp.Response(c.key, res.StatusCode, sdp)

	s.timer.fromResponse(s.a, c.req, res)
	s.timer.startLocked(s)

	if c.key.Method == "INVITE" && s.state == Early {
		s.state = Confirmed
		s.emitEstablished()
	}

	if role == offeranswer.Answer {
		s.emitAnswer(sdp)
	}

	if c.key.Method == "INVITE" {
		if s.invite == c {
			s.invite = nil
		}

		if role == offeranswer.Offer {
			s.acks[c.key.Seq] = nil
			s.emitOffer(&Offer{s: s, sdp: sdp, res: res, c: c})
		} else {
			s.ackLocked(c, nil)
		}
	}

	if c.done != nil {
		done := c.done
		s.emit(func() { done(res, nil) })
	}
}

// ackLocked sends the ACK of the 2xx to an INVITE, with bodies, and keeps it for the retransmissions of the 2xx
// (RFC 3261 §13.2.2.4).
func (s *Session) ackLocked(c *clientInvite, bodies []sip.BodyPart) {
	ack, err := s.d.NewAck(c.req)
	if err != nil {
		return
	}

	ack.SetBodies(bodies...)

	if sdp := sessionSDPOf(bodies); sdp != nil {
		s.sdp.Request(offeranswer.Key{From: local, Seq: c.key.Seq, Method: "ACK"}, sdp)
	}

	if err := s.a.cfg.Prepare(ack); err != nil {
		s.a.log.Debug("prepare ACK failed", slog.Any("error", err))
		return
	}

	s.acks[c.key.Seq] = ack
	s.resendAckLocked(c.key.Seq)
}

func (s *Session) resendAckLocked(seq uint32) {
	ack := s.acks[seq]
	if ack == nil {
		return
	}

	ack = ack.Clone()

	s.later(func() {
		if err := s.a.layer.SendAck(context.Background(), ack); err != nil {
			s.a.log.Debug("ACK failed", slog.Any("error", err))
		}
	})
}

// failureLocked handles a final response other than 2xx to a re-INVITE or an UPDATE of the agent: a 491 is retried
// after a random delay (RFC 3261 §14.1), a 422 with the Min-SE (RFC 4028 §7.3), a 408 or 481 loses the dialog
// (RFC 3261 §12.2.1.2).
func (s *Session) failureLocked(c *clientInvite, res *sip.Response) {
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
	case res.StatusCode == 491:
		s.retryLater(c, glareDelay(s.uac))
		return
	case res.StatusCode == 422 && hasMinSE && !c.retried:
		s.timer.noteMinSE(minSE)
		c.retried = true
		s.retryLater(c, 0)

		return
	case res.StatusCode == 408 || res.StatusCode == 481:
		e := End{Cause: EndLost, Response: res}
		if res.StatusCode == 481 {
			s.endLocked(e)
		} else {
			s.byeLocked(e)
		}
	}

	if c.done != nil {
		done := c.done
		s.emit(func() { done(res, nil) })
	}
}

// retryLater sends a re-INVITE or an UPDATE again after delay, with the same change.
func (s *Session) retryLater(c *clientInvite, delay time.Duration) {
	s.a.clock.AfterFunc(delay, func() {
		s.mu.Lock()

		fail := func(err error) {
			if c.done != nil {
				done := c.done
				s.emit(func() { done(nil, err) })
			}

			s.unlock()
		}

		if s.state == Terminated {
			fail(ErrState)
			return
		}

		if code, _ := s.conflictLocked(c.key.Method, sessionSDPOf(c.change.Bodies) != nil); code != 0 {
			fail(ErrPending)
			return
		}

		next, err := s.newClientInviteLocked(c.key.Method, c.change, c.done)
		if err != nil {
			fail(err)
			return
		}

		next.retried = c.retried

		s.unlock()

		if err := next.send(); err != nil && c.done != nil {
			s.ev.do(func() { c.done(nil, err) })
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
	once  sync.Once
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

	o.once.Do(func() {
		s := o.s
		s.mu.Lock()

		if o.prack {
			s.prackLocked(o.res, bodies)
		} else {
			s.ackLocked(o.c, bodies)
		}

		s.unlock()
	})

	return nil
}

// Reject refuses the offer: the PRACK goes without an answer and the INVITE is cancelled, or the ACK goes without
// an answer and the session ends with a BYE (RFC 3261 §13.2.2.4).
func (o *Offer) Reject() {
	o.once.Do(func() {
		s := o.s
		s.mu.Lock()

		if o.prack {
			s.prackLocked(o.res, nil)
			s.later(func() { o.cancelInvite() })
		} else {
			s.ackLocked(o.c, nil)
			s.byeLocked(End{Cause: EndBye})
		}

		s.unlock()
	})
}

func (o *Offer) cancelInvite() {
	if o.c.out != nil {
		_ = o.c.out.Cancel()
		return
	}

	o.s.mu.Lock()
	tx := o.c.tx
	o.s.unlock()

	if tx != nil {
		_ = tx.Cancel()
	}
}
