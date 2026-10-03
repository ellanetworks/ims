package sip

import (
	"fmt"
	"slices"
	"strings"
)

const (
	PrivacyHeader   = "header"
	PrivacySession  = "session"
	PrivacyUser     = "user"
	PrivacyNone     = "none"
	PrivacyCritical = "critical"
	PrivacyID       = "id"
	PrivacyHistory  = "history"
)

type Privacy []string

func ParsePrivacy(s string) (Privacy, error) {
	var out Privacy

	for v := range strings.FieldsFuncSeq(s, func(r rune) bool { return r == ';' || r == ',' || r == ' ' || r == '\t' }) {
		if !isToken(v) {
			return nil, fmt.Errorf("Privacy %q: invalid priv-value %q", s, v)
		}

		out = append(out, v)
	}

	return out, nil
}

func (p Privacy) Has(value string) bool {
	return slices.ContainsFunc(p, func(v string) bool { return strings.EqualFold(v, value) })
}

func (p Privacy) String() string {
	return strings.Join(p, ";")
}

func (fs Header) Privacy() (Privacy, error) {
	var out Privacy

	for _, v := range fs.Values("Privacy") {
		p, err := ParsePrivacy(v)
		if err != nil {
			return nil, err
		}

		out = append(out, p...)
	}

	return out, nil
}
