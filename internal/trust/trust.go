// Package trust implements the IMS trust domain (TS 24.229 §4.4): which
// sources are trusted, and the header fields and URI parameters removed when
// signalling crosses the domain's boundary.
package trust

import (
	"net/netip"
	"strings"

	"github.com/ellanetworks/ims/sip"
)

// boundary lists the header fields that a trust domain applies to (§4.4.1).
var boundary = []string{
	"P-Asserted-Identity",
	"P-Access-Network-Info",
	"History-Info",
	"P-Asserted-Service",
	"Resource-Priority",
	"P-Profile-Key",
	"P-Served-User",
	"P-Private-Network-Indication",
	"P-Early-Media",
	"Feature-Caps",
	"Restoration-Info",
	"Relayed-Charge",
	"Service-Interact-Info",
	"Cellular-Network-Info",
	"Response-Source",
	"Attestation-Info",
	"Origination-Id",
	"Additional-Identity",
	"Priority-Verstat",
	"DC-Info",
}

type Domain struct {
	networks []netip.Prefix
}

// New returns the trust domain made of the IMS's own addresses and the
// networks with an interconnect agreement.
func New(addresses []netip.Addr, networks []netip.Prefix) *Domain {
	d := &Domain{}

	for _, a := range addresses {
		a = a.Unmap()
		d.networks = append(d.networks, netip.PrefixFrom(a, a.BitLen()))
	}

	for _, p := range networks {
		d.networks = append(d.networks, p.Masked())
	}

	return d
}

func (d *Domain) Trusted(a netip.Addr) bool {
	a = a.Unmap()

	for _, p := range d.networks {
		if p.Contains(a) {
			return true
		}
	}

	return false
}

// StripRequest removes what a request from outside the trust domain must not
// carry into it: the boundary header fields, and the "cpc", "oli" (§4.4.12)
// and "iotl" (§4.4.15) parameters of the Request-URI.
func StripRequest(r *sip.Request) {
	for _, name := range boundary {
		r.Header.Del(name)
	}

	r.URI = stripURIParams(r.URI)
}

// StripResponse removes what a response must not carry out of the trust
// domain: the boundary header fields, Reason (§4.4.7) and
// P-Charging-Function-Addresses (TS 24.229 §5.3.2.1).
func StripResponse(r *sip.Response) {
	for _, name := range boundary {
		r.Header.Del(name)
	}

	r.Header.Del("Reason")
	r.Header.Del("P-Charging-Function-Addresses")
}

func stripURIParams(u sip.URI) sip.URI {
	u = u.Clone()

	switch {
	case u.IsTel():
		u.Params.Del("cpc")
		u.Params.Del("oli")
	case u.IsSIP():
		u.Params.Del("iotl")

		if i := strings.IndexByte(u.User, ';'); i >= 0 {
			u.User = u.User[:i] + stripUserParams(u.User[i:])
		}
	}

	return u
}

// stripUserParams removes "cpc" and "oli" from the tel parameters carried in
// the user part of a SIP URI.
func stripUserParams(s string) string {
	var b strings.Builder

	for p := range strings.SplitSeq(s[1:], ";") {
		name, _, _ := strings.Cut(p, "=")
		if strings.EqualFold(name, "cpc") || strings.EqualFold(name, "oli") {
			continue
		}

		b.WriteString(";" + p)
	}

	return b.String()
}
