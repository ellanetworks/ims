package sip

import (
	"slices"
	"testing"
)

func TestParseReason(t *testing.T) {
	for _, tc := range []struct {
		in, out  string
		protocol string
		cause    int
		hasCause bool
		text     string
	}{
		{`SIP;cause=200;text="Call completed elsewhere"`, "", ReasonSIP, 200, true, "Call completed elsewhere"},
		{`Q.850;cause=16;text="Terminated"`, "", ReasonQ850, 16, true, "Terminated"},
		{`RELEASE_CAUSE;cause=1;text="User ends call"`, "", ReasonReleaseCause, 1, true, "User ends call"},
		{`FAILURE_CAUSE ; cause = 1 ; text = "Media bearer or QoS lost"`, `FAILURE_CAUSE;cause=1;text="Media bearer or QoS lost"`, ReasonFailureCause, 1, true, "Media bearer or QoS lost"},
		{`SIP;text="a \"quoted\" ; text"`, "", ReasonSIP, 0, false, `a "quoted" ; text`},
		{`preemption;cause=1;reason-extension=x`, "", "preemption", 1, true, ""},
		{`SIP`, "", ReasonSIP, 0, false, ""},
	} {
		r, err := ParseReason(tc.in)
		if err != nil {
			t.Errorf("ParseReason(%q): %v", tc.in, err)
			continue
		}

		cause, ok := r.Cause()
		if r.Protocol != tc.protocol || cause != tc.cause || ok != tc.hasCause || r.Text() != tc.text {
			t.Errorf("ParseReason(%q) = %s %d %v %q", tc.in, r.Protocol, cause, ok, r.Text())
		}

		out := tc.out
		if out == "" {
			out = tc.in
		}

		if s := r.String(); s != out {
			t.Errorf("String() = %q, want %q", s, out)
		}
	}
}

func TestParseReasonErrors(t *testing.T) {
	for _, in := range []string{
		"", ";cause=1", "SIP;cause=x", "SIP;cause=-1", "SIP;cause=99999999999", "SIP;text=unquoted", `SIP;text="open`, "SIP cause=1", "SIP;text=\"a\r\nb\"",
		"SIP;text=\"a\x01b\"", "SIP;cause=200;cause=487", `SIP;text="a";TEXT="b"`,
	} {
		if r, err := ParseReason(in); err == nil {
			t.Errorf("ParseReason(%q) = %+v, want error", in, r)
		}
	}
}

func TestNewReason(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		cause    int
		text     string
		want     string
	}{
		{ReasonSIP, 503, ReasonText(ReasonSIP, 503), `SIP;cause=503;text="Service Unavailable"`},
		{ReasonSIP, 488, "", "SIP;cause=488"},
		{ReasonReleaseCause, ReleaseMediaBearerLoss, ReasonText(ReasonReleaseCause, ReleaseMediaBearerLoss), `RELEASE_CAUSE;cause=3;text="Media bearer loss"`},
		{ReasonFailureCause, FailureSignallingBearerReleased, ReasonText(ReasonFailureCause, FailureSignallingBearerReleased), `FAILURE_CAUSE;cause=2;text="Release of signalling bearer"`},
		{ReasonFailureCause, FailureResourcesAllocation, ReasonText(ReasonFailureCause, FailureResourcesAllocation), `FAILURE_CAUSE;cause=3;text="Indication of failed resources allocation"`},
		{ReasonQ850, 16, `say "bye"`, `Q.850;cause=16;text="say \"bye\""`},
		{ReasonQ850, 0, "a\x00b\x7fc\td", "Q.850;cause=0;text=\"a b c\td\""},
		{Reason5GMM, 9, "", "5GMM;cause=9"},
		{ReasonS1APRNL, 21, "", "S1AP-RNL;cause=21"},
	} {
		r, err := NewReason(tc.protocol, tc.cause, tc.text)
		if err != nil {
			t.Errorf("NewReason(%s, %d, %q): %v", tc.protocol, tc.cause, tc.text, err)
			continue
		}

		if s := r.String(); s != tc.want {
			t.Errorf("String() = %q, want %q", s, tc.want)
		}

		if _, err := ParseReason(r.String()); err != nil {
			t.Errorf("ParseReason(%q): %v", r, err)
		}
	}

	for _, tc := range []struct {
		protocol string
		cause    int
	}{
		{"", 1}, {"bad proto", 1}, {"SIP;x", 1}, {ReasonSIP, -1}, {ReasonSIP, 1 << 40},
	} {
		if r, err := NewReason(tc.protocol, tc.cause, ""); err == nil {
			t.Errorf("NewReason(%q, %d) = %q, want error", tc.protocol, tc.cause, r)
		}
	}

	for _, tc := range []struct {
		protocol string
		cause    int
		want     string
	}{
		{ReasonSIP, 487, "Request Terminated"},
		{ReasonSIP, 200, "Call completed elsewhere"},
		{ReasonSIP, 607, "Unwanted"},
		{ReasonSIP, 608, "Rejected"},
		{ReasonNGAPRNL, 20, ""},
		{ReasonSIP, 799, ""},
		{ReasonReleaseCause, ReleaseUserEndsCall, "User ends call"},
		{ReasonReleaseCause, ReleaseRTPTimeout, "RTP/RTCP time-out"},
		{ReasonReleaseCause, ReleaseNoACK, "SIP timeout - no ACK"},
		{ReasonReleaseCause, ReleaseResponseTimeout, "SIP response time-out"},
		{ReasonReleaseCause, ReleaseCallSetupTimeout, "Call-setup time-out"},
		{ReasonReleaseCause, ReleaseRedirectionFailure, "Redirection failure"},
		{ReasonReleaseCause, 8, ""},
		{ReasonFailureCause, FailureMediaBearerLost, "Media bearer or QoS lost"},
		{ReasonFailureCause, FailureResourcesAllocation, "Indication of failed resources allocation"},
		{"sip", 487, "Request Terminated"},
		{"release_cause", ReleaseUserEndsCall, "User ends call"},
		{"q.850", 16, ""},
		{ReasonQ850, 16, ""},
	} {
		if got := ReasonText(tc.protocol, tc.cause); got != tc.want {
			t.Errorf("ReasonText(%s, %d) = %q, want %q", tc.protocol, tc.cause, got, tc.want)
		}
	}
}

func TestHeaderReasons(t *testing.T) {
	var h Header

	h.Add("Reason", `SIP;cause=503;text="Service, Unavailable", Q.850;cause=38`)
	h.Add("Reason", "RELEASE_CAUSE;cause=3")

	rs, err := h.Reasons()
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, r := range rs {
		got = append(got, r.String())
	}

	want := []string{`SIP;cause=503;text="Service, Unavailable"`, "Q.850;cause=38", "RELEASE_CAUSE;cause=3"}
	if !slices.Equal(got, want) {
		t.Errorf("Reasons() = %q, want %q", got, want)
	}

	h.Add("Reason", "SIP;cause=x")

	if rs, err := h.Reasons(); err == nil || len(rs) != 3 {
		t.Errorf("Reasons() with a bad element = %d reasons, %v", len(rs), err)
	}
}

func TestReasonIs(t *testing.T) {
	r, err := ParseReason("sip;cause=487")
	if err != nil {
		t.Fatal(err)
	}

	if !r.Is(ReasonSIP) || r.Is(ReasonQ850) {
		t.Errorf("Is: %+v", r)
	}

	if got := ReasonText(r.Protocol, 487); got != "Request Terminated" {
		t.Errorf("ReasonText(%q, 487) = %q", r.Protocol, got)
	}

	if r, err := ParseReason("SIP;text=\"tab\there\""); err != nil || r.Text() != "tab\there" {
		t.Errorf("tab in text: %q, %v", r.Text(), err)
	}
}
