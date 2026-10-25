// Package as is where the IMS finds its built-in application servers: the host name each answers to, and the
// addresses it serves on the IMS's in-memory network. A request to a URI of that host, from the I-CSCF or the S-CSCF,
// goes to the built-in AS in memory, without a DNS query or a socket; every other URI resolves as before (RFC 3263),
// an external AS among them. The hosts are the IMS's own, under its home domain, so that no other server takes them.
package as

import (
	"context"
	"strings"
	"sync"

	"github.com/ellanetworks/ims/sip"
)

// Resolver resolves the hosts of the built-in ASs to their in-memory addresses, and the others through DNS.
type Resolver struct {
	// DNS resolves the URIs of no built-in AS.
	DNS *sip.Resolver

	mu    sync.RWMutex
	hosts map[string][]sip.Server
}

// Set has host resolve to servers, the in-memory addresses of a built-in AS; none removes host.
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

// Resolve returns the servers of u: those of the built-in AS of its host, whatever its port and transport, as the
// AS serves every request to its host; or else those DNS gives (sip.Resolver).
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

// Has reports whether host is that of a built-in AS: a PSI subdomain the I-CSCF routes to the AS itself (TS 24.229
// §5.3.2.1 step 3).
func (r *Resolver) Has(host string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.hosts[canonical(host)]) > 0
}

// canonical is host as a key: lower-cased, of no trailing dot of a fully qualified domain name.
func canonical(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}
