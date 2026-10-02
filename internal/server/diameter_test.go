package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/config"
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

// fakePeer is a passive Diameter node standing in for the HSS or the PCRF. It
// accepts the IMS over TCP on loopback and records the states it sees the IMS
// go through.
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

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", netip.AddrPortFrom(loopback, 0).String())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	f.port = ln.Addr().(*net.TCPAddr).Port

	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      host,
			OriginRealm:     realm,
			HostIPAddresses: []netip.Addr{loopback},
			ProductName:     "fake",
		},
		Handler:           handler,
		OnPeerStateChange: f.peerStateChanged,
		Logger:            slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("diameter.New: %v", err)
	}

	appIDs := make([]diameter.Application, 0, len(apps))
	for _, a := range apps {
		appIDs = append(appIDs, applications[a])
	}

	if err := node.SetPeers([]diameter.Peer{{
		ID:           "ims",
		Host:         imsHost,
		Addresses:    []netip.Addr{loopback},
		Transport:    diameter.TransportTCP,
		Applications: appIDs,
		Passive:      true,
	}}); err != nil {
		t.Fatalf("SetPeers: %v", err)
	}

	go func() { _ = node.Serve(diameter.NewTCPListener(ln.(*net.TCPListener))) }()

	f.node = node

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = node.Shutdown(ctx)
	})

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

	// A peer enters Closing only when it receives a DPR; a dropped connection
	// goes straight to Down.
	for _, f := range []*fakePeer{hss, pcrf} {
		eventually(t, f.host+" to see the IMS disconnect", func() bool {
			return f.sawState(diameter.PeerDown)
		})

		if !f.sawState(diameter.PeerClosing) {
			t.Errorf("%s did not receive a DPR", f.host)
		}
	}
}
