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

	// DialogLifetime bounds a dialog without a session timer;
	// DefaultDialogLifetime when zero.
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
}

type Options struct {
	RecordRoute *RecordRoute

	Timeout time.Duration

	OnReply func(r Reply) Verdict

	// Dialog tracks the INVITE dialog the request starts or belongs to. Its id
	// goes in the Record-Route, which an initial INVITE must have.
	Dialog *Dialog
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
	if req.Method == "ACK" || req.Method == "CANCEL" {
		return internal(fmt.Errorf("sip/proxy: Forward of a %s request", req.Method))
	}

	out, err := p.prepare(req, to)
	if err != nil {
		return err
	}

	d := opts.Dialog
	initial := false

	if d != nil {
		toTag, err := out.Header.To()
		if err != nil {
			return &sip.StatusError{StatusCode: 400, Err: err}
		}

		initial = toTag.Tag() == "" && out.Method == "INVITE"

		switch {
		case initial && opts.RecordRoute == nil:
			return internal(errors.New("sip/proxy: a tracked dialog without Record-Route"))
		case opts.RecordRoute != nil:
			rr := *opts.RecordRoute
			rr.Params = rr.Params.Clone()
			rr.Params.Set(dialogParam, d.id)
			opts.RecordRoute = &rr
		}
	}

	if opts.RecordRoute != nil {
		recordRoute(out, tx.Request().Flow, to, opts.RecordRoute)
	}

	c, fresh, err := p.context(tx)
	if err != nil {
		return err
	}

	if fresh {
		tx.OnTerminated(func() { p.forget(c) })
	}

	b, err := c.open(opts)
	if err != nil {
		return err
	}

	if d != nil {
		tracked := initial

		if initial {
			err = d.begin(tx, c, out, to, opts.RecordRoute)
		} else {
			tracked, err = d.request(out)
		}

		if err != nil {
			c.abandon(b, fresh)
			return err
		}

		if tracked {
			c.mu.Lock()
			b.dialog, b.req, b.initial = d, out, initial
			c.mu.Unlock()
		}
	}

	client, err := p.layer.Request(out, b)
	if err != nil {
		c.abandon(b, fresh)

		switch {
		case initial:
			d.abandon()
		case b.dialog != nil:
			// The request never left: undo what it changed, as a failure
			// response would.
			d.response(out, false, Reply{Response: sip.NewResponse(out, 500, ""), Err: err})
		}

		return internal(err)
	}

	if initial {
		d.started()
	}

	c.started(b, client)

	return nil
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

// Proxied reports whether the proxy forwarded the request of tx and has not
// answered it yet. A role that forwards an INVITE before HandleRequest returns
// can route a CANCEL by it; one that forwards later calls Cancel whatever it
// reports, which also handles a CANCEL that comes first.
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

// ForwardAck relays an ACK to a 2xx. With a dialog, the ACK confirms it, and
// is dropped when the proxy released the dialog.
func (p *Proxy) ForwardAck(ack *sip.Request, to Target, d *Dialog) error {
	if ack.Method != "ACK" {
		return fmt.Errorf("sip/proxy: ForwardAck of a %s request", ack.Method)
	}

	if d != nil {
		if err := d.ack(ack); err != nil {
			return err
		}
	}

	via, err := ack.Header.TopVia()
	if err != nil {
		return err
	}

	out, err := p.prepare(ack, to)
	if err != nil {
		return err
	}

	sum := sha256.Sum256([]byte(p.secret + via.Branch()))

	top, _ := out.Header.TopVia()
	top.Params.Set("branch", sip.MagicCookie+"-ack-"+hex.EncodeToString(sum[:12]))
	_ = out.Header.SetTopVia(top)

	return p.layer.Go(func(ctx context.Context) {
		if err := p.layer.SendAck(ctx, out); err != nil {
			p.log.Debug("forwarding an ACK failed", slog.String("request", out.StartLine()), slog.Any("error", err))
		}
	})
}

func (p *Proxy) prepare(req *sip.Request, to Target) (*sip.Request, error) {
	out := req.Clone()

	if err := decrementMaxForwards(out); err != nil {
		return nil, err
	}

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
	r.Header.InsertTop(recordRouteField(rr, down, to.Flow.Transport, true, nil))
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
