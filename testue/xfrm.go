package testue

import (
	"net/netip"
	"syscall"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/ue"
)

type XFRM interface {
	ue.IPsec
	Close() error
}

func OpenXFRM() (XFRM, error) {
	x, err := ipsec.Open()
	if err != nil {
		return nil, err
	}

	return xfrm{Selectors(x), x}, nil
}

type xfrm struct {
	ue.IPsec

	x *ipsec.XFRM
}

func (k xfrm) Close() error { return k.x.Close() }

func XFRMOf(x *ipsec.XFRM) ue.IPsec { return Selectors(x) }

type SelectorKernel interface {
	Install(s ipsec.Set, k ipsec.Keys) error
	Remove(s ipsec.Set) error
}

func Selectors(k SelectorKernel) ue.IPsec { return selectors{k: k, spis: ipsec.NewSPIs()} }

type selectors struct {
	k    SelectorKernel
	spis *ipsec.SPIs
}

func (k selectors) AllocateSPI(netip.Addr) (uint32, error) { return k.spis.AllocateOne() }

func (k selectors) ReleaseSPI(spi uint32) { k.spis.Release(spi) }

func (k selectors) Install(s ue.SASet, keys ue.Keys) error {
	return k.k.Install(ToIPsec(s), ipsec.Keys{CK: keys.CK, IK: keys.IK})
}

func (k selectors) Remove(s ue.SASet) error { return k.k.Remove(ToIPsec(s)) }

func (selectors) Protect(ue.SASet, uint16, syscall.RawConn) error { return nil }

func ToIPsec(s ue.SASet) ipsec.Set {
	ep := func(e ue.Endpoint) ipsec.Endpoint {
		return ipsec.Endpoint{Addr: e.Addr, PortC: e.PortC, PortS: e.PortS, SPIC: e.SPIC, SPIS: e.SPIS}
	}

	return ipsec.Set{
		Local:      ep(s.Local),
		Remote:     ep(s.Remote),
		Integrity:  ipsec.Integrity(s.Integrity),
		Encryption: ipsec.Encryption(s.Encryption),
	}
}
