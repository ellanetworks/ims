package pcscf

import (
	"slices"
	"testing"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/sip"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// serverSpans are the ended server spans of method, in the order they ended.
func serverSpans(sr *tracetest.SpanRecorder, method string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan

	for _, s := range sr.Ended() {
		if s.Name() == method && s.SpanKind() == trace.SpanKindServer {
			out = append(out, s)
		}
	}

	return out
}

func wantLinkedTo(t *testing.T, s, to sdktrace.ReadOnlySpan) {
	t.Helper()

	if s.Parent().IsValid() {
		t.Fatalf("%s continues a trace from the UE: parent %v", s.Name(), s.Parent())
	}

	var got []trace.SpanID
	for _, l := range s.Links() {
		got = append(got, l.SpanContext.SpanID())
	}

	if !slices.Equal(got, []trace.SpanID{to.SpanContext().SpanID()}) {
		t.Fatalf("%s links %v, want the %s's %s", s.Name(), got, to.Name(), to.SpanContext().SpanID())
	}
}

// A REGISTER from a UE starts a trace of its own: a refresh links to the REGISTER its registration last renewed with.
func TestRegisterRefreshLinksToTheRegisterItRenews(t *testing.T) {
	sr, tp := recorder()
	s, u, _ := newTracedRxIPsecScene(t, tp)

	var registered sdktrace.ReadOnlySpan

	eventually(t, "the REGISTER spans", func() bool {
		rs := serverSpans(sr, "REGISTER")
		if len(rs) < 2 {
			return false
		}

		registered = rs[len(rs)-1]

		return true
	})

	u.uc.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) {
		r.Header.Set("Contact", ueContact(u))
	}))

	req, f, _ := s.forwarded()
	answerRegister(s.icscf, s.scscf.Addr(), req, f, 600)
	wantStatus(t, first(u.us.RecvResponse()), 200)

	var refresh sdktrace.ReadOnlySpan

	eventually(t, "the refresh's span", func() bool {
		rs := serverSpans(sr, "REGISTER")
		refresh = rs[len(rs)-1]

		return refresh.SpanContext().SpanID() != registered.SpanContext().SpanID()
	})

	wantLinkedTo(t, refresh, registered)
}

// A request of a call from a UE starts a trace of its own, linked to the INVITE of the call.
func TestRequestOfACallLinksToItsInvite(t *testing.T) {
	sr, tp := recorder()
	s, u, pcrf := newTracedRxIPsecScene(t, tp)

	e := s.establishConfirmed(t, u, pcrf)

	bye, err := e.ue.NewRequest("BYE")
	if err != nil {
		t.Fatal(err)
	}

	s.ueSend(u, bye)

	fwd, ff := s.scscf.RecvRequest()
	s.scscf.Send(ff.Transport, ff.Remote, sip.NewResponse(fwd, 200, ""))
	wantStatus(t, first(u.us.RecvResponse()), 200)
	pcrf.wantSTR(e.session, rx.TerminationLogout)

	eventually(t, "the BYE span", func() bool { return len(serverSpans(sr, "BYE")) == 1 })

	wantLinkedTo(t, serverSpans(sr, "BYE")[0], serverSpans(sr, "INVITE")[0])
}
