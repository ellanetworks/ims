package ibcf

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func tracedScene(t *testing.T, trusted bool, configure func(*Config)) (*scene, *tracetest.SpanRecorder) {
	t.Helper()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

	return newTracedScene(t, trusted, tp, configure), rec
}

func attrOf(s sdktrace.ReadOnlySpan, k attribute.Key) (attribute.Value, bool) {
	for _, a := range s.Attributes() {
		if a.Key == k {
			return a.Value, true
		}
	}

	return attribute.Value{}, false
}

func spansOf(rec *tracetest.SpanRecorder, role, name string, kind trace.SpanKind) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan

	for _, s := range rec.Ended() {
		if r, _ := attrOf(s, tracing.RoleKey); r.AsString() == role && s.Name() == name && s.SpanKind() == kind {
			out = append(out, s)
		}
	}

	return out
}

func oneSpan(t *testing.T, rec *tracetest.SpanRecorder, role, name string, kind trace.SpanKind) sdktrace.ReadOnlySpan {
	t.Helper()

	spans := spansOf(rec, role, name, kind)
	if len(spans) != 1 {
		t.Fatalf("%d %s %s spans of %s, want 1", len(spans), name, kind, role)
	}

	return spans[0]
}

func wantSpanAttr(t *testing.T, s sdktrace.ReadOnlySpan, k attribute.Key, want any) {
	t.Helper()

	v, ok := attrOf(s, k)
	if !ok || v.AsInterface() != want {
		t.Errorf("%s %s: %s = %v, want %v", s.Name(), s.SpanKind(), k, v.AsInterface(), want)
	}
}

func noTraceContext(t *testing.T, p *party, methods ...string) {
	t.Helper()

	var got []string

	for _, m := range p.received() {
		h := m.Env().Header
		if h.Has("traceparent") || h.Has("tracestate") {
			t.Errorf("trace context sent to the trunk: %s", m.StartLine())
		}

		if req, ok := m.(*sip.Request); ok && !slices.Contains(got, req.Method) {
			got = append(got, req.Method)
		}
	}

	for _, m := range methods {
		if !slices.Contains(got, m) {
			t.Errorf("no %s reached the trunk: %v", m, got)
		}
	}
}

func TestNoTraceContextTowardsTheTrunk(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "untrusted", true: "trusted"}[trusted], func(t *testing.T) {
			s, _ := tracedScene(t, trusted, nil)

			s.call("offer-1")

			in := s.carrier.next("invite").req
			_ = in.Accept(sdpOf("answer-1")...)

			e := s.ims.next("established")
			s.ims.next("answer")
			s.carrier.next("established")

			_ = e.s.Reinvite(context.Background(), ua.Change{Bodies: sdpOf("offer-2")}, nil)

			r := s.carrier.next("request").req
			_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf("answer-2")})

			s.ims.until("answer")

			_ = e.s.Bye(context.Background(), ua.Change{}, nil)

			s.carrier.next("bye")

			s.carrier.until("ended")
			s.ims.until("ended")

			o := s.call("offer-3")

			s.carrier.until("invite")

			_ = o.Cancel(context.Background(), ua.Change{})

			s.carrier.until("ended")

			sent := make(chan error, 1)

			go func() {
				_, err := s.ibcf.Send(context.Background(), keepalive(), sip.Server{Transport: s.carrier.tr, Addr: carrierAddr})
				sent <- err
			}()

			s.net.Quiesce()

			if err := <-sent; err != nil {
				t.Fatalf("OPTIONS: %v", err)
			}

			noTraceContext(t, s.carrier, "INVITE", "ACK", "BYE", "CANCEL", "OPTIONS")
		})
	}
}

func keepalive() *sip.Request {
	target := sip.URI{Scheme: "sip", Host: "sip.carrier.example"}
	req := sip.NewRequest("OPTIONS", target)

	from := sip.Address{URI: sip.URI{Scheme: "sip", Host: home}}
	from.Params.Set("tag", sip.NewTag())

	req.Header.Set("Max-Forwards", "70")
	req.Header.Set("To", sip.Address{URI: target}.String())
	req.Header.Set("From", from.String())
	req.Header.Set("Call-ID", sip.NewTag())
	req.Header.Set("CSeq", sip.CSeq{Seq: 1, Method: "OPTIONS"}.String())

	return req
}

func TestExitSpansJoinTheCallersTrace(t *testing.T) {
	s, rec := tracedScene(t, false, nil)

	s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Reject(486)

	s.ims.next("failed")
	s.net.Quiesce()

	caller := oneSpan(t, rec, "ims", "INVITE", trace.SpanKindClient)
	server := oneSpan(t, rec, role, "INVITE", trace.SpanKindServer)
	client := oneSpan(t, rec, role, "INVITE", trace.SpanKindClient)

	if server.Parent().SpanID() != caller.SpanContext().SpanID() {
		t.Errorf("the IBCF's INVITE has parent %v, want the caller's %v", server.Parent(), caller.SpanContext())
	}

	if client.SpanContext().TraceID() != caller.SpanContext().TraceID() {
		t.Errorf("the INVITE to the trunk is in trace %s, want the caller's %s", client.SpanContext().TraceID(), caller.SpanContext().TraceID())
	}

	wantSpanAttr(t, server, tracing.ICIDKey, "icid-1")
	wantSpanAttr(t, client, tracing.TrunkIDKey, "carrier")
	wantSpanAttr(t, client, "server.address", carrierAddr.Addr().String())
	wantSpanAttr(t, client, "network.peer.address", carrierAddr.Addr().String())
	wantSpanAttr(t, client, "network.peer.port", int64(carrierAddr.Port()))

	if _, ok := attrOf(server, "network.peer.address"); !ok {
		t.Error("no network.peer.address on the IBCF's server span of the trust domain")
	}

	if _, ok := attrOf(server, tracing.TrunkIDKey); ok {
		t.Error("trunk ID on the server span of the I-CSCF's INVITE")
	}
}

func TestEntrySpansStartATrace(t *testing.T) {
	s, rec := tracedScene(t, false, nil)

	s.dial(s.carrier, "offer-1", f("P-Charging-Vector", "icid-value=carrier-icid"))

	r := s.ims.next("invite").req
	icid := icidOf(r.Message().Header)

	_ = r.Reject(480)

	s.carrier.next("failed")
	s.net.Quiesce()

	carrier := oneSpan(t, rec, "carrier", "INVITE", trace.SpanKindClient)
	server := oneSpan(t, rec, role, "INVITE", trace.SpanKindServer)
	client := oneSpan(t, rec, role, "INVITE", trace.SpanKindClient)
	icscf := oneSpan(t, rec, "ims", "INVITE", trace.SpanKindServer)

	if server.Parent().IsValid() || server.SpanContext().TraceID() == carrier.SpanContext().TraceID() {
		t.Errorf("the IBCF continues the carrier's trace: parent %v", server.Parent())
	}

	if icscf.Parent().SpanID() != client.SpanContext().SpanID() || client.Parent().SpanID() != server.SpanContext().SpanID() {
		t.Errorf("the I-CSCF's INVITE is not a child of the IBCF's: %v, %v", icscf.Parent(), client.Parent())
	}

	if icid == "" || icid == "carrier-icid" {
		t.Fatalf("ICID towards the I-CSCF %q", icid)
	}

	wantSpanAttr(t, server, tracing.ICIDKey, icid)
	wantSpanAttr(t, server, tracing.TrunkIDKey, "carrier")
	wantSpanAttr(t, server, "network.peer.address", carrierAddr.Addr().String())
	wantSpanAttr(t, server, "network.peer.port", int64(carrierAddr.Port()))
}

func TestOptionsOfTheTrunkHaveNoSpan(t *testing.T) {
	s, rec := tracedScene(t, false, nil)

	req := sip.NewRequest("OPTIONS", sip.URI{Scheme: "sip", Host: externalAddr.Addr().String(), Port: externalAddr.Port()})
	req.Header.Add("From", "<sip:carrier@sip.carrier.example>")
	req.Header.Add("To", "<sip:"+externalAddr.String()+">")

	done := make(chan *sip.Response, 1)

	if err := s.carrier.a.Request(context.Background(), req, func(res *sip.Response, _ error) { done <- res }); err != nil {
		t.Fatal(err)
	}

	sent := make(chan error, 1)

	go func() {
		_, err := s.ibcf.Send(context.Background(), keepalive(), sip.Server{Transport: sip.UDP, Addr: carrierAddr})
		sent <- err
	}()

	s.net.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 200 {
		t.Fatalf("OPTIONS: %v", res)
	}

	if err := <-sent; err != nil {
		t.Fatalf("keepalive: %v", err)
	}

	for _, kind := range []trace.SpanKind{trace.SpanKindServer, trace.SpanKindClient} {
		if spans := spansOf(rec, role, "OPTIONS", kind); len(spans) != 0 {
			t.Errorf("%d OPTIONS %s spans", len(spans), kind)
		}
	}
}

func TestResolutionFailureIsAnEvent(t *testing.T) {
	s, rec := tracedScene(t, false, func(c *Config) {
		c.Resolve = func(context.Context, sip.URI, []sip.Transport) ([]sip.Server, error) {
			return nil, errors.New("no such host")
		}
	})

	tr := s.trunk
	tr.Addresses = []settings.TrunkAddress{{Host: "sip.carrier.example", Port: 5060, Transport: settings.SIPTransportUDP}}
	s.setTrunk(tr)

	s.call("offer-1")

	if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != 503 {
		t.Fatalf("got %+v, want 503", e.res)
	}

	s.net.Quiesce()

	server := oneSpan(t, rec, role, "INVITE", trace.SpanKindServer)

	i := slices.IndexFunc(server.Events(), func(e sdktrace.Event) bool { return e.Name == resolutionEvent })
	if i < 0 {
		t.Fatalf("no %s event: %v", resolutionEvent, server.Events())
	}

	attrs := attribute.NewSet(server.Events()[i].Attributes...)

	if v, _ := attrs.Value("server.address"); v.AsString() != "sip.carrier.example" {
		t.Errorf("server.address %q", v.AsString())
	}

	if v, _ := attrs.Value("error.type"); v.AsString() != "_OTHER" {
		t.Errorf("error.type %q", v.AsString())
	}
}
