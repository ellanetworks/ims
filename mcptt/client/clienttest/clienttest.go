package clienttest

import (
	"sync"
	"time"

	"github.com/ellanetworks/ims/mcptt/amr"
	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/sip/transaction"
)

// RFC 4867 section 4.1
const frameDuration = 20 * time.Millisecond

// RFC 4867 section 5.3
func Silence(mode int) []byte {
	bits, _ := amr.WB.Bits(uint8(mode))
	b, _ := amr.WB.AppendStorage(nil, amr.Frame{Type: uint8(mode), Good: true, Bits: make([]byte, (bits+7)/8)})

	return b
}

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

func NewSpeaker(clock transaction.Clock) *Speaker {
	return &Speaker{clock: clock, talks: map[*client.Call]*talk{}}
}

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
