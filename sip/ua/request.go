package ua

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
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
	tx  *transaction.ServerTransaction
	req *sip.Request
	key offeranswer.Key

	// role is what the session description of the request is to the session.
	role offeranswer.Role

	final bool

	// ending is how the session ends when a final response other than 2xx of the agent ends its initial INVITE:
	// EndRejected unless set.
	ending *End

	// Reliable provisional responses (RFC 3262 §3): the RSeq of the last one, the one awaiting its PRACK, those
	// queued behind it, and the 2xx held until its PRACK.
	rseq    uint32
	unacked *reliable
	queued  []Reply
	held    *sip.Response

	// The 2xx of an INVITE, retransmitted until its ACK (RFC 3261 §13.3.1.4).
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

// Accept sends a 200 with bodies.
func (r *Request) Accept(bodies ...sip.BodyPart) error {
	return r.Respond(Reply{Code: 200, Bodies: bodies})
}

// Reject sends a final response other than 2xx.
func (r *Request) Reject(code int, header ...sip.Field) error {
	return r.Respond(Reply{Code: code, Header: header})
}

// build makes the response to the request, completed with Config.Respond.
func (r *Request) build(rp Reply) (*sip.Response, error) {
	res := sip.NewResponse(r.req, rp.Code, rp.Reason)

	for _, f := range rp.Header {
		res.Header.Add(f.Name, f.Value)
	}

	if rp.Code > 100 {
		if err := res.Header.SetToTag(r.tx.ToTag()); err != nil {
			return nil, err
		}
	}

	if r.s != nil {
		r.s.responseHeaders(r, rp, res)
	}

	res.SetBodies(rp.Bodies...)

	if r.a.cfg.Respond != nil {
		r.a.cfg.Respond(r.req, res)
	}

	return res, nil
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

		if !res.Header.Has("Supported") {
			res.Header.Add("Supported", s.a.supportedValue())
		}
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
			return ErrState
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
// §12.1.1), the remote target of a target refresh request replaced by its reliable 1xx or 2xx (RFC 6141 §4.6).
func (s *Session) dialogFor(r *Request, res *sip.Response) error {
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

	r.abandon(500)
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
			r.a.log.Debug("queued reliable 1xx failed", slog.Any("error", err))
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
// description (RFC 3262 §3): Respond returns at once; should the PRACK not come, or the session end, a 500 or a 487
// replaces it. The 2xx is retransmitted until its ACK (RFC 3261 §13.3.1.4).
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

	if r.s == nil {
		res, err := r.build(rp)
		if err != nil {
			return err
		}

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

	if rp.Code < 300 && r.req.Method == "INVITE" && r.unacked != nil && r.unacked.sdp {
		r.held = res
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

	s.sdp.Response(r.key, res.StatusCode, sessionSDP(res.Envelope))

	switch {
	case s.in == r:
		s.in = nil
		s.a.untrack(r)
	case s.inUpdate == r:
		s.inUpdate = nil
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

	if res.IsSuccess() && r.req.Method == "INVITE" && s.state != Terminated {
		r.ok = res
		r.interval = s.a.layer.T1()
		s.ack = r
		r.timer = s.st.after(r.interval, r.retransmitOK)
		r.deadline = s.st.after(64*s.a.layer.T1(), r.noAck)
	}

	err := r.tx.Respond(res)

	s.modified()

	return err
}

// sendFinal records and sends a final response, logging a failure.
func (r *Request) sendFinal(res *sip.Response) {
	if err := r.finish(res); err != nil {
		r.a.log.Debug("final response failed", slog.Any("error", err))
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

	if s.ack != r || s.state == Terminated {
		return
	}

	r.interval = min(2*r.interval, s.a.layer.T2())
	r.timer = s.st.after(r.interval, r.retransmitOK)
	_ = r.tx.Respond(r.ok)
}

// noAck ends the session with a BYE, 64*T1 after a 2xx without its ACK (RFC 3261 §13.3.1.4).
func (r *Request) noAck() {
	s := r.s

	if s.ack == r {
		s.ack = nil

		r.acked()
		s.byeEnd(End{Cause: EndTimeout, Err: ErrNoAck}, s.bye...)
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

	if r.final && r.held == nil {
		return
	}

	if r == s.initial() {
		s.end(End{Cause: EndCancelled, Remote: true, Request: cancel})
		r.abandon(487)

		return
	}

	r.abandon(487)

	s.emit(func(h Handler) {
		if h.Cancelled != nil {
			h.Cancelled(s, r, cancel)
		}
	})
}

// failed handles the error of the server transaction of an INVITE.
func (r *Request) failed(err error) {
	s := r.s

	r.final = true
	r.held = nil

	if r == s.initial() {
		s.end(End{Cause: EndTimeout, Err: err})
	}

	if s.in == r {
		s.in = nil
		s.sdp.Fail(r.key)
	}

	s.a.untrack(r)
	s.modified()
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
	s := newSession(a, st, Handler{}, false, id)

	if res := s.timer.check(a, req); res != nil {
		_ = tx.Respond(res)
		a.untrack(&Request{a: a, tx: tx, req: req})

		return
	}

	r := newRequest(s, tx, req)
	r.role = s.sdp.Request(r.key, sessionSDP(req.Envelope))
	s.in = r
	s.notePeer(req.Header)

	a.track(r)
	a.addSession(s)

	st.emit(func() { s.h = a.cfg.OnInvite(r) })
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
