package ua

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/offeranswer"
	"github.com/ellanetworks/ims/sip/transaction"
)

// Request is a request of the peer the application responds to: the initial INVITE of a session, a request within
// it, or a request outside a dialog. Its methods may be called from any goroutine; its state belongs to its strand.
type Request struct {
	a   *Agent
	st  *Strand
	s   *Session
	sub *Subscription
	tx  *transaction.ServerTransaction

	// server is the subscription of the agent as notifier a SUBSCRIBE refreshes, or that a SUBSCRIBE or a REFER
	// accepted created.
	server *ServerSubscription
	req    *sip.Request
	key    offeranswer.Key

	// role is what the session description of the request is to the session.
	role offeranswer.Role

	// refresh: a re-INVITE or an UPDATE that only refreshes the session, answered with unchanged (Refresh); applied:
	// an offer/answer exchange within this re-INVITE changed the session (RFC 6141 §3.3).
	refresh   bool
	unchanged []byte
	applied   bool

	final bool

	// tag is the To tag of a fork (Fork); forks are the members of the initial INVITE it shares with its forks.
	tag   string
	forks *forkSet

	// ending is how the session ends when a final response other than 2xx of the agent ends its initial INVITE:
	// EndRejected unless set.
	ending *End

	// Reliable provisional responses (RFC 3262 §3): the RSeq of the last one, the one awaiting its PRACK, those
	// queued behind it, and the 2xx held until its PRACK.
	rseq    uint32
	unacked *reliable
	queued  []Reply
	held    *sip.Response

	// answerBy answers the request the application left unanswered (Policy.AnswerTimeout).
	answerBy *timer

	// The 2xx of an INVITE, retransmitted until its ACK (RFC 3261 §13.3.1.4); initial marks the initial INVITE, whose
	// ACK confirms the session.
	initial         bool
	ok              *sip.Response
	interval        time.Duration
	timer, deadline *timer
}

type reliable struct {
	res      *sip.Response
	rseq     uint32
	sdp      bool
	interval time.Duration
	elapsed  time.Duration
	timer    *timer
}

func newRequest(s *Session, tx *transaction.ServerTransaction, req *sip.Request) *Request {
	r := &Request{s: s, tx: tx, req: req, key: keyOf(remote, req)}
	if s != nil {
		r.a, r.st = s.a, s.st
	}

	return r
}

// Message is the request.
func (r *Request) Message() *sip.Request {
	return r.req
}

// Done is closed once the transaction of the request ends, its final response sent.
func (r *Request) Done() <-chan struct{} {
	return r.tx.Done()
}

// Strand is the strand of the request: the one of its session, or one of its own outside a dialog.
func (r *Request) Strand() *Strand {
	return r.st
}

// Session is the session of the request, nil outside a dialog.
func (r *Request) Session() *Session {
	return r.s
}

// Offer is the offer the request carries: the answer goes in its 2xx, or in a reliable 1xx to an initial INVITE.
func (r *Request) Offer() ([]byte, bool) {
	if get(r.st, func() offeranswer.Role { return r.role }) != offeranswer.Offer {
		return nil, false
	}

	return sessionSDP(r.req.Envelope), true
}

// Refresh reports whether the request, a re-INVITE or an UPDATE, only refreshes the session: an UPDATE without an
// offer, a re-INVITE without one, or offering the session unchanged, its version the same (RFC 3264 §8, RFC 4028
// §7.4). Its header fields and other bodies may still ask for more, as the mcptt-info of an emergency upgrade of
// TS 24.379 §6.3.3.1.6: the application decides.
func (r *Request) Refresh() bool {
	return r.refresh
}

// AnswerUnchanged accepts a refresh (Refresh) with a 2xx carrying c and the session unchanged: the last session
// description of the agent, as the answer to an unchanged offer or as the offer a re-INVITE without one asks for, or
// none for an UPDATE without an offer.
func (r *Request) AnswerUnchanged(c Change) error {
	return r.st.do(func() error { return r.answerUnchanged(c) })
}

func (r *Request) answerUnchanged(c Change) error {
	if !r.refresh {
		return &Error{Op: "AnswerUnchanged", State: r.s.state, Err: errorString("the request changes the session")}
	}

	bodies := slices.Clone(c.Bodies)
	if r.unchanged != nil {
		bodies = append(bodies, SDP(r.unchanged))
	}

	return r.respond(Reply{Code: 200, Header: c.Header, Bodies: bodies})
}

// Accept sends a 200 with bodies.
func (r *Request) Accept(bodies ...sip.BodyPart) error {
	return r.Respond(Reply{Code: 200, Bodies: bodies})
}

// Reject sends a final response other than 2xx.
func (r *Request) Reject(code int, header ...sip.Field) error {
	return r.Respond(Reply{Code: code, Header: header})
}

// build makes the response to the request; complete completes it with Config.Respond, once the dialog recorded it.
func (r *Request) build(rp Reply) (*sip.Response, error) {
	res := sip.NewResponse(r.req, rp.Code, rp.Reason)

	for _, f := range rp.Header {
		res.Header.Add(f.Name, f.Value)
	}

	if rp.Code > 100 {
		if err := res.Header.SetToTag(r.toTag()); err != nil {
			return nil, err
		}
	}

	if r.s != nil {
		r.s.responseHeaders(r, rp, res)
	}

	res.SetBodies(rp.Bodies...)

	if r.s != nil {
		r.s.decorate(res)
	}

	return res, nil
}

func (r *Request) complete(res *sip.Response) {
	if r.a.cfg.Respond != nil {
		r.a.cfg.Respond(r.req, res)
	}
}

// responseHeaders adds to a response of the session the Contact of a response that creates the dialog or refreshes
// its target (RFC 3261 §12.1.1, §14.2; RFC 3311 §5.2), and the Allow and Supported of a 2xx to an INVITE.
func (s *Session) responseHeaders(r *Request, rp Reply, res *sip.Response) {
	refresh := r.req.Method == "INVITE" && rp.Code > 100 && rp.Code < 300 || r.req.Method == "UPDATE" && rp.Code < 300

	if refresh {
		switch {
		case res.Header.Has("Contact") && s.contact == "":
			s.contact = res.Header.Get("Contact")
		case !res.Header.Has("Contact") && s.contact != "":
			res.Header.Add("Contact", s.contact)
		case !res.Header.Has("Contact") && s.a.cfg.Contact.URI.Scheme != "":
			s.contact = s.a.cfg.Contact.String()
			res.Header.Add("Contact", s.contact)
		}
	}

	if r.req.Method == "INVITE" && rp.Code < 300 {
		if !res.Header.Has("Allow") {
			res.Header.Add("Allow", s.a.allowValue())
		}

		mergeSupported(&res.Header, s.a.supported)
	}
}

// Progress sends a provisional response to an INVITE, reliable when rp.Reliable or when the INVITE requires 100rel.
// A reliable one waits for the PRACK of the previous one (RFC 3262 §3).
func (r *Request) Progress(rp Reply) error {
	if r.s == nil || r.req.Method != "INVITE" || rp.Code <= 100 || rp.Code >= 200 {
		return fmt.Errorf("sip/ua: %d is no provisional response to an INVITE", rp.Code)
	}

	required := r.req.Header.HasOptionTag("Require", sip.OptionTag100rel)
	rp.Reliable = rp.Reliable || required

	if rp.Reliable && !required && !r.req.Header.HasOptionTag("Supported", sip.OptionTag100rel) {
		return errors.New("sip/ua: the INVITE does not support 100rel")
	}

	return r.st.do(func() error {
		if r.final || r.s.state == Terminated {
			return &Error{Op: "Progress", State: r.s.state, Err: ErrState}
		}

		if rp.Reliable && r.unacked != nil {
			r.queued = append(r.queued, rp)
			return nil
		}

		res, err := r.provisional(rp)
		if err != nil {
			return err
		}

		return r.tx.Respond(res)
	})
}

// provisional builds a provisional response, and records it: the dialog it creates or completes, the reliable
// response awaiting its PRACK, the session description it carries.
func (r *Request) provisional(rp Reply) (*sip.Response, error) {
	s := r.s

	sdp := sessionSDPOf(rp.Bodies)

	if rp.Reliable {
		// RFC 3262 §5: the first reliable response to an INVITE without an offer carries one.
		if r.role != offeranswer.Offer && r.rseq == 0 && sdp == nil {
			return nil, errors.New("sip/ua: the first reliable 1xx to an INVITE without an offer carries one")
		}

		if r.rseq == 0 {
			r.rseq = uint32(1 + randomInt(math.MaxInt32-1))
		} else {
			r.rseq++
		}

		rp.Header = rp.Header.Clone()
		addOptionTag(&rp.Header, "Require", sip.OptionTag100rel)
		rp.Header.Add("RSeq", strconv.FormatUint(uint64(r.rseq), 10))
	}

	res, err := r.build(rp)
	if err != nil {
		return nil, err
	}

	if err := s.dialogFor(r, res); err != nil {
		return nil, err
	}

	if rp.Reliable {
		if sdp != nil {
			s.sdp.Response(r.key, rp.Code, sdp)
		}

		rel := &reliable{res: res, rseq: r.rseq, sdp: sdp != nil, interval: s.a.layer.T1()}
		r.unacked = rel
		rel.timer = s.st.after(rel.interval, func() { r.retransmitReliable(rel) })
	}

	return res, nil
}

// dialogFor creates the dialog of an initial INVITE at its first response with a To tag, and records in it each
// response of the session: the Record-Route of the INVITE copied into those that create or confirm it (RFC 3261
// §12.1.1), the remote target of a target refresh request replaced by its reliable 1xx or 2xx (RFC 6141 §4.6). Then
// Config.Respond completes the response.
func (s *Session) dialogFor(r *Request, res *sip.Response) error {
	if err := s.record(r, res); err != nil {
		return err
	}

	r.complete(res)

	return nil
}

func (s *Session) record(r *Request, res *sip.Response) error {
	if s.d != nil {
		s.d.PrepareResponse(r.req, res)
		return nil
	}

	if r != s.in || s.uac || res.StatusCode >= 300 || s.state != Early {
		return nil
	}

	d, err := dialog.NewUAS(r.req, res)
	if err != nil {
		return err
	}

	s.d = d

	return nil
}

// retransmitReliable retransmits a reliable 1xx at T1 doubling, and rejects the INVITE with a 500 after 64*T1
// without its PRACK (RFC 3262 §3).
func (r *Request) retransmitReliable(rel *reliable) {
	s := r.s

	if r.unacked != rel || s.state == Terminated {
		return
	}

	rel.elapsed += rel.interval

	if rel.elapsed < 64*s.a.layer.T1() {
		// Retransmissions stop once a final response is sent, not while it is held.
		if !r.final || r.held != nil {
			_ = r.tx.Respond(rel.res)
		}

		rel.interval *= 2
		rel.timer = s.st.after(rel.interval, func() { r.retransmitReliable(rel) })

		return
	}

	r.unacked = nil
	r.queued = nil
	r.ending = &End{Cause: EndTimeout, Err: ErrNoPrack}

	if r.applied {
		r.acceptApplied()
		return
	}

	r.abandon(500)
}

// unanswered answers a request the application left without a final response past its deadline: a refresh as
// unchanged, a re-INVITE whose exchange changed the session with a 2xx (RFC 6141 §3.3), an initial INVITE with a 480,
// an OPTIONS with the capabilities, others with a 500 and Retry-After (RFC 3261 §21.5.1).
func (r *Request) unanswered() {
	s := r.s

	// The deadline of a forked INVITE is that of its first member, for them all.
	if r.forked() && !s.uac {
		if live := r.group(); len(live) > 0 {
			s.logger().Warn("INVITE left unanswered by the application")
			live[0].abandon(480)
		}

		return
	}

	if r.final || s.state == Terminated {
		return
	}

	s.logger().Warn("request left unanswered by the application", slog.String("method", r.req.Method))

	switch {
	case r == s.initial():
		r.abandon(480)
	case r.refresh:
		if err := r.answerUnchanged(Change{}); err != nil {
			r.abandon(500)
		}
	case r.req.Method == "INVITE" && r.applied:
		r.acceptApplied()
	case r.req.Method == "OPTIONS":
		_ = r.respond(Reply{Code: 200, Header: s.a.capabilities()})
	default:
		_ = r.respond(Reply{Code: 500, Header: sip.Header{{Name: "Retry-After", Value: strconv.Itoa(randomInt(11))}}})
	}
}

// acceptApplied ends with a 2xx a re-INVITE of the peer that can no longer complete as the application wished, as
// one cancelled, once an exchange within it changed the session (RFC 6141 §3.3, §3.8): with the offer of the session
// as it is, when the re-INVITE asks for one.
func (r *Request) acceptApplied() {
	s := r.s

	rp := Reply{Code: 200}

	if err := r.check(rp); err != nil {
		if mine, ok := s.sdp.Described(local); ok {
			rp.Bodies = []sip.BodyPart{SDP(mine)}
		}
	}

	r.final, r.held, r.queued = false, nil, nil

	if err := r.respond(rp); err != nil {
		s.logger().Debug("2xx of a re-INVITE failed", slog.Any("error", err))
		r.abandon(500)
	}
}

func (r *Request) awaitsPrack(rack sip.RAck) bool {
	return r.unacked != nil && rack.RSeq == r.unacked.rseq && rack.CSeq == r.key.Seq && rack.Method == r.key.Method
}

// pracked acknowledges the reliable response awaiting its PRACK, and sends what waited for it.
func (r *Request) pracked() {
	r.unacked.timer.stop()
	r.unacked = nil

	if len(r.queued) > 0 && !r.final {
		next := r.queued[0]
		r.queued = r.queued[1:]

		res, err := r.provisional(next)
		if err != nil {
			r.logger().Debug("queued reliable 1xx failed", slog.Any("error", err))
			return
		}

		_ = r.tx.Respond(res)

		return
	}

	if r.held != nil {
		res := r.held
		r.held = nil
		r.sendFinal(res)
	}
}

// Respond sends the final response. A 2xx to an INVITE waits for the PRACK of a reliable 1xx with a session
// description (RFC 3262 §3), or of any reliable 1xx with Reply.AfterPrack: Respond returns at once; should the PRACK
// not come, or the session end, a 500 or a 487 replaces it. The 2xx is retransmitted until its ACK (RFC 3261
// §13.3.1.4). The response to a BYE of Handler.Bye ends the session.
func (r *Request) Respond(rp Reply) error {
	if rp.Code < 200 {
		return fmt.Errorf("sip/ua: %d is no final response", rp.Code)
	}

	return r.st.do(func() error { return r.respond(rp) })
}

func (r *Request) respond(rp Reply) error {
	if r.final {
		return transaction.ErrFinalResponseSent
	}

	if r.sub != nil {
		return r.sub.answerNotify(r, rp)
	}

	if r.s == nil {
		res, err := r.build(rp)
		if err != nil {
			return err
		}

		r.complete(res)
		r.final = true
		r.a.untrack(r)

		return r.tx.Respond(res)
	}

	s := r.s

	if err := r.check(rp); err != nil {
		return err
	}

	if rp.Code < 300 && (r.req.Method == "INVITE" || r.req.Method == "UPDATE") {
		s.timer.responseHeaders(s.a, r.req, &rp)
	}

	res, err := r.build(rp)
	if err != nil {
		return err
	}

	r.final = true
	r.queued = nil

	if rp.Code < 300 && r.req.Method == "INVITE" && r.unacked != nil && (r.unacked.sdp || rp.AfterPrack) {
		r.held = res

		if r.forks != nil && r == s.initial() {
			r.forks.chosen = r
		}

		return nil
	}

	return r.finish(res)
}

// check refuses a 2xx that breaks the offer/answer model (RFC 3261 §13.2.1, RFC 3264, RFC 3311 §5.2).
func (r *Request) check(rp Reply) error {
	if rp.Code >= 300 {
		return nil
	}

	sdp := sessionSDPOf(rp.Bodies)
	offer, key, pending := r.s.sdp.Pending()

	switch {
	case r.role == offeranswer.Offer && pending && key == r.key && sdp == nil:
		return errors.New("sip/ua: the 2xx carries the answer to the offer of the request")
	case r.req.Method == "INVITE" && r.role == offeranswer.None && r.rseq == 0 && sdp == nil && (!pending || key != r.key):
		return errors.New("sip/ua: the 2xx to an INVITE without an offer carries one")
	case sdp != nil && r.role != offeranswer.Offer && (r.req.Method != "INVITE" || pending && offer.From == local && key != r.key):
		return errors.New("sip/ua: the 2xx can carry no session description")
	}

	return nil
}

// finish records a final response, and sends it.
func (r *Request) finish(res *sip.Response) error {
	s := r.s

	if r.unacked != nil {
		r.unacked.timer.stop()
	}

	initial := r == s.initial()

	if err := s.dialogFor(r, res); err != nil {
		return err
	}

	r.answerBy.stop()
	delete(s.open, r)

	s.offerResponse(r.key, res.StatusCode, sessionSDP(res.Envelope))

	switch {
	case s.in == r:
		s.in = nil
		s.a.untrack(r)
	case s.inUpdate == r:
		s.inUpdate = nil
	case s.inBye == r:
		// RFC 3261 §15.1.2: the session ends with the response to the BYE.
		r.deadline.stop()

		s.inBye = nil

		err := r.tx.Respond(res)
		s.end(End{Cause: EndBye, Remote: true, Request: r.req})

		return err
	}

	switch {
	case res.StatusCode >= 300 && initial:
		e := End{Cause: EndRejected, Response: res}
		if r.ending != nil {
			e = *r.ending
		}

		s.end(e)
	case res.IsSuccess() && (r.req.Method == "INVITE" || r.req.Method == "UPDATE"):
		s.timer.start(s)
	}

	if initial {
		r.settle(res)
	}

	if res.IsSuccess() && r.req.Method == "INVITE" && s.state != Terminated {
		r.ok = res
		r.initial = initial
		r.interval = s.a.layer.T1()
		s.acking[r.key.Seq] = r
		r.timer = s.st.after(r.interval, r.retransmitOK)
		r.deadline = s.st.after(64*s.a.layer.T1(), r.noAck)
	}

	err := r.tx.Respond(res)

	s.modified()
	s.pump()

	return err
}

// sendFinal records and sends a final response, logging a failure.
func (r *Request) sendFinal(res *sip.Response) {
	if err := r.finish(res); err != nil {
		r.logger().Debug("final response failed", slog.Any("error", err))
	}
}

// abandon ends a request of the peer the session can no longer answer as it wished: with code if no final response
// went yet, or in place of a 2xx held for a PRACK.
func (r *Request) abandon(code int) {
	if r.final && r.held == nil {
		return
	}

	r.final = true
	r.held = nil
	r.queued = nil

	if r.unacked != nil {
		r.unacked.timer.stop()
	}

	res, err := r.build(Reply{Code: code})
	if err != nil {
		return
	}

	r.sendFinal(res)
}

// initial is the initial INVITE of a UAS session, until its final response.
func (s *Session) initial() *Request {
	if s.uac || s.state != Early {
		return nil
	}

	return s.in
}

// retransmitOK retransmits the 2xx of an INVITE until its ACK, at T1 doubling to T2 (RFC 3261 §13.3.1.4).
func (r *Request) retransmitOK() {
	s := r.s

	if s.acking[r.key.Seq] != r || s.state == Terminated {
		return
	}

	r.interval = min(2*r.interval, s.a.layer.T2())
	r.timer = s.st.after(r.interval, r.retransmitOK)
	_ = r.tx.Respond(r.ok)
}

// noAck ends the session with a BYE, 64*T1 after a 2xx without its ACK (RFC 3261 §13.3.1.4).
func (r *Request) noAck() {
	s := r.s

	if s.acking[r.key.Seq] == r {
		delete(s.acking, r.key.Seq)

		r.acked()

		if s.byeAfterAck != nil {
			s.sendQueuedBye(EndTimeout)
			return
		}

		s.byeEnd(End{Cause: EndTimeout, Err: ErrNoAck}, Change{}, nil)
	}
}

func (r *Request) acked() {
	r.timer.stop()
	r.deadline.stop()
}

// cancelled ends an INVITE its CANCEL reached before the final response (RFC 3261 §9.2): the initial INVITE ends the
// session; a re-INVITE leaves it as it was, and Handler.Cancelled reports it.
func (r *Request) cancelled(cancel *sip.Request) {
	s := r.s

	// A forked INVITE ends in all its dialogs; the last one answers it with the 487.
	if r.forked() && !s.uac {
		for _, m := range r.group() {
			m.s.emitReceived(cancel)
			m.s.end(End{Cause: EndCancelled, Remote: true, Request: cancel})
		}

		return
	}

	if r.final && r.held == nil {
		return
	}

	s.emitReceived(cancel)

	if r == s.initial() {
		s.end(End{Cause: EndCancelled, Remote: true, Request: cancel})
		r.abandon(487)

		return
	}

	if r.applied {
		r.acceptApplied()
	} else {
		r.abandon(487)
	}

	s.emit(func(h Handler) {
		if h.Cancelled != nil {
			h.Cancelled(s, r, cancel)
		}
	})
}

// failed handles the error of the server transaction of an INVITE.
func (r *Request) failed(err error) {
	if r.forked() && !r.s.uac {
		for _, m := range r.group() {
			m.failedOne(err)
		}

		return
	}

	r.failedOne(err)
}

func (r *Request) failedOne(err error) {
	s := r.s

	r.final = true
	r.held = nil

	if r == s.initial() {
		s.end(failureEnd(err))
	}

	if s.in == r {
		s.in = nil
		s.sdp.Fail(r.key)
	}

	s.a.untrack(r)
	s.modified()
	s.pump()
}

// invite receives an initial INVITE (RFC 3261 §13.3). The session starts at once; OnInvite is its first event.
func (a *Agent) invite(st *Strand, tx *transaction.ServerTransaction, req *sip.Request) {
	if a.cfg.OnInvite == nil {
		a.reply(tx, 480)
		a.untrack(&Request{a: a, tx: tx, req: req})

		return
	}

	from, err := req.Header.From()
	if err != nil || from.Tag() == "" {
		a.reply(tx, 400)
		a.untrack(&Request{a: a, tx: tx, req: req})

		return
	}

	id := dialog.ID{CallID: req.Header.CallID(), LocalTag: tx.ToTag(), RemoteTag: from.Tag()}
	s := newSession(a, st, Handler{}, false, id, a.cfg.Policy)

	if res := s.timer.check(a, req); res != nil {
		_ = tx.Respond(res)
		a.untrack(&Request{a: a, tx: tx, req: req})

		return
	}

	r := newRequest(s, tx, req)
	r.role = s.offerRequest(r.key, sessionSDP(req.Envelope))
	r.forks = &forkSet{members: []*Request{r}}
	s.in = r
	s.notePeer(req.Header)

	// An INVITE that reached its strand as the agent closed: Shutdown no longer sees it.
	if !a.addIncoming(s, r) {
		a.reply(tx, 503)
		return
	}

	r.answerBy = st.after(s.policy.answerTimeout(), r.unanswered)

	// The handler is set with the strand held, for the methods of the application that read it.
	st.emit(func() {
		h := a.cfg.OnInvite(r)

		_ = st.do(func() error {
			if !s.handlerSet {
				s.h = h
			}

			return nil
		})
	})
}

// Replaces is the session an initial INVITE with a Replaces header field asks to replace, on the strand of the
// request (RFC 3891 §3); or, when it cannot, the code to reject the INVITE with: 400 for more than one Replaces, 481
// without such a dialog, for one not created by an INVITE, or for an early dialog the agent did not initiate, 603 for
// one that has ended, 486 for a confirmed one and early-only. ok is false without a Replaces, or without
// Extensions.Replaces. Authorising the
// replacement is up to the application, which ends the replaced session once it accepts the INVITE: with Bye when
// confirmed, by cancelling its Outgoing when early.
func (r *Request) Replaces() (s *Session, code int, ok bool) {
	if !r.a.cfg.Extensions.Replaces {
		return nil, 0, false
	}

	rep, ok, err := r.req.Header.Replaces()

	switch {
	case !ok:
		return nil, 0, false
	case err != nil || r.req.Method != "INVITE":
		return nil, 400, true
	}

	e, found := r.a.replaced(dialog.ReplacesID(rep))

	switch {
	case !found || !e.invited:
		return nil, 481, true
	case e.ended || e.invite == nil:
		return nil, 603, true
	}

	s = e.invite

	state, uac := get(s.st, func() State { return s.state }), s.uac

	switch {
	case state == Terminated:
		return nil, 603, true
	case state == Confirmed && rep.EarlyOnly:
		return nil, 486, true
	case state == Early && !uac:
		return nil, 481, true
	}

	return s, 0, true
}

// addOptionTag adds an option tag to the first header field name, or in one of its own without one.
func addOptionTag(h *sip.Header, name, tag string) {
	for i, f := range *h {
		if strings.EqualFold(sip.LongName(f.Name), name) {
			(*h)[i].Value = f.Value + ", " + tag
			return
		}
	}

	h.Add(name, tag)
}
