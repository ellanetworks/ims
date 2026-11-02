package floor

import (
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
)

type Clock interface {
	AfterFunc(d time.Duration, f func()) Timer
	Now() time.Time
}

type Timer interface {
	Stop() bool
}

type timer struct {
	t     Timer
	until time.Time
}

func (t *timer) start(c Clock, d time.Duration, f func()) {
	t.stop()
	t.t, t.until = c.AfterFunc(d, f), c.Now().Add(d)
}

func (t *timer) stop() {
	if t.t != nil {
		t.t.Stop()
		t.t = nil
	}
}

func (t *timer) running() bool { return t.t != nil }

func (t *timer) fired() { t.t = nil }

func (t *timer) remaining(c Clock) time.Duration {
	if t.t == nil {
		return 0
	}

	return max(t.until.Sub(c.Now()), 0)
}

// TS 24.380 clause 8.2.13
// TS 24.380 clause 8.2.2.1
func ack(msg floorctl.Message, source floorctl.Source, ssrc uint32) (floorctl.Message, bool) {
	if !msg.AckRequired {
		return floorctl.Message{}, false
	}

	t := msg.Type

	return floorctl.Message{Type: floorctl.Ack, SSRC: ssrc, Source: &source, MessageType: &t}, true
}

// TS 24.380 clause 8.2.3.3
func seconds(d time.Duration) uint16 {
	return uint16(min((d+time.Second-1)/time.Second, 0xffff))
}
