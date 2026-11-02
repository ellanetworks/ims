package transaction

type Scheduler interface {
	Go(f func())
}

type GoScheduler struct{}

func (GoScheduler) Go(f func()) {
	go f()
}
