package rxpolicy

import (
	"context"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/internal/policy"
)

// aaRecorder is a PCRF that keeps the AA-Requests it gets and answers them with its features.
type aaRecorder struct {
	t        *testing.T
	features rx.Features
	aars     []rx.AARequest
}

func (r *aaRecorder) Identity() diameter.Identity {
	return diameter.Identity{OriginHost: "ims.ims.example.org", OriginRealm: "ims.example.org"}
}

func (r *aaRecorder) NewSessionID() string { return "ims.ims.example.org;1;1" }

func (r *aaRecorder) Send(_ context.Context, req *diameter.Message, _ ...diameter.RequestOption) (*diameter.Message, error) {
	aar, err := rx.ParseAARequest(req)
	if err != nil {
		r.t.Fatalf("ParseAARequest: %v", err)
	}

	r.aars = append(r.aars, aar)

	return rx.NewAAAnswer(req, diameter.Identity{OriginHost: pcrf1, OriginRealm: pcrfRealm}, rx.AAAnswer{Features: r.features})
}

func (r *aaRecorder) last() rx.AARequest {
	r.t.Helper()

	if len(r.aars) == 0 {
		r.t.Fatal("no AA-Request")
	}

	return r.aars[len(r.aars)-1]
}

func priorityRequest(initial bool, p *policy.MCPTTPriority) policy.Request {
	return policy.Request{
		UE: ue, Initial: initial, Service: "urn:urn-7:3gpp-service.ims.icsi.mcptt", MCPTT: p,
		Components: []policy.MediaComponent{{Number: 1, Type: policy.MediaAudio, Status: policy.FlowEnabled}},
	}
}

func wantMCPTT(t *testing.T, aar rx.AARequest, namespace string, level *rx.ReservationPriority) {
	t.Helper()

	switch {
	case aar.MCPTTIdentifier != namespace:
		t.Errorf("MCPTT-Identifier %q, want %q", aar.MCPTTIdentifier, namespace)
	case level == nil && aar.ReservationPriority != nil:
		t.Errorf("Reservation-Priority %s, want none", *aar.ReservationPriority)
	case level != nil && (aar.ReservationPriority == nil || *aar.ReservationPriority != *level):
		t.Errorf("Reservation-Priority %v, want %s", aar.ReservationPriority, *level)
	}
}

// TS 29.214 Annex A.13.1 NOTE 1, Annex A.13.2, §5.4.1: the MCPTT namespace and priority value go in every AA-Request
// of a PCRF that negotiated MCPTT, the lowest value DEFAULT and the highest PRIORITY-FIFTEEN.
func TestAnnexA13MCPTTPriority(t *testing.T) {
	pcrf := &aaRecorder{t: t, features: rx.FeatureRel8 | rx.FeatureMCPTT}
	b := New(Config{Diameter: pcrf, Realm: func() string { return pcrfRealm }})

	g, err := b.Authorize(t.Context(), "s1", "", priorityRequest(true, &policy.MCPTTPriority{Namespace: "mcpttp", Level: 11}))
	if err != nil {
		t.Fatalf("Authorize initial: %v", err)
	}

	aar := pcrf.last()
	if aar.Features != rx.FeatureRel8|rx.FeatureMCPTT {
		t.Errorf("Supported-Features %s, want Rel8 and MCPTT", aar.Features)
	}

	wantMCPTT(t, aar, "mcpttp", new(rx.ReservationPriority(11)))

	for _, tc := range []struct {
		p    policy.MCPTTPriority
		want rx.ReservationPriority
	}{
		{policy.MCPTTPriority{Namespace: "mcpttp", Level: 15}, rx.ReservationPriorityFifteen},
		{policy.MCPTTPriority{Namespace: "mcpttp", Level: 11}, 11},
		{policy.MCPTTPriority{Namespace: "mcpttq", Level: 0}, rx.ReservationPriorityDefault},
	} {
		if g, err = b.Authorize(t.Context(), "s1", g.Ref, priorityRequest(false, &tc.p)); err != nil {
			t.Fatalf("Authorize update %+v: %v", tc.p, err)
		}

		wantMCPTT(t, pcrf.last(), tc.p.Namespace, &tc.want)
	}

	if _, err = b.Authorize(t.Context(), "s1", g.Ref, priorityRequest(false, nil)); err != nil {
		t.Fatalf("Authorize update: %v", err)
	}

	wantMCPTT(t, pcrf.last(), "", nil)
}

// TS 29.214 §5.4.1: the initial AA-Request sends the MCPTT priority before the PCRF tells its features; the
// updates of a session whose PCRF does not support MCPTT leave it out.
func TestAnnexA13MCPTTPriorityNotNegotiated(t *testing.T) {
	pcrf := &aaRecorder{t: t, features: rx.FeatureRel8}
	b := New(Config{Diameter: pcrf, Realm: func() string { return pcrfRealm }})

	p := &policy.MCPTTPriority{Namespace: "mcpttq", Level: 7}

	g, err := b.Authorize(t.Context(), "s1", "", priorityRequest(true, p))
	if err != nil {
		t.Fatalf("Authorize initial: %v", err)
	}

	wantMCPTT(t, pcrf.last(), "mcpttq", new(rx.ReservationPriority(7)))

	if _, err := b.Authorize(t.Context(), "s1", g.Ref, priorityRequest(false, p)); err != nil {
		t.Fatalf("Authorize update: %v", err)
	}

	wantMCPTT(t, pcrf.last(), "", nil)
}
