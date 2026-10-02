package icscf

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	homeDomain = "ims.mnc001.mcc001.3gppnetwork.org"
	imsHost    = "ims." + homeDomain
	hssHost    = "hss." + homeDomain

	testIMPI = "001010000000001@" + homeDomain
	testIMPU = "sip:001010000000001@" + homeDomain
	callee   = "sip:+15551230002@" + homeDomain + ";user=phone"

	testT1    = 20 * time.Millisecond
	cxTimeout = 300 * time.Millisecond
	quiet     = 100 * time.Millisecond
)

var (
	loopback  = netip.MustParseAddr("127.0.0.1")
	untrusted = netip.MustParseAddr("127.0.0.2")
)

// hssAnswer is what the fake HSS answers: a result, with a Server-Name or
// Server-Capabilities on success. A nil answer means no answer at all. A
// gate, when set, holds the answer until it is closed.
type hssAnswer struct {
	result tgpp.Result
	name   string
	caps   *cx.ServerCapabilities
	gate   chan struct{}
}

func success(code uint32) tgpp.Result {
	if code == diameter.ResultSuccess {
		return tgpp.Result{Code: code}
	}

	return tgpp.Experimental(code)
}

type fakeHSS struct {
	node *diameter.Node
	port int

	uars chan cx.UserAuthorizationRequest
	lirs chan cx.LocationInfoRequest

	mu   sync.Mutex
	uaa  []*hssAnswer
	lia  []*hssAnswer
	stop chan struct{}
}

func newFakeHSS(t *testing.T) *fakeHSS {
	t.Helper()

	h := &fakeHSS{
		uars: make(chan cx.UserAuthorizationRequest, 16),
		lirs: make(chan cx.LocationInfoRequest, 16),
		stop: make(chan struct{}),
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", netip.AddrPortFrom(loopback, 0).String())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	h.port = ln.Addr().(*net.TCPAddr).Port

	mux := diameter.NewMux()
	mux.Handle(cx.ApplicationID, cx.CommandUserAuthorization, diameter.HandlerFunc(h.userAuthorization))
	mux.Handle(cx.ApplicationID, cx.CommandLocationInfo, diameter.HandlerFunc(h.locationInfo))

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

	t.Cleanup(func() {
		close(h.stop)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = node.Shutdown(ctx)
	})

	return h
}

// answerUAR queues the answers to the next UARs.
func (h *fakeHSS) answerUAR(answers ...*hssAnswer) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.uaa = append(h.uaa, answers...)
}

func (h *fakeHSS) answerLIR(answers ...*hssAnswer) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.lia = append(h.lia, answers...)
}

func (h *fakeHSS) next(queue *[]*hssAnswer) *hssAnswer {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(*queue) == 0 {
		return nil
	}

	a := (*queue)[0]
	*queue = (*queue)[1:]

	return a
}

// wait waits for a gate to open, until the HSS stops for a nil one, and
// reports whether it opened.
func (h *fakeHSS) wait(ctx context.Context, gate chan struct{}) bool {
	select {
	case <-gate:
		return true
	case <-h.stop:
	case <-ctx.Done():
	}

	return false
}

func (h *fakeHSS) userAuthorization(ctx context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	uar, err := cx.ParseUserAuthorizationRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	h.uars <- uar

	a := h.next(&h.uaa)
	if a == nil {
		h.wait(ctx, nil)
		return nil
	}

	if a.gate != nil && !h.wait(ctx, a.gate) {
		return nil
	}

	if a.result.Failure() {
		return cx.NewAnswer(req, c.LocalIdentity(), a.result, 0)
	}

	ans, err := cx.NewUserAuthorizationAnswer(req, c.LocalIdentity(), cx.UserAuthorization{
		Result: a.result, ServerName: a.name, Capabilities: a.caps,
	})
	if err != nil {
		panic(err)
	}

	return ans
}

func (h *fakeHSS) locationInfo(ctx context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	lir, err := cx.ParseLocationInfoRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	h.lirs <- lir

	a := h.next(&h.lia)
	if a == nil {
		h.wait(ctx, nil)
		return nil
	}

	if a.gate != nil && !h.wait(ctx, a.gate) {
		return nil
	}

	if a.result.Failure() {
		return cx.NewAnswer(req, c.LocalIdentity(), a.result, 0)
	}

	ans, err := cx.NewLocationInfoAnswer(req, c.LocalIdentity(), cx.LocationInfo{
		Result: a.result, ServerName: a.name, Capabilities: a.caps,
	})
	if err != nil {
		panic(err)
	}

	return ans
}

func (h *fakeHSS) nextUAR(t *testing.T) cx.UserAuthorizationRequest {
	t.Helper()

	select {
	case r := <-h.uars:
		return r
	case <-time.After(siptest.Timeout):
		t.Fatal("timed out waiting for a UAR")
	}

	return cx.UserAuthorizationRequest{}
}

func (h *fakeHSS) nextLIR(t *testing.T) cx.LocationInfoRequest {
	t.Helper()

	select {
	case r := <-h.lirs:
		return r
	case <-time.After(siptest.Timeout):
		t.Fatal("timed out waiting for an LIR")
	}

	return cx.LocationInfoRequest{}
}

func (h *fakeHSS) noCx(t *testing.T) {
	t.Helper()

	select {
	case r := <-h.uars:
		t.Fatalf("unexpected UAR %+v", r)
	case r := <-h.lirs:
		t.Fatalf("unexpected LIR %+v", r)
	case <-time.After(quiet):
	}
}

// fakeSCSCF is an S-CSCF listening on its own socket.
type fakeSCSCF struct {
	t    *testing.T
	sock *siptest.Socket
	name string
	seen map[string]bool
}

func (s *fakeSCSCF) uri() string {
	return s.name
}

// recv returns the next request, skipping retransmissions.
func (s *fakeSCSCF) recv() (*sip.Request, sip.Flow) {
	s.t.Helper()

	for {
		req, f := s.sock.RecvRequest()

		via, err := req.Header.TopVia()
		if err != nil {
			s.t.Fatal(err)
		}

		key := via.Branch() + " " + req.Method
		if s.seen[key] {
			continue
		}

		s.seen[key] = true

		return req, f
	}
}

func (s *fakeSCSCF) respond(req *sip.Request, f sip.Flow, code int, edit ...func(*sip.Response)) {
	res := sip.NewResponse(req, code, "")
	if code > 100 {
		_ = res.Header.SetToTag(sip.NewTag())
	}

	for _, e := range edit {
		e(res)
	}

	s.sock.Send(f.Transport, f.Remote, res)
}

type harness struct {
	t      *testing.T
	hss    *fakeHSS
	node   *diameter.Node
	icscf  netip.AddrPort
	scscfs []*fakeSCSCF
	ic     *ICSCF
}

type harnessOptions struct {
	scscfs       int
	capabilities [][]uint32
	hssDown      bool

	// scscfAddr is the address of the S-CSCFs, the I-CSCF's by default.
	scscfAddr netip.Addr

	// down is the number of S-CSCFs, first in the table, that refuse
	// connections: they are reached over TCP, on a closed socket.
	down int
}

// lateHandler lets the layer start before the I-CSCF, which needs the port
// the layer bound.
type lateHandler struct {
	h atomic.Pointer[ICSCF]
}

func (l *lateHandler) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	l.h.Load().HandleRequest(tx, req)
}

func (l *lateHandler) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	l.h.Load().HandleCancel(tx, cancel)
}

func (l *lateHandler) HandleAck(ack *sip.Request) {
	l.h.Load().HandleAck(ack)
}

func (l *lateHandler) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	l.h.Load().HandleTransactionError(tx, err)
}

func newHarness(t *testing.T, o harnessOptions) *harness {
	t.Helper()

	if o.scscfs == 0 {
		o.scscfs = 1
	}

	h := &harness{t: t, hss: newFakeHSS(t)}

	hssPort := h.hss.port
	if o.hssDown {
		hssPort = unusedPort(t)
	}

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
		Port:         uint16(hssPort),
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

	if !o.hssDown {
		h.waitHSS()
	}

	var table []SCSCF

	if !o.scscfAddr.IsValid() {
		o.scscfAddr = loopback
	}

	for k := range o.scscfs {
		sock := siptest.NewSocket(t, netip.AddrPortFrom(o.scscfAddr, 0))
		name := "sip:scscf" + strconv.Itoa(k+1) + "." + homeDomain + ":" + strconv.Itoa(int(sock.Addr().Port()))

		if k < o.down {
			sock.Close()

			name += ";transport=tcp"
		}

		h.scscfs = append(h.scscfs, &fakeSCSCF{t: t, sock: sock, name: name, seen: map[string]bool{}})

		u, _ := sip.ParseURI(name)

		var capabilities []uint32
		if k < len(o.capabilities) {
			capabilities = o.capabilities[k]
		}

		table = append(table, SCSCF{Name: u, Capabilities: capabilities, Listeners: []netip.AddrPort{sock.Addr()}})
	}

	late := &lateHandler{}

	layer, _ := siptest.NewLayer(t, transaction.Config{
		Handler: late,
		Logger:  slog.New(slog.DiscardHandler),
		Aliases: []string{homeDomain, "scscf1." + homeDomain, "scscf2." + homeDomain},
		T1:      testT1,
	})
	h.icscf = siptest.ListenLayer(t, layer, loopback)

	h.ic = New(Config{
		HomeDomain:   homeDomain,
		Layer:        layer,
		Proxy:        proxy.New(proxy.Config{Layer: layer, Logger: slog.New(slog.DiscardHandler), Port: h.icscf.Port()}),
		Port:         h.icscf.Port(),
		Trust:        trust.New([]netip.Addr{loopback}, nil),
		SCSCFs:       table,
		HSS:          HSS{ID: "hss", Realm: homeDomain},
		Diameter:     node,
		CxTimeout:    cxTimeout,
		SCSCFTimeout: 32 * testT1,
		Logger:       testLogger(),
	})
	late.h.Store(h.ic)

	return h
}

func unusedPort(t *testing.T) int {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	return port
}

func (h *harness) waitHSS() {
	h.t.Helper()

	deadline := time.Now().Add(siptest.Timeout)

	for {
		if p, ok := h.node.Peer("hss"); ok && p.State == diameter.PeerOpen {
			return
		}

		if time.Now().After(deadline) {
			h.t.Fatal("timed out waiting for the HSS")
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) icscfURI() string {
	return "sip:" + homeDomain + ":" + strconv.Itoa(int(h.icscf.Port()))
}

// ue sends requests to the I-CSCF, as the P-CSCF or the S-CSCF would.
type ue struct {
	h    *harness
	sock *siptest.Socket
}

func (h *harness) newUE(addr netip.Addr) *ue {
	return &ue{h: h, sock: siptest.NewSocket(h.t, netip.AddrPortFrom(addr, 0))}
}

func (u *ue) register(edit ...func(*sip.Request)) *sip.Request {
	req := siptest.NewRequest("REGISTER", "sip:"+homeDomain, sip.UDP, u.sock.Addr())
	req.Header.Set("To", "<"+testIMPU+">")
	req.Header.Set("From", "<"+testIMPU+">;tag="+sip.NewTag())
	req.Header.Set("Expires", "600")
	req.Header.Add("Path", "<sip:term@pcscf."+homeDomain+";lr>")
	req.Header.Add("P-Visited-Network-ID", `"visited.example.org"`)
	req.Header.Add("Authorization", `Digest username="`+testIMPI+`", realm="`+homeDomain+`", uri="sip:`+homeDomain+
		`", nonce="", response="", integrity-protected="no"`)

	for _, e := range edit {
		e(req)
	}

	return req
}

func (u *ue) invite(target string, edit ...func(*sip.Request)) *sip.Request {
	req := siptest.NewRequest("INVITE", target, sip.UDP, u.sock.Addr())

	for _, e := range edit {
		e(req)
	}

	return req
}

func (u *ue) send(req *sip.Request) {
	u.sock.Send(sip.UDP, u.h.icscf, req)
}

// final returns the next final response, skipping provisional ones.
func (u *ue) final() *sip.Response {
	u.h.t.Helper()

	for {
		res, _ := u.sock.RecvResponse()
		if !res.IsProvisional() {
			return res
		}
	}
}

func (u *ue) wantFinal(code int) *sip.Response {
	u.h.t.Helper()

	res := u.final()
	if res.StatusCode != code {
		u.h.t.Fatalf("got %q, want %d", res.StartLine(), code)
	}

	return res
}

func (u *ue) cancel(invite *sip.Request) {
	u.h.t.Helper()

	cancel, err := sip.NewCancel(invite)
	if err != nil {
		u.h.t.Fatal(err)
	}

	u.send(cancel)
}

// finals returns the final responses to a cancelled INVITE and its CANCEL,
// by method, and acknowledges the INVITE's.
func (u *ue) finals(invite *sip.Request) map[string]int {
	u.h.t.Helper()

	got := map[string]int{}

	for len(got) < 2 {
		res := u.final()

		cseq, _ := res.Header.CSeq()
		got[cseq.Method] = res.StatusCode

		if cseq.Method == "INVITE" {
			u.ack(invite, res)
		}
	}

	return got
}

// ack acknowledges a non-2xx final response to an INVITE.
func (u *ue) ack(invite *sip.Request, res *sip.Response) {
	u.h.t.Helper()

	ack, err := sip.NewAck(invite, res)
	if err != nil {
		u.h.t.Fatal(err)
	}

	u.send(ack)
}

func routes(t *testing.T, req *sip.Request) []string {
	t.Helper()

	rs, err := req.Header.Routes()
	if err != nil {
		t.Fatal(err)
	}

	var out []string
	for _, r := range rs {
		out = append(out, r.URI.String())
	}

	return out
}

func testLogger() *slog.Logger {
	if os.Getenv("ICSCF_DEBUG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	return slog.New(slog.DiscardHandler)
}
