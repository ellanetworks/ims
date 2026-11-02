package as

import (
	"context"
	"strings"
	"sync"

	"github.com/ellanetworks/ims/sip"
)

type Resolver struct {
	DNS *sip.Resolver

	mu    sync.RWMutex
	hosts map[string][]sip.Server
}

func (r *Resolver) Set(host string, servers []sip.Server) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.hosts == nil {
		r.hosts = make(map[string][]sip.Server)
	}

	host = canonical(host)

	if len(servers) == 0 {
		delete(r.hosts, host)
		return
	}

	r.hosts[host] = servers
}

func (r *Resolver) Resolve(ctx context.Context, u sip.URI) ([]sip.Server, error) {
	if u.IsSIP() && !u.IsSIPS() {
		r.mu.RLock()
		servers := r.hosts[canonical(u.Host)]
		r.mu.RUnlock()

		if len(servers) > 0 {
			return servers, nil
		}
	}

	return r.DNS.Resolve(ctx, u)
}

// TS 24.229 clause 5.3.2.1
func (r *Resolver) Has(host string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.hosts[canonical(host)]) > 0
}

func canonical(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}
