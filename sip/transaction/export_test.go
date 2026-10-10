package transaction

import "github.com/ellanetworks/ims/sip"

// Sent reports whether the transaction has recorded the first send of its request, which its retransmissions wait for.
func (tx *ClientTransaction) Sent() bool {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	return tx.sent
}

// Sending reports whether the layer has a client transaction for the request that has not recorded sending it yet.
func (l *Layer) Sending(req *sip.Request) bool {
	branch, err := branchOf(req)
	if err != nil {
		return false
	}

	tx := l.client(clientKey{branch: branch, method: req.Method})

	return tx != nil && !tx.Sent()
}
