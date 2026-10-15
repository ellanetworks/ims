package sip

import (
	"slices"
	"strings"
	"testing"
)

func header(fields ...string) Header {
	var h Header
	for i := 0; i+1 < len(fields); i += 2 {
		h.Add(fields[i], fields[i+1])
	}

	return h
}

func TestOptionTags(t *testing.T) {
	h := header("Supported", "timer, 100rel", "k", "TDialog", "Require", "precondition")

	tags, err := h.OptionTags("Supported")
	if err != nil || !slices.Equal(tags, []string{"timer", "100rel", "TDialog"}) {
		t.Errorf("Supported %q, %v", tags, err)
	}

	if !h.HasOptionTag("Supported", OptionTagTargetDialog) || h.HasOptionTag("Supported", OptionTagNoReferSub) ||
		!h.HasOptionTag("Require", OptionTagPrecondition) {
		t.Error("HasOptionTag")
	}

	if _, err := header("Require", "a b").OptionTags("Require"); err == nil {
		t.Error("invalid option tag accepted")
	}
}

// RFC 5373 §6, TS 24.379 §6.3.2.2.5.2.
func TestAnswerMode(t *testing.T) {
	a, ok, err := header("Answer-Mode", "auto;require").AnswerMode()
	if !ok || err != nil || !a.Is(AnswerModeAuto) || !a.Require() || a.String() != "auto;require" {
		t.Errorf("Answer-Mode %+v, %v, %v", a, ok, err)
	}

	p, ok, err := header("Priv-Answer-Mode", "Manual").PrivAnswerMode()
	if !ok || err != nil || !p.Is(AnswerModeManual) || p.Require() {
		t.Errorf("Priv-Answer-Mode %+v, %v, %v", p, ok, err)
	}

	if _, ok, err := (Header{}).AnswerMode(); ok || err != nil {
		t.Error("absent Answer-Mode")
	}

	if _, _, err := header("Answer-Mode", "Auto", "Answer-Mode", "Manual").AnswerMode(); err == nil {
		t.Error("two Answer-Mode accepted")
	}

	s, ok, err := header("P-Answer-State", "Unconfirmed").PAnswerState()
	if !ok || err != nil || !s.Is(AnswerStateUnconfirmed) {
		t.Errorf("P-Answer-State %+v, %v, %v", s, ok, err)
	}
}

// RFC 4412 §3.1, RFC 8101.
func TestResourcePriority(t *testing.T) {
	rs, err := header("Resource-Priority", "mcpttp.13, dsn.flash", "Resource-Priority", "MCPTTQ.4").ResourcePriorities()
	if err != nil || len(rs) != 3 || rs[0] != (ResourcePriority{ResourcePriorityMCPTTP, "13"}) ||
		!rs[2].Equal(ResourcePriority{ResourcePriorityMCPTTQ, "4"}) || rs[1].String() != "dsn.flash" {
		t.Errorf("Resource-Priority %+v, %v", rs, err)
	}

	for _, v := range []string{"mcpttp", "mcpttp.1.2", ".1", "mcpttp.", "a b.1"} {
		if _, err := ParseResourcePriority(v); err == nil {
			t.Errorf("ParseResourcePriority(%q) accepted", v)
		}
	}

	if rs, err := header("Resource-Priority", "mcpttp.1", "Resource-Priority", "MCPTTP.2").ResourcePriorities(); err == nil || len(rs) != 2 {
		t.Errorf("namespace twice: %+v, %v", rs, err)
	}

	if rs, err := header("Accept-Resource-Priority", "").AcceptResourcePriorities(); err != nil || rs != nil {
		t.Errorf("empty Accept-Resource-Priority %+v, %v", rs, err)
	}
}

// RFC 4538 §7, RFC 3891 §6.1.
func TestTargetDialogAndReplaces(t *testing.T) {
	td, ok, err := header("Target-Dialog", "fa77as7dad8-sd98ajzz@host.example.com;local-tag=kkaz-;remote-tag=6544").TargetDialog()
	if !ok || err != nil || td.CallID != "fa77as7dad8-sd98ajzz@host.example.com" || td.LocalTag != "kkaz-" || td.RemoteTag != "6544" {
		t.Errorf("Target-Dialog %+v, %v, %v", td, ok, err)
	}

	if again, err := ParseTargetDialog(td.String()); err != nil || again.CallID != td.CallID || again.LocalTag != td.LocalTag {
		t.Errorf("Target-Dialog round trip %q: %v", td, err)
	}

	r, ok, err := header("Replaces", "98732@sip.example.com;from-tag=r33th4x0r;to-tag=ff87ff;early-only").Replaces()
	if !ok || err != nil || r.CallID != "98732@sip.example.com" || r.FromTag != "r33th4x0r" || r.ToTag != "ff87ff" || !r.EarlyOnly {
		t.Errorf("Replaces %+v, %v, %v", r, ok, err)
	}

	if again, err := ParseReplaces(r.String()); err != nil || again.String() != r.String() {
		t.Errorf("Replaces round trip %q: %v", r, err)
	}

	for _, v := range []string{"a@b;local-tag=1", "a@b;remote-tag=1", ";local-tag=1;remote-tag=2", "a b;local-tag=1;remote-tag=2", "a@b;local-tag=1;local-tag=3;remote-tag=2"} {
		if _, err := ParseTargetDialog(v); err == nil {
			t.Errorf("ParseTargetDialog(%q) accepted", v)
		}
	}

	for _, v := range []string{"a@b;to-tag=1", "a@b;from-tag=1", "a@b;to-tag=1;to-tag=2;from-tag=3", "a@b;to-tag=1;from-tag=2;early-only=yes"} {
		if _, err := ParseReplaces(v); err == nil {
			t.Errorf("ParseReplaces(%q) accepted", v)
		}
	}
}

// RFC 4488 §4, RFC 5318 §5.
func TestReferSubAndRefusedURIs(t *testing.T) {
	for v, want := range map[string]bool{"false": false, "TRUE": true, "false;x=1": false} {
		if sub, ok, err := header("Refer-Sub", v).ReferSub(); !ok || err != nil || sub.Subscribe != want {
			t.Errorf("Refer-Sub %q: %v, %v, %v", v, sub, ok, err)
		}
	}

	if s := (ReferSub{}).String(); s != "false" {
		t.Errorf("Refer-Sub %s", s)
	}

	if _, _, err := header("Refer-Sub", "maybe").ReferSub(); err == nil {
		t.Error("Refer-Sub maybe accepted")
	}

	refused, err := header("P-Refused-URI-List", `<sip:group@example.com>;members="<cid:a@b>", sip:alice@example.com`).RefusedURIs()
	if err != nil || len(refused) != 2 {
		t.Fatalf("P-Refused-URI-List %+v, %v", refused, err)
	}

	if m, _ := refused[0].Params.Get("members"); Unquote(m) != "<cid:a@b>" {
		t.Errorf("members %q", m)
	}
}

// RFC 4028 §4, §5, TS 24.379 Annex A.
func TestSessionExpires(t *testing.T) {
	se, ok, err := header("x", "3600;Refresher=UAC;x=1").SessionExpires()
	if !ok || err != nil || se.Seconds != 3600 || se.Refresher != RefresherUAC || se.String() != "3600;refresher=uac;x=1" {
		t.Errorf("Session-Expires %+v, %v, %v", se, ok, err)
	}

	if se, err := ParseSessionExpires("99999999999"); err != nil || se.Seconds != 1<<32-1 {
		t.Errorf("Session-Expires above 2**32-1: %+v, %v (RFC 3261 §10.2.1.1)", se, err)
	}

	for _, v := range []string{"", "-1", "90;refresher=both", "a"} {
		if _, err := ParseSessionExpires(v); err == nil {
			t.Errorf("ParseSessionExpires(%q) accepted", v)
		}
	}

	if n, ok, err := header("Min-SE", "90").MinSE(); !ok || err != nil || n != 90 {
		t.Errorf("Min-SE %d, %v, %v", n, ok, err)
	}
}

// RFC 6665 §8.2.
func TestEventHeaders(t *testing.T) {
	e, ok, err := header("o", "conference;id=7;x").Event()
	if !ok || err != nil || e.Package != "conference" || e.ID != "7" || e.String() != "conference;id=7;x" {
		t.Errorf("Event %+v, %v, %v", e, ok, err)
	}

	tmpl, err := ParseEvent("presence.winfo")
	if err != nil || tmpl.Package != "presence" || !slices.Equal(tmpl.Templates, []string{"winfo"}) || tmpl.Type() != "presence.winfo" {
		t.Errorf("Event template %+v, %v", tmpl, err)
	}

	if e.Matches(Event{Package: "conference"}) || !e.Matches(Event{Package: "conference", ID: "7"}) ||
		(Event{Package: "Conference"}).Matches(Event{Package: "conference"}) {
		t.Error("Event matching is byte by byte (RFC 6665 §8.2.1)")
	}

	for _, v := range []string{"", "a..b", ".a", "a;id=\"x\""} {
		if _, err := ParseEvent(v); err == nil {
			t.Errorf("ParseEvent(%q) accepted", v)
		}
	}

	events, err := header("Allow-Events", "conference, presence", "u", "poc-settings").AllowEvents()
	if err != nil || !slices.Equal(events, []string{"conference", "presence", "poc-settings"}) {
		t.Errorf("Allow-Events %q, %v", events, err)
	}

	ss, ok, err := header("Subscription-State", "Terminated;reason=Timeout;retry-after=30").SubscriptionState()
	if !ok || err != nil || ss.State != SubscriptionTerminated || ss.Reason != SubscriptionTimeout || ss.RetryAfter == nil ||
		*ss.RetryAfter != 30 || ss.Expires != nil {
		t.Errorf("Subscription-State %+v, %v, %v", ss, ok, err)
	}

	// TS 24.379 §9.2.1.6: the longest subscription.
	active, err := ParseSubscriptionState("active;expires=4294967295")
	if err != nil || active.Expires == nil || *active.Expires != 4294967295 || active.String() != "active;expires=4294967295" {
		t.Errorf("Subscription-State %+v, %v", active, err)
	}

	if _, err := ParseSubscriptionState("active;expires=x"); err == nil {
		t.Error("invalid expires accepted")
	}

	if tag, ok, err := header("SIP-ETag", "dx200xyz").SIPETag(); !ok || err != nil || tag != "dx200xyz" {
		t.Errorf("SIP-ETag %q, %v, %v", tag, ok, err)
	}

	if _, _, err := header("SIP-If-Match", "a b").SIPIfMatch(); err == nil {
		t.Error("invalid entity-tag accepted")
	}
}

// RFC 6086 §7.2, TS 24.379 Annex J.
func TestInfoPackages(t *testing.T) {
	p, ok, err := header("Info-Package", "g.3gpp.mcptt-floor-request").InfoPackage()
	if !ok || err != nil || p.Name != "g.3gpp.mcptt-floor-request" {
		t.Errorf("Info-Package %+v, %v, %v", p, ok, err)
	}

	recv, ok, err := header("Recv-Info", "g.3gpp.mcptt-floor-request, g.3gpp.mcptt-info").RecvInfo()
	if !ok || err != nil || !AcceptsInfo(recv, "g.3gpp.mcptt-info") || AcceptsInfo(recv, "G.3GPP.MCPTT-INFO") || AcceptsInfo(recv, "nosuch") {
		t.Errorf("Recv-Info %+v, %v, %v", recv, ok, err)
	}

	if recv, ok, err := header("Recv-Info", "").RecvInfo(); !ok || err != nil || recv != nil {
		t.Errorf("empty Recv-Info %+v, %v, %v", recv, ok, err)
	}

	if _, ok, _ := (Header{}).RecvInfo(); ok {
		t.Error("absent Recv-Info present")
	}
}

// RFC 6050 §4.4.
func TestServices(t *testing.T) {
	ids, err := header("P-Asserted-Service", "urn:urn-7:3gpp-service.ims.icsi.MCPTT").AssertedServices()
	if err != nil || !slices.Equal(ids, []string{"urn:urn-7:3gpp-service.ims.icsi.mcptt"}) {
		t.Errorf("P-Asserted-Service %q, %v", ids, err)
	}

	if !ServiceIs("urn:urn-7:3gpp-service.ims.icsi.mmtel.hd-video", "urn:urn-7:3gpp-service.ims.icsi.mmtel") ||
		ServiceIs("urn:urn-7:3gpp-service.ims.icsi.mmtelx", "urn:urn-7:3gpp-service.ims.icsi.mmtel") {
		t.Error("ServiceIs (TS 24.229 §7.2A.8.1)")
	}

	for _, v := range []string{"urn:urn-7:", "urn:urn-7:a..b", "urn:urn-6:a", "urn:urn-7:a_b", "urn:urn-7:" + strings.Repeat("a", 28)} {
		if IsServiceID(v) {
			t.Errorf("IsServiceID(%q)", v)
		}
	}

	if _, err := header("P-Preferred-Service", "mmtel").PreferredServices(); err == nil {
		t.Error("invalid Service-ID accepted")
	}
}
