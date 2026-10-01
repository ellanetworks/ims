package transaction

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestUnlockInOrderWaitsForAnotherDrainer(t *testing.T) {
	var tx core
	tx.init(nil, Trying)

	started, release := make(chan struct{}), make(chan struct{})

	tx.mu.Lock()
	tx.emit(func() {
		close(started)
		<-release
	})
	tx.mu.Unlock()

	go tx.drain()

	<-started

	var ran atomic.Bool

	returned := make(chan struct{})

	go func() {
		tx.mu.Lock()
		tx.emit(func() { ran.Store(true) })
		tx.unlockInOrder()
		close(returned)
	}()

	select {
	case <-returned:
		t.Fatal("unlockInOrder returned before its callback ran")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	<-returned

	if !ran.Load() {
		t.Fatal("callback did not run")
	}
}

func TestTimerEReachesT2(t *testing.T) {
	tests := []struct{ t1, t2, want time.Duration }{
		{500 * time.Millisecond, 4 * time.Second, 3500 * time.Millisecond},
		{time.Second, time.Second, time.Second},
		{100 * time.Millisecond, 4 * time.Second, 6300 * time.Millisecond},
	}

	for _, tt := range tests {
		if got := timerEReachesT2(tt.t1, tt.t2); got != tt.want {
			t.Errorf("timerEReachesT2(%s, %s) = %s, want %s", tt.t1, tt.t2, got, tt.want)
		}
	}
}
