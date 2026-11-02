package ua

import (
	"context"
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

type Request struct {
	ctx context.Context

	a   *Agent
	st  *Strand
	s   *Session
	sub *Subscription
	tx  *transaction.ServerTransaction

	server *ServerSubscription
	req    *sip.Request
	key    offeranswer.Key

	role offeranswer.Role

	refresh   bool
	unchanged []byte
	applied   bool

	final bool

	tag   string
	forks *forkSet

	ending *End

	rseq    uint32
	unacked *reliable
	queued  []Reply
	held    *sip.Response

	answerBy *timer

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

func newRequest(ctx context.Context, s *Session, tx *transaction.ServerTransaction, req *sip.Request) *Request {
	r := &Request{ctx: ctx, s: s, tx: tx, req: req, key: keyOf(remote, req)}
	if s != nil {
		r.a, r.st = s.a, s.st
	}

	return r
}

func (r *Request) Context() context.Context {
	return r.ctx
}

func (r *Request) Message() *sip.Request {
	return r.req
}

func (r *Request) Done() <-chan struct{} {
	return r.tx.Done()
}

func (r *Request) Strand() *Strand {
	return r.st
}

func (r *Request) Session() *Session {
	return r.s
}

func (r *Request) Offer() ([]byte, bool) {
	if get(r.st, func() offeranswer.Role { return r.role }) != offeranswer.Offer {
		return nil, false
	}

	return sessionSDP(r.req.Envelope), true
}

// RFC 3264 section 8
// RFC 4028 section 7.4
func (r *Request) Refresh() bool {
	return r.refresh
}

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

func (r *Request) Accept(bodies ...sip.BodyPart) error {
	return r.Respond(Reply{Code: 200, Bodies: bodies})
}

func (r *Request) Reject(code int, header ...sip.Field) error {
	return r.Respond(Reply{Code: code, Header: header})
}

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

// RFC 3261 section 12.1.1
// RFC 3261 section 14.2
// RFC 3311 section 5.2
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

// RFC 3262 section 3
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

func (r *Request) provisional(rp Reply) (*sip.Response, error) {
	s := r.s

	sdp := sessionSDPOf(rp.Bodies)

	if rp.Reliable {
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

// RFC 3261 section 12.1.1
// RFC 6141 section 4.6
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

// RFC 3262 section 3
func (r *Request) retransmitReliable(rel *reliable) {
	s := r.s

	if r.unacked != rel || s.state == Terminated {
		return
	}

	rel.elapsed += rel.interval

	if rel.elapsed < 64*s.a.layer.T1() {
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

// RFC 6141 section 3.3
// RFC 3261 section 21.5.1
func (r *Request) unanswered() {
	s := r.s

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

// RFC 6141 section 3.3
// RFC 6141 section 3.8
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

// RFC 3262 section 3
// RFC 3261 section 13.3.1.4
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

// RFC 3261 section 13.2.1
// RFC 3264
// RFC 3311 section 5.2
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
		r.deadline.stop()

		s.inBye = nil

		err := r.tx.Respond(res)
		s.end(r.ctx, End{Cause: EndBye, Remote: true, Request: r.req})

		return err
	}

	switch {
	case res.StatusCode >= 300 && initial:
		e := End{Cause: EndRejected, Response: res}
		if r.ending != nil {
			e = *r.ending
		}

		s.end(r.ctx, e)
	case res.IsSuccess() && (r.req.Method == "INVITE" || r.req.Method == "UPDATE"):
		s.timer.start(r.ctx, s)
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

func (r *Request) sendFinal(res *sip.Response) {
	if err := r.finish(res); err != nil {
		r.logger().Debug("final response failed", slog.Any("error", err))
	}
}

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

func (s *Session) initial() *Request {
	if s.uac || s.state != Early {
		return nil
	}

	return s.in
}

// RFC 3261 section 13.3.1.4
func (r *Request) retransmitOK() {
	s := r.s

	if s.acking[r.key.Seq] != r || s.state == Terminated {
		return
	}

	r.interval = min(2*r.interval, s.a.layer.T2())
	r.timer = s.st.after(r.interval, r.retransmitOK)
	_ = r.tx.Respond(r.ok)
}

// RFC 3261 section 13.3.1.4
func (r *Request) noAck() {
	s := r.s

	if s.acking[r.key.Seq] == r {
		delete(s.acking, r.key.Seq)

		r.acked()

		if s.byeAfterAck != nil {
			s.sendQueuedBye(r.ctx, EndTimeout)
			return
		}

		s.byeEnd(r.ctx, End{Cause: EndTimeout, Err: ErrNoAck}, Change{}, nil)
	}
}

func (r *Request) acked() {
	r.timer.stop()
	r.deadline.stop()
}

// RFC 3261 section 9.2
func (r *Request) cancelled(ctx context.Context, cancel *sip.Request) {
	s := r.s

	if r.forked() && !s.uac {
		for _, m := range r.group() {
			m.s.emitReceived(ctx, cancel)
			m.s.end(ctx, End{Cause: EndCancelled, Remote: true, Request: cancel})
		}

		return
	}

	if r.final && r.held == nil {
		return
	}

	s.emitReceived(ctx, cancel)

	if r == s.initial() {
		s.end(ctx, End{Cause: EndCancelled, Remote: true, Request: cancel})
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
			h.Cancelled(ctx, s, r, cancel)
		}
	})
}

func (r *Request) failed(ctx context.Context, err error) {
	if r.forked() && !r.s.uac {
		for _, m := range r.group() {
			m.failedOne(ctx, err)
		}

		return
	}

	r.failedOne(ctx, err)
}

func (r *Request) failedOne(ctx context.Context, err error) {
	s := r.s

	r.final = true
	r.held = nil

	if r == s.initial() {
		s.end(ctx, failureEnd(err))
	}

	if s.in == r {
		s.in = nil
		s.sdp.Fail(r.key)
	}

	s.a.untrack(r)
	s.modified()
	s.pump()
}

// RFC 3261 section 13.3
func (a *Agent) invite(ctx context.Context, st *Strand, tx *transaction.ServerTransaction, req *sip.Request) {
	if tx.State() == transaction.Terminated {
		a.untrack(&Request{a: a, tx: tx, req: req})
		return
	}

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

	r := newRequest(ctx, s, tx, req)
	r.role = s.offerRequest(r.key, sessionSDP(req.Envelope))
	r.forks = &forkSet{members: []*Request{r}}
	s.in = r
	s.notePeer(req.Header)

	if !a.addIncoming(s, r) {
		a.reply(tx, 503)
		return
	}

	r.answerBy = st.after(s.policy.answerTimeout(), r.unanswered)

	st.emit(func() {
		h := a.cfg.OnInvite(ctx, r)

		_ = st.do(func() error {
			if !s.handlerSet {
				s.h = h
			}

			return nil
		})
	})
}

// RFC 3891 section 3
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

func addOptionTag(h *sip.Header, name, tag string) {
	for i, f := range *h {
		if strings.EqualFold(sip.LongName(f.Name), name) {
			(*h)[i].Value = f.Value + ", " + tag
			return
		}
	}

	h.Add(name, tag)
}
