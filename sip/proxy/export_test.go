package proxy

import "slices"

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
