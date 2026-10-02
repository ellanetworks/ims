package proxy

func (p *Proxy) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.contexts)
}

// Responded reports whether the branch of every pending context got a
// response.
func (p *Proxy) Responded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, c := range p.contexts {
		c.mu.Lock()
		ok := c.branch != nil && c.branch.responded
		c.mu.Unlock()

		if !ok {
			return false
		}
	}

	return len(p.contexts) > 0
}
