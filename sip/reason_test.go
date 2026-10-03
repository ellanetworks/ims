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
	for _, in := range []string{"", ";cause=1", "SIP;cause=x", "SIP;cause=-1", "SIP;cause=99999999999", "SIP;text=unquoted", `SIP;text="open`, "SIP cause=1", "SIP;text=\"a\r\nb\""} {
		if r, err := ParseReason(in); err == nil {
			t.Errorf("ParseReason(%q) = %+v, want error", in, r)
		}
	}
}

func TestNewReason(t *testing.T) {
	for _, tc := range []struct {
		r    Reason
		want string
	}{
		{NewReason(ReasonSIP, 503, ReasonText(ReasonSIP, 503)), `SIP;cause=503;text="Service Unavailable"`},
		{NewReason(ReasonSIP, 488, ""), "SIP;cause=488"},
		{NewReason(ReasonReleaseCause, ReleaseMediaBearerLoss, ReasonText(ReasonReleaseCause, ReleaseMediaBearerLoss)), `RELEASE_CAUSE;cause=3;text="Media bearer loss"`},
		{NewReason(ReasonFailureCause, FailureSignallingBearerReleased, ReasonText(ReasonFailureCause, FailureSignallingBearerReleased)), `FAILURE_CAUSE;cause=2;text="Release of signalling bearer"`},
		{NewReason(ReasonQ850, 16, `say "bye"`), `Q.850;cause=16;text="say \"bye\""`},
	} {
		if s := tc.r.String(); s != tc.want {
			t.Errorf("String() = %q, want %q", s, tc.want)
		}

		if _, err := ParseReason(tc.r.String()); err != nil {
			t.Errorf("ParseReason(%q): %v", tc.r, err)
		}
	}

	for _, tc := range []struct {
		protocol string
		cause    int
		want     string
	}{
		{ReasonSIP, 487, "Request Terminated"},
		{ReasonSIP, 799, ""},
		{ReasonReleaseCause, ReleaseUserEndsCall, "User ends call"},
		{ReasonReleaseCause, ReleaseRTPTimeout, "RTP/RTCP time-out"},
		{ReasonReleaseCause, ReleaseNoACK, "SIP timeout - no ACK"},
		{ReasonReleaseCause, ReleaseResponseTimeout, "SIP response time-out"},
		{ReasonReleaseCause, ReleaseCallSetupTimeout, "Call-setup time-out"},
		{ReasonReleaseCause, ReleaseRedirectionFailure, "Redirection failure"},
		{ReasonReleaseCause, 8, ""},
		{ReasonFailureCause, FailureMediaBearerLost, "Media bearer or QoS lost"},
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

	if _, err := h.Reasons(); err == nil {
		t.Error("Reasons(): no error")
	}
}
