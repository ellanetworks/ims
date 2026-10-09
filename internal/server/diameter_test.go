package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/diametertest"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/settings"
)

const (
	imsHost  = "ims.ims.mnc001.mcc001.3gppnetwork.org"
	imsRealm = "ims.mnc001.mcc001.3gppnetwork.org"
)

var (
	loopback  = netip.MustParseAddr("127.0.0.1")
	loopback6 = netip.MustParseAddr("::1")
)

type fakePeer struct {
	host  string
	realm string
	apps  []settings.Application
	node  *diameter.Node
	port  int

	mu     sync.Mutex
	states []diameter.PeerState
}

func newFakePeer(t *testing.T, host, realm string, apps ...settings.Application) *fakePeer {
	t.Helper()

	return newFakePeerWithHandler(t, host, realm, diameter.NewMux(), apps...)
}

func newFakePeerWithHandler(t *testing.T, host, realm string, handler diameter.Handler, apps ...settings.Application) *fakePeer {
	t.Helper()

	f := &fakePeer{host: host, realm: realm, apps: apps}

	appIDs := make([]diameter.Application, 0, len(apps))
	for _, a := range apps {
		appIDs = append(appIDs, applications[a])
	}

	var addr netip.AddrPort

	f.node, addr = diametertest.Listen(t, diametertest.Config{
		Identity: diameter.Identity{
			OriginHost:      host,
			OriginRealm:     realm,
			HostIPAddresses: []netip.Addr{loopback},
			ProductName:     "fake",
		},
		Peer:              diameter.Peer{ID: "ims", Host: imsHost, Applications: appIDs},
		Handler:           handler,
		OnPeerStateChange: f.peerStateChanged,
	})
	f.port = int(addr.Port())

	return f
}

func (f *fakePeer) peerStateChanged(p diameter.PeerStatus) {
	if p.ID != "ims" {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.states = append(f.states, p.State)
}

func (f *fakePeer) sawState(s diameter.PeerState) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Contains(f.states, s)
}

func (f *fakePeer) config(id string) settings.Peer {
	return settings.Peer{
		ID:           id,
		Host:         f.host,
		Address:      loopback,
		Port:         f.port,
		Transport:    settings.TransportTCP,
		Applications: f.apps,
		Priority:     settings.DefaultPriority,
	}
}

func (f *fakePeer) envelope() tgpp.Envelope {
	return tgpp.Envelope{
		SessionID:        f.node.NewSessionID(),
		Origin:           f.node.Identity(),
		DestinationHost:  imsHost,
		DestinationRealm: imsRealm,
	}
}

func (f *fakePeer) send(t *testing.T, req *diameter.Message, err error) tgpp.Result {
	t.Helper()

	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ans, err := f.node.Do(ctx, "ims", req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	r, err := tgpp.ParseResult(ans)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}

	return r
}

func seedPeers(peers ...settings.Peer) []settings.Peer {
	return peers
}

func startIMS(t *testing.T, cfg testIMS) *Server {
	t.Helper()

	return startIMSWith(t, cfg, ipsectest.NewKernel())
}

func startIMSWith(t *testing.T, cfg testIMS, kernel *ipsectest.Kernel) *Server {
	t.Helper()

	cfg.seed(t)

	srv := &Server{Config: cfg.Config, Logger: slog.New(slog.DiscardHandler), IPsec: kernel}
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { srv.Shutdown(context.Background()) })

	return srv
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func getDiameter(t *testing.T, srv *Server) api.DiameterStatus {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+srv.APIAddr().String()+"/api/v1/diameter", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET diameter: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Result api.DiameterStatus `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	return body.Result
}

func getPeers(t *testing.T, srv *Server) []api.DiameterPeer {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+srv.APIAddr().String()+"/api/v1/diameter/peers", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET Diameter peers: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Result api.DiameterPeers `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	return body.Result.Items
}

func waitOpen(t *testing.T, srv *Server, ids ...string) map[string]api.DiameterPeer {
	t.Helper()

	var peers map[string]api.DiameterPeer

	eventually(t, "Diameter peers to open", func() bool {
		items := getPeers(t, srv)

		peers = make(map[string]api.DiameterPeer, len(items))
		for _, p := range items {
			peers[p.ID] = p
		}

		for _, id := range ids {
			if peers[id].Status.State != "open" {
				return false
			}
		}

		return true
	})

	return peers
}

func TestDiameterPeersOpen(t *testing.T) {
	hss := newFakePeer(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, settings.ApplicationCx)
	pcrf := newFakePeer(t, "pcrf.epc.mnc001.mcc001.3gppnetwork.org", "epc.mnc001.mcc001.3gppnetwork.org", settings.ApplicationRx)

	cfg := testConfig(t)
	cfg.Peers = seedPeers(hss.config("hss"), pcrf.config("pcrf"))

	srv := startIMS(t, cfg)

	peers := waitOpen(t, srv, "hss", "pcrf")

	for id, want := range map[string]struct {
		host, realm string
		apps        []string
	}{
		"hss":  {hss.host, hss.realm, []string{"cx"}},
		"pcrf": {pcrf.host, pcrf.realm, []string{"rx"}},
	} {
		p := peers[id]
		if p.Host != want.host || p.Status.Realm != want.realm || p.Transport != "tcp" || p.Status.RemoteAddress != "127.0.0.1" ||
			!slices.Equal(p.Applications, want.apps) {
			t.Errorf("peer %s = %+v, want host %s, realm %s, tcp from 127.0.0.1 with %v", id, p, want.host, want.realm, want.apps)
		}
	}

	if status := getDiameter(t, srv); status.Host != imsHost || status.Realm != imsRealm {
		t.Errorf("identity = %s %s, want %s %s", status.Host, status.Realm, imsHost, imsRealm)
	}
}

func TestDiameterOnePeerServesCxAndRx(t *testing.T) {
	core := newFakePeer(t, "core.mnc001.mcc001.3gppnetwork.org", "mnc001.mcc001.3gppnetwork.org",
		settings.ApplicationCx, settings.ApplicationRx)

	cfg := testConfig(t)
	cfg.Peers = seedPeers(core.config("core"))

	srv := startIMS(t, cfg)

	if apps := waitOpen(t, srv, "core")["core"].Applications; !slices.Equal(apps, []string{"cx", "rx"}) {
		t.Fatalf("applications = %v, want [cx rx]", apps)
	}
}

func TestDiameterRequestsAreAnswered(t *testing.T) {
	hss := newFakePeer(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, settings.ApplicationCx)
	pcrf := newFakePeer(t, "pcrf.epc.mnc001.mcc001.3gppnetwork.org", "epc.mnc001.mcc001.3gppnetwork.org", settings.ApplicationRx)

	cfg := testConfig(t)
	cfg.Peers = seedPeers(hss.config("hss"), pcrf.config("pcrf"))
	cfg.Policy = settings.Policy{Interface: settings.PolicyRx}

	srv := startIMS(t, cfg)
	waitOpen(t, srv, "hss", "pcrf")

	rtr, err := cx.NewRegistrationTerminationRequest(hss.envelope(), cx.RegistrationTerminationRequest{
		PrivateIdentity: "001010000000001@ims.mnc001.mcc001.3gppnetwork.org",
		Reason:          cx.DeregistrationReason{Code: cx.ReasonPermanentTermination},
	})
	if r := hss.send(t, rtr, err); r.Code != diameter.ResultSuccess || r.Experimental {
		t.Errorf("RTA result for an unregistered user = %s, want DIAMETER_SUCCESS", r)
	}

	rar, err := rx.NewReAuthRequest(pcrf.envelope(), rx.ReAuthRequest{
		SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer},
	})
	if r := pcrf.send(t, rar, err); r.Code != diameter.ResultUnknownSessionID || r.Experimental {
		t.Errorf("RAA result = %s, want DIAMETER_UNKNOWN_SESSION_ID", r)
	}

	asr, err := rx.NewAbortSessionRequest(pcrf.envelope(), rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
	if r := pcrf.send(t, asr, err); r.Code != diameter.ResultUnknownSessionID || r.Experimental {
		t.Errorf("ASA result = %s, want DIAMETER_UNKNOWN_SESSION_ID", r)
	}
}

func TestDiameterShutdownSendsDPR(t *testing.T) {
	hss := newFakePeer(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, settings.ApplicationCx)
	pcrf := newFakePeer(t, "pcrf.epc.mnc001.mcc001.3gppnetwork.org", "epc.mnc001.mcc001.3gppnetwork.org", settings.ApplicationRx)

	cfg := testConfig(t)
	cfg.Peers = seedPeers(hss.config("hss"), pcrf.config("pcrf"))

	cfg.seed(t)

	srv := &Server{Config: cfg.Config, Logger: slog.New(slog.DiscardHandler), IPsec: ipsectest.NewKernel()}
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitOpen(t, srv, "hss", "pcrf")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	srv.Shutdown(ctx)

	for _, f := range []*fakePeer{hss, pcrf} {
		eventually(t, f.host+" to see the IMS disconnect", func() bool {
			return f.sawState(diameter.PeerDown)
		})

		if !f.sawState(diameter.PeerClosing) {
			t.Errorf("%s did not receive a DPR", f.host)
		}
	}
}

func TestRTRMalformed(t *testing.T) {
	hss := newFakePeer(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, settings.ApplicationCx)

	cfg := testConfig(t)
	cfg.Peers = seedPeers(hss.config("hss"))

	srv := startIMS(t, cfg)
	waitOpen(t, srv, "hss")

	rtr, err := cx.NewRegistrationTerminationRequest(hss.envelope(), cx.RegistrationTerminationRequest{
		PrivateIdentity:  testIMPI,
		Reason:           cx.DeregistrationReason{Code: cx.ReasonNewServerAssigned},
		PublicIdentities: []string{testIMPU},
	})
	if err != nil {
		t.Fatal(err)
	}

	rtr.AVPs = slicesDeleteAVP(rtr.AVPs, cx.AVPPublicIdentity)

	if r := hss.send(t, rtr, nil); r.Success() {
		t.Fatalf("RTA result = %s for an RTR without the Public-Identity NEW_SERVER_ASSIGNED requires", r)
	}
}

func slicesDeleteAVP(avps []diameter.AVP, code uint32) []diameter.AVP {
	var out []diameter.AVP

	for _, a := range avps {
		if a.Code != code {
			out = append(out, a)
		}
	}

	return out
}

func dialIMS(t *testing.T, host string, port int, apps ...settings.Application) *diameter.Node {
	t.Helper()

	appIDs := make([]diameter.Application, 0, len(apps))
	for _, a := range apps {
		appIDs = append(appIDs, applications[a])
	}

	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      host,
			OriginRealm:     imsRealm,
			HostIPAddresses: []netip.Addr{loopback},
			ProductName:     "fake",
		},
		Handler: diameter.NewMux(),
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = node.Shutdown(context.Background()) })

	err = node.SetPeers([]diameter.Peer{{
		ID: "ims", Host: imsHost, Addresses: []netip.Addr{loopback}, Dial: &diameter.Dial{Port: uint16(port)},
		Transports: []diameter.Transport{diameter.TransportTCP}, Applications: appIDs,
	}})
	if err != nil {
		t.Fatal(err)
	}

	return node
}

func TestDiameterPeerDialsIMS(t *testing.T) {
	const hssHost = "hss.ims.mnc001.mcc001.3gppnetwork.org"

	cfg := testConfig(t)
	cfg.Peers = seedPeers(settings.Peer{
		ID: "hss", Host: hssHost, Address: loopback, Port: unusedPort(t),
		Transport: settings.TransportTCP, Applications: []settings.Application{settings.ApplicationCx},
	})
	cfg.Diameter.Port = unusedPort(t)

	srv := startIMS(t, cfg)

	hss := dialIMS(t, hssHost, cfg.Diameter.Port, settings.ApplicationCx)
	diametertest.WaitOpen(t, hss, "ims")
	waitOpen(t, srv, "hss")
}

func TestDiameterRejectsUnknownPeers(t *testing.T) {
	cfg := testConfig(t)
	cfg.Diameter.Port = unusedPort(t)

	startIMS(t, cfg)

	c := dialRaw(t, cfg.Diameter.Port)
	c.send(cer("rogue.ims.mnc001.mcc001.3gppnetwork.org"))

	if code := resultCode(t, c.recvCE()); code != diameter.ResultUnknownPeer {
		t.Fatalf("CEA Result-Code = %d, want DIAMETER_UNKNOWN_PEER", code)
	}
}

// RFC 6733 §5.6.4: the IMS, with the higher Origin-Host, wins: it answers the
// HSS's connection and drops its own.
func TestDiameterElection(t *testing.T) {
	const hssHost = "hss.ims.mnc001.mcc001.3gppnetwork.org"

	hss := listenRaw(t)

	cfg := testConfig(t)
	cfg.Peers = seedPeers(settings.Peer{
		ID: "hss", Host: hssHost, Address: loopback, Port: hss.port(),
		Transport: settings.TransportTCP, Applications: []settings.Application{settings.ApplicationCx},
	})
	cfg.Diameter.Port = unusedPort(t)

	srv := startIMS(t, cfg)

	initiated := hss.accept()
	initiated.recvCE()

	responded := dialRaw(t, cfg.Diameter.Port)
	responded.send(cer(hssHost))

	if code := resultCode(t, responded.recvCE()); code != diameter.ResultSuccess {
		t.Fatalf("CEA Result-Code = %d on the HSS's connection, want DIAMETER_SUCCESS", code)
	}

	initiated.wantClosed()
	waitOpen(t, srv, "hss")
}

func TestDiameterHandshakeTimeout(t *testing.T) {
	const timeout = 500 * time.Millisecond

	for _, c := range []struct {
		name   string
		answer bool
	}{
		{"answered in time", true},
		{"not answered", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			const hssHost = "hss.ims.mnc001.mcc001.3gppnetwork.org"

			hss := listenRaw(t)

			cfg := testConfig(t)
			cfg.Peers = seedPeers(settings.Peer{
				ID: "hss", Host: hssHost, Address: loopback, Port: hss.port(),
				Transport: settings.TransportTCP, Applications: []settings.Application{settings.ApplicationCx},
			})
			cfg.Diameter.Port = unusedPort(t)
			cfg.seed(t)

			srv := &Server{
				Config: cfg.Config, Logger: slog.New(slog.DiscardHandler), IPsec: ipsectest.NewKernel(),
				DiameterHandshakeTimeout: timeout,
			}
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}

			t.Cleanup(func() { srv.Shutdown(context.Background()) })

			conn := hss.accept()
			req := conn.recvCE()
			start := time.Now()

			if !c.answer {
				conn.wantClosed()

				if waited := time.Since(start); waited < timeout/2 {
					t.Fatalf("connection closed after %s, want the %s handshake timeout", waited, timeout)
				}

				return
			}

			time.Sleep(timeout / 2)
			conn.send(cea(req, hssHost))
			waitOpen(t, srv, "hss")
		})
	}
}

type rawConn struct {
	t    *testing.T
	conn net.Conn
}

type rawListener struct {
	t  *testing.T
	ln net.Listener
}

func listenRaw(t *testing.T) *rawListener {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = ln.Close() })

	return &rawListener{t: t, ln: ln}
}

func (l *rawListener) port() int {
	return l.ln.Addr().(*net.TCPAddr).Port
}

func (l *rawListener) accept() *rawConn {
	l.t.Helper()

	conn, err := l.ln.Accept()
	if err != nil {
		l.t.Fatalf("accept: %v", err)
	}

	l.t.Cleanup(func() { _ = conn.Close() })

	return &rawConn{t: l.t, conn: conn}
}

func dialRaw(t *testing.T, port int) *rawConn {
	t.Helper()

	var d net.Dialer

	conn, err := d.DialContext(t.Context(), "tcp", netip.AddrPortFrom(loopback, uint16(port)).String())
	if err != nil {
		t.Fatalf("dial the IMS: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	return &rawConn{t: t, conn: conn}
}

func (c *rawConn) send(m *diameter.Message) {
	c.t.Helper()

	b, err := m.Marshal()
	if err != nil {
		c.t.Fatal(err)
	}

	if _, err := c.conn.Write(b); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *rawConn) read() (*diameter.Message, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	header := make([]byte, 20)
	if _, err := io.ReadFull(c.conn, header); err != nil {
		return nil, err
	}

	b := make([]byte, int(header[1])<<16|int(header[2])<<8|int(header[3]))
	copy(b, header)

	if _, err := io.ReadFull(c.conn, b[20:]); err != nil {
		return nil, err
	}

	return diameter.Unmarshal(b)
}

func (c *rawConn) recvCE() *diameter.Message {
	c.t.Helper()

	m, err := c.read()
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}

	if m.CommandCode != diameter.CommandCapabilitiesExchange {
		c.t.Fatalf("command %d, want a capabilities exchange", m.CommandCode)
	}

	return m
}

func (c *rawConn) wantClosed() {
	c.t.Helper()

	for {
		m, err := c.read()
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) {
			return
		}

		if err != nil {
			c.t.Fatalf("read: %v, want the connection closed", err)
		}

		if m.CommandCode != diameter.CommandDisconnectPeer {
			c.t.Fatalf("command %d, want the connection closed", m.CommandCode)
		}
	}
}

func capabilities(host string) []diameter.AVP {
	return []diameter.AVP{
		diameter.UTF8String(diameter.AVPOriginHost, diameter.AVPFlagMandatory, 0, host),
		diameter.UTF8String(diameter.AVPOriginRealm, diameter.AVPFlagMandatory, 0, imsRealm),
		diameter.Address(diameter.AVPHostIPAddress, diameter.AVPFlagMandatory, 0, loopback),
		diameter.Unsigned32(diameter.AVPVendorID, diameter.AVPFlagMandatory, 0, 0),
		diameter.UTF8String(diameter.AVPProductName, 0, 0, "raw"),
		diameter.Grouped(diameter.AVPVendorSpecificApplicationID, diameter.AVPFlagMandatory, 0,
			diameter.Unsigned32(diameter.AVPVendorID, diameter.AVPFlagMandatory, 0, tgpp.VendorID),
			diameter.Unsigned32(diameter.AVPAuthApplicationID, diameter.AVPFlagMandatory, 0, cx.ApplicationID)),
	}
}

func cer(host string) *diameter.Message {
	return &diameter.Message{
		Flags: diameter.FlagRequest, CommandCode: diameter.CommandCapabilitiesExchange,
		HopByHopID: 1, EndToEndID: 1, AVPs: capabilities(host),
	}
}

func cea(req *diameter.Message, host string) *diameter.Message {
	return &diameter.Message{
		CommandCode: diameter.CommandCapabilitiesExchange, HopByHopID: req.HopByHopID, EndToEndID: req.EndToEndID,
		AVPs: append([]diameter.AVP{diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0,
			diameter.ResultSuccess)}, capabilities(host)...),
	}
}

func resultCode(t *testing.T, m *diameter.Message) uint32 {
	t.Helper()

	r, err := tgpp.ParseResult(m)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}

	return r.Code
}

// While a realm changes, a peer is on the routes to both the old realm and the new one.
func TestDiameterPeersRouteThePreviousRealms(t *testing.T) {
	previous := settings.Settings{
		Operator: settings.Operator{MCC: "001", MNC: "01"},
		Peers: []settings.Peer{{
			ID: "core", Host: "core.epc.example.org", Address: netip.MustParseAddr("192.0.2.1"), Port: 3868,
			Applications: []settings.Application{settings.ApplicationCx, settings.ApplicationRx}, Priority: 10,
		}},
		Routes: []settings.Route{
			{Application: settings.ApplicationCx, Realm: "old.example.org"},
			{Application: settings.ApplicationRx, Realm: "epc.example.org"},
		},
	}

	next := previous
	next.Routes = []settings.Route{
		{Application: settings.ApplicationCx, Realm: "new.example.org"},
		{Application: settings.ApplicationRx, Realm: "epc.example.org"},
	}

	route := func(realm string, app uint32) diameter.Route {
		return diameter.Route{Realm: realm, Application: app, Priority: 10}
	}

	if got, want := diameterPeers(next, previous)[0].Routes, []diameter.Route{
		route("new.example.org", cx.ApplicationID), route("old.example.org", cx.ApplicationID),
		route("epc.example.org", rx.ApplicationID),
	}; !slices.Equal(got, want) {
		t.Fatalf("routes while the realm changes = %+v, want %+v", got, want)
	}

	if got, want := diameterPeers(next)[0].Routes, []diameter.Route{
		route("new.example.org", cx.ApplicationID), route("epc.example.org", rx.ApplicationID),
	}; !slices.Equal(got, want) {
		t.Fatalf("routes = %+v, want %+v", got, want)
	}
}
