package pcscf

import (
	"log/slog"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

const homeDomain = "ims.mnc001.mcc001.3gppnetwork.org"

var loopback = netip.MustParseAddr("127.0.0.1")

type lateHandler struct {
	h atomic.Pointer[PCSCF]
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

// TestRegisterGoesToTheICSCF runs the P-CSCF and the I-CSCF on the same
// address, as the server does.
func TestRegisterGoesToTheICSCF(t *testing.T) {
	icscf := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	late := &lateHandler{}
	layer, fallback := siptest.NewLayer(t, transaction.Config{Handler: late, Logger: slog.New(slog.DiscardHandler)})
	pcscf := siptest.ListenLayer(t, layer, loopback)

	late.h.Store(New(Config{
		Proxy:     proxy.New(proxy.Config{Layer: layer, Port: pcscf.Port()}),
		ICSCFPort: icscf.Addr().Port(),
		Fallback:  fallback,
		Logger:    slog.New(slog.DiscardHandler),
	}))

	ue := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	register := siptest.NewRequest("REGISTER", "sip:"+homeDomain, sip.UDP, ue.Addr())
	register.Header.Add("Authorization", `Digest username="alice@`+homeDomain+`", realm="`+homeDomain+
		`", uri="sip:`+homeDomain+`", nonce="", response="", integrity-protected="yes"`)
	ue.Send(sip.UDP, pcscf, register)

	req, f := icscf.RecvRequest()

	a, err := sip.ParseAuth(req.Header.Get("Authorization"))
	if err != nil {
		t.Fatal(err)
	}

	if v, _ := a.Params.Get("integrity-protected"); v != `"no"` {
		t.Fatalf("integrity-protected = %s, want \"no\"", v)
	}

	res := sip.NewResponse(req, 401, "")
	_ = res.Header.SetToTag(sip.NewTag())
	res.Header.Add("WWW-Authenticate", `Digest realm="`+homeDomain+`", nonce="bm9uY2U=", algorithm=AKAv1-MD5, qop="auth", `+
		`ck="d53c02758b376066fad0af7daa6df765", ik="e1763cf28cc2e584588800193137ec92"`)
	icscf.Send(f.Transport, f.Remote, res)

	got, _ := ue.RecvResponse()

	want := `Digest realm="` + homeDomain + `", nonce="bm9uY2U=", algorithm=AKAv1-MD5, qop="auth"`
	if got.StatusCode != 401 || got.Header.Get("WWW-Authenticate") != want {
		t.Fatalf("got %q with WWW-Authenticate %q, want 401 with %q", got.StartLine(), got.Header.Get("WWW-Authenticate"), want)
	}
}

func TestOtherRequestsGoToTheFallback(t *testing.T) {
	late := &lateHandler{}
	layer, fallback := siptest.NewLayer(t, transaction.Config{Handler: late, Logger: slog.New(slog.DiscardHandler)})
	pcscf := siptest.ListenLayer(t, layer, loopback)

	late.h.Store(New(Config{Proxy: proxy.New(proxy.Config{Layer: layer}), Fallback: fallback}))

	ue := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	ue.Send(sip.UDP, pcscf, siptest.NewRequest("OPTIONS", "sip:"+homeDomain, sip.UDP, ue.Addr()))

	if req := fallback.NextRequest().Req; req.Method != "OPTIONS" {
		t.Fatalf("fallback got %s, want OPTIONS", req.Method)
	}
}
