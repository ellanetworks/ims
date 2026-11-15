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

func (l *Layer) Responding(res *sip.Response) bool {
	via, err := res.Header.TopVia()
	if err != nil {
		return false
	}

	cseq, err := res.Header.CSeq()
	if err != nil {
		return false
	}

	tx := l.server(serverKey{branchKey: branchKey{branch: via.Branch(), sentBy: sentByKey(via)}, method: cseq.Method})

	return tx != nil && tx.Sending()
}

func (tx *ServerTransaction) Sending() bool {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	return tx.sending > 0
}

func (l *Layer) HoldFlow(f sip.Flow) {
	l.enqueue(f, func(bool) { <-l.ctx.Done() })
}
