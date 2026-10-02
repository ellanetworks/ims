package sip

import (
	"fmt"
	"slices"
	"strings"
)

// SecurityMechanism is one sec-mechanism of a Security-Client, Security-Server
// or Security-Verify header field (RFC 3329 §2.2).
type SecurityMechanism struct {
	Name   string
	Params Params
}

func (m SecurityMechanism) String() string {
	return m.Name + m.Params.String()
}

func ParseSecurityMechanism(s string) (SecurityMechanism, error) {
	name, ps, err := ParseTokenParams(s)
	if err != nil {
		return SecurityMechanism{}, fmt.Errorf("sec-mechanism %w", err)
	}

	return SecurityMechanism{Name: name, Params: ps}, nil
}

// Equal compares mechanism names and parameters without regard to case or
// parameter order.
func (m SecurityMechanism) Equal(o SecurityMechanism) bool {
	if !strings.EqualFold(m.Name, o.Name) || len(m.Params) != len(o.Params) {
		return false
	}

	used := make([]bool, len(o.Params))

next:
	for _, p := range m.Params {
		for i, q := range o.Params {
			if !used[i] && strings.EqualFold(p.Name, q.Name) && strings.EqualFold(p.Value, q.Value) {
				used[i] = true
				continue next
			}
		}

		return false
	}

	return true
}

// EqualSecurityMechanisms compares two lists element by element, in order, as
// Security-Verify must mirror Security-Server (RFC 3329 §2.3.1).
func EqualSecurityMechanisms(a, b []SecurityMechanism) bool {
	return slices.EqualFunc(a, b, SecurityMechanism.Equal)
}

// SecurityMechanisms returns the mechanisms of every name header field, in
// order.
func (fs Header) SecurityMechanisms(name string) ([]SecurityMechanism, error) {
	var out []SecurityMechanism

	for _, e := range fs.Elements(name) {
		m, err := ParseSecurityMechanism(e)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}

		out = append(out, m)
	}

	return out, nil
}
