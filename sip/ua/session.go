package ua

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/offeranswer"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	local  = offeranswer.A
	remote = offeranswer.B
)

type State int

const (
	Early State = iota
	Confirmed
	Terminated
)

func (s State) String() string {
	switch s {
	case Early:
		return "early"
	case Confirmed:
		return "confirmed"
	case Terminated:
		return "terminated"
	}

	return "State(?)"
}

type EndCause int

const (
	EndBye EndCause = iota
	EndCancelled
	EndRejected
	EndNotChosen
	EndTimeout
	EndLost
	EndExpired
	EndClosed
	EndTerminated
	EndNoNotify
	EndUnsubscribed
	EndDropped
	EndNoSubscription
	EndRemoved
	EndFailed
	EndTransport
)

func failureEnd(err error) End {
	switch {
	case errors.Is(err, transaction.ErrDropped):
		return End{Cause: EndDropped, Err: err}
	case errors.Is(err, transaction.ErrTransport):
		return End{Cause: EndTransport, Err: err}
	}

	return End{Cause: EndTimeout, Err: err}
}

func (c EndCause) String() string {
	switch c {
	case EndBye:
		return "bye"
	case EndCancelled:
		return "cancelled"
	case EndRejected:
		return "rejected"
	case EndNotChosen:
		return "not chosen"
	case EndTimeout:
		return "timeout"
	case EndLost:
		return "lost"
	case EndExpired:
		return "expired"
	case EndClosed:
		return "closed"
	case EndTerminated:
		return "terminated"
	case EndNoNotify:
		return "no NOTIFY"
	case EndUnsubscribed:
		return "unsubscribed"
	case EndDropped:
		return "dropped"
	case EndNoSubscription:
		return "no subscription"
	case EndRemoved:
		return "removed"
	case EndFailed:
		return "failed"
	case EndTransport:
		return "transport error"
	}

	return "EndCause(?)"
}

type End struct {
	Cause EndCause

	Remote bool

	Request *sip.Request

	State sip.SubscriptionState

	Response *sip.Response

	Err error
}

// RFC 3326
func (e End) Reasons() []sip.Reason {
	if e.Request != nil {
		if rs, _ := e.Request.Header.Reasons(); len(rs) > 0 {
			return rs
		}
	}

	if e.Response != nil {
		if r, err := sip.NewReason("SIP", e.Response.StatusCode, e.Response.Reason); err == nil {
			return []sip.Reason{r}
		}
	}

	return nil
}

// RFC 5057
type Session struct {
	a   *Agent
	st  *Strand
	uac bool
	id  dialog.ID

	h Handler

	handlerSet bool
	policy     Policy
	d          *dialog.Dialog
	state      State
	sdp        offeranswer.Negotiation
	value      any

	exchanges uint64

	contact string

	out *Outgoing

	in       *Request
	inUpdate *Request
	acking   map[uint32]*Request
	inBye    *Request

	reinvite *clientInvite
	update   *clientInvite
	queue    []*modification
	pumping  bool
	repump   bool

	sends map[*clientRequest]struct{}
	open  map[*Request]struct{}

	rseq      map[uint32]uint32
	acks      map[uint32]*sip.Request
	answering map[uint32]*Offer

	byeAfterAck *pendingBye
	byeSent     bool

	peerAllow []string
	timer     sessionTimer

	unreported []*sip.Response

	log *slog.Logger
}

type pendingBye struct {
	change Change
	done   func(*sip.Response, error)
	cause  EndCause
}

func newSession(a *Agent, st *Strand, h Handler, uac bool, id dialog.ID, p Policy) *Session {
	return &Session{
		a: a, st: st, h: h, uac: uac, id: id, policy: p, sdp: offeranswer.Negotiation{KeepFirstAnswer: true},
		rseq: map[uint32]uint32{}, acks: map[uint32]*sip.Request{}, answering: map[uint32]*Offer{},
		acking: map[uint32]*Request{},
		sends:  map[*clientRequest]struct{}{}, open: map[*Request]struct{}{},
	}
}

func (s *Session) Strand() *Strand {
	return s.st
}

func (s *Session) ID() dialog.ID {
	return s.id
}

func (s *Session) UAC() bool {
	return s.uac
}

func (s *Session) State() State {
	return get(s.st, func() State { return s.state })
}

func (s *Session) ByePending() bool {
	return get(s.st, func() bool { return s.inBye != nil })
}

func (s *Session) Value() any {
	return get(s.st, func() any { return s.value })
}

func (s *Session) SetValue(v any) {
	_ = s.st.do(func() error {
		s.value = v
		return nil
	})
}

func (s *Session) SetHandler(h Handler) {
	_ = s.st.do(func() error {
		s.h = h
		s.handlerSet = true

		return nil
	})
}

func (s *Session) Policy() Policy {
	return get(s.st, func() Policy { return s.policy })
}

func (s *Session) SetPolicy(p Policy) {
	_ = s.st.do(func() error {
		s.policy = p
		return nil
	})
}

func (s *Session) Dialog() (dialog.Snapshot, bool) {
	var (
		snap dialog.Snapshot
		ok   bool
	)

	_ = s.st.do(func() error {
		if s.d != nil {
			snap, ok = s.d.Snapshot(), true
		}

		return nil
	})

	return snap, ok
}

func (s *Session) Negotiated() (localSDP, remoteSDP []byte, ok bool) {
	_ = s.st.do(func() error {
		localSDP, remoteSDP, ok = s.negotiated()
		return nil
	})

	return localSDP, remoteSDP, ok
}

func (s *Session) negotiated() (localSDP, remoteSDP []byte, ok bool) {
	offer, answer, ok := s.sdp.Session()
	if !ok {
		return nil, nil, false
	}

	if offer.From == local {
		return offer.Data, answer.Data, true
	}

	return answer.Data, offer.Data, true
}

// RFC 4028
func (s *Session) SessionTimer() (interval time.Duration, refresher bool) {
	_ = s.st.do(func() error {
		interval, refresher = time.Duration(s.timer.interval)*time.Second, s.timer.refresher
		return nil
	})

	return interval, refresher
}

func (s *Session) errorf(op string, err error) error {
	return &Error{Op: op, State: s.state, Err: err}
}

func (s *Session) usable() bool {
	return s.d != nil && s.state != Terminated && s.inBye == nil
}

// RFC 6141 section 3.3
func (s *Session) offerRequest(key offeranswer.Key, sdp []byte) offeranswer.Role {
	return s.exchanged(key, s.sdp.Request(key, sdp))
}

func (s *Session) offerResponse(key offeranswer.Key, code int, sdp []byte) offeranswer.Role {
	return s.exchanged(key, s.sdp.Response(key, code, sdp))
}

func (s *Session) exchanged(key offeranswer.Key, role offeranswer.Role) offeranswer.Role {
	if role != offeranswer.Answer {
		return role
	}

	s.exchanges++

	if r := s.in; r != nil && r != s.initial() && key != r.key {
		r.applied = true
	}

	return role
}

func (s *Session) decorate(m sip.Message) {
	if s.h.Decorate != nil {
		s.h.Decorate(s, m)
	}
}

func (s *Session) reply(tx *transaction.ServerTransaction, code int, extra ...sip.Field) {
	res := sip.NewResponse(tx.Request(), code, "")
	for _, f := range extra {
		res.Header.Add(f.Name, f.Value)
	}

	s.decorate(res)

	if err := s.a.respond(tx, res); err != nil {
		s.logger().Debug("response failed", slog.Int("code", code), slog.Any("error", err))
	}
}

func (s *Session) emit(f func(h Handler)) {
	s.st.emit(func() { f(s.h) })
}

func (s *Session) emitReceived(ctx context.Context, m sip.Message) {
	s.emit(func(h Handler) {
		if h.Received != nil {
			h.Received(ctx, s, m)
		}
	})
}

func (s *Session) emitEnded(ctx context.Context, e End) {
	s.emit(func(h Handler) {
		if h.Ended != nil {
			h.Ended(ctx, s, e)
		}
	})
}

func (s *Session) emitAnswer(ctx context.Context, sdp []byte) {
	s.emit(func(h Handler) {
		if h.Answer != nil {
			h.Answer(ctx, s, sdp)
		}
	})
}

func (s *Session) emitEstablished(ctx context.Context, m sip.Message) {
	s.emit(func(h Handler) {
		if h.Established != nil {
			h.Established(ctx, s, m)
		}
	})
}

func (s *Session) emitProgress(ctx context.Context, res *sip.Response) {
	s.emit(func(h Handler) {
		if h.Progress != nil {
			h.Progress(ctx, s, res)
		}
	})
}

func (s *Session) emitOffer(ctx context.Context, o *Offer) {
	s.emit(func(h Handler) {
		if h.Offer != nil {
			h.Offer(ctx, s, o)
			return
		}

		o.Reject(ctx)
	})
}

func (s *Session) emitDone(done func(*sip.Response, error), res *sip.Response, err error) {
	if done != nil {
		s.st.emit(func() { done(res, err) })
	}
}

// RFC 4028 section 7.2
// RFC 4028 section 9
func (s *Session) confirm(ctx context.Context, m sip.Message) {
	s.state = Confirmed
	s.timer.start(ctx, s)
	s.emitEstablished(ctx, m)
}

// RFC 3261 section 15.1.2
func (s *Session) end(ctx context.Context, e End) {
	s.st.check()

	if s.state == Terminated {
		return
	}

	initial := s.initial()

	s.state = Terminated
	s.timer.stop()

	s.dropModifications(s.errorf("modification", ErrState))

	for c := range s.sends {
		c.final = true
		s.emitDone(c.done, nil, s.errorf("Send", ErrState))
	}

	s.sends = nil

	abandon := func(r *Request) {
		code := 487
		if e.Cause == EndClosed && r == initial {
			code = 503
		}

		if r == initial && len(r.others()) > 0 {
			r.leave()
			return
		}

		r.abandon(code)
	}

	for _, r := range []*Request{s.in, s.inUpdate} {
		if r != nil {
			abandon(r)
		}
	}

	for r := range s.open {
		abandon(r)
	}

	if r := s.inBye; r != nil {
		s.inBye = nil

		r.deadline.stop()

		if res, err := r.build(Reply{Code: 200}); err == nil {
			r.complete(res)
			r.final = true
			_ = r.tx.Respond(res)
		}

		if e.Cause != EndBye {
			e = End{Cause: EndBye, Remote: true, Request: r.req, Err: fmt.Errorf("sip/ua: %s as the BYE of the peer was held", e.Cause)}
		}
	}

	for seq, r := range s.acking {
		r.acked()
		delete(s.acking, seq)
	}

	if pb := s.byeAfterAck; pb != nil {
		s.byeAfterAck = nil

		s.emitDone(pb.done, nil, s.errorf("Bye", ErrState))

		if pb.cause == EndClosed {
			s.a.await(-1)
		}
	}

	s.a.removeSession(s)

	s.emitEnded(ctx, e)
}

func (s *Session) awaitingAck() bool {
	return len(s.acking) > 0
}

func (s *Session) notePeer(h sip.Header) {
	if h.Has("Allow") {
		s.peerAllow = h.Elements("Allow")
	}
}

func (s *Session) peerAllows(method string) bool {
	return slices.Contains(s.peerAllow, method)
}

func (s *Session) transmit(ctx context.Context, req *sip.Request, h transaction.ClientHandler, sent func(*transaction.ClientTransaction)) error {
	s.st.check()

	out := req
	if s.h.Decorate != nil {
		out = req.Clone()
		s.decorate(out)
	}

	tx, err := s.a.prepareAndSend(ctx, out, h)
	if err == nil && sent != nil {
		sent(tx)
	}

	return err
}

func (s *Session) queueAck(ctx context.Context, ack *sip.Request) {
	if err := s.a.layer.QueueAck(ctx, ack, func(err error) {
		if err != nil {
			s.logger().Debug("ACK failed", slog.Any("error", err))
		}
	}); err != nil {
		s.logger().Debug("ACK not sent", slog.Any("error", err))
	}
}

func (s *Session) sendRequest(ctx context.Context, req *sip.Request, h transaction.ClientHandler) {
	if err := s.transmit(ctx, req, h, nil); err != nil {
		s.logger().Debug("request failed", slog.String("method", req.Method), slog.Any("error", err))

		if h != nil {
			h.HandleError(ctx, err)
		}
	}
}

// RFC 3261 section 13.2.2.4
func (s *Session) byeEnd(ctx context.Context, e End, c Change, done func(*sip.Response, error)) {
	for seq, o := range s.answering {
		o.used = true
		o.refuse(ctx, nil)
		delete(s.answering, seq)
	}

	if s.d != nil && s.state != Terminated && s.inBye == nil {
		bye := &clientRequest{s: s, method: "BYE", change: c, done: done, bye: true}
		s.byeSent = true

		s.a.await(1)

		if err := bye.send(ctx); err != nil {
			bye.finish(ctx, nil, err)
		}
	} else {
		s.emitDone(done, nil, s.errorf("Bye", ErrState))
	}

	s.end(ctx, e)
}

func (s *Session) close(ctx context.Context) {
	switch {
	case s.state == Terminated:
	case s.state == Early && s.awaitingAck() && s.byeAfterAck == nil:
		s.a.await(1)
		s.byeAfterAck = &pendingBye{cause: EndClosed}
	case s.state == Early && s.awaitingAck():
		if s.byeAfterAck.cause != EndClosed {
			s.byeAfterAck.cause = EndClosed
			s.a.await(1)
		}
	case s.state == Confirmed:
		s.byeEnd(ctx, End{Cause: EndClosed}, Change{}, nil)
	default:
		s.end(ctx, End{Cause: EndClosed})
	}
}

func (s *Session) sendQueuedBye(ctx context.Context, cause EndCause) {
	pb := s.byeAfterAck
	if pb == nil {
		return
	}

	s.byeAfterAck = nil

	if cause == EndBye {
		cause = pb.cause
	}

	s.byeEnd(ctx, End{Cause: cause, Err: errorOf(cause)}, pb.change, pb.done)

	if pb.cause == EndClosed {
		s.a.await(-1)
	}
}

func errorOf(cause EndCause) error {
	if cause == EndTimeout {
		return ErrNoAck
	}

	return nil
}

// RFC 5621
// TS 24.379 clause 6.5
func sessionSDP(e sip.Envelope) []byte {
	p, ok, err := e.Content().FindFunc(sip.BodyPart.IsSession)
	if err != nil || !ok || len(p.Body) == 0 {
		return nil
	}

	return p.Body
}

func sessionSDPOf(parts []sip.BodyPart) []byte {
	for _, p := range parts {
		if p.IsSession() && len(p.Body) > 0 {
			return p.Body
		}
	}

	return nil
}

func onlySDP(e sip.Envelope) bool {
	parts, err := e.Content().Parts()
	if err != nil {
		return false
	}

	for _, p := range parts {
		if !p.IsSession() {
			return false
		}
	}

	return true
}

func keyOf(from offeranswer.Side, req *sip.Request) offeranswer.Key {
	cseq, _ := req.Header.CSeq()
	return offeranswer.Key{From: from, Seq: cseq.Seq, Method: req.Method}
}

// RFC 3261 section 12.2.2
func (s *Session) receive(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	if s.state == Terminated || s.d == nil {
		s.reply(tx, 481)
		return
	}

	if err := s.d.ReceiveRequest(req); err != nil {
		code := 400

		var serr *sip.StatusError
		if errors.As(err, &serr) {
			code = serr.StatusCode
		}

		s.reply(tx, code)

		return
	}

	s.notePeer(req.Header)
	s.emitReceived(ctx, req)

	switch req.Method {
	case "BYE":
		s.receiveBye(ctx, tx, req)
	case "PRACK":
		s.receivePrack(ctx, tx, req)
	case "UPDATE", "INVITE":
		s.receiveOffer(ctx, tx, req)
	default:
		s.deliver(ctx, newRequest(ctx, s, tx, req))
	}
}

// RFC 3261 section 15.1.2
func (s *Session) receiveBye(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	if s.h.Bye == nil || s.inBye != nil {
		s.end(ctx, End{Cause: EndBye, Remote: true, Request: req})
		s.reply(tx, 200)

		return
	}

	r := newRequest(ctx, s, tx, req)
	s.inBye = r

	r.deadline = s.st.after(32*s.a.layer.T1(), func() {
		if !r.final {
			_ = r.respond(Reply{Code: 200})
		}
	})

	s.emit(func(h Handler) { h.Bye(ctx, s, r) })
}

func (s *Session) deliver(ctx context.Context, r *Request) {
	method := r.req.Method

	if !s.a.allows(method) {
		_ = r.respond(Reply{Code: 405, Header: sip.Header{{Name: "Allow", Value: s.a.allowValue()}}})
		return
	}

	timeout := 32 * s.a.layer.T1()
	if method == "INVITE" {
		timeout = s.policy.answerTimeout()
	}

	s.open[r] = struct{}{}
	r.answerBy = s.st.after(timeout, r.unanswered)

	s.emit(func(h Handler) {
		if h.Request != nil {
			h.Request(ctx, s, r)
			return
		}

		if method != "OPTIONS" && method != "INVITE" && method != "UPDATE" && method != "PRACK" {
			s.logger().Warn("no Handler.Request for a method the agent allows", slog.String("method", method))
		}

		_ = r.RespondDefault()
	})
}

// RFC 3261 section 11.2
func (r *Request) RespondDefault() error {
	switch r.req.Method {
	case "OPTIONS":
		return r.Respond(Reply{Code: 200, Header: r.a.capabilities()})
	case "INVITE", "UPDATE", "PRACK":
		if r.Refresh() {
			return r.AnswerUnchanged(Change{})
		}

		return r.Respond(Reply{Code: 488})
	}

	return r.Respond(Reply{Code: 500})
}

// RFC 3261 section 14.2
// RFC 3311 section 5.2
// RFC 4028 section 9
func (s *Session) receiveOffer(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	offer := sessionSDP(req.Envelope)

	if code, retry := s.conflict(req.Method, offer != nil); code != 0 {
		var extra []sip.Field
		if retry {
			extra = append(extra, sip.Field{Name: "Retry-After", Value: strconv.Itoa(randomInt(11))})
		}

		s.reply(tx, code, extra...)

		return
	}

	if res := s.timer.check(s.a, req); res != nil {
		s.decorate(res)

		if err := tx.Respond(res); err != nil {
			s.logger().Debug("422 failed", slog.Any("error", err))
		}

		return
	}

	r := newRequest(ctx, s, tx, req)

	r.unchanged, r.refresh = s.refreshAnswer(r, offer)
	r.role = s.offerRequest(r.key, offer)

	if req.Method == "INVITE" {
		s.in = r
		s.a.track(r)
	} else {
		s.inUpdate = r
	}

	if r.refresh && s.policy.AnswerRefreshes && onlySDP(req.Envelope) {
		if err := r.answerUnchanged(Change{}); err != nil {
			s.logger().Debug("refresh failed", slog.Any("error", err))
		}

		return
	}

	s.deliver(ctx, r)
}

func (s *Session) refreshAnswer(r *Request, offer []byte) ([]byte, bool) {
	mine, haveMine := s.sdp.Described(local)

	switch {
	case offer == nil && r.req.Method == "UPDATE":
		return nil, true
	case offer == nil:
		return mine, haveMine
	}

	_, theirs, ok := s.negotiated()

	return mine, ok && haveMine && sdp.Unchanged(theirs, offer)
}

// RFC 3261 section 14.2
// RFC 3311 section 5.2
// RFC 3311 section 5.1
func (s *Session) conflict(method string, offer bool) (code int, retry bool) {
	switch {
	case method == "INVITE" && (s.reinvite != nil || s.state == Early && s.uac):
		return 491, false
	case method == "INVITE" && s.in != nil:
		return 500, true
	case method == "UPDATE" && s.inUpdate != nil:
		return 500, true
	}

	if offer || method == "INVITE" {
		if o, _, pending := s.sdp.Pending(); pending {
			if o.From == remote {
				return 500, true
			}

			return 491, false
		}

		if _, _, done := s.sdp.Session(); method == "UPDATE" && !done {
			return 491, false
		}
	}

	return 0, false
}

// RFC 3262 section 3
// RFC 3262 section 5
func (s *Session) receivePrack(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	rack, err := req.Header.RAck()

	var in *Request

	if err == nil {
		if s.in != nil && s.in.awaitsPrack(rack) {
			in = s.in
		}

		for _, r := range s.acking {
			if r.awaitsPrack(rack) {
				in = r
			}
		}
	}

	if in == nil {
		s.reply(tx, 481)
		return
	}

	r := newRequest(ctx, s, tx, req)
	offer := sessionSDP(req.Envelope)
	r.role = s.offerRequest(r.key, offer)

	if r.role == offeranswer.Answer {
		s.emitAnswer(ctx, offer)
	}

	acked := in.unacked.res

	if in.unacked.sdp && in != s.initial() {
		if _, key, pending := s.sdp.Pending(); !pending || key != in.key {
			in.applied = true
		}
	}

	in.pracked()

	s.emit(func(h Handler) {
		if h.Pracked != nil {
			h.Pracked(ctx, s, acked)
		}
	})

	if r.role == offeranswer.Offer {
		s.deliver(ctx, r)
		return
	}

	_ = r.respond(Reply{Code: 200})

	s.pump()
}

// RFC 3261 section 13.3.1.4
func (s *Session) receiveAck(ctx context.Context, ack *sip.Request) {
	cseq, err := ack.Header.CSeq()
	if err != nil {
		return
	}

	r := s.acking[cseq.Seq]
	if r == nil {
		return
	}

	delete(s.acking, cseq.Seq)
	s.emitReceived(ctx, ack)

	r.acked()

	key := offeranswer.Key{From: remote, Seq: cseq.Seq, Method: "ACK"}
	answer := sessionSDP(ack.Envelope)

	if s.offerRequest(key, answer) == offeranswer.Answer {
		s.emitAnswer(ctx, answer)
	}

	if s.state == Early && s.d != nil && r.initial {
		s.confirm(ctx, ack)
	}

	switch offer, okey, pending := s.sdp.Pending(); {
	case pending && offer.From == local && okey == r.key:
		s.sdp.Fail(okey)
		s.byeEnd(ctx, End{Cause: EndLost, Err: errors.New("sip/ua: no answer in the ACK")}, Change{}, nil)
	case s.byeAfterAck != nil:
		s.sendQueuedBye(ctx, EndBye)
	default:
		s.pump()
	}
}

// RFC 3261 section 12.2.1.1
// RFC 3261 section 12.2.2
func (s *Session) Send(ctx context.Context, method string, c Change, done func(res *sip.Response, err error)) error {
	switch method {
	case "INVITE", "UPDATE", "PRACK", "ACK", "CANCEL", "BYE":
		return fmt.Errorf("sip/ua: %s is sent by the session", method)
	}

	return s.st.do(func() error {
		if !s.usable() {
			return s.errorf("Send", ErrState)
		}

		cr := &clientRequest{s: s, method: method, change: c, done: done}

		if err := cr.send(ctx); err != nil {
			return s.errorf("Send", err)
		}

		s.sends[cr] = struct{}{}

		return nil
	})
}

// RFC 3261 section 15.1.1
// RFC 3261 section 15
func (s *Session) Bye(ctx context.Context, c Change, done func(res *sip.Response, err error)) error {
	return s.st.do(func() error {
		switch {
		case s.d == nil || s.state == Terminated || s.inBye != nil:
			return s.errorf("Bye", ErrState)
		case !s.uac && s.state == Early && s.awaitingAck() && s.byeAfterAck == nil:
			s.byeAfterAck = &pendingBye{change: c, done: done, cause: EndBye}
			return nil
		case !s.uac && s.state == Early && s.in != nil && s.in.held != nil && len(s.in.others()) == 0:
			s.end(ctx, End{Cause: EndBye})
			s.emitDone(done, nil, nil)

			return nil
		case !s.uac && s.state == Early && s.in != nil && s.in == s.initial() && len(s.in.others()) > 0:
			s.in.drop(ctx, c)
			s.emitDone(done, nil, nil)

			return nil
		case !s.uac && s.state == Early:
			return s.errorf("Bye", ErrState)
		}

		s.byeEnd(ctx, End{Cause: EndBye}, c, done)

		return nil
	})
}

// RFC 5057 section 5.1
// RFC 5057 section 5.2
func (s *Session) fail(ctx context.Context, method string, res *sip.Response, err error) {
	if s.state == Terminated {
		return
	}

	switch failureOf(method, res, err) {
	case destroysDialog:
		s.a.destroyDialog(ctx, s.id, res, err)
	case destroysUsage:
		s.lost(ctx, res, err)
	}
}

// RFC 3261 section 12.2.1.2
func (s *Session) lost(ctx context.Context, res *sip.Response, err error) {
	e := End{Cause: EndLost, Response: res, Err: err}

	if res != nil && res.StatusCode == 481 {
		s.end(ctx, e)
		return
	}

	s.byeEnd(ctx, e, Change{}, nil)
}

// RFC 3261 section 14.1
func glareDelay(owner bool) time.Duration {
	if owner {
		return time.Duration(210+randomInt(191)) * 10 * time.Millisecond
	}

	return time.Duration(randomInt(201)) * 10 * time.Millisecond
}

func randomInt(n int) int {
	var b [8]byte

	_, _ = rand.Read(b[:])

	return int(binary.BigEndian.Uint64(b[:]) % uint64(n))
}

type clientRequest struct {
	s      *Session
	method string
	change Change
	req    *sip.Request
	done   func(*sip.Response, error)
	tries  int
	final  bool

	bye bool
}

func (c *clientRequest) send(ctx context.Context) error {
	s := c.s

	if c.req == nil {
		req, err := s.d.NewRequest(c.method)
		if err != nil {
			return err
		}

		c.change.apply(req)
		c.req = req
	}

	return s.transmit(ctx, c.req, s.st.client(c.response, c.error), nil)
}

func (c *clientRequest) response(ctx context.Context, res *sip.Response) {
	s := c.s

	if res.IsProvisional() || c.final {
		return
	}

	s.emitReceived(ctx, res)

	if s.d != nil {
		_ = s.d.ReceiveResponse(res)
	}

	if next, ok := s.challenged(c.req, res, c.tries, c.bye); ok {
		c.tries++
		c.req = next

		if err := c.send(ctx); err != nil {
			c.finish(ctx, nil, err)
		}

		return
	}

	c.finish(ctx, res, nil)
}

func (c *clientRequest) error(ctx context.Context, err error) {
	if c.final {
		return
	}

	c.finish(ctx, nil, err)
}

// RFC 5057 section 5.1
// RFC 5057 section 5.2
// RFC 5057 section 5.3
func (c *clientRequest) finish(ctx context.Context, res *sip.Response, err error) {
	s := c.s

	if c.final {
		return
	}

	c.final = true
	s.emitDone(c.done, res, err)

	if c.bye {
		s.a.await(-1)
		return
	}

	delete(s.sends, c)

	if res == nil || !res.IsSuccess() {
		s.fail(ctx, c.method, res, err)
	}
}

// RFC 3261 section 22.2
func (s *Session) challenged(req *sip.Request, res *sip.Response, tries int, ending bool) (*sip.Request, bool) {
	if res.StatusCode != 401 && res.StatusCode != 407 || s.d == nil || s.state == Terminated && !ending {
		return nil, false
	}

	next, err := s.d.NewRequest(req.Method)
	if err != nil {
		return nil, false
	}

	if req.Method == "PRACK" {
		next.Header.Set("RAck", req.Header.Get("RAck"))
	}

	for _, f := range req.Header {
		switch strings.ToLower(sip.LongName(f.Name)) {
		case "via", "cseq", "call-id", "from", "to", "route", "max-forwards", "content-length", "rack",
			"authorization", "proxy-authorization":
		default:
			next.Header.Add(f.Name, f.Value)
		}
	}

	next.Body = req.Body
	next.Header.Set("Content-Length", strconv.Itoa(len(req.Body)))

	if !s.a.authenticate(next, res, tries) {
		return nil, false
	}

	return next, true
}
