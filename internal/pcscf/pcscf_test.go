package pcscf

import (
	"log/slog"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

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

func TestRegisterGoesToTheICSCF(t *testing.T) {
	icscf := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	late := &lateHandler{}
	layer, fallback := siptest.NewLayer(t, transaction.Config{Handler: late, Logger: slog.New(slog.DiscardHandler)})
	pcscf := siptest.ListenLayer(t, layer, loopback)

	late.h.Store(New(Config{
		Proxy:     proxy.New(proxy.Config{Layer: layer, Port: pcscf.Port()}),
		Port:      pcscf.Port(),
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

type scene struct {
	t     *testing.T
	icscf *siptest.Socket
	pcscf netip.AddrPort
	ue    *siptest.Socket
}

func newScene(t *testing.T) *scene {
	t.Helper()

	icscf := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	late := &lateHandler{}
	layer, fallback := siptest.NewLayer(t, transaction.Config{Handler: late, Logger: slog.New(slog.DiscardHandler)})
	pcscf := siptest.ListenLayer(t, layer, loopback)

	late.h.Store(New(Config{
		Proxy:     proxy.New(proxy.Config{Layer: layer, Port: pcscf.Port()}),
		Port:      pcscf.Port(),
		ICSCFPort: icscf.Addr().Port(),
		Fallback:  fallback,
		Logger:    slog.New(slog.DiscardHandler),
	}))

	return &scene{t: t, icscf: icscf, pcscf: pcscf, ue: siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))}
}

func (s *scene) register(edit func(*sip.Request)) {
	req := siptest.NewRequest("REGISTER", "sip:"+homeDomain, sip.UDP, s.ue.Addr())
	edit(req)
	s.ue.Send(sip.UDP, s.pcscf, req)
}

func TestRegisterHeaderFieldsFromTheUE(t *testing.T) {
	s := newScene(t)

	stripped := []string{
		"P-Asserted-Identity", "P-Access-Network-Info", "P-Charging-Vector", "P-Charging-Function-Addresses",
		"P-Visited-Network-ID", "Path",
	}

	s.register(func(r *sip.Request) {
		r.Header.Add("P-Asserted-Identity", "<sip:bob@"+homeDomain+">")
		r.Header.Add("P-Access-Network-Info", "3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=00101000100000001")
		r.Header.Add("P-Charging-Vector", "icid-value=1234")
		r.Header.Add("P-Charging-Function-Addresses", "ccf=192.0.2.1")
		r.Header.Add("P-Visited-Network-ID", `"visited.example.org"`)
		r.Header.Add("Path", "<sip:attacker.example.org;lr>")
	})

	req, f := s.icscf.RecvRequest()

	for _, name := range stripped {
		if req.Header.Has(name) {
			t.Errorf("%s forwarded from the UE", name)
		}
	}

	res := sip.NewResponse(req, 200, "")
	_ = res.Header.SetToTag(sip.NewTag())
	res.Header.Add("Service-Route", "<sip:orig@scscf."+homeDomain+";lr>")
	res.Header.Add("P-Charging-Vector", "icid-value=1234")
	res.Header.Add("P-Charging-Function-Addresses", "ccf=192.0.2.1")
	res.Header.Add("P-Asserted-Identity", "<sip:bob@"+homeDomain+">")
	s.icscf.Send(f.Transport, f.Remote, res)

	got, _ := s.ue.RecvResponse()
	if got.StatusCode != 200 || !got.Header.Has("Service-Route") {
		t.Fatalf("got %q, want 200 with its Service-Route", got)
	}

	for _, name := range []string{"P-Charging-Vector", "P-Charging-Function-Addresses", "P-Asserted-Identity"} {
		if got.Header.Has(name) {
			t.Errorf("%s relayed to the UE", name)
		}
	}
}

func TestRegisterAuthorization(t *testing.T) {
	auth := `Digest username="alice@` + homeDomain + `", realm="` + homeDomain + `", uri="sip:` + homeDomain +
		`", nonce="", response=""`

	t.Run("none", func(t *testing.T) {
		s := newScene(t)
		s.register(func(*sip.Request) {})

		if req, _ := s.icscf.RecvRequest(); req.Header.Has("Authorization") {
			t.Fatalf("Authorization = %q, want none", req.Header.Get("Authorization"))
		}
	})

	t.Run("duplicate integrity-protected", func(t *testing.T) {
		s := newScene(t)
		s.register(func(r *sip.Request) {
			r.Header.Add("Authorization", auth+`, integrity-protected="yes", integrity-protected="yes"`)
		})

		req, _ := s.icscf.RecvRequest()

		a, err := sip.ParseAuth(req.Header.Get("Authorization"))
		if err != nil {
			t.Fatal(err)
		}

		var values []string

		for _, p := range a.Params {
			if p.Name == "integrity-protected" {
				values = append(values, p.Value)
			}
		}

		if len(values) != 1 || values[0] != `"no"` {
			t.Fatalf("integrity-protected = %v, want one \"no\"", values)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		s := newScene(t)
		s.register(func(r *sip.Request) { r.Header.Add("Authorization", "Digest username=") })

		if res, _ := s.ue.RecvResponse(); res.StatusCode != 400 {
			t.Fatalf("got %q, want 400", res.StartLine())
		}

		s.icscf.RecvNone(50 * time.Millisecond)
	})
}
