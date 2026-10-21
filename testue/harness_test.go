package testue

import (
	"testing"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/uetest"
	"github.com/ellanetworks/ims/ue"
)

const (
	imsi   = uetest.IMSI
	domain = uetest.Domain
	imei   = uetest.IMEI
)

var (
	loopback = uetest.Loopback
	testK    = uetest.K
	testOPc  = uetest.OPc
)

// network is the network of the UEs of the tests.
type network struct{ *uetest.Network }

func newNetwork(t *testing.T) *network { return &network{uetest.New(t)} }

func (n *network) newUE(cfg Config) *UE {
	n.T.Helper()

	kernel := ipsectest.NewKernel()

	cfg.IMSI, cfg.IMEI = imsi, imei
	cfg.PCSCF, cfg.Local = n.PCSCF.Addr(), n.Addr
	cfg.NoRegEvent = !n.RegEvent

	if cfg.K == nil {
		cfg.K, cfg.OPc = testK, testOPc
	}

	if !cfg.Plain {
		cfg.Kernel = kernelIPsec{kernel}
	}

	u, err := New(cfg)
	if err != nil {
		n.T.Fatal(err)
	}

	n.T.Cleanup(func() { _ = u.Close() })

	return u
}

// kernelIPsec is the fake kernel of ipsectest as the UE's IPsec.
type kernelIPsec struct{ k *ipsectest.Kernel }

func (k kernelIPsec) Install(s ue.SASet, keys ue.Keys) error {
	return k.k.Install(ToIPsec(s), ipsecKeys(keys))
}

func (k kernelIPsec) Remove(s ue.SASet) error { return k.k.Remove(ToIPsec(s)) }

func ipsecKeys(k ue.Keys) ipsec.Keys { return ipsec.Keys{CK: k.CK, IK: k.IK} }
