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
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/offeranswer"
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
	// EndRejected: a final response other than 2xx to the initial INVITE ended the session.
	EndRejected
	// EndNotChosen: another early dialog of the forked INVITE was answered (RFC 3261 §13.2.2.4).
	EndNotChosen
	// EndTimeout: a transaction timed out: no ACK to a 2xx, no PRACK to a reliable 1xx, no response.
	EndTimeout
	// EndLost: the peer no longer knows the dialog: 481 or 408 to a request within it (RFC 3261 §12.2.1.2).
	EndLost
	// EndExpired: the session timer expired without a refresh (RFC 4028 §10).
	EndExpired
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
	}

	return "EndCause(?)"
}

// End is how a session ended.
type End struct {
	Cause EndCause

	// Remote is true when the peer ended it: its BYE, its CANCEL, its final response.
	Remote bool

	// Request is the BYE or the CANCEL of the peer, with its Reason.
	Request *sip.Request

	// Response is the final response that ended the session.
	Response *sip.Response

	Err error
}

// Session is an INVITE dialog usage of the agent: one dialog, as UAC or UAS of its initial INVITE.
//
// The lock of a session is never held while calling the transaction layer, whose calls may run queued handlers of
// the agent: what is to be sent is decided under the lock, and sent once it is released.
type Session struct {
	a   *Agent
	h   Handler
	uac bool
	id  dialog.ID
	ev  serial

	mu    sync.Mutex
	d     *dialog.Dialog
	state State
	sdp   offeranswer.Negotiation
	sends []func()

	// out is the initial INVITE of a UAC session.
	out *Outgoing

	// in is the INVITE of the peer awaiting its final response, initial or re-INVITE; ack the one whose 2xx awaits
	// its ACK.
	in  *Request
	ack *Request

	// invite is the re-INVITE of the agent in progress.
	invite *clientInvite

	// rseq is the RSeq of the last reliable 1xx received in order for each INVITE of the agent (RFC 3262 §4); acks
	// the ACK of each 2xx, sent again on its retransmissions.
	rseq map[uint32]uint32
	acks map[uint32]*sip.Request

	peerAllow []string
	timer     sessionTimer
}

func newSession(a *Agent, h Handler, uac bool, id dialog.ID) *Session {
	return &Session{a: a, h: h, uac: uac, id: id, rseq: map[uint32]uint32{}, acks: map[uint32]*sip.Request{}}
}

func (s *Session) ID() dialog.ID {
	return s.id
}

// UAC reports whether the agent sent the initial INVITE of the session.
func (s *Session) UAC() bool {
	return s.uac
}

func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.state
}

// Dialog is the state of the dialog of the session, once a response created it.
func (s *Session) Dialog() (dialog.Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.d == nil {
		return dialog.Snapshot{}, false
	}

	return s.d.Snapshot(), true
}

// Negotiated is the session the agent and its peer agreed on: the session descriptions of the last offer and its
// answer, local and remote. ok is false before the first exchange.
func (s *Session) Negotiated() (localSDP, remoteSDP []byte, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	offer, answer, ok := s.sdp.Session()
	if !ok {
		return nil, nil, false
	}

	if offer.From == local {
		return offer.Data, answer.Data, true
	}

	return answer.Data, offer.Data, true
}

// later queues a send, under the lock of the session, for once it is released.
func (s *Session) later(f func()) {
	s.sends = append(s.sends, f)
}

// emit queues an event, under the lock of the session, to run in order once it is released.
func (s *Session) emit(f func()) {
	s.ev.enqueue(f)
}

// unlock releases the lock of the session, then sends what was queued, then runs the events.
func (s *Session) unlock() {
	sends := s.sends
	s.sends = nil
	s.mu.Unlock()

	for _, f := range sends {
		f()
	}

	s.ev.drain()
}

// The events of the handler read it when they run: the handler of an initial INVITE is set once OnInvite returns,
// before its events run.

func (s *Session) emitEnded(e End) {
	s.emit(func() {
		if f := s.h.Ended; f != nil {
			f(s, e)
		}
	})
}

func (s *Session) emitAnswer(sdp []byte) {
	s.emit(func() {
		if f := s.h.Answer; f != nil {
			f(s, sdp)
		}
	})
}

func (s *Session) emitEstablished() {
	s.emit(func() {
		if f := s.h.Established; f != nil {
			f(s)
		}
	})
}

func (s *Session) emitProgress(res *sip.Response) {
	s.emit(func() {
		if f := s.h.Progress; f != nil {
			f(s, res)
		}
	})
}

// emitOffer asks the application for the answer to an offer in a response, or rejects it without a handler.
func (s *Session) emitOffer(o *Offer) {
	s.emit(func() {
		if f := s.h.Offer; f != nil {
			f(s, o)
			return
		}

		o.Reject()
	})
}

// endLocked terminates the session, once.
func (s *Session) endLocked(e End) {
	if s.state == Terminated {
		return
	}

	s.state = Terminated
	s.timer.stop()

	if s.d != nil {
		s.d.Terminate()
	}

	s.a.remove(s)
	s.emitEnded(e)
}

func (s *Session) notePeerLocked(h sip.Header) {
	if h.Has("Allow") {
		s.peerAllow = h.Elements("Allow")
	}
}

func (s *Session) peerAllowsLocked(method string) bool {
	return slices.Contains(s.peerAllow, method)
}

// sendLater sends a request of the session once its lock is released, logging a failure.
func (s *Session) sendLater(req *sip.Request, h transaction.ClientHandler) {
	s.later(func() {
		if _, err := s.a.send(req, h); err != nil {
			s.a.log.Debug("request failed", slog.String("method", req.Method), slog.Any("error", err))
		}
	})
}

// byeLocked ends the session with a BYE, when the dialog allows one.
func (s *Session) byeLocked(e End, reasons ...sip.Reason) {
	if s.d != nil && s.state != Terminated {
		if bye, err := s.d.NewRequest("BYE"); err == nil {
			for _, r := range reasons {
				bye.Header.Add("Reason", r.String())
			}

			s.sendLater(bye, nil)
		}
	}

	s.endLocked(e)
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
	s.mu.Lock()

	if s.state == Terminated || s.d == nil {
		s.unlock()
		s.a.reply(tx, 481)

		return
	}

	if err := s.d.ReceiveRequest(req); err != nil {
		s.unlock()

		code := 400

		var serr *sip.StatusError
		if errors.As(err, &serr) {
			code = serr.StatusCode
		}

		s.a.reply(tx, code)

		return
	}

	s.notePeerLocked(req.Header)

	switch req.Method {
	case "BYE":
		s.receiveBye(tx, req)
	case "PRACK":
		s.receivePrack(tx, req)
	case "UPDATE", "INVITE":
		s.receiveOffer(tx, req)
	default:
		s.deliverLocked(newRequest(s, tx, req))
		s.unlock()
	}
}

func (s *Session) receiveBye(tx *transaction.ServerTransaction, req *sip.Request) {
	in := s.in
	s.endLocked(End{Cause: EndBye, Remote: true, Request: req})
	s.unlock()

	// RFC 3261 §15.1.2: the INVITE of an early dialog the BYE ends gets a 487.
	if in != nil {
		in.terminate(487)
	}

	s.a.reply(tx, 200)
}

// deliverLocked gives a request to the application, or answers it for lack of a handler.
func (s *Session) deliverLocked(r *Request) {
	s.emit(func() {
		if f := s.h.Request; f != nil {
			f(s, r)
			return
		}

		code := 405
		if r.req.Method == "INVITE" || r.req.Method == "UPDATE" || r.req.Method == "PRACK" {
			code = 488
		}

		_ = r.Respond(Reply{Code: code, Header: allowHeader(s.a, code)})
	})
}

func allowHeader(a *Agent, code int) sip.Header {
	if code != 405 {
		return nil
	}

	return sip.Header{{Name: "Allow", Value: a.allowValue()}}
}

// receiveOffer handles a re-INVITE or an UPDATE: glare (RFC 3261 §14.2, RFC 3311 §5.2), the session timer
// (RFC 4028 §9), and the offer it carries.
func (s *Session) receiveOffer(tx *transaction.ServerTransaction, req *sip.Request) {
	sdp := sessionSDP(req.Envelope)
	code, retry := s.conflictLocked(req.Method, sdp != nil)

	if code != 0 {
		s.unlock()

		var extra []sip.Field
		if retry {
			extra = append(extra, sip.Field{Name: "Retry-After", Value: strconv.Itoa(randomInt(11))})
		}

		s.a.reply(tx, code, extra...)

		return
	}

	if res := s.timer.check(s.a, req); res != nil {
		s.unlock()

		if err := s.a.respond(tx, res); err != nil {
			s.a.log.Debug("422 failed", slog.Any("error", err))
		}

		return
	}

	r := newRequest(s, tx, req)
	r.role = s.sdp.Request(r.key, sdp)

	if req.Method == "INVITE" {
		s.in = r
		s.a.track(r)
	}

	if req.Method == "UPDATE" && r.role == offeranswer.None {
		// An UPDATE without an offer refreshes the target or the session: the agent answers it.
		s.unlock()

		_ = r.Respond(Reply{Code: 200})

		return
	}

	s.deliverLocked(r)
	s.unlock()
}

// conflictLocked is the response refusing a re-INVITE or an offer that conflicts with a transaction in progress:
// 491 against one of the agent, 500 with Retry-After against one of the peer (RFC 3261 §14.2, RFC 3311 §5.2).
func (s *Session) conflictLocked(method string, offer bool) (code int, retry bool) {
	if method == "INVITE" {
		switch {
		case s.invite != nil || s.state == Early && s.uac:
			return 491, false
		case s.in != nil:
			return 500, true
		}
	}

	if offer || method == "INVITE" {
		if o, _, pending := s.sdp.Pending(); pending {
			if o.From == remote {
				return 500, true
			}

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
		s.unlock()
		s.a.reply(tx, 481)

		return
	}

	r := newRequest(s, tx, req)
	sdp := sessionSDP(req.Envelope)
	r.role = s.sdp.Request(r.key, sdp)

	if r.role == offeranswer.Answer {
		s.emitAnswer(sdp)
	}

	in.prackedLocked()

	if r.role == offeranswer.Offer {
		s.deliverLocked(r)
		s.unlock()

		return
	}

	s.unlock()

	_ = r.Respond(Reply{Code: 200})
}

// ack handles the ACK of a 2xx to an INVITE of the peer (RFC 3261 §13.3.1.4).
func (s *Session) receiveAck(ack *sip.Request) {
	s.mu.Lock()

	r := s.ack
	cseq, err := ack.Header.CSeq()

	if r == nil || err != nil || cseq.Seq != r.key.Seq {
		s.unlock()
		return
	}

	s.ack = nil
	r.ackedLocked()

	if sdp := sessionSDP(ack.Envelope); sdp != nil {
		if s.sdp.Request(offeranswer.Key{From: remote, Seq: cseq.Seq, Method: "ACK"}, sdp) == offeranswer.Answer {
			s.emitAnswer(sdp)
		}
	}

	if s.state == Early && s.d != nil {
		s.state = Confirmed
		s.emitEstablished()
	}

	s.unlock()
}

// NewRequest builds a request within the dialog, for Send (RFC 3261 §12.2.1.1).
func (s *Session) NewRequest(method string) (*sip.Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.d == nil || s.state == Terminated {
		return nil, ErrState
	}

	return s.d.NewRequest(method)
}

// Send sends a request of NewRequest other than INVITE, UPDATE, PRACK, ACK, CANCEL and BYE, which the session
// sends itself; done receives its final response or its error.
func (s *Session) Send(req *sip.Request, done func(res *sip.Response, err error)) error {
	switch req.Method {
	case "INVITE", "UPDATE", "PRACK", "ACK", "CANCEL", "BYE":
		return fmt.Errorf("sip/ua: %s is sent by the session", req.Method)
	}

	_, err := s.a.send(req, &clientRequest{s: s, done: done})

	return err
}

// Update sends an UPDATE, with an offer when c has a session description (RFC 3311). One without refreshes the
// session timer and the target.
func (s *Session) Update(c Change, done func(res *sip.Response, err error)) error {
	return s.request("UPDATE", c, done)
}

// Reinvite sends a re-INVITE, with an offer when c has a session description, or asking the peer for one
// (RFC 3261 §14.1). A 491 is retried after the delay of RFC 3261 §14.1.
func (s *Session) Reinvite(c Change, done func(res *sip.Response, err error)) error {
	return s.request("INVITE", c, done)
}

func (s *Session) request(method string, c Change, done func(*sip.Response, error)) error {
	s.mu.Lock()

	if s.d == nil || s.state == Terminated || method == "INVITE" && s.state != Confirmed {
		s.unlock()
		return ErrState
	}

	sdp := sessionSDPOf(c.Bodies)
	if code, _ := s.conflictLocked(method, sdp != nil); code != 0 || method == "INVITE" && s.in != nil {
		s.unlock()
		return ErrPending
	}

	ci, err := s.newClientInviteLocked(method, c, done)
	s.unlock()

	if err != nil {
		return err
	}

	return ci.send()
}

// newClientInviteLocked builds a re-INVITE or an UPDATE, and records its offer.
func (s *Session) newClientInviteLocked(method string, c Change, done func(*sip.Response, error)) (*clientInvite, error) {
	req, err := s.d.NewRequest(method)
	if err != nil {
		return nil, err
	}

	if s.a.cfg.Contact.URI.Scheme != "" {
		req.Header.Add("Contact", s.a.cfg.Contact.String())
	}

	req.Header.Add("Supported", strings.Join(s.a.supported(), ", "))
	req.Header.Add("Allow", s.a.allowValue())

	for _, f := range c.Header {
		req.Header.Set(f.Name, f.Value)
	}

	s.timer.requestHeaders(s.a, req)
	req.SetBodies(c.Bodies...)

	ci := &clientInvite{s: s, req: req, key: keyOf(local, req), change: c, done: done}
	s.sdp.Request(ci.key, sessionSDPOf(c.Bodies))

	if method == "INVITE" {
		s.invite = ci
	}

	return ci, nil
}

// Bye ends the session with a BYE carrying reasons (RFC 3261 §15.1.1, RFC 3326). The session ends at once; the BYE
// transaction completes on its own. A UAS cannot end a session before the ACK of its 2xx: it rejects the INVITE.
func (s *Session) Bye(reasons ...sip.Reason) error {
	s.mu.Lock()

	if s.d == nil || s.state == Terminated || !s.uac && s.state == Early {
		s.unlock()
		return ErrState
	}

	s.byeLocked(End{Cause: EndBye}, reasons...)
	s.unlock()

	return nil
}

// lost ends a session its peer no longer knows, with a BYE unless the peer answered 481 (RFC 3261 §12.2.1.2).
func (s *Session) lost(res *sip.Response, err error) {
	s.mu.Lock()

	e := End{Cause: EndLost, Response: res, Err: err}
	if res != nil && res.StatusCode == 481 {
		s.endLocked(e)
	} else {
		s.byeLocked(e)
	}

	s.unlock()
}

func (a *Agent) supported() []string {
	return []string{sip.OptionTag100rel, sip.OptionTagTimer}
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

// clientRequest is a request of the session sent with Send.
type clientRequest struct {
	s     *Session
	done  func(*sip.Response, error)
	final bool
}

func (c *clientRequest) HandleResponse(res *sip.Response) {
	c.s.mu.Lock()
	ignore := res.IsProvisional() || c.final
	c.final = c.final || !res.IsProvisional()
	c.s.mu.Unlock()

	if ignore {
		return
	}

	c.s.mu.Lock()
	if c.s.d != nil {
		_ = c.s.d.ReceiveResponse(res)
	}
	c.s.unlock()

	if res.StatusCode == 481 || res.StatusCode == 408 {
		c.s.lost(res, nil)
	}

	if c.done != nil {
		c.s.ev.do(func() { c.done(res, nil) })
	}
}

func (c *clientRequest) HandleError(err error) {
	c.s.mu.Lock()
	final := c.final
	c.final = true
	c.s.mu.Unlock()

	if final {
		return
	}

	if errors.Is(err, transaction.ErrTimeout) {
		c.s.lost(nil, err)
	}

	if c.done != nil {
		c.s.ev.do(func() { c.done(nil, err) })
	}
}

// serial runs functions one at a time, in the order they are queued, on the goroutines that drain it.
type serial struct {
	mu      sync.Mutex
	q       []func()
	running bool
	held    bool
}

// hold keeps queued functions from running until release.
func (s *serial) hold() {
	s.mu.Lock()
	s.held = true
	s.mu.Unlock()
}

func (s *serial) release() {
	s.mu.Lock()
	s.held = false
	s.mu.Unlock()
}

func (s *serial) enqueue(f func()) {
	s.mu.Lock()
	s.q = append(s.q, f)
	s.mu.Unlock()
}

func (s *serial) drain() {
	s.mu.Lock()

	if s.running || s.held {
		s.mu.Unlock()
		return
	}

	s.running = true

	for len(s.q) > 0 && !s.held {
		f := s.q[0]
		s.q = s.q[1:]

		s.mu.Unlock()
		f()
		s.mu.Lock()
	}

	s.running = false
	s.mu.Unlock()
}

func (s *serial) do(f func()) {
	s.enqueue(f)
	s.drain()
}
