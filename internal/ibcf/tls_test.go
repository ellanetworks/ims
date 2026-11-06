package ibcf

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/bgcf"
	"github.com/ellanetworks/ims/internal/sbitls/sbitlstest"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
	"github.com/ellanetworks/ims/sip/ua"
)

type realParty struct {
	t      *testing.T
	a      *ua.Agent
	udp    netip.AddrPort
	tls    netip.AddrPort
	events chan event
}

func newRealParty(t *testing.T, ip string, serve *tls.Config, client func(sip.Flow) (transport.TLSOptions, error)) *realParty {
	t.Helper()

	p := &realParty{t: t, events: make(chan event, 64)}

	h := func() ua.Handler {
		return ua.Handler{
			Progress: func(_ context.Context, s *ua.Session, res *sip.Response) {
				if res.StatusCode > 100 {
					p.events <- event{kind: "progress", s: s, res: res}
				}
			},
			Established: func(_ context.Context, s *ua.Session, m sip.Message) {
				p.events <- event{kind: "established", s: s, msg: m}
			},
			Bye: func(_ context.Context, s *ua.Session, r *ua.Request) {
				p.events <- event{kind: "bye", s: s, msg: r.Message()}

				_ = r.Respond(ua.Reply{Code: 200})
			},
			Failed: func(_ context.Context, _ *ua.Outgoing, res *sip.Response, _ error) {
				p.events <- event{kind: "failed", res: res}
			},
		}
	}

	a, err := ua.New(ua.Config{
		Transaction: transaction.Config{Transport: transport.Config{TLSClientOptions: client}},
		Prepare:     p.prepare,
		Contact:     sip.Address{URI: sip.URI{Scheme: "sip", Host: ip}},
		Policy:      ua.ClientPolicy,
		OnInvite: func(_ context.Context, r *ua.Request) ua.Handler {
			p.events <- event{kind: "invite", req: r, msg: r.Message()}
			return h()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = a.Shutdown(ctx)
	})

	if p.udp, err = a.Layer().Listen(t.Context(), netip.MustParseAddrPort(ip+":0")); err != nil {
		t.Fatal(err)
	}

	if serve != nil {
		if p.tls, err = a.Layer().ListenTLS(t.Context(), netip.MustParseAddrPort(ip+":0"), serve); err != nil {
			t.Fatal(err)
		}
	}

	p.a = a

	return p
}

func (p *realParty) prepare(req *sip.Request) error {
	target := req.URI
	if r, err := req.Header.TopRoute(); err == nil {
		target = r.URI
	}

	tr, dest, err := sip.Destination(target)
	if err != nil {
		return err
	}

	local := p.udp
	if tr == sip.TLS {
		local = p.tls
	}

	req.Flow = sip.Flow{Transport: tr, Local: local, Remote: dest}
	req.Header.Prepend("Via", sip.NewVia(tr, local).String())

	return nil
}

func (p *realParty) next(kind string) event {
	p.t.Helper()

	deadline := time.After(10 * time.Second)

	for {
		select {
		case e := <-p.events:
			if e.kind == kind {
				return e
			}
		case <-deadline:
			p.t.Fatalf("no %s event", kind)
			return event{}
		}
	}
}

func (p *realParty) none(d time.Duration) {
	p.t.Helper()

	select {
	case e := <-p.events:
		p.t.Fatalf("unexpected %s event", e.kind)
	case <-time.After(d):
	}
}

type tlsScene struct {
	t     *testing.T
	ibcf  *IBCF
	ims   *realParty
	mu    sync.Mutex
	st    settings.Settings
	ca    *sbitlstest.CA
	ibcfC sbitlstest.Issued
}

func (s *tlsScene) settings() settings.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.st
}

func newTLSScene(t *testing.T, ca *sbitlstest.CA, certificate bool, trunk settings.Trunk) *tlsScene {
	t.Helper()

	s := &tlsScene{t: t, ca: ca}
	s.ibcfC = s.ca.Issue(t, "ibcf", sbitlstest.Leaf{Hosts: []string{"ibcf.example"}})
	s.ims = newRealParty(t, "127.0.0.1", nil, nil)
	s.st = settings.Settings{Trunks: []settings.Trunk{trunk}, SIPRoutes: []settings.SIPRoute{{ID: "default", Trunks: []string{trunk.ID}}}}

	cfg := Config{
		HomeDomain: home,
		Internal:   []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
		External:   []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
		TLS:        []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
		ICSCF:      []netip.AddrPort{s.ims.udp},
		Trust:      trust.New([]netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil),
		BGCF:       bgcf.New(s.settings, nil),
		Settings:   s.settings,
	}

	if certificate {
		cfg.Certificate = &s.ibcfC.Cert
	}

	b, err := Start(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = b.Shutdown(ctx)
	})

	s.ibcf = b

	return s
}

func (s *tlsScene) call() {
	s.t.Helper()

	internal := s.ibcf.Internal()[0]
	req := sip.NewRequest("INVITE", sip.URI{Scheme: "tel", User: carol})
	req.Header.Add("Route", "<sip:"+internal.String()+";lr>")
	req.Header.Add("From", "<sip:alice@"+home+">")
	req.Header.Add("To", "<tel:"+carol+">")
	req.Header.Add("P-Asserted-Identity", "<tel:"+alice+">")
	req.SetBodies(ua.SDP([]byte("offer-1")))

	if _, err := s.ims.a.Invite(context.Background(), req, ua.Handler{
		Established: func(_ context.Context, sess *ua.Session, m sip.Message) {
			s.ims.events <- event{kind: "established", s: sess, msg: m}
		},
		Failed: func(_ context.Context, _ *ua.Outgoing, res *sip.Response, _ error) {
			s.ims.events <- event{kind: "failed", res: res}
		},
	}); err != nil {
		s.t.Fatal(err)
	}
}

func carrierTLS(t *testing.T, ca, ibcfCA *sbitlstest.CA, hosts ...string) (sbitlstest.Issued, *tls.Config) {
	t.Helper()

	cert := ca.Issue(t, "carrier", sbitlstest.Leaf{Hosts: hosts})

	cfg, err := transport.NewTLSServerConfig(transport.TLSOptions{
		Certificates: []tls.Certificate{cert.Cert}, Roots: sbitlstest.Pool(ibcfCA),
		Identities: []string{"ibcf.example"},
	})
	if err != nil {
		t.Fatal(err)
	}

	return cert, cfg
}

func tlsTrunk(addr netip.AddrPort, tlsSettings settings.TrunkTLS) settings.Trunk {
	return settings.Trunk{
		ID: "tls", Name: "TLS", Domain: "carrier.example", Enabled: true, TLS: tlsSettings,
		Addresses:    []settings.TrunkAddress{{Host: addr.Addr().String(), Port: int(addr.Port()), Transport: settings.SIPTransportTLS}},
		InboundCIDRs: []netip.Prefix{netip.PrefixFrom(addr.Addr(), 32)},
	}
}

func TestTLSOutgoingCA(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity string
		ok       bool
	}{
		{"identity matches", "carrier.example", true},
		{"wrong identity", "other.example", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ca, ibcfCA := sbitlstest.NewCA(t, "carriers"), sbitlstest.NewCA(t, "ims")
			_, serve := carrierTLS(t, ca, ibcfCA, "carrier.example")
			carrier := newRealParty(t, "127.0.0.3", serve, nil)

			s := newTLSScene(t, ibcfCA, true, tlsTrunk(carrier.tls, settings.TrunkTLS{CACertificates: [][]byte{ca.Cert.Raw}, Identities: []string{tc.identity}}))
			s.call()

			if !tc.ok {
				if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode < 500 {
					t.Fatalf("call over a carrier with the wrong identity: %v", e.res)
				}

				carrier.none(500 * time.Millisecond)

				return
			}

			in := carrier.next("invite").req
			m := in.Message()

			if m.Flow.Transport != sip.TLS || m.Flow.Remote != s.ibcf.TLS()[0] {
				t.Fatalf("carrier got the INVITE over %s from %s", m.Flow.Transport, m.Flow.Remote)
			}

			if c := values(m, "Contact"); strings.Contains(strings.ToLower(c), "tls") || strings.HasPrefix(c, "<sips:") {
				t.Errorf("Contact %s", c)
			}

			_ = in.Accept(sdpOf("answer-1")...)

			e := s.ims.next("established")
			carrier.next("established")

			_ = e.s.Bye(context.Background(), ua.Change{}, nil)

			if bye := carrier.next("bye"); bye.msg.Env().Flow.Transport != sip.TLS {
				t.Errorf("BYE over %s", bye.msg.Env().Flow.Transport)
			}
		})
	}
}

func TestTLSOutgoingPinned(t *testing.T) {
	ca, ibcfCA := sbitlstest.NewCA(t, "carriers"), sbitlstest.NewCA(t, "ims")
	cert, serve := carrierTLS(t, ca, ibcfCA, "carrier.example")
	carrier := newRealParty(t, "127.0.0.3", serve, nil)

	s := newTLSScene(t, ibcfCA, true, tlsTrunk(carrier.tls, settings.TrunkTLS{PinnedCertificates: [][]byte{cert.Cert.Certificate[0]}}))

	s.call()

	_ = carrier.next("invite").req.Reject(486)

	if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != 486 {
		t.Fatalf("got %v, want 486", e.res)
	}
}

func TestTLSIncomingMutual(t *testing.T) {
	ca, ibcfCA := sbitlstest.NewCA(t, "carriers"), sbitlstest.NewCA(t, "ims")
	cert, serve := carrierTLS(t, ca, ibcfCA, "carrier.example")

	carrier := newRealParty(t, "127.0.0.3", serve, func(sip.Flow) (transport.TLSOptions, error) {
		return transport.TLSOptions{Certificates: []tls.Certificate{cert.Cert}, Roots: sbitlstest.Pool(ibcfCA), Identities: []string{"ibcf.example"}}, nil
	})

	s := newTLSScene(t, ibcfCA, true, tlsTrunk(carrier.tls, settings.TrunkTLS{
		Required: true, RequireClientCertificate: true, PinnedCertificates: [][]byte{cert.Cert.Certificate[0]},
	}))

	dial := func(p *realParty, tr sip.Transport) {
		to := s.ibcf.External()[0]
		if tr == sip.TLS {
			to = s.ibcf.TLS()[0]
		}

		uri := sip.URI{Scheme: "sip", User: alice, Host: to.Addr().String(), Port: to.Port()}
		uri.Params.Set("user", "phone")

		if tr != sip.UDP {
			uri.Params.Set("transport", strings.ToLower(string(tr)))
		}

		req := sip.NewRequest("INVITE", uri)
		req.Header.Add("From", "<sip:"+carol+"@carrier.example;user=phone>")
		req.Header.Add("To", "<sip:"+alice+"@carrier.example;user=phone>")
		req.SetBodies(ua.SDP([]byte("offer-1")))

		if _, err := p.a.Invite(context.Background(), req, ua.Handler{
			Failed: func(_ context.Context, _ *ua.Outgoing, res *sip.Response, _ error) {
				p.events <- event{kind: "failed", res: res}
			},
			Established: func(_ context.Context, sess *ua.Session, m sip.Message) {
				p.events <- event{kind: "established", s: sess, msg: m}
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("over TLS", func(t *testing.T) {
		dial(carrier, sip.TLS)

		in := s.ims.next("invite").req
		_ = in.Accept(sdpOf("answer-1")...)

		carrier.next("established")
	})

	t.Run("over TCP from a TLS-only trunk", func(t *testing.T) {
		dial(carrier, sip.TCP)

		if e := carrier.next("failed"); e.res == nil || e.res.StatusCode != 403 {
			t.Fatalf("got %v, want 403", e.res)
		}
	})

	t.Run("without a client certificate", func(t *testing.T) {
		_, bareServe := carrierTLS(t, ca, ibcfCA, "carrier.example")

		bare := newRealParty(t, "127.0.0.3", bareServe, func(sip.Flow) (transport.TLSOptions, error) {
			return transport.TLSOptions{Roots: sbitlstest.Pool(ibcfCA), Identities: []string{"ibcf.example"}}, nil
		})

		dial(bare, sip.TLS)
		s.ims.none(2 * time.Second)

		select {
		case e := <-bare.events:
			if e.res != nil {
				t.Fatalf("got %v, want no response", e.res)
			}
		default:
		}
	})
}

func TestTLSPersistentWithoutCertificate(t *testing.T) {
	ca := sbitlstest.NewCA(t, "carriers")
	cert := ca.Issue(t, "carrier", sbitlstest.Leaf{Hosts: []string{"carrier.example"}})

	ln, err := tls.Listen("tcp", "127.0.0.3:0", &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert.Cert}})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = ln.Close() })

	conns := make(chan net.Conn, 8)

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}

			if err := c.(*tls.Conn).HandshakeContext(t.Context()); err != nil {
				_ = c.Close()
				continue
			}

			conns <- c
		}
	}()

	addr := netip.MustParseAddrPort(ln.Addr().String())
	trunk := tlsTrunk(addr, settings.TrunkTLS{Required: true, CACertificates: [][]byte{ca.Cert.Raw}, Identities: []string{"carrier.example"}})

	s := newTLSScene(t, sbitlstest.NewCA(t, "ims"), false, trunk)

	var first net.Conn

	select {
	case first = <-conns:
	case <-time.After(10 * time.Second):
		t.Fatal("no persistent TLS connection to the trunk")
	}

	if got := netip.MustParseAddrPort(first.RemoteAddr().String()); !slices.Contains(s.ibcf.TLS(), got) {
		t.Errorf("connection from %s, want from the TLS address %v", got, s.ibcf.TLS())
	}

	_ = first.Close()

	select {
	case c := <-conns:
		_ = c.Close()
	case <-time.After(10 * time.Second):
		t.Fatal("the persistent TLS connection was not reopened")
	}
}
