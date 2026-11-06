package ibcf

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/bgcf"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	home  = "ims.example.org"
	wait  = 5 * time.Second
	alice = "+15550001000"
	carol = "+33612345678"
)

var (
	imsAddr      = netip.MustParseAddrPort("10.0.0.1:5070")
	internalAddr = netip.MustParseAddrPort("10.0.0.1:5085")
	externalAddr = netip.MustParseAddrPort("203.0.113.1:5060")
	carrierAddr  = netip.MustParseAddrPort("198.51.100.1:5060")
	strangerAddr = netip.MustParseAddrPort("192.0.2.9:5060")
)

func init() {
	ua.CheckStrands(true)
}

type event struct {
	kind  string
	s     *ua.Session
	res   *sip.Response
	req   *ua.Request
	sdp   []byte
	offer *ua.Offer
	end   ua.End
	msg   sip.Message
}

type party struct {
	t      *testing.T
	net    *siptest.Net
	name   string
	a      *ua.Agent
	addr   netip.AddrPort
	tr     sip.Transport
	nat    map[netip.Addr]netip.Addr
	events chan event

	mu       sync.Mutex
	onInvite func(r *ua.Request) ua.Handler
	seen     []sip.Message
}

func (p *party) observe(m sip.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.seen = append(p.seen, m)

	return nil
}

func (p *party) received() []sip.Message {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.seen)
}

func newParty(t *testing.T, n *siptest.Net, name string, addr netip.AddrPort) *party {
	t.Helper()

	return newTracedParty(t, n, name, addr, nil)
}

func newTracedParty(t *testing.T, n *siptest.Net, name string, addr netip.AddrPort, tp trace.TracerProvider) *party {
	t.Helper()

	p := &party{t: t, net: n, name: name, addr: addr, tr: sip.UDP, events: make(chan event, 256)}

	tc := transaction.Config{Filter: p.observe}

	if tp != nil {
		roleAttrs := []attribute.KeyValue{tracing.RoleKey.String(name)}
		tc.TracerProvider = tp
		tc.TraceContext = func(sip.Flow) bool { return true }
		tc.SpanAttributes = func(*sip.Request, trace.SpanKind) []attribute.KeyValue { return roleAttrs }
	}

	a, err := ua.New(ua.Config{
		Transaction: n.Config(tc),
		Prepare:     p.prepare,
		Contact:     sip.Address{URI: sip.URI{Scheme: "sip", User: name, Host: addr.Addr().String(), Port: addr.Port()}},
		Policy:      ua.Policy{AnswerRefreshes: true, RetryGlare: true, AnswerTimeout: time.Hour},
		Allow:       append(slices.Clone(ua.Methods), "INFO"),
		OnInvite: func(_ context.Context, r *ua.Request) ua.Handler {
			p.mu.Lock()
			f := p.onInvite
			p.mu.Unlock()

			if f != nil {
				return f(r)
			}

			p.record(event{kind: "invite", req: r, msg: r.Message()})

			return p.handler()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Layer().Serve(addr); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		n.Await(func() { _ = a.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1)
	})

	p.a = a

	return p
}

func (p *party) prepare(_ context.Context, req *sip.Request) error {
	target := req.URI
	if r, err := req.Header.TopRoute(); err == nil {
		target = r.URI
	}

	addr, err := netip.ParseAddr(target.Host)
	if err != nil {
		return err
	}

	if a, ok := p.nat[addr]; ok {
		addr = a
	}

	port := target.Port
	if port == 0 {
		port = sip.DefaultPort
	}

	tr := p.tr
	if v, ok := target.Params.Get("transport"); ok {
		tr = sip.Transport(strings.ToUpper(v))
	}

	req.Flow = sip.Flow{Transport: tr, Local: p.addr, Remote: netip.AddrPortFrom(addr, port)}
	req.Header.Prepend("Via", sip.NewVia(tr, p.addr).String())

	return nil
}

func (p *party) record(e event) {
	select {
	case p.events <- e:
	default:
		p.t.Errorf("%s: event queue full, %s dropped", p.name, e.kind)
	}
}

func (p *party) handler() ua.Handler {
	return ua.Handler{
		Progress: func(_ context.Context, s *ua.Session, res *sip.Response) {
			p.record(event{kind: "progress", s: s, res: res, msg: res})
		},
		Established: func(_ context.Context, s *ua.Session, m sip.Message) {
			p.record(event{kind: "established", s: s, msg: m})
		},
		Answer: func(_ context.Context, s *ua.Session, sdp []byte) { p.record(event{kind: "answer", s: s, sdp: sdp}) },
		Offer: func(_ context.Context, s *ua.Session, o *ua.Offer) {
			p.record(event{kind: "offer", s: s, offer: o, sdp: o.SDP()})
		},
		Request: func(_ context.Context, s *ua.Session, r *ua.Request) {
			p.record(event{kind: "request", s: s, req: r, msg: r.Message()})
		},
		Bye: func(_ context.Context, s *ua.Session, r *ua.Request) {
			p.record(event{kind: "bye", s: s, req: r, msg: r.Message()})
			_ = r.Respond(ua.Reply{Code: 200})
		},
		Ended: func(_ context.Context, s *ua.Session, e ua.End) { p.record(event{kind: "ended", s: s, end: e}) },
		Failed: func(_ context.Context, _ *ua.Outgoing, res *sip.Response, _ error) {
			p.record(event{kind: "failed", res: res})
		},
	}
}

func (p *party) next(kind string) event {
	p.t.Helper()
	p.net.Quiesce()

	for {
		select {
		case e := <-p.events:
			if e.kind == kind {
				return e
			}

			if e.kind == "progress" && e.res != nil && e.res.StatusCode == 100 {
				continue
			}

			p.t.Fatalf("%s: %s event, want %s (%+v)", p.name, e.kind, kind, e)
		case <-time.After(wait):
			p.t.Fatalf("%s: no %s event", p.name, kind)
		}

		return event{}
	}
}

func (p *party) none() {
	p.t.Helper()
	p.net.Quiesce()

	select {
	case e := <-p.events:
		p.t.Fatalf("%s: unexpected %s event (%+v)", p.name, e.kind, e)
	default:
	}
}

func (p *party) invite(uri string, sdp string, header ...sip.Field) *ua.Outgoing {
	p.t.Helper()

	target, err := sip.ParseURI(uri)
	if err != nil {
		p.t.Fatal(err)
	}

	req := sip.NewRequest("INVITE", target)

	for _, f := range header {
		req.Header.Add(f.Name, f.Value)
	}

	if !req.Header.Has("From") {
		req.Header.Add("From", fmt.Sprintf("<sip:%s@%s>", p.name, p.addr.Addr()))
	}

	if !req.Header.Has("To") {
		req.Header.Add("To", "<"+uri+">")
	}

	if sdp != "" {
		req.SetBodies(ua.SDP([]byte(sdp)))
	}

	o, err := p.a.Invite(context.Background(), req, p.handler())
	if err != nil {
		p.t.Fatal(err)
	}

	return o
}

func f(name, value string) sip.Field { return sip.Field{Name: name, Value: value} }

func sdpOf(s string) []sip.BodyPart { return []sip.BodyPart{ua.SDP([]byte(s))} }

func hasSDP(m sip.Message, want string) bool {
	part, ok, _ := m.Env().Content().FindFunc(sip.BodyPart.IsSession)
	return ok && string(part.Body) == want
}

type scene struct {
	t       *testing.T
	net     *siptest.Net
	ibcf    *IBCF
	ims     *party
	carrier *party
	trunk   settings.Trunk
	mu      sync.Mutex
	st      settings.Settings
}

func newScene(t *testing.T, trusted bool) *scene {
	t.Helper()

	return newSceneWith(t, trusted, nil)
}

func newSceneWith(t *testing.T, trusted bool, configure func(*Config)) *scene {
	t.Helper()

	return newTracedScene(t, trusted, nil, configure)
}

func newTracedScene(t *testing.T, trusted bool, tp trace.TracerProvider, configure func(*Config)) *scene {
	t.Helper()

	n := siptest.NewNet(t)

	s := &scene{t: t, net: n}
	s.trunk = settings.Trunk{
		ID: "carrier", Name: "Carrier",
		Addresses:    []settings.TrunkAddress{{Host: carrierAddr.Addr().String(), Port: int(carrierAddr.Port()), Transport: carrierTransport(trusted)}},
		InboundCIDRs: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")},
		Domain:       "sip.carrier.example",
		Trusted:      trusted,
		Enabled:      true,
	}
	s.st = settings.Settings{Trunks: []settings.Trunk{s.trunk}, SIPRoutes: []settings.SIPRoute{{ID: "default", Trunks: []string{"carrier"}}}}

	s.ims = newTracedParty(t, n, "ims", imsAddr, tp)

	s.carrier = newTracedParty(t, n, "carrier", carrierAddr, tp)
	if trusted {
		s.carrier.tr = sip.TCP
	}

	cfg := Config{
		HomeDomain:  home,
		Internal:    []netip.AddrPort{internalAddr},
		External:    []netip.AddrPort{externalAddr},
		ICSCF:       []netip.AddrPort{imsAddr},
		Trust:       trust.New([]netip.Addr{imsAddr.Addr()}, nil),
		BGCF:        bgcf.New(s.settings, nil),
		Settings:    s.settings,
		Transaction: n.Config(transaction.Config{TracerProvider: tp}),
	}

	if configure != nil {
		configure(&cfg)
	}

	b, err := start(t.Context(), cfg, false)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		n.Await(func() { _ = b.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1)
	})

	s.ibcf = b

	return s
}

func (s *scene) settings() settings.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.st
}

func (s *scene) setTrunk(t settings.Trunk) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.st.Trunks = []settings.Trunk{t}
}

func (s *scene) call(sdp string, header ...sip.Field) *ua.Outgoing {
	s.t.Helper()

	base := []sip.Field{
		f("Route", "<sip:"+internalAddr.String()+";lr>"),
		f("From", "<sip:alice@"+home+">"),
		f("P-Asserted-Identity", "<sip:alice@"+home+">"),
		f("P-Asserted-Identity", `"Alice" <tel:`+alice+">"),
		f("P-Charging-Vector", "icid-value=icid-1;orig-ioi="+home),
	}

	return s.ims.invite("tel:"+carol, sdp, append(base, header...)...)
}

func (s *scene) dial(from *party, sdp string, header ...sip.Field) *ua.Outgoing {
	s.t.Helper()

	base := []sip.Field{f("From", `"Carol" <sip:`+carol+`@sip.carrier.example;user=phone>`)}

	return from.invite("sip:"+alice+"@"+externalAddr.String()+";user=phone", sdp, append(base, header...)...)
}

func values(m sip.Message, name string) string {
	return strings.Join(m.Env().Header.Values(name), ", ")
}

func (s *scene) settle(parties ...*party) {
	s.t.Helper()

	s.net.Advance(64*transaction.DefaultT1 + time.Second)
	s.net.Advance(64*transaction.DefaultT1 + time.Second)

	for _, p := range parties {
		if st := p.a.Stats(); st != (ua.Stats{}) {
			s.t.Errorf("%s: left %+v", p.name, st)
		}
	}

	if st := s.ibcf.agent.Stats(); st != (ua.Stats{}) {
		s.t.Errorf("ibcf: left %+v", st)
	}
}

func (p *party) all(kinds ...string) map[string]event {
	p.t.Helper()
	p.net.Quiesce()

	got := map[string]event{}

	for len(got) < len(kinds) {
		select {
		case e := <-p.events:
			if e.kind == "progress" && e.res != nil && e.res.StatusCode == 100 {
				continue
			}

			if !slices.Contains(kinds, e.kind) {
				p.t.Fatalf("%s: unexpected %s event, want %v", p.name, e.kind, kinds)
			}

			got[e.kind] = e
		case <-time.After(wait):
			p.t.Fatalf("%s: events %v, want %v", p.name, got, kinds)
		}
	}

	return got
}

func carrierTransport(trusted bool) settings.SIPTransport {
	if trusted {
		return settings.SIPTransportTCP
	}

	return settings.SIPTransportUDP
}

func (p *party) until(kind string) event {
	p.t.Helper()
	p.net.Quiesce()

	for {
		select {
		case e := <-p.events:
			if e.kind == kind {
				return e
			}
		case <-time.After(wait):
			p.t.Fatalf("%s: no %s event", p.name, kind)
			return event{}
		}
	}
}
