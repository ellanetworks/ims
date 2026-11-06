package as

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/ellanetworks/ims/sip"
)

const resolveTimeout = 2 * time.Second

// RFC 3261 section 8.1.2
// RFC 3261 section 18.1.1
// RFC 3263
func Prepare(ctx context.Context, locals []netip.AddrPort, resolver *sip.Resolver) func(context.Context, *sip.Request) error {
	return func(_ context.Context, req *sip.Request) error {
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
