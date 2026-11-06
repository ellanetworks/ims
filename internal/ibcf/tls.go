package ibcf

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transport"
)

const reresolve = 10 * time.Minute

var (
	errNoCertificate = errors.New("ibcf: no TLS certificate to accept connections")
	errNoTLSTrunk    = errors.New("ibcf: TLS peer of no SIP trunk with TLS settings")
)

type peer struct {
	trunk      string
	serverName string
}

type peers struct {
	mu    sync.Mutex
	peers map[netip.AddrPort]peer
}

func newPeers() *peers {
	return &peers{peers: map[netip.AddrPort]peer{}}
}

// RFC 5922 section 7.3
func (p *peers) note(t settings.Trunk, addr settings.TrunkAddress, srv sip.Server) {
	if srv.Transport != sip.TLS {
		return
	}

	name := srv.Domain
	if name == "" {
		if _, err := netip.ParseAddr(addr.Host); err != nil {
			name = addr.Host
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.peers[srv.Addr] = peer{trunk: t.ID, serverName: name}
}

func (p *peers) get(a netip.AddrPort) (peer, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	pr, ok := p.peers[unmap(a)]

	return pr, ok
}

func (b *IBCF) trunkByID(id string) (settings.Trunk, bool) {
	return b.cfg.Settings().Trunk(id)
}

func (b *IBCF) trunkFor(f sip.Flow) (settings.Trunk, bool) {
	if t, ok := b.trunkOf(f.Remote.Addr()); ok {
		return t, true
	}

	if f.Transport != sip.TLS {
		return settings.Trunk{}, false
	}

	if p, ok := b.peers.get(f.Remote); ok {
		return b.trunkByID(p.trunk)
	}

	return settings.Trunk{}, false
}

// SIPconnect 2.0 section 17.2
// RFC 5922 section 7.3
func (b *IBCF) tlsClient(f sip.Flow) (transport.TLSOptions, error) {
	var (
		t    settings.Trunk
		ok   bool
		name string
	)

	if p, known := b.peers.get(f.Remote); known {
		t, ok = b.trunkByID(p.trunk)
		name = p.serverName
	} else {
		t, ok = b.trunkOf(f.Remote.Addr())
	}

	if !ok || !t.TLS.Trusts() {
		return transport.TLSOptions{}, fmt.Errorf("%w: %s", errNoTLSTrunk, f.Remote)
	}

	o, err := t.TLS.ClientOptions(name)
	if err != nil {
		return transport.TLSOptions{}, err
	}

	if c := b.cfg.Certificate; c != nil {
		o.Certificates = []tls.Certificate{*c}
	}

	return o, nil
}

// SIPconnect 2.0 section 17.2
// SIPconnect 2.0 section 17.5
func (b *IBCF) serverConfig(hello *tls.ClientHelloInfo) (*tls.Config, error) {
	if b.cfg.Certificate == nil {
		return nil, errNoCertificate
	}

	remote, err := netip.ParseAddrPort(hello.Conn.RemoteAddr().String())
	if err != nil {
		return nil, err
	}

	t, ok := b.trunkOf(remote.Addr())
	if !ok || !t.TLS.Trusts() {
		return nil, fmt.Errorf("%w: %s", errNoTLSTrunk, remote)
	}

	o, err := t.TLS.InboundOptions()
	if err != nil {
		return nil, err
	}

	o.Certificates = []tls.Certificate{*b.cfg.Certificate}

	return transport.NewTLSServerConfig(o)
}

func (b *IBCF) listenTLS(ctx context.Context, want []netip.AddrPort) error {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, GetConfigForClient: b.serverConfig}

	_, err := b.listen(ctx, want, func(a netip.AddrPort) (netip.AddrPort, error) { return b.agent.Layer().ListenTLS(ctx, a, cfg) },
		func(x *addrs, a netip.AddrPort) { x.secure = append(x.secure, a) })

	return err
}

// SIPconnect 2.0 section 8
// SIPconnect 2.0 section 17.2
func (b *IBCF) transports(t settings.Trunk) []sip.Transport {
	var out []sip.Transport

	switch {
	case t.TLS.Required:
	case t.Trusted:
		out = []sip.Transport{sip.TCP}
	default:
		out = []sip.Transport{sip.UDP, sip.TCP}
	}

	if t.TLS.Trusts() && len(b.ls().secure) > 0 {
		out = append(out, sip.TLS)
	}

	return out
}

func usesTLS(t settings.Trunk) bool {
	return t.TLS.Required || slices.ContainsFunc(t.Addresses, func(a settings.TrunkAddress) bool { return a.Transport == settings.SIPTransportTLS })
}

type persister struct {
	b    *IBCF
	ctx  context.Context
	stop context.CancelFunc
	done chan struct{}
	live map[sip.Flow]func()
}

// SIPconnect 2.0 section 17.2
func (b *IBCF) startPersister() *persister {
	ctx, stop := context.WithCancel(b.life)
	p := &persister{b: b, ctx: ctx, stop: stop, done: make(chan struct{}), live: map[sip.Flow]func(){}}

	go p.run()

	return p
}

func (p *persister) close() {
	p.stop()
	<-p.done
}

func (p *persister) run() {
	defer close(p.done)

	defer func() {
		for _, stop := range p.live {
			stop()
		}
	}()

	tick := time.NewTicker(reresolve)
	defer tick.Stop()

	for {
		var changed <-chan struct{}
		if p.b.cfg.Changed != nil {
			changed = p.b.cfg.Changed()
		}

		p.apply()

		select {
		case <-p.ctx.Done():
			return
		case <-changed:
		case <-tick.C:
		}
	}
}

func (p *persister) apply() {
	want := map[sip.Flow]bool{}

	if p.b.cfg.Certificate == nil && len(p.b.ls().secure) > 0 {
		for _, t := range p.b.cfg.Settings().Trunks {
			if t.Enabled && usesTLS(t) {
				for _, f := range p.flows(t) {
					want[f] = true
				}
			}
		}
	}

	for f, stop := range p.live {
		if !want[f] {
			stop()
			delete(p.live, f)
		}
	}

	for f := range want {
		if p.live[f] != nil {
			continue
		}

		stop, err := p.b.agent.Layer().Persist(f, transport.KeepAlive{})
		if err != nil {
			p.b.log.Warn("no persistent TLS connection to the SIP trunk", slog.String("flow", f.String()), slog.Any("error", err))
			continue
		}

		p.live[f] = stop
	}
}

func (p *persister) flows(t settings.Trunk) []sip.Flow {
	var out []sip.Flow

	for _, a := range t.Addresses {
		if t.TLS.Required || a.Transport == settings.SIPTransportTLS {
			ctx, cancel := context.WithTimeout(p.ctx, resolveTimeout)
			servers, err := p.b.cfg.Resolve(ctx, addressURI(a), []sip.Transport{sip.TLS})

			cancel()

			if err != nil {
				continue
			}

			for _, srv := range servers {
				local, ok := p.b.local(sip.TLS, srv.Addr.Addr())
				if srv.Transport != sip.TLS || !ok {
					continue
				}

				p.b.peers.note(t, a, srv)
				out = append(out, sip.Flow{Transport: sip.TLS, Local: local, Remote: srv.Addr})

				break
			}
		}
	}

	return out
}
