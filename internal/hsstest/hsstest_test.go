package hsstest

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/milenage"
)

const (
	realm     = "ims.mnc001.mcc001.3gppnetwork.org"
	imsHost   = "ims." + realm
	impi      = "001010000000001@" + realm
	tempIMPU  = "sip:" + impi
	msisdn    = "sip:+15550001@" + realm
	scscfName = "sip:scscf." + realm + ":5060"
)

var (
	k   = []byte("0123456789abcdef")
	opc = []byte("fedcba9876543210")
)

type client struct {
	t    *testing.T
	hss  *HSS
	node *diameter.Node
	rtrs chan cx.RegistrationTerminationRequest
}

func newClient(t *testing.T) *client {
	t.Helper()

	h := New(t, Config{Realm: realm, IMSHost: imsHost})
	h.Add(Subscriber{
		IMPI:  impi,
		IMSI:  "001010000000001",
		K:     k,
		OPc:   opc,
		SQN:   32,
		IMPUs: []cx.ProfileIdentity{{Identity: tempIMPU, Barred: true}, {Identity: msisdn}},
	})

	c := &client{t: t, hss: h, rtrs: make(chan cx.RegistrationTerminationRequest, 1)}

	mux := diameter.NewMux()
	mux.Handle(cx.ApplicationID, cx.CommandRegistrationTermination, diameter.HandlerFunc(
		func(_ context.Context, conn *diameter.Conn, req *diameter.Message) *diameter.Message {
			rtr, err := cx.ParseRegistrationTerminationRequest(req)
			if err != nil {
				return cx.NewErrorAnswer(req, conn.LocalIdentity(), err, 0)
			}

			c.rtrs <- rtr

			ans, _ := cx.NewRegistrationTerminationAnswer(req, conn.LocalIdentity(), cx.RegistrationTermination{})

			return ans
		}))

	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      imsHost,
			OriginRealm:     realm,
			HostIPAddresses: []netip.Addr{h.Addr().Addr()},
			ProductName:     "client",
		},
		Handler: mux,
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := node.SetPeers([]diameter.Peer{{
		ID:           "hss",
		Host:         h.Host(),
		Addresses:    []netip.Addr{h.Addr().Addr()},
		Port:         h.Addr().Port(),
		Transport:    diameter.TransportTCP,
		Applications: []diameter.Application{{ID: cx.ApplicationID, VendorID: tgpp.VendorID}},
	}}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = node.Shutdown(ctx)
	})

	c.node = node

	deadline := time.Now().Add(10 * time.Second)
	for p, _ := node.Peer("hss"); p.State != diameter.PeerOpen; p, _ = node.Peer("hss") {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the HSS")
		}

		time.Sleep(10 * time.Millisecond)
	}

	return c
}

func (c *client) envelope() tgpp.Envelope {
	return tgpp.Envelope{
		SessionID:        c.node.NewSessionID(),
		Origin:           c.node.Identity(),
		DestinationHost:  c.hss.Host(),
		DestinationRealm: realm,
	}
}

func (c *client) do(req *diameter.Message, err error) *diameter.Message {
	c.t.Helper()

	if err != nil {
		c.t.Fatal(err)
	}

	ans, err := c.node.Do(c.t.Context(), "hss", req)
	if err != nil {
		c.t.Fatal(err)
	}

	return ans
}

func (c *client) uar(impu string, t cx.AuthorizationType) (cx.UserAuthorization, error) {
	c.t.Helper()

	return cx.ParseUserAuthorizationAnswer(c.do(cx.NewUserAuthorizationRequest(c.envelope(), cx.UserAuthorizationRequest{
		PrivateIdentity: impi, PublicIdentity: impu, VisitedNetwork: realm, AuthorizationType: t,
	})))
}

func (c *client) mar(resync *cx.Resync) (cx.MultimediaAuth, error) {
	c.t.Helper()

	return cx.ParseMultimediaAuthAnswer(c.do(cx.NewMultimediaAuthRequest(c.envelope(), cx.MultimediaAuthRequest{
		PrivateIdentity: impi, PublicIdentity: tempIMPU, ServerName: scscfName, NumberOfItems: 1,
		Scheme: cx.SchemeDigestAKAv1MD5, Resync: resync,
	})))
}

func (c *client) sar(t cx.AssignmentType) (cx.ServerAssignment, error) {
	c.t.Helper()

	return cx.ParseServerAssignmentAnswer(c.do(cx.NewServerAssignmentRequest(c.envelope(), cx.ServerAssignmentRequest{
		PrivateIdentity: impi, PublicIdentities: []string{tempIMPU}, ServerName: scscfName, Type: t,
	})))
}

func (c *client) lir(impu string) (cx.LocationInfo, error) {
	c.t.Helper()

	return cx.ParseLocationInfoAnswer(c.do(cx.NewLocationInfoRequest(c.envelope(), cx.LocationInfoRequest{PublicIdentity: impu})))
}

func wantExperimental(t *testing.T, err error, code uint32) {
	t.Helper()

	var re *cx.ResultError
	if !errors.As(err, &re) || !re.IsExperimental(code) {
		t.Fatalf("error = %v, want experimental result %d", err, code)
	}
}

func akaVector(t *testing.T, maa cx.MultimediaAuth) *cx.AKAVector {
	t.Helper()

	if len(maa.Items) != 1 || maa.Items[0].Scheme != cx.SchemeDigestAKAv1MD5 || maa.Items[0].AKA == nil {
		t.Fatalf("MAA = %+v, want one Digest-AKAv1-MD5 vector", maa)
	}

	return maa.Items[0].AKA
}

func TestRegistration(t *testing.T) {
	c := newClient(t)

	uaa, err := c.uar(tempIMPU, cx.AuthorizationRegistration)
	if err != nil || uaa.Result != tgpp.Experimental(tgpp.ResultFirstRegistration) || uaa.ServerName != "" {
		t.Fatalf("UAA = %+v, %v, want FIRST_REGISTRATION", uaa, err)
	}

	if r := c.hss.Next(t); r.UAR == nil || r.UAR.PublicIdentity != tempIMPU {
		t.Fatalf("request = %s, want the UAR", r)
	}

	maa, err := c.mar(nil)
	if err != nil {
		t.Fatal(err)
	}

	v := akaVector(t, maa)

	r, err := milenage.Respond(k, opc, v.RAND, v.AUTN)
	if err != nil || r.SQN != 33 || string(r.AMF) != "\x00\x00" || string(r.RES) != string(v.XRES) ||
		string(r.CK) != string(v.CK) || string(r.IK) != string(v.IK) {
		t.Fatalf("Respond = %+v, %v, want SQN 33, AMF 0 and the vector's values", r, err)
	}

	saa, err := c.sar(cx.AssignmentRegistration)
	if err != nil {
		t.Fatal(err)
	}

	sub, err := cx.ParseUserData(saa.UserData)
	if err != nil || sub.PrivateIdentity != impi || len(sub.ServiceProfiles) != 1 ||
		len(sub.ServiceProfiles[0].PublicIdentities) != 2 || !sub.ServiceProfiles[0].PublicIdentities[0].Barred {
		t.Fatalf("user data = %+v, %v", sub, err)
	}

	if s, _ := c.hss.Subscriber(impi); s.State != Registered || s.ServerName != scscfName || s.SQN != 33 {
		t.Fatalf("subscriber = %+v, want registered at %s with SQN 33", s, scscfName)
	}

	uaa, err = c.uar(msisdn, cx.AuthorizationRegistration)
	if err != nil || uaa.Result != tgpp.Experimental(tgpp.ResultSubsequentRegistration) || uaa.ServerName != scscfName {
		t.Fatalf("UAA = %+v, %v, want SUBSEQUENT_REGISTRATION at %s", uaa, err, scscfName)
	}

	if lia, err := c.lir(msisdn); err != nil || lia.ServerName != scscfName {
		t.Fatalf("LIA = %+v, %v, want %s", lia, err, scscfName)
	}

	if uaa, err := c.uar(tempIMPU, cx.AuthorizationDeregistration); err != nil || uaa.ServerName != scscfName {
		t.Fatalf("UAA = %+v, %v, want %s", uaa, err, scscfName)
	}

	if _, err := c.sar(cx.AssignmentUserDeregistration); err != nil {
		t.Fatal(err)
	}

	if s, _ := c.hss.Subscriber(impi); s.State != NotRegistered || s.ServerName != "" {
		t.Fatalf("subscriber = %+v, want not registered", s)
	}

	_, err = c.lir(msisdn)
	wantExperimental(t, err, tgpp.ResultErrorIdentityNotRegistered)

	_, err = c.uar(tempIMPU, cx.AuthorizationDeregistration)
	wantExperimental(t, err, tgpp.ResultErrorIdentityNotRegistered)
}

func TestResync(t *testing.T) {
	c := newClient(t)

	maa, err := c.mar(nil)
	if err != nil {
		t.Fatal(err)
	}

	v := akaVector(t, maa)

	auts, err := milenage.AUTS(k, opc, v.RAND, 1000)
	if err != nil {
		t.Fatal(err)
	}

	if maa, err = c.mar(&cx.Resync{RAND: v.RAND, AUTS: auts}); err != nil {
		t.Fatal(err)
	}

	v = akaVector(t, maa)

	if r, err := milenage.Respond(k, opc, v.RAND, v.AUTN); err != nil || r.SQN != 1001 {
		t.Fatalf("Respond = %+v, %v, want SQN 1001", r, err)
	}

	auts[0] ^= 1

	if maa, err = c.mar(&cx.Resync{RAND: v.RAND, AUTS: auts}); err != nil {
		t.Fatal(err)
	}

	v = akaVector(t, maa)

	if r, err := milenage.Respond(k, opc, v.RAND, v.AUTN); err != nil || r.SQN != 1002 {
		t.Fatalf("Respond = %+v, %v, want SQN 1002 after a bad AUTS", r, err)
	}
}

func TestWrongKey(t *testing.T) {
	c := newClient(t)
	c.hss.Update(impi, func(s *Subscriber) { s.K = []byte("not the ue's key") })

	maa, err := c.mar(nil)
	if err != nil {
		t.Fatal(err)
	}

	v := akaVector(t, maa)

	if _, err := milenage.Respond(k, opc, v.RAND, v.AUTN); !errors.Is(err, milenage.ErrMACFailure) {
		t.Fatalf("Respond = %v, want a MAC failure", err)
	}
}

func TestUnknownIdentities(t *testing.T) {
	c := newClient(t)

	_, err := c.uar("sip:stranger@"+realm, cx.AuthorizationRegistration)
	wantExperimental(t, err, tgpp.ResultErrorIdentitiesDontMatch)

	_, err = c.lir("sip:stranger@" + realm)
	wantExperimental(t, err, tgpp.ResultErrorUserUnknown)

	_, err = cx.ParseMultimediaAuthAnswer(c.do(cx.NewMultimediaAuthRequest(c.envelope(), cx.MultimediaAuthRequest{
		PrivateIdentity: "nobody@" + realm, PublicIdentity: tempIMPU, ServerName: scscfName, NumberOfItems: 1,
		Scheme: cx.SchemeDigestAKAv1MD5,
	})))
	wantExperimental(t, err, tgpp.ResultErrorUserUnknown)
}

func TestRTR(t *testing.T) {
	c := newClient(t)

	if _, err := c.mar(nil); err != nil {
		t.Fatal(err)
	}

	if _, err := c.sar(cx.AssignmentRegistration); err != nil {
		t.Fatal(err)
	}

	if _, err := c.hss.RTR(t.Context(), cx.DeregistrationReason{Code: cx.ReasonPermanentTermination}, impi); err != nil {
		t.Fatal(err)
	}

	select {
	case rtr := <-c.rtrs:
		if rtr.PrivateIdentity != impi || rtr.Reason.Code != cx.ReasonPermanentTermination {
			t.Fatalf("RTR = %+v", rtr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no RTR")
	}

	if s, _ := c.hss.Subscriber(impi); s.State != NotRegistered || s.ServerName != "" {
		t.Fatalf("subscriber = %+v, want not registered after the RTR", s)
	}
}
