package testue

import (
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/ue"
)

// XFRM is the kernel's IPsec as the UE's: its SAs in the SAD and SPD of the namespace it was opened in, by netlink.
type XFRM interface {
	ue.IPsec
	Close() error
}

func OpenXFRM() (XFRM, error) {
	x, err := ipsec.Open()
	if err != nil {
		return nil, err
	}

	return xfrm{x}, nil
}

type xfrm struct{ x *ipsec.XFRM }

func (k xfrm) Install(s ue.SASet, keys ue.Keys) error {
	return k.x.Install(ToIPsec(s), ipsec.Keys{CK: keys.CK, IK: keys.IK})
}

func (k xfrm) Remove(s ue.SASet) error { return k.x.Remove(ToIPsec(s)) }

func (k xfrm) Close() error { return k.x.Close() }

// ToIPsec is an SA set of the UE in the module's ipsec package: what a kernel or a P-CSCF installs for it.
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

// XFRMOf is x as the UE's IPsec: a netlink socket opened in the UE's namespace, which the test also uses.
func XFRMOf(x *ipsec.XFRM) ue.IPsec { return xfrm{x} }
