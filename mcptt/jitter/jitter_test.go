package jitter

import (
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/amr"
)

const samples = 320

var t0 = time.Unix(1_800_000_000, 0)

// speech is a 12.65 kbit/s frame of AMR-WB, its first octet n.
func speech(n int) amr.Frame {
	bits, _ := amr.WB.Bits(2)
	b := make([]byte, (bits+7)/8)
	b[0] = byte(n)

	return amr.Frame{Type: 2, Good: true, Bits: b}
}

func newBuffer() *Buffer { return New(Config{Codec: amr.WB}) }

// play takes n frames from b, one each 20 ms from at: the first octet of each speech frame, -1 for SPEECH_LOST, -2
// for NO_DATA.
func play(b *Buffer, at time.Time, n int) []int {
	out := make([]int, n)

	for i := range out {
		switch f := b.Get(at.Add(time.Duration(i) * frameDuration)); f.Type {
		case amr.FrameSpeechLost:
			out[i] = -1
		case amr.FrameNoData:
			out[i] = -2
		default:
			out[i] = int(f.Bits[0])
		}
	}

	return out
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// A talkspurt in time plays in order after the buffering delay, then silence.
func TestInOrder(t *testing.T) {
	b := newBuffer()

	for i := range 5 {
		b.Put(1, uint32(i*samples), []amr.Frame{speech(i)}, t0.Add(time.Duration(i)*frameDuration))
	}

	if got := play(b, t0, 2); !equal(got, []int{-2, -2}) {
		t.Fatalf("before the delay %v, want NO_DATA", got)
	}

	if got := play(b, t0.Add(b.Delay()), 5); !equal(got, []int{0, 1, 2, 3, 4}) {
		t.Fatalf("played %v", got)
	}
}

// TS 26.114 §8.2.2: frames out of order and duplicated play once, in order.
func TestReorderedAndDuplicated(t *testing.T) {
	b := newBuffer()

	for _, i := range []int{0, 2, 1, 1, 3, 2} {
		b.Put(1, uint32(i*samples), []amr.Frame{speech(i)}, t0)
	}

	if got := play(b, t0.Add(b.Delay()), 4); !equal(got, []int{0, 1, 2, 3}) {
		t.Fatalf("played %v", got)
	}

	if s := b.Stats(); s.Duplicates != 2 || s.Received != 6 {
		t.Fatalf("stats %+v", s)
	}
}

// A missing speech frame with later ones buffered is SPEECH_LOST, for the decoder to conceal (RFC 4867 §5.3).
func TestLoss(t *testing.T) {
	b := newBuffer()

	for _, i := range []int{0, 1, 3} {
		b.Put(1, uint32(i*samples), []amr.Frame{speech(i)}, t0)
	}

	if got := play(b, t0.Add(b.Delay()), 4); !equal(got, []int{0, 1, -1, 3}) {
		t.Fatalf("played %v", got)
	}

	if s := b.Stats(); s.Lost != 1 {
		t.Fatalf("stats %+v", s)
	}
}

// A packet of several frames (maxptime, RFC 4867 §4.3.5.2) plays a frame each 20 ms; its NO_DATA frames are DTX.
func TestFramesOfAPacket(t *testing.T) {
	b := newBuffer()
	sid := amr.Frame{Type: amr.WB.SID, Good: true, Bits: make([]byte, 5)}

	b.Put(1, 0, []amr.Frame{speech(0), speech(1), sid, {Type: amr.FrameNoData}}, t0)

	got := play(b, t0.Add(b.Delay()), 4)
	if got[0] != 0 || got[1] != 1 || got[3] != -2 {
		t.Fatalf("played %v, want the speech, the SID, then NO_DATA", got)
	}
}

// A frame after its playout is late, and the next talkspurt starts with a longer delay (TS 26.114 §8.2.3.1
// guideline 2).
func TestLateRaisesTheDelay(t *testing.T) {
	b := newBuffer()
	before := b.Delay()

	b.Put(1, 0, []amr.Frame{speech(0)}, t0)
	play(b, t0.Add(time.Second), 3)
	b.Put(1, samples, []amr.Frame{speech(1)}, t0.Add(time.Second+60*time.Millisecond))
	play(b, t0.Add(2*time.Second), idleFrames+1)

	if s := b.Stats(); s.Late != 1 {
		t.Fatalf("stats %+v", s)
	}

	if after := b.Delay(); after <= before {
		t.Fatalf("delay %s after a late frame, was %s", after, before)
	}
}

// The interarrival jitter (RFC 3550 §6.4.1) raises the delay, within MaxDelay.
func TestJitterRaisesTheDelay(t *testing.T) {
	b := newBuffer()

	for i := range 100 {
		offset := time.Duration(i%2) * 60 * time.Millisecond
		b.Put(1, uint32(i*samples), []amr.Frame{speech(i)}, t0.Add(time.Duration(i)*frameDuration+offset))
	}

	if d := b.Delay(); d < 150*time.Millisecond || d > DefaultMaxDelay {
		t.Fatalf("delay %s with 60 ms of jitter", d)
	}
}

// Another SSRC is another talker, a stream of its own: the talker of the floor plays first; the previous talker's tail
// is not wiped by the new one, nor the new one's by a late packet of the previous (RFC 3550 §3: one SSRC, one
// timeline).
func TestTalkers(t *testing.T) {
	b := newBuffer()
	b.SetTalker(2)

	b.Put(1, 0, []amr.Frame{speech(1)}, t0)
	b.Put(2, 9_000_000, []amr.Frame{speech(2), speech(3)}, t0)
	b.Put(1, samples, []amr.Frame{speech(4)}, t0)

	if got := play(b, t0.Add(b.Delay()), 2); !equal(got, []int{2, 3}) {
		t.Fatalf("played %v, want the talker's frames", got)
	}

	if got := play(b, t0.Add(b.Delay()+200*time.Millisecond), idleFrames+2); got[idleFrames] != 1 || got[idleFrames+1] != 4 {
		t.Fatalf("played %v, want the previous talker's frames once the talker's talkspurt ended", got)
	}
}

// Two talkers (dual floor, TS 24.380 §6.2.4.5.7): their packets interleaved, the talker of the floor plays whole.
func TestInterleavedTalkers(t *testing.T) {
	b := newBuffer()
	b.SetTalker(7)

	for i := range 10 {
		b.Put(7, uint32(i*samples), []amr.Frame{speech(i)}, t0.Add(time.Duration(i)*frameDuration))
		b.Put(8, uint32(5_000_000+i*samples), []amr.Frame{speech(100 + i)}, t0.Add(time.Duration(i)*frameDuration))
	}

	if got := play(b, t0.Add(b.Delay()), 10); !equal(got, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("played %v", got)
	}
}

// A frame of a talkspurt ended, arriving after it, is late: the next talkspurt does not start from it.
func TestStragglerAfterTheTalkspurt(t *testing.T) {
	b := newBuffer()

	for i := range 3 {
		b.Put(1, uint32(i*samples), []amr.Frame{speech(i)}, t0)
	}

	play(b, t0.Add(b.Delay()), 3+idleFrames)

	b.Put(1, 3*samples, []amr.Frame{speech(3)}, t0.Add(time.Second))

	at := t0.Add(2 * time.Second)
	for i := range 3 {
		b.Put(1, uint32((100+i)*samples), []amr.Frame{speech(10 + i)}, at)
	}

	if got := play(b, at.Add(b.Delay()), 3); !equal(got, []int{10, 11, 12}) {
		t.Fatalf("played %v, want the new talkspurt alone", got)
	}

	if s := b.Stats(); s.Late != 1 {
		t.Fatalf("stats %+v, want the straggler late", s)
	}
}

// Nothing taking the frames out, the buffer keeps MaxDelay of them, and playout starts the delay behind the newest.
func TestNotPlaying(t *testing.T) {
	b := newBuffer()

	for i := range 3000 {
		b.Put(1, uint32(i*samples), []amr.Frame{speech(i % 250)}, t0.Add(time.Duration(i)*frameDuration))
	}

	now := t0.Add(60 * time.Second)
	got := play(b, now, int(b.Delay()/frameDuration)+2)

	if want := 2999 % 250; got[len(got)-2] != want || got[len(got)-1] != -1 {
		t.Fatalf("played %v, want the last %s, the newest frame %d last", got, b.Delay(), want)
	}
}

// Redundant copies (RFC 4867 §4.5) of frames played are duplicates, not late; a talkspurt ending without SID loses
// no frame; NO_DATA has its quality bit set (TS 26.201 Table 1c).
func TestCopiesAndEnds(t *testing.T) {
	b := newBuffer()

	for i := range 3 {
		b.Put(1, uint32(i*samples), []amr.Frame{speech(i)}, t0)
	}

	play(b, t0.Add(b.Delay()), 2)
	b.Put(1, 0, []amr.Frame{speech(0), speech(1)}, t0)

	ends := play(b, t0.Add(b.Delay()+time.Second), 1+idleFrames)
	if ends[0] != 2 {
		t.Fatalf("played %v", ends)
	}

	if s := b.Stats(); s.Duplicates != 2 || s.Late != 0 || s.Lost != 0 {
		t.Fatalf("stats %+v, want two duplicates, none late or lost", s)
	}

	if f := b.Get(t0.Add(time.Hour)); f.Type != amr.FrameNoData || !f.Good {
		t.Fatalf("idle frame %+v, want NO_DATA of quality 1", f)
	}
}

// A talker whose clock runs ahead keeps the buffer within MaxDelay, dropping its oldest frames.
func TestOverflow(t *testing.T) {
	b := New(Config{Codec: amr.WB, MaxDelay: 100 * time.Millisecond})

	b.Put(1, 0, []amr.Frame{speech(0)}, t0)
	play(b, t0.Add(b.Delay()), 1)

	for i := 1; i <= 20; i++ {
		b.Put(1, uint32(i*samples), []amr.Frame{speech(i)}, t0.Add(b.Delay()))
	}

	if s := b.Stats(); s.Dropped == 0 {
		t.Fatalf("stats %+v, want frames dropped", s)
	}

	if got := play(b, t0.Add(b.Delay()), 1); got[0] < 15 {
		t.Fatalf("played frame %d, want one of the last 100 ms", got[0])
	}
}
