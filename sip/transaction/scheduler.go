package transaction

// A Scheduler starts the work the stack does in the background: the outboxes and in-memory lanes of a layer, the
// jobs of Layer.Go, and the strands of the agents on it. Each task runs to completion, concurrently with the others;
// a task may wait for another, as a send in order waits for its outbox. Tests use a scheduler that knows when no task
// is left (siptest.Net).
type Scheduler interface {
	Go(f func())
}

// GoScheduler runs each task on a goroutine of its own.
type GoScheduler struct{}

func (GoScheduler) Go(f func()) {
	go f()
}
