package floor

import (
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
)

func newParticipant(t *testing.T) (*Participant, *participantOut, *fakeClock) {
	t.Helper()

	c := &fakeClock{}
	out := &participantOut{}

	return newTestParticipant(t, c, out, 7), out, c
}

// testTimers are a client's configuration of the timers (TS 24.483): T103 as T1, three sends within 6 seconds
// (Table 11.1.1-1 NOTE 1, NOTE 2).
var testTimers = ParticipantTimers{T100: time.Second, T101: time.Second, T103: 4 * time.Second}

func newTestParticipant(t *testing.T, c *fakeClock, out *participantOut, ssrc uint32) *Participant {
	t.Helper()

	p, err := NewParticipant(ParticipantConfig{Clock: c, Out: out, Timers: testTimers, SSRC: ssrc})
	if err != nil {
		t.Fatal(err)
	}

	return p
}

// §11.1.1, TS 24.483: T100 and T101 are configured; counters default (Table 11.2.1-1); §11.1.3, TS 24.484: T7
// is configured, the others default (Table 11.1.3-1, Table 11.2.3-1).
func TestTimerConfig(t *testing.T) {
	if _, err := NewParticipant(ParticipantConfig{Timers: ParticipantTimers{T101: time.Second}}); err == nil {
		t.Error("participant without T100")
	}

	// Table 11.1.1-1 NOTE 1.
	if _, err := NewParticipant(ParticipantConfig{Timers: ParticipantTimers{T100: 2 * time.Second, T101: time.Second}}); err == nil {
		t.Error("Floor Releases for 6 seconds")
	}

	p := newTestParticipant(t, &fakeClock{}, &participantOut{}, 1)
	if p.cfg.Timers.C100 != 3 || p.cfg.Timers.C101 != 3 {
		t.Errorf("counters %+v", p.cfg.Timers)
	}

	tooLong := withT7(time.Second)
	tooLong.T1 = 7 * time.Second

	if _, err := NewServer(ServerConfig{Timers: tooLong}); err == nil {
		t.Error("T1 above its maximum of 6 seconds")
	}

	tooShort := withT7(time.Second)
	tooShort.T1 = MinT1 - time.Millisecond

	if _, err := NewServer(ServerConfig{Timers: tooShort}); err == nil {
		t.Error("T1 below its minimum")
	}

	if _, err := NewServer(ServerConfig{Timers: DefaultServerTimers}); err == nil {
		t.Error("server without T7")
	}

	noT8 := withT7(time.Second)
	noT8.T8 = 0

	if _, err := NewServer(ServerConfig{Timers: noT8}); err == nil {
		t.Error("server with T8 of 0")
	}

	// Table 11.1.3-1: T3 of 0 for audio cut-in.
	cutIn := withT7(time.Second)
	cutIn.T3, cutIn.C7 = 0, 0

	if s, err := NewServer(ServerConfig{Timers: cutIn}); err != nil || s.cfg.Timers != cutIn {
		t.Errorf("%+v, %v", s, err)
	}
}

func (o *participantOut) take() []floorctl.Message {
	s := o.sent
	o.sent = nil

	return s
}

func messageTypes(ms []floorctl.Message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, name(m.Type))
	}

	return out
}

// §6.2.4.3.5: a Floor Request in the participant's SSRC, with the location not provided, the priority and the call
// type configured.
func TestPress(t *testing.T) {
	p, out, _ := newParticipant(t)
	p.cfg.Priority, p.cfg.Indicator = 3, floorctl.IndicatorEmergency
	p.Join()
	p.Press()

	sent := out.take()
	if len(sent) != 1 || sent[0].Type != floorctl.Request || sent[0].SSRC != 7 || *sent[0].Priority != 3 ||
		*sent[0].Indicator != floorctl.IndicatorEmergency ||
		!slices.EqualFunc(sent[0].Other, []floorctl.Field{{ID: floorctl.FieldLocation, Value: []byte{0}}},
			func(a, b floorctl.Field) bool { return a.ID == b.ID && slices.Equal(a.Value, b.Value) }) {
		t.Fatalf("%+v", sent)
	}

	if p.state != pPendingRequest {
		t.Errorf("state %d", p.state)
	}

	// The message encodes as one of TS 24.380 §8.2.4.
	if _, err := sent[0].App(); err != nil {
		t.Error(err)
	}
}

// §6.2.4.4.5, §6.2.4.4.6: T101 repeats the Floor Request, C101 in all, then gives up.
func TestT101(t *testing.T) {
	p, out, c := newParticipant(t)
	p.Join()
	p.Press()
	c.advance(10*time.Second, nil)

	if got := messageTypes(out.take()); !slices.Equal(got, []string{"Request", "Request", "Request"}) {
		t.Fatalf("sent %v", got)
	}

	if p.state != pNoPermission || !slices.Equal(out.notices, []NoticeKind{NoticeRequestTimeout}) {
		t.Errorf("state %d, %v", p.state, out.notices)
	}
}

// §6.2.4.6.2, §6.2.4.6.3: T100 repeats the Floor Release, C100 in all, then the participant has no permission.
func TestT100(t *testing.T) {
	p, out, c := newParticipant(t)
	p.Join()
	p.Press()
	p.Receive(floorctl.Message{Type: floorctl.Granted, GrantedSSRC: ptr[uint32](55)})

	if p.state != pHasPermission || !out.transmit || out.ssrc != 55 {
		t.Fatalf("state %d, transmit %v %d", p.state, out.transmit, out.ssrc)
	}

	out.take()
	p.Unpress()
	c.advance(10*time.Second, nil)

	if got := messageTypes(out.take()); !slices.Equal(got, []string{"Release", "Release", "Release"}) {
		t.Fatalf("sent %v", got)
	}

	if p.state != pNoPermission || out.transmit {
		t.Errorf("state %d, transmit %v", p.state, out.transmit)
	}
}

// §6.2.4.5.4: a Floor Revoke stops the media and releases the floor; §6.2.4.6.4: the Floor Idle ends the release.
func TestRevoke(t *testing.T) {
	p, out, c := newParticipant(t)
	p.Join()
	p.Press()
	p.Receive(floorctl.Message{Type: floorctl.Granted})
	out.take()

	p.Receive(floorctl.Message{Type: floorctl.Revoke, RejectCause: &floorctl.RejectCause{Cause: floorctl.RevokeMediaBurstTooLong}})

	if got := messageTypes(out.take()); !slices.Equal(got, []string{"Release"}) || out.transmit || p.state != pPendingRelease {
		t.Fatalf("sent %v, transmit %v, state %d", got, out.transmit, p.state)
	}

	p.Receive(floorctl.Message{Type: floorctl.Idle})
	c.advance(10*time.Second, nil)

	if p.state != pNoPermission || len(out.take()) != 0 {
		t.Errorf("state %d", p.state)
	}

	if !slices.Equal(out.notices, []NoticeKind{NoticeGranted, NoticeRevoked, NoticeIdle}) {
		t.Errorf("notices %v", out.notices)
	}
}

// §6.2.4.2.2: the messages received before the 200 of an originating call are taken after it; §6.2.4.4.2: the 200's
// mc_granted grants the implicit request, and a later Floor Granted is acknowledged only.
func TestOriginate(t *testing.T) {
	p, out, c := newParticipant(t)
	p.Receive(floorctl.Message{Type: floorctl.Granted, AckRequired: true, GrantedSSRC: ptr[uint32](9)})

	if len(out.take()) != 0 || p.state != pStartStop {
		t.Fatal("answered before the 200")
	}

	p.Originate(Origination{Implicit: true})

	sent := out.take()
	if p.state != pHasPermission || out.ssrc != 9 || len(sent) != 1 || sent[0].Type != floorctl.Ack ||
		*sent[0].MessageType != floorctl.Granted || *sent[0].Source != floorctl.SourceParticipant {
		t.Fatalf("state %d, %+v", p.state, sent)
	}

	c.advance(time.Minute, nil)

	if len(out.take()) != 0 {
		t.Error("T101 running after the grant")
	}

	q, out, _ := newParticipant(t)
	q.Originate(Origination{Implicit: true, Granted: true, AudioSSRC: 42})

	if q.state != pHasPermission || !out.transmit || out.ssrc != 42 {
		t.Errorf("mc_granted: state %d, SSRC %d", q.state, out.ssrc)
	}

	// §6.2.4.5.5, §6.3.4.4.2 1f: the server's Floor Granted after the 200 gives the SSRC to use.
	q.Receive(floorctl.Message{Type: floorctl.Granted, GrantedSSRC: ptr[uint32](43)})

	if out.ssrc != 43 || !out.transmit {
		t.Errorf("SSRC %d after the Floor Granted", out.ssrc)
	}

	r, _, _ := newParticipant(t)
	r.Originate(Origination{})

	if r.state != pNoPermission {
		t.Errorf("without an implicit request: state %d", r.state)
	}
}

// §6.2.4.3.3, §6.2.4.3.6: a Floor Taken, acknowledged if asked, starts T103; its expiry ends the media heard;
// §6.2.4.3.4: media restarts it; §6.2.4.3.2: a Floor Idle stops it.
func TestT103(t *testing.T) {
	p, out, c := newParticipant(t)
	p.Join()
	p.Receive(floorctl.Message{Type: floorctl.Taken, AckRequired: true, GrantedPartyID: "sip:b@x"})

	if sent := out.take(); len(sent) != 1 || *sent[0].MessageType != floorctl.Taken {
		t.Fatalf("%+v", sent)
	}

	c.advance(3*time.Second, nil)
	p.Media()
	c.advance(3*time.Second, nil)

	if !slices.Equal(out.notices, []NoticeKind{NoticeTaken}) {
		t.Fatalf("notices %v", out.notices)
	}

	c.advance(time.Second, nil)

	if !slices.Equal(out.notices, []NoticeKind{NoticeTaken, NoticeMediaEnded}) {
		t.Fatalf("notices %v", out.notices)
	}

	p.Media()
	p.Receive(floorctl.Message{Type: floorctl.Idle})
	c.advance(time.Minute, nil)

	if !slices.Equal(out.notices, []NoticeKind{NoticeTaken, NoticeMediaEnded, NoticeIdle}) {
		t.Errorf("notices %v", out.notices)
	}
}

// §6.2.4.4.11: a Floor Taken while requesting ends the request; §6.2.4.4.8: releasing while requesting.
func TestPendingRequest(t *testing.T) {
	p, out, c := newParticipant(t)
	p.Join()
	p.Press()
	p.Receive(floorctl.Message{Type: floorctl.Taken})
	c.advance(time.Minute, nil)

	if p.state != pNoPermission || len(out.take()) != 1 {
		t.Fatalf("state %d", p.state)
	}

	p.Press()
	p.Unpress()

	if got := messageTypes(out.take()); !slices.Equal(got, []string{"Request", "Release"}) || p.state != pPendingRelease {
		t.Errorf("sent %v, state %d", got, p.state)
	}
}

// §6.2.4.7.2, §6.2.4.8.2: the call released, no messages or media.
func TestParticipantRelease(t *testing.T) {
	p, out, c := newParticipant(t)
	p.Join()
	p.Press()
	p.Receive(floorctl.Message{Type: floorctl.Granted})
	out.take()

	p.Release()
	p.Receive(floorctl.Message{Type: floorctl.Revoke})
	p.Press()
	c.advance(time.Minute, nil)

	if out.transmit || len(out.take()) != 0 || len(c.timers) != 0 {
		t.Errorf("transmit %v, %d timers", out.transmit, len(c.timers))
	}
}

// §6.2.4.2.2 2, §6.2.4.3.3 6: a Floor Taken received before the 200 of a call without an implicit request, or
// joined, is taken after it.
func TestJoinEarly(t *testing.T) {
	for _, join := range []func(*Participant){(*Participant).Join, func(p *Participant) { p.Originate(Origination{}) }} {
		p, out, _ := newParticipant(t)
		p.Receive(floorctl.Message{Type: floorctl.Taken, GrantedPartyID: "sip:b@x", GrantedSSRC: ptr[uint32](7)})
		join(p)

		if p.state != pNoPermission || p.ssrc != 7 || !slices.Equal(out.notices, []NoticeKind{NoticeTaken}) {
			t.Errorf("state %d, SSRC %d, %v", p.state, p.ssrc, out.notices)
		}
	}
}

// §6.2.4.5.4 5a i: the Floor Release after a Floor Revoke has the Floor Indicator, its G-bit 0.
func TestReleaseAfterRevoke(t *testing.T) {
	p, out, _ := newParticipant(t)
	p.Join()
	p.Press()
	p.Receive(floorctl.Message{Type: floorctl.Granted})
	out.take()

	p.Receive(floorctl.Message{Type: floorctl.Revoke, RejectCause: &floorctl.RejectCause{Cause: floorctl.RevokeMediaBurstTooLong}})

	sent := out.take()
	if len(sent) != 1 || sent[0].Indicator == nil || *sent[0].Indicator&floorctl.IndicatorDualFloor != 0 {
		t.Errorf("%+v", sent)
	}

	if _, err := sent[0].App(); err != nil {
		t.Error(err)
	}
}
