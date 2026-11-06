package ibcf

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/netip"
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

func TestConnectionsFromUnknownSource(t *testing.T) {
	st := settings.Settings{Trunks: []settings.Trunk{{
		ID: "c", Name: "C", Domain: "c.example", Enabled: true,
		Addresses:    []settings.TrunkAddress{{Host: "127.0.0.2", Port: 5060, Transport: settings.SIPTransportTCP}},
		InboundCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.2/32")},
	}}}
	get := func() settings.Settings { return st }

	tc := transaction.Config{}
	tc.Transport.MaxConnections = 4

	b, err := launch(t, Config{
		HomeDomain: home,
		Internal:   []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
		External:   []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
		ICSCF:      []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:5999")},
		SCSCF:      []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:5999")},
		Trust:      trust.New([]netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil),
		BGCF:       bgcf.New(get, nil),
		Settings:   get,
	}, tc, true)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })

	ext := b.External()[0]
	options := func(src string, i int) string {
		return fmt.Sprintf("OPTIONS sip:%s SIP/2.0\r\nVia: SIP/2.0/TCP %s:1;branch=z9hG4bK%d\r\nFrom: <sip:x@x>;tag=1\r\n"+
			"To: <sip:%s>\r\nCall-ID: %d@x\r\nCSeq: 1 OPTIONS\r\nMax-Forwards: 70\r\nContent-Length: 0\r\n\r\n", ext, src, i, ext, i)
	}

	dial := func(src string) (net.Conn, error) {
		d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(src)}}

		c, err := d.DialContext(t.Context(), "tcp", ext.String())
		if err == nil {
			t.Cleanup(func() { _ = c.Close() })
		}

		return c, err
	}

	for i := range 4 {
		c, err := dial("127.0.0.9")
		if err != nil {
			continue
		}

		_, _ = c.Write([]byte(options("127.0.0.9", i)))

		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatalf("connection %d from an unknown source answered", i)
		}
	}

	c, err := dial("127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}

	_, _ = c.Write([]byte(options("127.0.0.2", 9)))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))

	if line, err := bufio.NewReader(c).ReadString('\n'); err != nil || line != "SIP/2.0 200 OK\r\n" {
		t.Fatalf("trunk after 4 connections from an unknown source: %q, %v", line, err)
	}
}

func TestEntryHeaderAllowlist(t *testing.T) {
	hostile := []sip.Field{
		f("Answer-Mode", "Auto"),
		f("Priv-Answer-Mode", "Auto;require"),
		f("Reject-Contact", `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`),
		f("Request-Disposition", "no-fork"),
		f("Alert-Info", "<http://attacker.example/ring.wav>"),
		f("Call-Info", "<http://attacker.example/logo.png>;purpose=icon"),
		f("P-Preferred-Service", "urn:urn-7:3gpp-service.ims.icsi.mcptt"),
		f("Identity", "abc;info=<https://attacker.example/c.pem>"),
		f("P-Associated-URI", "<sip:x@y>"),
		f("Diversion", "<tel:+15550001>;reason=unconditional"),
		f("History-Info", "<tel:+15550001>;index=1"),
		f("Resource-Priority", "ets.0"),
		f("P-Asserted-Service", "urn:urn-7:3gpp-service.ims.icsi.mmtel"),
		f("User-to-User", "abcd;encoding=hex"),
	}

	for _, trusted := range []bool{false, true} {
		t.Run(fmt.Sprint("trusted ", trusted), func(t *testing.T) {
			s := newScene(t, trusted)
			s.dial(s.carrier, "offer-1", append(hostile, f("Privacy", "none"), f("Subject", "hello"), f("P-Early-Media", "supported"))...)

			m := s.ims.next("invite").req.Message()

			for _, h := range hostile {
				if m.Header.Has(h.Name) {
					t.Errorf("%s %q reached the IMS", h.Name, values(m, h.Name))
				}
			}

			if values(m, "Privacy") != "none" || values(m, "Subject") != "hello" {
				t.Errorf("Privacy %q, Subject %q", values(m, "Privacy"), values(m, "Subject"))
			}

			if got := m.Header.Has("P-Early-Media"); got != trusted {
				t.Errorf("P-Early-Media %q", values(m, "P-Early-Media"))
			}
		})
	}
}

func TestEntryFromHomeDomain(t *testing.T) {
	for _, tc := range []struct {
		from    string
		trusted bool
		want    string
	}{
		{"<sip:" + alice + "@" + home + ";user=phone>", false, "sip:" + alice + "@sip.carrier.example;user=phone"},
		{"<sip:" + alice + "@PCSCF." + home + ".>", false, "sip:" + alice + "@sip.carrier.example"},
		{"<sip:" + alice + "@not" + home + ">", false, "sip:" + alice + "@not" + home},
		{"<sip:" + alice + "@" + home + ";user=phone>", true, "sip:" + alice + "@" + home + ";user=phone"},
	} {
		t.Run(tc.from, func(t *testing.T) {
			s := newScene(t, tc.trusted)
			s.carrier.invite("sip:"+carol+"@"+externalAddr.String()+";user=phone", "offer-1", f("From", tc.from))

			m := s.ims.next("invite").req.Message()

			if from, _ := m.Header.From(); from.URI.String() != tc.want {
				t.Errorf("From %s, want %s", from.URI, tc.want)
			}
		})
	}
}

func TestTLSIncomingWithoutClientCertificateBye(t *testing.T) {
	ca, ibcfCA := sbitlstest.NewCA(t, "carriers"), sbitlstest.NewCA(t, "ims")
	cert, serve := carrierTLS(t, ca, ibcfCA, "carrier.example")

	carrier := newRealParty(t, "127.0.0.3", serve, func(sip.Flow) (transport.TLSOptions, error) {
		return transport.TLSOptions{Roots: sbitlstest.Pool(ibcfCA), Identities: []string{"ibcf.example"}}, nil
	})

	s := newTLSScene(t, ibcfCA, true, tlsTrunk(carrier.tls, settings.TrunkTLS{Pins: [][32]byte{sha256.Sum256(cert.Cert.Certificate[0])}}))

	to := s.ibcf.TLS()[0]
	carrier.secure = to
	uri := sip.URI{Scheme: "sip", User: alice, Host: to.Addr().String(), Port: to.Port()}
	uri.Params.Set("user", "phone")

	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<sip:"+carol+"@carrier.example;user=phone>")
	req.Header.Add("To", "<sip:"+alice+"@carrier.example;user=phone>")

	second, err := carrier.a.Layer().ListenTLS(t.Context(), netip.MustParseAddrPort("127.0.0.3:0"), serve)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Add("Contact", fmt.Sprintf("<sip:%s;transport=tls>", second))
	req.SetBodies(ua.SDP([]byte("offer-1")))

	if _, err := carrier.a.Invite(context.Background(), req, ua.Handler{
		Established: func(_ context.Context, sess *ua.Session, m sip.Message) {
			carrier.events <- event{kind: "established", s: sess, msg: m}
		},
		Bye: func(_ context.Context, sess *ua.Session, r *ua.Request) {
			carrier.events <- event{kind: "bye", s: sess, msg: r.Message()}

			_ = r.Respond(ua.Reply{Code: 200})
		},
	}); err != nil {
		t.Fatal(err)
	}

	in := s.ims.next("invite").req

	var h sip.Header
	h.Set("Contact", "<sip:"+s.ims.udp.String()+">")
	_ = in.Respond(ua.Reply{Code: 200, Header: h, Bodies: sdpOf("answer-1")})

	carrier.next("established")
	e := s.ims.next("established")

	if err := e.s.Bye(context.Background(), ua.Change{}, nil); err != nil {
		t.Fatal(err)
	}

	bye := carrier.next("bye").msg.Env().Flow
	if bye.Transport != sip.TLS || bye.Local != second {
		t.Errorf("BYE over %s", bye)
	}
}

func TestPeersPruned(t *testing.T) {
	p := newPeers()
	a := settings.TrunkAddress{Host: "carrier.example", Port: 5061, Transport: settings.SIPTransportTLS}
	tr := settings.Trunk{ID: "t", Enabled: true, Addresses: []settings.TrunkAddress{a}, TLS: settings.TrunkTLS{Pins: [][32]byte{{1}}}}
	srv := sip.Server{Transport: sip.TLS, Addr: netip.MustParseAddrPort("192.0.2.1:5061"), Domain: "carrier.example"}

	p.note([]settings.Trunk{tr}, tr, a, srv)
	p.prune([]settings.Trunk{tr})

	if _, ok := p.get(srv.Addr); !ok {
		t.Fatal("peer of a current trunk pruned")
	}

	moved := tr
	moved.Addresses = []settings.TrunkAddress{{Host: "other.example", Transport: settings.SIPTransportTLS}}

	p.prune([]settings.Trunk{moved})

	if _, ok := p.get(srv.Addr); ok {
		t.Fatal("peer of a changed address kept")
	}

	disabled := tr
	disabled.Enabled = false

	p.note([]settings.Trunk{tr}, tr, a, srv)
	p.prune([]settings.Trunk{disabled})

	if _, ok := p.get(srv.Addr); ok {
		t.Fatal("peer of a disabled trunk kept")
	}

	p.note([]settings.Trunk{tr}, tr, a, srv)
	p.prune(nil)

	if _, ok := p.get(srv.Addr); ok {
		t.Fatal("peer of a removed trunk kept")
	}
}

func TestTrunkDialogFlow(t *testing.T) {
	s := newScene(t, false)
	local := netip.MustParseAddrPort("203.0.113.1:5061")
	carrier := netip.MustParseAddrPort("198.51.100.1:5061")
	tr := s.trunk
	tr.TLS = settings.TrunkTLS{Pins: [][32]byte{{1}}}

	invite := func(remote netip.AddrPort, contact string) *sip.Request {
		req := sip.NewRequest("INVITE", sip.URI{Scheme: "sip", User: alice, Host: "203.0.113.1"})
		req.Header.Add("Contact", contact)
		req.Flow = sip.Flow{Transport: sip.TLS, Local: local, Remote: remote}

		return req
	}

	ephemeral := netip.MustParseAddrPort("198.51.100.1:40000")

	for _, tc := range []struct {
		name    string
		trunk   settings.Trunk
		dialed  bool
		contact string
		want    sip.Flow
	}{
		{"connection the IBCF dialed", tr, true, "<sip:198.51.100.1:5061;transport=tls>", sip.Flow{Transport: sip.TLS, Local: local, Remote: ephemeral}},
		{"carrier connection, Contact over TLS", tr, false, "<sip:198.51.100.1:5061;transport=tls>", sip.Flow{Transport: sip.TLS, Local: local, Remote: carrier}},
		{"carrier connection, Contact without transport", tr, false, "<sip:198.51.100.1>", sip.Flow{Transport: sip.TLS, Local: local, Remote: carrier}},
		{"carrier connection, Contact off the trunk", tr, false, "<sip:192.0.2.1:5061;transport=tls>", sip.Flow{Transport: sip.TLS, Local: local}},
		{"mutual TLS", func() settings.Trunk { r := tr; r.TLS.Required = true; return r }(), false, "<sip:198.51.100.1:5061;transport=tls>", sip.Flow{Transport: sip.TLS, Local: local, Remote: ephemeral}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := s.ibcf
			b.peers.mu.Lock()
			clear(b.peers.peers)
			b.peers.mu.Unlock()

			if tc.dialed {
				b.peers.note([]settings.Trunk{tc.trunk}, tc.trunk, settings.TrunkAddress{Host: "198.51.100.1", Transport: settings.SIPTransportTLS},
					sip.Server{Transport: sip.TLS, Addr: ephemeral})
			}

			s.setTrunk(tc.trunk)

			if got := b.trunkDialogFlow(t.Context(), tc.trunk, invite(ephemeral, tc.contact)); got != tc.want {
				t.Errorf("flow %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPeersSharedServer(t *testing.T) {
	srv := sip.Server{Transport: sip.TLS, Addr: netip.MustParseAddrPort("192.0.2.1:5061")}
	first := settings.TrunkAddress{Host: "a.carrier.example", Transport: settings.SIPTransportTLS}
	second := settings.TrunkAddress{Host: "b.carrier.example", Transport: settings.SIPTransportTLS}
	a := settings.Trunk{ID: "a", Addresses: []settings.TrunkAddress{first, second}}
	b := settings.Trunk{ID: "b", Addresses: []settings.TrunkAddress{first}}

	trunks := []settings.Trunk{a, b}

	for _, order := range [][]string{{"a", "b"}, {"b", "a"}} {
		p := newPeers()

		for _, id := range order {
			tr := a
			if id == "b" {
				tr = b
			}

			p.note(trunks, tr, first, srv)
		}

		if pr, _ := p.get(srv.Addr); pr.trunk != "a" {
			t.Fatalf("noted in order %v: server of trunk %s, want a", order, pr.trunk)
		}

		other := settings.TrunkAddress{Host: "a.carrier.example"}
		p.note(trunks, b, other, srv)

		if ok, first := p.note(trunks, b, other, srv); ok || first {
			t.Fatalf("repeated refusal: ok %v, logged %v", ok, first)
		}
	}

	p := newPeers()
	p.note(trunks, a, second, srv)
	p.note(trunks, a, first, srv)
	p.note(trunks, a, second, srv)

	if pr, _ := p.get(srv.Addr); pr.serverName != "a.carrier.example" {
		t.Fatalf("server name %s, want the first address's", pr.serverName)
	}
}

func TestPeersRankFollowsSettings(t *testing.T) {
	srv := sip.Server{Transport: sip.TLS, Addr: netip.MustParseAddrPort("192.0.2.1:5061")}
	addr := settings.TrunkAddress{Host: "carrier.example", Transport: settings.SIPTransportTLS}
	pins := settings.TrunkTLS{Pins: [][32]byte{{1}}}
	a := settings.Trunk{ID: "a", Enabled: true, Addresses: []settings.TrunkAddress{addr}, TLS: pins}
	b := settings.Trunk{ID: "b", Enabled: true, Addresses: []settings.TrunkAddress{addr}, TLS: pins}
	c := settings.Trunk{ID: "c", Enabled: true, Addresses: []settings.TrunkAddress{addr}, TLS: pins}

	p := newPeers()
	p.note([]settings.Trunk{a, b, c}, b, addr, srv)

	after := []settings.Trunk{b, c}
	p.prune(after)

	if ok, _ := p.note(after, c, addr, srv); ok {
		t.Fatal("a later trunk took the server over after an earlier trunk was deleted")
	}

	reordered := []settings.Trunk{c, b}
	p.prune(reordered)

	if ok, _ := p.note(reordered, c, addr, srv); !ok {
		t.Fatal("the first trunk did not take the server over")
	}

	if pr, _ := p.get(srv.Addr); pr.trunk != "c" {
		t.Fatalf("server of trunk %s, want c", pr.trunk)
	}
}

func TestSendToSharedServer(t *testing.T) {
	s := newScene(t, false)
	srv := sip.Server{Transport: sip.TLS, Addr: netip.MustParseAddrPort("198.51.100.1:5061")}
	addr := settings.TrunkAddress{Host: "198.51.100.1", Port: 5061, Transport: settings.SIPTransportTLS}
	owner := s.trunk
	owner.Addresses = []settings.TrunkAddress{addr}
	owner.TLS = settings.TrunkTLS{Pins: [][32]byte{{1}}}
	other := owner
	other.ID, other.Name = "other", "Other"

	s.mu.Lock()
	s.st.Trunks = []settings.Trunk{owner, other}
	s.mu.Unlock()

	if !s.ibcf.noteServer(owner, addr, srv) {
		t.Fatal("first trunk refused")
	}

	req := sip.NewRequest("OPTIONS", sip.URI{Scheme: "sip", Host: "198.51.100.1"})

	if _, err := s.ibcf.Send(t.Context(), other, addr, req, srv); !errors.Is(err, settings.ErrSharedServer) {
		t.Fatalf("err = %v, want ErrSharedServer", err)
	}
}
