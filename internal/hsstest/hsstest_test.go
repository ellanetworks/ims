package hsstest

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/diametertest"
	"github.com/ellanetworks/ims/internal/milenage"
)

const (
	realm     = "ims.mnc001.mcc001.3gppnetwork.org"
	imsHost   = "ims." + realm
	impi      = "001010000000001@" + realm
	tempIMPU  = "sip:" + impi
	msisdn    = "sip:+15550001@" + realm
	scscfName = "sip:scscf." + realm + ":5060"
	otherName = "sip:scscf2." + realm + ":5060"
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

	hold chan struct{}
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

	c := &client{t: t, hss: h, rtrs: make(chan cx.RegistrationTerminationRequest, 4)}

	mux := diameter.NewMux()
	mux.Handle(cx.ApplicationID, cx.CommandRegistrationTermination, diameter.HandlerFunc(
		func(_ context.Context, conn *diameter.Conn, req *diameter.Message) *diameter.Message {
			rtr, err := cx.ParseRegistrationTerminationRequest(req)
			if err != nil {
				return cx.NewErrorAnswer(req, conn.LocalIdentity(), err, 0)
			}

			c.rtrs <- rtr

			if c.hold != nil {
				<-c.hold
			}

			ans, _ := cx.NewRegistrationTerminationAnswer(req, conn.LocalIdentity(), cx.RegistrationTermination{})

			return ans
		}))

	c.node = diametertest.Dial(t, diametertest.Config{
		Identity: diameter.Identity{
			OriginHost:      imsHost,
			OriginRealm:     realm,
			HostIPAddresses: []netip.Addr{h.Addr().Addr()},
			ProductName:     "client",
		},
		Peer: diameter.Peer{
			ID:           "hss",
			Host:         h.Host(),
			Applications: []diameter.Application{{ID: cx.ApplicationID, VendorID: tgpp.VendorID}},
		},
		Handler: mux,
	}, h.Addr())

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

func (c *client) marWith(r cx.MultimediaAuthRequest) (cx.MultimediaAuth, error) {
	c.t.Helper()

	if r.PrivateIdentity == "" {
		r.PrivateIdentity = impi
	}

	if r.PublicIdentity == "" {
		r.PublicIdentity = tempIMPU
	}

	if r.ServerName == "" {
		r.ServerName = scscfName
	}

	if r.Scheme == "" {
		r.Scheme = cx.SchemeDigestAKAv1MD5
	}

	r.NumberOfItems = 1

	return cx.ParseMultimediaAuthAnswer(c.do(cx.NewMultimediaAuthRequest(c.envelope(), r)))
}

func (c *client) mar(resync *cx.Resync) (cx.MultimediaAuth, error) {
	c.t.Helper()

	return c.marWith(cx.MultimediaAuthRequest{Resync: resync})
}

func (c *client) sarWith(r cx.ServerAssignmentRequest) (cx.ServerAssignment, error) {
	c.t.Helper()

	if r.PrivateIdentity == "" {
		r.PrivateIdentity = impi
	}

	if r.PublicIdentities == nil {
		r.PublicIdentities = []string{tempIMPU}
	}

	if r.ServerName == "" {
		r.ServerName = scscfName
	}

	return cx.ParseServerAssignmentAnswer(c.do(cx.NewServerAssignmentRequest(c.envelope(), r)))
}

func (c *client) sar(t cx.AssignmentType) (cx.ServerAssignment, error) {
	c.t.Helper()

	return c.sarWith(cx.ServerAssignmentRequest{Type: t})
}

func (c *client) lir(impu string) (cx.LocationInfo, error) {
	c.t.Helper()

	return c.lirWith(cx.LocationInfoRequest{PublicIdentity: impu})
}

func (c *client) lirWith(r cx.LocationInfoRequest) (cx.LocationInfo, error) {
	c.t.Helper()

	return cx.ParseLocationInfoAnswer(c.do(cx.NewLocationInfoRequest(c.envelope(), r)))
}

func (c *client) register() {
	c.t.Helper()

	if _, err := c.mar(nil); err != nil {
		c.t.Fatal(err)
	}

	if _, err := c.sar(cx.AssignmentRegistration); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) state() Subscriber {
	c.t.Helper()

	s, _ := c.hss.Subscriber(impi)

	return s
}

func wantExperimental(t *testing.T, err error, code uint32) {
	t.Helper()

	var re *cx.ResultError
	if !errors.As(err, &re) || !re.IsExperimental(code) {
		t.Fatalf("error = %v, want experimental result %d", err, code)
	}
}

func wantBase(t *testing.T, err error, code uint32) {
	t.Helper()

	var re *cx.ResultError
	if !errors.As(err, &re) || re.Experimental || re.Code != code {
		t.Fatalf("error = %v, want result %d", err, code)
	}
}

var (
	success      = tgpp.Result{}
	first        = tgpp.Experimental(tgpp.ResultFirstRegistration)
	subsequent   = tgpp.Experimental(tgpp.ResultSubsequentRegistration)
	unregistered = tgpp.Experimental(tgpp.ResultUnregisteredService)
)

func sameResult(got, want tgpp.Result) bool {
	if want == success {
		return !got.Experimental && (got.Code == 0 || got.Code == diameter.ResultSuccess)
	}

	return got == want
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
	if err != nil || !sameResult(uaa.Result, first) || uaa.ServerName != "" {
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

	if s := c.state(); s.State != NotRegistered || !s.AuthPending || s.ServerName != scscfName {
		t.Fatalf("subscriber = %+v, want authentication pending at %s", s, scscfName)
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

	if s := c.state(); s.State != Registered || s.AuthPending || s.ServerName != scscfName || s.SQN != 33 {
		t.Fatalf("subscriber = %+v, want registered at %s with SQN 33", s, scscfName)
	}

	uaa, err = c.uar(msisdn, cx.AuthorizationRegistration)
	if err != nil || !sameResult(uaa.Result, subsequent) || uaa.ServerName != scscfName {
		t.Fatalf("UAA = %+v, %v, want SUBSEQUENT_REGISTRATION at %s", uaa, err, scscfName)
	}

	if lia, err := c.lir(msisdn); err != nil || !sameResult(lia.Result, success) || lia.ServerName != scscfName {
		t.Fatalf("LIA = %+v, %v, want DIAMETER_SUCCESS at %s", lia, err, scscfName)
	}

	saa, err = c.sarWith(cx.ServerAssignmentRequest{Type: cx.AssignmentReRegistration, UserDataAlreadyAvailable: true})
	if err != nil || saa.UserData != nil {
		t.Fatalf("SAA = %+v, %v, want no User-Data", saa, err)
	}

	if uaa, err := c.uar(tempIMPU, cx.AuthorizationDeregistration); err != nil || !sameResult(uaa.Result, success) ||
		uaa.ServerName != scscfName {
		t.Fatalf("UAA = %+v, %v, want DIAMETER_SUCCESS at %s", uaa, err, scscfName)
	}

	if _, err := c.sar(cx.AssignmentUserDeregistration); err != nil {
		t.Fatal(err)
	}

	if s := c.state(); s.State != NotRegistered || s.ServerName != "" {
		t.Fatalf("subscriber = %+v, want not registered", s)
	}

	_, err = c.lir(msisdn)
	wantExperimental(t, err, tgpp.ResultErrorIdentityNotRegistered)

	_, err = c.uar(tempIMPU, cx.AuthorizationDeregistration)
	wantExperimental(t, err, tgpp.ResultErrorIdentityNotRegistered)
}

func TestRegistrationAndCapabilities(t *testing.T) {
	c := newClient(t)
	c.register()

	uaa, err := c.uar(tempIMPU, cx.AuthorizationRegistrationAndCapabilities)
	if err != nil || !sameResult(uaa.Result, success) || uaa.ServerName != "" || !c.state().ReassignPending {
		t.Fatalf("UAA = %+v, %v, want DIAMETER_SUCCESS without a name", uaa, err)
	}

	if _, err := c.marWith(cx.MultimediaAuthRequest{ServerName: otherName}); err != nil {
		t.Fatal(err)
	}

	if _, err := c.sarWith(cx.ServerAssignmentRequest{Type: cx.AssignmentRegistration, ServerName: otherName}); err != nil {
		t.Fatal(err)
	}

	if s := c.state(); s.ServerName != otherName || s.ReassignPending || s.State != Registered {
		t.Fatalf("subscriber = %+v, want registered at %s", s, otherName)
	}
}

func TestAnotherSCSCF(t *testing.T) {
	c := newClient(t)
	c.register()

	for _, typ := range []cx.AssignmentType{cx.AssignmentRegistration, cx.AssignmentUnregisteredUser} {
		_, err := c.sarWith(cx.ServerAssignmentRequest{Type: typ, ServerName: otherName})
		wantExperimental(t, err, tgpp.ResultErrorIdentityAlreadyRegistered)
	}

	_, err := c.sarWith(cx.ServerAssignmentRequest{Type: cx.AssignmentNoAssignment, ServerName: otherName})
	wantBase(t, err, diameter.ResultUnableToComply)

	if s := c.state(); s.ServerName != scscfName || s.State != Registered {
		t.Fatalf("subscriber = %+v, want still registered at %s", s, scscfName)
	}

	saa, err := c.sar(cx.AssignmentNoAssignment)
	if err != nil || saa.UserData == nil {
		t.Fatalf("SAA = %+v, %v, want User-Data for the assigned S-CSCF", saa, err)
	}
}

func TestDeregistrationWhileAuthenticationPending(t *testing.T) {
	c := newClient(t)

	if _, err := c.mar(nil); err != nil {
		t.Fatal(err)
	}

	uaa, err := c.uar(tempIMPU, cx.AuthorizationDeregistration)
	if err != nil || !sameResult(uaa.Result, success) || uaa.ServerName != scscfName {
		t.Fatalf("UAA = %+v, %v, want DIAMETER_SUCCESS at %s", uaa, err, scscfName)
	}

	uaa, err = c.uar(tempIMPU, cx.AuthorizationRegistration)
	if err != nil || !sameResult(uaa.Result, subsequent) || uaa.ServerName != scscfName {
		t.Fatalf("UAA = %+v, %v, want SUBSEQUENT_REGISTRATION at %s", uaa, err, scscfName)
	}
}

func TestAuthenticationFailure(t *testing.T) {
	for _, typ := range []cx.AssignmentType{cx.AssignmentAuthenticationFailure, cx.AssignmentAuthenticationTimeout} {
		t.Run(typ.String(), func(t *testing.T) {
			c := newClient(t)

			if _, err := c.mar(nil); err != nil {
				t.Fatal(err)
			}

			if _, err := c.sar(typ); err != nil {
				t.Fatal(err)
			}

			if s := c.state(); s.ServerName != "" || s.AuthPending || s.State != NotRegistered {
				t.Fatalf("subscriber = %+v after a failure while not registered, want the name cleared", s)
			}

			c.register()

			if _, err := c.mar(nil); err != nil {
				t.Fatal(err)
			}

			if _, err := c.sar(typ); err != nil {
				t.Fatal(err)
			}

			if s := c.state(); s.ServerName != scscfName || s.AuthPending || s.State != Registered {
				t.Fatalf("subscriber = %+v after a failure while registered, want the registration kept", s)
			}
		})
	}
}

func TestUnregisteredUser(t *testing.T) {
	c := newClient(t)

	saa, err := c.sar(cx.AssignmentUnregisteredUser)
	if err != nil || saa.UserData == nil {
		t.Fatalf("SAA = %+v, %v, want User-Data", saa, err)
	}

	if s := c.state(); s.State != Unregistered || s.ServerName != scscfName {
		t.Fatalf("subscriber = %+v, want unregistered at %s", s, scscfName)
	}

	if lia, err := c.lir(msisdn); err != nil || !sameResult(lia.Result, success) || lia.ServerName != scscfName {
		t.Fatalf("LIA = %+v, %v, want DIAMETER_SUCCESS at %s", lia, err, scscfName)
	}

	uaa, err := c.uar(tempIMPU, cx.AuthorizationRegistration)
	if err != nil || !sameResult(uaa.Result, subsequent) || uaa.ServerName != scscfName {
		t.Fatalf("UAA = %+v, %v, want SUBSEQUENT_REGISTRATION at %s", uaa, err, scscfName)
	}

	if _, err := c.sar(cx.AssignmentTimeoutDeregistration); err != nil {
		t.Fatal(err)
	}

	if s := c.state(); s.State != NotRegistered || s.ServerName != "" {
		t.Fatalf("subscriber = %+v, want not registered", s)
	}
}

func TestDeregistrationStoringTheServerName(t *testing.T) {
	for _, typ := range []cx.AssignmentType{
		cx.AssignmentUserDeregistrationStoreServer, cx.AssignmentTimeoutDeregistrationStoreServer,
	} {
		t.Run(typ.String(), func(t *testing.T) {
			c := newClient(t)
			c.register()

			if _, err := c.sar(typ); err != nil {
				t.Fatal(err)
			}

			if s := c.state(); s.State != Unregistered || s.ServerName != scscfName {
				t.Fatalf("subscriber = %+v, want unregistered with the name kept", s)
			}

			if lia, err := c.lir(msisdn); err != nil || !sameResult(lia.Result, success) || lia.ServerName != scscfName {
				t.Fatalf("LIA = %+v, %v, want DIAMETER_SUCCESS at %s", lia, err, scscfName)
			}
		})
	}
}

func TestLocationOfAnUnregisteredUser(t *testing.T) {
	c := newClient(t)

	_, err := c.lir(msisdn)
	wantExperimental(t, err, tgpp.ResultErrorIdentityNotRegistered)

	lia, err := c.lirWith(cx.LocationInfoRequest{PublicIdentity: msisdn, Originating: true})
	if err != nil || !sameResult(lia.Result, unregistered) || lia.ServerName != "" {
		t.Fatalf("originating LIA = %+v, %v, want UNREGISTERED_SERVICE without a name", lia, err)
	}

	c.hss.Update(impi, func(s *Subscriber) { s.UnregisteredServices = true })

	lia, err = c.lir(msisdn)
	if err != nil || !sameResult(lia.Result, unregistered) || lia.ServerName != "" {
		t.Fatalf("LIA = %+v, %v, want UNREGISTERED_SERVICE without a name", lia, err)
	}

	if _, err := c.mar(nil); err != nil {
		t.Fatal(err)
	}

	lia, err = c.lir(msisdn)
	if err != nil || !sameResult(lia.Result, success) || lia.ServerName != scscfName {
		t.Fatalf("LIA = %+v, %v, want DIAMETER_SUCCESS at the assigned %s", lia, err, scscfName)
	}
}

func TestBarred(t *testing.T) {
	c := newClient(t)
	c.hss.Update(impi, func(s *Subscriber) {
		for i := range s.IMPUs {
			s.IMPUs[i].Barred = true
		}
	})

	_, err := c.uar(tempIMPU, cx.AuthorizationRegistration)
	wantBase(t, err, diameter.ResultAuthorizationRejected)
}

func TestAuthenticationScheme(t *testing.T) {
	c := newClient(t)

	for _, scheme := range []cx.AuthenticationScheme{cx.SchemeDigestAKAv2MD5, cx.SchemeUnknown, cx.SchemeSIPDigest} {
		_, err := c.marWith(cx.MultimediaAuthRequest{Scheme: scheme})
		wantExperimental(t, err, tgpp.ResultErrorAuthSchemeNotSupported)
	}
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

	other := make([]byte, milenage.RANDLen)

	auts, _ = milenage.AUTS(k, opc, other, 5000)

	if maa, err = c.mar(&cx.Resync{RAND: v.RAND, AUTS: auts}); err != nil {
		t.Fatal(err)
	}

	if r, err := milenage.Respond(k, opc, akaVector(t, maa).RAND, akaVector(t, maa).AUTN); err != nil || r.SQN != 1003 {
		t.Fatalf("Respond = %+v, %v, want SQN 1003 after an AUTS for another RAND", r, err)
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
	c.hss.Add(Subscriber{IMPI: "bob@" + realm, IMPUs: []cx.ProfileIdentity{{Identity: "sip:bob@" + realm}}})

	stranger := "sip:stranger@" + realm

	_, err := c.uar(stranger, cx.AuthorizationRegistration)
	wantExperimental(t, err, tgpp.ResultErrorUserUnknown)

	_, err = c.uar("sip:bob@"+realm, cx.AuthorizationRegistration)
	wantExperimental(t, err, tgpp.ResultErrorIdentitiesDontMatch)

	_, err = c.lir(stranger)
	wantExperimental(t, err, tgpp.ResultErrorUserUnknown)

	_, err = c.marWith(cx.MultimediaAuthRequest{PrivateIdentity: "nobody@" + realm})
	wantExperimental(t, err, tgpp.ResultErrorUserUnknown)

	_, err = c.marWith(cx.MultimediaAuthRequest{PublicIdentity: "sip:bob@" + realm})
	wantExperimental(t, err, tgpp.ResultErrorIdentitiesDontMatch)

	_, err = c.sarWith(cx.ServerAssignmentRequest{Type: cx.AssignmentRegistration, PublicIdentities: []string{stranger}})
	wantExperimental(t, err, tgpp.ResultErrorUserUnknown)

	_, err = c.sarWith(cx.ServerAssignmentRequest{Type: cx.AssignmentRegistration, PrivateIdentity: "nobody@" + realm})
	wantExperimental(t, err, tgpp.ResultErrorUserUnknown)
}

func TestRTR(t *testing.T) {
	c := newClient(t)
	c.register()

	rta, err := c.hss.RTR(t.Context(), cx.DeregistrationReason{Code: cx.ReasonPermanentTermination}, impi)
	if err != nil {
		t.Fatal(err)
	}

	if len(rta.AssociatedIdentities) != 0 {
		t.Fatalf("RTA = %+v, want no Associated-Identities", rta)
	}

	select {
	case rtr := <-c.rtrs:
		if rtr.PrivateIdentity != impi || rtr.Reason.Code != cx.ReasonPermanentTermination || len(rtr.PublicIdentities) != 0 {
			t.Fatalf("RTR = %+v", rtr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no RTR")
	}

	if s := c.state(); s.State != NotRegistered || s.ServerName != "" {
		t.Fatalf("subscriber = %+v, want not registered after the RTR", s)
	}

	c.register()

	if _, err := c.hss.RTR(t.Context(), cx.DeregistrationReason{Code: cx.ReasonPermanentTermination}, impi, msisdn); err != nil {
		t.Fatal(err)
	}

	if rtr := <-c.rtrs; len(rtr.PublicIdentities) != 1 || rtr.PublicIdentities[0] != msisdn {
		t.Fatalf("RTR = %+v, want %s", rtr, msisdn)
	}

	if s := c.state(); s.State != NotRegistered {
		t.Fatalf("subscriber = %+v, want not registered after the RTR of one IMPU", s)
	}
}

func TestRTRStateChangesFirst(t *testing.T) {
	c := newClient(t)
	c.register()

	release := make(chan struct{})
	c.hold = release

	done := make(chan error, 1)

	go func() {
		_, err := c.hss.RTR(context.Background(), cx.DeregistrationReason{Code: cx.ReasonPermanentTermination}, impi)
		done <- err
	}()

	<-c.rtrs

	if s := c.state(); s.State != NotRegistered {
		t.Fatalf("subscriber = %+v before the RTA, want not registered", s)
	}

	c.register()
	close(release)

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if s := c.state(); s.State != Registered {
		t.Fatalf("subscriber = %+v, want the new registration kept after the RTA", s)
	}
}

func TestRTRWithoutTheIMS(t *testing.T) {
	h := New(t, Config{Realm: realm, IMSHost: imsHost})

	if _, err := h.RTR(t.Context(), cx.DeregistrationReason{}, impi); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("RTR = %v, want ErrNotConnected", err)
	}
}

func TestDrain(t *testing.T) {
	c := newClient(t)
	c.register()

	c.hss.Drain()

	select {
	case r := <-c.hss.Requests():
		t.Fatalf("request %s after Drain", r)
	default:
	}

	if c.hss.Dropped() != 0 {
		t.Fatalf("%d requests dropped", c.hss.Dropped())
	}
}
