package sip

import (
	"fmt"
	"strings"
)

// AssertedServices are the service identifiers of P-Asserted-Service (RFC 6050 §4.2), lower-cased.
func (fs Header) AssertedServices() ([]string, error) {
	return fs.serviceIDs("P-Asserted-Service")
}

// PreferredServices are the service identifiers of P-Preferred-Service (RFC 6050 §4.3), lower-cased.
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

// IsServiceID reports whether s is a Service-ID: "urn:urn-7:" and dot-separated labels of letters, digits and
// hyphens, the first of at most 27 (RFC 6050 §4.4).
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

// ServiceIs reports whether a service identifier is service or one of its specialisations, which add labels to it
// (TS 24.229 §7.2A.8.1): urn:urn-7:3gpp-service.ims.icsi.mmtel.hd-video is a urn:urn-7:3gpp-service.ims.icsi.mmtel.
func ServiceIs(id, service string) bool {
	id, service = strings.ToLower(id), strings.ToLower(service)
	return id == service || strings.HasPrefix(id, service+".")
}
