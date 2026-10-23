// Package clienttest is the speech of an MCPTT client in the tests and the lab: a speaker of AMR-WB frames of
// silence while the client transmits, in place of a microphone and an encoder.
package clienttest

import (
	"sync"
	"time"

	"github.com/ellanetworks/ims/mcptt/amr"
	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/sip/transaction"
)

// frameDuration is the duration of a frame of AMR-WB (RFC 4867 §4.1).
const frameDuration = 20 * time.Millisecond

// Silence is a frame of mode in the storage format of RFC 4867 §5.3, its speech bits zero.
func Silence(mode int) []byte {
	bits, _ := amr.WB.Bits(uint8(mode))
	b, _ := amr.WB.AppendStorage(nil, amr.Frame{Type: uint8(mode), Good: true, Bits: make([]byte, (bits+7)/8)})

	return b
}

// Speaker sends a frame of silence on each call each 20 ms while the client transmits, from the start of the
// transmission (client.EventTransmit) so that the cadence does not drift with the delays of the timers, its first
// frame at once.
type Speaker struct {
	clock transaction.Clock

	mu    sync.Mutex
	talks map[*client.Call]*talk
}

type talk struct {
	start time.Time
	n     int
	frame []byte
	timer transaction.Timer
}

// NewSpeaker is a speaker on clock, the agent's of the client.
func NewSpeaker(clock transaction.Clock) *Speaker {
	return &Speaker{clock: clock, talks: map[*client.Call]*talk{}}
}

// Event takes an event of the client: its Config.OnEvent passes each.
func (s *Speaker) Event(e client.Event) {
	switch {
	case e.Kind == client.EventTransmit && e.Transmitting:
		s.start(e.Call, e.Mode)
	case e.Kind == client.EventTransmit, e.Kind == client.EventEnded:
		s.stop(e.Call)
	}
}

func (s *Speaker) start(c *client.Call, mode int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.talks[c]; ok {
		return
	}

	t := &talk{start: s.clock.Now(), frame: Silence(mode)}
	s.talks[c] = t

	s.sendLocked(c, t)
}

// sendLocked sends the next frame of t, frame n at 20·n ms from the start, and schedules the one after.
func (s *Speaker) sendLocked(c *client.Call, t *talk) {
	_ = c.SendSpeech(t.frame)
	t.n++

	due := t.start.Add(time.Duration(t.n) * frameDuration)
	t.timer = s.clock.AfterFunc(max(due.Sub(s.clock.Now()), 0), func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		if s.talks[c] == t {
			s.sendLocked(c, t)
		}
	})
}

func (s *Speaker) stop(c *client.Call) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if t, ok := s.talks[c]; ok {
		t.timer.Stop()
		delete(s.talks, c)
	}
}
