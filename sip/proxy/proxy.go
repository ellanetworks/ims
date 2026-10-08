package proxy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	DefaultTimerC = 3*time.Minute + 30*time.Second

	minTimerC = 3*time.Minute + time.Second

	// MaxBreadth is the Max-Breadth given to requests without one, and the most a request may
	// carry (RFC 5393 §5.3.3).
	MaxBreadth = 60
)

var (
	ErrForwarded = errors.New("sip/proxy: request already forwarded")

	ErrAnswered = errors.New("sip/proxy: request already answered")
)

type Config struct {
	Layer  *transaction.Layer
	Logger *slog.Logger

	Supported []string

	Port uint16

	LocalPorts []uint16

	TimerC time.Duration

	Clock transaction.Clock

	DialogLifetime time.Duration

	OnDialog func(DialogEvent)
}

type Proxy struct {
	layer      *transaction.Layer
	log        *slog.Logger
	supported  []string
	port       uint16
	localPorts []uint16
	timerC     time.Duration
	clock      transaction.Clock
	secret     string

	dialogLifetime time.Duration
	lingerBye      time.Duration
	onDialog       func(DialogEvent)

	mu       sync.Mutex
	contexts map[*transaction.ServerTransaction]*responseContext
	dialogs  map[string]*Dialog
}

type Target struct {
	Flow sip.Flow

	SentBy netip.AddrPort
}

type RecordRoute struct {
	User   string
	Params sip.Params

	Upstream netip.AddrPort

	Double         bool
	UpstreamParams sip.Params

	DownstreamParams sip.Params
}

type Options struct {
	RecordRoute *RecordRoute

	Timeout time.Duration

	NoAnswer time.Duration

	OnReply func(r Reply) Verdict

	Dialog *Dialog
}

// Branch is one target of a request (RFC 3261 §16.5): the request to send it, already retargeted,
// and where to send it.
type Branch struct {
	Request *sip.Request
	Target  Target
	Options Options

	// Retry holds branches to the same UA instance over its other flows, tried in turn in place
	// of this one while it fails with 430 (Flow Failed) (RFC 5626 §7).
	Retry []Branch

	// Key identifies what the branch reaches, such as a registered contact, so that the callee
	// legs of its dialog can be released by key (Dialog.ReleaseCallee). It must be comparable.
	Key any
}

type Reply struct {
	Response *sip.Response

	Err error

	Responded bool
}

type Verdict int

const (
	Relay Verdict = iota
	Hold
)

func New(cfg Config) *Proxy {
	if cfg.Layer == nil {
		panic("sip/proxy: nil Layer")
	}

	p := &Proxy{
		layer:      cfg.Layer,
		log:        cfg.Logger,
		supported:  cfg.Supported,
		port:       cfg.Port,
		localPorts: slices.Clone(cfg.LocalPorts),
		timerC:     cfg.TimerC,
		clock:      cfg.Clock,
		secret:     rand.Text(),
		contexts:   make(map[*transaction.ServerTransaction]*responseContext),
		dialogs:    make(map[string]*Dialog),

		dialogLifetime: cfg.DialogLifetime,
		lingerBye:      64 * cfg.Layer.T1(),
		onDialog:       cfg.OnDialog,
	}

	if p.dialogLifetime <= 0 {
		p.dialogLifetime = DefaultDialogLifetime
	}

	if p.log == nil {
		p.log = slog.Default()
	}

	switch {
	case p.timerC <= 0:
		p.timerC = DefaultTimerC
	case p.timerC < minTimerC:
		p.timerC = minTimerC
	}

	if p.clock == nil {
		p.clock = transaction.SystemClock{}
	}

	return p
}

func (p *Proxy) Check(req *sip.Request) *sip.Response {
	if req.Method == "ACK" {
		return nil
	}

	if mf, err := req.Header.MaxForwards(); err == nil && mf == 0 {
		return sip.NewResponse(req, 483, "")
	}

	var unsupported []string

	for _, tag := range req.Header.Elements("Proxy-Require") {
		if !slices.ContainsFunc(p.supported, func(s string) bool { return strings.EqualFold(s, tag) }) {
			unsupported = append(unsupported, tag)
		}
	}

	if len(unsupported) > 0 {
		res := sip.NewResponse(req, 420, "")
		res.Header.Insert("Unsupported", strings.Join(unsupported, ", "))

		return res
	}

	if _, err := req.Header.Routes(); err != nil {
		return sip.NewResponse(req, 400, "Bad Route")
	}

	return nil
}

func (p *Proxy) IsLocal(u sip.URI) bool {
	if !strings.EqualFold(u.Scheme, "sip") || u.Params.Has("gr") {
		return false
	}

	port := u.Port
	if port == 0 {
		port = sip.DefaultPort
	}

	return (p.port == 0 || port == p.port || slices.Contains(p.localPorts, port)) && p.layer.IsLocal(u.Host, port)
}

func (p *Proxy) Preprocess(req *sip.Request) (*sip.Request, []sip.URI, error) {
	routes, err := req.Header.Routes()
	if err != nil {
		return nil, nil, fmt.Errorf("sip/proxy: %w", err)
	}

	out := req.Clone()

	var removed []sip.URI

	if to, err := out.Header.To(); err == nil && to.Tag() != "" && len(routes) > 0 && p.IsLocal(out.URI) {
		removed = append(removed, out.URI)
		out.URI = routes[len(routes)-1].URI
		out.Header.PopLast("Route")

		routes = routes[:len(routes)-1]
	}

	if len(routes) == 0 || !p.IsLocal(routes[0].URI) {
		return out, removed, nil
	}

	out.Header.PopFirst("Route")

	removed = append(removed, routes[0].URI)

	if routes[0].URI.Params.Has("r2") && len(routes) > 1 && routes[1].URI.Params.Has("r2") && p.IsLocal(routes[1].URI) {
		out.Header.PopFirst("Route")

		removed = append(removed, routes[1].URI)
	}

	return out, removed, nil
}

func (p *Proxy) Forward(tx *transaction.ServerTransaction, req *sip.Request, to Target, opts Options) error {
	return p.Fork(tx, [][]Branch{{{Request: req, Target: to, Options: opts}}})
}

// Fork forwards a request to several targets (RFC 3261 §16.6). The groups are tried one after
// another, each once every branch of the one before ended without a 2xx or 6xx; the branches of a
// group run in parallel. Responses come back as in RFC 3261 §16.7: provisional responses and 2xx
// responses to an INVITE are relayed at once, and the best final response once nothing is left to try.
//
// Every branch of a fork carries the same tracked Dialog, or none; on an initial INVITE, the
// Dialog follows each early dialog the branches create, and the one answered.
func (p *Proxy) Fork(tx *transaction.ServerTransaction, groups [][]Branch) error {
	total := 0

	var d *Dialog

	for _, g := range groups {
		if len(g) == 0 {
			return internal(errors.New("sip/proxy: an empty group of branches"))
		}

		for _, b := range g {
			if total == 0 {
				d = b.Options.Dialog
			}

			for _, r := range append([]Branch{b}, b.Retry...) {
				switch {
				case r.Request.Method == "ACK" || r.Request.Method == "CANCEL":
					return internal(fmt.Errorf("sip/proxy: Forward of a %s request", r.Request.Method))
				case r.Request.Method != b.Request.Method:
					return internal(errors.New("sip/proxy: a retry of another method"))
				case r.Options.Dialog != d:
					return internal(errors.New("sip/proxy: branches of a fork with different dialogs"))
				}
			}

			total++
		}
	}

	if d != nil && total > 1 && groups[0][0].Request.Method != "INVITE" {
		return internal(errors.New("sip/proxy: a tracked dialog on a fork of a request other than an INVITE"))
	}

	if total == 0 {
		return internal(errors.New("sip/proxy: no branch to fork to"))
	}

	in := tx.Request()

	loop := p.loopKey(in)
	if looped(in, loop) {
		return &sip.StatusError{StatusCode: 482, Err: errors.New("sip/proxy: loop detected")}
	}

	breadth, err := incomingBreadth(in)
	if err != nil {
		return err
	}

	prepared := make([][]*branch, 0, len(groups))

	c, fresh, err := p.context(tx)
	if err != nil {
		return err
	}

	for _, g := range groups {
		pg := make([]*branch, 0, len(g))

		for _, spec := range g {
			b, err := p.prepareBranch(c, in, spec, loop)
			if err != nil {
				if fresh {
					p.forget(c)
				}

				return err
			}

			pg = append(pg, b)
		}

		prepared = append(prepared, pg)
	}

	if err := c.open(prepared, breadth); err != nil {
		return err
	}

	if fresh {
		tx.OnTerminated(func() { p.forget(c) })
	}

	first := prepared[0][0]
	begun := first.dialog != nil && first.initial

	if begun {
		if err := d.begin(tx, c, first.out, first.rr); err != nil {
			c.mu.Lock()
			c.groups = nil
			c.mu.Unlock()

			if fresh {
				p.forget(c)
			}

			return err
		}
	}

	// Branches that cannot be sent answer with an error, like any other, once one branch is out: the owner and
	// the dialog then learn of every branch. Until then, a fork none of whose branches could be sent fails as a
	// whole.
	var failures []failure

	for {
		c.mu.Lock()
		picked := c.pickLocked()
		c.mu.Unlock()

		if len(picked) == 0 {
			break
		}

		started, failed := c.start(picked)
		failures = append(failures, failed...)

		if started > 0 {
			if begun {
				d.started()
			}

			for _, f := range failures {
				c.dispatch(f.b, Reply{Response: c.generate(statusCode(f.err)), Err: f.err})
			}

			return nil
		}
	}

	var firstErr error
	if len(failures) > 0 {
		firstErr = failures[0].err
	}

	c.mu.Lock()
	c.groups, c.waiting = nil, nil
	c.mu.Unlock()

	if begun {
		d.abandon()
	}

	if fresh {
		p.forget(c)
	}

	return firstErr
}

func (p *Proxy) prepareBranch(c *responseContext, in *sip.Request, spec Branch, loop string) (*branch, error) {
	opts := spec.Options

	out, err := p.prepare(spec.Request, spec.Target)
	if err != nil {
		return nil, err
	}

	setBranch(out, loop)

	d := opts.Dialog
	initial := false

	if d != nil {
		to, err := out.Header.To()
		if err != nil {
			return nil, &sip.StatusError{StatusCode: 400, Err: err}
		}

		initial = to.Tag() == "" && out.Method == "INVITE"

		switch {
		case initial && opts.RecordRoute == nil:
			return nil, internal(errors.New("sip/proxy: a tracked dialog without Record-Route"))
		case opts.RecordRoute != nil:
			rr := *opts.RecordRoute
			rr.Params = rr.Params.Clone()
			rr.Params.Set(dialogParam, d.id)
			opts.RecordRoute = &rr
		}
	}

	if opts.RecordRoute != nil {
		recordRoute(out, in.Flow, spec.Target, opts.RecordRoute)
	}

	b := &branch{
		c: c, onReply: opts.OnReply, timeout: opts.Timeout, noAnswer: opts.NoAnswer,
		out: out, to: spec.Target, rr: opts.RecordRoute,
		dialog: d, req: out, initial: initial, key: spec.Key,
	}

	for _, r := range spec.Retry {
		rb, err := p.prepareBranch(c, in, r, loop)
		if err != nil {
			return nil, err
		}

		b.retry = append(b.retry, rb)
	}

	return b, nil
}

// RFC 5393 §5.3.3
func incomingBreadth(req *sip.Request) (int, error) {
	if !req.Header.Has("Max-Breadth") {
		return MaxBreadth, nil
	}

	n, err := req.Header.MaxBreadth()
	if err != nil {
		return 0, &sip.StatusError{StatusCode: 400, Err: err}
	}

	return min(n, MaxBreadth), nil
}

// loopKey is the second part of the Via branch of the requests forwarded for req: it varies with
// what decides where the request goes, but not with the method (RFC 5393 §4.2.1). The secret keeps
// it to this proxy.
func (p *Proxy) loopKey(req *sip.Request) string {
	h := sha256.New()

	h.Write([]byte(p.secret))
	h.Write([]byte{0})
	h.Write([]byte(req.URI.String()))

	for _, r := range req.Header.Values("Route") {
		h.Write([]byte{0})
		h.Write([]byte(r))
	}

	return hex.EncodeToString(h.Sum(nil)[:8])
}

const loopSeparator = "."

func setBranch(out *sip.Request, loop string) {
	top, err := out.Header.TopVia()
	if err != nil {
		return
	}

	top.Params.Set("branch", sip.NewBranch()+loopSeparator+loop)
	_ = out.Header.SetTopVia(top)
}

// looped reports a request that already went through this proxy with nothing changed that
// decides where it goes: a loop, not a spiral (RFC 5393 §4.2.2).
func looped(req *sip.Request, loop string) bool {
	vias, err := req.Header.Vias()
	if err != nil {
		return false
	}

	for _, v := range vias {
		if _, key, ok := strings.Cut(v.Branch(), loopSeparator); ok && key == loop {
			return true
		}
	}

	return false
}

func (p *Proxy) Relay(tx *transaction.ServerTransaction, res *sip.Response) error {
	p.mu.Lock()
	c := p.contexts[tx]
	p.mu.Unlock()

	if c == nil {
		if tx.Request().Method == "INVITE" && res.IsSuccess() {
			return tx.Relay(res)
		}

		return ErrAnswered
	}

	return c.relay(res.Clone())
}

func (p *Proxy) Proxied(tx *transaction.ServerTransaction) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	_, ok := p.contexts[tx]

	return ok
}

func (p *Proxy) Cancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	var reason []sip.Field

	if cancel != nil {
		for _, v := range cancel.Header.Values("Reason") {
			reason = append(reason, sip.Field{Name: "Reason", Value: v})
		}
	}

	p.mu.Lock()

	c, ok := p.contexts[tx]
	if !ok {
		c = newContext(p, tx)
		c.final = true
		p.contexts[tx] = c
	}

	p.mu.Unlock()

	if ok {
		if d := c.dialog(); d != nil {
			d.cancelledByCaller()
		}

		c.cancel(reason)

		return
	}

	p.answer(tx, 487)
	p.forget(c)
}

func (p *Proxy) ForwardAck(ack *sip.Request, to Target, d *Dialog) error {
	out, err := p.prepareAck(ack, to, d)
	if err != nil {
		return err
	}

	return p.layer.Go(func(ctx context.Context) {
		if err := p.layer.SendAck(ctx, out); err != nil {
			p.log.Debug("forwarding an ACK failed", slog.String("request", out.StartLine()), slog.Any("error", err))
		}
	})
}

// SendAck forwards ack like ForwardAck, but returns once it is sent.
func (p *Proxy) SendAck(ctx context.Context, ack *sip.Request, to Target, d *Dialog) error {
	out, err := p.prepareAck(ack, to, d)
	if err != nil {
		return err
	}

	return p.layer.SendAck(ctx, out)
}

func (p *Proxy) prepareAck(ack *sip.Request, to Target, d *Dialog) (*sip.Request, error) {
	if ack.Method != "ACK" {
		return nil, fmt.Errorf("sip/proxy: ForwardAck of a %s request", ack.Method)
	}

	if d != nil {
		if err := d.ack(ack); err != nil {
			return nil, err
		}
	}

	via, err := ack.Header.TopVia()
	if err != nil {
		return nil, err
	}

	out, err := p.prepare(ack, to)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256([]byte(p.secret + via.Branch()))

	top, _ := out.Header.TopVia()
	top.Params.Set("branch", sip.MagicCookie+"-ack-"+hex.EncodeToString(sum[:12]))
	_ = out.Header.SetTopVia(top)

	return out, nil
}

func (p *Proxy) prepare(req *sip.Request, to Target) (*sip.Request, error) {
	out := req.Clone()

	if err := decrementMaxForwards(out); err != nil {
		return nil, err
	}

	// Fork checked the Max-Breadth of the request that formed the response context. One that is invalid here, on
	// an ACK or a request a dialog generates, is replaced rather than refused.
	breadth, err := incomingBreadth(out)
	if err != nil {
		breadth = MaxBreadth
	}

	out.Header.Set("Max-Breadth", strconv.Itoa(breadth))

	if err := sip.ApplyStrictRoute(out); err != nil {
		return nil, &sip.StatusError{StatusCode: 400, Err: err}
	}

	out.Header.Prepend("Via", sip.NewVia(to.Flow.Transport, to.sentBy()).String())
	out.Flow = to.Flow

	return out, nil
}

func (p *Proxy) context(tx *transaction.ServerTransaction) (*responseContext, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if c, ok := p.contexts[tx]; ok {
		return c, false, nil
	}

	if s := tx.State(); s != transaction.Trying && s != transaction.Proceeding {
		return nil, false, ErrAnswered
	}

	c := newContext(p, tx)
	p.contexts[tx] = c

	return c, true, nil
}

func (p *Proxy) forget(c *responseContext) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.contexts[c.tx] == c {
		delete(p.contexts, c.tx)
	}
}

func (p *Proxy) answer(tx *transaction.ServerTransaction, code int) {
	if err := tx.Respond(sip.NewResponse(tx.Request(), code, "")); err != nil {
		p.log.Debug("proxy response failed", slog.Int("code", code), slog.Any("error", err))
	}
}

func internal(err error) error {
	var serr *sip.StatusError
	if errors.As(err, &serr) {
		return err
	}

	return &sip.StatusError{StatusCode: 500, Err: err}
}

func (t Target) sentBy() netip.AddrPort {
	if t.SentBy.IsValid() {
		return t.SentBy
	}

	return t.Flow.Local
}

func decrementMaxForwards(r *sip.Request) error {
	if !r.Header.Has("Max-Forwards") {
		r.Header.Set("Max-Forwards", "70")
		return nil
	}

	mf, err := r.Header.MaxForwards()
	if err != nil {
		return &sip.StatusError{StatusCode: 400, Err: err}
	}

	if mf == 0 {
		return &sip.StatusError{StatusCode: 483, Err: errors.New("Max-Forwards 0")}
	}

	r.Header.Set("Max-Forwards", strconv.Itoa(mf-1))

	return nil
}

func recordRoute(r *sip.Request, in sip.Flow, to Target, rr *RecordRoute) {
	up := rr.Upstream
	if !up.IsValid() {
		up = in.Local
	}

	down := to.sentBy()

	if up == down && in.Transport == to.Flow.Transport && !rr.Double {
		r.Header.InsertTop(recordRouteField(rr, down, to.Flow.Transport, false, nil))
		return
	}

	r.Header.InsertTop(recordRouteField(rr, up, in.Transport, true, rr.UpstreamParams))
	r.Header.InsertTop(recordRouteField(rr, down, to.Flow.Transport, true, rr.DownstreamParams))
}

func recordRouteField(rr *RecordRoute, addr netip.AddrPort, tr sip.Transport, double bool, extra sip.Params) sip.Field {
	u := sip.URI{Scheme: "sip", User: rr.User, Host: sip.FormatHost(addr.Addr()), Port: addr.Port()}

	if tr != sip.UDP {
		u.Params.Set("transport", strings.ToLower(string(tr)))
	}

	u.Params.Set("lr", "")

	if double {
		u.Params.Set("r2", "on")
	}

	for _, p := range rr.Params {
		u.Params.Set(p.Name, p.Value)
	}

	for _, p := range extra {
		u.Params.Set(p.Name, p.Value)
	}

	return sip.Field{Name: "Record-Route", Value: "<" + u.String() + ">"}
}
