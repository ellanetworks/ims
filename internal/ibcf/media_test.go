package ibcf

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/trgw"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

var (
	trgwPhone = netip.MustParseAddr("127.0.0.1")
	trgwTrunk = netip.MustParseAddr("127.0.0.2")
)

func mediaScene(t *testing.T, timeout time.Duration) *scene {
	t.Helper()

	return tracedMediaScene(t, timeout, nil)
}

func tracedMediaScene(t *testing.T, timeout time.Duration, tp trace.TracerProvider) *scene {
	t.Helper()

	g, err := trgw.New(trgw.WithAddresses(trgw.Address{Bind: trgwPhone}, trgw.Address{Bind: trgwTrunk}),
		trgw.WithPortRange(31000, 31999), trgw.WithMediaTimeout(timeout))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = g.Close() })

	return newTracedScene(t, false, tp, func(cfg *Config) {
		cfg.Media = &MediaConfig{Gateway: g, Phone: []netip.Addr{trgwPhone}, Trunk: []netip.Addr{trgwTrunk}}
	})
}

type endpoint struct {
	t    *testing.T
	conn *net.UDPConn
	seq  uint16
}

func newEndpoint(t *testing.T) *endpoint {
	t.Helper()

	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = c.Close() })

	return &endpoint{t: t, conn: c}
}

func (e *endpoint) addr() netip.AddrPort {
	return e.conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

func (e *endpoint) sdp(version int, dir string, extra ...string) string {
	a := e.addr()

	lines := []string{
		"v=0",
		fmt.Sprintf("o=ue 4711 %d IN IP4 10.45.0.7", version),
		"s=-",
		"c=IN IP4 " + a.Addr().String(),
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP 116 111", a.Port()),
		"b=AS:41",
		"a=rtpmap:116 AMR-WB/16000/1",
		"a=fmtp:116 mode-change-capability=2;max-red=220",
		"a=rtpmap:111 telephone-event/16000",
		"a=ptime:20",
		"a=rtcp-mux",
		"a=candidate:1 1 UDP 2130706431 10.45.0.7 5004 typ host",
		"a=" + dir,
	}

	return strings.Join(append(lines, extra...), "\r\n") + "\r\n"
}

func (e *endpoint) send(to netip.AddrPort) {
	e.seq++

	b := make([]byte, 12+20)
	b[0], b[1] = 0x80, 116
	binary.BigEndian.PutUint16(b[2:], e.seq)
	binary.BigEndian.PutUint32(b[4:], uint32(e.seq)*320)
	binary.BigEndian.PutUint32(b[8:], 0x1234)

	if _, err := e.conn.WriteToUDPAddrPort(b, to); err != nil {
		e.t.Fatal(err)
	}
}

func (e *endpoint) receive(from netip.AddrPort) {
	e.t.Helper()

	_ = e.conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	buf := make([]byte, 1500)

	n, src, err := e.conn.ReadFromUDPAddrPort(buf)
	if err != nil {
		e.t.Fatalf("no RTP: %v", err)
	}

	if src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port()); src != from || n != 32 || binary.BigEndian.Uint32(buf[8:]) != 0x1234 {
		e.t.Fatalf("RTP of %d bytes from %s, want 32 from %s", n, src, from)
	}
}

func parse(t *testing.T, m sip.Message) *sdp.Session {
	t.Helper()

	part, ok, _ := m.Env().Content().FindFunc(sip.BodyPart.IsSession)
	if !ok {
		t.Fatalf("no SDP in %s", m)
	}

	s, err := sdp.Parse(part.Body)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func rtpOf(t *testing.T, s *sdp.Session) netip.AddrPort {
	t.Helper()

	e, err := s.RTPEndpoint(0)
	if err != nil {
		t.Fatal(err)
	}

	return netip.AddrPortFrom(e.Addr, e.Port)
}

func TestMediaExitCall(t *testing.T) {
	s := mediaScene(t, 0)
	phone, carrier := newEndpoint(t), newEndpoint(t)

	s.call(phone.sdp(1, "sendrecv", "m=video 5006 RTP/AVP 112", "a=rtpmap:112 H265/90000"))

	in := s.carrier.next("invite").req
	offer := parse(t, in.Message())
	toTrunk := rtpOf(t, offer)

	o, _ := offer.Origin()

	switch {
	case toTrunk.Addr() != trgwTrunk:
		t.Errorf("trunk offer media %s, want on %s", toTrunk, trgwTrunk)
	case o.Address != trgwTrunk.String() || o.Username != "-":
		t.Errorf("trunk offer o=%s", o)
	case offer.Media[0].HasAttr("rtcp-mux") || offer.Media[0].HasAttr("candidate"):
		t.Errorf("trunk offer kept rtcp-mux or candidates: %s", offer)
	case offer.Media[1].Port() != 0:
		t.Errorf("video offered to the trunk: %s", offer)
	case !strings.Contains(offer.String(), "a=fmtp:116 mode-change-capability=2;max-red=220") || !strings.Contains(offer.String(), "b=AS:41"):
		t.Errorf("trunk offer lost codec lines: %s", offer)
	}

	_ = in.Accept(sdpOf(carrier.sdp(1, "sendrecv", "m=video 0 RTP/AVP 112"))...)

	e := s.ims.next("established")
	answer := parse(t, e.msg)
	toPhone := rtpOf(t, answer)

	if toPhone.Addr() != trgwPhone || toPhone == toTrunk {
		t.Fatalf("phone answer media %s", toPhone)
	}

	s.ims.next("answer")
	s.carrier.next("established")

	phone.send(toPhone)
	carrier.receive(toTrunk)

	carrier.send(toTrunk)
	phone.receive(toPhone)
}

func TestMediaEntryWithoutOffer(t *testing.T) {
	s := mediaScene(t, 0)
	phone, carrier := newEndpoint(t), newEndpoint(t)

	s.dial(s.carrier, "")

	in := s.ims.next("invite").req
	_ = in.Accept(sdpOf(phone.sdp(1, "sendrecv"))...)

	e := s.carrier.all("offer", "established")["offer"]

	offer, err := sdp.Parse(e.sdp)
	if err != nil {
		t.Fatal(err)
	}

	toTrunk := rtpOf(t, offer)
	if toTrunk.Addr() != trgwTrunk {
		t.Fatalf("carrier's offer media %s", toTrunk)
	}

	_ = e.offer.Answer(context.Background(), ua.Change{Bodies: sdpOf(carrier.sdp(1, "sendrecv"))})

	got := s.ims.all("established", "answer")

	answer, err := sdp.Parse(got["answer"].sdp)
	if err != nil {
		t.Fatal(err)
	}

	toPhone := rtpOf(t, answer)

	carrier.send(toTrunk)
	phone.receive(toPhone)

	phone.send(toPhone)
	carrier.receive(toTrunk)
}

func TestMediaHoldAndUnchangedOffer(t *testing.T) {
	s := mediaScene(t, 0)
	phone, carrier := newEndpoint(t), newEndpoint(t)

	s.call(phone.sdp(1, "sendrecv"))

	in := s.carrier.next("invite").req
	first, _ := parse(t, in.Message()).Origin()
	_ = in.Accept(sdpOf(carrier.sdp(1, "sendrecv"))...)

	e := s.ims.next("established")
	s.ims.next("answer")
	s.carrier.next("established")

	reinvite := func(body, answer string) (*sdp.Session, *sip.Response) {
		done := make(chan *sip.Response, 1)
		_ = e.s.Reinvite(context.Background(), ua.Change{Bodies: sdpOf(body)}, func(res *sip.Response, _ error) { done <- res })

		r := s.carrier.next("request").req
		got := parse(t, r.Message())
		_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf(answer)})

		s.net.Quiesce()

		res := <-done

		s.ims.until("answer")

		return got, res
	}

	held, res := reinvite(phone.sdp(2, "sendonly"), carrier.sdp(2, "recvonly"))
	o, _ := held.Origin()

	if held.MediaDirection(0) != sdp.SendOnly || o.SessionVersion == first.SessionVersion || o.SessionID != first.SessionID {
		t.Errorf("hold offer to the trunk: %s", held)
	}

	if parse(t, res).MediaDirection(0) != sdp.RecvOnly {
		t.Errorf("hold answer to the phone: %s", res)
	}

	heldAnswer, _ := parse(t, res).Origin()

	done := make(chan *sip.Response, 1)
	_ = e.s.Reinvite(context.Background(), ua.Change{Bodies: sdpOf(phone.sdp(3, "sendonly"))}, func(res *sip.Response, _ error) { done <- res })

	s.net.Quiesce()

	again := <-done
	if again == nil || again.StatusCode != 200 {
		t.Fatalf("re-INVITE unchanged but for its version: %v", again)
	}

	if o2, _ := parse(t, again).Origin(); o2.SessionVersion != heldAnswer.SessionVersion {
		t.Errorf("answer to an offer unchanged but for its version has version %s, want %s", o2.SessionVersion, heldAnswer.SessionVersion)
	}

	s.carrier.none()
}

func TestMediaCarrierMovesItsMedia(t *testing.T) {
	s := mediaScene(t, 0)
	phone, carrier, moved := newEndpoint(t), newEndpoint(t), newEndpoint(t)

	s.call(phone.sdp(1, "sendrecv"))

	in := s.carrier.next("invite").req
	toTrunk := rtpOf(t, parse(t, in.Message()))
	_ = in.Accept(sdpOf(carrier.sdp(1, "sendrecv"))...)

	toPhone := rtpOf(t, parse(t, s.ims.next("established").msg))
	s.ims.next("answer")

	ce := s.carrier.next("established")

	done := make(chan *sip.Response, 1)
	_ = ce.s.Reinvite(context.Background(), ua.Change{Bodies: sdpOf(moved.sdp(2, "sendrecv"))}, func(res *sip.Response, _ error) { done <- res })

	s.net.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 200 {
		t.Fatalf("re-INVITE: %v", res)
	}

	s.ims.none()

	phone.send(toPhone)
	moved.receive(toTrunk)

	carrier.send(toTrunk)
	moved.send(toTrunk)
	phone.receive(toPhone)
}

func TestMediaTimeoutReleasesTheCall(t *testing.T) {
	s := mediaScene(t, 300*time.Millisecond)
	phone, carrier := newEndpoint(t), newEndpoint(t)

	s.call(phone.sdp(1, "sendrecv"))

	in := s.carrier.next("invite").req
	_ = in.Accept(sdpOf(carrier.sdp(1, "sendrecv"))...)

	s.ims.next("established")
	s.ims.next("answer")
	s.carrier.next("established")

	time.Sleep(time.Second)

	if bye := s.carrier.until("bye"); !strings.Contains(values(bye.msg, "Reason"), "408") {
		t.Errorf("BYE to the trunk Reason %q", values(bye.msg, "Reason"))
	}

	s.ims.until("bye")
}

func TestMediaTimeoutSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	s := tracedMediaScene(t, 300*time.Millisecond, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	phone, carrier := newEndpoint(t), newEndpoint(t)

	s.call(phone.sdp(1, "sendrecv"))

	in := s.carrier.next("invite").req
	_ = in.Accept(sdpOf(carrier.sdp(1, "sendrecv"))...)

	s.ims.next("established")
	s.ims.next("answer")
	s.carrier.next("established")

	time.Sleep(time.Second)

	s.carrier.until("bye")
	s.ims.until("bye")
	s.net.Quiesce()

	timeout := oneSpan(t, rec, role, "media timeout", trace.SpanKindInternal)
	invite := oneSpan(t, rec, role, "INVITE", trace.SpanKindServer)

	if timeout.Parent().IsValid() || len(timeout.Links()) != 1 || timeout.Links()[0].SpanContext.SpanID() != invite.SpanContext().SpanID() {
		t.Fatalf("media timeout span: parent %v, links %v, want a new trace linked to the INVITE", timeout.Parent(), timeout.Links())
	}

	byes := spansOf(rec, role, "BYE", trace.SpanKindClient)
	if len(byes) != 2 {
		t.Fatalf("%d BYE spans, want 2", len(byes))
	}

	for _, b := range byes {
		if b.Parent().SpanID() != timeout.SpanContext().SpanID() {
			t.Errorf("BYE span parent %v, want the media timeout span", b.Parent())
		}
	}
}

func TestMediaEarly(t *testing.T) {
	s := mediaScene(t, 0)
	phone, carrier := newEndpoint(t), newEndpoint(t)

	s.call(phone.sdp(1, "sendrecv"), f("Supported", "100rel"))

	in := s.carrier.next("invite").req
	toTrunk := rtpOf(t, parse(t, in.Message()))

	_ = in.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf(carrier.sdp(1, "sendonly"))})

	p := s.ims.all("progress", "answer")["progress"]
	early := parse(t, p.res)

	if early.MediaDirection(0) != sdp.SendOnly {
		t.Errorf("early media direction %s", early.MediaDirection(0))
	}

	toPhone := rtpOf(t, early)

	carrier.send(toTrunk)
	phone.receive(toPhone)
}
