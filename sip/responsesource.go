package sip

import (
	"fmt"
	"strings"
)

const feNamespace = "urn:3gpp:fe:"

// The P-CSCF, as a functional entity of a Response-Source, and its side toward the UE (TS 24.229 table 7.2.17.7-2).
const (
	EntityPCSCF = "p-cscf"
	SideTerm    = "term"
)

// ResponseSource is a Response-Source value: the IMS functional entity that sent an error response (TS 24.229
// §7.2.17). Its tokens other than the "fe" parameter are not kept.
type ResponseSource struct {
	// Entity is the functional entity, an fe-id followed by its fe-params, as "p-cscf.term".
	Entity string
}

func ParseResponseSource(s string) (ResponseSource, error) {
	var r ResponseSource

	for t := trimWSP(s); ; {
		n := tokenLen(t)

		switch {
		case n == 0:
			return ResponseSource{}, fmt.Errorf("response-source %q: missing source-params", s)
		case strings.EqualFold(t[:n], "fe"):
			v := trimLeftWSP(t[n:])
			if v == "" || v[0] != '=' {
				return ResponseSource{}, fmt.Errorf("response-source %q: missing fe value", s)
			}

			v = trimLeftWSP(v[1:])

			end := strings.IndexByte(v, '>')
			if v == "" || v[0] != '<' || end < 0 {
				return ResponseSource{}, fmt.Errorf("response-source %q: fe value not in angle brackets", s)
			}

			urn := v[1:end]
			if len(urn) <= len(feNamespace) || !strings.EqualFold(urn[:len(feNamespace)], feNamespace) {
				return ResponseSource{}, fmt.Errorf("response-source %q: not a %s URN", s, feNamespace)
			}

			r.Entity = urn[len(feNamespace):]
			t = trimLeftWSP(v[end+1:])
		default:
			t = trimLeftWSP(t[n:])
		}

		if t == "" {
			break
		}

		if t[0] != ';' {
			return ResponseSource{}, fmt.Errorf("response-source %q: unexpected %q", s, t)
		}

		t = trimLeftWSP(t[1:])
	}

	if r.Entity == "" {
		return ResponseSource{}, fmt.Errorf("response-source %q: missing fe", s)
	}

	return r, nil
}

// FunctionalEntity returns the fe-id of the entity, as "p-cscf", in lower case.
func (r ResponseSource) FunctionalEntity() string {
	id, _, _ := strings.Cut(r.Entity, ".")
	return strings.ToLower(id)
}

func (r ResponseSource) String() string {
	return "fe=<" + feNamespace + r.Entity + ">"
}

func (fs Header) ResponseSource() (ResponseSource, bool, error) {
	v, ok, err := fs.single("Response-Source")
	if !ok || err != nil {
		return ResponseSource{}, ok, err
	}

	r, err := ParseResponseSource(v)

	return r, true, err
}
