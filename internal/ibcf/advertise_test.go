package ibcf

import (
	"context"
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

var publicAddr = netip.MustParseAddr("198.18.0.1")

func advertised(cfg *Config) {
	cfg.Advertised = map[netip.Addr]netip.Addr{externalAddr.Addr(): publicAddr}
}

func natScene(t *testing.T) *scene {
	s := newSceneWith(t, false, advertised)
	s.carrier.nat = map[netip.Addr]netip.Addr{publicAddr: externalAddr.Addr()}

	return s
}

func TestExitAdvertisedAddress(t *testing.T) {
	s := natScene(t)

	s.call("offer-1")

	in := s.carrier.next("invite").req
	m := in.Message()

	via, _ := m.Header.TopVia()
	want := netip.AddrPortFrom(publicAddr, externalAddr.Port()).String()

	switch {
	case via.SentBy() != want:
		t.Errorf("Via sent-by %s, want %s", via.SentBy(), want)
	case values(m, "Contact") != "<sip:"+want+">":
		t.Errorf("Contact %s, want <sip:%s>", values(m, "Contact"), want)
	case m.Flow.Remote != externalAddr:
		t.Errorf("sent from %s, want the bound %s", m.Flow.Remote, externalAddr)
	}

	_ = in.Progress(ua.Reply{Code: 180})

	s.ims.next("progress")

	_ = in.Accept(sdpOf("answer-1")...)

	e := s.ims.next("established")
	s.ims.next("answer")
	ce := s.carrier.next("established")

	done := make(chan *sip.Response, 1)
	_ = ce.s.Bye(context.Background(), ua.Change{}, func(res *sip.Response, _ error) { done <- res })

	s.ims.next("bye")
	s.net.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 200 {
		t.Fatalf("carrier BYE: %v", res)
	}

	if e.s == nil {
		t.Fatal("no caller session")
	}
}

func TestEntryAdvertisedAddress(t *testing.T) {
	s := natScene(t)

	s.dial(s.carrier, "offer-1")

	in := s.ims.next("invite").req
	if via, _ := in.Message().Header.TopVia(); via.Host != internalAddr.Addr().String() {
		t.Errorf("Via towards the I-CSCF %s, want the internal address", via.SentBy())
	}

	_ = in.Accept(sdpOf("answer-1")...)

	e := s.carrier.next("established")
	if want := "<sip:" + netip.AddrPortFrom(publicAddr, externalAddr.Port()).String() + ">"; values(e.msg, "Contact") != want {
		t.Errorf("trunk 200 Contact %s, want %s", values(e.msg, "Contact"), want)
	}

	s.carrier.next("answer")
	ims := s.ims.next("established")

	_ = ims.s.Bye(context.Background(), ua.Change{}, nil)

	bye := s.carrier.next("bye")
	if via, _ := bye.msg.Env().Header.TopVia(); via.Host != publicAddr.String() {
		t.Errorf("BYE Via %s, want the advertised address", via.SentBy())
	}
}
