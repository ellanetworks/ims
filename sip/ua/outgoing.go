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
	"go.opentelemetry.io/otel/trace"
)

// RFC 4028 section 7.3
const maxMinSERetries = 4

// RFC 3261 section 13.2.2
type Outgoing struct {
	a      *Agent
	st     *Strand
	h      Handler
	policy Policy

	tmpl  *sip.Request
	ci    *clientInvite
	sdp   offeranswer.Negotiation
	tx    *transaction.ClientTransaction
	tries int

	sessions map[string]*Session
	chosen   *Session
	final    bool

	minSE   uint32
	retries int

	cancel      *Change
	cancelCause trace.SpanContext
	origin      trace.SpanContext

	closing bool
	closeBy *timer
}

func (o *Outgoing) awaitFinal() {
	if o.final || o.closing {
		return
	}

	o.closing = true
	o.a.await(1)
	o.closeBy = o.st.after(64*o.a.layer.T1(), o.release)
}

func (o *Outgoing) release() {
	if o.closing {
		o.closing = false
		o.closeBy.stop()
		o.a.await(-1)
	}
}

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

	tags := slices.DeleteFunc(append(slices.Clone(a.supported), sip.OptionTag199), func(tag string) bool {
		return containsFold(op.without, tag)
	})
	mergeSupported(&tmpl.Header, tags)

	if !tmpl.Header.Has("Allow") {
		tmpl.Header.Set("Allow", a.allowValue())
	}

	if iv := a.cfg.SessionTimer.Interval; iv > 0 && !tmpl.Header.Has("Session-Expires") {
		tmpl.Header.Set("Session-Expires", strconv.Itoa(int(iv/time.Second)))
	}

	if minSE := seconds(a.cfg.SessionTimer.MinSE); minSE > minSessionExpires && !tmpl.Header.Has("Min-SE") {
		tmpl.Header.Set("Min-SE", strconv.FormatUint(uint64(minSE), 10))
	}

	o := &Outgoing{
		a: a, st: op.strand(a), h: h, policy: op.policyOr(a.cfg.Policy), tmpl: tmpl, sessions: map[string]*Session{},
		origin: trace.SpanContextFromContext(ctx),
	}

	if err := a.addOutgoing(o); err != nil {
		return nil, err
	}

	if err := o.st.do(func() error { return o.send(ctx) }); err != nil {
		a.removeOutgoing(o)
		return nil, err
	}

	return o, nil
}

func (o *Outgoing) Strand() *Strand {
	return o.st
}

func (o *Outgoing) SetPolicy(p Policy) {
	_ = o.st.do(func() error {
		o.policy = p
		return nil
	})
}

func (o *Outgoing) done() {
	o.a.removeOutgoing(o)
}

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
		return tx.CancelWith(trace.ContextWithSpanContext(context.Background(), o.cancelCause), o.cancel.apply)
	}

	return nil
}

func (o *Outgoing) Request() *sip.Request {
	return get(o.st, func() *sip.Request {
		if o.tx != nil {
			return o.tx.Request()
		}

		return o.ci.req
	})
}

func (o *Outgoing) Cancel(ctx context.Context, c Change) error {
	return o.st.do(func() error {
		if o.final {
			return &Error{Op: "Cancel", State: Confirmed, Err: ErrState}
		}

		return o.cancelWith(ctx, c)
	})
}

func (o *Outgoing) cancelWith(ctx context.Context, c Change) error {
	if o.cancel != nil {
		return nil
	}

	o.cancel = &c
	o.cancelCause = trace.SpanContextFromContext(ctx)

	if o.tx == nil || o.final {
		return nil
	}

	return o.tx.CancelWith(ctx, c.apply)
}

func (o *Outgoing) emitFailed(ctx context.Context, res *sip.Response, err error) {
	o.st.emit(func() {
		if f := o.h.Failed; f != nil {
			f(ctx, o, res, err)
		}
	})
}

func (o *Outgoing) response(ctx context.Context, res *sip.Response) {
	if res.StatusCode == 100 {
		o.report(ctx, nil, res)
		return
	}

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

	if _, seen := s.acks[o.ci.key.Seq]; !seen || !res.IsSuccess() {
		s.emitReceived(ctx, res)
	}

	if res.IsSuccess() && o.chosen == nil {
		o.chosen = s
		o.final = true
		o.st.later(ctx, 64*o.a.layer.T1(), o.endEarly)

		o.done()
	}

	if res.IsSuccess() && o.chosen != s {
		o.forked(ctx, s, res)
		return
	}

	if res.IsSuccess() && o.cancel != nil && s.state == Early {
		s.clientResponse(ctx, o.ci, res)
		s.byeEnd(ctx, End{Cause: EndCancelled, Response: res}, Change{Header: reasonFields(o.cancel.Header)}, nil)

		return
	}

	s.clientResponse(ctx, o.ci, res)
}

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

func reasonFields(h sip.Header) sip.Header {
	var out sip.Header

	for _, f := range h {
		if sip.LongName(f.Name) == "Reason" {
			out = append(out, f)
		}
	}

	return out
}

// RFC 6228 section 4
// RFC 6228 section 8
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

// RFC 3261 section 13.2.2.4
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

// RFC 3261 section 12.1.2
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
	o.a.addSession(s, o.origin)

	return s
}

// RFC 3261 section 22.2
// RFC 4028 section 7.3
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

func (o *Outgoing) endSessions(ctx context.Context, e End) {
	for tag, s := range o.sessions {
		if s != o.chosen && s.state == Early {
			delete(o.sessions, tag)
			s.end(ctx, e)
		}
	}
}

// RFC 3261 section 13.2.2.4
func (o *Outgoing) endEarly(ctx context.Context) {
	o.endSessions(ctx, End{Cause: EndNotChosen})
	o.done()
}

// RFC 3261 section 13.2.2.4
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

type clientInvite struct {
	s   *Session
	out *Outgoing
	m   *modification
	req *sip.Request
	key offeranswer.Key
	tx  *transaction.ClientTransaction

	tries int
	final bool

	cancelled   func(*sip.Request)
	cancelCause trace.SpanContext
}

func (c *clientInvite) send(ctx context.Context) error {
	s := c.s

	err := s.transmit(ctx, c.req, s.st.client(c.response, c.error), func(tx *transaction.ClientTransaction) {
		c.tx = tx

		if c.cancelled != nil {
			_ = tx.CancelWith(trace.ContextWithSpanContext(context.Background(), c.cancelCause), c.cancelled)
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

// RFC 3261 section 9.1
func (c *clientInvite) cancel(ctx context.Context, ch Change) {
	if c.cancelled != nil {
		return
	}

	c.cancelCause = trace.SpanContextFromContext(ctx)

	c.cancelled = func(cancel *sip.Request) {
		ch.apply(cancel)
		c.s.decorate(cancel)
	}

	if c.tx != nil {
		_ = c.tx.CancelWith(ctx, c.cancelled)
	}
}

func (c *clientInvite) response(ctx context.Context, res *sip.Response) {
	if res.StatusCode == 100 {
		c.s.emitReceived(ctx, res)
		return
	}

	c.s.clientResponse(ctx, c, res)
}

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

func (s *Session) clientResponse(ctx context.Context, c *clientInvite, res *sip.Response) {
	if res.StatusCode >= 200 && c.out == nil {
		if c.final && (!res.IsSuccess() || c.key.Method != "INVITE") {
			return
		}

		c.final = true
	}

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

// RFC 3262 section 4
// RFC 3262 section 5
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

	if last, seen := s.rseq[c.key.Seq]; seen && rseq != last+1 {
		return
	}

	s.rseq[c.key.Seq] = rseq

	offer := sessionSDP(res.Envelope)

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

// RFC 3261 section 22.2
// RFC 3261 section 12.2.1.2
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

func (s *Session) clientSuccess(ctx context.Context, c *clientInvite, res *sip.Response) {
	if c.key.Method == "INVITE" {
		if _, seen := s.acks[c.key.Seq]; seen {
			s.resendAck(ctx, c.key.Seq)
			return
		}
	}

	offer := sessionSDP(res.Envelope)

	if s.state == Terminated {
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

// RFC 3261 section 13.3.1.4
const answerDeadline = 32

// RFC 3261 section 13.2.2.4
// RFC 6026
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

	if err := s.a.cfg.Prepare(ctx, ack); err != nil {
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

// RFC 3261 section 13.3.1.4
// RFC 3261 section 13.2.2.4
func (s *Session) ackFailed(ctx context.Context, err error) {
	s.logger().Warn("ACK not sent", slog.Any("error", err))

	s.st.post(func() {
		if s.state != Terminated {
			s.byeEnd(ctx, End{Cause: EndFailed, Err: fmt.Errorf("sip/ua: ACK not sent: %w", err)}, Change{}, nil)
		}
	})
}

func (s *Session) resendAck(ctx context.Context, seq uint32) {
	if ack := s.acks[seq]; ack != nil {
		s.queueAck(ctx, ack.Clone())
	}
}

type Offer struct {
	s       *Session
	sdp     []byte
	res     *sip.Response
	c       *clientInvite
	prack   bool
	initial bool
	used    bool

	progress func(context.Context)

	deadline *timer
}

func (o *Offer) SDP() []byte {
	return o.sdp
}

func (o *Offer) Response() *sip.Response {
	return o.res
}

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

// RFC 3264 section 6
// RFC 3262 section 5
// RFC 3261 section 13.2.2.4
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
		o.refuse(ctx, o.cancelInvite)
	case waiting && o.initial:
		o.refuse(ctx, nil)
		s.byeEnd(ctx, End{Cause: EndBye}, Change{}, nil)
	case waiting:
		o.refuse(ctx, nil)
	}
}

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

func (o *Offer) cancelInvite(ctx context.Context) {
	if o.c.out != nil {
		_ = o.c.out.cancelWith(ctx, Change{})
		return
	}

	o.c.cancel(ctx, Change{})
}
