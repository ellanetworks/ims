package transaction

import "time"

type Clock interface {
	AfterFunc(d time.Duration, f func()) Timer
}

type Timer interface {
	Stop() bool
}

type SystemClock struct{}

func (SystemClock) AfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}
