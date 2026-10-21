package ue

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/milenage"
	"github.com/ellanetworks/ims/internal/uetest"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

func TestInitialRegistration(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUEAt(Config{}, 50)

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)

	if req.URI.String() != "sip:"+domain || req.Header.Get("To") != "<"+impu+">" ||
		!strings.HasPrefix(req.Header.Get("From"), "<"+impu+">;tag=") {
		t.Fatalf("REGISTER %s from %q to %q", req.URI, req.Header.Get("From"), req.Header.Get("To"))
	}

	contacts, err := req.Header.Contacts()
	if err != nil || len(contacts) != 1 {
		t.Fatalf("Contact = %q", req.Header.Values("Contact"))
	}

	c := contacts[0]
	instance, _ := c.Params.Get("+sip.instance")
	icsi, _ := c.Params.Get("+g.3gpp.icsi-ref")

	if instance != `"<urn:gsma:imei:35693803-564380-0>"` || icsi != `"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"` ||
		!c.Params.Has("+g.3gpp.smsip") || !c.Params.Has("audio") || len(c.URI.User) != 36 || c.URI.User[14] != '1' {
		t.Fatalf("Contact = %s", c)
	}

	via, _ := req.Header.TopVia()
	if _, rport := via.RPort(); !rport || f.Remote != u.Unprotected() {
		t.Fatalf("Via = %s from %s, want rport from the unprotected port %s", via, f.Remote, u.Unprotected())
	}

	ps := uetest.AuthParams(t, req)
	if ps["username"] != impi || ps["realm"] != domain || ps["uri"] != "sip:"+domain || ps["nonce"] != "" ||
		ps["response"] != "" || ps["algorithm"] != "AKAv1-MD5" {
		t.Fatalf("Authorization = %q", req.Header.Get("Authorization"))
	}

	clients, _ := req.Header.SecurityMechanisms("Security-Client")

	offers, _, err := ipsec.ParseOffers(clients)
	if err != nil || len(offers) != 2 || offers[0].Integrity != ipsec.HMACSHA196 || offers[1].Integrity != ipsec.HMACMD596 ||
		offers[0].Endpoint != offers[1].Endpoint {
		t.Fatalf("Security-Client = %q", req.Header.Values("Security-Client"))
	}

	if via.Port != u.Unprotected().Port() || c.URI.Port != u.Unprotected().Port() {
		t.Fatalf("Via %s and Contact %s on the unprotected REGISTER, want the unprotected port %d", via, c, u.Unprotected().Port())
	}

	for name, want := range map[string]string{
		"Expires": "600000", "Supported": "path", "Require": "sec-agree", "Proxy-Require": "sec-agree", "P-Access-Network-Info": "",
	} {
		if got := req.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	n.Challenge(req, f, n.PCSCF, true)

	protected, pf := n.Recv(n.PS)

	installed := kernel.Installed()
	if len(installed) != 1 {
		t.Fatalf("installed %v, want one set", installed)
	}

	for set, keys := range installed {
		want := ipsec.Set{
			Local:      offers[0].Endpoint,
			Remote:     ipsec.Endpoint{Addr: loopback, PortC: n.PC.Addr().Port(), PortS: n.PS.Addr().Port(), SPIC: n.SPIC, SPIS: n.SPIS},
			Integrity:  ipsec.HMACMD596,
			Encryption: ipsec.EncryptionNull,
		}
		want.Local.Addr = loopback

		if set != want || !bytes.Equal(keys.CK, n.Vector.CK) || !bytes.Equal(keys.IK, n.Vector.IK) {
			t.Fatalf("installed %s, want %s with the vector's keys", set, want)
		}
	}

	pvia, _ := protected.Header.TopVia()
	pcontacts, _ := protected.Header.Contacts()

	if pvia.Port != offers[0].Endpoint.PortS || len(pcontacts) != 1 || pcontacts[0].URI.Port != offers[0].Endpoint.PortS ||
		pcontacts[0].URI.User != c.URI.User {
		t.Fatalf("Via %s and Contact %q on the protected REGISTER, want port-s %d", pvia, protected.Header.Values("Contact"),
			offers[0].Endpoint.PortS)
	}

	if pf.Remote.Port() != offers[0].Endpoint.PortC || protected.Header.CallID() != req.Header.CallID() {
		t.Fatalf("protected REGISTER from %s with Call-ID %q, want from port-c %d and Call-ID %q",
			pf.Remote, protected.Header.CallID(), offers[0].Endpoint.PortC, req.Header.CallID())
	}

	if cseq, _ := protected.Header.CSeq(); cseq.Seq != 2 {
		t.Fatalf("CSeq = %d, want 2", cseq.Seq)
	}

	ps = uetest.AuthParams(t, protected)
	if ps["nonce"] != n.Nonce || ps["nc"] != "00000001" || ps["cnonce"] == "" || !uetest.Verify(ps, n.Vector.XRES) {
		t.Fatalf("Authorization = %q, want a response with RES", protected.Header.Get("Authorization"))
	}

	if !slices.Equal(protected.Header.Values("Security-Client"), req.Header.Values("Security-Client")) ||
		!slices.Equal(protected.Header.Values("Security-Verify"), n.PCSCFServer(t)) ||
		protected.Header.Get("P-Access-Network-Info") != "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001"+"0000001" {
		t.Fatalf("protected REGISTER:\n%s", protected)
	}

	n.OK(protected, pf, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	st := u.State()
	if !st.Registered || st.DefaultIMPU != msisdn || !st.Barred || len(st.ServiceRoute) != 1 ||
		time.Until(st.Expires) < 3590*time.Second || u.SQN() != n.SQN {
		t.Fatalf("state = %+v, SQN %d", st, u.SQN())
	}

	sas := u.SAs()
	if len(sas) != 1 || sas[0].State != Established || time.Until(sas[0].Expires) < 3620*time.Second {
		t.Fatalf("SAs = %+v, want one established for the registration and 30 s", sas)
	}
}

func TestResync(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUEAt(Config{}, 5000)

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	resync, f := n.Recv(n.PCSCF)

	ps := uetest.AuthParams(t, resync)

	auts, err := base64.StdEncoding.DecodeString(ps["auts"])
	if err != nil {
		t.Fatal(err)
	}

	sqnMS, err := milenage.Resync(testK, testOPc, n.Vector.RAND, auts)
	if err != nil || sqnMS != 5000 || ps["response"] == "" || ps["nonce"] != n.Nonce {
		t.Fatalf("resync REGISTER with AUTS for %d (%v), Authorization %q", sqnMS, err, resync.Header.Get("Authorization"))
	}

	if resync.Header.CallID() != req.Header.CallID() {
		t.Fatal("resync REGISTER with a new Call-ID")
	}

	before, _ := req.Header.SecurityMechanisms("Security-Client")
	after, _ := resync.Header.SecurityMechanisms("Security-Client")
	o1, _ := ipsec.ParseOffer(before[0])
	o2, _ := ipsec.ParseOffer(after[0])

	if o1.Endpoint.SPIC == o2.Endpoint.SPIC || o1.Endpoint.SPIS == o2.Endpoint.SPIS || o1.Endpoint.PortC == o2.Endpoint.PortC ||
		o1.Endpoint.PortS != o2.Endpoint.PortS {
		t.Fatalf("Security-Client %s then %s, want new spi-c, spi-s and port-c", before[0], after[0])
	}

	n.SQN = sqnMS
	n.Challenge(resync, f, n.PCSCF, true)

	protected, pf := n.Recv(n.PS)
	if pf.Remote.Port() != o2.Endpoint.PortC {
		t.Fatalf("protected REGISTER from %s, want port-c %d", pf.Remote, o2.Endpoint.PortC)
	}

	n.OK(protected, pf, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil || u.SQN() != 5001 {
		t.Fatalf("Register = %v with SQN %d, want SQN 5001", err, u.SQN())
	}
}

func TestMACFailure(t *testing.T) {
	n := newNetwork(t)
	n.K = []byte("the wrong K!!!!!")
	u, kernel := n.newUE(Config{})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	failure, f := n.Recv(n.PCSCF)

	ps := uetest.AuthParams(t, failure)
	if ps["response"] != "" || ps["auts"] != "" || ps["nonce"] != n.Nonce {
		t.Fatalf("Authorization = %q, want an empty response without auts", failure.Header.Get("Authorization"))
	}

	n.Reply(n.PCSCF, f, sip.NewResponse(failure, 403, ""))

	err := uetest.Wait(t, done)

	var re *ResponseError
	if !errors.Is(err, ErrNetworkAuthentication) || !errors.As(err, &re) || re.Response.StatusCode != 403 {
		t.Fatalf("Register = %v, want a network authentication failure ending in 403", err)
	}

	if len(kernel.Installed()) != 0 || u.State().Registered {
		t.Fatal("SAs or a registration after a MAC failure")
	}
}

func TestTwoInvalidChallenges(t *testing.T) {
	n := newNetwork(t)
	n.K = []byte("the wrong K!!!!!")
	u, _ := n.newUE(Config{})

	done := uetest.Start(u.Register)

	for range 3 {
		req, f := n.Recv(n.PCSCF)
		n.Challenge(req, f, n.PCSCF, true)
	}

	if err := uetest.Wait(t, done); !errors.Is(err, ErrNetworkAuthentication) {
		t.Fatalf("Register = %v, want ErrNetworkAuthentication after the third invalid challenge", err)
	}

	n.PCSCF.RecvNone(100 * time.Millisecond)
}

func TestNoSecurityServer(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})

	done := uetest.Start(u.Register)

	first, f := n.Recv(n.PCSCF)
	n.Challenge(first, f, n.PCSCF, false)

	again, f := n.Recv(n.PCSCF)
	if again.Header.CallID() == first.Header.CallID() || uetest.AuthParams(t, again)["nonce"] != "" {
		t.Fatalf("REGISTER after a 401 without Security-Server: Call-ID %q, Authorization %q",
			again.Header.CallID(), again.Header.Get("Authorization"))
	}

	n.Challenge(again, f, n.PCSCF, false)

	if err := uetest.Wait(t, done); !errors.Is(err, ErrNoSecurityServer) {
		t.Fatalf("Register = %v, want ErrNoSecurityServer", err)
	}
}

func TestIntervalTooBrief(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Expires: 30 * time.Second})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)

	res := sip.NewResponse(req, 423, "")
	res.Header.Add("Min-Expires", "60")
	n.Reply(n.PCSCF, f, res)

	req, f = n.Recv(n.PCSCF)
	if req.Header.Get("Expires") != "60" {
		t.Fatalf("Expires = %q after a 423, want 60", req.Header.Get("Expires"))
	}

	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	n.OK(req, f, n.PC, 60)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestForbidden(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	n.Reply(n.PC, f, sip.NewResponse(req, 403, ""))

	var re *ResponseError
	if err := uetest.Wait(t, done); !errors.As(err, &re) || re.Response.StatusCode != 403 {
		t.Fatalf("Register = %v, want a 403", err)
	}

	if len(kernel.Installed()) != 0 || len(kernel.Removed()) != 1 || len(u.SAs()) != 0 {
		t.Fatalf("installed %v and removed %v, want the temporary SAs deleted", kernel.Installed(), kernel.Removed())
	}
}

func TestReRegistrationWithoutChallenge(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})
	n.Register(u)

	established := u.SAs()[0]
	auth := ""

	done := uetest.Start(u.Reregister)

	req, f := n.Recv(n.PS)

	if f.Remote.Port() != established.Set.Local.PortC {
		t.Fatalf("re-REGISTER from %s, want the established port-c %d", f.Remote, established.Set.Local.PortC)
	}

	clients, _ := req.Header.SecurityMechanisms("Security-Client")
	o, _ := ipsec.ParseOffer(clients[0])

	if o.Endpoint.PortS != established.Set.Local.PortS || o.Endpoint.PortC == established.Set.Local.PortC ||
		o.Endpoint.SPIC == established.Set.Local.SPIC || o.Endpoint.SPIS == established.Set.Local.SPIS {
		t.Fatalf("Security-Client %s, want a new port-c, spi-c and spi-s with port-s %d", clients[0], established.Set.Local.PortS)
	}

	if ps := uetest.AuthParams(t, req); ps["nonce"] != n.Nonce || !uetest.Verify(ps, n.Vector.XRES) {
		t.Fatalf("Authorization = %q, want the last one", req.Header.Get("Authorization"))
	}

	auth = req.Header.Get("Authorization")

	if !slices.Equal(req.Header.Values("Security-Verify"), n.PCSCFServer(t)) || req.Header.Get("P-Access-Network-Info") == "" {
		t.Fatalf("re-REGISTER:\n%s", req)
	}

	n.OK(req, f, n.PC, 1200)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	sas := u.SAs()
	if len(sas) != 1 || sas[0].Set != established.Set || len(kernel.Installed()) != 1 {
		t.Fatalf("SAs = %+v, want the established set kept", sas)
	}

	done = uetest.Start(u.Reregister)

	req, f = n.Recv(n.PS)
	if req.Header.Get("Authorization") != auth {
		t.Fatalf("Authorization = %q, want %q", req.Header.Get("Authorization"), auth)
	}

	n.OK(req, f, n.PC, 1200)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestReAuthentication(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})
	n.Register(u)

	old := u.SAs()[0]

	done := uetest.Start(u.Reregister)

	req, f := n.Recv(n.PS)
	n.Challenge(req, f, n.PC, true)

	protected, pf := n.Recv(n.PS)

	clients, _ := req.Header.SecurityMechanisms("Security-Client")
	o, _ := ipsec.ParseOffer(clients[0])

	if pf.Remote.Port() != o.Endpoint.PortC {
		t.Fatalf("REGISTER after the re-authentication from %s, want the new port-c %d", pf.Remote, o.Endpoint.PortC)
	}

	if ps := uetest.AuthParams(t, protected); ps["nonce"] != n.Nonce || !uetest.Verify(ps, n.Vector.XRES) {
		t.Fatalf("Authorization = %q", protected.Header.Get("Authorization"))
	}

	if len(kernel.Installed()) != 2 {
		t.Fatalf("installed %v, want the old and the new sets", kernel.Installed())
	}

	if n.PC.Addr().Port() == old.Set.Remote.PortC {
		t.Fatal("the new set has the old one's port-c")
	}

	n.OK(protected, pf, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	sas := u.SAs()
	if len(sas) != 2 || sas[0].State != Old || sas[0].Set != old.Set || sas[1].State != Established ||
		sas[1].Set.Local.PortC != o.Endpoint.PortC || len(kernel.Installed()) != 2 {
		t.Fatalf("SAs = %+v, want the old set kept beside the new one", sas)
	}

	n.Request(n.PCs[0], toIPsec(old.Set))

	if len(u.SAs()) != 2 {
		t.Fatalf("SAs = %+v after a request through the old set, want both", u.SAs())
	}

	n.Request(n.PC, toIPsec(sas[1].Set))

	sas = u.SAs()
	if len(sas) != 1 || sas[0].State != Established || !slices.Contains(kernel.Removed(), toIPsec(old.Set)) {
		t.Fatalf("SAs = %+v, removed %v, want the old set deleted", sas, kernel.Removed())
	}
}

func TestDeregistration(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})
	n.Register(u)

	done := uetest.Start(u.Deregister)

	req, f := n.Recv(n.PS)
	if req.Header.Get("Expires") != "0" {
		t.Fatalf("Expires = %q, want 0", req.Header.Get("Expires"))
	}

	n.Reply(n.PC, f, sip.NewResponse(req, 200, ""))

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	if u.State().Registered || len(u.SAs()) != 0 || len(kernel.Installed()) != 0 {
		t.Fatalf("state %+v and SAs %v after deregistration", u.State(), kernel.Installed())
	}

	if err := u.Reregister(t.Context()); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("Reregister = %v, want ErrNotRegistered", err)
	}
}

func TestPlainRegistration(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Plain: true})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	if req.Header.Has("Security-Client") || req.Header.Has("Require") || req.Header.Has("Proxy-Require") {
		t.Fatalf("plain REGISTER:\n%s", req)
	}

	n.Challenge(req, f, n.PCSCF, false)

	req, f = n.Recv(n.PCSCF)
	if f.Remote != u.Unprotected() || !uetest.Verify(uetest.AuthParams(t, req), n.Vector.XRES) {
		t.Fatalf("answer from %s:\n%s", f.Remote, req)
	}

	n.OK(req, f, n.PCSCF, 3600)

	if err := uetest.Wait(t, done); err != nil || !u.State().Registered {
		t.Fatalf("Register = %v, state %+v", err, u.State())
	}

	done = uetest.Start(u.Reregister)

	req, f = n.Recv(n.PCSCF)
	n.OK(req, f, n.PCSCF, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestAutomaticReRegistration(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	n.OK(req, f, n.PC, 2)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	req, f = n.Recv(n.PS)
	if req.Header.Get("Expires") != "600000" {
		t.Fatalf("automatic re-REGISTER:\n%s", req)
	}

	n.OK(req, f, n.PC, 3600)

	deadline := time.Now().Add(5 * time.Second)
	for time.Until(u.State().Expires) < time.Hour-time.Minute {
		if time.Now().After(deadline) {
			t.Fatalf("state %+v, want the refreshed expiry", u.State())
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func TestExpiry(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})
	u.SetAutoReregister(false)

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	n.OK(req, f, n.PC, 1)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1500 * time.Millisecond)

	if u.State().Registered {
		t.Fatal("registered after the expiry")
	}

	n.PS.RecvNone(100 * time.Millisecond)
}

// TS 23.003 §13.2–§13.4B: the identities of a UE without an ISIM.
func TestIdentitiesFromIMSI(t *testing.T) {
	for _, tc := range []struct {
		imsi      string
		mncLength int
		want      Identities
	}{
		{
			"310150123456789", 3,
			Identities{
				IMPI:       "310150123456789@ims.mnc150.mcc310.3gppnetwork.org",
				IMPU:       "sip:310150123456789@ims.mnc150.mcc310.3gppnetwork.org",
				HomeDomain: "ims.mnc150.mcc310.3gppnetwork.org",
			},
		},
		{
			"234150999999999", 2,
			Identities{
				IMPI:       "234150999999999@ims.mnc015.mcc234.3gppnetwork.org",
				IMPU:       "sip:234150999999999@ims.mnc015.mcc234.3gppnetwork.org",
				HomeDomain: "ims.mnc015.mcc234.3gppnetwork.org",
			},
		},
	} {
		if got, err := IdentitiesFromIMSI(tc.imsi, tc.mncLength); err != nil || got != tc.want {
			t.Errorf("IdentitiesFromIMSI(%s, %d) = %+v, %v, want %+v", tc.imsi, tc.mncLength, got, err, tc.want)
		}
	}

	for _, bad := range []struct {
		imsi      string
		mncLength int
	}{{"12a", 2}, {"310150123456789", 4}} {
		if _, err := IdentitiesFromIMSI(bad.imsi, bad.mncLength); err == nil {
			t.Errorf("IdentitiesFromIMSI(%s, %d) accepted", bad.imsi, bad.mncLength)
		}
	}
}

// New refuses a UE without the identities, instance ID, Authenticator, IPsec or access network info it needs.
func TestNewChecksTheConfig(t *testing.T) {
	id := Identities{IMPI: "alice@example.org", IMPU: "sip:alice@example.org", HomeDomain: "example.org"}
	instance, _ := IMEIInstanceID(imei)
	full := Config{
		Identities: id, InstanceID: instance, AKA: simAKA{}, IPsec: kernelIPsec{}, AccessNetworkInfo: accessNetworkInfo,
		PCSCF: netip.AddrPortFrom(loopback, 5060), Local: loopback,
	}

	for name, change := range map[string]func(*Config){
		"identities":          func(c *Config) { c.Identities.IMPU = "" },
		"instance ID":         func(c *Config) { c.InstanceID = "" },
		"Authenticator":       func(c *Config) { c.AKA = nil },
		"IPsec":               func(c *Config) { c.IPsec = nil },
		"access network info": func(c *Config) { c.AccessNetworkInfo = "" },
	} {
		cfg := full
		change(&cfg)

		if u, err := New(cfg); err == nil {
			_ = u.Close()

			t.Errorf("New without the %s succeeded", name)
		}
	}

	u, err := New(full)
	if err != nil {
		t.Fatal(err)
	}

	_ = u.Close()
}

func TestRequestsOutsideTheSAsAreDropped(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})
	n.Register(u)

	sa := u.SAs()[0]
	stranger := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	stranger.Send(sip.UDP, u.Unprotected(), n.Options(u.Unprotected().Port()))
	stranger.Send(sip.UDP, netip.AddrPortFrom(loopback, sa.Set.Local.PortS), n.Options(sa.Set.Local.PortS))
	stranger.RecvNone(200 * time.Millisecond)

	n.PS.RecvNone(200 * time.Millisecond)

	n.Request(n.PC, toIPsec(sa.Set))

	for {
		select {
		case e := <-u.Events():
			if e.Request != nil && e.Request.Method == "OPTIONS" {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("no OPTIONS event")
		}
	}
}

func TestClientPortsAreReused(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})
	n.Register(u)

	ports := map[uint16]bool{u.SAs()[0].Set.Local.PortC: true}

	for range 5 {
		done := uetest.Start(u.Reregister)

		req, f := n.Recv(n.PS)

		clients, _ := req.Header.SecurityMechanisms("Security-Client")
		o, _ := ipsec.ParseOffer(clients[0])

		if o.Endpoint.PortC == f.Remote.Port() {
			t.Fatalf("Security-Client port-c %d is the established set's", o.Endpoint.PortC)
		}

		ports[o.Endpoint.PortC] = true

		n.OK(req, f, n.PC, 3600)

		if err := uetest.Wait(t, done); err != nil {
			t.Fatal(err)
		}
	}

	if len(ports) != 2 || len(u.clientPorts) != 2 {
		t.Fatalf("port-c values %v and listeners %v over five re-registrations, want two", ports, u.clientPorts)
	}
}

func TestProtectedRegisterChallengedAgain(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	first, f := n.Recv(n.PS)
	n.Challenge(first, f, n.PC, true)

	second, f := n.Recv(n.PS)

	if !slices.Equal(second.Header.Values("Security-Client"), req.Header.Values("Security-Client")) ||
		!slices.Equal(second.Header.Values("Security-Verify"), n.PCSCFServer(t)) || uetest.AuthParams(t, second)["nonce"] != n.Nonce {
		t.Fatalf("REGISTER after the second challenge:\n%s", second)
	}

	n.OK(second, f, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	sas := u.SAs()
	if len(sas) != 1 || len(kernel.Installed()) != 1 || len(kernel.Removed()) != 1 || sas[0].Set.Remote.SPIC != n.SPIC {
		t.Fatalf("SAs = %+v, removed %v, want the second set alone", sas, kernel.Removed())
	}

	c, err := u.newClient(0)
	if err != nil || c.spiC == sas[0].Set.Local.SPIC || c.spiS == sas[0].Set.Local.SPIS ||
		c.spiC == sas[0].Set.Local.SPIS || c.spiS == sas[0].Set.Local.SPIC {
		t.Fatalf("new client %+v, %v beside %s", c, err, sas[0].Set)
	}
}

func TestUnusableSecurityServer(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})

	done := uetest.Start(u.Register)

	for range 2 {
		req, f := n.Recv(n.PCSCF)

		n.NewVector()

		res := sip.NewResponse(req, 401, "")
		res.Header.Add("WWW-Authenticate", n.WWWAuthenticate())
		res.Header.Add("Security-Server", "ipsec-3gpp;prot=esp;mod=trans;alg=hmac-md5-96")
		n.Reply(n.PCSCF, f, res)
	}

	if err := uetest.Wait(t, done); !errors.Is(err, ErrNoSecurityServer) {
		t.Fatalf("Register = %v, want ErrNoSecurityServer after two unusable Security-Servers", err)
	}
}

func TestCloseDuringRegistration(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)
	n.Recv(n.PS)

	if err := u.Close(); err != nil {
		t.Fatal(err)
	}

	if err := uetest.Wait(t, done); err == nil {
		t.Fatal("Register succeeded after Close")
	}

	if len(kernel.Installed()) != 0 || len(u.SAs()) != 0 {
		t.Fatalf("installed %v after Close", kernel.Installed())
	}
}

func TestZeroExpiry(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	n.OK(req, f, n.PC, 0)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	if u.State().Registered || len(kernel.Installed()) != 0 {
		t.Fatalf("state %+v and SAs %v after a 200 granting no time", u.State(), kernel.Installed())
	}

	n.PS.RecvNone(200 * time.Millisecond)
}

func TestTCPRegistration(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Transport: sip.TCP})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	if f.Transport != sip.TCP || f.Remote != u.Unprotected() {
		t.Fatalf("REGISTER over %s, want TCP from the unprotected port %s", f, u.Unprotected())
	}

	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)

	via, _ := req.Header.TopVia()
	if f.Transport != sip.TCP || via.Transport != sip.TCP {
		t.Fatalf("protected REGISTER over %s with Via %s, want TCP", f, via)
	}

	n.OK(req, f, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	done = uetest.Start(u.Reregister)

	req, f = n.Recv(n.PS)
	if f.Transport != sip.TCP || f.Remote.Port() != u.SAs()[0].Set.Local.PortC {
		t.Fatalf("re-REGISTER over %s, want TCP from port_uc", f)
	}

	n.OK(req, f, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestIPv6Registration(t *testing.T) {
	ipv6 := netip.MustParseAddr("::1")
	n := newNetworkAt(t, ipv6)
	u, kernel := n.newUE(Config{})

	n.Register(u)

	for set := range kernel.Installed() {
		if set.Local.Addr != ipv6 || set.Remote.Addr != ipv6 {
			t.Fatalf("set %s, want IPv6", set)
		}
	}

	n.Request(n.PC, toIPsec(u.SAs()[0].Set))
}

func TestAESCBC(t *testing.T) {
	n := newNetwork(t)
	n.Ealg = "aes-cbc"
	u, kernel := n.newUE(Config{Offers: []Offer{{HMACSHA196, EncryptionAESCBC}}})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	if v := req.Header.Get("Security-Client"); !strings.Contains(v, "alg=hmac-sha-1-96") || !strings.Contains(v, "ealg=aes-cbc") {
		t.Fatalf("Security-Client = %q", v)
	}

	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	n.OK(req, f, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	for set, keys := range kernel.Installed() {
		if set.Integrity != ipsec.HMACSHA196 || set.Encryption != ipsec.AESCBC || !bytes.Equal(keys.CK, n.Vector.CK) {
			t.Fatalf("set %s, want hmac-sha-1-96/aes-cbc keyed with CK", set)
		}
	}
}

func TestResyncDuringReRegistration(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})
	n.Register(u)

	established := u.SAs()[0].Set
	sqnMS := u.SQN()

	done := uetest.Start(u.Reregister)

	req, f := n.Recv(n.PS)

	n.SQN = 10
	n.Challenge(req, f, n.PC, true)

	resync, f := n.Recv(n.PS)
	if f.Remote.Port() != established.Local.PortC {
		t.Fatalf("resync REGISTER from %s, want the established port_uc %d", f.Remote, established.Local.PortC)
	}

	auts, _ := base64.StdEncoding.DecodeString(uetest.AuthParams(t, resync)["auts"])
	if got, err := milenage.Resync(testK, testOPc, n.Vector.RAND, auts); err != nil || got != sqnMS {
		t.Fatalf("AUTS for %d, %v, want %d", got, err, sqnMS)
	}

	if slices.Equal(resync.Header.Values("Security-Client"), req.Header.Values("Security-Client")) {
		t.Fatal("resync REGISTER with the same Security-Client")
	}

	n.SQN = sqnMS
	n.Challenge(resync, f, n.PC, true)

	req, f = n.Recv(n.PS)
	n.OK(req, f, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	if sas := u.SAs(); len(sas) != 2 || sas[1].State != Established || sas[0].Set != established {
		t.Fatalf("SAs = %+v, want the new set beside the old one", sas)
	}
}

func TestMACFailureDuringReRegistration(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})
	n.Register(u)

	established := u.SAs()[0].Set

	done := uetest.Start(u.Reregister)

	req, f := n.Recv(n.PS)

	n.K = []byte("the wrong K!!!!!")
	n.Challenge(req, f, n.PC, true)

	failure, f := n.Recv(n.PS)
	if ps := uetest.AuthParams(t, failure); ps["response"] != "" || ps["auts"] != "" || f.Remote.Port() != established.Local.PortC {
		t.Fatalf("REGISTER from %s with Authorization %q, want an empty response over the established SAs",
			f.Remote, failure.Header.Get("Authorization"))
	}

	n.Reply(n.PC, f, sip.NewResponse(failure, 403, ""))

	if err := uetest.Wait(t, done); !errors.Is(err, ErrNetworkAuthentication) {
		t.Fatalf("Reregister = %v, want ErrNetworkAuthentication", err)
	}

	if sas := u.SAs(); len(sas) != 1 || sas[0].Set != established || !u.State().Registered {
		t.Fatalf("SAs = %+v, state %+v, want the registration and its SAs kept", sas, u.State())
	}
}

func TestIntervalTooBriefOnReRegistration(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Expires: 30 * time.Second})
	u.SetAutoReregister(false)
	n.Register(u)

	done := uetest.Start(u.Reregister)

	req, f := n.Recv(n.PS)

	res := sip.NewResponse(req, 423, "")
	res.Header.Add("Min-Expires", "60")
	n.Reply(n.PC, f, res)

	req, f = n.Recv(n.PS)
	if req.Header.Get("Expires") != "60" || f.Remote.Port() != u.SAs()[0].Set.Local.PortC {
		t.Fatalf("re-REGISTER after a 423 from %s with Expires %q", f.Remote, req.Header.Get("Expires"))
	}

	n.OK(req, f, n.PC, 60)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestChallengedDeregistration(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})
	n.Register(u)

	done := uetest.Start(u.Deregister)

	req, f := n.Recv(n.PS)
	n.Challenge(req, f, n.PC, true)

	req, f = n.Recv(n.PS)
	if req.Header.Get("Expires") != "0" || !uetest.Verify(uetest.AuthParams(t, req), n.Vector.XRES) {
		t.Fatalf("deregistration after the challenge:\n%s", req)
	}

	n.Reply(n.PC, f, sip.NewResponse(req, 200, ""))

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	if len(kernel.Installed()) != 0 || len(u.SAs()) != 0 || u.State().Registered {
		t.Fatalf("SAs %v and state %+v after deregistration", kernel.Installed(), u.State())
	}
}

func TestTemporarySAsExpire(t *testing.T) {
	saved := regAwaitAuth
	regAwaitAuth = 200 * time.Millisecond

	t.Cleanup(func() { regAwaitAuth = saved })

	n := newNetwork(t)
	u, kernel := n.newUE(Config{})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)
	n.Recv(n.PS)

	deadline := time.Now().Add(5 * time.Second)
	for len(kernel.Installed()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("temporary SAs %v still installed after reg-await-auth", kernel.Installed())
		}

		time.Sleep(10 * time.Millisecond)
	}

	if err := u.Close(); err != nil {
		t.Fatal(err)
	}

	if err := uetest.Wait(t, done); err == nil {
		t.Fatal("Register succeeded without an answer")
	}
}

func TestReregisterIn(t *testing.T) {
	for remaining, want := range map[time.Duration]time.Duration{
		3600 * time.Second: 3000 * time.Second,
		1201 * time.Second: 601 * time.Second,
		1200 * time.Second: 600 * time.Second,
		600 * time.Second:  300 * time.Second,
		2 * time.Second:    time.Second,
	} {
		if got := reregisterIn(remaining); got != want {
			t.Errorf("reregisterIn(%s) = %s, want %s", remaining, got, want)
		}
	}
}

func TestRegisteredIMPUNotBarred(t *testing.T) {
	n := newNetwork(t)
	n.Associated = "<" + impu + ">, <" + msisdn + ">"
	u, _ := n.newUE(Config{})

	n.Register(u)

	if st := u.State(); st.Barred || st.DefaultIMPU != impu {
		t.Fatalf("state = %+v, want %s registered and the default IMPU", st, impu)
	}
}

func TestInstanceID(t *testing.T) {
	for _, imei := range []string{"35693803564380", "356938035643809"} {
		if got, err := IMEIInstanceID(imei); err != nil || got != "urn:gsma:imei:35693803-564380-0" {
			t.Errorf("IMEIInstanceID(%s) = %q, %v", imei, got, err)
		}
	}

	for _, imei := range []string{"3569380356438", "3569380356438a", "3569380356438090"} {
		if _, err := IMEIInstanceID(imei); err == nil {
			t.Errorf("IMEIInstanceID(%s) accepted", imei)
		}
	}
}
