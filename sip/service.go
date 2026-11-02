package sip

import (
	"fmt"
	"strings"
)

// RFC 6050 section 4.2
func (fs Header) AssertedServices() ([]string, error) {
	return fs.serviceIDs("P-Asserted-Service")
}

// RFC 6050 section 4.3
func (fs Header) PreferredServices() ([]string, error) {
	return fs.serviceIDs("P-Preferred-Service")
}

func (fs Header) serviceIDs(name string) ([]string, error) {
	var out []string

	for _, v := range fs.Elements(name) {
		id := strings.ToLower(v)
		if !IsServiceID(id) {
			return nil, fmt.Errorf("%s: invalid Service-ID %q", name, v)
		}

		out = append(out, id)
	}

	return out, nil
}

// RFC 6050 section 4.4
func IsServiceID(s string) bool {
	if len(s) < 10 || !strings.EqualFold(s[:10], "urn:urn-7:") {
		return false
	}

	for i, l := range strings.Split(s[10:], ".") {
		if l == "" || i == 0 && len(l) > 27 || strings.ContainsFunc(l, func(r rune) bool { return r > 0x7e || !isAlnum(byte(r)) && r != '-' }) {
			return false
		}
	}

	return true
}

// TS 24.229 clause 7.2A.8.1
func ServiceIs(id, service string) bool {
	id, service = strings.ToLower(id), strings.ToLower(service)
	return id == service || strings.HasPrefix(id, service+".")
}
