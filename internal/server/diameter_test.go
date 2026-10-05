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
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/diametertest"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
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
	apps  []config.Application
	node  *diameter.Node
	port  int

	mu     sync.Mutex
	states []diameter.PeerState
}

func newFakePeer(t *testing.T, host, realm string, apps ...config.Application) *fakePeer {
	t.Helper()

	return newFakePeerWithHandler(t, host, realm, diameter.NewMux(), apps...)
}

func newFakePeerWithHandler(t *testing.T, host, realm string, handler diameter.Handler, apps ...config.Application) *fakePeer {
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

func (f *fakePeer) config(id string) config.DiameterPeer {
	return config.DiameterPeer{
		ID:           id,
		Host:         f.host,
		Realm:        f.realm,
		Address:      loopback,
		Port:         f.port,
		Transport:    config.TransportTCP,
		Applications: f.apps,
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

func diameterConfig(peers ...config.DiameterPeer) config.Diameter {
	return config.Diameter{
		OriginHost:  imsHost,
		OriginRealm: imsRealm,
		Address:     loopback,
		Peers:       peers,
	}
}

func startIMS(t *testing.T, cfg config.Config) *Server {
	t.Helper()

	return startIMSWith(t, cfg, ipsectest.NewKernel())
}

func startIMSWith(t *testing.T, cfg config.Config, kernel *ipsectest.Kernel) *Server {
	t.Helper()

	srv := &Server{Config: cfg, Logger: slog.New(slog.DiscardHandler), IPsec: kernel}
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

func waitOpen(t *testing.T, srv *Server, ids ...string) map[string]api.DiameterPeer {
	t.Helper()

	var peers map[string]api.DiameterPeer

	eventually(t, "Diameter peers to open", func() bool {
		status := getDiameter(t, srv)

		peers = make(map[string]api.DiameterPeer, len(status.Peers))
		for _, p := range status.Peers {
			peers[p.ID] = p
		}

		for _, id := range ids {
			if peers[id].State != "open" {
				return false
			}
		}

		return true
	})

	return peers
}

func TestDiameterPeersOpen(t *testing.T) {
	hss := newFakePeer(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, config.ApplicationCx)
	pcrf := newFakePeer(t, "pcrf.epc.mnc001.mcc001.3gppnetwork.org", "epc.mnc001.mcc001.3gppnetwork.org", config.ApplicationRx)

	cfg := testConfig(t)
	cfg.Diameter = diameterConfig(hss.config("hss"), pcrf.config("pcrf"))

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
		if p.Host != want.host || p.Realm != want.realm || p.Transport != "tcp" || p.Address != "127.0.0.1" ||
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
		config.ApplicationCx, config.ApplicationRx)

	cfg := testConfig(t)
	cfg.Diameter = diameterConfig(core.config("core"))

	srv := startIMS(t, cfg)

	if apps := waitOpen(t, srv, "core")["core"].Applications; !slices.Equal(apps, []string{"cx", "rx"}) {
		t.Fatalf("applications = %v, want [cx rx]", apps)
	}
}

func TestDiameterRequestsAreAnswered(t *testing.T) {
	hss := newFakePeer(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, config.ApplicationCx)
	pcrf := newFakePeer(t, "pcrf.epc.mnc001.mcc001.3gppnetwork.org", "epc.mnc001.mcc001.3gppnetwork.org", config.ApplicationRx)

	cfg := testConfig(t)
	cfg.Diameter = diameterConfig(hss.config("hss"), pcrf.config("pcrf"))
	cfg.PCSCF.Policy.Rx = "pcrf"

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
	hss := newFakePeer(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, config.ApplicationCx)
	pcrf := newFakePeer(t, "pcrf.epc.mnc001.mcc001.3gppnetwork.org", "epc.mnc001.mcc001.3gppnetwork.org", config.ApplicationRx)

	cfg := testConfig(t)
	cfg.Diameter = diameterConfig(hss.config("hss"), pcrf.config("pcrf"))

	srv := &Server{Config: cfg, Logger: slog.New(slog.DiscardHandler), IPsec: ipsectest.NewKernel()}
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
	hss := newFakePeer(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, config.ApplicationCx)

	cfg := testConfig(t)
	cfg.Diameter = diameterConfig(hss.config("hss"))

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

func dialIMS(t *testing.T, host string, port int, apps ...config.Application) *diameter.Node {
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
		ID: "ims", Host: imsHost, Addresses: []netip.Addr{loopback}, Port: uint16(port),
		Transport: diameter.TransportTCP, Applications: appIDs,
	}})
	if err != nil {
		t.Fatal(err)
	}

	return node
}

func TestDiameterPeerDialsIMS(t *testing.T) {
	const hssHost = "hss.ims.mnc001.mcc001.3gppnetwork.org"

	cfg := testConfig(t)
	cfg.Diameter = diameterConfig(config.DiameterPeer{
		ID: "hss", Host: hssHost, Realm: imsRealm, Address: loopback, Port: unusedPort(t),
		Transport: config.TransportTCP, Applications: []config.Application{config.ApplicationCx},
	})
	cfg.Diameter.Port = unusedPort(t)

	srv := startIMS(t, cfg)

	hss := dialIMS(t, hssHost, cfg.Diameter.Port, config.ApplicationCx)
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
	cfg.Diameter = diameterConfig(config.DiameterPeer{
		ID: "hss", Host: hssHost, Realm: imsRealm, Address: loopback, Port: hss.port(),
		Transport: config.TransportTCP, Applications: []config.Application{config.ApplicationCx},
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
			cfg.Diameter = diameterConfig(config.DiameterPeer{
				ID: "hss", Host: hssHost, Realm: imsRealm, Address: loopback, Port: hss.port(),
				Transport: config.TransportTCP, Applications: []config.Application{config.ApplicationCx},
			})
			cfg.Diameter.Port = unusedPort(t)

			srv := &Server{
				Config: cfg, Logger: slog.New(slog.DiscardHandler), IPsec: ipsectest.NewKernel(),
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
