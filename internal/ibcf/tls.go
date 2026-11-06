package ibcf

import (
	"bytes"
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
	address    settings.TrunkAddress
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
func (p *peers) note(t settings.Trunk, addr settings.TrunkAddress, srv sip.Server) bool {
	if srv.Transport != sip.TLS {
		return true
	}

	name := srv.Domain
	if name == "" {
		if _, err := netip.ParseAddr(addr.Host); err != nil {
			name = addr.Host
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	a := unmap(srv.Addr)

	if old, ok := p.peers[a]; ok && old.trunk != t.ID {
		return false
	}

	p.peers[a] = peer{trunk: t.ID, address: addr, serverName: name}

	return true
}

func (b *IBCF) noteServer(t settings.Trunk, addr settings.TrunkAddress, srv sip.Server) {
	if !b.peers.note(t, addr, srv) {
		b.log.Warn("SIP trunks share a TLS server, keeping the first", slog.String("trunk", t.Name), slog.String("server", srv.Addr.String()))
	}
}

func (p *peers) prune(trunks []settings.Trunk) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for a, pr := range p.peers {
		i := slices.IndexFunc(trunks, func(t settings.Trunk) bool { return t.ID == pr.trunk })
		if i < 0 || !trunks[i].TLS.Trusts() || !slices.Contains(trunks[i].Addresses, pr.address) {
			delete(p.peers, a)
		}
	}
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

	bind := func(a netip.AddrPort) (netip.AddrPort, error) { return b.agent.Layer().ListenTLS(ctx, a, cfg) }
	if b.cfg.Certificate == nil {
		bind = func(a netip.AddrPort) (netip.AddrPort, error) { return b.agent.Layer().ClientTLS(ctx, a) }
	}

	_, err := b.listen(ctx, want, bind, func(x *addrs, a netip.AddrPort) { x.secure = append(x.secure, a) })

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

func (b *IBCF) Transports(t settings.Trunk) []sip.Transport {
	return b.transports(t)
}

func usesTLS(t settings.Trunk, a settings.TrunkAddress) bool {
	return t.TLS.Required || a.Transport == settings.SIPTransportTLS
}

type persistKey struct {
	trunk   string
	address settings.TrunkAddress
}

type persistent struct {
	tls      settings.TrunkTLS
	interval time.Duration
	stop     func()
}

type persister struct {
	b    *IBCF
	ctx  context.Context
	stop context.CancelFunc
	done chan struct{}
	live map[persistKey]persistent
}

// SIPconnect 2.0 section 17.2
func (b *IBCF) startPersister() *persister {
	ctx, stop := context.WithCancel(b.life)
	p := &persister{b: b, ctx: ctx, stop: stop, done: make(chan struct{}), live: map[persistKey]persistent{}}

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
		for _, c := range p.live {
			c.stop()
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

// SIPconnect 2.0 section 17.2
// SIPconnect 2.0 section 17.4
func (p *persister) apply() {
	trunks := p.b.cfg.Settings().Trunks
	want := map[persistKey]settings.Trunk{}

	if p.b.cfg.Certificate == nil && len(p.b.ls().secure) > 0 {
		for _, t := range trunks {
			if !t.Enabled || !t.TLS.Trusts() {
				continue
			}

			for _, a := range t.Addresses {
				if usesTLS(t, a) {
					want[persistKey{trunk: t.ID, address: a}] = t
				}
			}
		}
	}

	for k, c := range p.live {
		if t, ok := want[k]; !ok || !sameTLS(c.tls, t.TLS) || c.interval != keepAliveInterval(t) {
			c.stop()
			delete(p.live, k)
		}
	}

	p.b.peers.prune(trunks)

	for k, t := range want {
		if _, ok := p.live[k]; ok {
			continue
		}

		ka := transport.KeepAlive{Interval: keepAliveInterval(t), PongTimeout: transport.DefaultPongTimeout}

		stop, err := p.b.agent.Layer().Persist(p.targets(t, k.address), ka)
		if err != nil {
			p.b.log.Warn("no persistent TLS connection to the SIP trunk", slog.String("trunk", t.Name),
				slog.String("address", k.address.String()), slog.Any("error", err))

			continue
		}

		p.live[k] = persistent{tls: t.TLS, interval: ka.Interval, stop: stop}
	}
}

// SIPconnect 2.0 section 17.4
func keepAliveInterval(t settings.Trunk) time.Duration {
	if t.KeepaliveInterval > 0 {
		return t.KeepaliveInterval
	}

	return transport.DefaultKeepAliveInterval
}

func sameTLS(a, b settings.TrunkTLS) bool {
	return a.Required == b.Required && slices.Equal(a.Pins, b.Pins) && slices.Equal(a.Identities, b.Identities) &&
		slices.EqualFunc(a.CACertificates, b.CACertificates, bytes.Equal)
}

// SIPconnect 2.0 section 17.2
// RFC 3263 section 4.3
func (p *persister) targets(t settings.Trunk, a settings.TrunkAddress) transport.Targets {
	return func(ctx context.Context) ([]sip.Flow, error) {
		ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
		defer cancel()

		servers, err := p.b.resolveVia(ctx, addressURI(a), []sip.Transport{sip.TLS})
		if err != nil {
			return nil, err
		}

		var out []sip.Flow

		for _, srv := range servers {
			local, ok := p.b.local(sip.TLS, srv.Addr.Addr())
			if srv.Transport != sip.TLS || !ok {
				continue
			}

			p.b.noteServer(t, a, srv)
			out = append(out, sip.Flow{Transport: sip.TLS, Local: local, Remote: srv.Addr})
		}

		return out, nil
	}
}
