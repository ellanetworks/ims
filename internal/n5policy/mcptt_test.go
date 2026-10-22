package n5policy

import (
	"encoding/json"
	"testing"

	"github.com/ellanetworks/ims/internal/n5"
	"github.com/ellanetworks/ims/internal/pcftest"
	"github.com/ellanetworks/ims/internal/policy"
)

func mcpttRequest(initial bool, p *policy.MCPTTPriority) policy.Request {
	r := callRequest(initial, audio())
	r.Service = "urn:urn-7:3gpp-service.ims.icsi.mcptt"
	r.MCPTT = p

	return r
}

// TS 29.514 §4.2.2.19, §4.2.3.19, Annex B.10.1 NOTE 1, Annex B.10.2: the MCPTT namespace in mcpttId and the
// priority value n in resPrio as PRIO_(n+1), at the creation and whenever the priority changes.
func TestAnnexB10MCPTTPriority(t *testing.T) {
	f := newFixtureWith(t, pcftest.Config{Features: n5.Features(n5.FeatureIMSSBI, n5.FeatureMCPTT, n5.FeaturePatchCorrection)})
	id := f.b.NewSessionID()

	g, err := f.b.Authorize(f.ctx(), id, "", mcpttRequest(true, &policy.MCPTTPriority{Namespace: "mcpttp", Level: 11}))
	if err != nil {
		t.Fatalf("Authorize initial: %v", err)
	}

	r := f.pcf.Next(t)
	if c := r.Create.AscReqData; r.Op != n5.OpCreate || c.McpttID != "mcpttp" || c.ResPrio != "PRIO_12" ||
		!c.SuppFeat.Has(n5.FeatureMCPTT) {
		t.Fatalf("got %s, want a create with mcpttp, PRIO_12 and the MCPTT feature", r)
	}

	for _, tc := range []struct {
		p    policy.MCPTTPriority
		want n5.ReservPriority
	}{
		{policy.MCPTTPriority{Namespace: "mcpttp", Level: 15}, "PRIO_16"},
		{policy.MCPTTPriority{Namespace: "mcpttp", Level: 11}, "PRIO_12"},
	} {
		if _, err := f.b.Authorize(f.ctx(), id, g.Ref, mcpttRequest(false, &tc.p)); err != nil {
			t.Fatalf("Authorize %+v: %v", tc.p, err)
		}

		r := f.pcf.Next(t)
		if r.Op != n5.OpModify || r.Context.McpttID != tc.p.Namespace || r.Context.ResPrio != tc.want {
			t.Fatalf("got %s, want a modify to %s %s", r, tc.p.Namespace, tc.want)
		}

		var patch struct {
			AscReqData map[string]json.RawMessage `json:"ascReqData"`
		}

		if err := json.Unmarshal(r.Patch, &patch); err != nil || len(patch.AscReqData) != 1 || patch.AscReqData["resPrio"] == nil {
			t.Fatalf("patch %s, want resPrio alone", r.Patch)
		}
	}

	if _, err := f.b.Authorize(f.ctx(), id, g.Ref, mcpttRequest(false, &policy.MCPTTPriority{Namespace: "mcpttp", Level: 11})); err != nil {
		t.Fatalf("Authorize unchanged: %v", err)
	}

	f.noRequest()
}

// TS 29.514 Annex B.10.2: mcpttq, its lowest value PRIO_1.
func TestAnnexB10MCPTTQueueing(t *testing.T) {
	f := newFixtureWith(t, pcftest.Config{Features: n5.Features(n5.FeatureIMSSBI, n5.FeatureMCPTT, n5.FeaturePatchCorrection)})

	if _, err := f.b.Authorize(f.ctx(), f.b.NewSessionID(), "", mcpttRequest(true, &policy.MCPTTPriority{Namespace: "mcpttq", Level: 0})); err != nil {
		t.Fatalf("Authorize initial: %v", err)
	}

	if r := f.pcf.Next(t); r.Context.McpttID != "mcpttq" || r.Context.ResPrio != "PRIO_1" {
		t.Fatalf("got %s, want mcpttq, PRIO_1", r)
	}
}

// TS 29.514 §4.2.2.19, §5.8: the create sends the MCPTT priority before the PCF tells its features; a PCF that does
// not support MCPTT gets no update of it.
func TestAnnexB10MCPTTPriorityNotNegotiated(t *testing.T) {
	f := newFixture(t)
	id := f.b.NewSessionID()

	g, err := f.b.Authorize(f.ctx(), id, "", mcpttRequest(true, &policy.MCPTTPriority{Namespace: "mcpttp", Level: 11}))
	if err != nil {
		t.Fatalf("Authorize initial: %v", err)
	}

	if r := f.pcf.Next(t); r.Context.McpttID != "mcpttp" || r.Context.ResPrio != "PRIO_12" {
		t.Fatalf("got %s, want mcpttp, PRIO_12", r)
	}

	if _, err := f.b.Authorize(f.ctx(), id, g.Ref, mcpttRequest(false, &policy.MCPTTPriority{Namespace: "mcpttp", Level: 15})); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	f.noRequest()
}
