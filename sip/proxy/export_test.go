package proxy

import (
	"slices"

	"github.com/ellanetworks/ims/sip/transaction"
)

func (p *Proxy) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.contexts)
}

func (p *Proxy) Responded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, c := range p.contexts {
		c.mu.Lock()
		ok := slices.ContainsFunc(c.branches, func(b *branch) bool { return b.responded })
		c.mu.Unlock()

		if !ok {
			return false
		}
	}

	return len(p.contexts) > 0
}

// ReachAll reaches the branches without a key of every request the proxy forwards (Reached).
func (p *Proxy) ReachAll() {
	p.mu.Lock()

	txs := make([]*transaction.ServerTransaction, 0, len(p.contexts))
	for tx := range p.contexts {
		txs = append(txs, tx)
	}

	p.mu.Unlock()

	for _, tx := range txs {
		p.Reached(tx, nil)
	}
}
