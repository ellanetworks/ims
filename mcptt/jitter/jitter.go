// Package jitter is the de-jitter buffer of the speech a client hears, with the functions TS 26.114 §8.2.2 requires
// of a jitter buffer management: frames of source-controlled rate (DTX), out of order or duplicated (max-red, RFC 4867
// §4.5) presented once and in order, the clock drift of the talker absorbed. Its delay is the least that keeps the
// frames in time (§8.2.3.1 guideline 1), adapted between talkspurts, the frame-based adaptation of §8.2.1: from the
// interarrival jitter of RFC 3550 §6.4.1, and raised after frames that came too late (guideline 2).
//
// It keeps a stream per SSRC: a talker (RFC 3550 §3) whose frames are its own timeline. The client names the talker
// of the floor (TS 24.380 §8.2.3.6, Floor Taken), whose stream plays; another plays while the talker's has nothing,
// the previous talker's tail drained.
package jitter

import (
	"sync"
	"time"

	"github.com/ellanetworks/ims/mcptt/amr"
)

// frameDuration is the duration of a frame of AMR and AMR-WB (RFC 4867 §4.1).
const frameDuration = 20 * time.Millisecond

const (
	// DefaultMinDelay and DefaultMaxDelay bound the delay of a Buffer configured without.
	DefaultMinDelay = 40 * time.Millisecond
	DefaultMaxDelay = 400 * time.Millisecond

	// lateStep is how much frames too late in a talkspurt raise the delay of the next ones, and decay is how much
	// each talkspurt without one takes off again.
	lateStep = 20 * time.Millisecond
	decay    = 10 * time.Millisecond

	// idleFrames are the frames missing, none buffered, after which a talkspurt is over.
	idleFrames = 5

	// playedWindow are the frames played a stream remembers, to tell a redundant copy (RFC 4867 §4.5) from a frame
	// too late.
	playedWindow = 64
)

type Config struct {
	Codec amr.Codec

	// MinDelay and MaxDelay bound the buffering delay: DefaultMinDelay and DefaultMaxDelay when zero.
	MinDelay, MaxDelay time.Duration
}

// Stats are the counts of a Buffer: frames received, late (after their playout), duplicated (received twice, or
// again once played), lost (missing between frames of a talkspurt), and dropped to keep the buffer within MaxDelay.
type Stats struct {
	Received, Late, Duplicates, Lost, Dropped int
}

// Buffer is a de-jitter buffer: Put gives it the frames of each RTP packet as they arrive, Get takes the next 20 ms
// frame for the decoder. Its methods may be called from any goroutine.
type Buffer struct {
	cfg     Config
	samples uint32
	// limit is MaxDelay in timestamp units.
	limit uint32

	mu      sync.Mutex
	streams map[uint32]*stream
	// talker is the SSRC of the floor's talker, its stream preferred; playing the stream playing.
	talker    uint32
	hasTalker bool
	playing   *stream

	// The network analyser (RFC 3550 §6.4.1), of the path from the server, which the talkers share: the jitter in
	// seconds; boost is the delay late frames added.
	jitter float64
	boost  time.Duration

	stats Stats
}

// stream is the frames of one SSRC.
type stream struct {
	ssrc   uint32
	frames map[uint32]amr.Frame

	// started is set once a frame played: next is then the timestamp of the frame to play next, frames before it
	// late or duplicated; played the timestamps played last, in a window.
	started bool
	next    uint32
	played  map[uint32]bool
	order   []uint32

	// playing is set while a talkspurt plays; startAt is when the talkspurt buffered starts; missing the frames
	// missing in a row, pending those of them given SPEECH_LOST, lost if the talkspurt goes on; last the type of the
	// frame given last; late the frames too late in the talkspurt.
	playing bool
	startAt time.Time
	missing int
	pending int
	last    uint8
	late    int

	// refArrival and refTS are the first packet of the stream, transit and analysed the transit of the last one.
	refArrival time.Time
	refTS      uint32
	transit    float64
	analysed   bool

	// seen is when a packet of the stream arrived last.
	seen time.Time
}

func New(cfg Config) *Buffer {
	if cfg.MinDelay <= 0 {
		cfg.MinDelay = DefaultMinDelay
	}

	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = DefaultMaxDelay
	}

	samples := cfg.Codec.ClockRate / 50

	return &Buffer{
		cfg: cfg, samples: samples, limit: uint32(cfg.MaxDelay/frameDuration) * samples, streams: map[uint32]*stream{},
	}
}

// SetTalker names the SSRC of the talker of the floor (TS 24.380 §8.2.3.6), whose speech plays before any other's.
func (b *Buffer) SetTalker(ssrc uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.talker, b.hasTalker = ssrc, true
}

// Put adds the frames of an RTP packet of ssrc and timestamp ts, the first frame's, that arrived at arrival.
func (b *Buffer) Put(ssrc uint32, ts uint32, frames []amr.Frame, arrival time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s := b.streams[ssrc]
	if s == nil {
		s = &stream{ssrc: ssrc, frames: map[uint32]amr.Frame{}, played: map[uint32]bool{}, last: amr.FrameNoData}
		b.streams[ssrc] = s
	}

	s.seen = arrival
	b.analyseLocked(s, ts, arrival)

	for i, f := range frames {
		fts := ts + uint32(i)*b.samples

		if f.Type == amr.FrameNoData {
			continue
		}

		b.stats.Received++

		switch _, dup := s.frames[fts]; {
		case dup, s.played[fts]:
			b.stats.Duplicates++
		case s.started && int32(fts-s.next) < 0:
			b.stats.Late++
			s.late++
		default:
			s.frames[fts] = f
		}
	}

	b.trimLocked(s)

	if !s.playing && s.startAt.IsZero() && len(s.frames) > 0 {
		s.startAt = arrival.Add(b.delayLocked())
	}

	b.expireLocked(arrival)
}

// analyseLocked updates the interarrival jitter with a packet of timestamp ts of s that arrived at arrival (RFC 3550
// §6.4.1, A.8).
func (b *Buffer) analyseLocked(s *stream, ts uint32, arrival time.Time) {
	if s.refArrival.IsZero() {
		s.refArrival, s.refTS = arrival, ts
	}

	transit := arrival.Sub(s.refArrival).Seconds() - float64(int32(ts-s.refTS))/float64(b.cfg.Codec.ClockRate)

	if s.analysed {
		d := transit - s.transit
		if d < 0 {
			d = -d
		}

		b.jitter += (d - b.jitter) / 16
	}

	s.transit, s.analysed = transit, true
}

// delayLocked is the buffering delay of the next talkspurt: a frame, four times the jitter, and what late frames
// added, within the bounds.
func (b *Buffer) delayLocked() time.Duration {
	d := frameDuration + time.Duration(4*b.jitter*float64(time.Second)) + b.boost

	return min(max(d, b.cfg.MinDelay), b.cfg.MaxDelay)
}

// trimLocked keeps the frames of s within MaxDelay of its newest, playing or not: the talker's clock faster than the
// decoder's, or nothing taking the frames out (TS 26.114 §8.2.2: clock drift).
func (b *Buffer) trimLocked(s *stream) {
	if len(s.frames) == 0 {
		return
	}

	newest, first := uint32(0), true
	for ts := range s.frames {
		if first || int32(ts-newest) > 0 {
			newest, first = ts, false
		}
	}

	floor := newest - b.limit + b.samples

	for ts := range s.frames {
		if int32(ts-floor) < 0 {
			delete(s.frames, ts)

			b.stats.Dropped++
		}
	}

	if s.started && int32(floor-s.next) > 0 {
		s.next = floor
	}
}

// expireLocked forgets the streams heard from no more, idle for twice MaxDelay with nothing to play.
func (b *Buffer) expireLocked(now time.Time) {
	for ssrc, s := range b.streams {
		if len(s.frames) == 0 && !s.playing && now.Sub(s.seen) > 2*b.cfg.MaxDelay {
			delete(b.streams, ssrc)

			if b.playing == s {
				b.playing = nil
			}
		}
	}
}

// Get is the frame to decode for the next 20 ms at now: the next of the talkspurt playing; SPEECH_LOST for a missing
// speech frame, which the decoder conceals; NO_DATA in silence, between SID frames, or with nothing to play (RFC 4867
// §5.3; TS 26.201 Table 1c).
func (b *Buffer) Get(now time.Time) amr.Frame {
	b.mu.Lock()
	defer b.mu.Unlock()

	s := b.playing

	// The talker of the floor waits: the stream playing, its frames all out, gives way.
	if p := b.preferredLocked(); s != nil && s.playing && p != nil && p != s && len(s.frames) == 0 {
		b.endTalkspurtLocked(s)
	}

	if s = b.playing; s == nil || !s.playing {
		s = b.startLocked(now)
		if s == nil {
			return amr.Frame{Type: amr.FrameNoData, Good: true}
		}
	}

	f, ok := s.frames[s.next]
	ts := s.next
	s.next += b.samples

	if ok {
		delete(s.frames, ts)
		s.markPlayed(ts)

		b.stats.Lost += s.pending
		s.missing, s.pending, s.last = 0, 0, f.Type

		return f
	}

	s.missing++

	if len(s.frames) == 0 && s.missing >= idleFrames {
		b.endTalkspurtLocked(s)

		return amr.Frame{Type: amr.FrameNoData, Good: true}
	}

	// A speech frame missing: lost if the talkspurt goes on, the tail of its end otherwise.
	if int(s.last) < len(b.cfg.Codec.FrameBits) && b.cfg.Codec.SpeechLost {
		s.pending++

		return amr.Frame{Type: amr.FrameSpeechLost}
	}

	return amr.Frame{Type: amr.FrameNoData, Good: true}
}

// preferredLocked is the stream to play: the talker's, if it has frames or plays, else any.
func (b *Buffer) preferredLocked() *stream {
	if b.hasTalker {
		if s := b.streams[b.talker]; s != nil && (len(s.frames) > 0 || s.playing) {
			return s
		}
	}

	return nil
}

// startLocked starts the talkspurt to play at now: the talker's, or the stream buffered longest, once its delay
// passed; started later than due by more than the delay, nothing taking the frames out meanwhile, at most the delay
// behind its newest frame.
func (b *Buffer) startLocked(now time.Time) *stream {
	s := b.preferredLocked()

	if s == nil {
		for _, c := range b.streams {
			if len(c.frames) > 0 && (s == nil || c.startAt.Before(s.startAt)) {
				s = c
			}
		}
	}

	if s == nil || len(s.frames) == 0 || now.Before(s.startAt) {
		return nil
	}

	oldest, newest, first := uint32(0), uint32(0), true
	for ts := range s.frames {
		if first || int32(ts-oldest) < 0 {
			oldest = ts
		}

		if first || int32(ts-newest) > 0 {
			newest = ts
		}

		first = false
	}

	start, delay := oldest, b.delayLocked()
	if back := uint32(delay/frameDuration) * b.samples; now.Sub(s.startAt) > delay && int32(newest-oldest) > int32(back) {
		start = newest - back
	}

	for ts := range s.frames {
		if int32(ts-start) < 0 {
			delete(s.frames, ts)

			b.stats.Dropped++
		}
	}

	s.playing, s.started, s.next, s.missing, s.pending, s.late = true, true, start, 0, 0, 0
	b.playing = s

	return s
}

// endTalkspurtLocked ends the talkspurt of s, adapting the delay of the next one; its frames before next stay late.
func (b *Buffer) endTalkspurtLocked(s *stream) {
	s.playing, s.startAt, s.last, s.pending = false, time.Time{}, amr.FrameNoData, 0

	if s.late > 0 {
		b.boost = min(b.boost+lateStep, b.cfg.MaxDelay)
	} else {
		b.boost = max(b.boost-decay, 0)
	}

	if b.playing == s {
		b.playing = nil
	}
}

func (s *stream) markPlayed(ts uint32) {
	s.played[ts] = true
	s.order = append(s.order, ts)

	if len(s.order) > playedWindow {
		delete(s.played, s.order[0])
		s.order = s.order[1:]
	}
}

// Reset empties the buffer and forgets what it learnt of the network: for another call.
func (b *Buffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()

	clear(b.streams)

	b.playing, b.hasTalker, b.jitter, b.boost = nil, false, 0, 0
}

// Delay is the buffering delay the buffer starts the next talkspurt with.
func (b *Buffer) Delay() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.delayLocked()
}

// Jitter is the interarrival jitter of the path from the server (RFC 3550 §6.4.1), in its estimate now.
func (b *Buffer) Jitter() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	return time.Duration(b.jitter * float64(time.Second))
}

func (b *Buffer) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.stats
}
