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

// Request is a request of the peer the application responds to: the initial INVITE of a session, or a request within
// it. Its methods may be called from any goroutine.
type Request struct {
	s   *Session
	tx  *transaction.ServerTransaction
	req *sip.Request
	key offeranswer.Key

	// role is what the session description of the request is to the session.
	role offeranswer.Role

	// The fields below are guarded by the lock of the session.
	final bool

	// Reliable provisional responses (RFC 3262 §3): the RSeq of the last one, the one awaiting its PRACK, those
	// queued behind it, and the 2xx held until its PRACK.
	rseq    uint32
	unacked *reliable
	queued  []Reply
	held    *sip.Response

	// The 2xx of an INVITE, retransmitted until its ACK (RFC 3261 §13.3.1.4).
	ok       *sip.Response
	interval time.Duration
	timer    transaction.Timer
}

type reliable struct {
	res      *sip.Response
	rseq     uint32
	sdp      bool
	interval time.Duration
	elapsed  time.Duration
	timer    transaction.Timer
}

func newRequest(s *Session, tx *transaction.ServerTransaction, req *sip.Request) *Request {
	return &Request{s: s, tx: tx, req: req, key: keyOf(remote, req)}
}

// Message is the request.
func (r *Request) Message() *sip.Request {
	return r.req
}

func (r *Request) Session() *Session {
	return r.s
}

// Offer is the offer the request carries: the answer goes in its 2xx, or in a reliable 1xx to an initial INVITE.
func (r *Request) Offer() ([]byte, bool) {
	if r.role != offeranswer.Offer {
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

func (r *Request) build(rp Reply) (*sip.Response, error) {
	a := r.s.a
	res := sip.NewResponse(r.req, rp.Code, rp.Reason)

	for _, f := range rp.Header {
		res.Header.Add(f.Name, f.Value)
	}

	if err := res.Header.SetToTag(r.tx.ToTag()); err != nil {
		return nil, err
	}

	// RFC 3261 §12.1.1, §14.2; RFC 3311 §5.2: responses that create the dialog or refresh its target carry a Contact.
	refresh := r.req.Method == "INVITE" && rp.Code > 100 && rp.Code < 300 || r.req.Method == "UPDATE" && rp.Code < 300
	if refresh && !res.Header.Has("Contact") && a.cfg.Contact.URI.Scheme != "" {
		res.Header.Add("Contact", a.cfg.Contact.String())
	}

	if r.req.Method == "INVITE" && rp.Code < 300 {
		if !res.Header.Has("Allow") {
			res.Header.Add("Allow", a.allowValue())
		}

		if !res.Header.Has("Supported") {
			res.Header.Add("Supported", strings.Join(a.supported(), ", "))
		}
	}

	res.SetBodies(rp.Bodies...)

	if a.cfg.Respond != nil {
		a.cfg.Respond(r.req, res)
	}

	return res, nil
}

// Progress sends a provisional response to an INVITE, reliable when rp.Reliable or when the INVITE requires 100rel.
// A reliable one waits for the PRACK of the previous one (RFC 3262 §3).
func (r *Request) Progress(rp Reply) error {
	if r.req.Method != "INVITE" || rp.Code <= 100 || rp.Code >= 200 {
		return fmt.Errorf("sip/ua: %d is no provisional response to an INVITE", rp.Code)
	}

	required := r.req.Header.HasOptionTag("Require", sip.OptionTag100rel)
	rp.Reliable = rp.Reliable || required

	if rp.Reliable && !required && !r.req.Header.HasOptionTag("Supported", sip.OptionTag100rel) {
		return errors.New("sip/ua: the INVITE does not support 100rel")
	}

	s := r.s
	s.mu.Lock()

	if r.final || s.state == Terminated {
		s.unlock()
		return ErrState
	}

	if rp.Reliable && r.unacked != nil {
		r.queued = append(r.queued, rp)
		s.unlock()

		return nil
	}

	res, err := r.provisionalLocked(rp)
	s.unlock()

	if err != nil {
		return err
	}

	return r.tx.Respond(res)
}

// provisionalLocked builds a provisional response, and records it: the dialog it creates, the reliable response
// awaiting its PRACK, the session description it carries.
func (r *Request) provisionalLocked(rp Reply) (*sip.Response, error) {
	s := r.s

	res, err := r.build(rp)
	if err != nil {
		return nil, err
	}

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

		res.Header.Add("Require", sip.OptionTag100rel)
		res.Header.Add("RSeq", strconv.FormatUint(uint64(r.rseq), 10))
	}

	if err := s.dialogLocked(r, res); err != nil {
		return nil, err
	}

	if rp.Reliable {
		if sdp != nil {
			s.sdp.Response(r.key, rp.Code, sdp)
		}

		rel := &reliable{res: res, rseq: r.rseq, sdp: sdp != nil, interval: s.a.layer.T1()}
		r.unacked = rel
		rel.timer = s.a.clock.AfterFunc(rel.interval, func() { r.retransmitReliable(rel) })
	}

	return res, nil
}

// dialogLocked creates the dialog of an initial INVITE at its first response with a To tag (RFC 3261 §12.1.1).
func (s *Session) dialogLocked(r *Request, res *sip.Response) error {
	if s.d != nil || r != s.in || s.uac || res.StatusCode >= 300 {
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
	s.mu.Lock()

	if r.unacked != rel || s.state == Terminated {
		s.unlock()
		return
	}

	rel.elapsed += rel.interval

	if rel.elapsed < 64*s.a.layer.T1() {
		// RFC 3262 §3: retransmissions stop once a final response is sent, not while it is held.
		if !r.final || r.held != nil {
			res := rel.res
			s.later(func() { _ = r.tx.Respond(res) })
		}

		rel.interval *= 2
		rel.timer = s.a.clock.AfterFunc(rel.interval, func() { r.retransmitReliable(rel) })
		s.unlock()

		return
	}

	r.unacked = nil
	r.queued = nil

	held := r.held != nil
	r.held = nil

	if !r.final {
		s.unlock()
		r.terminate(500)

		return
	}

	s.endLocked(End{Cause: EndTimeout, Err: errors.New("sip/ua: no PRACK to the reliable 1xx")})

	// The 2xx waiting for the PRACK is replaced by a 500.
	if held {
		if res, err := r.build(Reply{Code: 500}); err == nil {
			r.finalLaterLocked(res)
		}
	}

	s.unlock()
}

func (r *Request) awaitsPrack(rack sip.RAck) bool {
	return r.unacked != nil && rack.RSeq == r.unacked.rseq && rack.CSeq == r.key.Seq && rack.Method == r.key.Method
}

// prackedLocked acknowledges the reliable response awaiting its PRACK, and sends what waited for it.
func (r *Request) prackedLocked() {
	if r.unacked.timer != nil {
		r.unacked.timer.Stop()
	}

	r.unacked = nil

	if len(r.queued) > 0 && !r.final {
		next := r.queued[0]
		r.queued = r.queued[1:]

		res, err := r.provisionalLocked(next)
		if err != nil {
			r.s.a.log.Debug("queued reliable 1xx failed", slog.Any("error", err))
			return
		}

		r.s.later(func() { _ = r.tx.Respond(res) })

		return
	}

	if r.held != nil {
		res := r.held
		r.held = nil
		r.finalLaterLocked(res)
	}
}

// Respond sends the final response. A 2xx to an INVITE waits for the PRACK of a reliable 1xx with a session
// description (RFC 3262 §3): Respond returns at once, and a 500 replaces it if the PRACK never comes. The 2xx is
// retransmitted until its ACK (RFC 3261 §13.3.1.4).
func (r *Request) Respond(rp Reply) error {
	if rp.Code < 200 {
		return fmt.Errorf("sip/ua: %d is no final response", rp.Code)
	}

	s := r.s
	s.mu.Lock()

	if r.final {
		s.unlock()
		return transaction.ErrFinalResponseSent
	}

	res, err := r.build(rp)
	if err != nil {
		s.unlock()
		return err
	}

	if err := r.checkLocked(rp.Code, sessionSDPOf(rp.Bodies)); err != nil {
		s.unlock()
		return err
	}

	r.final = true
	r.queued = nil

	if rp.Code < 300 && (r.req.Method == "INVITE" || r.req.Method == "UPDATE") {
		s.timer.responseHeaders(s.a, r.req, res)
	}

	if rp.Code < 300 && r.req.Method == "INVITE" && r.unacked != nil && r.unacked.sdp {
		r.held = res
		s.unlock()

		return nil
	}

	send := r.finalLocked(res)
	s.unlock()

	return send()
}

// checkLocked refuses a 2xx that breaks the offer/answer model (RFC 3261 §13.2.1, RFC 3264).
func (r *Request) checkLocked(code int, sdp []byte) error {
	if code >= 300 {
		return nil
	}

	_, key, pending := r.s.sdp.Pending()

	switch {
	case r.role == offeranswer.Offer && pending && key == r.key && sdp == nil:
		return errors.New("sip/ua: the 2xx carries the answer to the offer of the request")
	case r.req.Method == "INVITE" && r.role == offeranswer.None && r.rseq == 0 && sdp == nil:
		return errors.New("sip/ua: the 2xx to an INVITE without an offer carries one")
	}

	return nil
}

// finalLocked records a final response, and returns the send of it.
func (r *Request) finalLocked(res *sip.Response) func() error {
	s := r.s

	if r.unacked != nil && r.unacked.timer != nil {
		r.unacked.timer.Stop()
	}

	initial := r == s.initial()

	if err := s.dialogLocked(r, res); err != nil {
		return func() error { return err }
	}

	s.sdp.Response(r.key, res.StatusCode, sessionSDP(res.Envelope))

	if s.in == r {
		s.in = nil
		s.a.untrack(r)
	}

	switch {
	case res.StatusCode >= 300 && initial:
		s.endLocked(End{Cause: EndRejected, Response: res})
	case res.IsSuccess() && (r.req.Method == "INVITE" || r.req.Method == "UPDATE"):
		s.timer.startLocked(s)
	}

	if res.IsSuccess() && r.req.Method == "INVITE" {
		r.ok = res
		r.interval = s.a.layer.T1()
		s.ack = r
		r.timer = s.a.clock.AfterFunc(r.interval, r.retransmitOK)
	}

	return func() error { return r.tx.Respond(res) }
}

// finalLaterLocked records a final response, and sends it once the lock is released.
func (r *Request) finalLaterLocked(res *sip.Response) {
	send := r.finalLocked(res)
	r.s.later(func() {
		if err := send(); err != nil {
			r.s.a.log.Debug("final response failed", slog.Any("error", err))
		}
	})
}

// initial is the initial INVITE of a UAS session, until its final response.
func (s *Session) initial() *Request {
	if s.uac || s.state != Early {
		return nil
	}

	return s.in
}

// retransmitOK retransmits the 2xx of an INVITE until its ACK, at T1 doubling to T2; the session ends with a BYE
// once the transaction ends, 64*T1 after the 2xx, without the ACK (RFC 3261 §13.3.1.4).
func (r *Request) retransmitOK() {
	s := r.s
	s.mu.Lock()

	if s.ack != r || s.state == Terminated {
		s.unlock()
		return
	}

	res := r.ok
	s.unlock()

	if err := r.tx.Respond(res); err != nil {
		s.mu.Lock()

		if s.ack == r {
			s.ack = nil
			s.byeLocked(End{Cause: EndTimeout, Err: errors.New("sip/ua: no ACK to the 2xx")})
		}

		s.unlock()

		return
	}

	s.mu.Lock()
	if s.ack == r {
		r.interval = min(2*r.interval, s.a.layer.T2())
		r.timer = s.a.clock.AfterFunc(r.interval, r.retransmitOK)
	}
	s.unlock()
}

func (r *Request) ackedLocked() {
	if r.timer != nil {
		r.timer.Stop()
	}
}

// terminate ends the INVITE with a final response of the agent: 487 on a CANCEL or a BYE, 500 without a PRACK.
func (r *Request) terminate(code int) {
	if err := r.Respond(Reply{Code: code}); err != nil && !errors.Is(err, transaction.ErrFinalResponseSent) {
		r.s.a.log.Debug("response failed", slog.Int("code", code), slog.Any("error", err))
	}
}

// cancelled ends an INVITE its CANCEL reached before the final response (RFC 3261 §9.2).
func (r *Request) cancelled(cancel *sip.Request) {
	s := r.s
	s.mu.Lock()

	if r.final {
		s.unlock()
		return
	}

	if r == s.initial() {
		s.endLocked(End{Cause: EndCancelled, Remote: true, Request: cancel})
	}

	s.unlock()
	r.terminate(487)
}

func (r *Request) failed(err error) {
	s := r.s
	s.mu.Lock()

	if r == s.initial() {
		s.endLocked(End{Cause: EndTimeout, Err: err})
	}

	s.unlock()
}

func (a *Agent) track(r *Request) {
	a.mu.Lock()
	a.invites[r.tx] = r
	a.mu.Unlock()
}

func (a *Agent) untrack(r *Request) {
	a.mu.Lock()
	delete(a.invites, r.tx)
	a.mu.Unlock()
}

// invite receives an initial INVITE (RFC 3261 §13.3).
func (a *Agent) invite(tx *transaction.ServerTransaction, req *sip.Request) {
	if a.cfg.OnInvite == nil {
		a.reply(tx, 480)
		return
	}

	from, err := req.Header.From()
	if err != nil || from.Tag() == "" {
		a.reply(tx, 400)
		return
	}

	// RFC 3261 §8.2.2.3
	for _, tag := range req.Header.Elements("Require") {
		if !containsFold(a.supported(), tag) {
			a.reply(tx, 420, sip.Field{Name: "Unsupported", Value: tag})
			return
		}
	}

	id := dialog.ID{CallID: req.Header.CallID(), LocalTag: tx.ToTag(), RemoteTag: from.Tag()}
	s := newSession(a, Handler{}, false, id)

	if res := s.timer.check(a, req); res != nil {
		_ = a.respond(tx, res)
		return
	}

	r := newRequest(s, tx, req)

	s.mu.Lock()
	r.role = s.sdp.Request(r.key, sessionSDP(req.Envelope))
	s.in = r
	s.notePeerLocked(req.Header)
	s.ev.hold()
	s.mu.Unlock()

	a.track(r)
	a.add(s)

	h := a.cfg.OnInvite(r)

	s.mu.Lock()
	s.h = h
	s.ev.release()
	s.unlock()
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}

	return false
}
