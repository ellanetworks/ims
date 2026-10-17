// Package ua is the core of a SIP user agent: dialogs and their usages, INVITE sessions as UAC and UAS with their
// offer/answer exchanges, reliable provisional responses, UPDATE, re-INVITE and session timers, and requests outside
// dialogs (RFC 3261 §8, §12–15, §22; RFC 3262; RFC 3264; RFC 3311; RFC 4028; RFC 5057). It sits on a transaction
// layer, and leaves to the application what to offer, how to answer, and what other requests to send.
package ua

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/transaction"
)

var (
	ErrClosed = errors.New("sip/ua: agent closed")

	// ErrPending refuses an offer while one is pending, or before the initial exchange completed, and a re-INVITE
	// while another INVITE transaction is in progress on the session (RFC 3261 §14.1, RFC 3311 §5.1).
	ErrPending = errors.New("sip/ua: an offer or an INVITE transaction is pending")

	ErrState = errors.New("sip/ua: not allowed in the state of the session")

	// ErrCancelled ends a re-INVITE cancelled before it was sent.
	ErrCancelled = errors.New("sip/ua: re-INVITE cancelled")

	// ErrNoPrack and ErrNoAck are the End.Err of a session that timed out without the PRACK of a reliable 1xx of the
	// agent (RFC 3262 §3), or the ACK of its 2xx (RFC 3261 §13.3.1.4).
	ErrNoPrack = errors.New("sip/ua: no PRACK to the reliable 1xx")
	ErrNoAck   = errors.New("sip/ua: no ACK to the 2xx")
)

// Methods are the methods an agent allows by default (RFC 3261 §20.5).
var Methods = []string{"INVITE", "ACK", "CANCEL", "BYE", "PRACK", "UPDATE", "INFO", "REFER", "NOTIFY", "MESSAGE", "OPTIONS"}

// SessionTimer is the session timer policy of an agent (RFC 4028).
type SessionTimer struct {
	// Interval is the session interval the agent asks for in its initial INVITE, and in a 2xx to an INVITE that asks
	// for none. Zero asks for none.
	Interval time.Duration

	// MinSE is the smallest session interval the agent accepts, 90 s at least; a request asking for less gets a 422
	// (RFC 4028 §9).
	MinSE time.Duration

	// Refresher is who refreshes when the UAC lets the agent choose, as UAS: RefresherUAC or RefresherUAS. A 2xx with
	// its own Session-Expires chooses for itself.
	Refresher string
}

type Config struct {
	// Transaction configures the transaction layer the agent creates; its Handler is the agent.
	Transaction transaction.Config

	// Prepare completes each request the agent sends, the ACK included, for the transport: it sets the Flow and adds
	// the top Via, with a branch from sip.NewBranch, and any header field of the access network (TS 24.229 §5.1.2A).
	//
	// Prepare, Respond and Authenticate complete messages: they must not call the agent, its sessions or requests.
	Prepare func(req *sip.Request) error

	// Respond completes each response the agent sends.
	Respond func(req *sip.Request, res *sip.Response)

	// Authenticate adds to a request the credentials a 401 or 407 challenge asks for; the agent sends it again, with
	// the next CSeq, up to twice (RFC 3261 §22.2). An error, or no Authenticate, ends with the challenge.
	Authenticate func(req *sip.Request, challenge *sip.Response) error

	// Contact is the Contact of the dialogs the agent creates, unless a request or a response carries its own; a
	// session keeps its own for the requests and responses that refresh its target.
	Contact sip.Address

	// Allow are the methods the agent allows; Methods when nil.
	Allow []string

	// Supported are the option tags the agent supports, beyond 100rel and timer, which it implements (RFC 3261
	// §8.2.2.3): precondition, for one, when the application handles preconditions.
	Supported []string

	SessionTimer SessionTimer

	// OnInvite receives an initial INVITE, and returns the handler of its session. Without it, initial INVITEs get a
	// 480.
	OnInvite func(r *Request) Handler

	// OnRequest receives the initial requests other than INVITE. Without it, they get a 405.
	OnRequest func(r *Request)

	Logger *slog.Logger
}

// Handler receives the events of a session. Each function is optional. Handlers run on the loop of the agent, in
// order, after the state change they report: they may call the agent, its sessions and requests, and must not block.
// Long work goes to a goroutine of the application, which calls the agent once done.
type Handler struct {
	// Progress reports a provisional response to an INVITE of the agent, initial or re-INVITE; a reliable one once
	// its PRACK completed, so that the requests the application sends then follow it. s is nil for a response to
	// the initial INVITE without a To tag, which creates no dialog.
	Progress func(s *Session, res *sip.Response)

	// Established reports that the session is confirmed by m: the 2xx (UAC), the ACK (UAS).
	Established func(s *Session, m sip.Message)

	// Answer reports the peer's answer to an offer of the agent.
	Answer func(s *Session, sdp []byte)

	// Pracked reports the PRACK of a reliable 1xx res of the agent (RFC 3262 §3), once the queued reliable 1xx and the
	// 2xx that waited for it are sent.
	Pracked func(s *Session, res *sip.Response)

	// Offer asks for the answer to an offer of the peer in a response: a reliable 1xx, answered in the PRACK, or a
	// 2xx, answered in the ACK (RFC 3261 §13.2.1, RFC 3262 §5). Without it, the offer is refused.
	Offer func(s *Session, o *Offer)

	// Request receives a request of the peer within the session that the agent does not handle itself: re-INVITE,
	// UPDATE and PRACK with a new offer, and the other methods. It must be responded to. Without it, a re-INVITE or
	// an UPDATE gets a 488, other requests a 405. The agent answers itself the refreshes: UPDATE without an offer,
	// re-INVITE without one or with the previous offer unchanged (RFC 3264 §8, RFC 4028 §7.4).
	Request func(s *Session, r *Request)

	// Cancelled reports a re-INVITE of the peer its CANCEL ended before the final response of the application: the
	// agent answered it with a 487, and the session goes on as before (RFC 3261 §9.2).
	Cancelled func(s *Session, r *Request, cancel *sip.Request)

	// Forked decides on a 2xx to the initial INVITE from another dialog than the first one answered, s its session
	// (RFC 3261 §13.2.2.4): true keeps s, confirmed, with its own events; false, or no Forked, acknowledges the 2xx,
	// refusing its offer, and ends s with a BYE.
	Forked func(s *Session, res *sip.Response) bool

	// Ended reports the end of the session.
	Ended func(s *Session, e End)

	// Failed reports the failure of the initial INVITE o (UAC): its final response, or the error of its transaction.
	// Its early sessions end too.
	Failed func(o *Outgoing, res *sip.Response, err error)
}

// Reply is a response the application asks the agent to send.
type Reply struct {
	Code   int
	Reason string

	// Header are header fields added to the response. A Session-Expires there sets the session timer of a 2xx.
	Header sip.Header

	// Bodies are its bodies; an SDP one is the session description.
	Bodies []sip.BodyPart

	// Reliable asks for a reliable provisional response (RFC 3262). A 1xx to an INVITE that requires 100rel is
	// always reliable.
	Reliable bool
}

// Change is what the application adds to a request the agent builds: header fields, and bodies, of which an SDP one
// is the session description.
type Change struct {
	Header sip.Header
	Bodies []sip.BodyPart
}

// SDP is an SDP body.
func SDP(b []byte) sip.BodyPart {
	return sip.NewBodyPart("application/sdp", b)
}

// dialogEntry is a dialog of the agent and its usages (RFC 5057): the INVITE session, or the subscription its NOTIFY
// created. The usages share the dialog, its route set and its CSeq space; the dialog ends with its last usage.
type dialogEntry struct {
	invite *Session
	sub    *Subscription
}

type mergeKey struct {
	callID, fromTag string
	cseq            sip.CSeq
}

// Agent is a SIP user agent. It creates the transaction layer it sends and receives on.
//
// One goroutine, the loop of the agent, owns its dialogs, sessions and outgoing INVITEs, and runs the handlers of the
// application. The methods of the agent, of its sessions and of their requests may be called from any goroutine: from
// a handler, they run at once; from elsewhere, on the loop, waiting for it.
type Agent struct {
	cfg       Config
	layer     *transaction.Layer
	log       *slog.Logger
	clock     transaction.Clock
	allow     []string
	supported []string
	loop      *loop

	// closing counts the BYEs Close waits for.
	closing sync.WaitGroup

	// The fields below belong to the loop.
	closed      bool
	dialogs     map[dialog.ID]*dialogEntry
	invites     map[*transaction.ServerTransaction]*Request
	initials    map[mergeKey]*transaction.ServerTransaction
	outgoing    map[*Outgoing]struct{}
	subscribing map[subscribeKey]*subscribing
	requests    map[*outOfDialog]struct{}
}

func New(cfg Config) (*Agent, error) {
	if cfg.Prepare == nil {
		return nil, errors.New("sip/ua: Config.Prepare is required")
	}

	if cfg.SessionTimer.MinSE < 90*time.Second {
		cfg.SessionTimer.MinSE = 90 * time.Second
	}

	if cfg.SessionTimer.Interval != 0 && cfg.SessionTimer.Interval < cfg.SessionTimer.MinSE {
		return nil, fmt.Errorf("sip/ua: session interval %v below Min-SE %v", cfg.SessionTimer.Interval, cfg.SessionTimer.MinSE)
	}

	if r := cfg.SessionTimer.Refresher; r != "" && r != sip.RefresherUAC && r != sip.RefresherUAS {
		return nil, fmt.Errorf("sip/ua: refresher %q", r)
	}

	a := &Agent{
		cfg:       cfg,
		log:       cfg.Logger,
		allow:     cfg.Allow,
		supported: []string{sip.OptionTag100rel, sip.OptionTagTimer},
		dialogs:   map[dialog.ID]*dialogEntry{},
		invites:   map[*transaction.ServerTransaction]*Request{},
		initials:  map[mergeKey]*transaction.ServerTransaction{},
		outgoing:  map[*Outgoing]struct{}{},

		subscribing: map[subscribeKey]*subscribing{},
		requests:    map[*outOfDialog]struct{}{},
	}

	for _, tag := range cfg.Supported {
		if !containsFold(a.supported, tag) {
			a.supported = append(a.supported, tag)
		}
	}

	if a.log == nil {
		a.log = slog.Default()
	}

	if a.allow == nil {
		a.allow = Methods
	}

	tcfg := cfg.Transaction
	tcfg.Handler = a
	a.layer = transaction.New(tcfg)
	a.clock = a.layer.Clock()
	a.loop = newLoop()

	return a, nil
}

// Layer is the transaction layer of the agent, to listen on.
func (a *Agent) Layer() *transaction.Layer {
	return a.layer
}

// Close ends the sessions of the agent: confirmed ones with a BYE, early ones by cancelling or rejecting their INVITE.
// It waits for those transactions until ctx is done, then closes the transaction layer and stops the loop. The
// sessions end with EndClosed. Close cannot be called from a handler, which the loop waits for.
func (a *Agent) Close(ctx context.Context) error {
	if a.loop.onLoop() {
		return errors.New("sip/ua: Close from a handler")
	}

	err := a.loop.call(func() error {
		if a.closed {
			return ErrClosed
		}

		a.closed = true

		for o := range a.outgoing {
			if err := o.cancel(nil); err != nil {
				a.log.Debug("CANCEL failed", slog.Any("error", err))
			}
		}

		var (
			sessions []*Session
			subs     []*Subscription
		)

		for _, e := range a.dialogs {
			if e.invite != nil {
				sessions = append(sessions, e.invite)
			}

			if e.sub != nil {
				subs = append(subs, e.sub)
			}
		}

		for _, att := range a.subscribing {
			if att.first.d == nil {
				subs = append(subs, att.first)
			}
		}

		for _, s := range sessions {
			s.close()
		}

		for _, s := range subs {
			s.close()
		}

		return nil
	})
	if err != nil {
		return nil
	}

	done := make(chan struct{})

	go func() {
		a.closing.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}

	err = a.layer.Close()

	// What the closed transaction layer did not complete fails now, before the loop stops.
	_ = a.loop.call(func() error {
		for o := range a.outgoing {
			o.closed()
		}

		for c := range a.requests {
			c.finish(nil, ErrClosed)
		}

		return nil
	})

	a.loop.stop()

	return err
}

func (a *Agent) allowValue() string {
	return strings.Join(a.allow, ", ")
}

func (a *Agent) supportedValue() string {
	return strings.Join(a.supported, ", ")
}

// session is the INVITE usage of a dialog, if any.
func (a *Agent) session(id dialog.ID) *Session {
	if e := a.dialogs[id]; e != nil {
		return e.invite
	}

	return nil
}

func (a *Agent) entry(id dialog.ID) *dialogEntry {
	e := a.dialogs[id]
	if e == nil {
		e = &dialogEntry{}
		a.dialogs[id] = e
	}

	return e
}

func (a *Agent) addSession(s *Session) {
	a.entry(s.id).invite = s
}

// removeSession ends the INVITE usage of a dialog, and the dialog with its last usage (RFC 5057 §5.4).
func (a *Agent) removeSession(s *Session) {
	e := a.dialogs[s.id]
	if e == nil || e.invite != s {
		return
	}

	e.invite = nil
	a.prune(s.id, e)
}

func (a *Agent) addSubscription(s *Subscription) {
	a.entry(s.d.ID()).sub = s
}

func (a *Agent) removeSubscription(s *Subscription) {
	id := s.d.ID()

	e := a.dialogs[id]
	if e == nil || e.sub != s {
		return
	}

	e.sub = nil
	a.prune(id, e)
}

func (a *Agent) prune(id dialog.ID, e *dialogEntry) {
	if e.invite == nil && e.sub == nil {
		delete(a.dialogs, id)
	}
}

func (a *Agent) track(r *Request) {
	a.invites[r.tx] = r
}

func (a *Agent) untrack(r *Request) {
	delete(a.invites, r.tx)

	if k, ok := initialKey(r.req); ok && a.initials[k] == r.tx {
		delete(a.initials, k)
	}
}

func initialKey(req *sip.Request) (mergeKey, bool) {
	from, err := req.Header.From()
	if err != nil {
		return mergeKey{}, false
	}

	cseq, err := req.Header.CSeq()
	if err != nil {
		return mergeKey{}, false
	}

	return mergeKey{callID: req.Header.CallID(), fromTag: from.Tag(), cseq: cseq}, true
}

// merged reports a request that reached the agent again on another path (RFC 3261 §8.2.2.2), and otherwise notes it
// as ongoing until its final response.
func (a *Agent) merged(tx *transaction.ServerTransaction, req *sip.Request) bool {
	k, ok := initialKey(req)
	if !ok {
		return false
	}

	if other, ok := a.initials[k]; ok && other != tx {
		return true
	}

	a.initials[k] = tx

	return false
}

// respond completes and sends a response the agent makes to a request.
func (a *Agent) respond(tx *transaction.ServerTransaction, res *sip.Response) error {
	if a.cfg.Respond != nil {
		a.cfg.Respond(tx.Request(), res)
	}

	return tx.Respond(res)
}

func (a *Agent) reply(tx *transaction.ServerTransaction, code int, extra ...sip.Field) {
	res := sip.NewResponse(tx.Request(), code, "")
	for _, f := range extra {
		res.Header.Add(f.Name, f.Value)
	}

	if err := a.respond(tx, res); err != nil {
		a.log.Debug("response failed", slog.Int("code", code), slog.Any("error", err))
	}
}

// prepareAndSend prepares and sends a request in a client transaction. The transaction layer sends it on a goroutine
// of its own.
func (a *Agent) prepareAndSend(req *sip.Request, h transaction.ClientHandler) (*transaction.ClientTransaction, error) {
	if err := a.cfg.Prepare(req); err != nil {
		return nil, fmt.Errorf("sip/ua: prepare %s: %w", req.Method, err)
	}

	return a.layer.Request(req, h)
}

// authenticate completes the next request after a 401 or 407 with credentials (RFC 3261 §22.2). It reports whether to
// send it.
func (a *Agent) authenticate(next *sip.Request, res *sip.Response, tries int) bool {
	if res.StatusCode != 401 && res.StatusCode != 407 || a.cfg.Authenticate == nil || tries >= 2 {
		return false
	}

	if err := a.cfg.Authenticate(next, res); err != nil {
		a.log.Debug("authentication failed", slog.Any("error", err))
		return false
	}

	return true
}

// unsupported are the option tags of the Require of a request the agent does not support (RFC 3261 §8.2.2.3).
func (a *Agent) unsupported(req *sip.Request) []string {
	var out []string

	for _, tag := range req.Header.Elements("Require") {
		if !containsFold(a.supported, tag) && !containsFold(out, tag) {
			out = append(out, tag)
		}
	}

	return out
}

// HandleRequest implements transaction.Handler.
func (a *Agent) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	a.loop.post(func() { a.receive(tx, req) })
}

func (a *Agent) receive(tx *transaction.ServerTransaction, req *sip.Request) {
	a.loop.check()

	to, err := req.Header.To()
	if err != nil {
		a.reply(tx, 400)
		return
	}

	if tags := a.unsupported(req); len(tags) > 0 {
		a.reply(tx, 420, sip.Field{Name: "Unsupported", Value: strings.Join(tags, ", ")})
		return
	}

	if to.Tag() != "" {
		id := dialog.RequestID(req)

		switch e := a.dialogs[id]; {
		case e != nil && e.invite != nil:
			e.invite.receive(tx, req)
		case e != nil && e.sub != nil:
			e.sub.receive(tx, req)
		case req.Method == "NOTIFY" && a.subscribing[subscribeKey{callID: id.CallID, fromTag: id.LocalTag}] != nil:
			a.subscribing[subscribeKey{callID: id.CallID, fromTag: id.LocalTag}].notifyFor(tx, req)
		default:
			a.reply(tx, 481)
		}

		return
	}

	if a.closed {
		a.reply(tx, 503)
		return
	}

	if a.merged(tx, req) {
		a.reply(tx, 482)
		return
	}

	if req.Method == "INVITE" {
		a.invite(tx, req)
		return
	}

	r := newRequest(nil, tx, req)
	r.a = a

	if a.cfg.OnRequest == nil {
		_ = r.respond(Reply{Code: 405, Header: sip.Header{{Name: "Allow", Value: a.allowValue()}}})
		return
	}

	a.loop.emit(func() { a.cfg.OnRequest(r) })
}

// HandleCancel implements transaction.Handler: the transaction layer answered the CANCEL; the INVITE gets a 487
// (RFC 3261 §9.2).
func (a *Agent) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	a.loop.post(func() {
		if r := a.invites[tx]; r != nil {
			r.cancelled(cancel)
		}
	})
}

// HandleAck implements transaction.Handler: the ACK of a 2xx (RFC 3261 §13.3.1.4).
func (a *Agent) HandleAck(ack *sip.Request) {
	a.loop.post(func() {
		if s := a.session(dialog.RequestID(ack)); s != nil {
			s.receiveAck(ack)
		}
	})
}

// HandleTransactionError implements transaction.Handler.
func (a *Agent) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	a.loop.post(func() {
		if r := a.invites[tx]; r != nil {
			r.failed(err)
		}
	})
}

func containsFold(list []string, v string) bool {
	return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, v) })
}
