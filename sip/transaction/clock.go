package transaction

import "time"

// Clock is the clock of the timers of the stack: tests run it on fake time.
type Clock interface {
	AfterFunc(d time.Duration, f func()) Timer

	// Now is the current time: deadlines that outlive a timer, such as the expiry of a subscription a NOTIFY
	// moves, are kept as times.
	Now() time.Time
}

type Timer interface {
	Stop() bool
}

type SystemClock struct{}

func (SystemClock) AfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}

func (SystemClock) Now() time.Time {
	return time.Now()
}
