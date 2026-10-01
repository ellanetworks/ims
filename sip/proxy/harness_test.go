package proxy_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
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

	recordRoute bool
	next        string
	hook        func(*sip.Response)
	held        chan *transaction.ServerTransaction
}

type routerConfig struct {
	recordRoute bool
	next        string
	hold        bool
	hook        func(*sip.Response)
	clock       *siptest.Clock
	timerC      time.Duration
}

func newRouter(t *testing.T, addr netip.Addr, cfg routerConfig) *router {
	t.Helper()

	r := &router{t: t, recordRoute: cfg.recordRoute, next: cfg.next, hook: cfg.hook}
	if cfg.hold {
		r.held = make(chan *transaction.ServerTransaction, 16)
	}

	tc := transaction.Config{Handler: r}
	pc := proxy.Config{Supported: []string{"sec-agree"}, TimerC: cfg.timerC}

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

	out := req.Clone()

	if _, err := r.p.Preprocess(out); err != nil {
		r.t.Error(err)
		return
	}

	if to, _ := out.Header.To(); to.Tag() == "" && r.next != "" {
		out.Header.Prepend("Route", "<"+r.next+">")
	}

	if err := r.p.Forward(tx, out, r.options(out, req.Flow.Local)); err != nil {
		r.t.Error(err)
	}
}

func (r *router) options(out *sip.Request, local netip.AddrPort) proxy.Options {
	tr, to, err := sip.NextHop(out)
	if err != nil {
		r.t.Error(err)
	}

	return proxy.Options{Flow: sip.Flow{Transport: tr, Local: local, Remote: to}, RecordRoute: r.recordRoute, Response: r.hook}
}

func (r *router) HandleCancel(tx *transaction.ServerTransaction, _ *sip.Request) {
	r.p.Cancel(tx)
}

func (r *router) HandleAck(ack *sip.Request) {
	out := ack.Clone()

	if _, err := r.p.Preprocess(out); err != nil {
		r.t.Error(err)
		return
	}

	if err := r.p.ForwardAck(context.Background(), out, r.options(out, ack.Flow.Local).Flow); err != nil {
		r.t.Error(err)
	}
}

func (r *router) HandleTransactionError(_ *transaction.ServerTransaction, err error) {
	if !errors.Is(err, transaction.ErrClosed) {
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
