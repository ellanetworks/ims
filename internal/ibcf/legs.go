package ibcf

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/lifetime"
)

type key struct {
	callID string
	tag    string
	ours   bool
}

type leg struct {
	call  *call
	trunk bool

	mu      sync.Mutex
	flow    sip.Flow
	server  string
	targets map[string]sip.Flow
}

func (l *leg) serverName(remote netip.AddrPort) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.server, l.server != "" && unmap(l.flow.Remote) == unmap(remote)
}

func (l *leg) target(u sip.URI) (sip.Flow, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	f, ok := l.targets[u.String()]

	return f, ok
}

func (l *leg) setTarget(u sip.URI, f sip.Flow) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.targets == nil {
		l.targets = map[string]sip.Flow{}
	}

	l.targets[u.String()] = f
}

func (l *leg) dialogFlow() sip.Flow {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.flow
}

func (l *leg) setFlow(f sip.Flow, server string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.flow, l.server = f, server
}

type legs struct {
	mu   sync.Mutex
	legs map[key]*leg
}

func newLegs() *legs {
	return &legs{legs: map[key]*leg{}}
}

func (r *legs) add(k key, l *leg) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if old, ok := r.legs[k]; ok && old.call != l.call && !old.call.isEnded() {
		return false
	}

	r.legs[k] = l
	l.call.keys = append(l.call.keys, k)

	return true
}

func (r *legs) get(k key) *leg {
	if k.tag == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.legs[k]
}

func (r *legs) remove(c *call) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, k := range c.keys {
		if r.legs[k] != nil && r.legs[k].call == c {
			delete(r.legs, k)
		}
	}
}

func (r *legs) resolved(a netip.Addr) bool {
	r.mu.Lock()

	calls := map[*call]bool{}

	for _, l := range r.legs {
		if l.trunk && l.call.route != nil {
			calls[l.call] = true
		}
	}

	r.mu.Unlock()

	for c := range calls {
		if t, known := c.owner(); !c.isEnded() && inTrunk(t, known, a) {
			return true
		}
	}

	return false
}

func tags(req *sip.Request) (string, string) {
	from, _ := req.Header.From()
	to, _ := req.Header.To()

	return from.Tag(), to.Tag()
}

// RFC 3261 section 12
func (r *legs) received(req *sip.Request) *leg {
	from, to := tags(req)
	id := req.Header.CallID()

	if l := r.get(key{id, to, true}); l != nil {
		return l
	}

	return r.get(key{id, from, false})
}

func (r *legs) sending(req *sip.Request) *leg {
	from, to := tags(req)
	id := req.Header.CallID()

	if l := r.get(key{id, from, true}); l != nil {
		return l
	}

	return r.get(key{id, to, false})
}

var errNoLeg = errors.New("ibcf: request of no call leg")

// RFC 3261 section 8.1.2
// RFC 3261 section 12.2.1.1
// SIPconnect 2.0 section 17.1.1.2
func (b *IBCF) prepare(ctx context.Context, req *sip.Request) error {
	l := b.legs.sending(req)
	if l == nil {
		return errNoLeg
	}

	f := l.dialogFlow()

	if target, ok := dialogTarget(req); ok {
		var err error

		if l.trunk {
			f = b.trunkFlow(ctx, l, f, target)
		} else if f, err = b.internalFlow(target); err != nil {
			return err
		}
	}

	if !f.Remote.IsValid() {
		return fmt.Errorf("ibcf: no destination for %s", req.StartLine())
	}

	req.Flow = f
	req.Header.Prepend("Via", sip.NewVia(f.Transport, b.advertise(f.Local)).String())

	return nil
}

func (b *IBCF) internalFlow(target sip.URI) (sip.Flow, error) {
	tr, dest, err := sip.Destination(target)
	if err != nil {
		return sip.Flow{}, err
	}

	local, ok := pick(b.ls().internal, dest.Addr())
	if !ok {
		return sip.Flow{}, fmt.Errorf("ibcf: no internal listener of the family of %s", dest)
	}

	return sip.Flow{Transport: tr, Local: local, Remote: dest}, nil
}

// SIPconnect 2.0 section 17.1.1.2
func (b *IBCF) trunkFlow(ctx context.Context, l *leg, dialog sip.Flow, u sip.URI) sip.Flow {
	if dialog.Transport != sip.UDP && dialog.Remote.IsValid() {
		return dialog
	}

	if f, ok := l.target(u); ok {
		return f
	}

	t, known := l.call.owner()

	ctx, cancel := context.WithTimeout(lifetime.With(b.life, ctx), resolveTimeout)
	defer cancel()

	servers, err := b.resolveTrunk(ctx, t, u)
	if err != nil {
		return dialog
	}

	for _, srv := range servers {
		if !inTrunk(t, known, srv.Addr.Addr()) {
			continue
		}

		if local, ok := b.local(srv.Transport, srv.Addr.Addr()); ok {
			f := sip.Flow{Transport: srv.Transport, Local: local, Remote: srv.Addr}
			l.setTarget(u, f)

			return f
		}
	}

	return dialog
}

func inTrunk(t settings.Trunk, known []netip.Addr, a netip.Addr) bool {
	a = a.Unmap()

	return slices.Contains(known, a) || slices.ContainsFunc(t.InboundCIDRs, func(p netip.Prefix) bool { return p.Contains(a) })
}

func dialogTarget(req *sip.Request) (sip.URI, bool) {
	if route, err := req.Header.TopRoute(); err == nil {
		return route.URI, true
	}

	if inDialog(req) {
		return req.URI, true
	}

	return sip.URI{}, false
}

func pick(locals []netip.AddrPort, remote netip.Addr) (netip.AddrPort, bool) {
	for _, l := range locals {
		if l.Addr().Is4() == remote.Unmap().Is4() {
			return l, true
		}
	}

	return netip.AddrPort{}, false
}
