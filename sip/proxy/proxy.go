package proxy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

const DefaultTimerC = 3*time.Minute + 30*time.Second

var (
	ErrForwarded = errors.New("sip/proxy: request already forwarded")

	ErrAnswered = errors.New("sip/proxy: request already answered")
)

type Config struct {
	Layer  *transaction.Layer
	Logger *slog.Logger

	Supported []string

	TimerC time.Duration

	Clock transaction.Clock
}

type Proxy struct {
	layer     *transaction.Layer
	log       *slog.Logger
	supported []string
	timerC    time.Duration
	clock     transaction.Clock
	secret    string

	mu       sync.Mutex
	contexts map[*transaction.ServerTransaction]*responseContext
}

func New(cfg Config) *Proxy {
	if cfg.Layer == nil {
		panic("sip/proxy: nil Layer")
	}

	p := &Proxy{
		layer:     cfg.Layer,
		log:       cfg.Logger,
		supported: cfg.Supported,
		timerC:    cfg.TimerC,
		clock:     cfg.Clock,
		secret:    rand.Text(),
		contexts:  make(map[*transaction.ServerTransaction]*responseContext),
	}

	if p.log == nil {
		p.log = slog.Default()
	}

	if p.timerC <= 0 {
		p.timerC = DefaultTimerC
	}

	if p.clock == nil {
		p.clock = realClock{}
	}

	return p
}

func (p *Proxy) Check(req *sip.Request) *sip.Response {
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
		res.Header = slices.Insert(res.Header, len(res.Header)-1, sip.Field{Name: "Unsupported", Value: strings.Join(unsupported, ", ")})

		return res
	}

	if _, err := req.Header.Routes(); err != nil {
		return sip.NewResponse(req, 400, "Bad Route")
	}

	return nil
}

func (p *Proxy) IsLocal(u sip.URI) bool {
	return u.IsSIP() && !u.Params.Has("gr") && p.layer.IsLocal(u.Host, u.Port)
}

func (p *Proxy) Preprocess(req *sip.Request) ([]sip.URI, error) {
	routes, err := req.Header.Routes()
	if err != nil {
		return nil, fmt.Errorf("sip/proxy: %w", err)
	}

	var removed []sip.URI

	if to, err := req.Header.To(); err == nil && to.Tag() != "" && len(routes) > 0 && p.IsLocal(req.URI) {
		removed = append(removed, req.URI)
		req.URI = routes[len(routes)-1].URI
		popLast(&req.Header, "Route")

		routes = routes[:len(routes)-1]
	}

	if len(routes) == 0 || !p.IsLocal(routes[0].URI) {
		return removed, nil
	}

	req.Header.PopFirst("Route")

	removed = append(removed, routes[0].URI)

	if routes[0].URI.Params.Has("r2") && len(routes) > 1 && routes[1].URI.Params.Has("r2") && p.IsLocal(routes[1].URI) {
		req.Header.PopFirst("Route")

		removed = append(removed, routes[1].URI)
	}

	return removed, nil
}

type Options struct {
	Flow sip.Flow

	RecordRoute bool

	Response func(res *sip.Response)
}

func (p *Proxy) Forward(tx *transaction.ServerTransaction, req *sip.Request, opts Options) error {
	if req.Method == "ACK" || req.Method == "CANCEL" {
		return fmt.Errorf("sip/proxy: Forward of a %s request", req.Method)
	}

	out := req.Clone()

	if err := decrementMaxForwards(out); err != nil {
		return err
	}

	if opts.RecordRoute {
		recordRoute(out, tx.Request().Flow, opts.Flow)
	}

	if err := strictNextHop(out); err != nil {
		return err
	}

	out.Header.Prepend("Via", sip.NewVia(opts.Flow.Transport, opts.Flow.Local).String())
	out.Flow = opts.Flow

	c := &responseContext{p: p, stx: tx, invite: out.Method == "INVITE", hook: opts.Response}

	p.mu.Lock()

	cur, forwarded := p.contexts[tx]

	if s := tx.State(); (forwarded && cur == nil) || (s != transaction.Trying && s != transaction.Proceeding) {
		p.mu.Unlock()
		return ErrAnswered
	}

	if forwarded {
		p.mu.Unlock()
		return ErrForwarded
	}

	p.contexts[tx] = c
	p.mu.Unlock()

	client, err := p.layer.Request(out, c)
	if err != nil {
		p.forget(tx, c)
		return err
	}

	c.mu.Lock()

	c.client = client

	if c.invite && !c.final {
		c.timer = p.clock.AfterFunc(p.timerC, c.timerC)
	}

	cancel := c.cancelled && !c.final

	c.mu.Unlock()

	if cancel {
		_ = client.Cancel()
	}

	return nil
}

func (p *Proxy) Cancel(tx *transaction.ServerTransaction) {
	p.mu.Lock()

	c, ok := p.contexts[tx]
	if !ok {
		p.contexts[tx] = nil
	}

	p.mu.Unlock()

	switch {
	case c != nil:
		c.cancel()
	case !ok:
		go func() {
			<-tx.Done()
			p.forget(tx, nil)
		}()

		if err := tx.Respond(sip.NewResponse(tx.Request(), 487, "")); err != nil {
			p.log.Debug("487 to a cancelled request failed", slog.Any("error", err))
		}
	}
}

func (p *Proxy) ForwardAck(ctx context.Context, ack *sip.Request, flow sip.Flow) error {
	if ack.Method != "ACK" {
		return fmt.Errorf("sip/proxy: ForwardAck of a %s request", ack.Method)
	}

	via, err := ack.Header.TopVia()
	if err != nil {
		return err
	}

	out := ack.Clone()

	if err := decrementMaxForwards(out); err != nil {
		return err
	}

	if err := strictNextHop(out); err != nil {
		return err
	}

	v := sip.NewVia(flow.Transport, flow.Local)
	sum := sha256.Sum256([]byte(p.secret + via.Branch()))
	v.Params.Set("branch", sip.MagicCookie+"-ack-"+hex.EncodeToString(sum[:12]))

	out.Header.Prepend("Via", v.String())
	out.Flow = flow

	return p.layer.SendAck(ctx, out)
}

func (p *Proxy) relayStateless(tx *transaction.ServerTransaction, res *sip.Response) {
	res.Flow = tx.Request().Flow

	go func() {
		if err := p.layer.SendResponse(context.Background(), res); err != nil {
			p.log.Debug("stateless relay failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}
	}()
}

func (p *Proxy) forget(tx *transaction.ServerTransaction, c *responseContext) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if cur, ok := p.contexts[tx]; ok && cur == c {
		delete(p.contexts, tx)
	}
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

func recordRoute(r *sip.Request, in, out sip.Flow) {
	if in.Local == out.Local && in.Transport == out.Transport {
		insertTop(&r.Header, "Record-Route", recordRouteValue(out, false))
		return
	}

	insertTop(&r.Header, "Record-Route", recordRouteValue(in, true))
	insertTop(&r.Header, "Record-Route", recordRouteValue(out, true))
}

func recordRouteValue(f sip.Flow, double bool) string {
	u := sip.URI{Scheme: "sip", Host: sip.FormatHost(f.Local.Addr()), Port: f.Local.Port()}

	if f.Transport != sip.UDP {
		u.Params.Set("transport", strings.ToLower(string(f.Transport)))
	}

	u.Params.Set("lr", "")

	if double {
		u.Params.Set("r2", "on")
	}

	return "<" + u.String() + ">"
}

func insertTop(h *sip.Header, name, value string) {
	i := slices.IndexFunc(*h, func(f sip.Field) bool { return strings.EqualFold(sip.LongName(f.Name), name) })
	if i < 0 {
		i = 0

		for j, f := range *h {
			if strings.EqualFold(sip.LongName(f.Name), "Via") {
				i = j + 1
			}
		}
	}

	*h = slices.Insert(*h, i, sip.Field{Name: name, Value: value})
}

func strictNextHop(r *sip.Request) error {
	route, ok, err := r.Header.TopRoute()
	if err != nil {
		return &sip.StatusError{StatusCode: 400, Err: err}
	}

	if !ok || route.URI.IsLooseRouter() {
		return nil
	}

	addLast(&r.Header, "Route", "<"+r.URI.String()+">")
	r.Header.PopFirst("Route")

	r.URI = route.URI
	r.URI.Headers = ""
	r.URI.Params.Del("method")

	return nil
}

func addLast(h *sip.Header, name, value string) {
	for i := len(*h) - 1; i >= 0; i-- {
		if strings.EqualFold((*h)[i].Name, name) {
			*h = slices.Insert(*h, i+1, sip.Field{Name: name, Value: value})
			return
		}
	}

	h.Add(name, value)
}

func popLast(h *sip.Header, name string) {
	for i := len(*h) - 1; i >= 0; i-- {
		if !strings.EqualFold((*h)[i].Name, name) {
			continue
		}

		elems := sip.SplitList((*h)[i].Value)
		if len(elems) <= 1 {
			*h = slices.Delete(*h, i, i+1)
		} else {
			(*h)[i].Value = strings.Join(elems[:len(elems)-1], ", ")
		}

		return
	}
}

type realClock struct{}

func (realClock) AfterFunc(d time.Duration, f func()) transaction.Timer {
	return time.AfterFunc(d, f)
}
