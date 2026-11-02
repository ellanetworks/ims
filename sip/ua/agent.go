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
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	ErrClosed = errors.New("sip/ua: agent closed")

	ErrPending = errors.New("sip/ua: an offer or an INVITE transaction is pending")

	ErrState = errors.New("sip/ua: not allowed in the state of the session")

	ErrCancelled = errors.New("sip/ua: re-INVITE cancelled")

	ErrStale = errors.New("sip/ua: the session changed while the offer waited")

	ErrNoPrack = errors.New("sip/ua: no PRACK to the reliable 1xx")
	ErrNoAck   = errors.New("sip/ua: no ACK to the 2xx")

	ErrNoAnswer = errors.New("sip/ua: no answer to the offer of the 2xx")

	ErrIntervalTooBrief = errors.New("sip/ua: subscription interval too brief")
)

type Error struct {
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

// RFC 3261 section 20.5
var Methods = []string{"INVITE", "ACK", "CANCEL", "BYE", "PRACK", "UPDATE", "OPTIONS"}

type Extensions struct {
	Replaces bool

	TargetDialog bool

	NoReferSub bool

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

type Policy struct {
	AnswerRefreshes bool

	RetryGlare bool

	SkipRefreshes bool

	AnswerTimeout time.Duration
}

const defaultAnswerTimeout = 3 * time.Minute

func (p Policy) answerTimeout() time.Duration {
	if p.AnswerTimeout > 0 {
		return p.AnswerTimeout
	}

	return defaultAnswerTimeout
}

var ClientPolicy = Policy{AnswerRefreshes: true, RetryGlare: true}

// RFC 4028
type SessionTimer struct {
	Interval time.Duration

	MinSE time.Duration

	Refresher string
}

type Config struct {
	Layer *transaction.Layer

	Transaction transaction.Config

	Prepare func(req *sip.Request) error

	Respond func(req *sip.Request, res *sip.Response)

	Authenticate func(req *sip.Request, challenge *sip.Response) error

	Contact sip.Address

	Allow []string

	Supported []string

	Understood []string

	SessionTimer SessionTimer

	Extensions Extensions

	Policy Policy

	StrandFor func(req *sip.Request) *Strand

	OnInvite func(ctx context.Context, r *Request) Handler

	OnRequest func(ctx context.Context, r *Request)

	Logger *slog.Logger
}

type Handler struct {
	Progress func(ctx context.Context, s *Session, res *sip.Response)

	Established func(ctx context.Context, s *Session, m sip.Message)

	Answer func(ctx context.Context, s *Session, sdp []byte)

	Pracked func(ctx context.Context, s *Session, res *sip.Response)

	Offer func(ctx context.Context, s *Session, o *Offer)

	Request func(ctx context.Context, s *Session, r *Request)

	Bye func(ctx context.Context, s *Session, r *Request)

	Cancelled func(ctx context.Context, s *Session, r *Request, cancel *sip.Request)

	Decorate func(s *Session, m sip.Message)

	Forked func(ctx context.Context, s *Session, res *sip.Response) bool

	Received func(ctx context.Context, s *Session, m sip.Message)

	Ended func(ctx context.Context, s *Session, e End)

	Failed func(ctx context.Context, o *Outgoing, res *sip.Response, err error)
}

type Reply struct {
	Code   int
	Reason string

	Header sip.Header

	Bodies []sip.BodyPart

	Reliable bool

	AfterPrack bool
}

type Change struct {
	Header sip.Header
	Bodies []sip.BodyPart
}

// RFC 3326
func Reasons(rs ...sip.Reason) Change {
	var c Change

	for _, r := range rs {
		c.Header.Add("Reason", r.String())
	}

	return c
}

func (c Change) apply(req *sip.Request) {
	for _, f := range c.Header {
		req.Header.Add(f.Name, f.Value)
	}

	if len(c.Bodies) > 0 {
		req.SetBodies(c.Bodies...)
	}
}

func SDP(b []byte) sip.BodyPart {
	return sip.NewBodyPart("application/sdp", b)
}

// RFC 5057 section 4
type dialogEntry struct {
	st     *Strand
	invite *Session
	subs   []*Subscription
	origin trace.SpanContext

	notifs []*ServerSubscription

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

const tracerName = "github.com/ellanetworks/ims/sip/ua"

type Agent struct {
	cfg       Config
	layer     *transaction.Layer
	ownLayer  bool
	log       *slog.Logger
	clock     transaction.Clock
	sched     transaction.Scheduler
	tracer    trace.Tracer
	allow     []string
	supported []string

	wmu     sync.Mutex
	wcond   sync.Cond
	workers int
	closing int

	mu          sync.Mutex
	closed      bool
	dialogs     map[dialog.ID]*dialogEntry
	invites     map[*transaction.ServerTransaction]*Request
	arriving    map[*transaction.ServerTransaction]*Strand
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
		arriving:  map[*transaction.ServerTransaction]*Strand{},
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
		tcfg.SpanLinks = a.chainSpanLinks(tcfg.SpanLinks)
		a.layer = transaction.New(tcfg)
		a.ownLayer = true
	}

	a.clock = a.layer.Clock()
	a.sched = a.layer.Scheduler()
	a.tracer = a.layer.TracerProvider().Tracer(tracerName, trace.WithSchemaURL(semconv.SchemaURL))

	return a, nil
}

func (a *Agent) Layer() *transaction.Layer {
	return a.layer
}

func (a *Agent) working(delta int) {
	a.wmu.Lock()
	a.workers += delta
	a.wcond.Broadcast()
	a.wmu.Unlock()
}

func (a *Agent) await(delta int) {
	a.wmu.Lock()
	a.closing += delta
	a.wcond.Broadcast()
	a.wmu.Unlock()
}

func (a *Agent) waitUntil(ctx context.Context, cond func() bool) bool {
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

	return cond()
}

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

func (a *Agent) Shutdown(ctx context.Context) error {
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
			p.close(ctx)
			return nil
		})
	}

	for _, o := range outgoing {
		_ = o.st.do(func() error {
			o.awaitFinal()

			if err := o.cancelWith(ctx, Change{}); err != nil {
				a.log.Debug("CANCEL failed", callAttr(o.ci.req), slog.Any("error", err))
			}

			return nil
		})
	}

	for _, n := range notifs {
		_ = n.st.do(func() error {
			n.close(ctx)
			return nil
		})
	}

	for _, s := range sessions {
		_ = s.st.do(func() error {
			s.close(ctx)
			return nil
		})
	}

	for _, s := range subs {
		_ = s.st.do(func() error {
			if s.d != nil || s.att.first == s {
				s.close(ctx)
			}

			return nil
		})
	}

	done := a.waitUntil(ctx, func() bool { return a.closing == 0 })

	var err error

	if a.ownLayer {
		err = a.layer.Shutdown(ctx)
	}

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

	for _, s := range subs {
		_ = s.st.do(func() error {
			s.end(ctx, End{Cause: EndClosed})
			return nil
		})
	}

	for _, n := range notifs {
		_ = n.st.do(func() error {
			n.end(ctx, End{Cause: EndClosed})
			return nil
		})
	}

	for _, p := range pubs {
		_ = p.st.do(func() error {
			p.end(ctx, End{Cause: EndClosed})
			return nil
		})
	}

	for _, o := range outgoing {
		_ = o.st.do(func() error {
			o.closed(ctx)
			return nil
		})
	}

	for _, c := range requests {
		_ = c.st.do(func() error {
			c.finish(nil, ErrClosed)
			return nil
		})
	}

	done = a.waitUntil(ctx, func() bool { return a.workers == 0 }) && done

	if !done && !errors.Is(err, ctx.Err()) {
		err = errors.Join(ctx.Err(), err)
	}

	return err
}

// TS 24.229 clause 5.1.1.4.1
func (a *Agent) Drop(ctx context.Context, match func(sip.Flow) bool) {
	a.layer.Drop(match)

	type ending struct {
		st  *Strand
		end func()
	}

	var ends []ending

	a.mu.Lock()

	for o := range a.outgoing {
		ends = append(ends, ending{o.st, func() { o.dropped(ctx) }})
	}

	dropped := End{Cause: EndDropped}

	for _, e := range a.dialogs {
		if s := e.invite; s != nil {
			ends = append(ends, ending{s.st, func() { s.end(ctx, dropped) }})
		}

		for _, s := range e.subs {
			ends = append(ends, ending{s.st, func() { s.end(ctx, dropped) }})
		}

		for _, n := range e.notifs {
			ends = append(ends, ending{n.st, func() { n.end(ctx, dropped) }})
		}
	}

	for _, att := range a.subscribing {
		s := att.first
		ends = append(ends, ending{s.st, func() { s.end(ctx, dropped) }})
	}

	for p := range a.pubs {
		ends = append(ends, ending{p.st, func() { p.end(ctx, dropped) }})
	}

	for c := range a.requests {
		ends = append(ends, ending{c.st, func() { c.finish(nil, transaction.ErrDropped) }})
	}

	a.mu.Unlock()

	for _, e := range ends {
		e.st.post(e.end)
	}
}

func (a *Agent) allowValue() string {
	return strings.Join(a.allow, ", ")
}

func (a *Agent) supportedValue() string {
	return strings.Join(a.supported, ", ")
}

func (a *Agent) Session(id dialog.ID) *Session {
	a.mu.Lock()
	defer a.mu.Unlock()

	if e := a.dialogs[id]; e != nil {
		return e.invite
	}

	return nil
}

// RFC 4538 section 4
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

func (a *Agent) replaced(id dialog.ID) (e dialogEntry, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if p := a.dialogs[id]; p != nil {
		return *p, true
	}

	return dialogEntry{}, false
}

func (a *Agent) entry(id dialog.ID, st *Strand, origin trace.SpanContext) *dialogEntry {
	e := a.dialogs[id]
	if e == nil {
		e = &dialogEntry{st: st, origin: origin}
		a.dialogs[id] = e
	}

	if e.st != st {
		panic("sip/ua: usages of one dialog on two strands")
	}

	e.ended = false

	return e
}

func (a *Agent) addSession(s *Session, origin trace.SpanContext) {
	a.mu.Lock()
	defer a.mu.Unlock()

	e := a.entry(s.id, s.st, origin)
	e.invite, e.invited = s, true
}

func (a *Agent) addIncoming(s *Session, r *Request) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.arriving, r.tx)

	if a.closed {
		return false
	}

	a.invites[r.tx] = r

	e := a.entry(s.id, s.st, trace.SpanContextFromContext(r.ctx))
	e.invite, e.invited = s, true

	return true
}

// RFC 5057 section 4
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

	e := a.entry(s.d.ID(), s.st, s.att.origin)
	e.subs = append(e.subs, s)
}

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

func (a *Agent) addServerSubscription(s *ServerSubscription, origin trace.SpanContext) {
	a.mu.Lock()
	defer a.mu.Unlock()

	e := a.entry(s.d.ID(), s.st, origin)
	e.notifs = append(e.notifs, s)
}

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

// RFC 5057 section 5.1
func (a *Agent) destroyDialog(ctx context.Context, id dialog.ID, res *sip.Response, err error) {
	s, subs, notifs := a.usages(id)
	e := End{Cause: EndLost, Response: res, Err: err}

	for _, sub := range subs {
		sub.end(ctx, e)
	}

	for _, n := range notifs {
		n.end(ctx, e)
	}

	if s != nil {
		s.end(ctx, e)
	}
}

// RFC 5057 section 4
func (a *Agent) usages(id dialog.ID) (*Session, []*Subscription, []*ServerSubscription) {
	a.mu.Lock()
	defer a.mu.Unlock()

	e := a.dialogs[id]
	if e == nil {
		return nil, nil, nil
	}

	return e.invite, slices.Clone(e.subs), slices.Clone(e.notifs)
}

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

func (a *Agent) track(r *Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.invites[r.tx] = r
}

func (a *Agent) untrack(r *Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.invites, r.tx)
	delete(a.arriving, r.tx)
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

// RFC 3261 section 8.2.2.2
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
		a.log.Debug("response failed", callAttr(tx.Request()), slog.Int("code", code), slog.Any("error", err))
	}
}

func (a *Agent) prepareAndSend(ctx context.Context, req *sip.Request, h transaction.ClientHandler) (*transaction.ClientTransaction, error) {
	out := req.Clone()

	if err := a.cfg.Prepare(out); err != nil {
		return nil, fmt.Errorf("sip/ua: prepare %s: %w", req.Method, err)
	}

	return a.layer.Request(ctx, out, h)
}

// RFC 3261 section 22.2
func (a *Agent) authenticate(next *sip.Request, res *sip.Response, tries int) bool {
	if res.StatusCode != 401 && res.StatusCode != 407 || a.cfg.Authenticate == nil || tries >= 2 {
		return false
	}

	if err := a.cfg.Authenticate(next, res); err != nil {
		a.log.Debug("authentication failed", callAttr(next), slog.Any("error", err))
		return false
	}

	return true
}

// RFC 3261 section 8.2.2.3
func (a *Agent) unsupported(req *sip.Request) []string {
	var out []string

	for _, tag := range req.Header.Elements("Require") {
		if !containsFold(a.supported, tag) && !containsFold(a.cfg.Understood, tag) && !containsFold(out, tag) {
			out = append(out, tag)
		}
	}

	return out
}

func (a *Agent) HandleRequest(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
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
		a.inDialog(ctx, tx, req)
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
		st = a.keyedStrand(resourceOf(req.URI))
	default:
		st = a.NewStrand()
	}

	if req.Method == "INVITE" {
		a.mu.Lock()
		a.arriving[tx] = st
		a.mu.Unlock()

		st.post(func() { a.invite(ctx, st, tx, req) })

		return
	}

	r := newRequest(ctx, nil, tx, req)
	r.a, r.st = a, st

	switch {
	case a.cfg.OnRequest != nil && a.allows(req.Method):
		st.Post(func() { a.cfg.OnRequest(ctx, r) })
	case req.Method == "OPTIONS":
		_ = r.respond(Reply{Code: 200, Header: a.capabilities()})
	default:
		_ = r.respond(Reply{Code: 405, Header: sip.Header{{Name: "Allow", Value: a.allowValue()}}})
	}
}

func (a *Agent) allows(method string) bool {
	return slices.Contains(a.allow, method)
}

type Stats struct {
	Dialogs, Lingering int

	Invites, Initials int

	Outgoing, Subscribing, Requests, Publications int
}

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

// RFC 5057 section 5.3
// RFC 6665 section 4.4.1
func (a *Agent) inDialog(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
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
		e.st.post(func() { a.route(ctx, id, tx, req) })
	case att != nil:
		att.st.post(func() { att.notifyFor(ctx, tx, req) })
	default:
		a.reply(tx, 481)
	}
}

// RFC 5057 section 5.3
func (a *Agent) route(ctx context.Context, id dialog.ID, tx *transaction.ServerTransaction, req *sip.Request) {
	s, subs, notifs := a.usages(id)
	ev, _, _ := req.Header.Event()

	switch req.Method {
	case "NOTIFY":
		for _, sub := range subs {
			if sub.matches(ev) {
				sub.receive(ctx, tx, req)
				return
			}
		}
	case "SUBSCRIBE":
		for _, n := range notifs {
			if n.event.Matches(ev) {
				n.receive(ctx, tx, req)
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
		s.receive(ctx, tx, req)
	case len(subs) == 0 && len(notifs) == 0:
		a.reply(tx, 481)
	case req.Method == "NOTIFY":
		a.reply(tx, unmatchedNotify(req, subs))
	case req.Method == "REFER":
		a.reply(tx, 603)
	case req.Method == "OPTIONS":
		a.reply(tx, 200, a.capabilities()...)
	case usageOf(req.Method) == "invite":
		a.reply(tx, 481)
	default:
		a.reply(tx, 405, sip.Field{Name: "Allow", Value: a.allowValue()})
	}
}

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

// RFC 3261 section 9.2
func (a *Agent) HandleCancel(ctx context.Context, tx *transaction.ServerTransaction, cancel *sip.Request) {
	a.mu.Lock()
	r, st := a.invites[tx], a.arriving[tx]
	a.mu.Unlock()

	switch {
	case r != nil:
		r.st.post(func() { r.cancelled(ctx, cancel) })
	case st != nil:
		st.post(func() {
			if r := a.tracked(tx); r != nil {
				r.cancelled(ctx, cancel)
			}
		})
	}
}

// RFC 3261 section 13.3.1.4
func (a *Agent) HandleAck(ctx context.Context, ack *sip.Request) {
	if s := a.Session(dialog.RequestID(ack)); s != nil {
		s.st.post(func() { s.receiveAck(ctx, ack) })
	}
}

func (a *Agent) HandleTransactionError(ctx context.Context, tx *transaction.ServerTransaction, err error) {
	if r := a.tracked(tx); r != nil {
		r.st.post(func() { r.failed(ctx, err) })
	}
}

func (a *Agent) SpanLinks(req *sip.Request) []trace.SpanContext {
	id := dialog.RequestID(req)
	if id.LocalTag == "" {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if e := a.dialogs[id]; e != nil && e.origin.IsValid() {
		return []trace.SpanContext{e.origin}
	}

	return nil
}

func (a *Agent) chainSpanLinks(next func(*sip.Request) []trace.SpanContext) func(*sip.Request) []trace.SpanContext {
	if next == nil {
		return a.SpanLinks
	}

	return func(req *sip.Request) []trace.SpanContext {
		return append(next(req), a.SpanLinks(req)...)
	}
}

func (a *Agent) startDetached(causes []trace.SpanContext, name string) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindInternal)}

	for _, c := range causes {
		if c.IsValid() {
			opts = append(opts, trace.WithLinks(trace.Link{SpanContext: c}))
		}
	}

	return a.tracer.Start(a.layer.Lifetime(), name, opts...)
}

func containsFold(list []string, v string) bool {
	return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, v) })
}
