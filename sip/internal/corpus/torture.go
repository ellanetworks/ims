package corpus

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

type Expectation int

const (
	Valid Expectation = iota

	Invalid

	Liberal

	Semantic
)

func (e Expectation) String() string {
	switch e {
	case Valid:
		return "valid"
	case Invalid:
		return "invalid"
	case Liberal:
		return "liberal"
	default:
		return "semantic"
	}
}

type TortureCase struct {
	Name    string
	Section string
	Expect  Expectation
	Note    string

	Outcome int
}

const (
	Accept        = 0
	ParseRejected = -1
	Dropped       = -2
)

func TortureCases() []TortureCase {
	return slices.Clone(torture)
}

var torture = []TortureCase{
	{"wsinv", "3.1.1.1", Valid, "short tortuous INVITE: folding, odd whitespace, compact forms; its top Via has no RFC 3261 branch", 400},
	{"intmeth", "3.1.1.2", Valid, "wide range of valid characters", Accept},
	{"esc01", "3.1.1.3", Valid, "valid use of % escaping", Accept},
	{"escnull", "3.1.1.4", Valid, "escaped nulls in URIs", Accept},
	{"esc02", "3.1.1.5", Valid, "use of % when it is not an escape", Accept},
	{"lwsdisp", "3.1.1.6", Valid, "message with no LWS between display name and <", Accept},
	{"longreq", "3.1.1.7", Valid, "long values in header fields; its top Via has no RFC 3261 branch", 400},
	{"dblreq", "3.1.1.8", Valid, "extra trailing octets in a UDP datagram", Accept},
	{"semiuri", "3.1.1.9", Valid, "semicolon-separated parameters in URI user part", Accept},
	{"transports", "3.1.1.10", Valid, "varied and unknown transport types", Accept},
	{"mpart01", "3.1.1.11", Valid, "multipart MIME message", Accept},
	{"unreason", "3.1.1.12", Valid, "unusual reason phrase", Accept},
	{"noreason", "3.1.1.13", Valid, "empty reason phrase", Accept},
	{"badinv01", "3.1.2.1", Invalid, "extraneous header field separators", Dropped},
	{"clerr", "3.1.2.2", Invalid, "Content-Length larger than message", ParseRejected},
	{"ncl", "3.1.2.3", Invalid, "negative Content-Length", ParseRejected},
	{"scalar02", "3.1.2.4", Invalid, "request scalar fields with overlarge values", 400},
	{"scalarlg", "3.1.2.5", Invalid, "response scalar fields with overlarge values", Dropped},
	{"quotbal", "3.1.2.6", Invalid, "unterminated quoted string in display name", 400},
	{"ltgtruri", "3.1.2.7", Invalid, "<> enclosing Request-URI", ParseRejected},
	{"lwsruri", "3.1.2.8", Invalid, "malformed Request-URI (embedded LWS)", ParseRejected},
	{"lwsstart", "3.1.2.9", Liberal, "multiple SP separating Request-Line elements", ParseRejected},
	{"trws", "3.1.2.10", Liberal, "SP characters at end of Request-Line", ParseRejected},
	{"escruri", "3.1.2.11", Liberal, "escaped headers in Request-URI", Accept},
	{"baddate", "3.1.2.12", Liberal, "invalid time zone in Date", Accept},
	{"regbadct", "3.1.2.13", Liberal, "name-addr URI not enclosed in <>", Accept},
	{"badaspec", "3.1.2.14", Liberal, "spaces within addr-spec", 400},
	{"baddn", "3.1.2.15", Invalid, "non-token characters in display name; the archive's file lacks the final empty line", ParseRejected},
	{"badvers", "3.1.2.16", Invalid, "unknown protocol version (505)", 505},
	{"mismatch01", "3.1.2.17", Invalid, "start line and CSeq method mismatch", 400},
	{"mismatch02", "3.1.2.18", Invalid, "unknown method with CSeq method mismatch", 400},
	{"bigcode", "3.1.2.19", Invalid, "overlarge response code", ParseRejected},
	{"badbranch", "3.2.1", Semantic, "missing transaction identifier (no z9hG4bK)", 400},
	{"insuf", "3.3.1", Invalid, "missing required header fields (400)", 400},
	{"unkscm", "3.3.2", Semantic, "Request-URI with unknown scheme (416)", 416},
	{"novelsc", "3.3.3", Semantic, "Request-URI with known but atypical scheme", 416},
	{"unksm2", "3.3.4", Semantic, "unknown URI schemes in header fields", Accept},
	{"bext01", "3.3.5", Semantic, "Proxy-Require and Require (420)", Accept},
	{"invut", "3.3.6", Semantic, "unknown Content-Type (415)", Accept},
	{"regaut01", "3.3.7", Semantic, "unknown authorization scheme", Accept},
	{"multi01", "3.3.8", Invalid, "multiple values in single-value required fields (400)", 400},
	{"mcl01", "3.3.9", Invalid, "multiple Content-Length values (400)", ParseRejected},
	{"bcast", "3.3.10", Semantic, "200 OK with broadcast Via", Accept},
	{"zeromf", "3.3.11", Semantic, "Max-Forwards of zero (483 for proxies)", Accept},
	{"cparam01", "3.3.12", Semantic, "REGISTER with a Contact header parameter", Accept},
	{"cparam02", "3.3.13", Semantic, "REGISTER with a url-parameter", Accept},
	{"regescrt", "3.3.14", Semantic, "REGISTER with a URL escaped header", Accept},
	{"sdp01", "3.3.15", Semantic, "unacceptable Accept offering (406)", Accept},
	{"inv2543", "3.4.1", Semantic, "INVITE with RFC 2543 syntax", 400},
	{"test", "-", Invalid, "extra file in the archive: no SIP version, bare header line", ParseRejected},
}

func TortureMessage(name string) ([]byte, error) {
	return testdata.ReadFile("testdata/rfc4475/" + name + ".dat")
}

func RunTorture(t *testing.T) {
	t.Helper()

	for _, tc := range torture {
		t.Run(tc.Name, func(t *testing.T) {
			raw, err := TortureMessage(tc.Name)
			if err != nil {
				t.Fatal(err)
			}

			if problem := tortureProblem(tc, raw); problem != "" {
				t.Errorf("%s %s (%s): %s", tc.Section, tc.Expect, tc.Note, problem)
			}
		})
	}
}

func tortureProblem(tc TortureCase, raw []byte) string {
	msg, err := sip.Parse(raw)

	if got := outcome(msg, err); got != tc.Outcome {
		return fmt.Sprintf("outcome %d, want %d", got, tc.Outcome)
	}

	switch tc.Expect {
	case Valid, Semantic:
		if err != nil {
			return "rejected by the parser: " + err.Error()
		}

		if tc.Expect == Valid {
			return validRoundTrip(msg, raw)
		}
	case Invalid:
		if err != nil {
			return ""
		}

		if verr := msg.Validate(); verr != nil {
			return ""
		}

		return "accepted"
	case Liberal:
		if err == nil {
			if _, rerr := sip.Parse(msg.Bytes()); rerr != nil {
				return "accepted, but its serialization does not parse: " + rerr.Error()
			}
		}
	}

	return ""
}

func validRoundTrip(msg sip.Message, raw []byte) string {
	ref, ok := scan(raw)
	if !ok {
		return "fixture not scannable"
	}

	if !bytes.Equal(msg.Env().Body, ref.Body) {
		return fmt.Sprintf("body %q, want %q", msg.Env().Body, ref.Body)
	}

	again, err := sip.Parse(msg.Bytes())
	if err != nil {
		return "serialization does not parse: " + err.Error()
	}

	if !bytes.Equal(again.Env().Body, ref.Body) {
		return fmt.Sprintf("body after round trip %q, want %q", again.Env().Body, ref.Body)
	}

	if again.StartLine() != msg.StartLine() || msg.StartLine() != ref.StartLine {
		return fmt.Sprintf("start line %q, then %q, want %q", msg.StartLine(), again.StartLine(), ref.StartLine)
	}

	if req, ok := msg.(*sip.Request); ok {
		_, rest, _ := strings.Cut(ref.StartLine, " ")
		if got, want := fromURI(req.URI), referenceURI(strings.TrimSuffix(rest, " SIP/2.0")); got != want {
			return fmt.Sprintf("Request-URI %v, want %v", got, want)
		}
	}

	return ""
}

func outcome(msg sip.Message, err error) int {
	if err != nil {
		return ParseRejected
	}

	var serr *sip.StatusError

	switch verr := msg.Validate(); {
	case verr == nil:
		return Accept
	case errors.As(verr, &serr):
		return serr.StatusCode
	default:
		return Dropped
	}
}
