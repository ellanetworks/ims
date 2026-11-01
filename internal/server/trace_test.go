package server

import (
	"net/netip"
	"testing"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestDiameterIsTraced checks that the IMS traces its Diameter requests with its provider, and that the HSS continues
// the IMS's trace.
func TestDiameterIsTraced(t *testing.T) {
	imsSpans, hssSpans := tracetest.NewSpanRecorder(), tracetest.NewSpanRecorder()

	hss := hsstest.New(t, hsstest.Config{
		Realm: imsRealm, IMSHost: imsHost,
		TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(hssSpans)),
	})
	hss.Add(hsstest.Subscriber{
		IMPI: testIMPI, IMSI: testIMSI, K: testK, OPc: testOPc, SQN: 32,
		IMPUs: []cx.ProfileIdentity{{Identity: testIMPU}},
	})

	peer := settings.Peer{
		ID: "hss", Host: hss.Host(), Address: hss.Addr().Addr(), Port: int(hss.Addr().Port()),
		Transport: settings.TransportTCP, Applications: []settings.Application{settings.ApplicationCx},
	}

	cfg := testConfig(t)
	cfg.SIP.Addresses = []netip.Addr{loopback}
	cfg.Peers = seedPeers(peer)
	cfg.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(imsSpans))

	srv := startIMS(t, cfg)
	waitOpen(t, srv, peer.ID)

	ue := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	register := newRegister(ue.Addr())
	register.Header.Add("Expires", "600")
	register.Header.Add("Security-Client", "ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;port-c=6301;"+
		"port-s=6300;alg=hmac-sha-1-96;ealg=null")
	register.Header.Add("Require", "sec-agree")
	register.Header.Add("Proxy-Require", "sec-agree")
	ue.Send(sip.UDP, sipListener(t, srv, rolePCSCF, loopback), register)

	if uar := hss.Next(t).UAR; uar == nil {
		t.Fatal("no UAR")
	}

	var sent, served []sdktrace.ReadOnlySpan

	eventually(t, "the User-Authorization spans", func() bool {
		sent = spansNamed(imsSpans.Ended(), "Cx/User-Authorization", trace.SpanKindClient)
		served = spansNamed(hssSpans.Ended(), "Cx/User-Authorization", trace.SpanKindServer)

		return len(sent) > 0 && len(served) > 0
	})

	if served[0].Parent().SpanID() != sent[0].SpanContext().SpanID() {
		t.Fatalf("the HSS's span has parent %v, want the IMS's request %v", served[0].Parent(), sent[0].SpanContext())
	}
}

// TestRegisterIsOneTrace checks that a UE's REGISTER is one trace through the P-CSCF, I-CSCF and S-CSCF, with their
// Diameter requests in it, which starts at the P-CSCF: the UE is outside the trust domain, and its trace context is
// neither continued nor sent back to it.
func TestRegisterIsOneTrace(t *testing.T) {
	spans := tracetest.NewSpanRecorder()

	hss := hsstest.New(t, hsstest.Config{Realm: imsRealm, IMSHost: imsHost})
	hss.Add(hsstest.Subscriber{
		IMPI: testIMPI, IMSI: testIMSI, K: testK, OPc: testOPc, SQN: 32,
		IMPUs: []cx.ProfileIdentity{{Identity: testIMPU}},
	})

	peer := settings.Peer{
		ID: "hss", Host: hss.Host(), Address: hss.Addr().Addr(), Port: int(hss.Addr().Port()),
		Transport: settings.TransportTCP, Applications: []settings.Application{settings.ApplicationCx},
	}

	cfg := testConfig(t)
	cfg.SIP.Addresses = []netip.Addr{loopback}
	cfg.Peers = seedPeers(peer)
	cfg.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

	srv := startIMS(t, cfg)
	waitOpen(t, srv, peer.ID)

	// The UE is outside the IMS's addresses, as UEs are.
	ue := newPeer(t, srv, netip.MustParseAddrPort("127.0.0.2:6000"))
	register := newRegister(ue.Addr())
	register.Header.Add("Expires", "600")
	register.Header.Add("Security-Client", "ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;port-c=6301;"+
		"port-s=6300;alg=hmac-sha-1-96;ealg=null")
	register.Header.Add("Require", "sec-agree")
	register.Header.Add("Proxy-Require", "sec-agree")
	register.Header.Add("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ue.Send(sip.UDP, sipListener(t, srv, rolePCSCF, loopback), register)

	res, _ := ue.RecvResponse()
	for res.IsProvisional() {
		res, _ = ue.RecvResponse()
	}

	if res.StatusCode != 401 {
		t.Fatalf("REGISTER answered %d, want 401", res.StatusCode)
	}

	if res.Header.Has("traceparent") || res.Header.Has("tracestate") {
		t.Fatalf("trace context sent to the UE: %q", res.Header.Get("traceparent"))
	}

	role := func(r string) sdktrace.ReadOnlySpan {
		for _, s := range spansNamed(spans.Ended(), "REGISTER", trace.SpanKindServer) {
			for _, a := range s.Attributes() {
				if a.Key == "ella.ims.role" && a.Value.AsString() == r {
					return s
				}
			}
		}

		return nil
	}

	var pcscf, icscf, scscf sdktrace.ReadOnlySpan

	eventually(t, "the REGISTER spans of the roles", func() bool {
		pcscf, icscf, scscf = role(rolePCSCF), role(roleICSCF), role(roleSCSCF)
		return pcscf != nil && icscf != nil && scscf != nil
	})

	if pcscf.Parent().IsValid() {
		t.Fatalf("the P-CSCF continues the UE's trace: parent %v", pcscf.Parent())
	}

	tid := pcscf.SpanContext().TraceID()
	byID := map[trace.SpanID]sdktrace.ReadOnlySpan{}

	for _, s := range spans.Ended() {
		if s.SpanContext().TraceID() == tid {
			byID[s.SpanContext().SpanID()] = s
		}
	}

	// under reports whether s descends from ancestor.
	under := func(s, ancestor sdktrace.ReadOnlySpan) bool {
		for p, ok := s, true; ok; p, ok = byID[p.Parent().SpanID()] {
			if p.Parent().SpanID() == ancestor.SpanContext().SpanID() {
				return true
			}
		}

		return false
	}

	if !under(icscf, pcscf) || !under(scscf, icscf) {
		t.Fatalf("the roles' spans are not one chain: %v", names(spans.Ended()))
	}

	for _, want := range []struct {
		name   string
		parent sdktrace.ReadOnlySpan
	}{{"Cx/User-Authorization", icscf}, {"Cx/Multimedia-Auth", scscf}} {
		ds := spansNamed(spans.Ended(), want.name, trace.SpanKindClient)
		if len(ds) != 1 || !under(ds[0], want.parent) {
			t.Fatalf("%s spans %d, want one in the trace of the REGISTER: %v", want.name, len(ds), names(spans.Ended()))
		}
	}
}

func names(spans []sdktrace.ReadOnlySpan) []string {
	var out []string
	for _, s := range spans {
		out = append(out, s.Name()+"/"+s.SpanKind().String())
	}

	return out
}

func spansNamed(spans []sdktrace.ReadOnlySpan, name string, kind trace.SpanKind) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan

	for _, s := range spans {
		if s.Name() == name && s.SpanKind() == kind {
			out = append(out, s)
		}
	}

	return out
}
