package client_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/amr"
	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/client/clienttest"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/floorctl"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/mcptt/rtp"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/ua"
)

func call(t *testing.T, e *env, a, b *user) (*client.Call, *client.Call) {
	t.Helper()

	ca, err := a.Call(context.Background(), mustURI(t, fire))
	if err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	es := b.expectUnordered(t, "incoming", "floor:idle", "established")
	cb := es[0].Call

	if !cb.Incoming() || cb.Manual() || cb.Group().String() != fire || cb.Caller().String() != a.id {
		t.Errorf("invitation: incoming %v manual %v group %s caller %s", cb.Incoming(), cb.Manual(), cb.Group(), cb.Caller())
	}

	return ca, cb
}

// RFC 4867 section 4.1
// RFC 4867 section 4.3.2
// RFC 4867 section 5.3
// RFC 3550 section 5.1
func TestSpeechFrames(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)
	a := e.user("alice", 1, s.addr, nil)
	a.mute.Store(true)

	var (
		mu   sync.Mutex
		sent []rtp.Packet
	)

	e.m.Lose(func(from, _ netip.AddrPort, b []byte) bool {
		if from == netip.MustParseAddrPort("192.0.2.1:30000") {
			if p, err := rtp.Parse(b); err == nil {
				mu.Lock()
				defer mu.Unlock()

				sent = append(sent, p)
			}
		}

		return false
	})

	ca, cb := call(t, e, a, b)
	a.expect(t, "established", "floor:idle")

	ca.PressPTT()
	e.n.Quiesce()

	if es := a.expect(t, "floor:granted", "transmit:on"); es[1].Mode != 2 {
		t.Fatalf("transmit at mode %d, want 12.65 kbit/s", es[1].Mode)
	}

	b.expect(t, "floor:taken")

	speech, noData := clienttest.Silence(2), []byte{amr.FrameNoData<<3 | 0x04}

	for _, f := range [][]byte{speech, speech, noData, noData, speech} {
		if err := ca.SendSpeech(f); err != nil {
			t.Fatal(err)
		}
	}

	for _, f := range [][]byte{speech[:5], {amr.FrameSpeechLost << 3}, append(speech, 0)} {
		if err := ca.SendSpeech(f); err == nil {
			t.Errorf("frame % x accepted", f)
		}
	}

	e.n.Quiesce()
	b.expect(t, "heard")

	mu.Lock()
	if len(sent) != 3 {
		t.Fatalf("%d packets sent, want 3", len(sent))
	}

	for i, want := range []struct {
		marker bool
		ts     uint32
	}{{true, 0}, {false, 320}, {true, 4 * 320}} {
		if p := sent[i]; p.Marker != want.marker || p.Timestamp-sent[0].Timestamp != want.ts || i > 0 && p.Sequence != sent[i-1].Sequence+1 {
			t.Errorf("packet %d: marker %v, timestamp +%d, sequence %d", i, p.Marker, p.Timestamp-sent[0].Timestamp, p.Sequence)
		}
	}
	mu.Unlock()

	e.n.Advance(60 * time.Millisecond)

	var played []byte
	for range 5 {
		played = append(played, cb.NextSpeech()[0]>>3)
	}

	if !slices.Equal(played, []byte{2, 2, amr.FrameSpeechLost, amr.FrameSpeechLost, 2}) {
		t.Errorf("played frame types %v", played)
	}
}

// TS 24.379 clause 10.1.1.2.1
// TS 24.380 clause 6.2.4
func TestGroupCall(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)
	a := e.user("alice", 1, s.addr, nil)

	var (
		mu   sync.Mutex
		sent []rtp.Packet
	)

	e.m.Lose(func(from, _ netip.AddrPort, b []byte) bool {
		if from == netip.MustParseAddrPort("192.0.2.1:30000") {
			if p, err := rtp.Parse(b); err == nil {
				mu.Lock()
				defer mu.Unlock()

				sent = append(sent, p)
			}
		}

		return false
	})

	ca, cb := call(t, e, a, b)

	es := a.expect(t, "established", "floor:idle")
	if res := es[0].Response; res == nil || res.StatusCode != 200 {
		t.Errorf("established with %v", res)
	}

	checkInvite(t, s.inviteReq)
	checkOK(t, s.ok)

	ca.PressPTT()
	e.n.Quiesce()

	a.expect(t, "floor:granted", "transmit:on")

	es = b.expectUnordered(t, "floor:taken", "heard")
	talker := s.originLeg.AudioSSRC()

	for _, ev := range es {
		if ev.Kind == client.EventFloor && ev.Floor.Party != a.id || ev.Kind == client.EventHeard && ev.SSRC != talker {
			t.Errorf("event %+v, talker %d", ev, talker)
		}
	}

	for range 50 {
		e.n.Advance(20 * time.Millisecond)
	}

	heard := cb.Heard()
	if len(heard) != 1 || heard[0].SSRC != talker || heard[0].Party != a.id || heard[0].Packets != ca.Sent() || ca.Sent() != 51 {
		t.Fatalf("heard %+v, %d sent", heard, ca.Sent())
	}

	ca.ReleasePTT()
	e.n.Advance(time.Second)

	a.expect(t, "transmit:off", "floor:idle")
	b.expect(t, "floor:idle")

	if ca.Sent() != 51 || cb.Heard()[0].Packets != 51 {
		t.Errorf("media after the release: %d sent", ca.Sent())
	}

	mu.Lock()
	if len(sent) != 51 {
		t.Fatalf("%d packets sent", len(sent))
	}

	for i, p := range sent {
		if p.Marker != (i == 0) || p.PayloadType != 96 || p.SSRC != talker || len(p.Payload) != 33 ||
			i > 0 && (p.Sequence != sent[i-1].Sequence+1 || p.Timestamp != sent[i-1].Timestamp+320) {
			t.Fatalf("packet %d: %+v after %+v", i, p, sent[max(i-1, 0)])
		}
	}
	mu.Unlock()

	cb.PressPTT()
	e.n.Advance(100 * time.Millisecond)

	b.expect(t, "floor:granted", "transmit:on")
	a.expectUnordered(t, "floor:taken", "heard")

	if h := ca.Heard(); len(h) != 1 || h[0].Party != b.id || h[0].Packets != cb.Sent() {
		t.Errorf("caller heard %+v, %d sent", h, cb.Sent())
	}

	cb.ReleasePTT()
	e.n.Advance(time.Second)

	a.expect(t, "floor:idle")
	b.expect(t, "transmit:off", "floor:idle")

	ca.Leave(context.Background())
	e.n.Quiesce()

	a.expect(t, "ended")

	es = b.expect(t, "ended")
	if !es[0].End.Remote {
		t.Errorf("end %+v", es[0].End)
	}

	for _, u := range []*user{a, b} {
		if st := u.agent.Stats(); st.Outgoing != 0 || st.Invites != 0 {
			t.Errorf("%s: %+v", u.id, st)
		}
	}
}

// TS 24.380 clause 6.2.4.2.2
// TS 24.380 clause 14.2.4
// TS 24.380 clause 14.2.5
// TS 24.380 clause 14.4
func TestImplicitGranted(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)
	s.grant = true

	a := e.user("alice", 1, s.addr, func(c *client.Config) { c.ImplicitRequest, c.Granted = true, true })

	ca, err := a.Call(context.Background(), mustURI(t, fire))
	if err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	a.expectGrant(t, "established", "floor:granted", "transmit:on")
	es := b.expectUnordered(t, "incoming", "established", "floor:taken", "heard")

	e.n.Advance(200 * time.Millisecond)

	es = slices.DeleteFunc(es, func(ev client.Event) bool { return ev.Kind != client.EventHeard })
	if es[0].SSRC != s.originLeg.AudioSSRC() || ca.Sent() != 11 {
		t.Errorf("heard SSRC %d, want %d; %d sent", es[0].SSRC, s.originLeg.AudioSSRC(), ca.Sent())
	}

	ca.Leave(context.Background())
	e.n.Quiesce()

	a.expect(t, "transmit:off", "ended")
	b.expect(t, "ended")
}

func TestFloorAfterOK(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)
	s.grant, s.lateFloor = true, true

	a := e.user("alice", 1, s.addr, func(c *client.Config) { c.ImplicitRequest, c.Granted = true, true })

	ca, err := a.Call(context.Background(), mustURI(t, fire))
	if err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()
	e.n.Advance(200 * time.Millisecond)

	a.expectGrant(t, "established", "floor:granted", "transmit:on")
	b.expectUnordered(t, "incoming", "established", "floor:taken", "heard")

	ca.Leave(context.Background())
	e.n.Quiesce()

	a.expect(t, "transmit:off", "ended")
	b.expect(t, "ended")
}

// TS 24.379 clause 10.1.1.2.1.2
// TS 24.379 clause 6.2.3.2.2
func TestManualCommencement(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)
	s.answerMode = sip.AnswerModeManual
	a := e.user("alice", 1, s.addr, nil)

	if _, err := a.Call(context.Background(), mustURI(t, fire)); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	es := b.expect(t, "incoming")
	if !es[0].Call.Manual() {
		t.Fatal("not manual")
	}

	es[0].Call.Decline()
	e.n.Quiesce()

	if len(s.declined) != 1 || s.declined[0].StatusCode != 480 {
		t.Fatalf("declined %v", s.declined)
	}

	ws, err := s.declined[0].Header.Warnings()
	if err != nil || len(ws) != 1 || ws[0].Code != 399 || ws[0].Text != identity.WarnUserDeclined {
		t.Errorf("warnings %v", ws)
	}

	b.expect(t, "ended")

	s.declined = nil
	s.invite(s.members[0])
	e.n.Quiesce()

	es = b.expect(t, "incoming")
	es[0].Call.Accept()
	e.n.Quiesce()

	b.expectUnordered(t, "floor:idle", "established")
	a.expect(t, "established", "floor:idle")
}

// TS 24.379 clause 10.1.1.2.1.1
func checkInvite(t *testing.T, req *sip.Request) {
	t.Helper()

	cs, err := req.Header.Contacts()
	if err != nil || len(cs) != 1 {
		t.Fatalf("Contact %v", req.Header.Values("Contact"))
	}

	if fs, _ := cs[0].Features(); !fs.Has("g.3gpp.mcptt") || !slices.Equal(fs.Tokens("g.3gpp.icsi-ref"), []string{identity.ICSI}) {
		t.Errorf("Contact features %v", fs)
	}

	acs, err := req.Header.AcceptContact()
	if err != nil || len(acs) != 2 {
		t.Fatalf("Accept-Contact %v", req.Header.Values("Accept-Contact"))
	}

	for i, tag := range []string{"g.3gpp.mcptt", "g.3gpp.icsi-ref"} {
		if ac := acs[i]; !ac.Require || !ac.Explicit || len(ac.Features) != 1 || !ac.Features.Has(tag) {
			t.Errorf("Accept-Contact %d: %+v", i, ac)
		}
	}

	if req.URI.String() != psi || req.Header.Get("P-Preferred-Service") != identity.ICSI || !req.Header.HasOptionTag("Supported", "timer") {
		t.Errorf("INVITE %s, P-Preferred-Service %q, Supported %v", req.URI, req.Header.Get("P-Preferred-Service"), req.Header.Values("Supported"))
	}

	part, ok, err := req.Content().Find(mcpttinfo.ContentType)
	if !ok || err != nil {
		t.Fatal("no mcptt-info")
	}

	info, err := mcpttinfo.Decode(part.Body)
	if err != nil || info.Params == nil {
		t.Fatal(err)
	}

	p := info.Params
	group, _ := p.RequestURI.URI()
	clientID, _ := p.ClientID.Text()

	if p.SessionType != mcpttinfo.SessionPrearranged || group != fire || clientID != "urn:uuid:00000000-0000-1000-8000-000000000001" {
		t.Errorf("mcptt-info %s", part.Body)
	}
}

// TS 24.379 clause 6.2.3.1.1
func checkOK(t *testing.T, res *sip.Response) {
	t.Helper()

	cs, err := res.Header.Contacts()
	if err != nil || len(cs) != 1 {
		t.Fatalf("Contact %v", res.Header.Values("Contact"))
	}

	if fs, _ := cs[0].Features(); !fs.Has("g.3gpp.mcptt") || !slices.Equal(fs.Tokens("g.3gpp.icsi-ref"), []string{identity.ICSI}) {
		t.Errorf("Contact features %v", fs)
	}

	se, ok, err := res.Header.SessionExpires()
	if !ok || err != nil || se.Refresher != sip.RefresherUAS || !res.Header.HasOptionTag("Require", "timer") {
		t.Errorf("Session-Expires %q, Require %v", res.Header.Get("Session-Expires"), res.Header.Values("Require"))
	}
}

// TS 24.379 clause 10.1.1.2.3.1
// TS 24.379 clause 6.2.4.1
func TestMemberLeaves(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)
	a := e.user("alice", 1, s.addr, nil)

	ca, cb := call(t, e, a, b)
	a.expect(t, "established", "floor:idle")

	cb.Leave(context.Background())
	e.n.Quiesce()

	es := b.expect(t, "ended")
	if es[0].End.Remote {
		t.Errorf("end %+v", es[0].End)
	}

	a.expect(t)

	if st := b.agent.Stats(); st.Invites != 0 {
		t.Errorf("%s: %+v", b.id, st)
	}

	ca.Leave(context.Background())
	e.n.Quiesce()

	a.expect(t, "ended")
}

// TS 24.379 clause 6.2.5.1
func TestCancel(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)
	s.answerMode = sip.AnswerModeManual
	a := e.user("alice", 1, s.addr, nil)

	ca, err := a.Call(context.Background(), mustURI(t, fire))
	if err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	es := b.expect(t, "incoming")

	ca.Leave(context.Background())
	e.n.Quiesce()

	got := a.expect(t, "failed")
	if res := got[0].Response; res == nil || res.StatusCode != 487 {
		t.Errorf("failed with %v, %v", res, got[0].Err)
	}

	if st := a.agent.Stats(); st.Outgoing != 0 || st.Invites != 0 {
		t.Errorf("%s: %+v", a.id, st)
	}

	es[0].Call.Decline()
	e.n.Quiesce()

	b.expect(t, "ended")
}

// TS 24.380 clause 6.2.4.2.2
func TestFloorBeforeOK(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)

	a := e.user("alice", 1, s.addr, func(c *client.Config) { c.ImplicitRequest = true })

	ca, err := a.Call(context.Background(), mustURI(t, fire))
	if err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	a.expectGrant(t, "established", "floor:granted", "transmit:on")
	b.expectUnordered(t, "incoming", "established", "floor:taken", "heard")

	ca.Leave(context.Background())
	e.n.Quiesce()

	a.expect(t, "transmit:off", "ended")
	b.expect(t, "ended")
}

// RFC 3264 section 6
// TS 24.379 clause 6.2.1
func TestBadAnswer(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)
	s.badAudio = true
	a := e.user("alice", 1, s.addr, nil)

	if _, err := a.Call(context.Background(), mustURI(t, fire)); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	es := a.expect(t, "failed")
	if es[0].Err == nil || es[0].Response == nil || es[0].Response.StatusCode != 200 {
		t.Errorf("failed with %v, %v", es[0].Response, es[0].Err)
	}

	got := kinds(b.events.take())
	rest := slices.DeleteFunc(slices.Sorted(slices.Values(got)), func(k string) bool { return k == "floor:idle" })

	if want := []string{"ended", "established", "incoming"}; !slices.Equal(rest, want) {
		t.Errorf("%s: events %v, want %v and perhaps floor:idle", b.id, got, want)
	}

	if st := a.agent.Stats(); st.Outgoing != 0 || st.Invites != 0 {
		t.Errorf("%s: %+v", a.id, st)
	}
}

// TS 24.379 clause 10.1.1.2.1.2
func TestNoPort(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, func(c *client.Config) { c.MinPort, c.MaxPort = 30000, 30005 })
	s := e.standIn(b)
	a := e.user("alice", 1, s.addr, func(c *client.Config) { c.MinPort, c.MaxPort = 30000, 30005 })

	if _, err := a.Call(context.Background(), mustURI(t, fire)); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	b.expectUnordered(t, "incoming", "floor:idle", "established")
	a.expect(t, "established", "floor:idle")

	if _, err := a.Call(context.Background(), mustURI(t, fire)); !errors.Is(err, client.ErrNoPort) {
		t.Errorf("second call: %v", err)
	}

	s.invite(s.members[0])
	e.n.Quiesce()

	if len(s.declined) != 1 || s.declined[0].StatusCode != 480 {
		t.Errorf("declined %v", s.declined)
	}

	b.expect(t)
}

func TestDropsLogged(t *testing.T) {
	var logs syncBuffer

	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, func(c *client.Config) { c.Logger = logger })
	s := e.standIn(b)
	a := e.user("alice", 1, s.addr, nil)

	call(t, e, a, b)

	peer, err := e.m.Peer(netip.MustParseAddrPort("192.0.2.9:4000"))
	if err != nil {
		t.Fatal(err)
	}

	audio, control := netip.MustParseAddrPort("192.0.2.2:30000"), netip.MustParseAddrPort("192.0.2.2:30002")

	for seq := range uint16(3) {
		p := rtp.Packet{PayloadType: 100, Sequence: seq, SSRC: 7, Payload: []byte{0xf0}}

		raw, err := p.Append(nil)
		if err != nil {
			t.Fatal(err)
		}

		_ = peer.WriteTo(raw, audio)
	}

	unknown, err := rtp.AppendRTCP(nil, &rtp.App{Subtype: 0b01011, SSRC: 7, Name: floorctl.Name})
	if err != nil {
		t.Fatal(err)
	}

	_ = peer.WriteTo(unknown, control)

	e.n.Quiesce()

	out := logs.String()
	if n := strings.Count(out, `msg="audio dropped" reason="payload type 100, 96 negotiated"`); n != 1 {
		t.Errorf("audio dropped logged %d times: %s", n, out)
	}

	if n := strings.Count(out, `msg="floor control message dropped"`); n != 1 {
		t.Errorf("floor control dropped logged %d times: %s", n, out)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.String()
}

// TS 24.379 clause 10.1.1.2.1.2
func TestOneCallAGroup(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)
	a := e.user("alice", 1, s.addr, nil)

	ca, _ := call(t, e, a, b)
	a.expect(t, "established", "floor:idle")

	s.st.Post(func() { s.invite(s.members[0]) })
	e.n.Quiesce()

	if len(s.declined) != 1 || s.declined[0] == nil || s.declined[0].StatusCode != 486 {
		t.Fatalf("declined %+v", s.declined)
	}

	b.expect(t)

	if _, err := b.Call(context.Background(), mustURI(t, fire)); !errors.Is(err, client.ErrInCall) {
		t.Errorf("second call: %v", err)
	}

	ca.Leave(context.Background())
	e.n.Quiesce()
	a.expect(t, "ended")
	b.expect(t, "ended")

	if _, err := b.Call(context.Background(), mustURI(t, fire)); errors.Is(err, client.ErrInCall) {
		t.Errorf("a call after the end: %v", err)
	}
}

// RFC 4028
// TS 24.379 clause 10.1.1.2.1.1
// TS 24.380 clause 6.2.4.4.6
func TestSessionTimerAndProbe(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)
	a := e.user("alice", 1, s.addr, nil)

	ca, cb := call(t, e, a, b)
	a.expect(t, "established", "floor:idle")

	if se, ok, err := s.inviteReq.Header.SessionExpires(); !ok || err != nil || se.Seconds != 90 || se.Refresher != "" {
		t.Errorf("alice's Session-Expires %+v %v %v", se, ok, err)
	}

	if se, ok, err := s.ok.Header.SessionExpires(); !ok || err != nil || se.Seconds != 90 || se.Refresher != sip.RefresherUAS {
		t.Errorf("bob's Session-Expires %+v %v %v", se, ok, err)
	}

	e.m.Lose(func(from, _ netip.AddrPort, _ []byte) bool { return from == netip.MustParseAddrPort("192.0.2.2:30002") })

	cb.PressPTT()

	for range 10 {
		e.n.Advance(time.Second)
	}

	b.expect(t, "floor:timeout")

	if n := s.updates.Load(); n != 1 {
		t.Errorf("%d UPDATEs", n)
	}

	s.forget.Store(true)
	cb.PressPTT()

	for range 10 {
		e.n.Advance(time.Second)
	}

	if es := b.expect(t, "floor:timeout", "ended"); es[1].End.Cause != ua.EndLost {
		t.Errorf("end %+v", es[1].End)
	}

	ca.Leave(context.Background())
	e.n.Quiesce()
	a.expect(t, "ended")
}

// TS 24.380 clause 14.2.5
// TS 24.380 clause 6.2.4.4.8
// TS 24.380 clause 14.3.5
// TS 24.380 clause 6.2.4.3.5
func TestImplicitNotAccepted(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)
	s.noImplicit = true

	a := e.user("alice", 1, s.addr, func(c *client.Config) { c.ImplicitRequest, c.Granted = true, true })

	ca, err := a.Call(context.Background(), mustURI(t, fire))
	if err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	es := slices.DeleteFunc(a.events.take(), func(e client.Event) bool { return e.Kind == client.EventFloor && e.Floor.Kind == floor.NoticeIdle })
	if got := kinds(es); !slices.Equal(got, []string{"established", "floor:granted", "transmit:on"}) {
		t.Fatalf("events %v", got)
	}

	ca.ReleasePTT()
	e.n.Quiesce()
	e.n.Advance(time.Second)
	a.expect(t, "transmit:off", "floor:idle")

	ca.Leave(context.Background())
	e.n.Quiesce()
	a.expect(t, "ended")
}

func TestImplicitReleasedEarly(t *testing.T) {
	e := newEnv(t)
	b := e.user("bob", 2, netip.AddrPort{}, nil)
	s := e.standIn(b)
	s.grant = true

	a := e.user("alice", 1, s.addr, func(c *client.Config) { c.ImplicitRequest, c.Granted = true, true })

	ca, err := a.Call(context.Background(), mustURI(t, fire))
	if err != nil {
		t.Fatal(err)
	}

	ca.ReleasePTT()
	e.n.Quiesce()
	e.n.Advance(time.Second)

	a.expect(t, "established", "floor:granted", "transmit:on", "transmit:off", "floor:idle")

	if ca.Sent() != 0 {
		t.Errorf("%d sent", ca.Sent())
	}

	ca.Leave(context.Background())
	e.n.Quiesce()
	a.expect(t, "ended")
}
