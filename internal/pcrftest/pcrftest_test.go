package pcrftest

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/diametertest"
)

const (
	realm   = "ims.mnc001.mcc001.3gppnetwork.org"
	imsHost = "ims." + realm
)

type client struct {
	t    *testing.T
	pcrf *PCRF
	node *diameter.Node
	asrs chan string
	rars chan []rx.SpecificAction
}

func newClient(t *testing.T) *client {
	t.Helper()

	p := New(t, Config{Realm: realm, IMSHost: imsHost, Logger: slog.New(slog.DiscardHandler)})
	c := &client{t: t, pcrf: p, asrs: make(chan string, 4), rars: make(chan []rx.SpecificAction, 4)}

	mux := diameter.NewMux()
	mux.Handle(rx.ApplicationID, rx.CommandAbortSession, diameter.HandlerFunc(
		func(_ context.Context, conn *diameter.Conn, req *diameter.Message) *diameter.Message {
			if _, err := rx.ParseAbortSessionRequest(req); err != nil {
				return rx.NewErrorAnswer(req, conn.LocalIdentity(), err, 0)
			}

			c.asrs <- tgpp.ParseEnvelope(req).SessionID

			ans, _ := rx.NewAbortSessionAnswer(req, conn.LocalIdentity(), rx.AbortSessionAnswer{})

			return ans
		}))
	mux.Handle(rx.ApplicationID, rx.CommandReAuth, diameter.HandlerFunc(
		func(_ context.Context, conn *diameter.Conn, req *diameter.Message) *diameter.Message {
			rar, err := rx.ParseReAuthRequest(req)
			if err != nil {
				return rx.NewErrorAnswer(req, conn.LocalIdentity(), err, 0)
			}

			c.rars <- rar.SpecificActions

			ans, _ := rx.NewReAuthAnswer(req, conn.LocalIdentity(), rx.ReAuthAnswer{})

			return ans
		}))

	c.node = diametertest.Dial(t, diametertest.Config{
		Identity: diameter.Identity{
			OriginHost:      imsHost,
			OriginRealm:     realm,
			HostIPAddresses: []netip.Addr{p.Addr().Addr()},
			ProductName:     "client",
		},
		Peer: diameter.Peer{
			ID:           "pcrf",
			Host:         p.Host(),
			Applications: []diameter.Application{{ID: rx.ApplicationID, VendorID: tgpp.VendorID}},
		},
		Handler: mux,
	}, p.Addr())

	return c
}

func (c *client) envelope(session string) tgpp.Envelope {
	return tgpp.Envelope{
		SessionID:        session,
		Origin:           c.node.Identity(),
		DestinationHost:  c.pcrf.Host(),
		DestinationRealm: realm,
	}
}

func (c *client) do(req *diameter.Message, err error) *diameter.Message {
	c.t.Helper()

	if err != nil {
		c.t.Fatal(err)
	}

	ans, err := c.node.Do(c.t.Context(), "pcrf", req)
	if err != nil {
		c.t.Fatal(err)
	}

	return ans
}

func TestSession(t *testing.T) {
	c := newClient(t)
	session := c.node.NewSessionID()
	ue := netip.MustParseAddr("10.0.0.2")

	signalling := rx.FlowUsageAFSignalling

	ans := c.do(rx.NewAARequest(c.envelope(session), rx.AARequest{
		FramedIPAddress: ue,
		MediaComponents: []rx.MediaComponent{{SubComponents: []rx.MediaSubComponent{{FlowUsage: &signalling}}}},
	}))
	if aaa, err := rx.ParseAAAnswer(ans); err != nil || len(aaa.Class) != 1 || string(aaa.Class[0]) != session {
		t.Fatalf("AAA = %+v, %v; want the Session-Id as Class", aaa, err)
	}

	if r := c.pcrf.Next(t); r.SessionID != session || r.AAR == nil || r.AAR.FramedIPAddress != ue {
		t.Fatalf("recorded %s, want the AAR for %s", r, ue)
	}

	if _, err := c.pcrf.RAR(t.Context(), session, rx.ActionIndicationOfLossOfBearer); err != nil {
		t.Fatalf("RAR: %v", err)
	}

	if got := <-c.rars; len(got) != 1 || got[0] != rx.ActionIndicationOfLossOfBearer {
		t.Fatalf("RAR actions = %v", got)
	}

	if _, err := c.pcrf.ASR(t.Context(), session, rx.AbortBearerReleased); err != nil {
		t.Fatalf("ASR: %v", err)
	}

	if got := <-c.asrs; got != session {
		t.Fatalf("ASR for %s, want %s", got, session)
	}

	ans = c.do(rx.NewSessionTerminationRequest(c.envelope(session), rx.SessionTerminationRequest{Cause: rx.TerminationAdministrative}))
	if _, err := rx.ParseSessionTerminationAnswer(ans); err != nil {
		t.Fatalf("STA: %v", err)
	}

	if r := c.pcrf.Next(t); r.SessionID != session || r.STR == nil || r.STR.Cause != rx.TerminationAdministrative {
		t.Fatalf("recorded %s, want the STR", r)
	}
}

func TestASRWithoutTheIMS(t *testing.T) {
	p := New(t, Config{Realm: realm, IMSHost: imsHost, Logger: slog.New(slog.DiscardHandler)})

	if _, err := p.ASR(t.Context(), "ims;1;1", rx.AbortBearerReleased); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("ASR err = %v, want ErrNotConnected", err)
	}
}

func resultOf(t *testing.T, ans *diameter.Message) uint32 {
	t.Helper()

	_, err := rx.ParseAAAnswer(ans)
	if err == nil {
		return diameter.ResultSuccess
	}

	r, ok := tgpp.ResultOf(err)
	if !ok {
		t.Fatalf("AAA error %v carries no result", err)
	}

	return r.Code
}

func audio(flows ...string) []rx.MediaComponent {
	return []rx.MediaComponent{{Number: 1, Type: new(rx.MediaAudio), SubComponents: []rx.MediaSubComponent{{FlowNumber: 1, FlowDescriptions: flows}}}}
}

// The Open5GS PCRF binds every AA-Request and refuses Flow-Descriptions its SMF cannot use.
func TestAARChecks(t *testing.T) {
	ue := netip.MustParseAddr("10.0.0.2")
	update := rx.RequestUpdate

	for name, tc := range map[string]struct {
		r    rx.AARequest
		want uint32
	}{
		"no binding": {rx.AARequest{MediaComponents: audio()}, tgpp.ResultIPCANSessionNotAvailable},
		"valid": {rx.AARequest{FramedIPAddress: ue, MediaComponents: audio(
			"permit in 17 from 10.0.0.2 to 192.0.2.9 5000", "permit out 17 from 192.0.2.9 to 10.0.0.2 4000",
		)}, diameter.ResultSuccess},
		"UE on the wrong side": {rx.AARequest{FramedIPAddress: ue, MediaComponents: audio(
			"permit out 17 from 10.0.0.2 to 192.0.2.9 5000",
		)}, diameter.ResultInvalidAVPValue},
		"no destination port": {rx.AARequest{FramedIPAddress: ue, MediaComponents: audio(
			"permit out 17 from 192.0.2.9 to 10.0.0.2",
		)}, diameter.ResultInvalidAVPValue},
		"update of an unknown session": {rx.AARequest{FramedIPAddress: ue, RequestType: &update}, diameter.ResultUnknownSessionID},
	} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t)

			if got := resultOf(t, c.do(rx.NewAARequest(c.envelope(c.node.NewSessionID()), tc.r))); got != tc.want {
				t.Fatalf("result %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSTRForAnUnknownSession(t *testing.T) {
	c := newClient(t)

	ans := c.do(rx.NewSessionTerminationRequest(c.envelope(c.node.NewSessionID()), rx.SessionTerminationRequest{
		Cause: rx.TerminationLogout,
	}))

	if _, err := rx.ParseSessionTerminationAnswer(ans); err == nil {
		t.Fatal("STA succeeded for an unknown session")
	}
}

func TestHoldAndRefuse(t *testing.T) {
	c := newClient(t)
	ue := netip.MustParseAddr("10.0.0.2")

	release := c.pcrf.HoldAA()
	done := make(chan uint32, 1)

	go func() {
		done <- resultOf(t, c.do(rx.NewAARequest(c.envelope(c.node.NewSessionID()), rx.AARequest{FramedIPAddress: ue})))
	}()

	c.pcrf.Next(t)

	select {
	case <-done:
		t.Fatal("AAA sent while held")
	case <-time.After(100 * time.Millisecond):
	}

	c.pcrf.RefuseWhen(func(rx.AARequest) *tgpp.Result {
		return &tgpp.Result{Code: tgpp.ResultRequestedServiceNotAuthorized, Experimental: true, VendorID: tgpp.VendorID}
	})
	release()

	if got := <-done; got != tgpp.ResultRequestedServiceNotAuthorized {
		t.Fatalf("result %d, want the refusal", got)
	}
}
