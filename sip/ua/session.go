package ua

import (
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
	// EndLost: the peer no longer knows the usage or the dialog: a failure of a request within it that destroys it
	// (RFC 5057 §5.1, §5.2; RFC 3261 §12.2.1.2), or the offer/answer exchange broke down.
	EndLost
	// EndExpired: the session timer expired without a refresh (RFC 4028 §10), or the subscription (RFC 6665 §4.1.2.2).
	EndExpired
	// EndClosed: the agent closed.
	EndClosed
	// EndTerminated: a NOTIFY terminated the subscription (RFC 6665 §4.1.3).
	EndTerminated
	// EndNoNotify: no NOTIFY came within Timer N of a SUBSCRIBE (RFC 6665 §4.1.2.4).
	EndNoNotify
	// EndUnsubscribed: the application ended the subscription before its first NOTIFY, or the SUBSCRIBE that ends it
	// failed.
	EndUnsubscribed
	// EndDropped: the application ended the subscription without the network: Drop, or a NOTIFY it refused; or the
	// session, the subscription or the publication with all of the agent's (Agent.Drop).
	EndDropped
	// EndNoSubscription: the 2xx to a REFER said it created no subscription (RFC 4488, RFC 7614).
	EndNoSubscription
	// EndRemoved: the application removed the publication (RFC 3903 §4.5).
	EndRemoved
	// EndFailed: a local failure, of the agent or the application, ended it, the peer still knowing it; End.Err
	// tells which: an ACK that could not be sent, a NOTIFY that could not be built.
	EndFailed
)

// failureEnd is the end of a usage whose transaction failed with err: dropped with the agent's (Agent.Drop), or timed
// out, or failed in transport.
func failureEnd(err error) End {
	if errors.Is(err, transaction.ErrDropped) {
		return End{Cause: EndDropped, Err: err}
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
	}

	return "EndCause(?)"
}

// End is how a session or a subscription ended. The causes of a session are EndBye, EndCancelled, EndRejected,
// EndNotChosen, EndTimeout, EndLost, EndExpired, EndClosed and EndFailed; a subscription has the others as well.
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

// Reasons are the reasons of the end, to pass on to another leg of a call (RFC 3326): the Reason header fields of
// the BYE or the CANCEL of the peer, or else the SIP status of the final response that ended the session.
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

// Session is the INVITE usage of a dialog of the agent (RFC 5057), as UAC or UAS of its initial INVITE. Its state
// belongs to its strand.
type Session struct {
	a   *Agent
	st  *Strand
	uac bool
	id  dialog.ID

	h Handler

	// handlerSet marks a handler set with SetHandler, which the one OnInvite returns does not replace.
	handlerSet bool
	policy     Policy
	d          *dialog.Dialog
	state      State
	sdp        offeranswer.Negotiation
	value      any

	// exchanges counts the offer/answer exchanges completed in the session, for the offers of fixed changes that
	// waited (ErrStale).
	exchanges uint64

	// contact is the Contact of the agent in the dialog, for the requests and responses that refresh its target.
	contact string

	// out is the initial INVITE of a UAC session.
	out *Outgoing

	// in is the INVITE of the peer awaiting its final response, initial or re-INVITE; inUpdate its UPDATE; acking the
	// INVITEs whose 2xx awaits its ACK, by CSeq (RFC 3261 §13.3.1.4: a re-INVITE may overtake the ACK of the 2xx
	// before); inBye the BYE of the peer Handler.Bye has.
	in       *Request
	inUpdate *Request
	acking   map[uint32]*Request
	inBye    *Request

	// The modifications of the agent (modify.go): its re-INVITE and its UPDATE in progress, and those waiting.
	reinvite *clientInvite
	update   *clientInvite
	queue    []*modification
	pumping  bool
	repump   bool

	// sends are the requests of Send in progress; open the requests of the peer the application has to answer, each
	// with its deadline, all answered when the session ends.
	sends map[*clientRequest]struct{}
	open  map[*Request]struct{}

	// rseq is the RSeq of the last reliable 1xx received in order for each INVITE of the agent (RFC 3262 §4); acks
	// the ACK of each 2xx, sent again on its retransmissions; answering the offers of 2xx awaiting their answer.
	rseq      map[uint32]uint32
	acks      map[uint32]*sip.Request
	answering map[uint32]*Offer

	// byeAfterAck is the BYE of a UAS waiting for the ACK of its 2xx (RFC 3261 §15); byeSent marks the BYE that
	// ended the session.
	byeAfterAck *pendingBye
	byeSent     bool

	peerAllow []string
	timer     sessionTimer

	// unreported are the reliable 1xx whose PRACK has not completed yet, reported in order once it has.
	unreported []*sip.Response

	// log is the logger of the dialog, made once.
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

// Strand is the strand of the session: its events run there, and the application may post its own jobs to it.
func (s *Session) Strand() *Strand {
	return s.st
}

func (s *Session) ID() dialog.ID {
	return s.id
}

// UAC reports whether the agent sent the initial INVITE of the session.
func (s *Session) UAC() bool {
	return s.uac
}

func (s *Session) State() State {
	return get(s.st, func() State { return s.state })
}

// Value is the value the application attached to the session.
func (s *Session) Value() any {
	return get(s.st, func() any { return s.value })
}

func (s *Session) SetValue(v any) {
	_ = s.st.do(func() error {
		s.value = v
		return nil
	})
}

// SetHandler replaces the handler of the session, for the events that come next. Set within OnInvite, it is the
// handler of the session, in place of the one OnInvite returns: for a UAS whose Handler.Decorate completes the
// responses it sends from OnInvite, or a B2BUA leg (b2bua.Call.Incoming).
func (s *Session) SetHandler(h Handler) {
	_ = s.st.do(func() error {
		s.h = h
		s.handlerSet = true

		return nil
	})
}

// Policy is the policy of the session.
func (s *Session) Policy() Policy {
	return get(s.st, func() Policy { return s.policy })
}

// SetPolicy sets the policy of the session, for what comes next.
func (s *Session) SetPolicy(p Policy) {
	_ = s.st.do(func() error {
		s.policy = p
		return nil
	})
}

// Dialog is the state of the dialog of the session, once a response created it.
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

// Negotiated is the session the agent and its peer agreed on: the session descriptions of the last offer and its
// answer, local and remote. ok is false before the first exchange.
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

// SessionTimer is the session timer of the session (RFC 4028): its interval, zero without one, and whether the agent
// refreshes.
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

// usable reports whether the application may send requests in the session: it has a dialog, and has not ended, nor
// is ending on a BYE of the peer.
func (s *Session) usable() bool {
	return s.d != nil && s.state != Terminated && s.inBye == nil
}

// The offer/answer exchanges of the session go through offerRequest and offerResponse, which count the completed
// ones and note those within a re-INVITE of the peer (RFC 6141 §3.3).
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

	// An exchange within a re-INVITE of the peer, other than the one of its final response: a PRACK, an UPDATE. The
	// answer of a reliable 1xx counts once PRACKed (receivePrack).
	if r := s.in; r != nil && r != s.initial() && key != r.key {
		r.applied = true
	}

	return role
}

// decorate completes a message of the session with Handler.Decorate.
func (s *Session) decorate(m sip.Message) {
	if s.h.Decorate != nil {
		s.h.Decorate(s, m)
	}
}

// reply sends a response of the agent to a request of the session.
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

// emit makes a handler event due. The handler is read when the event runs: the one of an initial INVITE is set by
// OnInvite, its first event.
func (s *Session) emit(f func(h Handler)) {
	s.st.emit(func() { f(s.h) })
}

// emitReceived reports a message of the peer the session took.
func (s *Session) emitReceived(m sip.Message) {
	s.emit(func(h Handler) {
		if h.Received != nil {
			h.Received(s, m)
		}
	})
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

		o.Reject()
	})
}

func (s *Session) emitDone(done func(*sip.Response, error), res *sip.Response, err error) {
	if done != nil {
		s.st.emit(func() { done(res, err) })
	}
}

// confirm confirms the session, with m its 2xx or its ACK; its session timer starts (RFC 4028 §7.2, §9: not on an
// early dialog).
func (s *Session) confirm(m sip.Message) {
	s.state = Confirmed
	s.timer.start(s)
	s.emitEstablished(m)
}

// end terminates the session, once: its timers stop, and a request of the peer still unanswered gets a 487, a 2xx
// held for a PRACK included (RFC 3261 §15.1.2); the initial INVITE gets a 503 when the agent closes.
func (s *Session) end(e End) {
	s.st.check()

	if s.state == Terminated {
		return
	}

	initial := s.initial()

	s.state = Terminated
	s.timer.stop()

	// The requests of the application in progress or waiting complete now: their transactions may never end, as a
	// re-INVITE the peer leaves unanswered once it sent its BYE. A late 2xx to a re-INVITE is still acknowledged.
	s.dropModifications(s.errorf("modification", ErrState))

	for c := range s.sends {
		c.final = true
		s.emitDone(c.done, nil, s.errorf("Send", ErrState))
	}

	s.sends = nil

	// RFC 3261 §15.1.2: the requests still open get a 487, the initial INVITE a 503 as the agent closes; a BYE of
	// the peer the application held gets its 2xx, and ends the session, whatever else ended it locally at the same
	// time.
	abandon := func(r *Request) {
		code := 487
		if e.Cause == EndClosed && r == initial {
			code = 503
		}

		// An early dialog of a forked INVITE ends alone while others are left to answer it.
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

	// The dialog ends with its last usage (RFC 5057 §4), its entry kept for the late responses.
	s.a.removeSession(s)

	s.emitEnded(e)
}

// awaitingAck reports a 2xx of the agent awaiting its ACK.
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

// transmit sends a request of the dialog in a client transaction; sent receives the transaction. The transaction
// layer sends the requests of a flow in the order it takes them, those of the dialog in the order of their CSeq.
func (s *Session) transmit(req *sip.Request, h transaction.ClientHandler, sent func(*transaction.ClientTransaction)) error {
	s.st.check()

	// The request stays as built, for one sent again after a challenge, which Decorate completes anew.
	out := req
	if s.h.Decorate != nil {
		out = req.Clone()
		s.decorate(out)
	}

	tx, err := s.a.prepareAndSend(out, h)
	if err == nil && sent != nil {
		sent(tx)
	}

	return err
}

// queueAck sends an ACK of the dialog in the order of its flow, before the requests sent after it.
func (s *Session) queueAck(ack *sip.Request) {
	if err := s.a.layer.QueueAck(ack, func(err error) {
		if err != nil {
			s.logger().Debug("ACK failed", slog.Any("error", err))
		}
	}); err != nil {
		s.logger().Debug("ACK not sent", slog.Any("error", err))
	}
}

// sendRequest sends a request of the session, logging a failure.
func (s *Session) sendRequest(req *sip.Request, h transaction.ClientHandler) {
	if err := s.transmit(req, h, nil); err != nil {
		s.logger().Debug("request failed", slog.String("method", req.Method), slog.Any("error", err))

		if h != nil {
			h.HandleError(err)
		}
	}
}

// byeEnd ends the session with a BYE carrying c, when the dialog allows one; done receives its final response. A 2xx
// offer still unanswered is acknowledged first, refused (RFC 3261 §13.2.2.4). The BYE transaction completes after
// the session ended; Shutdown waits for it.
func (s *Session) byeEnd(e End, c Change, done func(*sip.Response, error)) {
	for seq, o := range s.answering {
		o.used = true
		o.refuse(nil)
		delete(s.answering, seq)
	}

	if s.d != nil && s.state != Terminated && s.inBye == nil {
		bye := &clientRequest{s: s, method: "BYE", change: c, done: done, bye: true}
		s.byeSent = true

		s.a.await(1)

		if err := bye.send(); err != nil {
			bye.finish(nil, err)
		}
	} else {
		s.emitDone(done, nil, s.errorf("Bye", ErrState))
	}

	s.end(e)
}

// close ends the session as the agent closes: with a BYE once confirmed, or once the ACK of its 2xx came; an early
// one rejects or abandons its INVITE.
func (s *Session) close() {
	switch {
	case s.state == Terminated:
	case s.state == Early && s.awaitingAck() && s.byeAfterAck == nil:
		// RFC 3261 §15: not before the ACK, or its timeout.
		s.a.await(1)
		s.byeAfterAck = &pendingBye{cause: EndClosed}
	case s.state == Early && s.awaitingAck():
		// The BYE of the application waits for the ACK: Shutdown waits for it too.
		if s.byeAfterAck.cause != EndClosed {
			s.byeAfterAck.cause = EndClosed
			s.a.await(1)
		}
	case s.state == Confirmed:
		s.byeEnd(End{Cause: EndClosed}, Change{}, nil)
	default:
		s.end(End{Cause: EndClosed})
	}
}

// sendQueuedBye sends the BYE that waited for the ACK of the 2xx of a UAS.
func (s *Session) sendQueuedBye(cause EndCause) {
	pb := s.byeAfterAck
	if pb == nil {
		return
	}

	s.byeAfterAck = nil

	if cause == EndBye {
		cause = pb.cause
	}

	s.byeEnd(End{Cause: cause, Err: errorOf(cause)}, pb.change, pb.done)

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

// onlySDP reports whether a message carries no body but an SDP one.
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

// receive handles a request of the peer within the dialog (RFC 3261 §12.2.2).
func (s *Session) receive(tx *transaction.ServerTransaction, req *sip.Request) {
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
	s.emitReceived(req)

	switch req.Method {
	case "BYE":
		s.receiveBye(tx, req)
	case "PRACK":
		s.receivePrack(tx, req)
	case "UPDATE", "INVITE":
		s.receiveOffer(tx, req)
	default:
		s.deliver(newRequest(s, tx, req))
	}
}

// receiveBye ends the session on a BYE of the peer (RFC 3261 §15.1.2): at once with a 200, or once Handler.Bye
// responded, or 32*T1 after the BYE without a response, half the time the peer waits for one (Timer F).
func (s *Session) receiveBye(tx *transaction.ServerTransaction, req *sip.Request) {
	if s.h.Bye == nil || s.inBye != nil {
		s.end(End{Cause: EndBye, Remote: true, Request: req})
		s.reply(tx, 200)

		return
	}

	r := newRequest(s, tx, req)
	s.inBye = r

	r.deadline = s.st.after(32*s.a.layer.T1(), func() {
		if !r.final {
			_ = r.respond(Reply{Code: 200})
		}
	})

	s.emit(func(h Handler) { h.Bye(s, r) })
}

// deliver gives a request to the application, or answers it for lack of a handler.
func (s *Session) deliver(r *Request) {
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
			h.Request(s, r)
			return
		}

		if method != "OPTIONS" && method != "INVITE" && method != "UPDATE" && method != "PRACK" {
			s.logger().Warn("no Handler.Request for a method the agent allows", slog.String("method", method))
		}

		_ = r.RespondDefault()
	})
}

// RespondDefault answers a request within a session as the agent does without Handler.Request: a session refresh
// unchanged (RFC 4028), a re-INVITE, an UPDATE or a PRACK that changes the session with a 488, an OPTIONS with the
// capabilities of the agent (RFC 3261 §11.2), others with a 500.
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

// receiveOffer handles a re-INVITE or an UPDATE: its conflicts with transactions in progress (RFC 3261 §14.2,
// RFC 3311 §5.2), the session timer (RFC 4028 §9), and its offer. It goes to the application, unless Policy answers
// the session refreshes.
func (s *Session) receiveOffer(tx *transaction.ServerTransaction, req *sip.Request) {
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

	r := newRequest(s, tx, req)

	// RFC 3264 §8, RFC 4028 §7.4: a refresh repeats the session unchanged, or asks for an offer of it.
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

// conflict is the response refusing a re-INVITE or an offer of the peer that conflicts with a transaction in
// progress: 491 against one of the agent, 500 with Retry-After against one of the peer (RFC 3261 §14.2, RFC 3311
// §5.2). An offer in an UPDATE comes after the initial exchange (RFC 3311 §5.1).
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

// receivePrack handles a PRACK (RFC 3262 §3, §5).
func (s *Session) receivePrack(tx *transaction.ServerTransaction, req *sip.Request) {
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

	r := newRequest(s, tx, req)
	offer := sessionSDP(req.Envelope)
	r.role = s.offerRequest(r.key, offer)

	if r.role == offeranswer.Answer {
		s.emitAnswer(offer)
	}

	acked := in.unacked.res

	// RFC 6141 §3.3: the answer of the reliable 1xx to the offer of a re-INVITE changed the session once PRACKed.
	if in.unacked.sdp && in != s.initial() {
		if _, key, pending := s.sdp.Pending(); !pending || key != in.key {
			in.applied = true
		}
	}

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

	s.pump()
}

// receiveAck handles the ACK of a 2xx to an INVITE of the peer (RFC 3261 §13.3.1.4).
func (s *Session) receiveAck(ack *sip.Request) {
	cseq, err := ack.Header.CSeq()
	if err != nil {
		return
	}

	r := s.acking[cseq.Seq]
	if r == nil {
		return
	}

	delete(s.acking, cseq.Seq)
	s.emitReceived(ack)

	r.acked()

	key := offeranswer.Key{From: remote, Seq: cseq.Seq, Method: "ACK"}
	answer := sessionSDP(ack.Envelope)

	if s.offerRequest(key, answer) == offeranswer.Answer {
		s.emitAnswer(answer)
	}

	// The ACK of the 2xx to the initial INVITE confirms the session, whatever came after it.
	if s.state == Early && s.d != nil && r.initial {
		s.confirm(ack)
	}

	switch offer, okey, pending := s.sdp.Pending(); {
	case pending && offer.From == local && okey == r.key:
		// RFC 3264 §4: the offer of the 2xx went unanswered: the session cannot go on.
		s.sdp.Fail(okey)
		s.byeEnd(End{Cause: EndLost, Err: errors.New("sip/ua: no answer in the ACK")}, Change{}, nil)
	case s.byeAfterAck != nil:
		s.sendQueuedBye(EndBye)
	default:
		s.pump()
	}
}

// Send sends a request within the dialog other than INVITE, UPDATE, PRACK, ACK, CANCEL and BYE, which the session
// sends itself, with c added (RFC 3261 §12.2.1.1); done receives its final response or its error, as a handler. The
// requests of the dialog reach the peer in the order of their CSeq (RFC 3261 §12.2.2); those in progress when the
// session ends complete with ErrState.
func (s *Session) Send(method string, c Change, done func(res *sip.Response, err error)) error {
	switch method {
	case "INVITE", "UPDATE", "PRACK", "ACK", "CANCEL", "BYE":
		return fmt.Errorf("sip/ua: %s is sent by the session", method)
	}

	return s.st.do(func() error {
		if !s.usable() {
			return s.errorf("Send", ErrState)
		}

		cr := &clientRequest{s: s, method: method, change: c, done: done}

		if err := cr.send(); err != nil {
			return s.errorf("Send", err)
		}

		s.sends[cr] = struct{}{}

		return nil
	})
}

// Bye ends the session with a BYE carrying c (RFC 3261 §15.1.1): its Reason header fields (RFC 3326, Reasons), and
// others. The session ends at once; done receives the final response of the BYE, its transaction completing on its
// own. A UAS whose 2xx awaits its ACK sends the BYE once the ACK comes (RFC 3261 §15); before its 2xx, a UAS rejects
// the INVITE instead, unless its 2xx is held for a PRACK, which a 487 replaces, or the INVITE was forked
// (Request.Fork): Bye on one of its early dialogs ends that one alone, with
// a 199 carrying c when the INVITE supports it (RFC 6228), done getting no response. Bye on an early dialog of a UAC
// ends that dialog alone: Outgoing.Cancel cancels the INVITE, which ends them all.
func (s *Session) Bye(c Change, done func(res *sip.Response, err error)) error {
	return s.st.do(func() error {
		switch {
		case s.d == nil || s.state == Terminated || s.inBye != nil:
			return s.errorf("Bye", ErrState)
		case !s.uac && s.state == Early && s.awaitingAck() && s.byeAfterAck == nil:
			s.byeAfterAck = &pendingBye{change: c, done: done, cause: EndBye}
			return nil
		case !s.uac && s.state == Early && s.in != nil && s.in.held != nil && len(s.in.others()) == 0:
			// The 2xx held for a PRACK is not sent: the INVITE gets a 487 in its place, and the session ends
			// (RFC 3261 §15).
			s.end(End{Cause: EndBye})
			s.emitDone(done, nil, nil)

			return nil
		case !s.uac && s.state == Early && s.in != nil && s.in == s.initial() && len(s.in.others()) > 0:
			// One early dialog of a forked INVITE, its others left to answer it.
			s.in.drop(c)
			s.emitDone(done, nil, nil)

			return nil
		case !s.uac && s.state == Early:
			return s.errorf("Bye", ErrState)
		}

		s.byeEnd(End{Cause: EndBye}, c, done)

		return nil
	})
}

// fail applies to the session and its dialog the failure of a request within the dialog: res a final response other
// than 2xx, or err the error of its transaction (RFC 5057 §5.1, §5.2). A failure that destroys the dialog ends all
// its usages, without a BYE; one that destroys the usage ends the session.
func (s *Session) fail(method string, res *sip.Response, err error) {
	if s.state == Terminated {
		return
	}

	switch failureOf(method, res, err) {
	case destroysDialog:
		s.a.destroyDialog(s.id, res, err)
	case destroysUsage:
		s.lost(res, err)
	}
}

// lost ends a session its peer no longer knows, with a BYE unless the peer answered 481 (RFC 3261 §12.2.1.2).
func (s *Session) lost(res *sip.Response, err error) {
	e := End{Cause: EndLost, Response: res, Err: err}

	if res != nil && res.StatusCode == 481 {
		s.end(e)
		return
	}

	s.byeEnd(e, Change{}, nil)
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

// clientRequest is a request of the session other than INVITE, UPDATE, PRACK and ACK: of Send, or a BYE, sent again
// after a challenge.
type clientRequest struct {
	s      *Session
	method string
	change Change
	req    *sip.Request
	done   func(*sip.Response, error)
	tries  int
	final  bool

	// bye marks the BYE that ends the session: it completes after the session ended, Shutdown waiting for it.
	bye bool
}

// send builds the request with the next CSeq of the dialog, and sends it.
func (c *clientRequest) send() error {
	s := c.s

	if c.req == nil {
		req, err := s.d.NewRequest(c.method)
		if err != nil {
			return err
		}

		c.change.apply(req)
		c.req = req
	}

	return s.transmit(c.req, s.st.client(c.response, c.error), nil)
}

func (c *clientRequest) response(res *sip.Response) {
	s := c.s

	if res.IsProvisional() || c.final {
		return
	}

	s.emitReceived(res)

	if s.d != nil {
		_ = s.d.ReceiveResponse(res)
	}

	if next, ok := s.challenged(c.req, res, c.tries, c.bye); ok {
		c.tries++
		c.req = next

		if err := c.send(); err != nil {
			c.finish(nil, err)
		}

		return
	}

	c.finish(res, nil)
}

func (c *clientRequest) error(err error) {
	if c.final {
		return
	}

	c.finish(nil, err)
}

// finish completes the request, then applies its failure to the session and the dialog: an OPTIONS or a MESSAGE,
// of no usage, only ever affects the dialog (RFC 5057 §5.1–§5.3).
func (c *clientRequest) finish(res *sip.Response, err error) {
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
		s.fail(c.method, res, err)
	}
}

// challenged is the request of the dialog to send again after a 401 or 407 to req: the next CSeq, with the credentials
// of Config.Authenticate (RFC 3261 §22.2). The BYE that ended the session is sent again too.
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
