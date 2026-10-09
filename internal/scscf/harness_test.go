package scscf

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/diametertest"
	"github.com/ellanetworks/ims/internal/regmetrics"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	homeDomain = "ims.mnc001.mcc001.3gppnetwork.org"
	imsHost    = "ims." + homeDomain
	hssHost    = "hss." + homeDomain
	scscfName  = "scscf." + homeDomain
	sipPort    = 5060

	testIMPI     = "001010000000001@" + homeDomain
	testIMPU     = "sip:001010000000001@" + homeDomain
	testMSISDN   = "sip:+15551230001@" + homeDomain + ";user=phone"
	testTel      = "tel:+15551230001"
	testAlias    = "sip:alice@" + homeDomain
	secondIMPU   = "sip:second@" + homeDomain
	unknownIMPU  = "sip:unknown@" + homeDomain
	testPath     = "<sip:term@pcscf." + homeDomain + ";lr>"
	testInstance = `"<urn:gsma:imei:35622410-483840-0>"`
)

var (
	loopback  = netip.MustParseAddr("127.0.0.1")
	testEpoch = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	testVector = cx.AKAVector{
		RAND: bytes.Repeat([]byte{0x01}, 16),
		AUTN: bytes.Repeat([]byte{0x02}, 16),
		XRES: bytes.Repeat([]byte{0x03}, 8),
		CK:   bytes.Repeat([]byte{0x04}, 16),
		IK:   bytes.Repeat([]byte{0x05}, 16),
	}
)

type fakeClock struct {
	*siptest.Clock
}

func (c fakeClock) Now() time.Time {
	return testEpoch.Add(c.Clock.Now())
}

type fakeHSS struct {
	id diameter.Identity

	mars chan cx.MultimediaAuthRequest
	sars chan cx.ServerAssignmentRequest

	mu            sync.Mutex
	marResult     uint32
	sarResult     uint32
	akaScheme     cx.AuthenticationScheme
	vector        cx.AKAVector
	subscriptions []cx.IMSSubscription
	marGate       chan struct{}
	sarGate       chan struct{}

	userData   []byte
	noUserData bool
}

func newFakeHSS(t *testing.T) *fakeHSS {
	t.Helper()

	h := &fakeHSS{
		id:            diameter.Identity{OriginHost: hssHost, OriginRealm: homeDomain, ProductName: "fake-hss"},
		mars:          make(chan cx.MultimediaAuthRequest, 16),
		sars:          make(chan cx.ServerAssignmentRequest, 16),
		akaScheme:     cx.SchemeDigestAKAv1MD5,
		vector:        testVector,
		subscriptions: testSubscriptions(),
	}

	return h
}

func (h *fakeHSS) serve(ctx context.Context, req *diameter.Message) *diameter.Message {
	switch req.CommandCode {
	case cx.CommandMultimediaAuth:
		return h.multimediaAuth(ctx, req)
	case cx.CommandServerAssignment:
		return h.serverAssignment(ctx, req)
	default:
		return diameter.NewAnswer(req, h.id, diameter.ResultCommandUnsupported)
	}
}

func (h *fakeHSS) set(f func(h *fakeHSS)) {
	h.mu.Lock()
	defer h.mu.Unlock()

	f(h)
}

func failure(req *diameter.Message, id diameter.Identity, code uint32) *diameter.Message {
	r := tgpp.Experimental(code)
	if code == diameter.ResultUnableToComply || code == diameter.ResultTooBusy {
		r = tgpp.Result{Code: code}
	}

	return cx.NewAnswer(req, id, r, 0)
}

func (h *fakeHSS) multimediaAuth(_ context.Context, req *diameter.Message) *diameter.Message {
	mar, err := cx.ParseMultimediaAuthRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, h.id, err, 0)
	}

	h.mars <- mar

	h.mu.Lock()
	code, scheme, gate, v := h.marResult, h.akaScheme, h.marGate, h.vector
	h.mu.Unlock()

	if gate != nil {
		<-gate
	}

	if code != 0 {
		return failure(req, h.id, code)
	}

	ans, err := cx.NewMultimediaAuthAnswer(req, h.id, cx.MultimediaAuth{
		Items: []cx.AuthItem{{Scheme: scheme, AKA: &v}},
	})
	if err != nil {
		panic(err)
	}

	return ans
}

func (h *fakeHSS) serverAssignment(ctx context.Context, req *diameter.Message) *diameter.Message {
	sar, err := cx.ParseServerAssignmentRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, h.id, err, 0)
	}

	h.sars <- sar

	h.mu.Lock()
	code, gate := h.sarResult, h.sarGate
	h.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil
		}
	}

	if code != 0 {
		return failure(req, h.id, code)
	}

	var a cx.ServerAssignment

	if (sar.Type == cx.AssignmentRegistration || sar.Type == cx.AssignmentReRegistration) && !sar.UserDataAlreadyAvailable {
		h.mu.Lock()
		sub, ok := h.subscription(sar.PublicIdentities[0])
		raw, none := h.userData, h.noUserData
		h.mu.Unlock()

		switch {
		case none:
			a.UserData = []byte("<IMSSubscription/>")
			ans := must(cx.NewServerAssignmentAnswer(req, h.id, a))
			ans.AVPs = slices.DeleteFunc(ans.AVPs, func(avp diameter.AVP) bool { return avp.Code == cx.AVPUserData })

			return ans
		case raw != nil:
			a.UserData = raw
			return must(cx.NewServerAssignmentAnswer(req, h.id, a))
		}

		if !ok {
			return failure(req, h.id, tgpp.ResultErrorUserUnknown)
		}

		if a.UserData, err = cx.MarshalUserData(sub); err != nil {
			panic(err)
		}
	}

	ans, err := cx.NewServerAssignmentAnswer(req, h.id, a)
	if err != nil {
		panic(err)
	}

	return ans
}

func (h *fakeHSS) subscription(impu string) (cx.IMSSubscription, bool) {
	for _, sub := range h.subscriptions {
		for _, profile := range sub.ServiceProfiles {
			for _, pi := range profile.PublicIdentities {
				if identityKeyOf(pi.Identity) == identityKeyOf(impu) {
					return sub, true
				}
			}
		}
	}

	return cx.IMSSubscription{}, false
}

func testSubscriptions() []cx.IMSSubscription {
	return []cx.IMSSubscription{
		{
			PrivateIdentity: testIMPI,
			ServiceProfiles: []cx.ServiceProfile{
				{PublicIdentities: []cx.ProfileIdentity{
					{Identity: testIMPU, Barred: true},
					{Identity: testMSISDN, DisplayName: "Alice"},
					{Identity: testTel},
				}},
				{PublicIdentities: []cx.ProfileIdentity{{Identity: testAlias}}},
			},
		},
		{
			PrivateIdentity: testIMPI,
			ServiceProfiles: []cx.ServiceProfile{{PublicIdentities: []cx.ProfileIdentity{{Identity: secondIMPU}}}},
		},
	}
}

func (h *fakeHSS) nextMAR(t *testing.T) cx.MultimediaAuthRequest {
	t.Helper()

	select {
	case m := <-h.mars:
		return m
	case <-time.After(siptest.Timeout):
		t.Fatal("timed out waiting for a MAR")
	}

	return cx.MultimediaAuthRequest{}
}

func (h *fakeHSS) nextSAR(t *testing.T) cx.ServerAssignmentRequest {
	t.Helper()

	select {
	case s := <-h.sars:
		return s
	case <-time.After(siptest.Timeout):
		t.Fatal("timed out waiting for a SAR")
	}

	return cx.ServerAssignmentRequest{}
}

func (h *fakeHSS) wantSAR(t *testing.T, want cx.AssignmentType) cx.ServerAssignmentRequest {
	t.Helper()

	sar := h.nextSAR(t)
	if sar.Type != want || sar.PrivateIdentity != testIMPI || sar.ServerName != "sip:scscf."+homeDomain+":5060" {
		t.Fatalf("SAR = %+v, want %s for %s", sar, want, testIMPI)
	}

	return sar
}

func (h *fakeHSS) noCx(t *testing.T) {
	t.Helper()

	select {
	case m := <-h.mars:
		t.Fatalf("unexpected MAR %+v", m)
	case s := <-h.sars:
		t.Fatalf("unexpected SAR %+v", s)
	default:
	}
}

type harness struct {
	t     *testing.T
	hss   *fakeHSS
	loop  *diametertest.Loop
	db    *db.DB
	clock fakeClock
	reg   *Registrar
	scscf netip.AddrPort

	sipClock *siptest.Clock
	cfg      Config
	pcscf    *fakePCSCF

	icscf      *siptest.Socket
	numbering  Numbering
	sessions   *Sessions
	sipProxy   *proxy.Proxy
	scscfLayer *transaction.Layer
}

type fakePCSCF struct {
	reg      atomic.Pointer[Registrar]
	sessions atomic.Pointer[Sessions]
	wg       sync.WaitGroup
}

func (p *fakePCSCF) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	respond := func(res *sip.Response) { _ = tx.Respond(res) }

	switch {
	case req.Method == "SUBSCRIBE" && IsRegEvent(req):
		p.wg.Go(func() { p.reg.Load().Subscribe(context.Background(), req, routes(req), respond) })
	case req.Method == "REGISTER":
		p.wg.Go(func() { p.reg.Load().Register(context.Background(), req, respond) })
	default:
		p.sessions.Load().HandleRequest(tx, req)
	}
}

func routes(req *sip.Request) []sip.URI {
	var out []sip.URI

	if rs, err := req.Header.Routes(); err == nil {
		for _, r := range rs {
			out = append(out, r.URI)
		}
	}

	return out
}

func (p *fakePCSCF) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	p.sessions.Load().HandleCancel(tx, cancel)
}

func (p *fakePCSCF) HandleAck(ack *sip.Request) {
	p.sessions.Load().HandleAck(ack)
}

func (*fakePCSCF) HandleTransactionError(*transaction.ServerTransaction, error) {}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{t: t, hss: newFakeHSS(t), clock: fakeClock{siptest.NewClock()}, sipClock: siptest.NewClock()}

	database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	t.Cleanup(func() { _ = database.Close() })

	h.db = database

	h.loop = &diametertest.Loop{
		Local:   diameter.Identity{OriginHost: imsHost, OriginRealm: homeDomain, ProductName: "ims"},
		Handler: h.hss.serve,
	}

	h.pcscf = &fakePCSCF{}
	t.Cleanup(h.pcscf.wg.Wait)

	layer, _ := siptest.NewLayer(t, transaction.Config{
		Handler: h.pcscf,
		Logger:  slog.New(slog.DiscardHandler),
		Clock:   h.sipClock,
	})
	h.scscf = siptest.ListenLayer(t, layer, loopback)
	h.scscfLayer = layer
	h.icscf = siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	h.cfg = Config{
		HomeDomain:           homeDomain,
		Name:                 sip.URI{Scheme: "sip", Host: scscfName, Port: sipPort},
		MinExpires:           60 * time.Second,
		MaxExpires:           3600 * time.Second,
		HSS:                  HSS{ID: "hss", Host: hssHost, Realm: homeDomain},
		Diameter:             h.loop,
		DB:                   database,
		RegistrationAttempts: regmetrics.New(),
		Clock:                h.clock,
		Logger:               slog.New(slog.DiscardHandler),
		Layer:                layer,
		Listeners:            []netip.AddrPort{h.scscf},
	}

	h.start()

	return h
}

func (h *harness) start() {
	h.reg = New(h.cfg)
	h.t.Cleanup(h.reg.Close)

	h.pcscf.reg.Store(h.reg)

	h.sipProxy = proxy.New(proxy.Config{Layer: h.scscfLayer, Logger: h.cfg.Logger, Port: h.scscf.Port(), Clock: h.sipClock})
	h.sessions = h.reg.Sessions(SessionConfig{Proxy: h.sipProxy, ICSCF: []netip.AddrPort{h.icscf.Addr()}, Numbering: func() Numbering { return h.numbering }})
	h.pcscf.sessions.Store(h.sessions)

	h.reg.Start()
}

func (h *harness) restart() {
	h.reg.Close()
	h.start()
}

type ue struct {
	h       *harness
	sock    *siptest.Socket
	inbox   *siptest.Socket
	impi    string
	impu    string
	contact string
	path    string
	callID  string
	cseq    int
}

func (h *harness) newUE() *ue {
	sock := siptest.NewSocket(h.t, netip.AddrPortFrom(loopback, 0))
	inbox := siptest.NewSocket(h.t, netip.AddrPortFrom(loopback, 0))

	return &ue{
		h:       h,
		sock:    sock,
		inbox:   inbox,
		impi:    testIMPI,
		impu:    testIMPU,
		contact: "sip:001010000000001@127.0.0.1:" + strconv.Itoa(int(inbox.Addr().Port())),
		path:    testPath,
		callID:  sip.NewTag() + "@127.0.0.1",
	}
}

type registerOptions struct {
	auth      string
	expires   string
	contact   string
	noContact bool
	supported string
}

func (u *ue) request(o registerOptions) *sip.Request {
	u.cseq++

	req := siptest.NewRequest("REGISTER", "sip:scscf."+homeDomain+":5060", sip.UDP, u.sock.Addr())
	req.Header.Set("To", "<"+u.impu+">")
	req.Header.Set("From", "<"+u.impu+">;tag="+sip.NewTag())
	req.Header.Set("Call-ID", u.callID)
	req.Header.Set("CSeq", strconv.Itoa(u.cseq)+" REGISTER")
	req.Header.Del("Contact")

	switch {
	case o.noContact:
	case o.contact != "":
		req.Header.Add("Contact", o.contact)
	default:
		req.Header.Add("Contact", "<"+u.contact+">;+sip.instance="+testInstance+";+g.3gpp.smsip")
	}

	if o.expires != "" {
		req.Header.Add("Expires", o.expires)
	}

	if o.supported != "" {
		req.Header.Add("Supported", o.supported)
	}

	if o.auth != "" {
		req.Header.Add("Authorization", o.auth)
	}

	req.Header.Add("Path", u.path)

	return req
}

func (u *ue) send(o registerOptions) *sip.Response {
	u.h.t.Helper()

	u.sock.Send(sip.UDP, u.h.scscf, u.request(o))

	return u.recv()
}

func (u *ue) recv() *sip.Response {
	u.h.t.Helper()

	for {
		res, _ := u.sock.RecvResponse()
		if !res.IsProvisional() {
			return res
		}
	}
}

func (u *ue) unprotected() string {
	return fmt.Sprintf(`Digest username="%s", realm="%s", uri="sip:%s", nonce="", response="", integrity-protected="no"`,
		u.impi, homeDomain, homeDomain)
}

func (u *ue) protected(nonce string, xres []byte) string {
	const nc, cnonce = "00000001", "0a4f113b"

	uri := "sip:" + homeDomain
	ha1 := hexMD5([]byte(u.impi+":"+homeDomain+":"), xres)
	ha2 := hexMD5([]byte("REGISTER:" + uri))
	response := hexMD5([]byte(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2))

	return fmt.Sprintf(`Digest username="%s", realm="%s", uri="%s", nonce="%s", response="%s", algorithm=AKAv1-MD5, `+
		`qop=auth, nc=%s, cnonce="%s", integrity-protected="yes"`, u.impi, homeDomain, uri, nonce, response, nc, cnonce)
}

func (u *ue) resync(nonce string, auts []byte, protected bool) string {
	integrity := "no"
	if protected {
		integrity = "yes"
	}

	auth := u.protected(nonce, nil)
	auth = auth[:strings.LastIndex(auth, "integrity-protected=")]

	return auth + `integrity-protected="` + integrity + `", auts="` + base64.StdEncoding.EncodeToString(auts) + `"`
}

func hexMD5(parts ...[]byte) string {
	h := md5.New()
	for _, p := range parts {
		h.Write(p)
	}

	return hex.EncodeToString(h.Sum(nil))
}

func testNonce() string {
	return base64.StdEncoding.EncodeToString(append(append([]byte(nil), testVector.RAND...), testVector.AUTN...))
}

func wantStatus(t *testing.T, res *sip.Response, code int) {
	t.Helper()

	if res.StatusCode != code {
		t.Fatalf("got %q, want %d", res.StartLine(), code)
	}
}

func (u *ue) challenged(o registerOptions) string {
	u.h.t.Helper()

	o.auth = u.unprotected()
	res := u.send(o)
	wantStatus(u.h.t, res, 401)
	u.h.hss.nextMAR(u.h.t)

	return sip.Unquote(challengeParams(u.h.t, res)["nonce"])
}

func (u *ue) register(o registerOptions) *sip.Response {
	u.h.t.Helper()

	nonce := u.challenged(o)

	o.auth = u.protected(nonce, testVector.XRES)
	res := u.send(o)
	wantStatus(u.h.t, res, 200)

	return res
}

func challengeParams(t *testing.T, res *sip.Response) map[string]string {
	t.Helper()

	v := res.Header.Get("WWW-Authenticate")

	a, err := sip.ParseAuth(v)
	if err != nil || a.Scheme != "Digest" {
		t.Fatalf("WWW-Authenticate = %q, want Digest", v)
	}

	params := map[string]string{}

	for _, p := range a.Params {
		params[p.Name] = p.Value
	}

	return params
}

func must(ans *diameter.Message, err error) *diameter.Message {
	if err != nil {
		panic(err)
	}

	return ans
}
