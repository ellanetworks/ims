// Package floor is the floor control of MCPTT on-network (TS 24.380 §6): the floor control server of the
// controlling function (§6.3), its arbitration logic and its interface towards each floor participant, and the floor
// participant of a client (§6.2.4). Basic operation only: no queueing, pre-emption, dual floor, multi-talker,
// non-controlling function or temporary group session.
//
// The machines are pure: their inputs are method calls, all made on one strand, the call's; their outputs are calls
// of an Out interface; their timers run on a Clock that fires on that strand. They neither block nor read the time
// but from the Clock.
package floor

import (
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
)

// Clock is the clock of the machines' timers: AfterFunc runs f on the call's strand, as ua.Strand.AfterFunc does,
// unless stopped first.
type Clock interface {
	AfterFunc(d time.Duration, f func()) Timer
	Now() time.Time
}

// Timer is a timer of a Clock.
type Timer interface {
	Stop() bool
}

// timer is a timer of a machine, nil when not running.
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

// fired marks t expired, from its function.
func (t *timer) fired() { t.t = nil }

// remaining is the time left on t.
func (t *timer) remaining(c Clock) time.Duration {
	if t.t == nil {
		return 0
	}

	return max(t.until.Sub(c.Now()), 0)
}

// ack is the Floor Ack of msg from source, if msg asks for one (§8.2.13, §8.2.2.1).
func ack(msg floorctl.Message, source floorctl.Source, ssrc uint32) (floorctl.Message, bool) {
	if !msg.AckRequired {
		return floorctl.Message{}, false
	}

	t := msg.Type

	return floorctl.Message{Type: floorctl.Ack, SSRC: ssrc, Source: &source, MessageType: &t}, true
}

// seconds is d in whole seconds for the Duration field (§8.2.3.3), rounded up and capped to 16 bits.
func seconds(d time.Duration) uint16 {
	return uint16(min((d+time.Second-1)/time.Second, 0xffff))
}
