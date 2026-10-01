package sip_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/internal/corpus"
)

const (
	// parserRejects marks a torture message the parser rejects.
	parserRejects = -1
	// dropped marks a request validation rejects as unanswerable.
	dropped = -2
)

// TestTortureOutcome pins how each RFC 4475 request is answered: by the
// parser, with a status code from validation, or 0 when it is accepted.
func TestTortureOutcome(t *testing.T) {
	want := map[string]int{
		"badvers":    505,
		"unkscm":     416,
		"novelsc":    416,
		"insuf":      400,
		"multi01":    400,
		"mismatch01": 400,
		"mismatch02": 400,
		"scalar02":   400,
		"quotbal":    400,
		"badinv01":   dropped, // its only Via is malformed
		"badbranch":  400,
		"inv2543":    400,
		// Valid in RFC 4475, but their top Via has no RFC 3261 branch.
		"wsinv":   400,
		"longreq": 400,
		// The archive's baddn has no empty line after its header fields.
		"baddn":      parserRejects,
		"mcl01":      parserRejects,
		"clerr":      parserRejects,
		"ncl":        parserRejects,
		"ltgtruri":   parserRejects,
		"lwsruri":    parserRejects,
		"zeromf":     0,
		"bext01":     0,
		"invut":      0,
		"regaut01":   0,
		"cparam01":   0,
		"cparam02":   0,
		"regescrt":   0,
		"sdp01":      0,
		"unksm2":     0,
		"esc01":      0,
		"esc02":      0,
		"escnull":    0,
		"intmeth":    0,
		"lwsdisp":    0,
		"semiuri":    0,
		"transports": 0,
		"mpart01":    0,
		"dblreq":     0,
	}

	for name, code := range want {
		raw, err := corpus.TortureMessage(name)
		if err != nil {
			t.Fatal(err)
		}

		got := 0

		msg, err := sip.Parse(raw)

		var serr *sip.StatusError

		switch {
		case err != nil:
			got = parserRejects
		default:
			verr := msg.(*sip.Request).Validate()

			switch {
			case errors.As(verr, &serr):
				got = serr.StatusCode
			case errors.Is(verr, sip.ErrUnanswerable):
				got = dropped
			}
		}

		if got != code {
			t.Errorf("%s: outcome %d (%v), want %d", name, got, err, code)
		}
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
		code int // 0: valid; dropped: unanswerable
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
