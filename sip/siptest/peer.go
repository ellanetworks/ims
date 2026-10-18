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

	// net, for a peer of a Net, is quiesced before the peer looks at what it received.
	net *Net
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

	r, ok := p.RecvWithin(Timeout)
	if !ok {
		p.t.Fatalf("siptest: nothing received on %s", p.addr)
	}

	return r
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

// RecvNone checks that nothing arrives within d; on a Net, that nothing arrived once the net is quiet.
func (p *Peer) RecvNone(d time.Duration) {
	p.t.Helper()

	if p.net != nil {
		p.net.Quiesce()

		select {
		case r := <-p.recv:
			p.t.Fatalf("siptest: unexpected %q on %s", r.Msg.StartLine(), p.addr)
		default:
		}

		return
	}

	select {
	case r := <-p.recv:
		p.t.Fatalf("siptest: unexpected %q on %s", r.Msg.StartLine(), p.addr)
	case <-time.After(d):
	}
}

func (p *Peer) quiesce() {
	p.t.Helper()

	if p.net != nil {
		p.net.Quiesce()
	}
}

func (p *Peer) deliver(m sip.Message) {
	select {
	case p.recv <- Received{Msg: m, Flow: m.Env().Flow}:
	case <-p.done:
	}
}

// RecvWithin takes what the peer received within d, if anything; on a Net, what it received once the net is quiet,
// or else within d.
func (p *Peer) RecvWithin(d time.Duration) (Received, bool) {
	p.t.Helper()
	p.quiesce()

	select {
	case r := <-p.recv:
		return r, true
	default:
	}

	if d <= 0 {
		return Received{}, false
	}

	select {
	case r := <-p.recv:
		return r, true
	case <-time.After(d):
		return Received{}, false
	}
}
