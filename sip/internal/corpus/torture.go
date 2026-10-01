package corpus

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

// Expectation is what RFC 4475 expects a SIP element to do with a
// torture message.
type Expectation int

const (
	// Valid messages must be parsed and processed.
	Valid Expectation = iota
	// Invalid messages must be rejected: by the parser, or by request
	// validation before a transaction is created (400/505).
	Invalid
	// Liberal messages are invalid, but RFC 4475 also allows an element to
	// be liberal and accept them, as long as it does not forward the bad
	// syntax. The parser must not break either way.
	Liberal
	// Semantic messages are syntactically valid; rejecting them is the
	// job of the application or of request validation (e.g. 416, 420,
	// 483), not of the parser.
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

// TortureCase is one RFC 4475 message (file testdata/rfc4475/<Name>.dat).
type TortureCase struct {
	Name    string
	Section string
	Expect  Expectation
	Note    string
}

// TortureCases lists every message in the RFC 4475 Appendix A archive.
func TortureCases() []TortureCase {
	return slices.Clone(torture)
}

var torture = []TortureCase{
	{"wsinv", "3.1.1.1", Valid, "short tortuous INVITE: folding, odd whitespace, compact forms"},
	{"intmeth", "3.1.1.2", Valid, "wide range of valid characters"},
	{"esc01", "3.1.1.3", Valid, "valid use of % escaping"},
	{"escnull", "3.1.1.4", Valid, "escaped nulls in URIs"},
	{"esc02", "3.1.1.5", Valid, "use of % when it is not an escape"},
	{"lwsdisp", "3.1.1.6", Valid, "message with no LWS between display name and <"},
	{"longreq", "3.1.1.7", Valid, "long values in header fields"},
	{"dblreq", "3.1.1.8", Valid, "extra trailing octets in a UDP datagram"},
	{"semiuri", "3.1.1.9", Valid, "semicolon-separated parameters in URI user part"},
	{"transports", "3.1.1.10", Valid, "varied and unknown transport types"},
	{"mpart01", "3.1.1.11", Valid, "multipart MIME message"},
	{"unreason", "3.1.1.12", Valid, "unusual reason phrase"},
	{"noreason", "3.1.1.13", Valid, "empty reason phrase"},
	{"badinv01", "3.1.2.1", Invalid, "extraneous header field separators"},
	{"clerr", "3.1.2.2", Invalid, "Content-Length larger than message"},
	{"ncl", "3.1.2.3", Invalid, "negative Content-Length"},
	{"scalar02", "3.1.2.4", Invalid, "request scalar fields with overlarge values"},
	{"scalarlg", "3.1.2.5", Invalid, "response scalar fields with overlarge values"},
	{"quotbal", "3.1.2.6", Invalid, "unterminated quoted string in display name"},
	{"ltgtruri", "3.1.2.7", Invalid, "<> enclosing Request-URI"},
	{"lwsruri", "3.1.2.8", Invalid, "malformed Request-URI (embedded LWS)"},
	{"lwsstart", "3.1.2.9", Liberal, "multiple SP separating Request-Line elements"},
	{"trws", "3.1.2.10", Liberal, "SP characters at end of Request-Line"},
	{"escruri", "3.1.2.11", Liberal, "escaped headers in Request-URI"},
	{"baddate", "3.1.2.12", Liberal, "invalid time zone in Date"},
	{"regbadct", "3.1.2.13", Liberal, "name-addr URI not enclosed in <>"},
	{"badaspec", "3.1.2.14", Liberal, "spaces within addr-spec"},
	{"baddn", "3.1.2.15", Invalid, "non-token characters in display name"},
	{"badvers", "3.1.2.16", Invalid, "unknown protocol version (505)"},
	{"mismatch01", "3.1.2.17", Invalid, "start line and CSeq method mismatch"},
	{"mismatch02", "3.1.2.18", Invalid, "unknown method with CSeq method mismatch"},
	{"bigcode", "3.1.2.19", Invalid, "overlarge response code"},
	{"badbranch", "3.2.1", Semantic, "missing transaction identifier (no z9hG4bK)"},
	{"insuf", "3.3.1", Invalid, "missing required header fields (400)"},
	{"unkscm", "3.3.2", Semantic, "Request-URI with unknown scheme (416)"},
	{"novelsc", "3.3.3", Semantic, "Request-URI with known but atypical scheme"},
	{"unksm2", "3.3.4", Semantic, "unknown URI schemes in header fields"},
	{"bext01", "3.3.5", Semantic, "Proxy-Require and Require (420)"},
	{"invut", "3.3.6", Semantic, "unknown Content-Type (415)"},
	{"regaut01", "3.3.7", Semantic, "unknown authorization scheme"},
	{"multi01", "3.3.8", Invalid, "multiple values in single-value required fields (400)"},
	{"mcl01", "3.3.9", Invalid, "multiple Content-Length values (400)"},
	{"bcast", "3.3.10", Semantic, "200 OK with broadcast Via"},
	{"zeromf", "3.3.11", Semantic, "Max-Forwards of zero (483 for proxies)"},
	{"cparam01", "3.3.12", Semantic, "REGISTER with a Contact header parameter"},
	{"cparam02", "3.3.13", Semantic, "REGISTER with a url-parameter"},
	{"regescrt", "3.3.14", Semantic, "REGISTER with a URL escaped header"},
	{"sdp01", "3.3.15", Semantic, "unacceptable Accept offering (406)"},
	{"inv2543", "3.4.1", Semantic, "INVITE with RFC 2543 syntax"},
	{"test", "-", Invalid, "extra file in the archive: no SIP version, bare header line"},
}

// TortureMessage returns the raw message of a torture case.
func TortureMessage(name string) ([]byte, error) {
	return testdata.ReadFile("testdata/rfc4475/" + name + ".dat")
}

// RunTorture runs every RFC 4475 case as a subtest.
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

// tortureProblem returns why the sip package does not meet a case's
// expectation, or "".
func tortureProblem(tc TortureCase, raw []byte) string {
	msg, err := sip.Parse(raw)

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
		// Either outcome is allowed; a liberal parse must still serialize
		// into something parseable.
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
