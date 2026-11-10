package trust

import (
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/ellanetworks/ims/sip"
)

// TS 24.229 clause 4.4.1
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
	peers    atomic.Pointer[[]netip.Addr]
}

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

func (d *Domain) SetPeers(addrs []netip.Addr) {
	peers := make([]netip.Addr, len(addrs))
	for i, a := range addrs {
		peers[i] = a.Unmap()
	}

	d.peers.Store(&peers)
}

func (d *Domain) Trusted(a netip.Addr) bool {
	a = a.Unmap()

	for _, p := range d.networks {
		if p.Contains(a) {
			return true
		}
	}

	if peers := d.peers.Load(); peers != nil {
		return slices.Contains(*peers, a)
	}

	return false
}

func (d *Domain) Overlaps(p netip.Prefix) bool {
	p = p.Masked()

	for _, n := range d.networks {
		if n.Overlaps(p) {
			return true
		}
	}

	return false
}

func StripRequest(r *sip.Request) {
	for _, name := range boundary {
		r.Header.Del(name)
	}

	r.URI = StripURI(r.URI)
}

func Boundary(name string) bool {
	for _, b := range boundary {
		if strings.EqualFold(b, name) {
			return true
		}
	}

	return false
}

func StripResponse(r *sip.Response) {
	for _, name := range boundary {
		r.Header.Del(name)
	}

	r.Header.Del("Reason")
	r.Header.Del("P-Charging-Function-Addresses")
}

// TS 24.229 clause 4.4.12
// TS 24.229 clause 4.4.15
func StripURI(u sip.URI) sip.URI {
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
