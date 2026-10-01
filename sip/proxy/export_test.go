package proxy

func (p *Proxy) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.contexts)
}
