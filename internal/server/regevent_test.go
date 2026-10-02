package server

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

const (
	e2eIMPI   = "001010000000001@" + imsRealm
	e2eMSISDN = "sip:+15551230001@" + imsRealm + ";user=phone"
	e2eTel    = "tel:+15551230001"
)

var (
	ueAddr  = netip.MustParseAddr("127.0.0.2")
	e2eXRES = bytes.Repeat([]byte{3}, 8)
)

// e2e runs the IMS with a fake HSS and a UE that registers over IPsec (with a
// fake kernel: the protected ports carry plain SIP).
type e2e struct {
	t      *testing.T
	srv    *Server
	hss    *fakePeer
	sars   chan cx.ServerAssignmentRequest
	pcscf  netip.AddrPort
	ps     netip.AddrPort
	scscf  netip.AddrPort
	name   atomic.Pointer[string]
	uc, us *siptest.Socket
	queued []*sip.Request
	server sip.SecurityMechanism
	callID string
	cseq   int
}

func newE2E(t *testing.T) *e2e {
	t.Helper()

	e := &e2e{t: t, sars: make(chan cx.ServerAssignmentRequest, 16), callID: sip.NewTag() + "@" + ueAddr.String()}

	mux := diameter.NewMux()
	mux.Handle(cx.ApplicationID, cx.CommandUserAuthorization, diameter.HandlerFunc(
		func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			ans, _ := cx.NewUserAuthorizationAnswer(req, c.LocalIdentity(), cx.UserAuthorization{
				Result: tgpp.Experimental(tgpp.ResultFirstRegistration),
			})

			return ans
		}))
	mux.Handle(cx.ApplicationID, cx.CommandMultimediaAuth, diameter.HandlerFunc(
		func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			ans, _ := cx.NewMultimediaAuthAnswer(req, c.LocalIdentity(), cx.MultimediaAuth{Items: []cx.AuthItem{{
				Scheme: cx.SchemeDigestAKAv1MD5,
				AKA: &cx.AKAVector{
					RAND: bytes.Repeat([]byte{1}, 16), AUTN: bytes.Repeat([]byte{2}, 16), XRES: e2eXRES,
					CK: bytes.Repeat([]byte{4}, 16), IK: bytes.Repeat([]byte{5}, 16),
				},
			}}})

			return ans
		}))
	mux.Handle(cx.ApplicationID, cx.CommandServerAssignment, diameter.HandlerFunc(
		func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			sar, err := cx.ParseServerAssignmentRequest(req)
			if err != nil {
				return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
			}

			e.sars <- sar

			var a cx.ServerAssignment

			if sar.Type == cx.AssignmentRegistration || sar.Type == cx.AssignmentReRegistration {
				a.UserData, _ = cx.MarshalUserData(cx.IMSSubscription{
					PrivateIdentity: e2eIMPI,
					ServiceProfiles: []cx.ServiceProfile{{PublicIdentities: []cx.ProfileIdentity{
						{Identity: "sip:" + e2eIMPI, Barred: true},
						{Identity: e2eMSISDN},
						{Identity: e2eTel},
					}}},
				})
			}

			ans, _ := cx.NewServerAssignmentAnswer(req, c.LocalIdentity(), a)

			return ans
		}))
	mux.Handle(cx.ApplicationID, cx.CommandLocationInfo, diameter.HandlerFunc(
		func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			ans, _ := cx.NewLocationInfoAnswer(req, c.LocalIdentity(), cx.LocationInfo{ServerName: *e.name.Load()})

			return ans
		}))

	e.hss = newFakePeerWithHandler(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, mux,
		config.ApplicationCx, config.ApplicationRx)

	cfg := testConfig(t)
	cfg.SIP.Addresses = []netip.Addr{loopback}
	cfg.Diameter = diameterConfig(e.hss.config("hss"))
	cfg.SCSCF.MinExpires = 1
	cfg.SCSCF.MaxExpires = 3600

	e.srv = startIMSWith(t, cfg, ipsectest.NewKernel())
	waitOpen(t, e.srv, "hss")

	e.pcscf = sipListener(t, e.srv, rolePCSCF, loopback)
	e.scscf = sipListener(t, e.srv, roleSCSCF, loopback)

	name := "sip:scscf." + imsRealm + ":" + strconv.Itoa(int(e.scscf.Port()))
	e.name.Store(&name)
	e.uc = siptest.NewSocket(t, netip.AddrPortFrom(ueAddr, 0))
	e.us = siptest.NewSocket(t, netip.AddrPortFrom(ueAddr, 0))

	return e
}

func (e *e2e) contact() string {
	return "sip:ue@" + e.us.Addr().String()
}

func (e *e2e) securityClient() string {
	return fmt.Sprintf("ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;port-c=%d;port-s=%d;alg=hmac-sha-1-96;ealg=null",
		e.uc.Addr().Port(), e.us.Addr().Port())
}

func (e *e2e) request(method, uri string, sentBy netip.AddrPort) *sip.Request {
	e.cseq++

	req := siptest.NewRequest(method, uri, sip.UDP, sentBy)
	req.Header.Set("Call-ID", e.callID)
	req.Header.Set("CSeq", strconv.Itoa(e.cseq)+" "+method)
	req.Header.Set("Contact", "<"+e.contact()+">;+sip.instance=\"<urn:gsma:imei:35622410-483840-0>\";+g.3gpp.smsip")

	return req
}

func (e *e2e) register(expires string, auth string) *sip.Request {
	req := e.request("REGISTER", "sip:"+imsRealm, e.us.Addr())
	req.Header.Set("To", "<"+e2eMSISDN+">")
	req.Header.Set("From", "<"+e2eMSISDN+">;tag="+sip.NewTag())
	req.Header.Add("Expires", expires)
	req.Header.Add("Authorization", auth)
	req.Header.Add("Security-Client", e.securityClient())
	req.Header.Add("Require", "sec-agree")
	req.Header.Add("Proxy-Require", "sec-agree")

	return req
}

func digestAuth(nonce string, response bool) string {
	uri := "sip:" + imsRealm
	if !response {
		return `Digest username="` + e2eIMPI + `", realm="` + imsRealm + `", uri="` + uri + `", nonce="", response=""`
	}

	const nc, cnonce = "00000001", "0a4f113b"

	ha1 := md5Hex([]byte(e2eIMPI+":"+imsRealm+":"), e2eXRES)
	ha2 := md5Hex([]byte("REGISTER:" + uri))
	res := md5Hex([]byte(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2))

	return fmt.Sprintf(`Digest username="%s", realm="%s", uri="%s", nonce="%s", response="%s", algorithm=AKAv1-MD5, `+
		`qop=auth, nc=%s, cnonce="%s"`, e2eIMPI, imsRealm, uri, nonce, res, nc, cnonce)
}

func md5Hex(parts ...[]byte) string {
	h := md5.New()
	for _, p := range parts {
		h.Write(p)
	}

	return hex.EncodeToString(h.Sum(nil))
}

// registerIPsec registers the UE: a challenge, then the authenticated
// REGISTER over the new security associations.
func (e *e2e) registerIPsec(expires string) *sip.Response {
	e.t.Helper()

	e.uc.Send(sip.UDP, e.pcscf, e.register(expires, digestAuth("", false)))

	res := wantResponse(e.t, e.uc, 401, "REGISTER")

	ms, err := res.Header.SecurityMechanisms("Security-Server")
	if err != nil || len(ms) != 1 {
		e.t.Fatalf("Security-Server = %q", res.Header.Get("Security-Server"))
	}

	e.server = ms[0]

	a, _ := sip.ParseAuth(res.Header.Get("WWW-Authenticate"))
	nonce, _ := a.Params.Get("nonce")

	ps, _ := e.server.Params.Get("port-s")
	port, _ := strconv.Atoi(ps)
	e.ps = netip.AddrPortFrom(loopback, uint16(port))

	res = e.protectedRegister(expires, digestAuth(sip.Unquote(nonce), true))

	if sar := next(e.t, e.sars); sar.Type != cx.AssignmentRegistration {
		e.t.Fatalf("SAR %s, want REGISTRATION", sar.Type)
	}

	return res
}

func (e *e2e) protectedRegister(expires, auth string) *sip.Response {
	e.t.Helper()

	req := e.register(expires, auth)
	req.Header.Add("Security-Verify", e.server.String())
	e.uc.Send(sip.UDP, e.ps, req)

	return e.response(200, "REGISTER")
}

// response receives a response on the UE's protected server port, keeping
// the NOTIFYs that overtake it: they take one hop fewer.
func (e *e2e) response(code int, method string) *sip.Response {
	e.t.Helper()

	for {
		r := e.us.Recv()

		if req, ok := r.Msg.(*sip.Request); ok {
			req.Flow = r.Flow
			e.queued = append(e.queued, req)

			continue
		}

		res := r.Msg.(*sip.Response)
		if cseq, _ := res.Header.CSeq(); res.StatusCode != code || cseq.Method != method {
			e.t.Fatalf("got %q to %s, want %d to %s", res.StartLine(), cseq.Method, code, method)
		}

		return res
	}
}

// subscribe sends the UE's reg event SUBSCRIBE over its security
// associations and returns the 200.
func (e *e2e) subscribe(serviceRoute string) *sip.Response {
	e.t.Helper()

	req := e.request("SUBSCRIBE", e2eMSISDN, e.us.Addr())
	req.Header.Set("Call-ID", sip.NewTag()+"@"+ueAddr.String())
	req.Header.Set("To", "<"+e2eMSISDN+">")
	req.Header.Set("From", "<"+e2eMSISDN+">;tag="+sip.NewTag())
	req.Header.Add("Route", serviceRoute)
	req.Header.Add("Event", "reg")
	req.Header.Add("Accept", regevent.ContentType)
	req.Header.Add("Expires", "600000")
	req.Header.Add("P-Preferred-Identity", "<"+e2eTel+">")
	req.Header.Add("Proxy-Require", "sec-agree")
	req.Header.Add("Require", "sec-agree")
	req.Header.Add("Security-Verify", e.server.String())
	e.uc.Send(sip.UDP, e.ps, req)

	return e.response(200, "SUBSCRIBE")
}

// notified receives a NOTIFY on the UE's protected server port, answers it
// and returns its Subscription-State and body.
func (e *e2e) notified() (string, regevent.Reginfo) {
	e.t.Helper()

	var (
		req *sip.Request
		f   sip.Flow
	)

	if len(e.queued) > 0 {
		req, f = e.queued[0], e.queued[0].Flow
		e.queued = e.queued[1:]
	} else {
		req, f = e.us.RecvRequest()
	}

	if req.Method != "NOTIFY" {
		e.t.Fatalf("got %s, want NOTIFY", req.Method)
	}

	if pc, _ := e.server.Params.Get("port-c"); strconv.Itoa(int(f.Remote.Port())) != pc {
		e.t.Fatalf("NOTIFY from %s, want the P-CSCF's protected client port %s", f.Remote, pc)
	}

	info, err := regevent.Decode(req.Body)
	if err != nil {
		e.t.Fatalf("reginfo: %v\n%s", err, req.Body)
	}

	// As the Samsung of the corpus does (ipsec_reg/023), answer from the
	// protected client port to the Via's port.
	via, err := req.Header.TopVia()
	if err != nil {
		e.t.Fatal(err)
	}

	if f.Transport == sip.UDP {
		e.uc.Send(sip.UDP, netip.AddrPortFrom(loopback, via.Port), sip.NewResponse(req, 200, ""))
	} else {
		e.us.Send(f.Transport, f.Remote, sip.NewResponse(req, 200, ""))
	}

	return req.Header.Get("Subscription-State"), info
}

func contactEvent(t *testing.T, info regevent.Reginfo, aor string) (string, regevent.Event) {
	t.Helper()

	for _, r := range info.Registrations {
		if r.AOR == aor && len(r.Contacts) == 1 {
			return r.Contacts[0].State, r.Contacts[0].Event
		}
	}

	t.Fatalf("no single contact for %s in %+v", aor, info)

	return "", ""
}

func (e *e2e) subscriptions() []db.RegSubscription {
	e.t.Helper()

	subs, err := e.srv.database.ListRegSubscriptions(context.Background(), e2eIMPI)
	if err != nil {
		e.t.Fatal(err)
	}

	return subs
}

func (e *e2e) pcscfRegistrations() []db.PCSCFRegistration {
	e.t.Helper()

	regs, err := e.srv.database.ListPCSCFRegistrations(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}

	return regs
}

func (e *e2e) subscriber(s db.Subscriber) (db.RegSubscription, bool) {
	for _, sub := range e.subscriptions() {
		if sub.Subscriber == s {
			return sub, true
		}
	}

	return db.RegSubscription{}, false
}

// setUp registers the UE and has both the UE and the P-CSCF subscribed.
func (e *e2e) setUp(expires string) {
	e.t.Helper()

	res := e.registerIPsec(expires)

	eventually(e.t, "the P-CSCF's subscription", func() bool {
		s, ok := e.subscriber(db.SubscriberPCSCF)
		return ok && s.Version >= 0
	})

	e.subscribe(res.Header.Get("Service-Route"))

	state, info := e.notified()
	if !strings.HasPrefix(state, "active") {
		e.t.Fatalf("Subscription-State = %q", state)
	}

	if st, ev := contactEvent(e.t, info, e2eMSISDN); st != regevent.Active || ev != regevent.Registered {
		e.t.Fatalf("contact %s/%s, want active/registered", st, ev)
	}

	if regs := e.pcscfRegistrations(); len(regs) != 1 || len(regs[0].AssociatedURIs) != 2 {
		e.t.Fatalf("P-CSCF registrations = %+v", regs)
	}
}

func TestRegEventThroughTheRoles(t *testing.T) {
	e := newE2E(t)
	e.setUp("600")

	ue, _ := e.subscriber(db.SubscriberUE)
	if ue.IMPU != e2eMSISDN {
		t.Fatalf("UE subscription = %+v", ue)
	}

	pcscf, _ := e.subscriber(db.SubscriberPCSCF)

	e.protectedRegister("600", `Digest username="`+e2eIMPI+`", realm="`+imsRealm+`", uri="sip:`+imsRealm+
		`", nonce="", response=""`)

	state, info := e.notified()
	if st, ev := contactEvent(t, info, e2eTel); !strings.HasPrefix(state, "active") || st != regevent.Active ||
		ev != regevent.Refreshed {
		t.Fatalf("re-registration NOTIFY %q %s/%s, want active refreshed", state, st, ev)
	}

	eventually(t, "the re-registration NOTIFY to the P-CSCF", func() bool {
		s, ok := e.subscriber(db.SubscriberPCSCF)
		return ok && s.Version > pcscf.Version
	})
}

func TestRegEventServerChange(t *testing.T) {
	e := newE2E(t)
	e.setUp("600")

	rtr, err := cx.NewRegistrationTerminationRequest(e.hss.envelope(), cx.RegistrationTerminationRequest{
		PrivateIdentity: e2eIMPI,
		Reason:          cx.DeregistrationReason{Code: cx.ReasonServerChange},
	})
	if r := e.hss.send(t, rtr, err); r.Code != diameter.ResultSuccess {
		t.Fatalf("RTA result = %s", r)
	}

	state, info := e.notified()
	if st, ev := contactEvent(t, info, e2eMSISDN); state != "terminated;reason=deactivated" ||
		st != regevent.Terminated || ev != regevent.Deactivated {
		t.Fatalf("NOTIFY %q %s/%s, want terminated deactivated", state, st, ev)
	}

	eventually(t, "both subscriptions and the P-CSCF's registration to end", func() bool {
		return len(e.subscriptions()) == 0 && len(e.pcscfRegistrations()) == 0
	})

	e.wantSAsShortened()
}

func (e *e2e) wantSAsShortened() {
	e.t.Helper()

	eventually(e.t, "the security associations to be shortened", func() bool {
		sas, err := e.srv.database.ListSecurityAssociations(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}

		for _, sa := range sas {
			if time.Until(sa.ExpiresAt) > 200*time.Second {
				return false
			}
		}

		return len(sas) > 0
	})
}

func TestRegEventExpiry(t *testing.T) {
	e := newE2E(t)
	e.setUp("2")

	state, info := e.notified()
	if st, ev := contactEvent(t, info, e2eMSISDN); state != "terminated;reason=noresource" ||
		st != regevent.Terminated || ev != regevent.Expired {
		t.Fatalf("NOTIFY %q %s/%s, want terminated expired", state, st, ev)
	}

	if sar := next(t, e.sars); sar.Type != cx.AssignmentTimeoutDeregistration {
		t.Fatalf("SAR %s, want TIMEOUT_DEREGISTRATION", sar.Type)
	}

	eventually(t, "the P-CSCF's registration to end", func() bool {
		return len(e.subscriptions()) == 0 && len(e.pcscfRegistrations()) == 0
	})

	e.wantSAsShortened()
}

func TestRegEventUEDeregistration(t *testing.T) {
	e := newE2E(t)
	e.setUp("600")

	req := e.register("0", `Digest username="`+e2eIMPI+`", realm="`+imsRealm+`", uri="sip:`+imsRealm+
		`", nonce="", response=""`)
	req.Header.Add("Security-Verify", e.server.String())
	e.uc.Send(sip.UDP, e.ps, req)
	e.response(200, "REGISTER")

	if sar := next(t, e.sars); sar.Type != cx.AssignmentUserDeregistration {
		t.Fatalf("SAR %s, want USER_DEREGISTRATION", sar.Type)
	}

	eventually(t, "the P-CSCF's subscription to end with its NOTIFY", func() bool {
		return len(e.subscriptions()) == 0 && len(e.pcscfRegistrations()) == 0
	})

	e.us.RecvNone(100 * time.Millisecond)
}

func TestRTRMalformed(t *testing.T) {
	e := newE2E(t)

	rtr, err := cx.NewRegistrationTerminationRequest(e.hss.envelope(), cx.RegistrationTerminationRequest{
		PrivateIdentity:  e2eIMPI,
		Reason:           cx.DeregistrationReason{Code: cx.ReasonNewServerAssigned},
		PublicIdentities: []string{e2eTel},
	})
	if err != nil {
		t.Fatal(err)
	}

	rtr.AVPs = slicesDeleteAVP(rtr.AVPs, cx.AVPPublicIdentity)

	if r := e.hss.send(t, rtr, nil); r.Success() {
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
