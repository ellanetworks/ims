package ibcf

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/trgw"
	"github.com/ellanetworks/ims/mcptt/rtp"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
)

var trgwPhone6 = netip.MustParseAddr("::1")

func ipv6Loopback(t *testing.T) {
	t.Helper()

	c, err := net.ListenUDP("udp6", net.UDPAddrFromAddrPort(netip.AddrPortFrom(trgwPhone6, 0)))
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}

	_ = c.Close()
}

func gatewayScene(t *testing.T, lo, hi uint16, phone []netip.Addr) (*scene, *trgw.Gateway) {
	t.Helper()

	addrs := []trgw.Address{{Bind: trgwTrunk}}
	for _, a := range phone {
		addrs = append(addrs, trgw.Address{Bind: a})
	}

	g, err := trgw.New(trgw.WithAddresses(addrs...), trgw.WithPortRange(lo, hi))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = g.Close() })

	s := newSceneWith(t, false, func(cfg *Config) {
		cfg.Media = &MediaConfig{Gateway: g, Phone: phone, Trunk: []netip.Addr{trgwTrunk}}
	})

	return s, g
}

func newEndpointAt(t *testing.T, a netip.Addr) *endpoint {
	t.Helper()

	c, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(a, 0)))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = c.Close() })

	return &endpoint{t: t, conn: c}
}

func (e *endpoint) sdpLines(version int, lines ...string) string {
	a := e.addr()
	family := "IP4"

	if !a.Addr().Unmap().Is4() {
		family = "IP6"
	}

	head := []string{
		"v=0",
		fmt.Sprintf("o=ue 4711 %d IN %s %s", version, family, a.Addr().Unmap()),
		"s=-",
		fmt.Sprintf("c=IN %s %s", family, a.Addr().Unmap()),
		"t=0 0",
	}

	return strings.Join(append(head, lines...), "\r\n") + "\r\n"
}

func (e *endpoint) audio(version int, extra ...string) string {
	lines := []string{
		fmt.Sprintf("m=audio %d RTP/AVP 116 111", e.addr().Port()),
		"b=AS:41",
		"a=rtpmap:116 AMR-WB/16000/1",
		"a=rtpmap:111 telephone-event/16000",
		"a=ptime:20",
	}

	return e.sdpLines(version, append(lines, extra...)...)
}

func (e *endpoint) receiveRTCP(from netip.AddrPort) {
	e.t.Helper()

	_ = e.conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	buf := make([]byte, 1500)

	n, src, err := e.conn.ReadFromUDPAddrPort(buf)
	if err != nil {
		e.t.Fatalf("no RTCP: %v", err)
	}

	if src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port()); src != from || !rtp.IsRTCP(buf[:n]) {
		e.t.Fatalf("%d octets from %s, want RTCP from %s", n, src, from)
	}
}

func receiverReport(t *testing.T) []byte {
	t.Helper()

	b, err := rtp.AppendRTCP(nil, &rtp.ReceiverReport{SSRC: 0x55667788})
	if err != nil {
		t.Fatal(err)
	}

	return b
}

type established struct {
	toTrunk, toPhone netip.AddrPort
	offer, answer    *sdp.Session
	ims, carrier     event
}

func establish(t *testing.T, s *scene, offer, answer string) established {
	t.Helper()

	s.call(offer)

	in := s.carrier.next("invite").req
	e := established{offer: parse(t, in.Message())}
	_ = in.Accept(sdpOf(answer)...)

	e.ims = s.ims.next("established")
	e.answer = parse(t, e.ims.msg)
	s.ims.next("answer")
	e.carrier = s.carrier.next("established")
	e.toTrunk, e.toPhone = rtpOf(t, e.offer), rtpOf(t, e.answer)

	return e
}

func TestMediaPreconditionsRemoved(t *testing.T) {
	s := mediaScene(t, 0)
	phone, carrier := newEndpoint(t), newEndpoint(t)

	precondition := []string{"a=curr:qos local none", "a=curr:qos remote none", "a=des:qos mandatory local sendrecv", "a=des:qos optional remote sendrecv", "a=conf:qos remote sendrecv"}

	e := establish(t, s, phone.audio(1, precondition...), carrier.audio(1, precondition...))

	for _, got := range []*sdp.Session{e.offer, e.answer} {
		for _, name := range []string{sdp.Current, sdp.Desired, sdp.Confirmed} {
			if got.Media[0].HasAttr(name) {
				t.Errorf("a=%s crossed the IBCF: %s", name, got)
			}
		}
	}
}

func TestMediaRTCPMuxAccepted(t *testing.T) {
	s := mediaScene(t, 0)
	phone, carrier, carrierRTCP := newEndpoint(t), newEndpoint(t), newEndpoint(t)

	carrierAnswer := carrier.audio(1, fmt.Sprintf("a=rtcp:%d", carrierRTCP.addr().Port()))
	e := establish(t, s, phone.audio(1, "a=rtcp-mux", "a=rtcp-mux-only"), carrierAnswer)
	toTrunk, toPhone, offer, answer := e.toTrunk, e.toPhone, e.offer, e.answer

	if offer.Media[0].HasAttr("rtcp-mux") || offer.Media[0].HasAttr("rtcp-mux-only") {
		t.Errorf("trunk offer multiplexes RTCP: %s", offer)
	}

	if !answer.Media[0].HasAttr("rtcp-mux") || answer.Media[0].HasAttr("rtcp-mux-only") || answer.Media[0].HasAttr("rtcp") {
		t.Fatalf("answer to an rtcp-mux-only offer: %s", answer)
	}

	trunkRTCP := netip.AddrPortFrom(toTrunk.Addr(), toTrunk.Port()+1)

	rr := receiverReport(t)
	if _, err := phone.conn.WriteToUDPAddrPort(rr, toPhone); err != nil {
		t.Fatal(err)
	}

	carrierRTCP.receiveRTCP(trunkRTCP)

	if _, err := carrierRTCP.conn.WriteToUDPAddrPort(rr, trunkRTCP); err != nil {
		t.Fatal(err)
	}

	phone.receiveRTCP(toPhone)

	phone.send(toPhone)
	carrier.receive(toTrunk)
}

func TestMediaMultiplePortsCollapsed(t *testing.T) {
	s := mediaScene(t, 0)
	phone, carrier := newEndpoint(t), newEndpoint(t)

	offer := phone.sdpLines(1, fmt.Sprintf("m=audio %d/2 RTP/AVP 0", phone.addr().Port()))

	e := establish(t, s, offer, carrier.audio(1))

	if d, _ := e.offer.Media[0].Desc(); d.NumPorts != 0 {
		t.Fatalf("trunk offer m=%s keeps the number of ports", d)
	}
}

func TestMediaStreamRemovedAndReadded(t *testing.T) {
	s, g := gatewayScene(t, 31200, 31299, []netip.Addr{trgwPhone})
	phone, carrier := newEndpoint(t), newEndpoint(t)

	e := establish(t, s, phone.audio(1), carrier.audio(1)).ims

	if g.Relays() != 1 {
		t.Fatalf("%d relays for the call", g.Relays())
	}

	done := make(chan *sip.Response, 1)
	removed := phone.sdpLines(2, "m=audio 0 RTP/AVP 116", "m=image 5008 udptl t38")
	_ = e.s.Reinvite(context.Background(), ua.Change{Bodies: sdpOf(removed)}, func(res *sip.Response, _ error) { done <- res })

	r := s.carrier.next("request").req
	offer := parse(t, r.Message())

	for i, md := range offer.Media {
		if md.Port() != 0 {
			t.Fatalf("m-line %d offered to the trunk with a port: %s", i, offer)
		}
	}

	_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf(carrier.sdpLines(2, "m=audio 0 RTP/AVP 116", "m=image 0 udptl t38"))})

	s.net.Quiesce()

	res := <-done
	if res == nil || res.StatusCode != 200 {
		t.Fatalf("re-INVITE removing the stream: %v", res)
	}

	for i, md := range parse(t, res).Media {
		if md.Port() != 0 {
			t.Fatalf("m-line %d answered with a port: %s", i, res.Body)
		}
	}

	s.ims.until("answer")

	if g.Relays() != 0 {
		t.Fatalf("%d relays after the stream was removed", g.Relays())
	}

	_ = e.s.Reinvite(context.Background(), ua.Change{Bodies: sdpOf(phone.audio(3))}, func(res *sip.Response, _ error) { done <- res })

	r = s.carrier.next("request").req
	toTrunk := rtpOf(t, parse(t, r.Message()))
	_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf(carrier.audio(3))})

	s.net.Quiesce()

	res = <-done
	if res == nil || res.StatusCode != 200 {
		t.Fatalf("re-INVITE adding the stream back: %v", res)
	}

	toPhone := rtpOf(t, parse(t, res))

	phone.send(toPhone)
	carrier.receive(toTrunk)
}

func TestMediaPortsExhaustedAnswer503(t *testing.T) {
	s, g := gatewayScene(t, 31300, 31301, []netip.Addr{trgwPhone})

	held, err := g.Allocate(context.Background(), trgw.Allocation{TrunkID: "other", PhoneAddress: trgwPhone, TrunkAddress: trgwTrunk})
	if err != nil {
		t.Skipf("ports of the range are taken: %v", err)
	}

	t.Cleanup(func() { _ = held.Close() })

	phone := newEndpoint(t)
	s.call(phone.audio(1))

	if e := s.ims.until("failed"); e.res == nil || e.res.StatusCode != 503 {
		t.Fatalf("call with no TrGW port left: %+v", e.res)
	}

	s.carrier.none()
}

func TestMediaTrunkFamilyRefused(t *testing.T) {
	ipv6Loopback(t)

	s := mediaScene(t, 0)
	carrier := newEndpointAt(t, trgwPhone6)

	s.dial(s.carrier, carrier.audio(1))

	e := s.carrier.until("failed")
	if e.res == nil || e.res.StatusCode != 488 || !strings.HasPrefix(values(e.res, "Warning"), "301 ") {
		t.Fatalf("IPv6 media over IPv4 signalling: %v", e.res)
	}

	s.ims.none()
}

func TestMediaPhoneAlternateFamilies(t *testing.T) {
	ipv6Loopback(t)

	s, g := gatewayScene(t, 31400, 31499, []netip.Addr{trgwPhone, trgwPhone6})
	phone, carrier := newEndpointAt(t, trgwPhone6), newEndpoint(t)

	carrierOffer := carrier.audio(1, "a=altc:1 IP4 192.0.2.77 4000", fmt.Sprintf("a=altc:2 IP4 %s %d", carrier.addr().Addr(), carrier.addr().Port()))
	s.dial(s.carrier, carrierOffer)

	in := s.ims.next("invite").req
	offer := parse(t, in.Message())

	if strings.Contains(offer.String(), "192.0.2.77") || strings.Contains(offer.String(), carrier.addr().String()) {
		t.Fatalf("the carrier's addresses reached the phone: %s", offer)
	}

	alts := offer.Media[0].Attrs("altc")
	v4 := rtpOf(t, offer)

	if v4.Addr() != trgwPhone || len(alts) != 2 ||
		alts[0] != fmt.Sprintf("1 IP4 %s %d", trgwPhone, v4.Port()) || !strings.HasPrefix(alts[1], "2 IP6 ::1 ") {
		t.Fatalf("offer to the phone c=%s altc %q", v4, alts)
	}

	var port int
	if _, err := fmt.Sscanf(alts[1], "2 IP6 ::1 %d", &port); err != nil {
		t.Fatal(err)
	}

	toPhone := netip.AddrPortFrom(trgwPhone6, uint16(port))

	_ = in.Accept(sdpOf(phone.audio(1))...)

	ce := s.carrier.next("established")
	toTrunk := rtpOf(t, parse(t, ce.msg))

	s.ims.next("established")

	phone.send(toPhone)
	carrier.receive(toTrunk)

	carrier.send(toTrunk)
	phone.receive(toPhone)

	if g.Relays() != 1 {
		t.Fatalf("%d relays", g.Relays())
	}

	c, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(v4))
	if err != nil {
		t.Fatalf("the unused IPv4 binding %s was kept: %v", v4, err)
	}

	_ = c.Close()
}

func TestMediaInterworkingBandwidth(t *testing.T) {
	ipv6Loopback(t)

	s, _ := gatewayScene(t, 31500, 31599, []netip.Addr{trgwPhone6})
	phone, carrier := newEndpointAt(t, trgwPhone6), newEndpoint(t)

	offer := phone.audio(1, "a=bw-info:116 sendrecv MaxSupBw=41;MinDesBw=30")
	e := establish(t, s, offer, carrier.audio(1))
	toTrunk, toPhone, trunkOffer, phoneAnswer := e.toTrunk, e.toPhone, e.offer, e.answer

	if as, _ := trunkOffer.Media[0].Bandwidth(sdp.BandwidthAS); as != 33 {
		t.Errorf("b=AS:%d towards the IPv4 trunk, want 33", as)
	}

	if got, _ := trunkOffer.Media[0].Attr("bw-info"); got != "116 sendrecv MaxSupBw=33;MinDesBw=22;IpVer=4;MaxPRate=50" {
		t.Errorf("a=bw-info:%s towards the IPv4 trunk", got)
	}

	if as, _ := phoneAnswer.Media[0].Bandwidth(sdp.BandwidthAS); as != 49 {
		t.Errorf("b=AS:%d towards the IPv6 phone, want 49", as)
	}

	if toPhone.Addr() != trgwPhone6 || toTrunk.Addr() != trgwTrunk {
		t.Fatalf("media %s and %s", toPhone, toTrunk)
	}

	phone.send(toPhone)
	carrier.receive(toTrunk)
}

func TestRecalculateBandwidthInfo(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []string
		to4  bool
		want []string
	}{
		{
			"implied IPv6 to IPv4",
			[]string{"a=bw-info:97 send MaxSupBw=40; MinSupBw=20"},
			true,
			[]string{"a=bw-info:97 send MaxSupBw=32;MinSupBw=12;IpVer=4;MaxPRate=50;MinPRate=50"},
		},
		{
			"explicit IPv4 to IPv6 with packet rates",
			[]string{"a=bw-info:97 recv MaxDesBw=30;IpVer=4;MaxPRate=25"},
			false,
			[]string{"a=bw-info:97 recv MaxDesBw=34;IpVer=6;MaxPRate=25"},
		},
		{
			"both versions untouched",
			[]string{"a=bw-info:97 send MaxSupBw=40;IpVer=6", "a=bw-info:97 send MaxSupBw=32;IpVer=4"},
			true,
			[]string{"a=bw-info:97 send MaxSupBw=40;IpVer=6", "a=bw-info:97 send MaxSupBw=32;IpVer=4"},
		},
		{
			"already in the target version",
			[]string{"a=bw-info:* sendrecv MaxSupBw=40;IpVer=4"},
			true,
			[]string{"a=bw-info:* sendrecv MaxSupBw=40;IpVer=4"},
		},
		{
			"overlapping payload-type lists",
			[]string{"a=bw-info:96,97 send MaxSupBw=40;IpVer=6", "a=bw-info:96 send MaxSupBw=32;IpVer=4"},
			true,
			[]string{"a=bw-info:96 send MaxSupBw=40;IpVer=6", "a=bw-info:97 send MaxSupBw=32;IpVer=4;MaxPRate=50", "a=bw-info:96 send MaxSupBw=32;IpVer=4"},
		},
		{
			"same payload-type set in another order",
			[]string{"a=bw-info:96,97 send MaxSupBw=40;IpVer=6", "a=bw-info:97,96 send MaxSupBw=32;IpVer=4"},
			true,
			[]string{"a=bw-info:96,97 send MaxSupBw=40;IpVer=6", "a=bw-info:97,96 send MaxSupBw=32;IpVer=4"},
		},
		{
			"sendrecv overlapping send",
			[]string{"a=bw-info:97 sendrecv MaxSupBw=40", "a=bw-info:97 send MaxSupBw=32;IpVer=4"},
			true,
			[]string{"a=bw-info:97 send MaxSupBw=40", "a=bw-info:97 recv MaxSupBw=32;IpVer=4;MaxPRate=50", "a=bw-info:97 send MaxSupBw=32;IpVer=4"},
		},
		{
			"wildcard overlapping a payload type",
			[]string{"a=bw-info:* send MaxSupBw=40", "a=bw-info:97 send MinSupBw=20;IpVer=4"},
			true,
			[]string{"a=bw-info:97 send MaxSupBw=40", "a=bw-info:101 send MaxSupBw=32;IpVer=4;MaxPRate=50", "a=bw-info:97 send MinSupBw=20;IpVer=4"},
		},
		{
			"repeated payload type",
			[]string{"a=bw-info:97,97,101 send MaxSupBw=40;IpVer=6", "a=bw-info:101 send MaxSupBw=32;IpVer=4"},
			true,
			[]string{"a=bw-info:97 send MaxSupBw=32;IpVer=4;MaxPRate=50", "a=bw-info:101 send MaxSupBw=40;IpVer=6", "a=bw-info:101 send MaxSupBw=32;IpVer=4"},
		},
		{
			"repeated payload type converted whole",
			[]string{"a=bw-info:97,97 send MaxSupBw=40"},
			true,
			[]string{"a=bw-info:97 send MaxSupBw=32;IpVer=4;MaxPRate=50"},
		},
		{
			"wildcard converted whole",
			[]string{"a=bw-info:* sendrecv MaxSupBw=40"},
			true,
			[]string{"a=bw-info:* sendrecv MaxSupBw=32;IpVer=4;MaxPRate=50"},
		},
		{
			"malformed payload type ignored",
			[]string{"a=bw-info:9a send MaxSupBw=40"},
			true,
			[]string{"a=bw-info:9a send MaxSupBw=40"},
		},
		{
			"unknown direction ignored",
			[]string{"a=bw-info:97 sideways MaxSupBw=40"},
			true,
			[]string{"a=bw-info:97 sideways MaxSupBw=40"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := strings.Join(append([]string{"v=0", "o=- 1 1 IN IP6 ::1", "s=-", "c=IN IP6 ::1", "t=0 0", "m=audio 4000 RTP/AVP 97 101", "a=rtpmap:97 AMR/8000", "a=rtpmap:101 telephone-event/8000"}, c.in...), "\r\n") + "\r\n"

			s, err := sdp.Parse([]byte(body))
			if err != nil {
				t.Fatal(err)
			}

			maxRate, minRate := packetRates(s.Media[0])
			recalculate(s.Media[0], c.to4, maxRate, minRate)

			got := s.Media[0].Attrs("bw-info")
			for i := range got {
				got[i] = "a=bw-info:" + got[i]
			}

			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestRecalculateBandwidthInfoRepeatedFormat(t *testing.T) {
	body := "v=0\r\no=- 1 1 IN IP6 ::1\r\ns=-\r\nc=IN IP6 ::1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 97 97 101\r\n" +
		"a=rtpmap:97 AMR/8000\r\na=rtpmap:101 telephone-event/8000\r\n" +
		"a=bw-info:* send MaxSupBw=40;IpVer=6\r\na=bw-info:101 send MaxSupBw=32;IpVer=4\r\n"

	s, err := sdp.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	maxRate, minRate := packetRates(s.Media[0])
	recalculate(s.Media[0], true, maxRate, minRate)

	want := []string{"97 send MaxSupBw=32;IpVer=4;MaxPRate=50", "101 send MaxSupBw=40;IpVer=6", "101 send MaxSupBw=32;IpVer=4"}
	if got := s.Media[0].Attrs("bw-info"); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPolicedRate(t *testing.T) {
	body := "v=0\r\no=- 1 1 IN IP4 192.0.2.1\r\ns=-\r\nc=IN IP4 192.0.2.1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 0 101\r\nb=AS:200\r\na=rtpmap:101 telephone-event/8000\r\n"

	s, err := sdp.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	headers := float64((12 + 8 + 40) * 8 * policedPacketRate)
	want := func(payload float64) uint64 { return uint64((payload + headers) * policedHeadroom) }

	if got := policed(s.Media[0]); got != want(200000) {
		t.Errorf("rate %d with b=AS:200, want %d", got, want(200000))
	}

	s.Media[0].SetBandwidth(sdp.BandwidthAS, 32)

	if got := policed(s.Media[0]); got != want(64000) {
		t.Errorf("rate %d for PCMU under its b=AS, want %d", got, want(64000))
	}

	s.Media[0].SetDesc(sdp.MediaDesc{Type: "audio", Port: 4000, Proto: "RTP/AVP", Formats: []string{"97", "101"}})
	s.Media[0].AddAttr("rtpmap", "97 G726-32/8000")

	if got := policed(s.Media[0]); got != want(32000) {
		t.Errorf("rate %d for an unknown codec with b=AS:32, want %d", got, want(32000))
	}

	if sent := float64(4000+(12+8+40)*50) * 8; float64(policed(s.Media[0])) < sent {
		t.Errorf("rate %d below the %v bit/s G726-32 sends over IPv6 at 20 ms", policed(s.Media[0]), sent)
	}

	s.Media[0].AddAttr("ptime", "5")

	if got, wantFast := policed(s.Media[0]), uint64((32000+float64((12+8+40)*8*200))*policedHeadroom); got != wantFast {
		t.Errorf("rate %d with a=ptime:5, want %d", got, wantFast)
	}

	s.Media[0].Del('b')

	if got := policed(s.Media[0]); got != 0 {
		t.Errorf("rate %d with neither b=AS nor a known codec, want no policing", got)
	}

	s.Media[0].SetBandwidth(sdp.BandwidthAS, math.MaxUint64)

	if got := policed(s.Media[0]); got != math.MaxUint64 {
		t.Errorf("rate %d with b=AS:%d, want %d", got, uint64(math.MaxUint64), uint64(math.MaxUint64))
	}
}

func TestTinyPtime(t *testing.T) {
	body := "v=0\r\no=- 1 1 IN IP6 ::1\r\ns=-\r\nc=IN IP6 ::1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 97\r\nb=AS:30\r\na=rtpmap:97 G726-32/8000\r\na=ptime:0.0000001\r\na=maxptime:0.0000002\r\n"

	s, err := sdp.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	if maxRate, minRate := packetRates(s.Media[0]); maxRate != 1000 || minRate != 1000 {
		t.Errorf("packet rates %v and %v, want 1000", maxRate, minRate)
	}

	if got, want := policed(s.Media[0]), uint64((30000+float64((12+8+40)*8*1000))*policedHeadroom); got != want {
		t.Errorf("rate %d, want %d", got, want)
	}

	interwork(s, s.Media[0], false, true)

	if as, _ := s.Media[0].Bandwidth(sdp.BandwidthAS); as != 1 {
		t.Errorf("b=AS:%d towards IPv4, want 1", as)
	}
}

func TestConflictsWithRTCP(t *testing.T) {
	for _, c := range []struct {
		formats []string
		want    bool
	}{
		{[]string{"0", "8", "96", "101"}, false},
		{[]string{"63", "96"}, false},
		{[]string{"96", "72"}, true},
		{[]string{"64"}, true},
		{[]string{"95"}, true},
	} {
		md := &sdp.Media{}
		md.SetDesc(sdp.MediaDesc{Type: "audio", Port: 4000, Proto: "RTP/AVP", Formats: c.formats})

		if got := conflictsWithRTCP(md); got != c.want {
			t.Errorf("formats %v conflict %v, want %v", c.formats, got, c.want)
		}
	}
}

func TestMediaTrunkAnswersOtherFamily(t *testing.T) {
	ipv6Loopback(t)

	s := mediaScene(t, 0)
	phone, carrier := newEndpoint(t), newEndpointAt(t, trgwPhone6)

	s.call(phone.audio(1))

	in := s.carrier.next("invite").req
	_ = in.Accept(sdpOf(carrier.audio(1))...)

	e := s.ims.until("failed")
	if e.res == nil || e.res.StatusCode != 502 || values(e.res, "Warning") != "" {
		t.Fatalf("trunk answer in IPv6 to an IPv4 offer: %v", e.res)
	}
}

func TestMediaRejectedReofferForgetsAddress(t *testing.T) {
	s := mediaScene(t, 0)
	phone, carrier, moved := newEndpoint(t), newEndpoint(t), newEndpoint(t)

	e := establish(t, s, phone.audio(1), carrier.audio(1))

	done := make(chan *sip.Response, 1)
	_ = e.carrier.s.Reinvite(context.Background(), ua.Change{Bodies: sdpOf(moved.audio(2, "a=sendonly"))}, func(res *sip.Response, _ error) { done <- res })

	r := s.ims.next("request").req
	_ = r.Respond(ua.Reply{Code: 488})

	s.net.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 488 {
		t.Fatalf("rejected re-INVITE: %v", res)
	}

	moved.send(e.toTrunk)

	if phone.got() {
		t.Fatal("media from the address of the rejected re-offer reached the phone")
	}

	carrier.send(e.toTrunk)
	phone.receive(e.toPhone)
}

func TestMediaFailedReofferReleasesFamily(t *testing.T) {
	ipv6Loopback(t)

	const lo, hi = 31700, 31709

	s, _ := gatewayScene(t, lo, hi, []netip.Addr{trgwPhone, trgwPhone6})
	phone, moved, carrier := newEndpoint(t), newEndpointAt(t, trgwPhone6), newEndpoint(t)

	e := establish(t, s, phone.audio(1), carrier.audio(1))

	bad := strings.Replace(moved.audio(2), fmt.Sprintf("m=audio %d ", moved.addr().Port()), "m=audio 65535 ", 1)

	done := make(chan *sip.Response, 1)
	_ = e.ims.s.Reinvite(context.Background(), ua.Change{Bodies: sdpOf(bad)}, func(res *sip.Response, _ error) { done <- res })

	s.net.Quiesce()

	if res := <-done; res == nil || res.StatusCode < 400 {
		t.Fatalf("re-INVITE with an unusable IPv6 remote: %v", res)
	}

	s.carrier.none()

	for port := lo; port <= hi; port++ {
		c, err := net.ListenUDP("udp6", net.UDPAddrFromAddrPort(netip.AddrPortFrom(trgwPhone6, uint16(port))))
		if err != nil {
			t.Fatalf("IPv6 port %d still bound after the failed re-offer: %v", port, err)
		}

		_ = c.Close()
	}

	phone.send(e.toPhone)
	carrier.receive(e.toTrunk)
}

func TestMediaLegacyHoldKeepsMusic(t *testing.T) {
	s := mediaScene(t, 0)
	phone, carrier := newEndpoint(t), newEndpoint(t)

	e := establish(t, s, phone.audio(1), carrier.audio(1))

	held := strings.Replace(carrier.audio(2), "c=IN IP4 127.0.0.1", "c=IN IP4 0.0.0.0", 1)

	done := make(chan *sip.Response, 1)
	_ = e.carrier.s.Reinvite(context.Background(), ua.Change{Bodies: sdpOf(held)}, func(res *sip.Response, _ error) { done <- res })

	r := s.ims.next("request").req
	if got := parse(t, r.Message()); got.MediaDirection(0) != sdp.SendOnly {
		t.Fatalf("legacy hold offered to the phone as %s", got.MediaDirection(0))
	}

	_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf(phone.audio(2, "a=recvonly"))})

	s.net.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 200 {
		t.Fatalf("legacy hold re-INVITE: %v", res)
	}

	carrier.send(e.toTrunk)
	phone.receive(e.toPhone)
}

func TestMediaPhoneChangesFamily(t *testing.T) {
	ipv6Loopback(t)

	s, _ := gatewayScene(t, 31600, 31699, []netip.Addr{trgwPhone, trgwPhone6})
	phone, moved, carrier := newEndpoint(t), newEndpointAt(t, trgwPhone6), newEndpoint(t)

	e := establish(t, s, phone.audio(1), carrier.audio(1))

	if e.toPhone.Addr() != trgwPhone || e.answer.Media[0].HasAttr("altc") {
		t.Fatalf("answer to the IPv4 phone: %s", e.answer)
	}

	phone.send(e.toPhone)
	carrier.receive(e.toTrunk)

	done := make(chan *sip.Response, 1)
	_ = e.ims.s.Reinvite(context.Background(), ua.Change{Bodies: sdpOf(moved.audio(2))}, func(res *sip.Response, _ error) { done <- res })

	r := s.carrier.next("request").req
	if got := rtpOf(t, parse(t, r.Message())); got != e.toTrunk {
		t.Fatalf("trunk re-offer media %s, want %s unchanged", got, e.toTrunk)
	}

	_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf(carrier.audio(2))})

	s.net.Quiesce()

	res := <-done
	if res == nil || res.StatusCode != 200 {
		t.Fatalf("re-INVITE moving the phone to IPv6: %v", res)
	}

	toPhone := rtpOf(t, parse(t, res))
	if toPhone.Addr() != trgwPhone6 {
		t.Fatalf("answer to the phone on IPv6 has media %s", toPhone)
	}

	s.ims.until("answer")

	phone.send(e.toPhone)
	carrier.receive(e.toTrunk)

	moved.send(toPhone)
	carrier.receive(e.toTrunk)

	carrier.send(e.toTrunk)
	moved.receive(toPhone)

	deadline := time.Now().Add(2 * time.Second)

	for {
		c, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(e.toPhone))
		if err == nil {
			_ = c.Close()
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("the IPv4 binding %s outlived media on IPv6", e.toPhone)
		}

		time.Sleep(10 * time.Millisecond)
	}
}
