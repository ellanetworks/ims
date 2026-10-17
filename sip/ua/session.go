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

// The agent is side A of the offer/answer exchanges of its sessions, the peer side B.
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
	// EndBye: a BYE ended the session.
	EndBye EndCause = iota
	// EndCancelled: the initial INVITE was cancelled.
	EndCancelled
	// EndRejected: a final response other than 2xx to the initial INVITE ended the session, or one to a SUBSCRIBE
	// the subscription.
	EndRejected
	// EndNotChosen: another early dialog of the forked INVITE was answered (RFC 3261 §13.2.2.4).
	EndNotChosen
	// EndTimeout: a transaction timed out: no ACK to a 2xx, no PRACK to a reliable 1xx, no response.
	EndTimeout
	// EndLost: the peer no longer knows the dialog: 481 or 408 to a request within it (RFC 3261 §12.2.1.2), or broke
	// the offer/answer exchange.
	EndLost
	// EndExpired: the session timer expired without a refresh (RFC 4028 §10), or the subscription (RFC 6665 §4.1.2.2).
	EndExpired
	// EndClosed: the agent closed.
	EndClosed
	// EndTerminated: a NOTIFY terminated the subscription (RFC 6665 §4.1.3).
	EndTerminated
	// EndNoNotify: no NOTIFY came within Timer N of a SUBSCRIBE (RFC 6665 §4.1.2.4).
	EndNoNotify
	// EndUnsubscribed: the application ended the subscription before its first NOTIFY, or could not send the
	// SUBSCRIBE that ends it.
	EndUnsubscribed
)

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
	}

	return "EndCause(?)"
}

// End is how a session or a subscription ended.
type End struct {
	Cause EndCause

	// Remote is true when the peer ended it: its BYE, its CANCEL, its final response, its NOTIFY.
	Remote bool

	// Request is the BYE or the CANCEL of the peer, with its Reason, or the NOTIFY that terminated the subscription.
	Request *sip.Request

	// State is the Subscription-State of that NOTIFY.
	State sip.SubscriptionState

	// Response is the final response that ended the session.
	Response *sip.Response

	Err error
}

// Session is the INVITE usage of a dialog of the agent (RFC 5057), as UAC or UAS of its initial INVITE. Its state
// belongs to the loop of the agent.
type Session struct {
	a   *Agent
	uac bool
	id  dialog.ID

	h     Handler
	d     *dialog.Dialog
	state State
	sdp   offeranswer.Negotiation
	value any

	// contact is the Contact of the agent in the dialog, for the requests and responses that refresh its target.
	contact string

	// out is the initial INVITE of a UAC session.
	out *Outgoing

	// in is the INVITE of the peer awaiting its final response, initial or re-INVITE; inUpdate its UPDATE; ack the
	// INVITE whose 2xx awaits its ACK.
	in       *Request
	inUpdate *Request
	ack      *Request

	// invite is the re-INVITE of the agent in progress, updates its UPDATEs.
	invite  *clientInvite
	updates []*clientInvite

	// deferred are the re-INVITEs and UPDATEs of the agent waiting to be sent: for the ACK of its 2xx, or after a
	// 491 or a 422.
	deferred []*deferred

	// sending is the request of Send in progress, waiting those queued behind it: one at a time, so that they reach
	// the peer in the order of their CSeq (RFC 3261 §12.2.2).
	sending *clientRequest
	waiting []*clientRequest

	// rseq is the RSeq of the last reliable 1xx received in order for each INVITE of the agent (RFC 3262 §4); acks
	// the ACK of each 2xx, sent again on its retransmissions; answering the offers of 2xx awaiting their answer.
	rseq      map[uint32]uint32
	acks      map[uint32]*sip.Request
	answering map[uint32]*Offer

	// bye are the reasons of the BYE of a UAS waiting for the ACK of its 2xx (RFC 3261 §15), byeQueued whether there
	// is one.
	bye       []sip.Reason
	byeQueued bool

	// acking counts the ACKs being written, off the loop; the requests of the dialog sent meanwhile wait in queued,
	// not to overtake them.
	acking int
	queued []func()

	peerAllow []string
	timer     sessionTimer

	// unreported are the reliable 1xx whose PRACK has not completed yet, reported in order once it has.
	unreported []*sip.Response
}

func newSession(a *Agent, h Handler, uac bool, id dialog.ID) *Session {
	return &Session{
		a: a, h: h, uac: uac, id: id,
		rseq: map[uint32]uint32{}, acks: map[uint32]*sip.Request{}, answering: map[uint32]*Offer{},
	}
}

func (s *Session) ID() dialog.ID {
	return s.id
}

// UAC reports whether the agent sent the initial INVITE of the session.
func (s *Session) UAC() bool {
	return s.uac
}

func (s *Session) State() State {
	return get(s.a.loop, func() State { return s.state })
}

// Value is the value the application attached to the session.
func (s *Session) Value() any {
	return get(s.a.loop, func() any { return s.value })
}

func (s *Session) SetValue(v any) {
	_ = s.a.loop.call(func() error {
		s.value = v
		return nil
	})
}

// Dialog is the state of the dialog of the session, once a response created it.
func (s *Session) Dialog() (dialog.Snapshot, bool) {
	var (
		snap dialog.Snapshot
		ok   bool
	)

	_ = s.a.loop.call(func() error {
		if s.d != nil {
			snap, ok = s.d.Snapshot(), true
		}

		return nil
	})

	return snap, ok
}

// Negotiated is the session the agent and its peer agreed on: the session descriptions of the last offer and its
// answer, local and remote. ok is false before the first exchange.
func (s *Session) Negotiated() (localSDP, remoteSDP []byte, ok bool) {
	_ = s.a.loop.call(func() error {
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

// SessionTimer is the session timer of the session (RFC 4028): its interval, zero without one, and whether the agent
// refreshes.
func (s *Session) SessionTimer() (interval time.Duration, refresher bool) {
	_ = s.a.loop.call(func() error {
		interval, refresher = time.Duration(s.timer.interval)*time.Second, s.timer.refresher
		return nil
	})

	return interval, refresher
}

// emit makes a handler event due. The handler is read when the event runs: the one of an initial INVITE is set by
// OnInvite, its first event.
func (s *Session) emit(f func(h Handler)) {
	s.a.loop.emit(func() { f(s.h) })
}

func (s *Session) emitEnded(e End) {
	s.emit(func(h Handler) {
		if h.Ended != nil {
			h.Ended(s, e)
		}
	})
}

func (s *Session) emitAnswer(sdp []byte) {
	s.emit(func(h Handler) {
		if h.Answer != nil {
			h.Answer(s, sdp)
		}
	})
}

func (s *Session) emitEstablished(m sip.Message) {
	s.emit(func(h Handler) {
		if h.Established != nil {
			h.Established(s, m)
		}
	})
}

func (s *Session) emitProgress(res *sip.Response) {
	s.emit(func(h Handler) {
		if h.Progress != nil {
			h.Progress(s, res)
		}
	})
}

// emitOffer asks the application for the answer to an offer in a response, or refuses it without a handler.
func (s *Session) emitOffer(o *Offer) {
	s.emit(func(h Handler) {
		if h.Offer != nil {
			h.Offer(s, o)
			return
		}

		o.reject()
	})
}

func (s *Session) emitDone(done func(*sip.Response, error), res *sip.Response, err error) {
	if done != nil {
		s.a.loop.emit(func() { done(res, err) })
	}
}

// end terminates the session, once: its timers stop, and a request of the peer still unanswered gets a 487, a 2xx
// held for a PRACK included (RFC 3261 §15.1.2).
func (s *Session) end(e End) {
	s.a.loop.check()

	if s.state == Terminated {
		return
	}

	s.state = Terminated
	s.timer.stop()

	// The requests of the application in progress or waiting complete now: their transactions may never end, as a
	// re-INVITE the peer leaves unanswered once it sent its BYE. A late 2xx to a re-INVITE is still acknowledged.
	for _, c := range append([]*clientInvite{s.invite}, s.updates...) {
		if c != nil && !c.final {
			c.final = true
			s.emitDone(c.done, nil, ErrState)
		}
	}

	if c := s.sending; c != nil && !c.final {
		c.final = true
		s.emitDone(c.done, nil, ErrState)
	}

	for _, d := range s.deferred {
		d.t.stop()
		s.emitDone(d.done, nil, ErrState)
	}

	for _, c := range s.waiting {
		s.emitDone(c.done, nil, ErrState)
	}

	s.deferred, s.waiting = nil, nil

	for _, r := range []*Request{s.in, s.inUpdate} {
		if r != nil {
			r.abandon(487)
		}
	}

	if s.ack != nil {
		s.ack.acked()
		s.ack = nil
	}

	if s.d != nil {
		s.d.Terminate()
	}

	s.a.removeSession(s)
	s.emitEnded(e)
}

func (s *Session) notePeer(h sip.Header) {
	if h.Has("Allow") {
		s.peerAllow = h.Elements("Allow")
	}
}

func (s *Session) peerAllows(method string) bool {
	return slices.Contains(s.peerAllow, method)
}

// transmit sends a request of the dialog in a client transaction, once the ACKs being written are; sent receives the
// transaction. An error is returned when the request goes at once; h receives it otherwise.
func (s *Session) transmit(req *sip.Request, h transaction.ClientHandler, sent func(*transaction.ClientTransaction)) error {
	s.a.loop.check()

	send := func() error {
		tx, err := s.a.prepareAndSend(req, h)
		if err == nil && sent != nil {
			sent(tx)
		}

		return err
	}

	if s.acking == 0 {
		return send()
	}

	s.queued = append(s.queued, func() {
		if err := send(); err != nil && h != nil {
			h.HandleError(err)
		}
	})

	return nil
}

// writeAck writes an ACK of the dialog off the loop: the transport may block, connecting or writing.
func (s *Session) writeAck(ack *sip.Request) {
	s.acking++

	err := s.a.layer.Go(func(ctx context.Context) {
		if err := s.a.layer.SendAck(ctx, ack); err != nil {
			s.a.log.Debug("ACK failed", slog.Any("error", err))
		}

		s.a.loop.post(s.ackWritten)
	})
	if err != nil {
		s.a.log.Debug("ACK not sent", slog.Any("error", err))
		s.ackWritten()
	}
}

func (s *Session) ackWritten() {
	s.acking--

	if s.acking > 0 {
		return
	}

	queued := s.queued
	s.queued = nil

	for _, f := range queued {
		f()
	}
}

// sendRequest sends a request of the session, logging a failure.
func (s *Session) sendRequest(req *sip.Request, h transaction.ClientHandler) {
	if err := s.transmit(req, h, nil); err != nil {
		s.a.log.Debug("request failed", slog.String("method", req.Method), slog.Any("error", err))

		if h != nil {
			h.HandleError(err)
		}
	}
}

// byeEnd ends the session with a BYE, when the dialog allows one. A 2xx offer still unanswered is acknowledged
// first, refused (RFC 3261 §13.2.2.4).
func (s *Session) byeEnd(e End, reasons ...sip.Reason) {
	for seq, o := range s.answering {
		o.used = true
		o.refuse(nil)
		delete(s.answering, seq)
	}

	if s.d != nil && s.state != Terminated {
		if bye, err := s.d.NewRequest("BYE"); err == nil {
			for _, r := range reasons {
				bye.Header.Add("Reason", r.String())
			}

			s.a.closing.Add(1)

			finished := false

			done := func() {
				if !finished {
					finished = true

					s.a.closing.Done()
				}
			}

			s.sendRequest(bye, s.a.client(func(res *sip.Response) {
				if !res.IsProvisional() {
					done()
				}
			}, func(error) { done() }))
		}
	}

	s.end(e)
}

// close ends the session as the agent closes: with a BYE once confirmed; an early one rejects or abandons its INVITE.
func (s *Session) close() {
	e := End{Cause: EndClosed}

	if s.state == Confirmed || s.state == Early && s.ack != nil {
		s.byeEnd(e)
	} else {
		s.end(e)
	}
}

// sessionSDP is the session description of a message, if any (RFC 5621, TS 24.379 §6.5).
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

func keyOf(from offeranswer.Side, req *sip.Request) offeranswer.Key {
	cseq, _ := req.Header.CSeq()
	return offeranswer.Key{From: from, Seq: cseq.Seq, Method: req.Method}
}

// receive handles a request of the peer within the dialog (RFC 3261 §12.2.2).
func (s *Session) receive(tx *transaction.ServerTransaction, req *sip.Request) {
	if s.state == Terminated || s.d == nil {
		s.a.reply(tx, 481)
		return
	}

	if err := s.d.ReceiveRequest(req); err != nil {
		code := 400

		var serr *sip.StatusError
		if errors.As(err, &serr) {
			code = serr.StatusCode
		}

		s.a.reply(tx, code)

		return
	}

	s.notePeer(req.Header)

	switch req.Method {
	case "BYE":
		s.end(End{Cause: EndBye, Remote: true, Request: req})
		s.a.reply(tx, 200)
	case "PRACK":
		s.receivePrack(tx, req)
	case "UPDATE", "INVITE":
		s.receiveOffer(tx, req)
	default:
		s.deliver(newRequest(s, tx, req))
	}
}

// deliver gives a request to the application, or answers it for lack of a handler.
func (s *Session) deliver(r *Request) {
	s.emit(func(h Handler) {
		if h.Request != nil {
			h.Request(s, r)
			return
		}

		code := 405
		if r.req.Method == "INVITE" || r.req.Method == "UPDATE" || r.req.Method == "PRACK" {
			code = 488
		}

		var header sip.Header
		if code == 405 {
			header = sip.Header{{Name: "Allow", Value: s.a.allowValue()}}
		}

		_ = r.respond(Reply{Code: code, Header: header})
	})
}

// receiveOffer handles a re-INVITE or an UPDATE: its conflicts with transactions in progress (RFC 3261 §14.2,
// RFC 3311 §5.2), the session timer (RFC 4028 §9), and its offer. The agent answers the refreshes itself.
func (s *Session) receiveOffer(tx *transaction.ServerTransaction, req *sip.Request) {
	offer := sessionSDP(req.Envelope)

	if code, retry := s.conflict(req.Method, offer != nil, false); code != 0 {
		var extra []sip.Field
		if retry {
			extra = append(extra, sip.Field{Name: "Retry-After", Value: strconv.Itoa(randomInt(11))})
		}

		s.a.reply(tx, code, extra...)

		return
	}

	if res := s.timer.check(s.a, req); res != nil {
		if err := tx.Respond(res); err != nil {
			s.a.log.Debug("422 failed", slog.Any("error", err))
		}

		return
	}

	r := newRequest(s, tx, req)

	// RFC 3264 §8, RFC 4028 §7.4: a refresh repeats the session unchanged, or asks for an offer of it.
	answer, refresh := s.refreshAnswer(r, offer)

	r.role = s.sdp.Request(r.key, offer)

	if req.Method == "INVITE" {
		s.in = r
		s.a.track(r)
	} else {
		s.inUpdate = r
	}

	if refresh {
		var bodies []sip.BodyPart
		if answer != nil {
			bodies = []sip.BodyPart{SDP(answer)}
		}

		if err := r.respond(Reply{Code: 200, Bodies: bodies}); err != nil {
			s.a.log.Debug("refresh failed", slog.Any("error", err))
		}

		return
	}

	s.deliver(r)
}

// refreshAnswer is the session description the agent answers a refresh with: none to an UPDATE without an offer, its
// last one to an unchanged offer or to a re-INVITE without one.
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

// conflict is the response refusing a re-INVITE or an offer that conflicts with a transaction in progress: 491
// against one of the agent, 500 with Retry-After against one of the peer (RFC 3261 §14.2, RFC 3311 §5.2). An offer
// in an UPDATE comes after the initial exchange (RFC 3311 §5.1). mine checks a request of the agent itself.
func (s *Session) conflict(method string, offer, mine bool) (code int, retry bool) {
	switch {
	case mine && s.ack != nil:
		return 491, false
	case method == "INVITE" && (s.invite != nil || s.state == Early && (s.uac || mine)):
		return 491, false
	case method == "INVITE" && s.in != nil:
		return 500, true
	case method == "UPDATE" && !mine && s.inUpdate != nil:
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

// receivePrack handles a PRACK (RFC 3262 §3, §5).
func (s *Session) receivePrack(tx *transaction.ServerTransaction, req *sip.Request) {
	rack, err := req.Header.RAck()

	var in *Request

	if err == nil {
		for _, r := range []*Request{s.in, s.ack} {
			if r != nil && r.awaitsPrack(rack) {
				in = r
			}
		}
	}

	if in == nil {
		s.a.reply(tx, 481)
		return
	}

	r := newRequest(s, tx, req)
	offer := sessionSDP(req.Envelope)
	r.role = s.sdp.Request(r.key, offer)

	if r.role == offeranswer.Answer {
		s.emitAnswer(offer)
	}

	acked := in.unacked.res
	in.pracked()

	s.emit(func(h Handler) {
		if h.Pracked != nil {
			h.Pracked(s, acked)
		}
	})

	if r.role == offeranswer.Offer {
		s.deliver(r)
		return
	}

	_ = r.respond(Reply{Code: 200})
}

// receiveAck handles the ACK of a 2xx to an INVITE of the peer (RFC 3261 §13.3.1.4).
func (s *Session) receiveAck(ack *sip.Request) {
	r := s.ack
	cseq, err := ack.Header.CSeq()

	if r == nil || err != nil || cseq.Seq != r.key.Seq {
		return
	}

	s.ack = nil

	r.acked()

	key := offeranswer.Key{From: remote, Seq: cseq.Seq, Method: "ACK"}
	answer := sessionSDP(ack.Envelope)

	if s.sdp.Request(key, answer) == offeranswer.Answer {
		s.emitAnswer(answer)
	}

	if s.state == Early && s.d != nil {
		s.state = Confirmed
		s.emitEstablished(ack)
	}

	switch offer, okey, pending := s.sdp.Pending(); {
	case pending && offer.From == local && okey == r.key:
		// RFC 3264 §4: the offer of the 2xx went unanswered: the session cannot go on.
		s.sdp.Fail(okey)
		s.byeEnd(End{Cause: EndLost, Err: errors.New("sip/ua: no answer in the ACK")})
	case s.byeQueued:
		s.byeEnd(End{Cause: EndBye}, s.bye...)
	default:
		s.sendDeferred(nil)
	}
}

// Send sends a request within the dialog other than INVITE, UPDATE, PRACK, ACK, CANCEL and BYE, which the session
// sends itself, with c added (RFC 3261 §12.2.1.1); done receives its final response or its error, as a handler.
//
// The requests of Send go one at a time: each waits for the final response to the previous one, and takes its CSeq
// when sent, so that they reach the peer in order (RFC 3261 §12.2.2). Those still waiting when the session ends fail
// with ErrState.
func (s *Session) Send(method string, c Change, done func(res *sip.Response, err error)) error {
	switch method {
	case "INVITE", "UPDATE", "PRACK", "ACK", "CANCEL", "BYE":
		return fmt.Errorf("sip/ua: %s is sent by the session", method)
	}

	return s.a.loop.call(func() error {
		if s.d == nil || s.state == Terminated {
			return ErrState
		}

		cr := &clientRequest{s: s, method: method, change: c, done: done}

		if s.sending != nil {
			s.waiting = append(s.waiting, cr)
			return nil
		}

		s.sending = cr

		if err := cr.send(); err != nil {
			s.sending = nil
			return err
		}

		return nil
	})
}

// sendNext sends the next request of Send once the one in progress completed.
func (s *Session) sendNext(prev *clientRequest) {
	if s.sending != prev {
		return
	}

	s.sending = nil

	for len(s.waiting) > 0 && s.state != Terminated {
		cr := s.waiting[0]
		s.waiting = s.waiting[1:]

		if err := cr.send(); err != nil {
			cr.final = true
			s.emitDone(cr.done, nil, err)

			continue
		}

		s.sending = cr

		return
	}
}

// Update sends an UPDATE, with an offer when c has a session description (RFC 3311). One without refreshes the
// session timer and the target. While a 2xx of the agent awaits its ACK, the UPDATE waits for it.
func (s *Session) Update(c Change, done func(res *sip.Response, err error)) error {
	return s.a.loop.call(func() error { return s.request("UPDATE", c, done) })
}

// Reinvite sends a re-INVITE, with an offer when c has a session description, or asking the peer for one
// (RFC 3261 §14.1). A 491 is retried after the delay of RFC 3261 §14.1. While a 2xx of the agent awaits its ACK, the
// re-INVITE waits for it. CancelReinvite cancels it.
func (s *Session) Reinvite(c Change, done func(res *sip.Response, err error)) error {
	return s.a.loop.call(func() error { return s.request("INVITE", c, done) })
}

func (s *Session) request(method string, c Change, done func(*sip.Response, error)) error {
	if s.d == nil || s.state == Terminated {
		return ErrState
	}

	// The 2xx of the agent awaits its ACK: the request follows it.
	if s.ack != nil {
		s.deferred = append(s.deferred, &deferred{method: method, change: c, done: done})
		return nil
	}

	if method == "INVITE" && s.state != Confirmed {
		return ErrState
	}

	if code, _ := s.conflict(method, sessionSDPOf(c.Bodies) != nil, true); code != 0 {
		return ErrPending
	}

	ci, err := s.newClientInvite(method, c, done)
	if err != nil {
		return err
	}

	return ci.send()
}

// newClientInvite builds a re-INVITE or an UPDATE of the session, and records its offer.
func (s *Session) newClientInvite(method string, c Change, done func(*sip.Response, error)) (*clientInvite, error) {
	req, err := s.d.NewRequest(method)
	if err != nil {
		return nil, err
	}

	for _, f := range c.Header {
		req.Header.Add(f.Name, f.Value)
	}

	if !req.Header.Has("Contact") && s.contact != "" {
		req.Header.Add("Contact", s.contact)
	}

	if !req.Header.Has("Supported") {
		req.Header.Add("Supported", s.a.supportedValue())
	}

	if !req.Header.Has("Allow") {
		req.Header.Add("Allow", s.a.allowValue())
	}

	s.timer.requestHeaders(s.a, req)
	req.SetBodies(c.Bodies...)

	ci := &clientInvite{s: s, req: req, key: keyOf(local, req), change: c, done: done}
	s.sdp.Request(ci.key, sessionSDPOf(c.Bodies))
	s.started(ci)

	return ci, nil
}

// started records a re-INVITE or an UPDATE of the agent as in progress, finished once it completes.
func (s *Session) started(c *clientInvite) {
	if c.key.Method == "INVITE" {
		s.invite = c
	} else {
		s.updates = append(s.updates, c)
	}
}

func (s *Session) finished(c *clientInvite) {
	if s.forget(c) {
		s.modified()
	}
}

// forget drops a re-INVITE or an UPDATE of the agent from those in progress, as it completes or before it is sent
// again.
func (s *Session) forget(c *clientInvite) bool {
	if c == s.invite {
		s.invite = nil
		return true
	}

	if i := slices.Index(s.updates, c); i >= 0 {
		s.updates = slices.Delete(s.updates, i, i+1)
		return true
	}

	return false
}

// modifying reports an INVITE or an UPDATE in progress on the session, of either side: it may refresh the session.
func (s *Session) modifying() bool {
	return s.invite != nil || len(s.updates) > 0 || s.in != nil || s.inUpdate != nil
}

// modified ends the session whose timer expired while it was being modified, once no modification refreshed it.
func (s *Session) modified() {
	if s.timer.expired && !s.modifying() && s.state != Terminated {
		s.byeEnd(End{Cause: EndExpired})
	}
}

// CancelReinvite cancels the re-INVITE of Reinvite (RFC 3261 §9.1): one in progress with a CANCEL carrying reasons,
// its done receiving the 487, or the 2xx that came first; one waiting to be sent, done receiving ErrCancelled. It
// returns ErrState without one. The session refreshes of the agent are not cancelled.
func (s *Session) CancelReinvite(reasons ...sip.Reason) error {
	return s.a.loop.call(func() error {
		found := false

		if c := s.invite; c != nil && !c.refresh {
			found = true

			c.cancel(func(cancel *sip.Request) {
				for _, r := range reasons {
					cancel.Header.Add("Reason", r.String())
				}
			})
		}

		kept := s.deferred[:0]

		for _, d := range s.deferred {
			if d.method != "INVITE" || d.refresh {
				kept = append(kept, d)
				continue
			}

			found = true

			d.t.stop()
			s.emitDone(d.done, nil, ErrCancelled)
		}

		s.deferred = kept

		if !found {
			return ErrState
		}

		return nil
	})
}

// Bye ends the session with a BYE carrying reasons (RFC 3261 §15.1.1, RFC 3326). The session ends at once; the BYE
// transaction completes on its own. A UAS whose 2xx awaits its ACK sends the BYE once the ACK comes; before its 2xx,
// a UAS rejects the INVITE instead.
func (s *Session) Bye(reasons ...sip.Reason) error {
	return s.a.loop.call(func() error {
		switch {
		case s.d == nil || s.state == Terminated:
			return ErrState
		case !s.uac && s.state == Early && s.ack != nil:
			s.bye, s.byeQueued = slices.Clone(reasons), true
			return nil
		case !s.uac && s.state == Early:
			return ErrState
		}

		s.byeEnd(End{Cause: EndBye}, reasons...)

		return nil
	})
}

// lost ends a session its peer no longer knows, with a BYE unless the peer answered 481 (RFC 3261 §12.2.1.2).
func (s *Session) lost(res *sip.Response, err error) {
	e := End{Cause: EndLost, Response: res, Err: err}

	if res != nil && res.StatusCode == 481 {
		s.end(e)
		return
	}

	s.byeEnd(e)
}

// RFC 3261 §14.1: 2.1 to 4 s for the owner of the Call-ID, the UAC of the initial INVITE, 0 to 2 s for the other side,
// in units of 10 ms.
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

// clientRequest is a request of Send, sent again after a challenge.
type clientRequest struct {
	s      *Session
	method string
	change Change
	req    *sip.Request
	done   func(*sip.Response, error)
	tries  int
	final  bool
}

// send builds the request with the next CSeq of the dialog, and sends it.
func (c *clientRequest) send() error {
	s := c.s

	if c.req == nil {
		req, err := s.d.NewRequest(c.method)
		if err != nil {
			return err
		}

		for _, f := range c.change.Header {
			req.Header.Add(f.Name, f.Value)
		}

		req.SetBodies(c.change.Bodies...)
		c.req = req
	}

	return s.transmit(c.req, s.a.client(c.response, c.error), nil)
}

func (c *clientRequest) response(res *sip.Response) {
	s := c.s

	if res.IsProvisional() || c.final {
		return
	}

	if s.d != nil {
		_ = s.d.ReceiveResponse(res)
	}

	if next, ok := s.challenged(c.req, res, c.tries); ok {
		c.tries++
		c.req = next

		if err := c.send(); err != nil {
			c.finish(nil, err, false)
		}

		return
	}

	c.finish(res, nil, res.StatusCode == 481 || res.StatusCode == 408)
}

func (c *clientRequest) error(err error) {
	if c.final {
		return
	}

	c.finish(nil, err, errors.Is(err, transaction.ErrTimeout))
}

// finish completes the request, then loses the dialog when its outcome says so (RFC 3261 §12.2.1.2), then sends
// the next one.
func (c *clientRequest) finish(res *sip.Response, err error, lost bool) {
	s := c.s

	c.final = true
	s.emitDone(c.done, res, err)

	if lost && s.state != Terminated {
		s.lost(res, err)
	}

	s.sendNext(c)
}

// challenged is the request of the dialog to send again after a 401 or 407 to req: the next CSeq, with the credentials
// of Config.Authenticate (RFC 3261 §22.2).
func (s *Session) challenged(req *sip.Request, res *sip.Response, tries int) (*sip.Request, bool) {
	if res.StatusCode != 401 && res.StatusCode != 407 || s.d == nil || s.state == Terminated {
		return nil, false
	}

	next, err := s.d.NewRequest(req.Method)
	if err != nil {
		return nil, false
	}

	for _, f := range req.Header {
		switch strings.ToLower(sip.LongName(f.Name)) {
		case "via", "cseq", "call-id", "from", "to", "route", "max-forwards", "content-length",
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
