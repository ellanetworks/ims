package pcscf

import (
	"context"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/rxpolicy"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func noTrace() tracing.Detached {
	return tracing.NewDetached(nil, tracerName, "pcscf")
}

func recorder() (*tracetest.SpanRecorder, trace.TracerProvider) {
	sr := tracetest.NewSpanRecorder()

	return sr, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
}

func traceDetached(p *PCSCF, tp trace.TracerProvider) {
	d := tracing.NewDetached(tp, tracerName, "pcscf")
	p.detached = d
	p.regs.detached = d

	if p.sas != nil {
		p.sas.detached = d
	}
}

func startCause(tp trace.TracerProvider, name string) (context.Context, trace.Span) {
	return tp.Tracer("test").Start(context.Background(), name)
}

func endedSpan(t *testing.T, sr *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()

	var found sdktrace.ReadOnlySpan

	eventually(t, "the span "+name, func() bool {
		for _, s := range sr.Ended() {
			if s.Name() == name {
				found = s
				return true
			}
		}

		return false
	})

	return found
}

func wantDetached(t *testing.T, s sdktrace.ReadOnlySpan, cause trace.SpanContext) {
	t.Helper()

	if s.Parent().IsValid() || s.SpanContext().TraceID() == cause.TraceID() {
		t.Errorf("%s: parent %v, trace %s; want the root of a trace of its own", s.Name(), s.Parent(), s.SpanContext().TraceID())
	}

	if s.SpanKind() != trace.SpanKindInternal {
		t.Errorf("%s: kind %s, want internal", s.Name(), s.SpanKind())
	}

	if s.InstrumentationScope().Name != tracerName {
		t.Errorf("%s: scope %q, want %q", s.Name(), s.InstrumentationScope().Name, tracerName)
	}

	role := false

	for _, a := range s.Attributes() {
		role = role || a.Key == tracing.RoleKey && a.Value.AsString() == "pcscf"
	}

	if !role {
		t.Errorf("%s: attributes %v, want %s=pcscf", s.Name(), s.Attributes(), tracing.RoleKey)
	}

	if links := s.Links(); len(links) != 1 || !links[0].SpanContext.Equal(cause) {
		t.Errorf("%s: links %v, want one to %s", s.Name(), links, cause.SpanID())
	}
}

func TestPolicyRequestInTheTraceOfItsCause(t *testing.T) {
	_, tp := recorder()

	c := newPolicyClient(Policy{Backend: unanswered{}}, slog.New(slog.DiscardHandler), time.Now)

	ctx, span := startCause(tp, "INVITE")
	ctx, cancelCause := context.WithCancel(ctx)

	rctx, cancel := c.deadline(ctx, 0)
	defer cancel()

	cancelCause()
	span.End()

	if got := trace.SpanContextFromContext(rctx); !got.Equal(span.SpanContext()) {
		t.Errorf("request span context %v, want the cause's %v", got, span.SpanContext())
	}

	if err := rctx.Err(); err != nil {
		t.Fatalf("request ended with its cause: %v", err)
	}

	if _, ok := rctx.Deadline(); !ok {
		t.Error("request without a deadline")
	}

	c.close(context.Background())

	select {
	case <-rctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("request outlived the client")
	}
}

func TestRegistrationExpiryIsDetached(t *testing.T) {
	sr, tp := recorder()
	clk := fakeClock{siptest.NewClock()}

	life, stop := context.WithCancel(context.Background())
	defer stop()

	rs := newRegistrations(life, nil, clk, time.Minute, slog.New(slog.DiscardHandler), tracing.NewDetached(tp, tracerName, "pcscf"))

	expired := make(chan context.Context, 1)
	rs.expired = func(ctx context.Context, _ db.PCSCFRegistration) { expired <- ctx }

	ctx, register := startCause(tp, "REGISTER")
	rs.save(ctx, db.PCSCFRegistration{
		IMPI: testIMPI, FlowToken: "t", UEAddress: netip.AddrPortFrom(ueAddr, 5060), PCSCFAddress: loopback,
		ExpiresAt: clk.Now().Add(time.Minute),
	})
	register.End()

	clk.Advance(time.Minute)

	var got context.Context

	select {
	case got = <-expired:
	default:
		t.Fatal("registration not expired")
	}

	s := endedSpan(t, sr, "registration expiry")
	wantDetached(t, s, register.SpanContext())

	if sc := trace.SpanContextFromContext(got); !sc.Equal(s.SpanContext()) {
		t.Errorf("expiry ctx span %v, want the expiry's", sc)
	}

	if got.Err() != nil {
		t.Fatal("expiry ctx done before the P-CSCF stopped")
	}

	stop()

	if got.Err() == nil {
		t.Error("expiry ctx outlived the P-CSCF")
	}
}

func TestRxSTROnExpiryInItsTrace(t *testing.T) {
	sr, tp := recorder()
	s, pcrf := newRxScene(t, 0)
	traceDetached(s.p, tp)

	strs := pcrf.contexts(rx.CommandSessionTermination)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	s.clock.Advance(600 * time.Second)
	pcrf.wantSTR(id, rx.TerminationAuthExpired)

	ctx := strs.of(id)
	expiry := endedSpan(t, sr, "registration expiry")

	if got := trace.SpanContextFromContext(ctx); got.TraceID() != expiry.SpanContext().TraceID() {
		t.Errorf("STR in trace %s, want the expiry's %s", got.TraceID(), expiry.SpanContext().TraceID())
	}
}

func TestRegEventRefreshIsDetached(t *testing.T) {
	sr, tp := recorder()
	s := newRegScene(t)
	traceDetached(s.p, tp)

	_, sub, f := s.registered(7200)
	answerSubscribe(s.icscf, s.scscf, sub, f, 3000)

	eventually(t, "the refresh and the registration expiry to be scheduled", func() bool { return s.clock.Pending() == 2 })

	ctx, armed := startCause(tp, "NOTIFY")

	s.p.subs.mu.Lock()
	s.p.subs.schedule(ctx, s.p.subs.byIMPI[testIMPI])
	s.p.subs.mu.Unlock()

	armed.End()

	s.clock.Advance(2400 * time.Second)

	if refresh, _ := s.scscf.RecvRequest(); refresh.Method != "SUBSCRIBE" {
		t.Fatalf("got %s, want the refresh", refresh.StartLine())
	}

	wantDetached(t, endedSpan(t, sr, "reg event refresh"), armed.SpanContext())
}

func TestMediaLossIsDetached(t *testing.T) {
	sr, tp := recorder()
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	traceDetached(s.p, tp)

	e := s.establishConfirmed(t, u, pcrf)
	strs := pcrf.contexts(rx.CommandSessionTermination)

	ctx, rar := startCause(tp, "RAR")
	if !s.p.cfg.Policy.Backend.(*rxpolicy.Backend).ReAuth(ctx, e.session, rx.ReAuthRequest{
		SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer},
	}) {
		t.Fatal("ReAuth reported an unknown session")
	}

	rar.End()

	clk.Advance(lossTimeout)

	if bye, _ := s.scscf.RecvRequest(); bye.Method != "BYE" {
		t.Fatalf("S-CSCF got %s, want the BYE", bye.Method)
	}

	loss := endedSpan(t, sr, "media loss")
	wantDetached(t, loss, rar.SpanContext())

	pcrf.wantSTR(e.session, rx.TerminationLogout)

	if got := trace.SpanContextFromContext(strs.of(e.session)); got.TraceID() != loss.SpanContext().TraceID() {
		t.Errorf("STR in trace %s, want the media loss's %s", got.TraceID(), loss.SpanContext().TraceID())
	}
}

func (f *fakePCRF) contexts(command uint32) *requestContexts {
	r := &requestContexts{t: f.t, c: make(chan requestContext, 8)}

	f.answerWith(func(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode == command {
			r.c <- requestContext{ctx: ctx, session: tgpp.ParseEnvelope(req).SessionID}
		}

		return succeed(req)
	})

	return r
}

type requestContext struct {
	ctx     context.Context
	session string
}

type requestContexts struct {
	t *testing.T
	c chan requestContext
}

func (r *requestContexts) of(session string) context.Context {
	r.t.Helper()

	for {
		select {
		case got := <-r.c:
			if got.session == session {
				return got.ctx
			}
		case <-time.After(5 * time.Second):
			r.t.Fatalf("no request for %s", session)
		}
	}
}

func newTracedRxIPsecScene(t *testing.T, tp trace.TracerProvider) (*ipsecScene, *ue, *fakePCRF) {
	t.Helper()

	f := newFakePCRF(t)
	kernel := ipsectest.NewKernel()

	s := newIPsecSceneAt(t, loopback, ipsec.DefaultPolicy(), kernel, openStore(t), transaction.Config{TracerProvider: tp},
		f.config(0))
	s.kernel = kernel
	s.ue = siptest.NewSocket(t, netip.AddrPortFrom(ueAddr, 0))

	u := newUEAt(t, ueAddr, 25656)
	s.registerOverIPsec(u)
	f.aar()

	return s, u, f
}

func wantInTraceOf(t *testing.T, sr *tracetest.SpanRecorder, method string, ctx context.Context) {
	t.Helper()

	var found sdktrace.ReadOnlySpan

	eventually(t, "the "+method+" server span", func() bool {
		for _, s := range sr.Ended() {
			if s.Name() == method && s.SpanKind() == trace.SpanKindServer {
				found = s
			}
		}

		return found != nil
	})

	if got := trace.SpanContextFromContext(ctx); got.TraceID() != found.SpanContext().TraceID() {
		t.Errorf("policy request in trace %s, want the %s's %s", got.TraceID(), method, found.SpanContext().TraceID())
	}
}

func TestCallPolicyInTheTraceOfItsRequest(t *testing.T) {
	sr, tp := recorder()
	s, u, pcrf := newTracedRxIPsecScene(t, tp)

	aars := pcrf.contexts(rx.CommandAA)

	e := s.establishConfirmed(t, u, pcrf)
	wantInTraceOf(t, sr, "INVITE", aars.of(e.session))

	strs := pcrf.contexts(rx.CommandSessionTermination)

	bye, err := e.ue.NewRequest("BYE")
	if err != nil {
		t.Fatal(err)
	}

	s.ueSend(u, bye)

	fwd, ff := s.scscf.RecvRequest()
	s.scscf.Send(ff.Transport, ff.Remote, sip.NewResponse(fwd, 200, ""))
	wantStatus(t, first(u.us.RecvResponse()), 200)
	pcrf.wantSTR(e.session, rx.TerminationLogout)

	wantInTraceOf(t, sr, "BYE", strs.of(e.session))
}
