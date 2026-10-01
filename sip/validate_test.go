package sip_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/internal/corpus"
)

const dropped = -2

func TestBadDisplayName(t *testing.T) {
	raw, err := corpus.TortureMessage("baddn")
	if err != nil {
		t.Fatal(err)
	}

	msg, err := sip.Parse(append(raw, "\r\n"...))
	if err != nil {
		t.Fatal(err)
	}

	var serr *sip.StatusError
	if err := msg.Validate(); !errors.As(err, &serr) || serr.StatusCode != 400 {
		t.Errorf("Validate() = %v, want 400", err)
	}
}

func TestValidateWrapsCause(t *testing.T) {
	raw, err := corpus.TortureMessage("insuf")
	if err != nil {
		t.Fatal(err)
	}

	msg, err := sip.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	if err := msg.Validate(); !errors.Is(err, sip.ErrMissingHeader) {
		t.Errorf("Validate() = %v, want it to wrap ErrMissingHeader", err)
	}
}

func TestParseErrorKeepsRequest(t *testing.T) {
	raw, err := corpus.TortureMessage("mcl01")
	if err != nil {
		t.Fatal(err)
	}

	_, err = sip.Parse(raw)

	var perr *sip.ParseError
	if !errors.As(err, &perr) || perr.Request == nil || perr.Request.Header.CallID() == "" {
		t.Fatalf("Parse(mcl01) = %v, want a *ParseError with the request", err)
	}

	res := sip.NewResponse(perr.Request, 400, "")
	if res.Header.CallID() != perr.Request.Header.CallID() || res.Header.Count("Via") != 1 {
		t.Errorf("400 = %s", res)
	}
}

func TestValidateResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{"valid", "SIP/2.0 200 OK\r\nVia: SIP/2.0/UDP a;branch=z9hG4bK1\r\nFrom: <sip:a@b>;tag=1\r\nTo: <sip:c@d>;tag=2\r\nCall-ID: x\r\nCSeq: 1 INVITE\r\n\r\n", true},
		{"no Via", "SIP/2.0 200 OK\r\nFrom: <sip:a@b>;tag=1\r\nTo: <sip:c@d>\r\nCall-ID: x\r\nCSeq: 1 INVITE\r\n\r\n", false},
		{"two To", "SIP/2.0 200 OK\r\nVia: SIP/2.0/UDP a;branch=z9hG4bK1\r\nFrom: <sip:a@b>;tag=1\r\nTo: <sip:c@d>\r\nTo: <sip:c@d>\r\nCall-ID: x\r\nCSeq: 1 INVITE\r\n\r\n", false},
		{"version", "SIP/3.0 200 OK\r\nVia: SIP/2.0/UDP a;branch=z9hG4bK1\r\nFrom: <sip:a@b>;tag=1\r\nTo: <sip:c@d>\r\nCall-ID: x\r\nCSeq: 1 INVITE\r\n\r\n", false},
	} {
		msg, err := sip.Parse([]byte(tc.raw))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if err := msg.(*sip.Response).Validate(); (err == nil) != tc.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestValidateRequest(t *testing.T) {
	const rest = "From: <sip:a@x>;tag=1\r\nTo: <sip:b@x>\r\nCall-ID: c\r\nCSeq: 1 INVITE\r\n\r\n"

	for _, tc := range []struct {
		name string
		raw  string
		code int
	}{
		{"emergency URN", "INVITE urn:service:sos SIP/2.0\r\nVia: SIP/2.0/UDP ue;branch=z9hG4bK1\r\n" + rest, 0},
		{"unknown scheme", "INVITE http://x SIP/2.0\r\nVia: SIP/2.0/UDP ue;branch=z9hG4bK1\r\n" + rest, 416},
		{"no Via", "INVITE http://x SIP/2.0\r\n" + rest, dropped},
		{"malformed top Via", "INVITE sip:b@x SIP/2.0\r\nVia: SIP/2.0/UDP ue;;\r\n" + rest, dropped},
		{"malformed lower Via", "INVITE sip:b@x SIP/2.0\r\nVia: SIP/2.0/UDP p;branch=z9hG4bK2, junk\r\nVia: junk\r\n" + rest, 0},
		{"bad rport", "INVITE sip:b@x SIP/2.0\r\nVia: SIP/2.0/UDP ue;branch=z9hG4bK1;rport=99999\r\n" + rest, 400},
		{"rport flag", "INVITE sip:b@x SIP/2.0\r\nVia: SIP/2.0/UDP ue;branch=z9hG4bK1;rport\r\n" + rest, 0},
		{"bad received", "INVITE sip:b@x SIP/2.0\r\nVia: SIP/2.0/UDP ue;branch=z9hG4bK1;received=ue.example\r\n" + rest, 400},
		{"IPv6 received", "INVITE sip:b@x SIP/2.0\r\nVia: SIP/2.0/UDP ue;branch=z9hG4bK1;received=2001:db8::1\r\n" + rest, 0},
		{"Expires not checked", "INVITE sip:b@x SIP/2.0\r\nVia: SIP/2.0/UDP ue;branch=z9hG4bK1\r\nExpires: soon\r\n" + rest, 0},
		{"large CSeq", "INVITE sip:b@x SIP/2.0\r\nVia: SIP/2.0/UDP ue;branch=z9hG4bK1\r\n" + strings.Replace(rest, "CSeq: 1", "CSeq: 3000000000", 1), 0},
	} {
		msg, err := sip.Parse([]byte(tc.raw))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		got := 0
		verr := msg.(*sip.Request).Validate()

		var serr *sip.StatusError

		switch {
		case errors.As(verr, &serr):
			got = serr.StatusCode
		case errors.Is(verr, sip.ErrUnanswerable):
			got = dropped
		case verr != nil:
			t.Fatalf("%s: unexpected error %v", tc.name, verr)
		}

		if got != tc.code {
			t.Errorf("%s: outcome %d (%v), want %d", tc.name, got, verr, tc.code)
		}
	}
}
