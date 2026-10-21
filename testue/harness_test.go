package testue

import (
	"testing"

	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/uetest"
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
		cfg.Kernel = Selectors(kernel)
	}

	u, err := New(cfg)
	if err != nil {
		n.T.Fatal(err)
	}

	n.T.Cleanup(func() { _ = u.Close() })

	return u
}
