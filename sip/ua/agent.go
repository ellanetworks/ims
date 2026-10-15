// Package ua is the core of a SIP user agent: INVITE sessions as UAC and UAS, with their dialogs, offer/answer
// exchanges, reliable provisional responses, UPDATE, re-INVITE and session timers (RFC 3261 §8, §12–15; RFC 3262;
// RFC 3264; RFC 3311; RFC 4028). It sits on a transaction layer, and leaves to the application what to offer, how to
// answer, and what other requests to send.
package ua

import (
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

	// ErrPending refuses an offer while one is pending, or a re-INVITE while another INVITE transaction is in
	// progress on the session (RFC 3261 §14.1, RFC 3311 §5.1).
	ErrPending = errors.New("sip/ua: an offer or an INVITE transaction is pending")

	ErrState = errors.New("sip/ua: not allowed in the state of the session")
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

	// Refresher is who refreshes when the UAC lets the agent choose, as UAS: RefresherUAC or RefresherUAS.
	Refresher string
}

type Config struct {
	// Transaction configures the transaction layer the agent creates; its Handler is the agent.
	Transaction transaction.Config

	// Prepare completes each request the agent sends, the ACK included, for the transport: it sets the Flow and adds
	// the top Via, with a branch from sip.NewBranch, and any header field of the access network (TS 24.229 §5.1.2A).
	Prepare func(req *sip.Request) error

	// Respond completes each response the agent sends to a request, before it goes to the transaction layer.
	Respond func(req *sip.Request, res *sip.Response)

	// Contact is the Contact of the dialogs the agent creates, unless a request or a response carries its own.
	Contact sip.Address

	// Allow are the methods the agent allows; Methods when nil.
	Allow []string

	SessionTimer SessionTimer

	// OnInvite receives an initial INVITE, and returns the handler of its session. Without it, initial INVITEs get a
	// 480.
	OnInvite func(r *Request) Handler

	// OnRequest receives the requests outside a session: other initial requests, and requests of other dialogs.
	// Without it, they get a 405, or a 481 within a dialog.
	OnRequest func(tx *transaction.ServerTransaction, req *sip.Request)

	Logger *slog.Logger
}

// Handler receives the events of a session. Each function is optional; the events of a session come one at a time,
// in order.
type Handler struct {
	// Progress reports a provisional response to the initial INVITE (UAC), after the PRACK of a reliable one. s is
	// nil for a response without a To tag, which creates no dialog.
	Progress func(s *Session, res *sip.Response)

	// Established reports that the session is confirmed: by the 2xx (UAC), by the ACK (UAS).
	Established func(s *Session)

	// Answer reports the peer's answer to an offer of the agent.
	Answer func(s *Session, sdp []byte)

	// Offer asks for the answer to an offer of the peer in a response: a reliable 1xx, answered in the PRACK, or a
	// 2xx, answered in the ACK (RFC 3261 §13.2.1, RFC 3262 §5). Without it, the offer is rejected.
	Offer func(s *Session, o *Offer)

	// Request receives a request of the peer within the session that the agent does not handle itself: re-INVITE,
	// UPDATE and PRACK with an offer, and the other methods. It must be responded to. Without it, a re-INVITE or an
	// UPDATE gets a 488, other requests a 405.
	Request func(s *Session, r *Request)

	// Ended reports the end of the session.
	Ended func(s *Session, e End)

	// Failed reports the failure of the initial INVITE (UAC): its final response, or the error of its transaction.
	Failed func(res *sip.Response, err error)
}

// Reply is a response the application asks the agent to send.
type Reply struct {
	Code   int
	Reason string

	// Header are header fields added to the response.
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

// Agent is a SIP user agent. It creates the transaction layer it sends and receives on.
type Agent struct {
	cfg   Config
	layer *transaction.Layer
	log   *slog.Logger
	clock transaction.Clock
	allow []string

	mu       sync.Mutex
	closed   bool
	sessions map[dialog.ID]*Session
	invites  map[*transaction.ServerTransaction]*Request
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
		cfg:      cfg,
		log:      cfg.Logger,
		allow:    cfg.Allow,
		sessions: map[dialog.ID]*Session{},
		invites:  map[*transaction.ServerTransaction]*Request{},
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

	return a, nil
}

// Layer is the transaction layer of the agent, to listen on and to send requests outside sessions.
func (a *Agent) Layer() *transaction.Layer {
	return a.layer
}

func (a *Agent) Close() error {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()

	return a.layer.Close()
}

func (a *Agent) allowValue() string {
	return strings.Join(a.allow, ", ")
}

func (a *Agent) allows(method string) bool {
	return slices.Contains(a.allow, method)
}

func (a *Agent) session(id dialog.ID) *Session {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.sessions[id]
}

func (a *Agent) add(s *Session) {
	a.mu.Lock()
	a.sessions[s.id] = s
	a.mu.Unlock()
}

func (a *Agent) remove(s *Session) {
	a.mu.Lock()
	if a.sessions[s.id] == s {
		delete(a.sessions, s.id)
	}
	a.mu.Unlock()
}

// respond sends a response the agent makes to a request, completed by Config.Respond.
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

// send prepares and sends a request in a client transaction.
func (a *Agent) send(req *sip.Request, h transaction.ClientHandler) (*transaction.ClientTransaction, error) {
	if err := a.cfg.Prepare(req); err != nil {
		return nil, fmt.Errorf("sip/ua: prepare %s: %w", req.Method, err)
	}

	return a.layer.Request(req, h)
}

// HandleRequest implements transaction.Handler.
func (a *Agent) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	to, err := req.Header.To()
	if err != nil {
		a.reply(tx, 400)
		return
	}

	if to.Tag() != "" {
		if s := a.session(dialog.RequestID(req)); s != nil {
			s.receive(tx, req)
			return
		}

		a.other(tx, req, 481)

		return
	}

	if req.Method == "INVITE" {
		a.invite(tx, req)
		return
	}

	a.other(tx, req, 405)
}

func (a *Agent) other(tx *transaction.ServerTransaction, req *sip.Request, code int) {
	if a.cfg.OnRequest != nil {
		a.cfg.OnRequest(tx, req)
		return
	}

	if code == 405 {
		a.reply(tx, code, sip.Field{Name: "Allow", Value: a.allowValue()})
		return
	}

	a.reply(tx, code)
}

// HandleCancel implements transaction.Handler: the transaction layer answered the CANCEL; the INVITE gets a 487
// (RFC 3261 §9.2).
func (a *Agent) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	a.mu.Lock()
	r := a.invites[tx]
	a.mu.Unlock()

	if r != nil {
		r.cancelled(cancel)
	}
}

// HandleAck implements transaction.Handler: the ACK of a 2xx (RFC 3261 §13.3.1.4).
func (a *Agent) HandleAck(ack *sip.Request) {
	if s := a.session(dialog.RequestID(ack)); s != nil {
		s.receiveAck(ack)
	}
}

// HandleTransactionError implements transaction.Handler.
func (a *Agent) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	a.mu.Lock()
	r := a.invites[tx]
	a.mu.Unlock()

	if r != nil {
		r.failed(err)
	}
}
