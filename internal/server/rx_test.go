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
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/config"
)

func (e *e2e) aar() (string, rx.AARequest) {
	e.t.Helper()

	m := next(e.t, e.rx)
	if m.CommandCode != rx.CommandAA {
		e.t.Fatalf("Rx request %d, want an AAR", m.CommandCode)
	}

	r, err := rx.ParseAARequest(m)
	if err != nil {
		e.t.Fatalf("ParseAARequest: %v", err)
	}

	return tgpp.ParseEnvelope(m).SessionID, r
}

func (e *e2e) wantSTR(session string, cause rx.TerminationCause) {
	e.t.Helper()

	m := next(e.t, e.rx)
	if m.CommandCode != rx.CommandSessionTermination {
		e.t.Fatalf("Rx request %d, want an STR", m.CommandCode)
	}

	r, err := rx.ParseSessionTerminationRequest(m)
	if err != nil {
		e.t.Fatalf("ParseSessionTerminationRequest: %v", err)
	}

	if id := tgpp.ParseEnvelope(m).SessionID; id != session || r.Cause != cause {
		e.t.Fatalf("STR %s %s, want %s %s", id, r.Cause, session, cause)
	}
}

func (e *e2e) noRx(d time.Duration) {
	e.t.Helper()

	select {
	case m := <-e.rx:
		e.t.Fatalf("unexpected Rx request %d", m.CommandCode)
	case <-time.After(d):
	}
}

func (e *e2e) rxEnvelope(session string) tgpp.Envelope {
	env := e.hss.envelope()
	env.SessionID = session

	return env
}

func (e *e2e) setUpRx() string {
	e.t.Helper()

	e.setUp("600")

	id, r := e.aar()

	signalling := rx.FlowUsageAFSignalling
	if r.FramedIPAddress != ueAddr || len(r.MediaComponents) != 1 || r.MediaComponents[0].Number != 0 ||
		len(r.MediaComponents[0].SubComponents) != 1 || r.MediaComponents[0].SubComponents[0].FlowNumber != 0 ||
		*r.MediaComponents[0].SubComponents[0].FlowUsage != signalling ||
		!slices.Equal(r.SpecificActions, []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer}) {
		e.t.Fatalf("AAR = %+v, want the AF signalling subscription for %s", r, ueAddr)
	}

	eventually(e.t, "the Rx session on the P-CSCF's registration", func() bool {
		regs := e.pcscfRegistrations()
		return len(regs) == 1 && regs[0].RxSessionID == id
	})

	return id
}

func TestRxSessionFollowsTheRegistration(t *testing.T) {
	e := newE2E(t)
	id := e.setUpRx()

	e.protectedRegister("600", digestAuth("", false))
	e.noRx(200 * time.Millisecond)

	e.protectedRegister("0", digestAuth("", false))
	e.wantSTR(id, rx.TerminationLogout)
}

func TestRxSessionEndsWithTheNetworkDeregistration(t *testing.T) {
	e := newE2E(t)
	id := e.setUpRx()

	rtr, err := cx.NewRegistrationTerminationRequest(e.hss.envelope(), cx.RegistrationTerminationRequest{
		PrivateIdentity: e2eIMPI,
		Reason:          cx.DeregistrationReason{Code: cx.ReasonPermanentTermination},
	})

	if r := e.hss.send(t, rtr, err); r.Code != diameter.ResultSuccess {
		t.Fatalf("RTA result = %s", r)
	}

	e.notified()
	e.wantSTR(id, rx.TerminationAdministrative)
}

func TestRxReAuth(t *testing.T) {
	e := newE2E(t)
	id := e.setUpRx()

	rar, err := rx.NewReAuthRequest(e.rxEnvelope(id), rx.ReAuthRequest{
		SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfReleaseOfBearer},
	})

	if r := e.hss.send(t, rar, err); r.Code != diameter.ResultSuccess || r.Experimental {
		t.Fatalf("RAA result = %s, want DIAMETER_SUCCESS", r)
	}

	eventually(t, "the signalling to be marked lost", func() bool {
		regs := e.pcscfRegistrations()
		return len(regs) == 1 && regs[0].SignallingLost && regs[0].RxSessionID == id
	})

	e.noRx(200 * time.Millisecond)

	rar, err = rx.NewReAuthRequest(e.rxEnvelope(id), rx.ReAuthRequest{
		SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer},
	})
	if err != nil {
		t.Fatal(err)
	}

	rar.AVPs = slices.DeleteFunc(rar.AVPs, func(a diameter.AVP) bool {
		return a.Code == diameter.AVPDestinationHost && a.VendorID == 0
	})

	if r := e.hss.send(t, rar, nil); r.Code != diameter.ResultMissingAVP {
		t.Fatalf("RAA result without Destination-Host = %s, want DIAMETER_MISSING_AVP", r)
	}
}

func TestRxAbortSession(t *testing.T) {
	e := newE2E(t)
	id := e.setUpRx()

	asr, err := rx.NewAbortSessionRequest(e.rxEnvelope(id), rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
	if r := e.hss.send(t, asr, err); r.Code != diameter.ResultSuccess || r.Experimental {
		t.Fatalf("ASA result = %s, want DIAMETER_SUCCESS", r)
	}

	e.wantSTR(id, rx.TerminationAdministrative)

	eventually(t, "the session to be cleared and the registration kept", func() bool {
		regs := e.pcscfRegistrations()
		return len(regs) == 1 && regs[0].SignallingLost && regs[0].RxSessionID == ""
	})

	// Unknown once its STA is received.
	eventually(t, "an ASR for the ended session to get DIAMETER_UNKNOWN_SESSION_ID", func() bool {
		asr, err := rx.NewAbortSessionRequest(e.rxEnvelope(id), rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
		return e.hss.send(t, asr, err).Code == diameter.ResultUnknownSessionID
	})

	e.protectedRegister("600", digestAuth("", false))

	again, _ := e.aar()
	if again == id {
		t.Fatal("the new registration reused the aborted session")
	}

	eventually(t, "the new session and the signalling restored", func() bool {
		regs := e.pcscfRegistrations()
		return len(regs) == 1 && !regs[0].SignallingLost && regs[0].RxSessionID == again
	})
}

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
