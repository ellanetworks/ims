package ibcf

import (
	"context"
	"fmt"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

type sent struct {
	res *sip.Response
	err error
}

type sender chan sent

func (s sender) HandleResponse(_ context.Context, res *sip.Response) {
	if res.StatusCode >= 200 {
		s.done(sent{res: res})
	}
}

func (s sender) HandleError(_ context.Context, err error) {
	s.done(sent{err: err})
}

func (s sender) done(r sent) {
	select {
	case s <- r:
	default:
	}
}

// RFC 3261 section 8.1.2
// RFC 3263 section 4.3
// SIPconnect 2.0 section 17.4
func (b *IBCF) Send(ctx context.Context, t settings.Trunk, a settings.TrunkAddress, req *sip.Request, srv sip.Server) (*sip.Response, error) {
	if !b.noteServer(t, a, srv) {
		return nil, fmt.Errorf("%w: %s", settings.ErrSharedServer, srv.Addr)
	}

	local, ok := b.local(srv.Transport, srv.Addr.Addr())
	if !ok {
		return nil, fmt.Errorf("ibcf: no external address of the family of %s", srv.Addr)
	}

	out := req.Clone()
	out.Flow = sip.Flow{Transport: srv.Transport, Local: local, Remote: srv.Addr}
	out.Header.Prepend("Via", sip.NewVia(srv.Transport, b.advertise(local)).String())

	if !out.Header.Has("Contact") {
		out.Header.Set("Contact", b.contact(local, srv.Transport).String())
	}

	done := make(sender, 1)

	if b.Untraced(out) {
		ctx = transaction.WithoutSpan(ctx)
	}

	if _, err := b.cfg.Layer.Request(ctx, out, done); err != nil {
		return nil, err
	}

	select {
	case r := <-done:
		return r.res, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
