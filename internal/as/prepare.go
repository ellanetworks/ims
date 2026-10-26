package as

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/ellanetworks/ims/sip"
)

// resolveTimeout bounds the DNS queries for the next hop of a request.
const resolveTimeout = 2 * time.Second

// Prepare sends a request of the server to its next hop, the host of its top Route, or else of its Request-URI (RFC
// 3261 §8.1.2, §18.1.1: over TCP if too large for UDP, which the transport does), located by RFC 3263 (sip.Resolver: a
// domain name, such as an S-CSCF's or the PSI host of another core, by its SRV then address records, an IP address as
// it is): to the first of its servers of the family of one of locals, from the first of locals of that family. As the
// S-CSCF does for an AS (TS 24.229 §5.4.1.7), the first server is tried, without the failover of RFC 3263 §4.3.
//
// Prepare runs with the strand of the request held (ua.Config.Prepare), and sip/ua has no hook to locate the next
// hop before it, off the strand: the DNS queries of a domain name block the strand of its call, up to resolveTimeout
// and no later than the end of ctx, the lifetime of the server, after which they fail at once.
func Prepare(ctx context.Context, locals []netip.AddrPort, resolver *sip.Resolver) func(req *sip.Request) error {
	return func(req *sip.Request) error {
		target := req.URI

		switch route, err := req.Header.TopRoute(); {
		case err == nil:
			target = route.URI
		case !errors.Is(err, sip.ErrMissingHeader):
			return err
		}

		ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
		defer cancel()

		servers, err := resolver.Resolve(ctx, target)
		if err != nil {
			return err
		}

		for _, srv := range servers {
			for _, l := range locals {
				if l.Addr().Is4() == srv.Addr.Addr().Is4() {
					req.Flow = sip.Flow{Transport: srv.Transport, Local: l, Remote: srv.Addr}
					req.Header.Prepend("Via", sip.NewVia(srv.Transport, l).String())

					return nil
				}
			}
		}

		return fmt.Errorf("no SIP address of the family of the servers of %s in sip.addresses", target.Host)
	}
}
