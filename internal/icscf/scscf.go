package icscf

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
)

func (i *ICSCF) ours(name string) bool {
	u, err := sip.ParseURI(name)

	return err == nil && i.cfg.SCSCF.Name.Equivalent(u)
}

// assigned is the S-CSCF a REGISTER goes to: ours, the only one of the network, whatever S-CSCF the HSS names.
func (i *ICSCF) assigned(name, impu string) sip.URI {
	if i.ours(name) {
		u, _ := sip.ParseURI(name)
		return u
	}

	i.log.Warn("the HSS names another S-CSCF; forwarding to ours", slog.String("impu", impu),
		slog.String("server-name", name), slog.String("scscf", i.cfg.SCSCF.Name.String()))

	return i.cfg.SCSCF.Name
}

func (i *ICSCF) capable(caps *cx.ServerCapabilities) bool {
	if caps == nil {
		return true
	}

	if len(caps.ServerNames) > 0 {
		if slices.ContainsFunc(caps.ServerNames, i.ours) {
			return true
		}
	} else if !slices.ContainsFunc(caps.Mandatory, func(c uint32) bool { return !slices.Contains(i.cfg.SCSCF.Capabilities, c) }) {
		return true
	}

	i.log.Error("the S-CSCF lacks the capabilities required by the HSS", slog.Any("mandatory", caps.Mandatory),
		slog.Any("server-names", caps.ServerNames), slog.Any("capabilities", i.cfg.SCSCF.Capabilities))

	return false
}

func (i *ICSCF) target(in sip.Flow, u sip.URI) (proxy.Target, bool) {
	tr := sip.UDP

	if v, ok := u.Params.Get("transport"); ok {
		switch tr = sip.Transport(strings.ToUpper(v)); tr {
		case sip.UDP, sip.TCP:
		default:
			return proxy.Target{}, false
		}
	}

	local := in.Local.Addr().Unmap()
	listeners := i.cfg.SCSCF.Listeners

	remote, ok := pick(listeners, func(a netip.Addr) bool { return a == local })
	if !ok {
		remote, ok = pick(listeners, func(a netip.Addr) bool { return a.Is4() == local.Is4() })
	}

	if !ok {
		return proxy.Target{}, false
	}

	return proxy.Target{Flow: sip.Flow{
		Transport: tr,
		Local:     netip.AddrPortFrom(in.Local.Addr(), i.cfg.Port),
		Remote:    remote,
	}}, true
}

// serverTarget is the flow to a server the HSS names, from the I-CSCF's address of the request's flow: the first
// address the URI resolves to (RFC 3263 §4) in that address family and in the trust domain. The trust domain goes by
// the address, whatever the form of the URI.
func (i *ICSCF) serverTarget(ctx context.Context, in sip.Flow, u sip.URI) (proxy.Target, error) {
	servers, err := i.cfg.Resolver.Resolve(ctx, u)
	if err != nil {
		return proxy.Target{}, err
	}

	err = fmt.Errorf("no I-CSCF address in the address family of %s", u)

	for _, srv := range servers {
		a := srv.Addr.Addr()

		switch {
		case in.Local.Addr().Unmap().Is4() != a.Is4():
			continue
		case !i.cfg.Trust.Trusted(a):
			err = fmt.Errorf("%s is outside the trust domain", a)
			continue
		}

		return proxy.Target{Flow: sip.Flow{
			Transport: srv.Transport, Local: netip.AddrPortFrom(in.Local.Addr(), i.cfg.Port), Remote: srv.Addr,
		}}, nil
	}

	return proxy.Target{}, err
}

func pick(listeners []netip.AddrPort, match func(netip.Addr) bool) (netip.AddrPort, bool) {
	for _, l := range listeners {
		if match(l.Addr()) {
			return l, true
		}
	}

	return netip.AddrPort{}, false
}
