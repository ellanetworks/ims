package server

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/config"
)

func TestRxNotNegotiatedWithAPeerConfiguredForCx(t *testing.T) {
	// The HSS advertises Rx too, but is configured for Cx only.
	hss := newFakePeer(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, config.ApplicationCx, config.ApplicationRx)
	pcrf := newFakePeer(t, "pcrf.epc.mnc001.mcc001.3gppnetwork.org", "epc.mnc001.mcc001.3gppnetwork.org", config.ApplicationRx)

	cfg := testConfig(t)
	cfg.Diameter = diameterConfig(hss.config("hss"), pcrf.config("pcrf"))
	cfg.Diameter.Peers[0].Applications = []config.Application{config.ApplicationCx}

	srv := startIMS(t, cfg)
	waitOpen(t, srv, "hss", "pcrf")

	asr, err := rx.NewAbortSessionRequest(hss.envelope(), rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if _, err := hss.node.Do(ctx, "ims", asr); !errors.Is(err, diameter.ErrApplicationUnsupported) {
		t.Fatalf("ASR from the HSS: err = %v, want Rx not negotiated with it", err)
	}
}

// stubSessions knows one session, and records the termination of an aborted
// one.
type stubSessions struct {
	session    string
	aborted    chan struct{}
	terminated chan struct{}
}

func (s *stubSessions) ReAuth(session string, _ rx.ReAuthRequest) bool {
	return session == s.session
}

func (s *stubSessions) AbortSession(session string, _ rx.AbortSessionRequest) (func(), bool) {
	if session != s.session {
		return nil, false
	}

	close(s.aborted)

	return func() { close(s.terminated) }, true
}

// rxClient connects to a node serving the Rx handler h, as the peer the node
// knows as "ims".
func rxClient(t *testing.T, h *rxHandler) (*fakePeer, *diameter.Node) {
	t.Helper()

	server := newFakePeerWithHandler(t, "pcscf.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, newDiameterMux(newRTRHandler(h.log), h),
		config.ApplicationRx)

	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost: imsHost, OriginRealm: imsRealm, HostIPAddresses: []netip.Addr{loopback}, ProductName: "pcrf",
		},
		Handler: diameter.NewMux(),
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := node.SetPeers([]diameter.Peer{{
		ID: "server", Host: server.host, Addresses: []netip.Addr{loopback}, Port: uint16(server.port),
		Transport: diameter.TransportTCP, Applications: []diameter.Application{applications[config.ApplicationRx]},
	}}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = node.Shutdown(ctx)
	})

	eventually(t, "the Rx client to connect", func() bool {
		p, _ := node.Peer("server")
		return p.State == diameter.PeerOpen
	})

	return server, node
}

func abortSession(t *testing.T, server *fakePeer, node *diameter.Node, session string) tgpp.Result {
	t.Helper()

	asr, err := rx.NewAbortSessionRequest(tgpp.Envelope{
		SessionID: session, Origin: node.Identity(), DestinationHost: server.host, DestinationRealm: server.realm,
	}, rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ans, err := node.Do(ctx, "server", asr)
	if err != nil {
		t.Fatalf("ASR: %v", err)
	}

	r, err := tgpp.ParseResult(ans)
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func TestRxHandlerAnswersOnlyTheRxPeer(t *testing.T) {
	const session = "pcscf;1;1"

	for _, tc := range []struct {
		name string
		peer string
		want uint32
	}{
		{"from the Rx peer", "ims", diameter.ResultSuccess},
		{"from another peer", "pcrf", diameter.ResultUnknownSessionID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := &stubSessions{session: session, aborted: make(chan struct{}), terminated: make(chan struct{})}

			h := newRxHandler(tc.peer, slog.New(slog.DiscardHandler))
			h.bind(sessions)

			server, node := rxClient(t, h)

			if r := abortSession(t, server, node, session); r.Code != tc.want || r.Experimental {
				t.Fatalf("ASA result = %s, want %d", r, tc.want)
			}

			if tc.want != diameter.ResultSuccess {
				select {
				case <-sessions.aborted:
					t.Fatal("a session aborted by a peer other than the Rx peer")
				default:
				}

				return
			}

			// The termination runs once the ASA is written.
			select {
			case <-sessions.terminated:
			case <-time.After(5 * time.Second):
				t.Fatal("the session's termination did not run after the ASA")
			}
		})
	}
}

func TestRxHandlerRejectsAMalformedRAR(t *testing.T) {
	const session = "pcscf;1;1"

	h := newRxHandler("ims", slog.New(slog.DiscardHandler))
	h.bind(&stubSessions{session: session})

	server, node := rxClient(t, h)

	rar, err := rx.NewReAuthRequest(tgpp.Envelope{
		SessionID: session, Origin: node.Identity(), DestinationHost: server.host, DestinationRealm: server.realm,
	}, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}})
	if err != nil {
		t.Fatal(err)
	}

	rar.AVPs = slices.DeleteFunc(rar.AVPs, func(a diameter.AVP) bool {
		return a.Code == diameter.AVPDestinationHost && a.VendorID == 0
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ans, err := node.Do(ctx, "server", rar)
	if err != nil {
		t.Fatalf("RAR: %v", err)
	}

	if r, err := tgpp.ParseResult(ans); err != nil || r.Code != diameter.ResultMissingAVP {
		t.Fatalf("RAA result = %s, %v, want DIAMETER_MISSING_AVP", r, err)
	}
}
