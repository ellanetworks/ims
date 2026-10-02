//go:build linux && (amd64 || arm64)

package pcscf

import (
	"context"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/netnstest"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transport"
)

func TestMain(m *testing.M) {
	netnstest.Main(m)
}

func openXFRM(t *testing.T, n *netnstest.Netns) *ipsec.XFRM {
	t.Helper()

	var (
		x   *ipsec.XFRM
		err error
	)

	n.Do(func() { x, err = ipsec.Open() })

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = x.Close() })

	return x
}

var (
	xfrmSPI     = regexp.MustCompile(`spi (0x[0-9a-f]+)`)
	xfrmPackets = regexp.MustCompile(`(\d+)\(packets\)`)
)

func espPackets(t *testing.T, n *netnstest.Netns) map[uint32]int {
	t.Helper()

	out, err := n.Command("ip", "-s", "xfrm", "state")
	if err != nil {
		t.Fatal(err)
	}

	packets := make(map[uint32]int)

	for _, block := range strings.Split(string(out), "\nsrc ") {
		spi, count := xfrmSPI.FindStringSubmatch(block), xfrmPackets.FindStringSubmatch(block)
		if spi == nil || count == nil {
			continue
		}

		s, _ := strconv.ParseUint(spi[1], 0, 32)
		c, _ := strconv.Atoi(count[1])
		packets[uint32(s)] = c
	}

	return packets
}

func TestRegistrationOverESP(t *testing.T) {
	for _, tr := range []sip.Transport{sip.UDP, sip.TCP} {
		t.Run(string(tr), func(t *testing.T) { testRegistrationOverESP(t, tr) })
	}
}

func testRegistrationOverESP(t *testing.T, tr sip.Transport) {
	pn, un := netnstest.New(t), netnstest.New(t)
	pAddr, uAddr := netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")
	netnstest.Link(t, pn, un, []netip.Prefix{netip.PrefixFrom(pAddr, 24)}, []netip.Prefix{netip.PrefixFrom(uAddr, 24)})

	pk, uk := openXFRM(t, pn), openXFRM(t, un)

	var s *ipsecScene

	inNetns := transport.Config{Dial: func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error) {
		var (
			c   net.Conn
			err error
		)

		pn.Do(func() { c, err = d.DialContext(ctx, network, address) })

		return c, err
	}}

	pn.Do(func() { s = newIPsecSceneAt(t, pAddr, ipsec.DefaultPolicy(), pk, openStore(t), inNetns) })

	var u *ue

	un.Do(func() {
		s.ue = siptest.NewSocket(t, netip.AddrPortFrom(uAddr, 0))
		u = newUEAt(t, uAddr, 25656)
	})

	server := s.challenge(u)

	offer, err := ipsec.ParseOffer(server)
	if err != nil {
		t.Fatal(err)
	}

	ueSet := ipsec.Set{
		Local:      ipsec.Endpoint{Addr: uAddr, PortC: u.uc.Addr().Port(), PortS: u.us.Addr().Port(), SPIC: u.spiC, SPIS: u.spiS},
		Remote:     offer.Endpoint,
		Integrity:  offer.Integrity,
		Encryption: offer.Encryption,
	}
	ueSet.Remote.Addr = pAddr

	keys := ipsec.Keys{CK: mustHex(t, testCK), IK: mustHex(t, testIK)}
	if err := uk.Install(ueSet, keys); err != nil {
		t.Fatal(err)
	}

	inbound := map[string]uint32{"inbound to the protected server port": offer.Endpoint.SPIS}

	if tr == sip.UDP {
		s.authenticate(u, server)

		inbound["outbound to the UE's server port"] = u.spiS
	} else {
		un.Do(func() { u.uc.Conn(s.ps) })

		r := u.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) {
			r.Header.Add("Security-Verify", server.String())

			via, _ := r.Header.TopVia()
			via.Transport = sip.TCP
			_ = r.Header.SetTopVia(via)
		})
		u.uc.Send(sip.TCP, s.ps, r)

		req, f, integrity := s.forwarded()
		if integrity != "yes" {
			t.Fatalf("integrity-protected = %q, want yes", integrity)
		}

		s.answer(req, f, 200)

		if res, from := u.uc.RecvResponse(); res.StatusCode != 200 || from.Transport != sip.TCP {
			t.Fatalf("got %q over %s, want 200 on the UE's connection", res.StartLine(), from)
		}

		inbound["outbound to the UE's client port"] = u.spiC
	}

	packets := espPackets(t, pn)

	for name, spi := range inbound {
		if packets[spi] == 0 {
			t.Errorf("the SA %s (SPI %d) carried no packets: %v", name, spi, packets)
		}
	}

	if err := uk.Remove(ueSet); err != nil {
		t.Fatal(err)
	}

	if tr == sip.UDP {
		u.uc.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "c4c4", nil))
		s.icscf.RecvNone(quiet)
	}
}
