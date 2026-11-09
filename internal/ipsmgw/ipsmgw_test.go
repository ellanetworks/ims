package ipsmgw

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/reginfo"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel/trace"
)

const home = "ims.mnc001.mcc001.3gppnetwork.org"

type fakeStore struct {
	mu    sync.Mutex
	regs  map[string]db.IPSMGWRegistration
	hss   map[string]db.IPSMGWHSSRegistration
	onPut func(ctx context.Context)
}

func newFakeStore() *fakeStore {
	return &fakeStore{regs: map[string]db.IPSMGWRegistration{}, hss: map[string]db.IPSMGWHSSRegistration{}}
}

func (s *fakeStore) IPSMGWRegistrations(context.Context) ([]db.IPSMGWRegistration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []db.IPSMGWRegistration
	for _, r := range s.regs {
		out = append(out, r)
	}

	return out, nil
}

func (s *fakeStore) PutIPSMGWRegistration(ctx context.Context, r db.IPSMGWRegistration) error {
	if s.onPut != nil {
		s.onPut(ctx)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.regs[r.IMPUKey] = r

	return nil
}

func (s *fakeStore) DeleteIPSMGWRegistration(_ context.Context, k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.regs, k)

	return nil
}

func (s *fakeStore) IPSMGWHSSRegistrations(context.Context) ([]db.IPSMGWHSSRegistration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []db.IPSMGWHSSRegistration
	for _, r := range s.hss {
		out = append(out, r)
	}

	return out, nil
}

func (s *fakeStore) PutIPSMGWHSSRegistration(_ context.Context, r db.IPSMGWHSSRegistration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.hss[r.UserKey] = r

	return nil
}

func (s *fakeStore) DeleteIPSMGWHSSRegistration(_ context.Context, k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.hss, k)

	return nil
}

func (s *fakeStore) reg(k string) db.IPSMGWRegistration {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.regs[k]
}

func (s *fakeStore) waitEmpty(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for {
		s.mu.Lock()
		regs, hss := len(s.regs), len(s.hss)
		s.mu.Unlock()

		if regs == 0 && hss == 0 {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("kept %d registrations and %d HSS registrations", regs, hss)
		}

		time.Sleep(time.Millisecond)
	}
}

type update struct {
	msisdn, impu, number string
}

type fakeHSS struct {
	mu      sync.Mutex
	updates []update
	fail    bool
	reject  bool
	calls   int
	to      []db.HSS
}

var answeringHSS = db.HSS{Host: "hss1.example.org", Realm: "example.org"}

func (h *fakeHSS) UpdateSMSRegistration(_ context.Context, u User, number string, to db.HSS) (db.HSS, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.calls++
	h.to = append(h.to, to)

	if h.fail {
		return db.HSS{}, errors.New("HSS down")
	}

	if h.reject {
		return db.HSS{}, fmt.Errorf("%w: user unknown", ErrRejected)
	}

	impu := ""
	if u.MSISDN == "" {
		impu = u.IMPU.String()
	}

	h.updates = append(h.updates, update{u.MSISDN, impu, number})

	return answeringHSS, nil
}

func (h *fakeHSS) addressed() []db.HSS {
	h.mu.Lock()
	defer h.mu.Unlock()

	return slices.Clone(h.to)
}

func (h *fakeHSS) waitUpdates(t *testing.T, want ...update) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for {
		h.mu.Lock()
		got := slices.Clone(h.updates)
		h.mu.Unlock()

		if slices.Equal(got, want) {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("HSS updates = %+v, want %+v", got, want)
		}

		time.Sleep(time.Millisecond)
	}
}

type fakeSMSCs struct {
	mu        sync.Mutex
	none      bool
	forwarded []sgd.MOForwardShortMessage
	ui        []byte
	err       error
}

func (f *fakeSMSCs) Any() bool { return !f.none }

func (f *fakeSMSCs) Forward(_ context.Context, m sgd.MOForwardShortMessage) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if m.ServiceCentreAddress != "15550000000" {
		return nil, ErrUnknownServiceCentre
	}

	f.forwarded = append(f.forwarded, m)

	return f.ui, f.err
}

type world struct {
	t       *testing.T
	n       *siptest.Net
	scscf   *siptest.Peer
	as      netip.AddrPort
	a       *AS
	store   *fakeStore
	hss     *fakeHSS
	smscs   *fakeSMSCs
	router  *fakeRouter
	nodes   *fakeNodes
	cseq    int
	lastSub *sip.Request
}

func newWorld(t *testing.T, store *fakeStore) *world {
	t.Helper()

	return newWorldTraced(t, store, nil)
}

func newWorldTraced(t *testing.T, store *fakeStore, tp trace.TracerProvider) *world {
	t.Helper()

	n := siptest.NewNet(t)
	w := &world{
		t: t, n: n, scscf: n.Peer(), store: store, hss: &fakeHSS{}, smscs: &fakeSMSCs{}, router: &fakeRouter{},
		nodes: &fakeNodes{answers: map[string]fakeAnswer{}},
	}
	w.as = netip.AddrPortFrom(w.scscf.Addr().Addr(), Port)

	a, err := Start(context.Background(), Config{
		Addresses: []netip.Addr{w.scscf.Addr().Addr()}, Transaction: n.Config(transaction.Config{TracerProvider: tp}),
		HomeDomain: home, PLMN: "001001",
		Store: store, HSS: w.hss, SMSCs: w.smscs, Router: w.router, Nodes: w.nodes,
		Number: func() string { return "15550000100" },
	})
	if err != nil {
		t.Fatal(err)
	}

	w.a = a

	t.Cleanup(func() {
		n.Await(func() { _ = a.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1)
	})

	return w
}

func (w *world) register(impu string, expires int, contentType, body string) *sip.Response {
	w.t.Helper()

	return w.registerWith(impu, "", fmt.Sprintf("Expires: %d", expires), contentType, body)
}

func (w *world) registerWith(impu, params, header, contentType, body string) *sip.Response {
	w.t.Helper()

	w.cseq++

	msg := fmt.Sprintf("REGISTER sip:ipsmgw.%s SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP %s;branch=z9hG4bK-reg%d\r\n"+
		"Max-Forwards: 70\r\n"+
		"From: <sip:scscf.%s>;tag=s1\r\n"+
		"To: <%s>\r\n"+
		"Call-ID: reg-%s\r\n"+
		"CSeq: %d REGISTER\r\n"+
		"Contact: <sip:%s>%s\r\n"+
		"P-Charging-Vector: icid-value=abc;orig-ioi=%s\r\n",
		home, w.scscf.Addr(), w.cseq, home, impu, impu, w.cseq, w.scscf.Addr(), params, home)

	if header != "" {
		msg += strings.ReplaceAll(header, "\n", "\r\n") + "\r\n"
	}

	if contentType != "" {
		msg += "Content-Type: " + contentType + "\r\n"
	}

	msg += fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)

	w.scscf.Send(sip.UDP, w.as, mustParse(w.t, msg))

	res, _ := w.scscf.RecvResponse()

	return res
}

func (w *world) subscribed() *sip.Request {
	w.t.Helper()

	req, _ := w.scscf.RecvRequest()
	if req.Method != "SUBSCRIBE" {
		w.t.Fatalf("got %s, want a SUBSCRIBE", req.Method)
	}

	res := sip.NewResponse(req, 200, "OK")
	to, _ := res.Header.To()
	to.Params.Set("tag", "n1")
	res.Header.Set("To", to.String())
	res.Header.Set("Expires", "600")
	res.Header.Set("Contact", "<sip:"+w.scscf.Addr().String()+">")

	w.scscf.Send(sip.UDP, w.as, res)

	return req
}

func (w *world) notify(sub *sip.Request, state string, doc reginfo.Reginfo) *sip.Response {
	w.t.Helper()

	body, err := reginfo.Encode(doc)
	if err != nil {
		w.t.Fatal(err)
	}

	from, _ := sub.Header.From()
	contacts, _ := sub.Header.Contacts()
	w.cseq++

	msg := fmt.Sprintf("NOTIFY %s SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP %s;branch=z9hG4bK-notify%d\r\n"+
		"Max-Forwards: 70\r\n"+
		"From: %s;tag=n1\r\n"+
		"To: %s\r\n"+
		"Call-ID: %s\r\n"+
		"CSeq: %d NOTIFY\r\n"+
		"Contact: <sip:%s>\r\n"+
		"Event: reg\r\n"+
		"Subscription-State: %s\r\n"+
		"P-Charging-Vector: icid-value=n%d;orig-ioi=%s\r\n"+
		"Content-Type: %s\r\n"+
		"Content-Length: %d\r\n\r\n%s",
		contacts[0].URI, w.scscf.Addr(), w.cseq, sub.Header.Get("To"), from.String(), sub.Header.CallID(), w.cseq,
		w.scscf.Addr(), state, w.cseq, home, reginfo.ContentType, len(body), body)

	w.scscf.Send(sip.UDP, w.as, mustParse(w.t, msg))

	res, _ := w.scscf.RecvResponse()

	return res
}

func mustParse(t *testing.T, s string) sip.Message {
	t.Helper()

	m, err := sip.Parse([]byte(s))
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}

	return m
}

func doc(version uint32, aor string, smsip bool) reginfo.Reginfo {
	c := reginfo.Contact{ID: "c1", State: reginfo.Active, Event: reginfo.Registered, URI: "sip:ue@192.0.2.1:5060"}
	if smsip {
		c.UnknownParams = []reginfo.Param{{Name: "+g.3gpp.smsip"}}
	}

	return reginfo.Reginfo{Version: version, State: reginfo.Full, Registrations: []reginfo.Registration{{
		AOR: aor, ID: "r1", State: reginfo.Active, Contacts: []reginfo.Contact{c},
	}}}
}

const serviceInfo = `<?xml version="1.0"?><ims-3gpp version="1"><service-info>15551230001</service-info></ims-3gpp>`

// TS 24.341 clause 5.3.3.2
func TestRegistrationFollowsAvailability(t *testing.T) {
	w := newWorld(t, newFakeStore())
	impu := "sip:alice@" + home

	if res := w.register(impu, 600, "application/3gpp-ims+xml", serviceInfo); res.StatusCode != 200 ||
		res.Header.Get("Expires") != "600" || !strings.Contains(res.Header.Get("P-Charging-Vector"), "term-ioi="+home) {
		t.Fatalf("REGISTER = %d %v", res.StatusCode, res.Header)
	}

	sub := w.subscribed()
	if sub.URI.String() != impu || sub.Header.Get("Event") != "reg" || !strings.Contains(sub.Header.Get("Route"), ";lr") {
		t.Fatalf("SUBSCRIBE = %s %v", sub.URI, sub.Header)
	}

	if res := w.notify(sub, "active;expires=600", doc(0, impu, false)); res.StatusCode != 200 ||
		!strings.Contains(res.Header.Get("P-Charging-Vector"), "term-ioi="+home) {
		t.Fatalf("NOTIFY = %d %v", res.StatusCode, res.Header)
	}

	w.n.Quiesce()
	w.hss.waitUpdates(t)

	w.notify(sub, "active;expires=600", doc(1, impu, true))
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})

	if got := w.store.reg(key(mustURI(t, impu))); !got.Available || got.MSISDN != "15551230001" {
		t.Fatalf("stored = %+v", got)
	}

	w.notify(sub, "active;expires=600", doc(1, impu, false))
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})

	w.notify(sub, "active;expires=600", doc(2, impu, false))
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"}, update{msisdn: "15551230001"})
}

func TestRegistrationWithoutMSISDN(t *testing.T) {
	w := newWorld(t, newFakeStore())
	impu := "sip:001010000000001@" + home

	ue := "REGISTER sip:" + home + " SIP/2.0\r\nVia: SIP/2.0/UDP 192.0.2.1;branch=z9hG4bK-ue\r\n" +
		"From: <" + impu + ">;tag=u\r\nTo: <" + impu + ">\r\nCall-ID: ue\r\nCSeq: 1 REGISTER\r\n" +
		`Authorization: Digest username="001010000000001@` + home + `", realm="` + home + `", nonce="n", uri="sip:` +
		home + `", response=""` + "\r\nContent-Length: 0\r\n\r\n"

	w.register(impu, 600, "message/sip", ue)

	sub := w.subscribed()
	w.notify(sub, "active;expires=600", doc(0, impu, true))
	w.hss.waitUpdates(t, update{impu: impu, number: "15550000100"})

	if got := w.store.reg(key(mustURI(t, impu))); got.IMSI != "001010000000001" {
		t.Fatalf("stored = %+v", got)
	}
}

func TestDeregistration(t *testing.T) {
	w := newWorld(t, newFakeStore())
	impu := "sip:bob@" + home

	w.register(impu, 600, "application/3gpp-ims+xml", serviceInfo)
	sub := w.subscribed()
	w.notify(sub, "active;expires=600", doc(0, impu, true))
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})

	w.notify(sub, "terminated;reason=deactivated", reginfo.Reginfo{
		Version: 1, State: reginfo.Full,
		Registrations: []reginfo.Registration{{AOR: impu, ID: "r1", State: reginfo.Terminated}},
	})
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"}, update{msisdn: "15551230001"})

	if got := w.hss.addressed(); !slices.Equal(got, []db.HSS{{}, answeringHSS}) {
		t.Fatalf("Sh-Updates addressed %+v, want the realm, then the HSS that answered", got)
	}

	w.n.Quiesce()
	w.store.waitEmpty(t)

	if res := w.register(impu, 0, "", ""); res.StatusCode != 200 {
		t.Fatalf("deregistration = %d", res.StatusCode)
	}
}

func TestRegistrationExpires(t *testing.T) {
	w := newWorld(t, newFakeStore())
	impu := "sip:carol@" + home

	w.register(impu, 60, "application/3gpp-ims+xml", serviceInfo)
	sub := w.subscribed()
	w.notify(sub, "active;expires=600", doc(0, impu, true))
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})

	w.n.Advance(61 * time.Second)
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"}, update{msisdn: "15551230001"})
}

func TestRestore(t *testing.T) {
	store := newFakeStore()
	store.regs["sip:dave@"+home] = db.IPSMGWRegistration{
		IMPUKey: "sip:dave@" + home, IMPU: "sip:dave@" + home, MSISDN: "15551230004", SCSCF: "sip:192.0.2.9",
		AS: "sip:ipsmgw." + home, ExpiresAt: time.Unix(1, 0), Available: true,
	}
	store.hss["msisdn:15551230004"] = db.IPSMGWHSSRegistration{
		UserKey: "msisdn:15551230004", MSISDN: "15551230004", Number: "15550000100",
	}

	w := newWorld(t, store)
	w.hss.waitUpdates(t, update{msisdn: "15551230004"})
	store.waitEmpty(t)
}

func mustURI(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

func TestIMSIOf(t *testing.T) {
	for nai, want := range map[string]string{
		"001010000000001@ims.mnc001.mcc001.3gppnetwork.org":                "001010000000001",
		"234150999999999@ims.mnc015.mcc234.3gppnetwork.org":                "234150999999999",
		"234150999999999@ims.nid000007ed9d5.mnc015.mcc234.3gppnetwork.org": "234150999999999",
		"310410000000001@ims.mnc410.mcc310.3gppnetwork.org":                "310410000000001",
		"alice@ims.mnc001.mcc001.3gppnetwork.org":                          "",
		"999990000000001@ims.mnc001.mcc001.3gppnetwork.org":                "",
		"001010000000001@example.org":                                      "",
	} {
		if got := imsiOf(nai); got != want {
			t.Errorf("imsiOf(%s) = %q, want %q", nai, got, want)
		}
	}
}

func TestSubscribed(t *testing.T) {
	tel := mustURI(t, "tel:+15551230001")
	sipURI := mustURI(t, "sip:alice@"+home)

	if got := subscribed(tel, []sip.URI{tel, sipURI}, home); got.String() != "sip:+15551230001@"+home+";user=phone" {
		t.Fatalf("subscribed(tel) = %s", got)
	}

	if got := subscribed(sipURI, nil, home); !got.Equivalent(sipURI) {
		t.Fatalf("subscribed(sip) = %s", got)
	}
}

// RFC 3261 section 10.3
func TestRegistrationExpiration(t *testing.T) {
	w := newWorld(t, newFakeStore())

	if res := w.registerWith("sip:erin@"+home, ";expires=60", "Expires: 600", "", ""); res.Header.Get("Expires") != "60" {
		t.Fatalf("Expires = %s, want the Contact's", res.Header.Get("Expires"))
	}

	w.subscribed()

	if res := w.registerWith("sip:frank@"+home, "", "", "", ""); res.StatusCode != 200 || res.Header.Get("Expires") != "3600" {
		t.Fatalf("REGISTER = %d %s, want the default", res.StatusCode, res.Header.Get("Expires"))
	}
}

// TS 29.328 clause 6.1.2.1
func TestUserKeyChangeKeepsTheHSSRegistration(t *testing.T) {
	w := newWorld(t, newFakeStore())
	impu := "sip:grace@" + home

	w.register(impu, 600, "", "")

	sub := w.subscribed()
	w.notify(sub, "active;expires=600", doc(0, impu, true))
	w.hss.waitUpdates(t, update{impu: impu, number: "15550000100"})

	w.register(impu, 600, "application/3gpp-ims+xml", serviceInfo)
	w.n.Quiesce()

	w.notify(sub, "active;expires=600", doc(1, impu, false))
	w.hss.waitUpdates(t, update{impu: impu, number: "15550000100"}, update{impu: impu})
}

func TestUserAvailableWhileOneIMPUIs(t *testing.T) {
	w := newWorld(t, newFakeStore())
	a, b := "sip:heidi@"+home, "sip:heidi.work@"+home

	w.register(a, 600, "application/3gpp-ims+xml", serviceInfo)
	subA := w.subscribed()
	w.register(b, 600, "application/3gpp-ims+xml", serviceInfo)
	subB := w.subscribed()

	w.notify(subA, "active;expires=600", doc(0, a, true))
	w.notify(subB, "active;expires=600", doc(0, b, true))
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})

	w.notify(subA, "active;expires=600", doc(1, a, false))
	w.n.Quiesce()
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})

	w.notify(subB, "active;expires=600", doc(1, b, false))
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"}, update{msisdn: "15551230001"})
}

// TS 24.229 clause 5.4.1.8
func TestOtherIMPUOfTheSetTerminated(t *testing.T) {
	w := newWorld(t, newFakeStore())
	impu, other := "sip:ivan@"+home, "sip:ivan.other@"+home

	w.registerWith(impu, "", "Expires: 600\nP-Associated-URI: <"+other+">", "application/3gpp-ims+xml", serviceInfo)

	sub := w.subscribed()
	d := doc(0, impu, true)
	d.Registrations = append(d.Registrations, reginfo.Registration{
		AOR: other, ID: "r2", State: reginfo.Active,
		Contacts: []reginfo.Contact{{ID: "c2", State: reginfo.Active, Event: reginfo.Registered, URI: "sip:ue2@192.0.2.2"}},
	})
	w.notify(sub, "active;expires=600", d)
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})

	d.Version = 1
	d.Registrations[1] = reginfo.Registration{AOR: other, ID: "r2", State: reginfo.Terminated}
	w.notify(sub, "active;expires=600", d)
	w.n.Quiesce()

	if got := w.store.reg(impu); !got.Available {
		t.Fatalf("registration = %+v, want available still", got)
	}

	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})
}

// RFC 6665 section 4.1.3
func TestDeactivatedSubscriptionRenewed(t *testing.T) {
	w := newWorld(t, newFakeStore())
	impu := "sip:judy@" + home

	w.register(impu, 600, "application/3gpp-ims+xml", serviceInfo)
	sub := w.subscribed()

	w.notify(sub, "terminated;reason=deactivated", doc(0, impu, true))

	if again := w.subscribed(); again.Header.CallID() == sub.Header.CallID() {
		t.Fatal("the SUBSCRIBE is not of a new subscription")
	}
}

// RFC 3840 section 9
func TestSMSIPFeatureTag(t *testing.T) {
	for v, want := range map[string]bool{"": true, `"TRUE"`: true, "TRUE": true, `"FALSE"`: false, `"!TRUE"`: false} {
		c := reginfo.Contact{UnknownParams: []reginfo.Param{{Name: "+g.3gpp.smsip", Value: v}}}
		if got := smsip(c); got != want {
			t.Errorf("smsip(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestHSSRefusalNotRetried(t *testing.T) {
	for _, reject := range []bool{true, false} {
		h := &fakeHSS{reject: reject, fail: !reject}
		s := newHSSSync(t.Context(), nil, h, newFakeStore(), func() string { return "15550000100" },
			func(string) (User, bool) { return User{MSISDN: "15551230001"}, true }, nil, slog.New(slog.DiscardHandler))

		s.sync(t.Context(), "msisdn:15551230001", trace.SpanContext{})

		s.mu.Lock()
		retried := s.attempts["msisdn:15551230001"] > 0
		s.mu.Unlock()

		if retried == reject || h.calls != 1 {
			t.Fatalf("reject %v: retried %v after %d calls", reject, retried, h.calls)
		}
	}
}

func TestSuspendKeepsTheHSSRegistration(t *testing.T) {
	store := newFakeStore()
	w := newWorld(t, store)
	impu := "sip:ken@" + home

	w.register(impu, 600, "application/3gpp-ims+xml", serviceInfo)
	sub := w.subscribed()
	w.notify(sub, "active;expires=600", doc(0, impu, true))
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})

	w.n.Await(func() { _ = w.a.Suspend(t.Context()) }, 2*64*transaction.DefaultT1)

	time.Sleep(50 * time.Millisecond)
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})

	if got := store.reg(key(mustURI(t, impu))); !got.Available {
		t.Fatalf("registration = %+v, want kept", got)
	}
}

func TestShutdownDeregisters(t *testing.T) {
	store := newFakeStore()
	w := newWorld(t, store)
	impu := "sip:ken@" + home

	w.register(impu, 600, "application/3gpp-ims+xml", serviceInfo)
	sub := w.subscribed()
	w.notify(sub, "active;expires=600", doc(0, impu, true))
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})

	w.n.Await(func() { _ = w.a.Shutdown(t.Context()) }, 2*64*transaction.DefaultT1)
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"}, update{msisdn: "15551230001"})

	if got := store.reg(key(mustURI(t, impu))); !got.Available {
		t.Fatalf("registration = %+v, want kept", got)
	}

	store.mu.Lock()
	r := store.regs[key(mustURI(t, impu))]
	r.ExpiresAt = r.ExpiresAt.Add(1000 * time.Hour)
	store.regs[r.IMPUKey] = r
	store.mu.Unlock()

	w2 := newWorld(t, store)
	w2.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})

	if got := w.hss.addressed(); got[len(got)-1] != answeringHSS {
		t.Fatalf("the shutdown Sh-Update addressed %+v, want %+v", got[len(got)-1], answeringHSS)
	}
}
