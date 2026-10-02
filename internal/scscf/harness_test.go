package scscf

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
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
	node *diameter.Node
	port int

	mars chan cx.MultimediaAuthRequest
	sars chan cx.ServerAssignmentRequest

	mu            sync.Mutex
	marResult     uint32
	sarResult     uint32
	akaScheme     cx.AuthenticationScheme
	vector        cx.AKAVector
	subscriptions []cx.IMSSubscription
	marGate       chan struct{}
}

func newFakeHSS(t *testing.T) *fakeHSS {
	t.Helper()

	h := &fakeHSS{
		mars:          make(chan cx.MultimediaAuthRequest, 16),
		sars:          make(chan cx.ServerAssignmentRequest, 16),
		akaScheme:     cx.SchemeDigestAKAv1MD5,
		vector:        testVector,
		subscriptions: testSubscriptions(),
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", netip.AddrPortFrom(loopback, 0).String())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	h.port = ln.Addr().(*net.TCPAddr).Port

	mux := diameter.NewMux()
	mux.Handle(cx.ApplicationID, cx.CommandMultimediaAuth, diameter.HandlerFunc(h.multimediaAuth))
	mux.Handle(cx.ApplicationID, cx.CommandServerAssignment, diameter.HandlerFunc(h.serverAssignment))

	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      hssHost,
			OriginRealm:     homeDomain,
			HostIPAddresses: []netip.Addr{loopback},
			ProductName:     "fake-hss",
		},
		Handler: mux,
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("diameter.New: %v", err)
	}

	if err := node.SetPeers([]diameter.Peer{{
		ID:           "ims",
		Host:         imsHost,
		Addresses:    []netip.Addr{loopback},
		Transport:    diameter.TransportTCP,
		Applications: []diameter.Application{{ID: cx.ApplicationID, VendorID: tgpp.VendorID}},
		Passive:      true,
	}}); err != nil {
		t.Fatalf("SetPeers: %v", err)
	}

	go func() { _ = node.Serve(diameter.NewTCPListener(ln.(*net.TCPListener))) }()

	h.node = node

	t.Cleanup(func() { h.shutdown() })

	return h
}

func (h *fakeHSS) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = h.node.Shutdown(ctx)
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

func (h *fakeHSS) multimediaAuth(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	mar, err := cx.ParseMultimediaAuthRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	h.mars <- mar

	h.mu.Lock()
	code, scheme, gate, v := h.marResult, h.akaScheme, h.marGate, h.vector
	h.mu.Unlock()

	if gate != nil {
		<-gate
	}

	if code != 0 {
		return failure(req, c.LocalIdentity(), code)
	}

	ans, err := cx.NewMultimediaAuthAnswer(req, c.LocalIdentity(), cx.MultimediaAuth{
		Items: []cx.AuthItem{{Scheme: scheme, AKA: &v}},
	})
	if err != nil {
		panic(err)
	}

	return ans
}

func (h *fakeHSS) serverAssignment(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	sar, err := cx.ParseServerAssignmentRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	h.sars <- sar

	h.mu.Lock()
	code := h.sarResult
	h.mu.Unlock()

	if code != 0 {
		return failure(req, c.LocalIdentity(), code)
	}

	var a cx.ServerAssignment

	if (sar.Type == cx.AssignmentRegistration || sar.Type == cx.AssignmentReRegistration) && !sar.UserDataAlreadyAvailable {
		h.mu.Lock()
		sub, ok := h.subscription(sar.PublicIdentities[0])
		h.mu.Unlock()

		if !ok {
			return failure(req, c.LocalIdentity(), tgpp.ResultErrorUserUnknown)
		}

		if a.UserData, err = cx.MarshalUserData(sub); err != nil {
			panic(err)
		}
	}

	ans, err := cx.NewServerAssignmentAnswer(req, c.LocalIdentity(), a)
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
	node  *diameter.Node
	db    *db.DB
	clock fakeClock
	reg   *Registrar
	scscf netip.AddrPort
}

type fakePCSCF struct {
	reg *Registrar
	wg  sync.WaitGroup
}

func (p *fakePCSCF) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	p.wg.Go(func() {
		_ = tx.Respond(p.reg.Register(context.Background(), req))
	})
}

func (*fakePCSCF) HandleCancel(*transaction.ServerTransaction, *sip.Request) {}

func (*fakePCSCF) HandleAck(*sip.Request) {}

func (*fakePCSCF) HandleTransactionError(*transaction.ServerTransaction, error) {}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{t: t, hss: newFakeHSS(t), clock: fakeClock{siptest.NewClock()}}

	database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	t.Cleanup(func() { _ = database.Close() })

	h.db = database

	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      imsHost,
			OriginRealm:     homeDomain,
			HostIPAddresses: []netip.Addr{loopback},
			ProductName:     "ims",
		},
		Handler: diameter.NewMux(),
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("diameter.New: %v", err)
	}

	if err := node.SetPeers([]diameter.Peer{{
		ID:           "hss",
		Host:         hssHost,
		Addresses:    []netip.Addr{loopback},
		Port:         uint16(h.hss.port),
		Transport:    diameter.TransportTCP,
		Applications: []diameter.Application{{ID: cx.ApplicationID, VendorID: tgpp.VendorID}},
	}}); err != nil {
		t.Fatalf("SetPeers: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = node.Shutdown(ctx)
	})

	h.node = node
	h.waitHSS(diameter.PeerOpen)

	h.reg = New(Config{
		HomeDomain: homeDomain,
		Name:       sip.URI{Scheme: "sip", Host: scscfName, Port: sipPort},
		MinExpires: 60 * time.Second,
		MaxExpires: 3600 * time.Second,
		HSS:        HSS{ID: "hss", Host: hssHost, Realm: homeDomain},
		Diameter:   node,
		DB:         database,
		Clock:      h.clock,
		Logger:     slog.New(slog.DiscardHandler),
	})
	t.Cleanup(h.reg.Close)

	pcscf := &fakePCSCF{reg: h.reg}
	t.Cleanup(pcscf.wg.Wait)

	layer, _ := siptest.NewLayer(t, transaction.Config{
		Handler: pcscf,
		Logger:  slog.New(slog.DiscardHandler),
	})
	h.scscf = siptest.ListenLayer(t, layer, loopback)

	return h
}

func (h *harness) waitHSS(want diameter.PeerState) {
	h.t.Helper()

	deadline := time.Now().Add(siptest.Timeout)

	for {
		if p, ok := h.node.Peer("hss"); ok && p.State == want {
			return
		}

		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for the HSS to be %s", want)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

type ue struct {
	h       *harness
	sock    *siptest.Socket
	impi    string
	impu    string
	contact string
	callID  string
	cseq    int
}

func (h *harness) newUE() *ue {
	sock := siptest.NewSocket(h.t, netip.AddrPortFrom(loopback, 0))

	return &ue{
		h:       h,
		sock:    sock,
		impi:    testIMPI,
		impu:    testIMPU,
		contact: "sip:001010000000001@127.0.0.1:" + strconv.Itoa(int(sock.Addr().Port())),
		callID:  sip.NewTag() + "@127.0.0.1",
	}
}

type registerOptions struct {
	auth      string
	expires   string
	contact   string
	noContact bool
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

	if o.auth != "" {
		req.Header.Add("Authorization", o.auth)
	}

	req.Header.Add("Path", testPath)

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
