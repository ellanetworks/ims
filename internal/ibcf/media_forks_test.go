package ibcf

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/trgw"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/prometheus/client_golang/prometheus"
)

func (e *endpoint) got() bool {
	_ = e.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))

	buf := make([]byte, 1500)
	_, _, err := e.conn.ReadFromUDPAddrPort(buf)

	return err == nil
}

func versionOf(t *testing.T, s *sdp.Session) (string, uint64) {
	t.Helper()

	o, err := s.Origin()
	if err != nil {
		t.Fatal(err)
	}

	v, err := strconv.ParseUint(o.SessionVersion, 10, 64)
	if err != nil {
		t.Fatal(err)
	}

	return o.SessionID, v
}

// RFC 3264 section 8
func TestMediaEntryForkedPhones(t *testing.T) {
	s := mediaScene(t, 0)
	phoneA, phoneB, carrier := newEndpoint(t), newEndpoint(t), newEndpoint(t)

	s.dial(s.carrier, carrier.sdp(1, "sendrecv"), f("Supported", "100rel"))

	in := s.ims.next("invite").req
	toPhoneSide := rtpOf(t, parse(t, in.Message()))

	_ = in.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf(phoneA.sdp(1, "sendrecv"))})

	s.net.Quiesce()

	fk, err := in.Fork(s.ims.handler())
	if err != nil {
		t.Fatal(err)
	}

	_ = fk.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf(phoneB.sdp(1, "sendonly"))})

	s.net.Quiesce()

	_ = in.Accept()

	s.net.Quiesce()

	early := map[*ua.Session]*sdp.Session{}

	var confirmed *ua.Session

	for confirmed == nil {
		switch e := s.carrier.nextOf("progress", "established"); e.kind {
		case "progress":
			if e.res.StatusCode == 183 {
				early[e.s] = parse(t, e.res)
			}
		case "established":
			confirmed = e.s
		}
	}

	answer := early[confirmed]
	if len(early) != 2 || answer == nil || answer.MediaDirection(0) != sdp.SendRecv {
		t.Fatalf("carrier's early dialogs %v, confirmed one answered %v", early, answer)
	}

	toTrunkSide := rtpOf(t, answer)

	phoneA.send(toPhoneSide)
	carrier.receive(toTrunkSide)

	carrier.send(toTrunkSide)
	phoneA.receive(toPhoneSide)

	if phoneB.got() {
		t.Error("carrier RTP goes to phone B, which did not answer")
	}

	id, version := versionOf(t, answer)

	s.ims.until("established")

	done := make(chan *sip.Response, 1)
	_ = in.Session().Reinvite(context.Background(), ua.Change{Bodies: sdpOf(phoneA.sdp(2, "sendonly"))}, func(res *sip.Response, _ error) { done <- res })

	r := s.carrier.until("request").req
	held := parse(t, r.Message())
	_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf(carrier.sdp(2, "recvonly"))})

	s.net.Quiesce()
	<-done

	if gotID, got := versionOf(t, held); gotID != id || got != version+1 {
		t.Errorf("re-INVITE to the carrier has o= %s %d, want %s %d", gotID, got, id, version+1)
	}
}

func TestMediaExitForkedCarrier(t *testing.T) {
	s := mediaScene(t, 0)
	phone, carrierA, carrierB := newEndpoint(t), newEndpoint(t), newEndpoint(t)

	s.call(phone.sdp(1, "sendrecv"), f("Supported", "100rel"))

	in := s.carrier.next("invite").req
	toTrunkSide := rtpOf(t, parse(t, in.Message()))

	_ = in.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf(carrierA.sdp(1, "sendrecv"))})

	s.net.Quiesce()

	fk, err := in.Fork(s.carrier.handler())
	if err != nil {
		t.Fatal(err)
	}

	_ = fk.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf(carrierB.sdp(1, "sendrecv"))})

	s.net.Quiesce()

	_ = in.Accept()

	toPhoneSide := rtpOf(t, parse(t, s.ims.until("progress").res))
	s.ims.until("established")

	phone.send(toPhoneSide)
	carrierA.receive(toTrunkSide)

	if carrierB.got() {
		t.Error("phone RTP goes to the carrier fork that did not answer")
	}

	carrierA.send(toTrunkSide)
	phone.receive(toPhoneSide)
}

func TestMediaEntryForkedOfferless(t *testing.T) {
	s := mediaScene(t, 0)
	phoneA, phoneB, carrier := newEndpoint(t), newEndpoint(t), newEndpoint(t)

	s.dial(s.carrier, "", f("Supported", "100rel"))

	in := s.ims.next("invite").req

	_ = in.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf(phoneA.sdp(1, "sendrecv"))})

	offers := map[*ua.Session]event{}

	for len(offers) < 1 {
		if e := s.carrier.until("offer"); e.offer != nil {
			offers[e.s] = e
		}
	}

	fk, err := in.Fork(s.ims.handler())
	if err != nil {
		t.Fatal(err)
	}

	_ = fk.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf(phoneB.sdp(1, "sendrecv"))})

	for len(offers) < 2 {
		if e := s.carrier.until("offer"); e.offer != nil {
			offers[e.s] = e
		}
	}

	for _, e := range offers {
		_ = e.offer.Answer(context.Background(), ua.Change{Bodies: sdpOf(carrier.sdp(1, "sendrecv"))})

		s.net.Quiesce()
	}

	_ = in.Accept()

	confirmed := s.carrier.until("established").s

	offer, err := sdp.Parse(offers[confirmed].sdp)
	if err != nil {
		t.Fatal(err)
	}

	toTrunkSide := rtpOf(t, offer)

	carrier.send(toTrunkSide)

	if !phoneA.got() {
		t.Error("carrier RTP does not reach phone A, which answered")
	}

	if phoneB.got() {
		t.Error("carrier RTP goes to phone B, which did not answer")
	}
}

// RFC 3263 section 4.3
func TestMediaFailoverAcrossFamilies(t *testing.T) {
	v6 := netip.MustParseAddr("::1")

	g, err := trgw.New(trgw.WithAddresses(trgw.Address{Bind: trgwPhone}, trgw.Address{Bind: trgwTrunk}, trgw.Address{Bind: v6}),
		trgw.WithPortRange(32000, 32999))
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}

	t.Cleanup(func() { _ = g.Close() })

	ext6 := netip.MustParseAddrPort("[2001:db8::99]:5060")
	car6 := netip.MustParseAddrPort("[2001:db8::1]:5060")

	s := newSceneWith(t, false, func(cfg *Config) {
		cfg.External = append(cfg.External, ext6)
		cfg.Media = &MediaConfig{Gateway: g, Phone: []netip.Addr{trgwPhone}, Trunk: []netip.Addr{trgwTrunk, v6}}
	})
	s.ibcf.cfg.Resolve = func(context.Context, sip.URI, []sip.Transport) ([]sip.Server, error) {
		return []sip.Server{{Transport: sip.UDP, Addr: car6}, {Transport: sip.UDP, Addr: carrierAddr}}, nil
	}
	first := newParty(t, s.net, "carrier6", car6)

	phone := newEndpoint(t)
	s.call(phone.sdp(1, "sendrecv"))

	r := first.next("invite").req
	if a := rtpOf(t, parse(t, r.Message())).Addr(); a != v6 {
		t.Errorf("offer to the IPv6 server carries media address %s", a)
	}

	_ = r.Reject(503)

	got := parse(t, s.carrier.next("invite").req.Message())

	if a := rtpOf(t, got).Addr(); a != trgwTrunk {
		t.Errorf("offer to the IPv4 server carries media address %s", a)
	}

	if o, _ := got.Origin(); o.AddrType != sdp.IP4 {
		t.Errorf("offer to the IPv4 server has o=%s", o)
	}

	if n := g.Relays(); n != 1 {
		t.Errorf("%d relays after the failover, want 1", n)
	}
}

func TestMediaFailoverRelayOfTheNextTrunk(t *testing.T) {
	m := trgw.NewMetrics()

	g, err := trgw.New(trgw.WithAddresses(trgw.Address{Bind: trgwPhone}, trgw.Address{Bind: trgwTrunk}),
		trgw.WithPortRange(33000, 33999), trgw.WithMetrics(m))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = g.Close() })

	s := newSceneWith(t, false, func(cfg *Config) {
		cfg.Media = &MediaConfig{Gateway: g, Phone: []netip.Addr{trgwPhone}, Trunk: []netip.Addr{trgwTrunk}}
	})
	second := withSecondTrunk(s, false)

	phone := newEndpoint(t)
	s.call(phone.sdp(1, "sendrecv"))

	_ = s.carrier.next("invite").req.Reject(503)

	second.next("invite")

	if got := relaysOf(t, m); got["second"] != 1 || got["carrier"] != 0 {
		t.Errorf("relays by trunk %v, want 1 of the second trunk", got)
	}
}

func relaysOf(t *testing.T, m *trgw.Metrics) map[string]float64 {
	t.Helper()

	r := prometheus.NewPedanticRegistry()
	r.MustRegister(m.Collectors()...)

	families, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]float64{}

	for _, f := range families {
		if f.GetName() != "ellaims_trunk_media_relays" {
			continue
		}

		for _, mt := range f.GetMetric() {
			for _, l := range mt.GetLabel() {
				if l.GetName() == "trunk" {
					got[l.GetValue()] = mt.GetGauge().GetValue()
				}
			}
		}
	}

	return got
}

func (p *party) nextOf(kinds ...string) event {
	p.t.Helper()
	p.net.Quiesce()

	for {
		select {
		case e := <-p.events:
			if slices.Contains(kinds, e.kind) {
				return e
			}
		case <-time.After(wait):
			p.t.Fatalf("%s: no %v event", p.name, kinds)
			return event{}
		}
	}
}

// RFC 3264 section 8.4
func TestMediaHoldWithUnspecifiedAddress(t *testing.T) {
	s := mediaScene(t, 0)
	phone, carrier := newEndpoint(t), newEndpoint(t)

	s.call(phone.sdp(1, "sendrecv"))

	in := s.carrier.next("invite").req
	_ = in.Accept(sdpOf(carrier.sdp(1, "sendrecv"))...)

	e := s.ims.next("established")
	s.ims.next("answer")
	s.carrier.next("established")

	for i, tc := range []struct{ dir, want sdp.Direction }{{sdp.SendRecv, sdp.SendOnly}, {sdp.RecvOnly, sdp.Inactive}} {
		body := strings.Replace(phone.sdp(2+i, string(tc.dir)), "c=IN IP4 127.0.0.1", "c=IN IP4 0.0.0.0", 1)
		done := make(chan *sip.Response, 1)
		_ = e.s.Reinvite(context.Background(), ua.Change{Bodies: sdpOf(body)}, func(res *sip.Response, _ error) { done <- res })

		r := s.carrier.next("request").req
		got := parse(t, r.Message())
		_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf(carrier.sdp(2+i, "recvonly"))})

		s.net.Quiesce()
		<-done
		s.ims.until("answer")

		if d := got.MediaDirection(0); d != tc.want || rtpOf(t, got).Addr().IsUnspecified() {
			t.Errorf("%s held with c=0.0.0.0 reaches the carrier as %s at %s", tc.dir, d, rtpOf(t, got))
		}
	}
}

// SIPconnect 2.0 section 5
func TestMediaOneAudioStream(t *testing.T) {
	g, err := trgw.New(trgw.WithAddresses(trgw.Address{Bind: trgwPhone}, trgw.Address{Bind: trgwTrunk}),
		trgw.WithPortRange(34000, 39999), trgw.WithMediaTimeout(0))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = g.Close() })

	s := newSceneWith(t, false, func(cfg *Config) {
		cfg.Media = &MediaConfig{Gateway: g, Phone: []netip.Addr{trgwPhone}, Trunk: []netip.Addr{trgwTrunk}}
	})

	lines := []string{"v=0", "o=c 1 1 IN IP4 127.0.0.1", "s=-", "c=IN IP4 127.0.0.1", "t=0 0"}
	for i := range 300 {
		lines = append(lines, fmt.Sprintf("m=audio %d RTP/AVP 0", 40000+2*i), "a=inactive")
	}

	s.dial(s.carrier, strings.Join(lines, "\r\n")+"\r\n")

	offer := parse(t, s.ims.next("invite").msg)

	if n := g.Relays(); n != 1 {
		t.Errorf("one INVITE holds %d relays", n)
	}

	for i, md := range offer.Media {
		if (md.Port() != 0) != (i == 0) {
			t.Errorf("m-line %d port %d", i, md.Port())
		}
	}
}

// SIPconnect 2.0 section 14.2
func TestMediaOnlyRTPOverUDP(t *testing.T) {
	s := mediaScene(t, 0)
	phone := newEndpoint(t)
	a := phone.addr()

	body := strings.Join([]string{
		"v=0", "o=ue 1 1 IN IP4 127.0.0.1", "s=-", "c=IN IP4 127.0.0.1", "t=0 0",
		"m=audio 4000 RTP/SAVP 116", "c=IN IP4 127.0.0.1", "a=rtpmap:116 AMR-WB/16000",
		"m=audio 4002 UDP/TLS/RTP/SAVP 116", "a=rtpmap:116 AMR-WB/16000",
		"m=audio 4004 TCP/RTP/AVP 116", "a=rtpmap:116 AMR-WB/16000",
		fmt.Sprintf("m=audio %d RTP/AVPF 116", a.Port()), "a=rtpmap:116 AMR-WB/16000",
		"m=video 4006 RTP/AVP 112", "c=IN IP4 127.0.0.1", "a=rtpmap:112 H265/90000",
	}, "\r\n") + "\r\n"

	s.call(body)

	offer := parse(t, s.carrier.next("invite").msg)

	for i, md := range offer.Media {
		_, err := md.Connection()

		switch {
		case (md.Port() != 0) != (i == 3):
			t.Errorf("m-line %d port %d: %s", i, md.Port(), offer)
		case i != 3 && err == nil:
			t.Errorf("disabled m-line %d keeps its c=: %s", i, offer)
		}
	}
}

func TestMediaNoRelayableStream(t *testing.T) {
	s := mediaScene(t, 0)

	body := strings.Join([]string{
		"v=0", "o=ue 1 1 IN IP4 127.0.0.1", "s=-", "c=IN IP4 127.0.0.1", "t=0 0",
		"m=audio 4000 RTP/SAVP 116", "a=rtpmap:116 AMR-WB/16000",
		"m=video 4002 RTP/AVP 112", "a=rtpmap:112 H265/90000",
	}, "\r\n") + "\r\n"

	s.call(body)

	if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != 488 {
		t.Fatalf("offer without a relayable stream got %+v", e.res)
	}

	s.carrier.none()
}
