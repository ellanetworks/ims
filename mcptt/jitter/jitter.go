package jitter

import (
	"sync"
	"time"

	"github.com/ellanetworks/ims/mcptt/amr"
)

// RFC 4867 section 4.1
const frameDuration = 20 * time.Millisecond

const (
	DefaultMinDelay = 40 * time.Millisecond
	DefaultMaxDelay = 400 * time.Millisecond

	lateStep = 20 * time.Millisecond
	decay    = 10 * time.Millisecond

	idleFrames = 5

	// RFC 4867 section 4.5
	playedWindow = 64
)

type Config struct {
	Codec amr.Codec

	MinDelay, MaxDelay time.Duration
}

type Stats struct {
	Received, Late, Duplicates, Lost, Dropped int
}

type Buffer struct {
	cfg     Config
	samples uint32
	limit   uint32

	mu        sync.Mutex
	streams   map[uint32]*stream
	talker    uint32
	hasTalker bool
	playing   *stream

	jitter float64
	boost  time.Duration

	stats Stats
}

type stream struct {
	ssrc   uint32
	frames map[uint32]amr.Frame

	started bool
	next    uint32
	played  map[uint32]bool
	order   []uint32

	playing bool
	startAt time.Time
	missing int
	pending int
	last    uint8
	late    int

	refArrival time.Time
	refTS      uint32
	transit    float64
	analysed   bool

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

func (b *Buffer) SetTalker(ssrc uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.talker, b.hasTalker = ssrc, true
}

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

// RFC 3550 section 6.4.1
// RFC 3550 section A.8
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

func (b *Buffer) delayLocked() time.Duration {
	d := frameDuration + time.Duration(4*b.jitter*float64(time.Second)) + b.boost

	return min(max(d, b.cfg.MinDelay), b.cfg.MaxDelay)
}

// TS 26.114 clause 8.2.2
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

// RFC 4867 section 5.3
// TS 26.201 clause 4.1.3
func (b *Buffer) Get(now time.Time) amr.Frame {
	b.mu.Lock()
	defer b.mu.Unlock()

	s := b.playing

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

	if int(s.last) < len(b.cfg.Codec.FrameBits) && b.cfg.Codec.SpeechLost {
		s.pending++

		return amr.Frame{Type: amr.FrameSpeechLost}
	}

	return amr.Frame{Type: amr.FrameNoData, Good: true}
}

func (b *Buffer) preferredLocked() *stream {
	if b.hasTalker {
		if s := b.streams[b.talker]; s != nil && (len(s.frames) > 0 || s.playing) {
			return s
		}
	}

	return nil
}

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

func (b *Buffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()

	clear(b.streams)

	b.playing, b.hasTalker, b.jitter, b.boost = nil, false, 0, 0
}

func (b *Buffer) Delay() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.delayLocked()
}

// RFC 3550 section 6.4.1
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
