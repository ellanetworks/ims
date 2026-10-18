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

	// ErrStale ends a re-INVITE or an UPDATE of a fixed Change with an offer that waited, for a transaction in progress
	// or after a 491, while another offer/answer exchange completed: its offer no longer builds on the session
	// (RFC 3264 §8), so it is not sent. Modify, whose Build makes the change anew, does not go stale.
	ErrStale = errors.New("sip/ua: the session changed while the offer waited")

	// ErrNoPrack and ErrNoAck are the End.Err of a session that timed out without the PRACK of a reliable 1xx of the
	// agent (RFC 3262 §3), or the ACK of its 2xx (RFC 3261 §13.3.1.4).
	ErrNoPrack = errors.New("sip/ua: no PRACK to the reliable 1xx")
	ErrNoAck   = errors.New("sip/ua: no ACK to the 2xx")

	// ErrNoAnswer is the End.Err of a session whose application did not answer the offer of a 2xx in time.
	ErrNoAnswer = errors.New("sip/ua: no answer to the offer of the 2xx")

	// ErrIntervalTooBrief is the error of accepting a SUBSCRIBE that asked for less than Acceptance.MinExpires: the
	// agent answered it with a 423.
	ErrIntervalTooBrief = errors.New("sip/ua: subscription interval too brief")
)

// Error is the failure of an operation of a session, an INVITE or a request of the peer, in the state it was in. Err
// is one of the errors of the package, such as ErrState, or the error of the transaction layer.
type Error struct {
	// Op is the method called: "Bye", "Reinvite", "Respond"...
	Op    string
	State State
	Err   error
}

func (e *Error) Error() string {
	return fmt.Sprintf("sip/ua: %s in the %s state: %v", e.Op, e.State, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

// Methods are the methods an agent allows by default (RFC 3261 §20.5): those of its INVITE sessions, which it handles
// itself, and OPTIONS, which it answers. An application that handles others in Handler.Request or OnRequest, such as
// INFO, MESSAGE, REFER or NOTIFY, lists them in Config.Allow; requests of methods not allowed get a 405.
var Methods = []string{"INVITE", "ACK", "CANCEL", "BYE", "PRACK", "UPDATE", "OPTIONS"}

// Extensions are the SIP extensions the application implements with the help of the agent. The agent ignores the
// header fields of one off, as a user agent without it does, and refuses its option tag in Require with a 420.
type Extensions struct {
	// Replaces: an INVITE with Replaces goes to the strand of the dialog it names, and Request.Replaces finds it
	// (RFC 3891 §3). Off, an INVITE with Replaces is a new call.
	Replaces bool

	// TargetDialog: a request with a Target-Dialog goes to the strand of the dialog it names, and Agent.TargetDialog
	// finds it (RFC 4538 §4), as for a REFER outside a dialog (TS 24.379 §6.2.4.2).
	TargetDialog bool

	// NoReferSub: a REFER asking for no subscription may be accepted without one (RFC 4488, ReferAcceptance).
	NoReferSub bool

	// ExplicitSub: a REFER requiring explicitsub is accepted with a Refer-Events-At (RFC 7614 §4.3).
	ExplicitSub bool
}

func (e Extensions) tags() []string {
	var tags []string

	for _, t := range []struct {
		on  bool
		tag string
	}{
		{e.Replaces, sip.OptionTagReplaces},
		{e.TargetDialog, sip.OptionTagTargetDialog},
		{e.NoReferSub, sip.OptionTagNoReferSub},
		{e.ExplicitSub, sip.OptionTagExplicitSub},
	} {
		if t.on {
			tags = append(tags, t.tag)
		}
	}

	return tags
}

// Policy is what the agent does on its own in a session beyond what the RFCs require of every user agent, which it
// always does: the ACK of a 2xx and the PRACK of a reliable 1xx, the responses to conflicting requests (RFC 3261 §14,
// RFC 3311 §5.2), the session timer (RFC 4028: refreshes as refresher, 422, the retry with its Min-SE), the challenges
// with Config.Authenticate, and the answers to the requests the application has no handler for. The zero Policy, for
// servers such as a B2BUA, does nothing more: every request of the peer and every failure of the application's
// requests reach the application. ClientPolicy suits the user agent of a user.
type Policy struct {
	// AnswerRefreshes answers the session refreshes of the peer (Request.Refresh) that carry no body but an SDP one,
	// as Request.AnswerUnchanged does. Otherwise they reach Handler.Request with the other modifications.
	AnswerRefreshes bool

	// RetryGlare sends a re-INVITE or an UPDATE of the application that got a 491 again, once, after the delay of
	// RFC 3261 §14.1. Otherwise its done receives the 491.
	RetryGlare bool

	// SkipRefreshes leaves the session refreshes of the agent unsent: a fault, for tests of the peer's session timer
	// (RFC 4028 §10). The session still ends when it expires.
	SkipRefreshes bool

	// AnswerTimeout is how long the application has to answer an INVITE of the peer, initial or re-INVITE: past it,
	// the agent answers 480 to an initial INVITE, and to a re-INVITE a 500 with Retry-After, the unchanged answer of
	// a refresh, or a 2xx once an exchange within it changed the session (RFC 6141 §3.3). Three minutes when zero, as
	// Timer C (RFC 3261 §16.6). The other requests have 32*T1, half the time the peer waits (Timer F).
	AnswerTimeout time.Duration
}

// defaultAnswerTimeout is the AnswerTimeout of a Policy without one.
const defaultAnswerTimeout = 3 * time.Minute

func (p Policy) answerTimeout() time.Duration {
	if p.AnswerTimeout > 0 {
		return p.AnswerTimeout
	}

	return defaultAnswerTimeout
}

// ClientPolicy answers the session refreshes of the peer and retries glare.
var ClientPolicy = Policy{AnswerRefreshes: true, RetryGlare: true}

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
	// Layer is a transaction layer the agent shares with other users, such as a proxy on the same ports: their
	// dispatcher gives the agent the requests it owns (Agent.Owns), and its CANCELs, ACKs and transaction errors.
	// Without one, the agent creates a layer of its own from Transaction, and is its Handler.
	Layer *transaction.Layer

	// Transaction configures the transaction layer the agent creates without Layer.
	Transaction transaction.Config

	// Prepare completes each request the agent sends, the ACK included, for the transport: it sets the Flow and adds
	// the top Via, with a branch from sip.NewBranch, and any header field of the access network (TS 24.229 §5.1.2A).
	//
	// Prepare, Respond and Authenticate complete messages, with the strand of the message held: they must not call the
	// agent, its sessions or requests, nor wait for a lock the application holds while calling them.
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
	// §8.2.2.3), and those of Extensions: advertised in every request and response of the agent that carries
	// Supported, and accepted in Require.
	Supported []string

	// Understood are option tags accepted in Require, but advertised only where the application puts them, in a
	// request, a Change or a Reply: precondition, which a UE advertises for the calls it runs them on (TS 24.229
	// §5.1.3.1 b).
	Understood []string

	SessionTimer SessionTimer

	// Extensions are the extensions the application implements with the agent: their option tags are in Supported
	// and accepted in Require (RFC 3261 §8.2.2.3, §20.37).
	Extensions Extensions

	// Policy is the policy of the sessions of the agent, unless WithPolicy or Session.SetPolicy sets another.
	Policy Policy

	// StrandFor chooses the strand of an initial request of the peer, such as the INVITEs of the participants of a
	// group call to the group's (TS 24.379 §10.1.1.4.5.1): nil gives it a strand of its own. A Replaces or a
	// Target-Dialog of an extension of the agent naming one of its dialogs takes precedence. It runs as requests come,
	// before any strand: it must be fast, and must not call the agent.
	StrandFor func(req *sip.Request) *Strand

	// OnInvite receives an initial INVITE, and returns the handler of its session. Without it, initial INVITEs get a
	// 480.
	OnInvite func(r *Request) Handler

	// OnRequest receives the initial requests other than INVITE. Without it, OPTIONS gets a 200 (RFC 3261 §11.2), the
	// other methods a 405.
	OnRequest func(r *Request)

	Logger *slog.Logger
}

// Handler receives the events of a session. Each function is optional. Handlers run on the strand of the session, in
// order, after the state change they report: they may call the agent, its sessions and requests, and should not
// block, the strand waiting for them. Long work goes to a goroutine of the application, which calls the agent once
// done, or posts to the strand.
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

	// Request receives a request of the peer within the session other than BYE, ACK, CANCEL and the PRACKs without an
	// offer, which the agent handles: re-INVITE and UPDATE, session refreshes included unless Policy.AnswerRefreshes
	// answers them (Request.Refresh), a PRACK with a new offer, and the other methods Config.Allow lists. It must be
	// responded to. Without it, the agent answers the refreshes, a re-INVITE or an UPDATE that changes the session
	// gets a 488, an OPTIONS a 200, other requests a 500.
	Request func(s *Session, r *Request)

	// Bye receives the BYE of the peer, for the application to respond to, once the other leg of a call has ended for
	// one: the session ends as the response is sent, or 32*T1 after the BYE, when the agent sends a 200 itself, half
	// the time the peer waits for one (RFC 3261 §17.1.2.2, Timer F).
	// Without it, the agent answers the BYE with a 200 and the session ends at once (RFC 3261 §15.1.2).
	Bye func(s *Session, r *Request)

	// Cancelled reports a re-INVITE of the peer its CANCEL ended before the final response of the application: the
	// agent answered it with a 487, and the session goes on as before (RFC 3261 §9.2); or, when an offer/answer
	// exchange within the re-INVITE had already changed the session, with a 2xx (RFC 6141 §3.3, §3.8).
	Cancelled func(s *Session, r *Request, cancel *sip.Request)

	// Decorate completes each message the agent sends in the session, requests and responses, those it makes on its
	// own included (ACK, PRACK, refreshes, BYE, automatic responses), before Config.Prepare or Config.Respond. Unlike
	// the other functions, it runs as the message is built, with the strand held: it must not call the agent.
	Decorate func(s *Session, m sip.Message)

	// Forked decides on a 2xx to the initial INVITE from another dialog than the first one answered, s its session
	// (RFC 3261 §13.2.2.4): true keeps s, confirmed, with its own events; false, or no Forked, acknowledges the 2xx,
	// refusing its offer, and ends s with a BYE.
	Forked func(s *Session, res *sip.Response) bool

	// Received reports each message of the peer the session took, once handled, in order with the other events:
	// requests within the dialog, its ACKs, the responses to the requests of the agent; the retransmissions the
	// transaction layer or the session absorbed excluded. A response to the initial INVITE that created no session,
	// as one without a To tag or a final failure, comes with s nil. The message is the agent's: read it, or clone it.
	Received func(s *Session, m sip.Message)

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

	// AfterPrack holds a 2xx to an INVITE until the PRACK of the last reliable 1xx, even one without a session
	// description, which RFC 3262 §3 alone does not require (TS 24.379 §6.3.2.2.5.2).
	AfterPrack bool
}

// Change is what the application adds to a request the agent builds: header fields, and bodies, of which an SDP one
// is the session description.
type Change struct {
	Header sip.Header
	Bodies []sip.BodyPart
}

// Reasons is a Change carrying Reason header fields (RFC 3326), for a BYE or a CANCEL.
func Reasons(rs ...sip.Reason) Change {
	var c Change

	for _, r := range rs {
		c.Header.Add("Reason", r.String())
	}

	return c
}

// apply adds the change to a request the agent built.
func (c Change) apply(req *sip.Request) {
	for _, f := range c.Header {
		req.Header.Add(f.Name, f.Value)
	}

	if len(c.Bodies) > 0 {
		req.SetBodies(c.Bodies...)
	}
}

// SDP is an SDP body.
func SDP(b []byte) sip.BodyPart {
	return sip.NewBodyPart("application/sdp", b)
}

// dialogEntry is a dialog of the agent and its usages (RFC 5057 §4): at most one INVITE session, and subscriptions,
// told apart by their Event (RFC 6665 §4.5.2). The usages share the dialog, its route set and its CSeq space, and its
// strand; the dialog ends with its last usage. A failure that destroys the dialog ends them all (RFC 5057 §5.1).
//
// Once its last usage ended, the entry stays for 64*T1 with none (RFC 3261 §17.1.1.2): the usages that ended still
// acknowledge and end a 2xx that comes late, and an INVITE replacing the dialog gets a 603 (RFC 3891 §3).
type dialogEntry struct {
	st     *Strand
	invite *Session
	subs   []*Subscription

	// notifs are the subscriptions of the agent as notifier: of a SUBSCRIBE, or implicit ones of REFERs.
	notifs []*ServerSubscription

	// invited: an INVITE session used the dialog; ended: no usage is left.
	invited bool
	ended   bool
}

func (e *dialogEntry) empty() bool {
	return e.invite == nil && len(e.subs) == 0 && len(e.notifs) == 0
}

type mergeKey struct {
	callID, fromTag string
	cseq            sip.CSeq
}

// Agent is a SIP user agent, on a transaction layer of its own or shared.
//
// Each dialog set of the agent (an initial INVITE or SUBSCRIBE and the dialogs of its responses and requests) is on
// a Strand, which serialises its state changes and runs its handlers. The methods of the agent, of its sessions,
// subscriptions and requests may be called from any goroutine, handlers included.
type Agent struct {
	cfg       Config
	layer     *transaction.Layer
	ownLayer  bool
	log       *slog.Logger
	clock     transaction.Clock
	sched     transaction.Scheduler
	allow     []string
	supported []string

	// wmu guards the counts of the strands at work and of the requests Close waits for.
	wmu     sync.Mutex
	wcond   sync.Cond
	workers int
	closing int

	// mu guards the tables of the agent. A strand's lock may be held when taking it, not the other way.
	mu          sync.Mutex
	closed      bool
	dialogs     map[dialog.ID]*dialogEntry
	invites     map[*transaction.ServerTransaction]*Request
	initials    map[mergeKey]*transaction.ServerTransaction
	outgoing    map[*Outgoing]struct{}
	subscribing map[subscribeKey]*subscribing
	requests    map[*outOfDialog]struct{}
	pubs        map[*Publication]struct{}
	keyed       [64]*Strand
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
		pubs:        map[*Publication]struct{}{},
	}

	for _, tag := range append(cfg.Extensions.tags(), cfg.Supported...) {
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

	a.wcond.L = &a.wmu

	if cfg.Layer != nil {
		a.layer = cfg.Layer
	} else {
		tcfg := cfg.Transaction
		tcfg.Handler = a
		a.layer = transaction.New(tcfg)
		a.ownLayer = true
	}

	a.clock = a.layer.Clock()
	a.sched = a.layer.Scheduler()

	return a, nil
}

// Layer is the transaction layer of the agent, to listen on.
func (a *Agent) Layer() *transaction.Layer {
	return a.layer
}

// working counts the strands at work.
func (a *Agent) working(delta int) {
	a.wmu.Lock()
	a.workers += delta
	a.wcond.Broadcast()
	a.wmu.Unlock()
}

// await counts the requests Close waits for: their transactions, once sent.
func (a *Agent) await(delta int) {
	a.wmu.Lock()
	a.closing += delta
	a.wcond.Broadcast()
	a.wmu.Unlock()
}

// waitUntil waits for cond, under wmu, until ctx is done.
func (a *Agent) waitUntil(ctx context.Context, cond func() bool) {
	stop := context.AfterFunc(ctx, func() {
		a.wmu.Lock()
		a.wcond.Broadcast()
		a.wmu.Unlock()
	})
	defer stop()

	a.wmu.Lock()
	defer a.wmu.Unlock()

	for !cond() && ctx.Err() == nil {
		a.wcond.Wait()
	}
}

// Owns reports whether a request belongs to the agent, for the dispatcher of a shared layer: a request within one of
// its dialogs, or a NOTIFY of one of its SUBSCRIBEs. The dispatcher decides on the initial requests itself.
func (a *Agent) Owns(req *sip.Request) bool {
	id := dialog.RequestID(req)
	if id.LocalTag == "" {
		return false
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.dialogs[id] != nil {
		return true
	}

	return req.Method == "NOTIFY" && a.subscribing[subscribeKey{callID: id.CallID, fromTag: id.LocalTag}] != nil
}

// Close ends the sessions of the agent, confirmed ones with a BYE, early ones by cancelling or rejecting their INVITE,
// and its subscriptions, unsubscribing. It waits for those transactions until ctx is done, then closes the
// transaction layer the agent created, and waits, until ctx is done too, for the strands to deliver the last events.
// The sessions and subscriptions end with EndClosed. Close must not be called from a handler, whose strand it waits for.
func (a *Agent) Close(ctx context.Context) error {
	a.mu.Lock()

	if a.closed {
		a.mu.Unlock()
		return nil
	}

	a.closed = true

	var (
		outgoing []*Outgoing
		sessions []*Session
		subs     []*Subscription
		notifs   []*ServerSubscription
	)

	for o := range a.outgoing {
		outgoing = append(outgoing, o)
	}

	for _, e := range a.dialogs {
		if e.invite != nil {
			sessions = append(sessions, e.invite)
		}

		subs = append(subs, e.subs...)
		notifs = append(notifs, e.notifs...)
	}

	for _, att := range a.subscribing {
		subs = append(subs, att.first)
	}

	pubs := make([]*Publication, 0, len(a.pubs))
	for p := range a.pubs {
		pubs = append(pubs, p)
	}

	a.mu.Unlock()

	for _, p := range pubs {
		_ = p.st.do(func() error {
			p.close()
			return nil
		})
	}

	// The INVITEs cancelled: Close waits for their final response, a 2xx crossing the CANCEL acknowledged and ended
	// with a BYE (RFC 3261 §15), or for 64*T1.
	for _, o := range outgoing {
		_ = o.st.do(func() error {
			o.awaitFinal()

			if err := o.cancelWith(Change{}); err != nil {
				a.log.Debug("CANCEL failed", slog.Any("error", err))
			}

			return nil
		})
	}

	// The notifications first, in their dialogs still: a REFER subscription in the dialog of a session.
	for _, n := range notifs {
		_ = n.st.do(func() error {
			n.close()
			return nil
		})
	}

	for _, s := range sessions {
		_ = s.st.do(func() error {
			s.close()
			return nil
		})
	}

	for _, s := range subs {
		_ = s.st.do(func() error {
			if s.d != nil || s.att.first == s {
				s.close()
			}

			return nil
		})
	}

	a.waitUntil(ctx, func() bool { return a.closing == 0 })

	var err error

	if a.ownLayer {
		err = a.layer.Shutdown(ctx)
	}

	// What the transaction layer did not complete fails now.
	a.mu.Lock()

	outgoing = nil

	for o := range a.outgoing {
		outgoing = append(outgoing, o)
	}

	requests := make([]*outOfDialog, 0, len(a.requests))
	for c := range a.requests {
		requests = append(requests, c)
	}

	subs, notifs = nil, nil

	for _, e := range a.dialogs {
		subs = append(subs, e.subs...)
		notifs = append(notifs, e.notifs...)
	}

	pubs = pubs[:0]
	for p := range a.pubs {
		pubs = append(pubs, p)
	}

	a.mu.Unlock()

	// The subscriptions whose terminated NOTIFY did not come in time, or not answered; the publications whose removal
	// did not complete.
	for _, s := range subs {
		_ = s.st.do(func() error {
			s.end(End{Cause: EndClosed})
			return nil
		})
	}

	for _, n := range notifs {
		_ = n.st.do(func() error {
			n.end(End{Cause: EndClosed})
			return nil
		})
	}

	for _, p := range pubs {
		_ = p.st.do(func() error {
			p.end(End{Cause: EndClosed})
			return nil
		})
	}

	for _, o := range outgoing {
		_ = o.st.do(func() error {
			o.closed()
			return nil
		})
	}

	for _, c := range requests {
		_ = c.st.do(func() error {
			c.finish(nil, ErrClosed)
			return nil
		})
	}

	a.waitUntil(ctx, func() bool { return a.workers == 0 })

	return err
}

func (a *Agent) allowValue() string {
	return strings.Join(a.allow, ", ")
}

func (a *Agent) supportedValue() string {
	return strings.Join(a.supported, ", ")
}

// The functions below use the tables of the agent under a.mu; they may be called with a strand held.

// Session is the INVITE session of the dialog id, as the agent sees it (local and remote tags), nil without one.
func (a *Agent) Session(id dialog.ID) *Session {
	a.mu.Lock()
	defer a.mu.Unlock()

	if e := a.dialogs[id]; e != nil {
		return e.invite
	}

	return nil
}

// TargetDialog is the INVITE session of the dialog a request outside it designates with its Target-Dialog (RFC 4538
// §4), nil without one, with both tags, matching. The agent gives a request with a matching Target-Dialog the strand
// of the session. Authorising the request on that ground is up to the application.
func (a *Agent) TargetDialog(req *sip.Request) *Session {
	if !a.cfg.Extensions.TargetDialog {
		return nil
	}

	td, ok, err := req.Header.TargetDialog()
	if !ok || err != nil {
		return nil
	}

	return a.Session(dialog.TargetID(td))
}

// strandOf is the strand of the dialog a request outside it designates, by its Replaces (RFC 3891) or its
// Target-Dialog (RFC 4538): the replacement, or the request on a dialog, is then serialised with the dialog.
func (a *Agent) strandOf(req *sip.Request) *Strand {
	var id dialog.ID

	ext := a.cfg.Extensions

	if r, ok, err := req.Header.Replaces(); ext.Replaces && ok && err == nil && req.Method == "INVITE" {
		id = dialog.ReplacesID(r)
	} else if td, ok, err := req.Header.TargetDialog(); ext.TargetDialog && ok && err == nil {
		id = dialog.TargetID(td)
	} else {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if e := a.dialogs[id]; e != nil {
		return e.st
	}

	return nil
}

// replaced is the dialog entry an INVITE replaces, and its INVITE session.
func (a *Agent) replaced(id dialog.ID) (e dialogEntry, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if p := a.dialogs[id]; p != nil {
		return *p, true
	}

	return dialogEntry{}, false
}

func (a *Agent) entry(id dialog.ID, st *Strand) *dialogEntry {
	e := a.dialogs[id]
	if e == nil {
		e = &dialogEntry{st: st}
		a.dialogs[id] = e
	}

	if e.st != st {
		panic("sip/ua: usages of one dialog on two strands")
	}

	e.ended = false

	return e
}

func (a *Agent) addSession(s *Session) {
	a.mu.Lock()
	defer a.mu.Unlock()

	e := a.entry(s.id, s.st)
	e.invite, e.invited = s, true
}

// addIncoming registers the session of an initial INVITE of the peer, unless the agent is closed.
func (a *Agent) addIncoming(s *Session, r *Request) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return false
	}

	a.invites[r.tx] = r

	e := a.entry(s.id, s.st)
	e.invite, e.invited = s, true

	return true
}

// removeSession ends the INVITE usage of a dialog. It reports whether that was the last usage, which ends the dialog
// (RFC 5057 §4).
func (a *Agent) removeSession(s *Session) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	e := a.dialogs[s.id]
	if e == nil {
		return true
	}

	if e.invite != s {
		return false
	}

	e.invite = nil

	return a.prune(s.id, e)
}

func (a *Agent) addSubscription(s *Subscription) {
	a.mu.Lock()
	defer a.mu.Unlock()

	e := a.entry(s.d.ID(), s.st)
	e.subs = append(e.subs, s)
}

// removeSubscription ends a subscription usage of a dialog, reporting whether it was its last usage.
func (a *Agent) removeSubscription(s *Subscription) bool {
	id := s.d.ID()

	a.mu.Lock()
	defer a.mu.Unlock()

	e := a.dialogs[id]
	if e == nil {
		return true
	}

	i := slices.Index(e.subs, s)
	if i < 0 {
		return false
	}

	e.subs = slices.Delete(e.subs, i, i+1)

	return a.prune(id, e)
}

// prune notes a dialog whose last usage ended, kept for 64*T1, and reports it.
func (a *Agent) addServerSubscription(s *ServerSubscription) {
	a.mu.Lock()
	defer a.mu.Unlock()

	e := a.entry(s.d.ID(), s.st)
	e.notifs = append(e.notifs, s)
}

// removeServerSubscription ends a subscription usage of the agent as notifier, reporting whether it was the last
// usage of its dialog.
func (a *Agent) removeServerSubscription(s *ServerSubscription) bool {
	id := s.d.ID()

	a.mu.Lock()
	defer a.mu.Unlock()

	e := a.dialogs[id]
	if e == nil {
		return true
	}

	i := slices.Index(e.notifs, s)
	if i < 0 {
		return false
	}

	e.notifs = slices.Delete(e.notifs, i, i+1)

	return a.prune(id, e)
}

func (a *Agent) prune(id dialog.ID, e *dialogEntry) bool {
	if e == nil {
		return true
	}

	if !e.empty() {
		return false
	}

	if !e.ended {
		e.ended = true

		a.clock.AfterFunc(64*a.layer.T1(), func() {
			a.mu.Lock()
			defer a.mu.Unlock()

			if a.dialogs[id] == e && e.ended {
				delete(a.dialogs, id)
			}
		})
	}

	return true
}

// destroyDialog ends the usages of a dialog a failure destroyed (RFC 5057 §5.1), with its strand held: the peer
// no longer has the dialog, so no BYE or unsubscribe goes.
func (a *Agent) destroyDialog(id dialog.ID, res *sip.Response, err error) {
	s, subs, notifs := a.usages(id)
	e := End{Cause: EndLost, Response: res, Err: err}

	for _, sub := range subs {
		sub.end(e)
	}

	for _, n := range notifs {
		n.end(e)
	}

	if s != nil {
		s.end(e)
	}
}

// usages are the usages of a dialog (RFC 5057 §4): its INVITE session, its subscriptions as subscriber and as
// notifier.
func (a *Agent) usages(id dialog.ID) (*Session, []*Subscription, []*ServerSubscription) {
	a.mu.Lock()
	defer a.mu.Unlock()

	e := a.dialogs[id]
	if e == nil {
		return nil, nil, nil
	}

	return e.invite, slices.Clone(e.subs), slices.Clone(e.notifs)
}

// The registrations of usages the application starts refuse them once the agent is closed, under the lock Close
// takes its snapshot with.

func (a *Agent) addOutgoing(o *Outgoing) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return ErrClosed
	}

	a.outgoing[o] = struct{}{}

	return nil
}

func (a *Agent) removeOutgoing(o *Outgoing) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.outgoing, o)
}

func (a *Agent) addSubscribing(att *subscribing) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return ErrClosed
	}

	a.subscribing[att.key] = att

	return nil
}

func (a *Agent) removeSubscribing(att *subscribing) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.subscribing[att.key] == att {
		delete(a.subscribing, att.key)
	}
}

func (a *Agent) addRequest(c *outOfDialog) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return ErrClosed
	}

	a.requests[c] = struct{}{}

	return nil
}

func (a *Agent) removeRequest(c *outOfDialog) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.requests, c)
}

func (a *Agent) addPublication(p *Publication) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return ErrClosed
	}

	a.pubs[p] = struct{}{}

	return nil
}

func (a *Agent) removePublication(p *Publication) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.pubs, p)
}

func (a *Agent) isClosed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.closed
}

// track notes an INVITE of the peer awaiting its final response, for its CANCEL and the errors of its transaction.
func (a *Agent) track(r *Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.invites[r.tx] = r
}

func (a *Agent) untrack(r *Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.invites, r.tx)
}

func (a *Agent) tracked(tx *transaction.ServerTransaction) *Request {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.invites[tx]
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
// as ongoing until its transaction ends.
func (a *Agent) merged(tx *transaction.ServerTransaction, req *sip.Request) bool {
	k, ok := initialKey(req)
	if !ok {
		return false
	}

	a.mu.Lock()

	if other, ok := a.initials[k]; ok && other != tx {
		a.mu.Unlock()
		return true
	}

	a.initials[k] = tx
	a.mu.Unlock()

	tx.OnTerminated(func() {
		a.mu.Lock()
		defer a.mu.Unlock()

		if a.initials[k] == tx {
			delete(a.initials, k)
		}
	})

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

// prepareAndSend sends a copy of req, completed by Config.Prepare, in a client transaction: req stays as built, for
// the request sent again after a challenge, which Prepare completes anew. The transaction layer sends the requests of
// a flow in the order it takes them.
func (a *Agent) prepareAndSend(req *sip.Request, h transaction.ClientHandler) (*transaction.ClientTransaction, error) {
	out := req.Clone()

	if err := a.cfg.Prepare(out); err != nil {
		return nil, fmt.Errorf("sip/ua: prepare %s: %w", req.Method, err)
	}

	return a.layer.Request(out, h)
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
		if !containsFold(a.supported, tag) && !containsFold(a.cfg.Understood, tag) && !containsFold(out, tag) {
			out = append(out, tag)
		}
	}

	return out
}

// HandleRequest implements transaction.Handler.
func (a *Agent) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
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
		a.inDialog(tx, req)
		return
	}

	if a.isClosed() {
		a.reply(tx, 503)
		return
	}

	if a.merged(tx, req) {
		a.reply(tx, 482)
		return
	}

	st := a.strandOf(req)
	if st == nil && a.cfg.StrandFor != nil {
		st = a.cfg.StrandFor(req)
	}

	switch {
	case st != nil:
	case req.Method == "PUBLISH":
		// RFC 3903 §6: the PUBLISHes of a Request-URI in order.
		st = a.keyedStrand(resourceOf(req.URI))
	default:
		st = a.NewStrand()
	}

	if req.Method == "INVITE" {
		st.post(func() { a.invite(st, tx, req) })
		return
	}

	r := newRequest(nil, tx, req)
	r.a, r.st = a, st

	switch {
	case a.cfg.OnRequest != nil && a.allows(req.Method):
		st.Post(func() { a.cfg.OnRequest(r) })
	case req.Method == "OPTIONS":
		_ = r.respond(Reply{Code: 200, Header: a.capabilities()})
	default:
		_ = r.respond(Reply{Code: 405, Header: sip.Header{{Name: "Allow", Value: a.allowValue()}}})
	}
}

func (a *Agent) allows(method string) bool {
	return slices.Contains(a.allow, method)
}

// Capabilities are the header fields of a 200 to an OPTIONS (RFC 3261 §11.2): Allow, Supported and Accept, for an
// application answering OPTIONS itself.
// Stats are the sizes of the tables of an agent: for tests to check that nothing is left once its usages end, and for
// metrics.
type Stats struct {
	// Dialogs is the number of dialogs with usages; Lingering, of those kept without, for 64*T1 after their last
	// usage ended.
	Dialogs, Lingering int

	// Invites is the number of INVITEs of peers awaiting their final response; Initials, of initial requests whose
	// transaction is in progress (RFC 3261 §8.2.2.2).
	Invites, Initials int

	// Outgoing is the number of INVITEs of the agent not ended; Subscribing, of its initial SUBSCRIBEs whose NOTIFYs
	// may still create subscriptions; Requests, of its requests outside dialogs in progress; Publications, of its
	// publications.
	Outgoing, Subscribing, Requests, Publications int
}

// Stats are the sizes of the tables of the agent.
func (a *Agent) Stats() Stats {
	a.mu.Lock()
	defer a.mu.Unlock()

	st := Stats{
		Invites:      len(a.invites),
		Initials:     len(a.initials),
		Outgoing:     len(a.outgoing),
		Subscribing:  len(a.subscribing),
		Requests:     len(a.requests),
		Publications: len(a.pubs),
	}

	for _, e := range a.dialogs {
		if e.ended {
			st.Lingering++
		} else {
			st.Dialogs++
		}
	}

	return st
}

func (a *Agent) Capabilities() sip.Header {
	return a.capabilities()
}

func (a *Agent) capabilities() sip.Header {
	return sip.Header{
		{Name: "Allow", Value: a.allowValue()},
		{Name: "Supported", Value: a.supportedValue()},
		{Name: "Accept", Value: "application/sdp"},
	}
}

// inDialog gives a request within a dialog to the usage it belongs to, on the strand of the dialog (RFC 5057 §5.3),
// or to the SUBSCRIBE whose NOTIFY creates the dialog (RFC 6665 §4.4.1).
func (a *Agent) inDialog(tx *transaction.ServerTransaction, req *sip.Request) {
	id := dialog.RequestID(req)

	a.mu.Lock()

	e := a.dialogs[id]
	ended := e != nil && e.ended

	var att *subscribing
	if e == nil && req.Method == "NOTIFY" {
		att = a.subscribing[subscribeKey{callID: id.CallID, fromTag: id.LocalTag}]
	}

	a.mu.Unlock()

	switch {
	case ended:
		a.reply(tx, 481)
	case e != nil:
		e.st.post(func() { a.route(id, tx, req) })
	case att != nil:
		att.st.post(func() { att.notifyFor(tx, req) })
	default:
		a.reply(tx, 481)
	}
}

// route gives a request within a dialog to its usage, with the dialog's strand held: INVITE, UPDATE, PRACK, BYE and
// INFO to the INVITE session; SUBSCRIBE, NOTIFY and REFER to the subscription of their Event; the requests of no
// usage, such as OPTIONS and MESSAGE, to the INVITE session if any (RFC 5057 §5.3).
func (a *Agent) route(id dialog.ID, tx *transaction.ServerTransaction, req *sip.Request) {
	s, subs, notifs := a.usages(id)
	ev, _, _ := req.Header.Event()

	switch req.Method {
	case "NOTIFY":
		for _, sub := range subs {
			if sub.matches(ev) {
				sub.receive(tx, req)
				return
			}
		}
	case "SUBSCRIBE":
		// A refresh or an unsubscribe of a subscription of the agent as notifier; a SUBSCRIBE for none of them is a
		// new usage, refused (RFC 5057 §5.6), and one for the refer event of no REFER gets a 403 (RFC 3515 §2.4.4).
		for _, n := range notifs {
			if n.event.Matches(ev) {
				n.receive(tx, req)
				return
			}
		}

		code := 603
		if ev.Type() == "refer" {
			code = 403
		}

		a.reply(tx, code)

		return
	}

	switch {
	case s != nil:
		s.receive(tx, req)
	case len(subs) == 0 && len(notifs) == 0:
		a.reply(tx, 481)
	case req.Method == "NOTIFY":
		// RFC 6665 §4.1.3: 489 for an event package of none of the subscriptions, 481 for another subscription.
		a.reply(tx, unmatchedNotify(req, subs))
	case req.Method == "REFER":
		// RFC 5057 §5.6: a new usage refused, the dialog unharmed.
		a.reply(tx, 603)
	case req.Method == "OPTIONS":
		a.reply(tx, 200, a.capabilities()...)
	case usageOf(req.Method) == "invite":
		a.reply(tx, 481)
	default:
		a.reply(tx, 405, sip.Field{Name: "Allow", Value: a.allowValue()})
	}
}

// unmatchedNotify is the response to a NOTIFY of none of the subscriptions of its dialog.
func unmatchedNotify(req *sip.Request, subs []*Subscription) int {
	ev, ok, err := req.Header.Event()
	if !ok || err != nil {
		return 400
	}

	for _, sub := range subs {
		if sub.event.Type() == ev.Type() {
			return 481
		}
	}

	return 489
}

// HandleCancel implements transaction.Handler: the transaction layer answered the CANCEL; the INVITE gets a 487
// (RFC 3261 §9.2).
func (a *Agent) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	if r := a.tracked(tx); r != nil {
		r.st.post(func() { r.cancelled(cancel) })
	}
}

// HandleAck implements transaction.Handler: the ACK of a 2xx (RFC 3261 §13.3.1.4).
func (a *Agent) HandleAck(ack *sip.Request) {
	if s := a.Session(dialog.RequestID(ack)); s != nil {
		s.st.post(func() { s.receiveAck(ack) })
	}
}

// HandleTransactionError implements transaction.Handler.
func (a *Agent) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	if r := a.tracked(tx); r != nil {
		r.st.post(func() { r.failed(err) })
	}
}

func containsFold(list []string, v string) bool {
	return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, v) })
}
