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

	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      imsHost,
			OriginRealm:     realm,
			HostIPAddresses: []netip.Addr{p.Addr().Addr()},
			ProductName:     "client",
		},
		Handler: mux,
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := node.SetPeers([]diameter.Peer{{
		ID:           "pcrf",
		Host:         p.Host(),
		Addresses:    []netip.Addr{p.Addr().Addr()},
		Port:         p.Addr().Port(),
		Transport:    diameter.TransportTCP,
		Applications: []diameter.Application{{ID: rx.ApplicationID, VendorID: tgpp.VendorID}},
	}}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = node.Shutdown(ctx)
	})

	c.node = node

	p.WaitConnected(t)

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
	if _, err := rx.ParseAAAnswer(ans); err != nil {
		t.Fatalf("AAA: %v", err)
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
