package icscf

import (
	"log/slog"
	"net/netip"
	"slices"
	"strings"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
)

// lookup returns the S-CSCF a URI names (RFC 3261 §19.1.4).
func (i *ICSCF) lookup(u sip.URI) *SCSCF {
	for _, s := range i.scscfs {
		if s.Name.Equivalent(u) {
			return s
		}
	}

	return nil
}

func (i *ICSCF) lookupName(name string) *SCSCF {
	u, err := sip.ParseURI(name)
	if err != nil {
		return nil
	}

	return i.lookup(u)
}

// choose selects an S-CSCF from Server-Capabilities (TS 29.228 Annex A),
// excluding the S-CSCFs already tried. Without capabilities, any S-CSCF fits.
func (i *ICSCF) choose(caps *cx.ServerCapabilities, tried []*SCSCF) *SCSCF {
	if caps != nil && len(caps.ServerNames) > 0 {
		for _, name := range caps.ServerNames {
			if s := i.lookupName(name); s != nil && !slices.Contains(tried, s) {
				return s
			}
		}

		return nil
	}

	var (
		best  *SCSCF
		score = -1
	)

	for _, s := range i.scscfs {
		if slices.Contains(tried, s) || caps != nil && !supportsAll(s, caps.Mandatory) {
			continue
		}

		n := 0

		if caps != nil {
			for _, c := range caps.Optional {
				if slices.Contains(s.Capabilities, c) {
					n++
				}
			}
		}

		if n > score {
			best, score = s, n
		}
	}

	if best == nil && caps != nil && len(tried) == 0 {
		i.log.Error("no S-CSCF has the mandatory capabilities required by the HSS",
			slog.Any("mandatory", caps.Mandatory), slog.Any("optional", caps.Optional))
	}

	return best
}

// untried reports whether an S-CSCF is left to select.
func (i *ICSCF) untried(tried []*SCSCF) bool {
	return slices.ContainsFunc(i.scscfs, func(s *SCSCF) bool { return !slices.Contains(tried, s) })
}

func supportsAll(s *SCSCF, capabilities []uint32) bool {
	for _, c := range capabilities {
		if !slices.Contains(s.Capabilities, c) {
			return false
		}
	}

	return true
}

// target is the flow towards an S-CSCF, from the I-CSCF listener on the
// address the request arrived on. The URI's transport parameter picks the
// transport.
func (i *ICSCF) target(s *SCSCF, in sip.Flow, u sip.URI) (proxy.Target, bool) {
	tr := sip.UDP

	if v, ok := u.Params.Get("transport"); ok {
		switch tr = sip.Transport(strings.ToUpper(v)); tr {
		case sip.UDP, sip.TCP:
		default:
			return proxy.Target{}, false
		}
	}

	local := in.Local.Addr().Unmap()

	remote, ok := pick(s.Listeners, func(a netip.Addr) bool { return a == local })
	if !ok {
		remote, ok = pick(s.Listeners, func(a netip.Addr) bool { return a.Is4() == local.Is4() })
	}

	if !ok {
		return proxy.Target{}, false
	}

	return proxy.Target{Flow: sip.Flow{
		Transport: tr,
		Local:     netip.AddrPortFrom(remote.Addr(), i.cfg.Port),
		Remote:    remote,
	}}, true
}

func pick(listeners []netip.AddrPort, match func(netip.Addr) bool) (netip.AddrPort, bool) {
	for _, l := range listeners {
		if match(l.Addr()) {
			return l, true
		}
	}

	return netip.AddrPort{}, false
}
