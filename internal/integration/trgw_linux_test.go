//go:build linux && (amd64 || arm64)

package integration

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/testue"
)

type rtpPeer struct {
	t    *testing.T
	conn *net.UDPConn
	seq  uint16
}

func listenRTP(t *testing.T, at netip.AddrPort, do func(func())) *rtpPeer {
	t.Helper()

	var (
		c   *net.UDPConn
		err error
	)

	do(func() { c, err = net.ListenUDP("udp", net.UDPAddrFromAddrPort(at)) })

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = c.Close() })

	return &rtpPeer{t: t, conn: c}
}

func (p *rtpPeer) addr() netip.AddrPort {
	a := p.conn.LocalAddr().(*net.UDPAddr).AddrPort()
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
}

func (p *rtpPeer) send(to netip.AddrPort) {
	p.t.Helper()

	p.seq++

	b := make([]byte, 12+32)
	b[0], b[1] = 0x80, 116
	binary.BigEndian.PutUint16(b[2:], p.seq)
	binary.BigEndian.PutUint32(b[4:], uint32(p.seq)*320)
	binary.BigEndian.PutUint32(b[8:], 0xcafe)

	if _, err := p.conn.WriteToUDPAddrPort(b, to); err != nil {
		p.t.Fatal(err)
	}
}

func (p *rtpPeer) receive(from netip.AddrPort) {
	p.t.Helper()

	_ = p.conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	buf := make([]byte, 1500)

	n, src, err := p.conn.ReadFromUDPAddrPort(buf)
	if err != nil {
		p.t.Fatalf("no RTP from %s: %v", from, err)
	}

	if src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port()); src != from || n != 44 {
		p.t.Fatalf("RTP of %d bytes from %s, want 44 from %s", n, src, from)
	}
}

func (p *rtpPeer) exchange(other *rtpPeer, mine, theirs netip.AddrPort) {
	p.t.Helper()

	for range 3 {
		p.send(mine)
		other.receive(theirs)

		other.send(theirs)
		p.receive(mine)
	}
}

func carrierSDP(media netip.AddrPort, version int, dir string) []byte {
	return []byte(strings.Join([]string{
		"v=0",
		fmt.Sprintf("o=- 1 %d IN IP4 %s", version, media.Addr()),
		"s=-",
		"c=IN IP4 " + media.Addr().String(),
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP 116 100", media.Port()),
		"a=rtpmap:116 AMR-WB/16000/1",
		"a=fmtp:116 mode-change-capability=2;max-red=0",
		"a=rtpmap:100 telephone-event/16000",
		"a=ptime:20",
		"a=" + dir,
	}, "\r\n") + "\r\n")
}

func mediaOf(t *testing.T, body []byte) netip.AddrPort {
	t.Helper()

	s, err := sdp.Parse(body)
	if err != nil {
		t.Fatal(err)
	}

	e, err := s.RTPEndpoint(0)
	if err != nil {
		t.Fatal(err)
	}

	return netip.AddrPortFrom(e.Addr, e.Port)
}

func sdpBody(t *testing.T, m sip.Message) []byte {
	t.Helper()

	part, ok, _ := m.Env().Content().FindFunc(sip.BodyPart.IsSession)
	if !ok {
		t.Fatalf("no SDP in %s", m)
	}

	return part.Body
}

func (s *scene) phoneRTP(c *testue.Call, i int) (*rtpPeer, netip.AddrPort) {
	s.t.Helper()

	var st testue.Stream

	deadline := time.Now().Add(10 * time.Second)

	for {
		var ok bool
		if st, ok = c.Stream("audio"); ok && st.Remote.Addr.IsValid() {
			break
		}

		if time.Now().After(deadline) {
			s.t.Fatalf("no audio stream with a remote: %+v", c.Streams())
		}

		time.Sleep(50 * time.Millisecond)
	}

	return listenRTP(s.t, netip.AddrPortFrom(st.Local.Addr, st.Local.Port), s.host(i).ns.Do), netip.AddrPortFrom(st.Remote.Addr, st.Remote.Port)
}

func TestTrunkMediaOutgoing(t *testing.T) {
	s, c := newTrunkScene(t, false)
	a := s.caller(0, false, testue.Config{})
	ctx := s.ctx()

	carrierMedia := listenRTP(t, netip.MustParseAddrPort("127.0.0.2:0"), func(f func()) { f() })

	ac := invite(t, a, "tel:"+offNet, testue.CallOptions{})

	in := c.next("invite").req
	toTrunk := mediaOf(t, sdpBody(t, in.Message()))

	if toTrunk.Addr() != ibcfExternal.Addr() {
		t.Fatalf("carrier offered media on %s, want the TrGW on %s", toTrunk, ibcfExternal.Addr())
	}

	_ = in.Progress(ua.Reply{Code: 180})

	if err := in.Accept(ua.SDP(carrierSDP(carrierMedia.addr(), 1, "sendrecv"))); err != nil {
		t.Fatal(err)
	}

	if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v", res, err)
	}

	c.next("established")

	phone, toPhone := s.phoneRTP(ac, 0)
	if toPhone.Addr() != imsAddrs[0].Addr() {
		t.Fatalf("phone sends media to %s, want the TrGW on %s", toPhone, imsAddrs[0].Addr())
	}

	phone.exchange(carrierMedia, toPhone, toTrunk)

	t.Run("hold and resume", func(t *testing.T) {
		for i, dir := range []string{"recvonly", "sendrecv"} {
			done := make(chan error, 1)

			go func() {
				if i == 0 {
					done <- ac.Hold(ctx)
				} else {
					done <- ac.Resume(ctx)
				}
			}()

			r := c.next("request").req
			if offer, ok := r.Offer(); !ok || mediaOf(t, offer) != toTrunk {
				t.Fatalf("re-INVITE to the carrier: %s", r.Message())
			}

			if err := r.Respond(ua.Reply{Code: 200, Bodies: []sip.BodyPart{ua.SDP(carrierSDP(carrierMedia.addr(), 2+i, dir))}}); err != nil {
				t.Fatal(err)
			}

			if err := <-done; err != nil {
				t.Fatalf("%s: %v", dir, err)
			}
		}

		phone.exchange(carrierMedia, toPhone, toTrunk)
	})

	if err := ac.Bye(ctx); err != nil {
		t.Fatal(err)
	}

	c.next("bye")

	if p, ok := s.pol.(*rxPolicy); ok {
		for _, m := range p.calls(1) {
			for _, r := range m.requests {
				for _, f := range r.flows {
					if !strings.Contains(f, imsAddrs[0].Addr().String()) {
						t.Errorf("flow %q authorised for the phone, want it towards the TrGW %s", f, imsAddrs[0].Addr())
					}
				}
			}
		}
	}
}

func TestTrunkMediaIncomingWithoutOffer(t *testing.T) {
	s, c := newTrunkScene(t, false)
	b := s.caller(1, false, testue.Config{})
	ctx := s.ctx()

	carrierMedia := listenRTP(t, netip.MustParseAddrPort("127.0.0.2:0"), func(f func()) { f() })

	uri := sip.URI{Scheme: "sip", User: "+15550002", Host: ibcfExternal.Addr().String(), Port: ibcfExternal.Port()}
	uri.Params.Set("user", "phone")

	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<sip:"+offNet+"@"+carrierDomain+";user=phone>")
	req.Header.Add("To", "<sip:+15550002@"+carrierDomain+";user=phone>")

	if _, err := c.a.Invite(context.Background(), req, c.handler()); err != nil {
		t.Fatal(err)
	}

	bc := <-b.MMTel().Calls()

	go func() { _ = bc.Answer(ctx) }()

	e := c.next("offer")
	toTrunk := mediaOf(t, e.offer.SDP())

	if err := e.offer.Answer(ctx, ua.Change{Bodies: []sip.BodyPart{ua.SDP(carrierSDP(carrierMedia.addr(), 1, "sendrecv"))}}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for bc.State() != testue.CallConfirmed {
		if time.Now().After(deadline) {
			t.Fatalf("phone call %s, want confirmed", bc.State())
		}

		time.Sleep(50 * time.Millisecond)
	}

	phone, toPhone := s.phoneRTP(bc, 1)
	phone.exchange(carrierMedia, toPhone, toTrunk)
}

func TestTrunkMediaTimeout(t *testing.T) {
	s := newMediaScene(t, 2*time.Second)
	c := newCarrier(t, carrierSIP)
	id := s.addTrunk("Carrier", c, false, 0)
	s.post("/api/v1/sip-routes", map[string]any{"prefix": "+33", "trunks": []string{id}}, nil)

	a := s.caller(0, false, testue.Config{})
	ctx := s.ctx()

	ac := invite(t, a, "tel:"+offNet, testue.CallOptions{})
	answerWithOffer(t, c.next("invite").req)

	if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v", res, err)
	}

	bye := c.next("bye")
	if !strings.Contains(values(bye.msg, "Reason"), "408") {
		t.Errorf("BYE Reason %q", values(bye.msg, "Reason"))
	}

	ended(t, ac, testue.RemoteBye)
}
