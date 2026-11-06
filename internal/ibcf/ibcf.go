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
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/internal/bgcf"
	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

const (
	role       = "ibcf"
	tracerName = "github.com/ellanetworks/ims/internal/ibcf"
)

const resolveTimeout = 4 * time.Second

// RFC 3261 section 16.6
const timerC = 3*time.Minute + 10*time.Second

const answerTimeout = 10 * time.Minute

const maxConnectionsPerSource = 64

// RFC 4028 section 7
// RFC 4028 section 9
// TS 24.229 clause 5.10.2.2
// TS 24.229 clause 5.10.3.2
var sessionTimer = ua.SessionTimer{Interval: 30 * time.Minute, MinSE: 90 * time.Second}

var allow = []string{"INVITE", "ACK", "CANCEL", "BYE", "PRACK", "UPDATE", "OPTIONS", "INFO"}

type Config struct {
	HomeDomain string

	Internal []netip.AddrPort
	External []netip.AddrPort
	TLS      []netip.AddrPort

	Advertised  map[netip.Addr]netip.Addr
	Certificate *tls.Certificate

	ICSCF []netip.AddrPort

	Trust    *trust.Domain
	BGCF     *bgcf.BGCF
	Settings func() settings.Settings
	Changed  func() <-chan struct{}
	Resolve  ResolveFunc
	Metrics  *Metrics
	Records  *callrecords.Recorder
	Media    *MediaConfig

	Transaction transaction.Config

	Logger *slog.Logger
}

type IBCF struct {
	cfg      Config
	log      *slog.Logger
	agent    *ua.Agent
	amu      sync.Mutex
	addrs    atomic.Pointer[addrs]
	legs     *legs
	peers    *peers
	persist  *persister
	detached tracing.Detached
	life     context.Context
	kill     context.CancelFunc
}

func Start(ctx context.Context, cfg Config) (*IBCF, error) {
	return start(ctx, cfg, true)
}

func start(ctx context.Context, cfg Config, bind bool) (_ *IBCF, err error) {
	switch {
	case len(cfg.Internal) == 0:
		return nil, errors.New("start the IBCF: no internal address")
	case len(cfg.External) == 0:
		return nil, errors.New("start the IBCF: no external address")
	case len(cfg.ICSCF) == 0:
		return nil, errors.New("start the IBCF: no I-CSCF")
	case cfg.HomeDomain == "":
		return nil, errors.New("start the IBCF: no home domain")
	case cfg.BGCF == nil || cfg.Settings == nil || cfg.Trust == nil:
		return nil, errors.New("start the IBCF: no BGCF, settings or trust domain")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	logger = logger.With(slog.String("role", role))

	if cfg.Resolve == nil {
		cfg.Resolve = SystemResolve
	}

	life, kill := context.WithCancel(context.Background())
	b := &IBCF{cfg: cfg, log: logger, legs: newLegs(), peers: newPeers(), life: life, kill: kill}
	b.addrs.Store(&addrs{})

	tc := cfg.Transaction
	tc.Logger = logger
	tc.Filter = b.filter
	tc.OnServerDone = b.logTransaction
	tc.TraceContext = func(f sip.Flow) bool { return b.isInternal(f.Local) && cfg.Trust.Trusted(f.Remote.Addr()) }
	tc.SpanAttributes = b.spanAttributes
	tc.Untraced = b.untraced
	b.detached = tracing.NewDetached(tc.TracerProvider, tracerName, role)
	tc.Transport.TLSClientOptions = b.tlsClient
	tc.Transport.Accept = b.accept

	if tc.Transport.MaxConnectionsPerAddress == 0 {
		tc.Transport.MaxConnectionsPerAddress = maxConnectionsPerSource
	}

	for _, a := range cfg.Advertised {
		tc.Aliases = append(tc.Aliases, a.String())
	}

	b.agent, err = ua.New(ua.Config{
		Transaction:  tc,
		Prepare:      b.prepare,
		Contact:      contact(cfg.Internal[0], sip.UDP),
		Allow:        allow,
		SessionTimer: sessionTimer,
		Policy:       ua.Policy{AnswerTimeout: answerTimeout},
		Logger:       logger,
		OnInvite:     b.onInvite,
		OnRequest:    b.onRequest,
	})
	if err != nil {
		kill()
		return nil, err
	}

	defer func() {
		if err != nil {
			err = errors.Join(err, b.Shutdown(ctx))
		}
	}()

	changed := b.configure()
	if changed != nil {
		go b.watch(changed)
	}

	if !bind {
		b.addrs.Store(&addrs{internal: cfg.Internal, external: cfg.External})

		for _, a := range append(slices.Clone(cfg.Internal), cfg.External...) {
			if err := b.agent.Layer().Serve(a); err != nil {
				return nil, fmt.Errorf("serve the IBCF in memory: %w", err)
			}
		}

		return b, nil
	}

	internal, err := b.listen(ctx, cfg.Internal, func(a netip.AddrPort) (netip.AddrPort, error) { return b.agent.Layer().Listen(ctx, a) },
		func(x *addrs, a netip.AddrPort) { x.internal = append(x.internal, a) })
	if err != nil {
		return nil, err
	}

	for _, a := range internal {
		if err := b.agent.Layer().Serve(a); err != nil {
			return nil, fmt.Errorf("serve the IBCF in memory: %w", err)
		}
	}

	if _, err := b.listen(ctx, cfg.External, func(a netip.AddrPort) (netip.AddrPort, error) { return b.agent.Layer().Listen(ctx, a) },
		func(x *addrs, a netip.AddrPort) { x.external = append(x.external, a) }); err != nil {
		return nil, err
	}

	if err := b.listenTLS(ctx, cfg.TLS); err != nil {
		return nil, err
	}

	b.persist = b.startPersister()

	x := b.ls()
	logger.InfoContext(ctx, "IBCF started", slog.Any("internal", x.internal), slog.Any("external", x.external),
		slog.Any("tls", x.secure), slog.Bool("certificate", cfg.Certificate != nil), slog.Any("advertised", cfg.Advertised))

	return b, nil
}

type addrs struct {
	internal, external, secure []netip.AddrPort
}

func (b *IBCF) ls() *addrs {
	return b.addrs.Load()
}

func (b *IBCF) listen(_ context.Context, want []netip.AddrPort, bind func(netip.AddrPort) (netip.AddrPort, error),
	add func(*addrs, netip.AddrPort),
) ([]netip.AddrPort, error) {
	out := make([]netip.AddrPort, 0, len(want))

	var port uint16

	for _, a := range want {
		if a.Port() != 0 || port == 0 {
			port = a.Port()
		}

		bound, err := bind(netip.AddrPortFrom(a.Addr().Unmap(), port))
		if err != nil {
			return nil, err
		}

		b.amu.Lock()
		next := *b.ls()
		next.internal, next.external, next.secure = slices.Clone(next.internal), slices.Clone(next.external), slices.Clone(next.secure)
		add(&next, bound)
		b.addrs.Store(&next)
		b.amu.Unlock()

		port = bound.Port()
		out = append(out, bound)
	}

	return out, nil
}

func (b *IBCF) configure() <-chan struct{} {
	var changed <-chan struct{}
	if b.cfg.Changed != nil {
		changed = b.cfg.Changed()
	}

	trunks := b.cfg.Settings().Trunks
	ids := make([]string, 0, len(trunks))

	for _, t := range trunks {
		ids = append(ids, t.ID)
	}

	b.cfg.Metrics.configure(ids)

	if b.cfg.Media != nil && b.cfg.Media.Gateway != nil {
		b.cfg.Media.Gateway.Configure(ids)
	}

	return changed
}

func (b *IBCF) watch(changed <-chan struct{}) {
	for {
		select {
		case <-b.life.Done():
			return
		case <-changed:
			changed = b.configure()
		}
	}
}

func (b *IBCF) Internal() []netip.AddrPort { return slices.Clone(b.ls().internal) }

func (b *IBCF) External() []netip.AddrPort { return slices.Clone(b.ls().external) }

func (b *IBCF) TLS() []netip.AddrPort { return slices.Clone(b.ls().secure) }

func (b *IBCF) Shutdown(ctx context.Context) error {
	defer b.kill()

	if b.persist != nil {
		b.persist.close()
	}

	return b.agent.Shutdown(ctx)
}

func (b *IBCF) isInternal(a netip.AddrPort) bool {
	return slices.Contains(b.ls().internal, unmap(a))
}

func (b *IBCF) isExternal(a netip.AddrPort) bool {
	x := b.ls()

	return slices.Contains(x.external, unmap(a)) || slices.Contains(x.secure, unmap(a))
}

func (b *IBCF) locals(tr sip.Transport) []netip.AddrPort {
	if tr == sip.TLS {
		return b.ls().secure
	}

	return b.ls().external
}

func (b *IBCF) local(tr sip.Transport, remote netip.Addr) (netip.AddrPort, bool) {
	return pick(b.locals(tr), remote)
}

// RFC 3261 section 18.2.1
// RFC 3581 section 4
func (b *IBCF) advertise(local netip.AddrPort) netip.AddrPort {
	if a, ok := b.cfg.Advertised[local.Addr().Unmap()]; ok && b.isExternal(local) {
		return netip.AddrPortFrom(a, local.Port())
	}

	return local
}

func unmap(a netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
}

func (b *IBCF) trunkOf(a netip.Addr) (settings.Trunk, bool) {
	a = a.Unmap()

	for _, t := range b.cfg.Settings().Trunks {
		if slices.ContainsFunc(t.InboundCIDRs, func(p netip.Prefix) bool { return p.Contains(a) }) {
			return t, true
		}
	}

	return settings.Trunk{}, false
}

var errForbidden = errors.New("ibcf: request from an unknown source")

var errUnknownSource = errors.New("ibcf: request from no SIP trunk")

func (b *IBCF) filter(m sip.Message) error {
	req, ok := m.(*sip.Request)
	if !ok {
		return nil
	}

	err := b.admit(req)
	if err == nil {
		b.noteEnding(req)
	}

	return err
}

func (b *IBCF) noteEnding(req *sip.Request) {
	if req.Method != "BYE" && req.Method != "CANCEL" {
		return
	}

	if l := b.legs.received(req); l != nil {
		l.call.ending(l.trunk, req.Method)
	}
}

// TS 24.229 clause 4.4.1
// TS 24.229 clause 5.10.1
func (b *IBCF) admit(req *sip.Request) error {
	f := req.Flow

	switch {
	case b.isInternal(f.Local):
		if b.cfg.Trust.Trusted(f.Remote.Addr()) {
			return nil
		}

		return b.forbid(req)
	case b.isExternal(f.Local):
		err := b.fromTrunk(req)
		if errors.Is(err, errUnknownSource) && f.Transport != sip.UDP {
			b.agent.Layer().CloseFlow(f)
		}

		return err
	}

	return nil
}

// TS 24.229 clause 4.4.1
// TS 24.229 clause 5.10.1
func (b *IBCF) accept(local, remote netip.AddrPort) bool {
	switch {
	case b.isInternal(local):
		return b.cfg.Trust.Trusted(remote.Addr())
	case b.isExternal(local):
		_, ok := b.trunkOf(remote.Addr())
		return ok
	}

	return false
}

func (b *IBCF) fromTrunk(req *sip.Request) error {
	f := req.Flow
	remote := f.Remote.Addr()

	if l := b.legs.received(req); l != nil && l.trunk {
		t, known := l.call.owner()

		if p, ok := b.peers.get(f.Remote); ok && f.Transport == sip.TLS && p.trunk == t.ID {
			known = append(known, remote)
		}

		switch {
		case !inTrunk(t, known, remote):
			if _, ok := b.trunkFor(f); ok {
				return b.forbid(req)
			}

			return errUnknownSource
		case !b.allowed(t, f.Transport):
			return b.forbid(req)
		}

		return nil
	}

	t, ok := b.trunkFor(f)

	switch {
	case !ok:
		return errUnknownSource
	case !b.allowed(t, f.Transport):
		return b.forbid(req)
	case !t.Enabled && !inDialog(req):
		return b.forbid(req)
	}

	return nil
}

// SIPconnect 2.0 section 17.2
func (b *IBCF) allowed(t settings.Trunk, tr sip.Transport) bool {
	switch {
	case t.TLS.Required:
		return tr == sip.TLS
	case t.Trusted:
		return tr != sip.UDP
	}

	return true
}

func (b *IBCF) forbid(req *sip.Request) error {
	b.log.Debug("SIP request refused", slog.String("method", req.Method), slog.String("source", req.Flow.Remote.String()),
		slog.String("local", req.Flow.Local.String()))

	return &sip.StatusError{StatusCode: 403, Err: errForbidden}
}

func inDialog(req *sip.Request) bool {
	to, err := req.Header.To()
	return err == nil && to.Tag() != ""
}

func (b *IBCF) onInvite(ctx context.Context, r *ua.Request) ua.Handler {
	if b.isExternal(r.Message().Flow.Local) {
		b.entry(ctx, r)
	} else {
		b.exit(ctx, r)
	}

	return ua.Handler{}
}

func (b *IBCF) onRequest(_ context.Context, r *ua.Request) {
	if r.Message().Method == "OPTIONS" {
		b.options(r)
		return
	}

	b.notAllowed(r)
}

// RFC 3261 section 11.2
// SIPconnect 2.0 section 17.4
func (b *IBCF) options(r *ua.Request) {
	_ = r.Respond(ua.Reply{Code: 200, Header: b.agent.Capabilities()})
}

// SIPconnect 2.0 section 5
func (b *IBCF) notAllowed(r *ua.Request) {
	_ = r.Reject(405, sip.Field{Name: "Allow", Value: b.agent.Capabilities().Get("Allow")})
}

func (b *IBCF) logTransaction(ctx context.Context, req *sip.Request, res *sip.Response, elapsed time.Duration) {
	level := slog.LevelDebug

	switch req.Method {
	case "INVITE", "BYE", "CANCEL", "UPDATE":
		level = slog.LevelInfo
	}

	if !b.log.Enabled(ctx, level) {
		return
	}

	attrs := []slog.Attr{
		slog.String("method", req.Method),
		slog.String("call_id", req.Header.CallID()),
		slog.String("remote", req.Flow.Remote.String()),
		slog.Duration("duration", elapsed),
	}

	if res == nil {
		b.log.LogAttrs(ctx, level, "SIP transaction ended without a final response", attrs...)
		return
	}

	attrs = append(attrs, slog.Int("status", res.StatusCode), slog.String("reason", res.Reason))
	b.log.LogAttrs(ctx, level, "SIP transaction", attrs...)
}

// RFC 3261 section 26.2.2
// SIPconnect 2.0 section 8.1
func (b *IBCF) contact(local netip.AddrPort, tr sip.Transport) sip.Address {
	return contact(b.advertise(local), tr)
}

func contact(local netip.AddrPort, tr sip.Transport) sip.Address {
	u := sip.URI{Scheme: "sip", Host: sip.FormatHost(local.Addr()), Port: local.Port()}
	if tr == sip.TCP || tr == sip.TLS {
		u.Params.Set("transport", "tcp")
	}

	return sip.Address{URI: u}
}
