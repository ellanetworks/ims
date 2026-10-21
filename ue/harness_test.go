package ue

import (
	"errors"
	"net/netip"
	"slices"
	"sync"
	"syscall"
	"testing"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/uetest"
	"github.com/ellanetworks/ims/internal/usim"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

const (
	imsi   = uetest.IMSI
	domain = uetest.Domain
	impi   = uetest.IMPI
	impu   = uetest.IMPU
	msisdn = uetest.MSISDN
	imei   = uetest.IMEI

	// accessNetworkInfo is the P-Access-Network-Info of the test UEs.
	accessNetworkInfo = "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001" + "0000001"
)

var (
	loopback = uetest.Loopback
	testK    = uetest.K
	testOPc  = uetest.OPc
)

// network is the network of the UEs of the tests.
type network struct{ *uetest.Network }

func newNetwork(t *testing.T) *network { return &network{uetest.New(t)} }

func newNetworkAt(t *testing.T, addr netip.Addr) *network { return &network{uetest.NewAt(t, addr)} }

// testUE is a UE of the tests, with its USIM and its events.
type testUE struct {
	*UE

	sim    *usim.USIM
	events chan Event
	ipsec  *kernelIPsec
}

func (u *testUE) Events() <-chan Event { return u.events }

func (u *testUE) SQN() uint64 { return u.sim.SQN() }

// newUE is a UE of the network's subscriber, configured with cfg, its USIM at SQN 0.
func (n *network) newUE(cfg Config) (*testUE, *ipsectest.Kernel) {
	return n.newUEAt(cfg, 0)
}

// newUEAt is a UE of the network's subscriber, configured with cfg, its USIM at sqn.
func (n *network) newUEAt(cfg Config, sqn uint64) (*testUE, *ipsectest.Kernel) {
	n.T.Helper()

	kernel := ipsectest.NewKernel()
	u := &testUE{sim: usim.New(testK, testOPc, sqn), events: make(chan Event, 1024)}
	u.ipsec = &kernelIPsec{k: kernel, spis: ipsec.NewSPIs()}

	u.UE = n.newWith(cfg, u.sim, u.ipsec, func(e Event) {
		select {
		case u.events <- e:
		default:
		}
	})

	return u, kernel
}

func (n *network) newWith(cfg Config, sim *usim.USIM, kernel *kernelIPsec, onEvent func(Event)) *UE {
	n.T.Helper()

	if cfg.Identities == (Identities{}) {
		cfg.Identities, _ = IdentitiesFromIMSI(imsi, 2)
	}

	if cfg.Services == nil {
		cfg.Services = phoneServices()
	}

	cfg.InstanceID, _ = IMEIInstanceID(imei)
	cfg.AKA = simAKA{sim}
	cfg.PCSCF, cfg.Local = n.PCSCF.Addr(), n.Addr
	cfg.NoRegEvent = !n.RegEvent
	cfg.OnEvent = onEvent

	if cfg.AccessNetworkInfo == "" && !cfg.Plain {
		cfg.AccessNetworkInfo = accessNetworkInfo
	}

	if !cfg.Plain {
		cfg.IPsec = kernel
	}

	u, err := New(cfg)
	if err != nil {
		n.T.Fatal(err)
	}

	n.T.Cleanup(func() { _ = u.Close() })

	return u
}

// phoneServices are the services of a phone's UE: voice calls of MMTel (TS 24.173 §5.1) and SMS over IP (TS 24.341
// §5.3.1.2), as the tests see them.
func phoneServices() []Service {
	mmtel := sip.NewFeature("+g.3gpp.icsi-ref", icsiMMTel)
	smsip := sip.FeatureSet{sip.NewFeature("+g.3gpp.smsip")}

	return []Service{
		{
			Name:          "mmtel",
			ICSI:          icsiMMTel,
			Features:      sip.FeatureSet{sip.NewFeature("audio")},
			AcceptContact: []sip.CallerPreference{{Features: sip.FeatureSet{mmtel}}},
			Allow:         []string{"INVITE", "UPDATE", "PRACK"},
			OnInvite: func(r *ua.Request) ua.Handler {
				_ = r.Reject(486)
				return ua.Handler{}
			},
		},
		{
			Name:             "sms",
			RegisterFeatures: smsip,
			AcceptContact:    []sip.CallerPreference{{Features: smsip}},
			Allow:            []string{"MESSAGE"},
			OnRequest:        func(r *ua.Request) { _ = r.Respond(ua.Reply{Code: 200}) },
		},
	}
}

const icsiMMTel = "urn:urn-7:3gpp-service.ims.icsi.mmtel"

// simAKA is a USIM as an Authenticator.
type simAKA struct{ u *usim.USIM }

func (a simAKA) Authenticate(rand, autn []byte) (AKAResult, error) {
	res, ck, ik, auts, err := a.u.Authenticate(rand, autn)
	if errors.Is(err, usim.ErrMACFailure) {
		return AKAResult{}, ErrMACFailure
	}

	return AKAResult{RES: res, CK: ck, IK: ik, AUTS: auts}, err
}

// kernelIPsec is the fake kernel of ipsectest as the UE's IPsec: a kernel of selectors, SPIs of its own; it records
// the SPIs allocated and released, and the sockets the UE has it protect, by port and type.
type kernelIPsec struct {
	k    *ipsectest.Kernel
	spis *ipsec.SPIs

	mu                  sync.Mutex
	allocated, released []uint32
	protected           []protection
}

type protection struct {
	set  SASet
	port uint16
	typ  int
}

func (k *kernelIPsec) AllocateSPI(netip.Addr) (uint32, error) {
	spi, err := k.spis.AllocateOne()

	k.mu.Lock()
	defer k.mu.Unlock()

	k.allocated = append(k.allocated, spi)

	return spi, err
}

func (k *kernelIPsec) ReleaseSPI(spi uint32) {
	k.spis.Release(spi)

	k.mu.Lock()
	defer k.mu.Unlock()

	k.released = append(k.released, spi)
}

func (k *kernelIPsec) Protect(s SASet, port uint16, c syscall.RawConn) error {
	typ := 0

	if err := c.Control(func(fd uintptr) { typ, _ = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_TYPE) }); err != nil {
		return err
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	k.protected = append(k.protected, protection{s, port, typ})

	return nil
}

func (k *kernelIPsec) protections() []protection {
	k.mu.Lock()
	defer k.mu.Unlock()

	return slices.Clone(k.protected)
}

func (k *kernelIPsec) Install(s SASet, keys Keys) error {
	return k.k.Install(toIPsec(s), ipsec.Keys{CK: keys.CK, IK: keys.IK})
}

func (k *kernelIPsec) Remove(s SASet) error { return k.k.Remove(toIPsec(s)) }
