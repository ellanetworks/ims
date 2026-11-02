package transaction

import "github.com/ellanetworks/ims/sip"

func (tx *ClientTransaction) Sent() bool {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	return tx.sent
}

func (l *Layer) Sending(req *sip.Request) bool {
	branch, err := branchOf(req)
	if err != nil {
		return false
	}

	tx := l.client(clientKey{branch: branch, method: req.Method})

	return tx != nil && !tx.Sent()
}

func (tx *ServerTransaction) Sending() bool {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	return tx.sending > 0
}

func (l *Layer) HoldFlow(f sip.Flow) {
	l.enqueue(f, func(bool) { <-l.ctx.Done() })
}
