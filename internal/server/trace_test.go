package server

import (
	"net/netip"
	"testing"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

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

func TestTelemetryOffTracesNothing(t *testing.T) {
	global := tracetest.NewSpanRecorder()

	previous := otel.GetTracerProvider()

	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(global)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

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

	srv := startIMS(t, cfg)
	waitOpen(t, srv, peer.ID)

	ue := newPeer(t, srv, netip.MustParseAddrPort("127.0.0.2:6000"))
	register := newRegister(ue.Addr())
	register.Header.Add("Expires", "600")
	register.Header.Add("Security-Client", "ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;port-c=6301;"+
		"port-s=6300;alg=hmac-sha-1-96;ealg=null")
	register.Header.Add("Require", "sec-agree")
	register.Header.Add("Proxy-Require", "sec-agree")
	ue.Send(sip.UDP, sipListener(t, srv, rolePCSCF, loopback), register)

	res, _ := ue.RecvResponse()
	for res.IsProvisional() {
		res, _ = ue.RecvResponse()
	}

	if res.StatusCode != 401 {
		t.Fatalf("REGISTER answered %d, want 401", res.StatusCode)
	}

	getDiameter(t, srv)

	if started := global.Started(); len(started) != 0 {
		t.Fatalf("telemetry off traced with the global provider: %v", names(global.Ended()))
	}
}

func TestSettingsChangeIsLinkedToTheAPIRequest(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

	cfg := testConfig(t)
	cfg.TracerProvider = tp

	srv := startIMS(t, cfg)
	before := srv.core.Load()

	op := srv.settings.Get().Operator
	op.MNC = "02"

	ctx, request := tp.Tracer("test").Start(t.Context(), "PUT /api/v1/operator")
	if err := srv.settings.UpdateOperator(ctx, op); err != nil {
		t.Fatalf("UpdateOperator: %v", err)
	}

	request.End()

	eventually(t, "the core under the new identity", func() bool {
		c := srv.core.Load()
		return c != nil && c != before
	})

	var applied []sdktrace.ReadOnlySpan

	eventually(t, "the span of the restart", func() bool {
		applied = spansNamed(spans.Ended(), "apply settings", trace.SpanKindInternal)
		return len(applied) > 0
	})

	s := applied[0]

	if len(applied) != 1 || s.Parent().IsValid() || s.SpanContext().TraceID() == request.SpanContext().TraceID() {
		t.Fatalf("%d restart spans, parent %v: want one root of its own", len(applied), s.Parent())
	}

	if l := s.Links(); len(l) != 1 || !l[0].SpanContext.Equal(request.SpanContext()) {
		t.Fatalf("restart links %+v, want the request %v", l, request.SpanContext())
	}

	attrs := attribute.NewSet(s.Attributes()...)
	if v, _ := attrs.Value("ella.ims.role"); v.AsString() != roleIMS {
		t.Fatalf("restart role %q, want %q", v.AsString(), roleIMS)
	}

	if s.Status().Code != codes.Unset {
		t.Fatalf("restart status %v, want unset", s.Status())
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
