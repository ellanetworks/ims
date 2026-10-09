package proxy_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

const quiet = 50 * time.Millisecond

var (
	loopback   = netip.MustParseAddr("127.0.0.1")
	transports = []sip.Transport{sip.UDP, sip.TCP}
)

type router struct {
	t     *testing.T
	l     *transaction.Layer
	p     *proxy.Proxy
	local netip.AddrPort

	opts      proxy.Options
	onReply   func(tx *transaction.ServerTransaction, req *sip.Request, r proxy.Reply) proxy.Verdict
	sentBy    atomic.Pointer[netip.AddrPort]
	out       atomic.Pointer[netip.AddrPort]
	next      string
	held      chan *transaction.ServerTransaction
	cancelled chan struct{}

	track   bool
	dialogs chan *proxy.Dialog
	events  chan proxy.DialogEvent
}

type routerConfig struct {
	opts    proxy.Options
	onReply func(tx *transaction.ServerTransaction, req *sip.Request, r proxy.Reply) proxy.Verdict
	sentBy  netip.AddrPort
	next    string
	hold    bool
	clock   *siptest.Clock
	timerC  time.Duration

	track    bool
	lifetime time.Duration
}

var recordRoute = &proxy.RecordRoute{}

func newRouter(t *testing.T, addr netip.Addr, cfg routerConfig) *router {
	t.Helper()

	r := &router{t: t, opts: cfg.opts, onReply: cfg.onReply, next: cfg.next, cancelled: make(chan struct{}, 16), track: cfg.track}
	r.sentBy.Store(&cfg.sentBy)

	if cfg.hold {
		r.held = make(chan *transaction.ServerTransaction, 16)
	}

	tc := transaction.Config{Handler: r}
	pc := proxy.Config{Supported: []string{"sec-agree"}, TimerC: cfg.timerC, DialogLifetime: cfg.lifetime}

	if cfg.track {
		r.dialogs = make(chan *proxy.Dialog, 16)
		r.events = make(chan proxy.DialogEvent, 64)
		pc.OnDialog = func(e proxy.DialogEvent) { r.events <- e }
	}

	if cfg.clock != nil {
		tc.Clock, pc.Clock = cfg.clock, cfg.clock
	}

	r.l, _ = siptest.NewLayer(t, tc)
	pc.Layer = r.l
	r.p = proxy.New(pc)
	r.local = siptest.ListenLayer(t, r.l, addr)

	return r
}

func (r *router) uri() string {
	return "sip:" + r.local.String()
}

func (r *router) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	if res := r.p.Check(req); res != nil {
		_ = tx.Respond(res)
		return
	}

	if r.held != nil {
		r.held <- tx
		return
	}

	out, removed, err := r.p.Preprocess(req)
	if err != nil {
		r.t.Error(err)
		return
	}

	to, _ := out.Header.To()

	if to.Tag() == "" && r.next != "" {
		out.Header.Prepend("Route", "<"+r.next+">")
	}

	opts := r.opts
	if r.onReply != nil {
		opts.OnReply = func(rep proxy.Reply) proxy.Verdict { return r.onReply(tx, out, rep) }
	}

	if r.track {
		switch {
		case to.Tag() == "" && req.Method == "INVITE":
			opts.Dialog = r.p.NewDialog(proxy.DialogConfig{})
			r.dialogs <- opts.Dialog
		case to.Tag() != "":
			opts.Dialog = r.p.Dialog(removed)
		}
	}

	local := req.Flow.Local
	if o := r.out.Load(); o != nil {
		local = *o
	}

	err = r.p.Forward(tx, out, r.target(out, local), opts)

	var serr *sip.StatusError

	switch {
	case err == nil:
	case r.track && errors.As(err, &serr):
		_ = tx.Respond(sip.NewResponse(req, serr.StatusCode, ""))
	default:
		r.t.Error(err)
	}
}

func (r *router) preprocess(req *sip.Request) *sip.Request {
	out, _, err := r.p.Preprocess(req)
	if err != nil {
		r.t.Error(err)
		return nil
	}

	return out
}

func (r *router) target(out *sip.Request, local netip.AddrPort) proxy.Target {
	tr, to, err := sip.NextHop(out)
	if err != nil {
		r.t.Error(err)
	}

	return proxy.Target{Flow: sip.Flow{Transport: tr, Local: local, Remote: to}, SentBy: *r.sentBy.Load()}
}

func (r *router) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	r.p.Cancel(tx, cancel)

	r.cancelled <- struct{}{}
}

func (r *router) HandleAck(ack *sip.Request) {
	out, removed, err := r.p.Preprocess(ack)
	if err != nil {
		r.t.Error(err)
		return
	}

	var d *proxy.Dialog
	if r.track {
		d = r.p.Dialog(removed)
	}

	if err := r.p.ForwardAck(out, r.target(out, ack.Flow.Local), d); err != nil && !errors.Is(err, proxy.ErrDialogEnded) {
		r.t.Error(err)
	}
}

func (r *router) nextDialog() *proxy.Dialog {
	r.t.Helper()

	select {
	case d := <-r.dialogs:
		return d
	case <-time.After(siptest.Timeout):
		r.t.Fatal("no dialog started")
		return nil
	}
}

func (r *router) nextEvent(kind proxy.EventKind) proxy.DialogEvent {
	r.t.Helper()

	for {
		select {
		case e := <-r.events:
			if e.Kind == proxy.EventNegotiated && kind != proxy.EventNegotiated {
				continue
			}

			if e.Kind != kind {
				r.t.Fatalf("got dialog event %+v, want kind %d", e, kind)
			}

			return e
		case <-time.After(siptest.Timeout):
			r.t.Fatalf("no dialog event of kind %d", kind)
			return proxy.DialogEvent{}
		}
	}
}

func (r *router) noEvent() {
	r.t.Helper()

	for {
		select {
		case e := <-r.events:
			if e.Kind != proxy.EventNegotiated {
				r.t.Fatalf("unexpected dialog event %+v", e)
			}
		case <-time.After(quiet):
			return
		}
	}
}

func (r *router) HandleTransactionError(_ *transaction.ServerTransaction, err error) {
	if !errors.Is(err, transaction.ErrClosed) && !errors.Is(err, transaction.ErrTimeout) {
		r.t.Error(err)
	}
}

func wantResponse(t *testing.T, s *siptest.Socket, code int) *sip.Response {
	t.Helper()

	res, _ := s.RecvResponse()
	if res.StatusCode != code {
		t.Fatalf("got %q, want %d", res.StartLine(), code)
	}

	return res
}

func reply(t *testing.T, s *siptest.Socket, req *sip.Request, f sip.Flow, code int, extra ...string) {
	t.Helper()

	res := sip.NewResponse(req, code, "")
	if code > 100 {
		_ = res.Header.SetToTag("callee")
	}

	for i := 0; i+1 < len(extra); i += 2 {
		res.Header.Add(extra[i], extra[i+1])
	}

	s.Send(f.Transport, f.Remote, res)
}

func uriParam(tr sip.Transport) string {
	if tr == sip.TCP {
		return ";transport=tcp"
	}

	return ""
}

func target(s *siptest.Socket, tr sip.Transport) string {
	return "sip:bob@" + s.Addr().String() + uriParam(tr)
}

func viaCount(m sip.Message) int {
	vias, _ := m.Env().Header.Vias()
	return len(vias)
}

func forEachFamily(t *testing.T, f func(t *testing.T, addr netip.Addr)) {
	for _, addr := range []netip.Addr{loopback, netip.MustParseAddr("::1")} {
		t.Run(map[bool]string{true: "IPv4", false: "IPv6"}[addr.Is4()], func(t *testing.T) {
			if addr.Is6() {
				var lc net.ListenConfig

				l, err := lc.ListenPacket(context.Background(), "udp6", "[::1]:0")
				if err != nil {
					t.Skip("no IPv6 loopback")
				}

				_ = l.Close()
			}

			f(t, addr)
		})
	}
}

func forEachTransport(t *testing.T, f func(t *testing.T, tr sip.Transport)) {
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) { f(t, tr) })
	}
}
