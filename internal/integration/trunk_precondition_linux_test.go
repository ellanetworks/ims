//go:build linux && (amd64 || arm64)

package integration

import (
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/testue"
)

func TestTrunkPreconditionsRemoved(t *testing.T) {
	s, c := newTrunkScene(t, false)
	a := s.caller(0, false, testue.Config{})
	ctx := s.ctx()

	carrierMedia := listenRTP(t, netip.MustParseAddrPort("127.0.0.2:0"), func(f func()) { f() })

	ac := invite(t, a, "tel:"+offNet, testue.CallOptions{Preconditions: true})

	in := c.next("invite").req
	m := in.Message()

	if m.Header.HasOptionTag("Supported", "precondition") || m.Header.HasOptionTag("Require", "precondition") {
		t.Fatalf("precondition option tag sent to the carrier: %s", m)
	}

	offer, err := sdp.Parse(sdpBody(t, m))
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{sdp.Current, sdp.Desired, sdp.Confirmed} {
		if offer.Media[0].HasAttr(name) {
			t.Fatalf("a=%s sent to the carrier: %s", name, offer)
		}
	}

	toTrunk := mediaOf(t, sdpBody(t, m))

	if err := in.Accept(ua.SDP(carrierSDP(carrierMedia.addr(), 1, "sendrecv"))); err != nil {
		t.Fatal(err)
	}

	if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v", res, err)
	}

	c.next("established")

	phone, toPhone := s.phoneRTP(ac, 0)
	phone.exchange(carrierMedia, toPhone, toTrunk)

	if err := ac.Bye(ctx); err != nil {
		t.Fatal(err)
	}

	c.next("bye")
}
