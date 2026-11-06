//go:build linux && (amd64 || arm64)

package integration

import (
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/testue"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func newTracedTrunkScene(t *testing.T) (*scene, *carrier, string, *tracetest.SpanRecorder) {
	t.Helper()

	spans := tracetest.NewSpanRecorder()

	s := newSceneWith(t, func(srv *server.Server) {
		srv.Config.IBCF = &config.IBCF{
			Port:     5085,
			External: config.IBCFExternal{Addresses: []config.IBCFAddress{{Address: ibcfExternal.Addr()}}},
			Media:    config.IBCFMedia{MinPort: 10000, MaxPort: 10999},
		}
		srv.TrunkMediaTimeout = time.Minute
		srv.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	})
	c := newCarrier(t, carrierSIP)

	id := s.addTrunk("Carrier", c, false, 0)
	s.post("/api/v1/sip-routes", map[string]any{"prefix": "+33", "trunks": []string{id}}, nil)

	return s, c, id, spans
}

func spanAttr(s sdktrace.ReadOnlySpan, k attribute.Key) string {
	for _, a := range s.Attributes() {
		if a.Key == k {
			return a.Value.String()
		}
	}

	return ""
}

func inviteSpans(spans *tracetest.SpanRecorder, role string, kind trace.SpanKind) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan

	for _, s := range spans.Ended() {
		if s.Name() == "INVITE" && s.SpanKind() == kind && spanAttr(s, "ella.ims.role") == role {
			out = append(out, s)
		}
	}

	return out
}

func TestTrunkOutgoingCallTraceAndRecord(t *testing.T) {
	s, c, id, spans := newTracedTrunkScene(t)
	a := s.caller(0, false, testue.Config{})
	ctx := s.ctx()

	ac := invite(t, a, "tel:"+offNet, testue.CallOptions{})

	in := c.next("invite").req
	if h := in.Message().Header; h.Has("traceparent") || h.Has("tracestate") {
		t.Errorf("trace context sent to the trunk: %q", h.Get("traceparent"))
	}

	answerWithOffer(t, in)

	if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v", res, err)
	}

	c.next("established")

	if err := ac.Bye(ctx); err != nil {
		t.Fatalf("Bye: %v", err)
	}

	c.next("bye")
	ended(t, ac, testue.LocalBye)

	r := s.callRecord(ac)
	if r.OutTrunkID != id || r.OutTrunkName == "" || r.InTrunkID != "" {
		t.Errorf("record over %q %q (in %q), want out %s", r.OutTrunkID, r.OutTrunkName, r.InTrunkID, id)
	}

	var trunk, scscf []sdktrace.ReadOnlySpan

	eventually(t, "the INVITE spans of the S-CSCF and the IBCF", func() bool {
		trunk = inviteSpans(spans, "ibcf", trace.SpanKindClient)
		scscf = inviteSpans(spans, "scscf", trace.SpanKindServer)

		return len(trunk) == 1 && len(scscf) > 0
	})

	if spanAttr(trunk[0], "ella.ims.trunk.id") != id {
		t.Errorf("trunk span attributes %v", trunk[0].Attributes())
	}

	shared := false

	for _, sc := range scscf {
		shared = shared || sc.SpanContext().TraceID() == trunk[0].SpanContext().TraceID()
	}

	if !shared {
		t.Error("the INVITE to the trunk is not in the trace of the S-CSCF")
	}
}

func TestTrunkIncomingCallTraceAndRecord(t *testing.T) {
	s, c, id, spans := newTracedTrunkScene(t)
	b := s.caller(1, false, testue.Config{})
	ctx := s.ctx()

	c.invite("+15550002")

	bc := <-b.MMTel().Calls()

	if err := bc.Answer(ctx); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	e := c.next("established")

	if err := e.s.Bye(ctx, ua.Change{}, nil); err != nil {
		t.Fatal(err)
	}

	ended(t, bc, testue.RemoteBye)

	var r api.CallRecordResponse

	eventually(t, "the call record of the incoming call", func() bool {
		for _, x := range s.callRecords() {
			if x.InTrunkID != "" && !x.InProgress {
				r = x

				return true
			}
		}

		return false
	})

	if r.InTrunkID != id || r.InTrunkName == "" || r.OutTrunkID != "" || r.Outcome != "answered" || r.EndedBy != "caller" {
		t.Errorf("record %+v", r)
	}

	var entry, out, icscf []sdktrace.ReadOnlySpan

	eventually(t, "the INVITE spans of the IBCF and the I-CSCF", func() bool {
		entry = inviteSpans(spans, "ibcf", trace.SpanKindServer)
		out = inviteSpans(spans, "ibcf", trace.SpanKindClient)
		icscf = inviteSpans(spans, "icscf", trace.SpanKindServer)

		return len(entry) == 1 && len(out) == 1 && len(icscf) > 0
	})

	if entry[0].Parent().IsValid() || spanAttr(entry[0], "ella.ims.trunk.id") != id || spanAttr(entry[0], "ella.ims.icid") != r.ICID {
		t.Errorf("IBCF span parent %v, attributes %v", entry[0].Parent(), entry[0].Attributes())
	}

	if icscf[0].Parent().SpanID() != out[0].SpanContext().SpanID() {
		t.Errorf("the I-CSCF's INVITE has parent %v, want the IBCF's %v", icscf[0].Parent(), out[0].SpanContext())
	}
}
