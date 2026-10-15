package sip

import (
	"math"
	"testing"
)

// RFC 3841 §7.2.5.
func TestCallerPreferenceExample(t *testing.T) {
	contacts := map[string]string{
		"u1": `<sip:u1@h.example.com>;audio;video;methods="INVITE,BYE";q=0.2`,
		"u2": `<sip:u2@h.example.com>;audio="FALSE";methods="INVITE";actor="msg-taker";q=0.2`,
		"u3": `<sip:u3@h.example.com>;audio;actor="msg-taker";methods="INVITE";video;q=0.3`,
		"u4": `<sip:u4@h.example.com>;audio;methods="INVITE,OPTIONS";q=0.2`,
		"u5": `<sip:u5@h.example.com>;q=0.5`,
	}

	var h Header

	h.Add("Reject-Contact", `*;actor="msg-taker";video`)
	h.Add("Accept-Contact", "*;audio;require")
	h.Add("Accept-Contact", "*;video;explicit")
	h.Add("Accept-Contact", `*;methods="BYE";class="business";q=1.0`)

	rejects, err := h.RejectContact()
	if err != nil {
		t.Fatal(err)
	}

	accepts, err := h.AcceptContact()
	if err != nil || len(accepts) != 3 || !accepts[0].Require || !accepts[1].Explicit || accepts[2].Require {
		t.Fatalf("Accept-Contact %+v, %v", accepts, err)
	}

	for name, want := range map[string]struct {
		rejected bool
		qa       float64
		kept     bool
	}{
		"u1": {qa: 2.5 / 3, kept: true},
		"u2": {kept: false},
		"u3": {rejected: true},
		"u4": {qa: 0.5, kept: true},
		"u5": {qa: 1, kept: true},
	} {
		a, err := ParseAddress(contacts[name])
		if err != nil {
			t.Fatal(err)
		}

		fs, err := a.Features()
		if err != nil {
			t.Fatal(err)
		}

		if got := fs.RejectedBy(rejects); got != want.rejected {
			t.Errorf("%s: rejected %v, want %v", name, got, want.rejected)
		}

		if want.rejected {
			continue
		}

		qa, kept := fs.Preference(accepts)
		if kept != want.kept || kept && math.Abs(qa-want.qa) > 1e-9 {
			t.Errorf("%s: Qa %v, kept %v; want %v, %v", name, qa, kept, want.qa, want.kept)
		}
	}
}

func TestParseFeatureSet(t *testing.T) {
	// RFC 3840 §5.
	a, err := ParseAddress(`<sip:user@pc.example.com>;mobility="fixed";events="!presence,message-summary";language="en,de"` +
		`;description="<PC>";+sip.newparam;+rangeparam="#-4:+5.125";other-param=66372;q=0.5`)
	if err != nil {
		t.Fatal(err)
	}

	fs, err := a.Features()
	if err != nil {
		t.Fatal(err)
	}

	want := FeatureSet{
		{Tag: "sip.mobility", Values: []FeatureValue{{Text: "fixed"}}},
		{Tag: "sip.events", Values: []FeatureValue{{Negated: true, Text: "presence"}, {Text: "message-summary"}}},
		{Tag: "language", Values: []FeatureValue{{Text: "en"}, {Text: "de"}}},
		{Tag: "sip.description", Values: []FeatureValue{{Kind: FeatureString, Text: "PC"}}},
		{Tag: "sip.newparam", Values: []FeatureValue{{Text: "TRUE"}}},
		{Tag: "rangeparam", Values: []FeatureValue{{Kind: FeatureNumeric, Min: -4, Max: 5.125}}},
	}

	if !equalFeatureSets(fs, want) {
		t.Fatalf("features %+v\nwant %+v", fs, want)
	}

	out := Address{URI: a.URI, Params: fs.Params()}.String()
	if out != `<sip:user@pc.example.com>;mobility="fixed";events="!presence,message-summary";language="en,de";description="<PC>";+sip.newparam;+rangeparam="#-4:5.125"` {
		t.Errorf("encoded as %s", out)
	}
}

// TS 24.229 §7.9.2: a URN value is percent-encoded in a token.
func TestFeatureICSI(t *testing.T) {
	for _, v := range []string{
		`"urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt"`,
		`urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt`,
		`"urn%3aurn-7%3a3gpp-service.ims.icsi.mcptt,urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`,
	} {
		fs, err := ParseFeatureSet(Params{{Name: "+g.3gpp.icsi-ref", Value: v}, {Name: "+g.3gpp.mcptt"}})
		if err != nil {
			t.Fatal(err)
		}

		if got := fs.Tokens("g.3gpp.icsi-ref"); len(got) == 0 || got[0] != "urn:urn-7:3gpp-service.ims.icsi.mcptt" || !fs.Has("g.3gpp.mcptt") {
			t.Errorf("%s: ICSIs %q", v, got)
		}
	}

	icsi := NewFeature("g.3gpp.icsi-ref", "urn:urn-7:3gpp-service.ims.icsi.mcptt")
	if p := icsi.Param(); p.Name != "+g.3gpp.icsi-ref" || p.Value != `"urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt"` {
		t.Errorf("encoded as %s=%s", p.Name, p.Value)
	}

	if p := NewFeature("g.3gpp.mcptt").Param(); p.Name != "+g.3gpp.mcptt" || p.Value != "" {
		t.Errorf("TRUE encoded as %s=%s", p.Name, p.Value)
	}

	// TS 24.379 §6.2.1: the Accept-Contact of an MCPTT request matches the Contact of an MCPTT client.
	var h Header

	h.Add("Accept-Contact", `*;+g.3gpp.mcptt;require;explicit`)
	h.Add("a", `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt";require;explicit`)

	accepts, err := h.AcceptContact()
	if err != nil {
		t.Fatal(err)
	}

	client := FeatureSet{NewFeature("g.3gpp.mcptt"), icsi}
	if qa, ok := client.Preference(accepts); !ok || qa != 1 {
		t.Errorf("MCPTT client: Qa %v, kept %v", qa, ok)
	}

	mmtel := FeatureSet{NewFeature("g.3gpp.icsi-ref", "urn:urn-7:3gpp-service.ims.icsi.mmtel")}
	if _, ok := mmtel.Preference(accepts); ok {
		t.Error("MMTel client kept")
	}
}

func TestFeatureTagNames(t *testing.T) {
	for _, tc := range []struct{ param, tag string }{
		{"audio", "sip.audio"},
		{"ISFOCUS", "sip.isfocus"},
		{"language", "language"},
		{"type", "type"},
		{"+sip.instance", "sip.instance"},
		{"+g.3gpp.mcptt", "g.3gpp.mcptt"},
		{"+urn!example'tag", "urn:example/tag"},
	} {
		if got := featureTag(tc.param); got != tc.tag {
			t.Errorf("featureTag(%s) = %s, want %s", tc.param, got, tc.tag)
		}

		if got := featureTag(featureParam(tc.tag)); got != tc.tag {
			t.Errorf("featureParam(%s) does not round-trip: %s", tc.tag, featureParam(tc.tag))
		}
	}

	// RFC 3841 §7.2.3: a "+" parameter for a base tag already there is ignored.
	fs, err := ParseFeatureSet(Params{{Name: "+sip.video", Value: `"FALSE"`}, {Name: "video"}, {Name: "reg-id", Value: "1"}})
	if err != nil || len(fs) != 1 || fs.Tokens("sip.video")[0] != "TRUE" {
		t.Errorf("features %+v, %v", fs, err)
	}
}

// RFC 2533 matching, as RFC 3841 §7.2.4 uses it.
func TestFeatureMatching(t *testing.T) {
	for _, tc := range []struct {
		pref, contact string
		match         bool
	}{
		{`"fixed"`, `"FIXED"`, true},
		{`"fixed"`, `"mobile"`, false},
		{`"fixed,mobile"`, `"mobile"`, true},
		{`"!fixed"`, `"fixed,mobile"`, true},
		{`"!fixed"`, `"fixed"`, false},
		{`"!fixed"`, `"!mobile"`, true},
		{`"<PC>"`, `"<PC>"`, true},
		{`"<PC>"`, `"<pc>"`, false},
		{`"<PC>"`, `"PC"`, false},
		{`"#>=4"`, `"#=5"`, true},
		{`"#>=4"`, `"#<=3"`, false},
		{`"#2:4"`, `"#4:9"`, true},
		{`"#2:4"`, `"#4.5:9"`, false},
		{`"!#2:4"`, `"#=5"`, true},
		{`"!#2:4"`, `"#=3"`, false},
		{`"#=3"`, `"three"`, false},
		{``, `"FALSE"`, false},
		{``, ``, true},
	} {
		pref, err := parseFeatureValues(tc.pref)
		if err != nil {
			t.Fatalf("%s: %v", tc.pref, err)
		}

		contact, err := parseFeatureValues(tc.contact)
		if err != nil {
			t.Fatalf("%s: %v", tc.contact, err)
		}

		got := FeatureTerm{Values: pref}.matches(FeatureTerm{Values: contact})
		if got != tc.match {
			t.Errorf("%s against %s: %v, want %v", tc.pref, tc.contact, got, tc.match)
		}
	}
}

func TestParseFeatureValuesErrors(t *testing.T) {
	for _, v := range []string{`"<PC"`, `"a b"`, `"#>=x"`, `"#4"`, `"#5:4"`, `"#1e9"`, `""`, `"a,,b"`, `"%zz"`, `"#inf"`} {
		if fv, err := parseFeatureValues(v); err == nil {
			t.Errorf("parseFeatureValues(%s) = %+v, want error", v, fv)
		}
	}

	if _, err := (Header{{Name: "Accept-Contact", Value: "sip:x;audio"}}).AcceptContact(); err == nil {
		t.Error("Accept-Contact without * accepted")
	}
}

func TestFeatureCaps(t *testing.T) {
	var h Header

	h.Add("Feature-Caps", `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";video;+g.3gpp.mcptt.ambient-listening-call-release`)
	h.Add("Feature-Caps", `*;+g.3gpp.trf="<sip:trf.ims.example.com>"`)

	caps, err := h.FeatureCaps()
	if err != nil || len(caps) != 2 {
		t.Fatalf("Feature-Caps %+v, %v", caps, err)
	}

	if caps[0].Has("sip.video") || !caps[0].Has("g.3gpp.mcptt.ambient-listening-call-release") {
		t.Errorf("first indicator set %+v: base tags are not feature-capability indicators (RFC 6809 §6.3)", caps[0])
	}

	if got := caps[1].Tokens("g.3gpp.trf"); len(got) != 1 || got[0] != "sip:trf.ims.example.com" {
		t.Errorf("string indicator %q", got)
	}
}

func equalFeatureSets(a, b FeatureSet) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i].Tag != b[i].Tag || len(a[i].Values) != len(b[i].Values) {
			return false
		}

		for j := range a[i].Values {
			if a[i].Values[j] != b[i].Values[j] {
				return false
			}
		}
	}

	return true
}
