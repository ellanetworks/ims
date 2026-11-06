package ibcf

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"time"

	"github.com/ellanetworks/ims/internal/bgcf"
	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
	"go.opentelemetry.io/otel/trace"
)

const (
	role       = "ibcf"
	tracerName = "github.com/ellanetworks/ims/internal/ibcf"
)

const resolveTimeout = 4 * time.Second

// RFC 3261 section 16.6
const timerC = 3*time.Minute + 10*time.Second

const answerTimeout = 10 * time.Minute

const MaxConnectionsPerSource = 64

// RFC 4028 section 7
// RFC 4028 section 9
// TS 24.229 clause 5.10.2.2
// TS 24.229 clause 5.10.3.2
var sessionTimer = ua.SessionTimer{Interval: 30 * time.Minute, MinSE: 90 * time.Second}

var allow = []string{"INVITE", "ACK", "CANCEL", "BYE", "PRACK", "UPDATE", "OPTIONS", "INFO"}

type Config struct {
	HomeDomain string

	Layer *transaction.Layer

	Internal []netip.AddrPort
	External []netip.AddrPort
	TLS      []netip.AddrPort

	Advertised  map[netip.Addr]netip.Addr
	Certificate *tls.Certificate

	ICSCF []netip.AddrPort
	SCSCF []netip.AddrPort

	Trust    *trust.Domain
	BGCF     *bgcf.BGCF
	Settings func() settings.Settings
	Changed  func() <-chan struct{}
	Resolve  ResolveFunc
	Metrics  *Metrics
	Records  *callrecords.Recorder
	Media    *MediaConfig

	Logger *slog.Logger
}

type IBCF struct {
	cfg      Config
	log      *slog.Logger
	agent    *ua.Agent
	legs     *legs
	peers    *peers
	persist  *persister
	detached tracing.Detached
	life     context.Context
	kill     context.CancelFunc
}

func New(ctx context.Context, cfg Config) (*IBCF, error) {
	switch {
	case cfg.Layer == nil:
		return nil, errors.New("start the IBCF: no transaction layer")
	case len(cfg.Internal) == 0:
		return nil, errors.New("start the IBCF: no internal address")
	case len(cfg.External) == 0:
		return nil, errors.New("start the IBCF: no external address")
	case len(cfg.ICSCF) == 0 || len(cfg.SCSCF) == 0:
		return nil, errors.New("start the IBCF: no I-CSCF or S-CSCF")
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

	cfg.Internal, cfg.External, cfg.TLS = unmapAll(cfg.Internal), unmapAll(cfg.External), unmapAll(cfg.TLS)
	cfg.SCSCF, cfg.ICSCF = unmapAll(cfg.SCSCF), unmapAll(cfg.ICSCF)

	life, kill := context.WithCancel(context.Background())
	b := &IBCF{cfg: cfg, log: logger, legs: newLegs(), peers: newPeers(), life: life, kill: kill}
	b.detached = tracing.NewDetached(cfg.Layer.TracerProvider(), tracerName, role)

	agent, err := ua.New(ua.Config{
		Layer:        cfg.Layer,
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

	b.agent = agent

	if changed := b.configure(); changed != nil {
		go b.watch(changed)
	}

	b.persist = b.startPersister()

	logger.InfoContext(ctx, "IBCF started", slog.Any("internal", cfg.Internal), slog.Any("external", cfg.External),
		slog.Any("tls", cfg.TLS), slog.Bool("certificate", cfg.Certificate != nil), slog.Any("advertised", cfg.Advertised))

	return b, nil
}

func unmapAll(as []netip.AddrPort) []netip.AddrPort {
	out := make([]netip.AddrPort, len(as))
	for i, a := range as {
		out[i] = unmap(a)
	}

	return out
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

func (b *IBCF) Internal() []netip.AddrPort { return slices.Clone(b.cfg.Internal) }

func (b *IBCF) External() []netip.AddrPort { return slices.Clone(b.cfg.External) }

func (b *IBCF) TLS() []netip.AddrPort { return slices.Clone(b.cfg.TLS) }

func (b *IBCF) Handler() transaction.Handler { return b.agent }

func (b *IBCF) SpanLinks(req *sip.Request) []trace.SpanContext { return b.agent.SpanLinks(req) }

// TS 24.229 clause 4.4.1
func (b *IBCF) TraceContext(f sip.Flow) bool {
	return b.isInternal(f.Local) && b.cfg.Trust.Trusted(f.Remote.Addr())
}

func (b *IBCF) Shutdown(ctx context.Context) error {
	defer b.kill()

	if b.persist != nil {
		b.persist.close()
	}

	return b.agent.Shutdown(ctx)
}

func (b *IBCF) isInternal(a netip.AddrPort) bool {
	return slices.Contains(b.cfg.Internal, unmap(a))
}

func (b *IBCF) isExternal(a netip.AddrPort) bool {
	return slices.Contains(b.cfg.External, unmap(a)) || slices.Contains(b.cfg.TLS, unmap(a))
}

func (b *IBCF) locals(tr sip.Transport) []netip.AddrPort {
	if tr == sip.TLS {
		return b.cfg.TLS
	}

	return b.cfg.External
}

func (b *IBCF) local(tr sip.Transport, remote netip.Addr) (netip.AddrPort, bool) {
	return pick(b.locals(tr), remote)
}

// RFC 3261 section 18.1.1
// RFC 3261 section 18.2.1
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
	return trunkAt(b.cfg.Settings(), a)
}

func trunkAt(st settings.Settings, a netip.Addr) (settings.Trunk, bool) {
	a = a.Unmap()

	for _, t := range st.Trunks {
		if slices.ContainsFunc(t.InboundCIDRs, func(p netip.Prefix) bool { return p.Contains(a) }) {
			return t, true
		}
	}

	return settings.Trunk{}, false
}

var errForbidden = errors.New("ibcf: request from an unknown source")

var errUnknownSource = errors.New("ibcf: request from no SIP trunk")

func (b *IBCF) Filter(m sip.Message) error {
	req, ok := m.(*sip.Request)
	if !ok {
		return nil
	}

	err := b.admit(req)
	if err == nil {
		err = req.Validate()
	}

	if err == nil {
		err = screen(req)
	}

	if err == nil {
		b.noteEnding(req)
	}

	return err
}

func (b *IBCF) noteEnding(req *sip.Request) {
	if req.Method != "BYE" && req.Method != "CANCEL" {
		return
	}

	if l := b.legs.received(req); l != nil && (req.Method != "CANCEL" || l.call.cancels(req)) {
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
			b.cfg.Layer.CloseFlow(f)
		}

		return err
	}

	return nil
}

// TS 24.229 clause 4.4.1
// TS 24.229 clause 5.10.1
func (b *IBCF) Accept(local, remote netip.AddrPort) bool {
	switch {
	case b.isInternal(local):
		return b.cfg.Trust.Trusted(remote.Addr())
	case b.isExternal(local):
		if _, ok := b.trunkOf(remote.Addr()); ok {
			return true
		}

		return b.legs.resolved(remote.Addr())
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
		return disabled(req)
	}

	return nil
}

var errDisabled = errors.New("ibcf: request from a disabled SIP trunk")

// TS 24.229 clause 5.10.1
func disabled(req *sip.Request) error {
	if req.Method == "ACK" {
		return errDisabled
	}

	return &sip.StatusError{StatusCode: 403, Err: errDisabled}
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
