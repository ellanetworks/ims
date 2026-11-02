package n5policy

import (
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/n5"
	"github.com/ellanetworks/ims/internal/pcftest"
	"github.com/ellanetworks/ims/internal/policy"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func spanNamed(t *testing.T, rec *tracetest.SpanRecorder, name string, kind trace.SpanKind) sdktrace.ReadOnlySpan {
	t.Helper()

	var found []sdktrace.ReadOnlySpan

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		found = found[:0]

		for _, s := range rec.Ended() {
			if s.Name() == name && s.SpanKind() == kind {
				found = append(found, s)
			}
		}

		if len(found) > 0 {
			break
		}
	}

	if len(found) != 1 {
		t.Fatalf("%d %v spans %q, want 1", len(found), kind, name)
	}

	return found[0]
}

func attr(s sdktrace.ReadOnlySpan, key attribute.Key) string {
	attrs := attribute.NewSet(s.Attributes()...)
	v, _ := attrs.Value(key)

	return v.AsString()
}

func TestRequestsToThePCFAreTraced(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	f := newTracedFixture(t, pcftest.Config{}, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	id := f.b.NewSessionID()

	uri, err := f.b.OpenSignalling(f.ctx(), id, policy.Signalling{UE: netip.MustParseAddr("2001:db8::1")}, false)
	if err != nil {
		t.Fatalf("OpenSignalling: %v", err)
	}

	created := f.pcf.Next(t)

	if err := f.b.Terminate(f.ctx(), id, uri, policy.TerminationLogout, false); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	deleted := f.pcf.Next(t)

	for _, tc := range []struct {
		name     string
		template string
		received pcftest.Request
	}{
		{"POST " + n5.AppSessionsPath, n5.AppSessionsPath, created},
		{"POST " + n5.AppSessionsPath + "/{appSessionId}/delete", n5.AppSessionsPath + "/{appSessionId}/delete", deleted},
	} {
		s := spanNamed(t, rec, tc.name, trace.SpanKindClient)

		if got := attr(s, "url.template"); got != tc.template {
			t.Errorf("%s url.template = %q, want %q", tc.name, got, tc.template)
		}

		sc := s.SpanContext()
		if want := "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"; tc.received.Traceparent != want {
			t.Errorf("%s reached the PCF with traceparent %q, want the span's %q", tc.name, tc.received.Traceparent, want)
		}
	}
}

func TestNotificationsContinueThePCFsTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	f := newTracedFixture(t, pcftest.Config{}, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	id := f.b.NewSessionID()

	g, err := f.b.Authorize(f.ctx(), id, "", callRequest(true, audio(), video()))
	if err != nil {
		t.Fatal(err)
	}

	f.pcf.Next(t)
	f.sink.add(id)

	pcfCtx, pcfSpan := sdktrace.NewTracerProvider().Tracer("pcf").Start(f.ctx(), "notify")
	defer pcfSpan.End()

	failed := []n5.AfEventNotification{{Event: n5.EventFailedResourcesAllocation}}

	res, err := f.pcf.Notify(pcfCtx, g.Ref, n5.EventsNotification{EvNotifs: failed})
	if err != nil || res.Status != http.StatusNoContent {
		t.Fatalf("notify = %+v, %v, want 204", res, err)
	}

	<-f.sink.notifies

	route := SessionsPath + "/{id}/" + n5.NotifySegment
	s := spanNamed(t, rec, "POST "+route, trace.SpanKindServer)

	if got := attr(s, "http.route"); got != route {
		t.Errorf("http.route = %q, want %q", got, route)
	}

	if s.Parent().SpanID() != pcfSpan.SpanContext().SpanID() || len(s.Links()) != 0 {
		t.Errorf("notification span parent %v, links %+v; want a child of the PCF's span", s.Parent(), s.Links())
	}
}

func TestOrphanDeleteIsLinkedToTheTermination(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	f := newTracedFixture(t, pcftest.Config{}, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	id := f.b.NewSessionID()

	uri, err := f.b.OpenSignalling(f.ctx(), id, policy.Signalling{UE: netip.MustParseAddr("2001:db8::1")}, false)
	if err != nil {
		t.Fatalf("OpenSignalling: %v", err)
	}

	f.pcf.Next(t)

	res, err := f.pcf.Terminate(f.ctx(), uri, n5.TerminationPDUSessionTermination)
	if err != nil || res.Status != http.StatusNoContent {
		t.Fatalf("terminate = %+v, %v, want 204", res, err)
	}

	if r := f.pcf.Next(t); r.Op != n5.OpDelete {
		t.Fatalf("got %s, want the delete", r)
	}

	terminate := spanNamed(t, rec, "POST "+SessionsPath+"/{id}/"+n5.TerminateSegment, trace.SpanKindServer)
	orphan := spanNamed(t, rec, "delete orphan context", trace.SpanKindInternal)
	del := spanNamed(t, rec, "POST "+n5.AppSessionsPath+"/{appSessionId}/delete", trace.SpanKindClient)

	if orphan.Parent().IsValid() || orphan.SpanContext().TraceID() == terminate.SpanContext().TraceID() {
		t.Errorf("orphan delete has parent %v in trace %v, want a root of its own", orphan.Parent(), orphan.SpanContext().TraceID())
	}

	if l := orphan.Links(); len(l) != 1 || !l[0].SpanContext.Equal(terminate.SpanContext()) {
		t.Errorf("orphan delete links %+v, want the termination %v", l, terminate.SpanContext())
	}

	if got := attr(orphan, "ella.ims.role"); got != "pcscf" {
		t.Errorf("orphan delete role %q, want pcscf", got)
	}

	if del.Parent().SpanID() != orphan.SpanContext().SpanID() {
		t.Errorf("DELETE parent %v, want the orphan delete %v", del.Parent(), orphan.SpanContext())
	}
}
