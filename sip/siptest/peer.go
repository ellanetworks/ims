package siptest

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

type Peer struct {
	t     testing.TB
	layer *transaction.Layer
	addr  netip.AddrPort
	recv  chan Received
	done  chan struct{}
}

func NewPeer(t testing.TB, l *transaction.Layer, addr netip.AddrPort) *Peer {
	t.Helper()

	p := &Peer{t: t, layer: l, addr: addr, recv: make(chan Received, 256), done: make(chan struct{})}

	detach, err := l.Attach(addr, p.deliver)
	if err != nil {
		t.Fatalf("siptest: attach a peer on %s: %v", addr, err)
	}

	t.Cleanup(func() {
		detach()
		close(p.done)
	})

	return p
}

func (p *Peer) Addr() netip.AddrPort { return p.addr }

func (p *Peer) Send(tr sip.Transport, to netip.AddrPort, m sip.Message) {
	p.t.Helper()

	m.Env().Flow = sip.Flow{Transport: tr, Local: p.addr, Remote: to}

	if err := p.layer.SendOnFlow(context.Background(), m); err != nil {
		p.t.Fatalf("siptest: send from %s to %s: %v", p.addr, to, err)
	}
}

func (p *Peer) Recv() Received {
	p.t.Helper()

	select {
	case r := <-p.recv:
		return r
	case <-time.After(Timeout):
		p.t.Fatalf("siptest: nothing received on %s", p.addr)
		return Received{}
	}
}

func (p *Peer) RecvRequest() (*sip.Request, sip.Flow) {
	p.t.Helper()

	r := p.Recv()

	req, ok := r.Msg.(*sip.Request)
	if !ok {
		p.t.Fatalf("siptest: got %q on %s, want a request", r.Msg.StartLine(), p.addr)
	}

	return req, r.Flow
}

func (p *Peer) RecvResponse() (*sip.Response, sip.Flow) {
	p.t.Helper()

	r := p.Recv()

	res, ok := r.Msg.(*sip.Response)
	if !ok {
		p.t.Fatalf("siptest: got %q on %s, want a response", r.Msg.StartLine(), p.addr)
	}

	return res, r.Flow
}

func (p *Peer) RecvNone(d time.Duration) {
	p.t.Helper()

	select {
	case r := <-p.recv:
		p.t.Fatalf("siptest: unexpected %q on %s", r.Msg.StartLine(), p.addr)
	case <-time.After(d):
	}
}

func (p *Peer) deliver(m sip.Message) {
	select {
	case p.recv <- Received{Msg: m, Flow: m.Env().Flow}:
	case <-p.done:
	}
}
