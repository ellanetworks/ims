package ipsmgw

import (
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/as"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const Port = config.IPSMGWPort

func Host(homeDomain string) string { return "ipsmgw." + homeDomain }

type Store interface {
	IPSMGWRegistrations(ctx context.Context) ([]db.IPSMGWRegistration, error)
	PutIPSMGWRegistration(ctx context.Context, r db.IPSMGWRegistration) error
	DeleteIPSMGWRegistration(ctx context.Context, impuKey string) error
	IPSMGWHSSRegistrations(ctx context.Context) ([]db.IPSMGWHSSRegistration, error)
	PutIPSMGWHSSRegistration(ctx context.Context, r db.IPSMGWHSSRegistration) error
	DeleteIPSMGWHSSRegistration(ctx context.Context, userKey string) error
}

type Config struct {
	Addresses   []netip.Addr
	Transaction transaction.Config
	HomeDomain  string
	PLMN        string
	Store       Store
	HSS         HSS
	SMSCs       SMSCs
	Router      Router
	Nodes       Nodes
	Metrics     *Metrics
	Number      func() string
	Logger      *slog.Logger
}

type AS struct {
	agent   *ua.Agent
	reg     *registrar
	hss     *hssSync
	kill    context.CancelFunc
	life    context.Context
	work    sync.WaitGroup
	locals  []netip.AddrPort
	trusted []netip.Prefix
	smscs   SMSCs
	router  Router
	nodes   Nodes
	number  func() string
	home    string
	plmn    string
	corr    correlations
	pending pendingReports
	metrics *Metrics
	log     *slog.Logger
}

func Start(ctx context.Context, cfg Config) (_ *AS, err error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	logger = logger.With(slog.String("role", "ip-sm-gw"))

	switch {
	case len(cfg.Addresses) == 0:
		return nil, errors.New("start the IP-SM-GW: no SIP address to listen on")
	case cfg.HomeDomain == "":
		return nil, errors.New("start the IP-SM-GW: no home domain to name it under")
	case cfg.PLMN == "":
		return nil, errors.New("start the IP-SM-GW: no PLMN")
	}

	rows, err := cfg.Store.IPSMGWRegistrations(ctx)
	if err != nil {
		return nil, err
	}

	told, err := cfg.Store.IPSMGWHSSRegistrations(ctx)
	if err != nil {
		return nil, err
	}

	a := &AS{
		log: logger, smscs: cfg.SMSCs, router: cfg.Router, nodes: cfg.Nodes, number: cfg.Number, home: cfg.HomeDomain,
		plmn: cfg.PLMN, metrics: cfg.Metrics,
	}

	for _, addr := range cfg.Addresses {
		a.locals = append(a.locals, netip.AddrPortFrom(addr, Port))
		a.trusted = append(a.trusted, netip.PrefixFrom(addr, addr.BitLen()))
	}

	life, kill := context.WithCancel(context.Background())
	a.kill, a.life = kill, life

	g := &registrar{
		store: cfg.Store, home: cfg.HomeDomain, log: logger, life: life, seed: maphash.MakeSeed(),
		regs: map[string]*registration{}, dirty: map[string]bool{},
	}
	a.reg = g

	tc := cfg.Transaction
	tc.Logger, tc.Filter, tc.OnServerDone = logger, a.trustDomain, a.logTransaction
	tc.TraceContext = func(f sip.Flow) bool { return a.isTrusted(f.Remote.Addr()) }
	roleAttrs := []attribute.KeyValue{tracing.RoleKey.String(role)}
	tc.SpanAttributes = func(*sip.Request, trace.SpanKind) []attribute.KeyValue { return roleAttrs }

	agent, err := ua.New(ua.Config{
		Transaction: tc,
		Prepare:     as.Prepare(life, a.locals, &sip.Resolver{}),
		Contact:     sip.Address{URI: sip.URI{Scheme: "sip", Host: sip.FormatHost(a.locals[0].Addr()), Port: a.locals[0].Port()}},
		Allow:       []string{"REGISTER", "MESSAGE", "NOTIFY", "OPTIONS"},
		Logger:      logger,
		StrandFor: func(req *sip.Request) *ua.Strand {
			if to, err := req.Header.To(); err == nil && req.Method == "REGISTER" {
				return g.strandOf(key(to.URI))
			}

			return nil
		},
		OnRequest: a.onRequest,
	})
	if err != nil {
		kill()
		return nil, err
	}

	a.agent = agent
	g.agent = agent
	g.detached = tracing.NewDetached(agent.Layer().TracerProvider(), tracerName, role)

	for i := range g.strands {
		g.strands[i] = agent.NewStrand()
	}

	a.hss = newHSSSync(life, agent.Layer().TracerProvider(), cfg.HSS, cfg.Store, cfg.Number, g.want, told, logger)
	g.hss = a.hss

	defer func() {
		if err != nil {
			err = errors.Join(err, a.Shutdown(ctx))
		}
	}()

	g.restore(ctx, rows)

	for _, l := range a.locals {
		if err := agent.Layer().Serve(l); err != nil {
			return nil, fmt.Errorf("serve the IP-SM-GW in memory: %w", err)
		}
	}

	a.hss.start()

	logger.InfoContext(ctx, "IP-SM-GW started", slog.String("name", "sip:"+Host(cfg.HomeDomain)), slog.Int("registrations", len(g.regs)))

	return a, nil
}

func (a *AS) Servers() []sip.Server {
	servers := make([]sip.Server, len(a.locals))
	for i, l := range a.locals {
		servers[i] = sip.Server{Transport: sip.UDP, Addr: l}
	}

	return servers
}

func (a *AS) Resubscribe(ctx context.Context) {
	a.reg.resubscribeAll(ctx)
	a.hss.markAll(trace.SpanContextFromContext(ctx), a.reg.userKeys())
}

func (a *AS) NumberChanged(ctx context.Context) {
	a.hss.markAll(trace.SpanContextFromContext(ctx), a.reg.userKeys())
}

func (a *AS) Shutdown(ctx context.Context) error {
	return a.stop(ctx, true)
}

func (a *AS) Suspend(ctx context.Context) error {
	return a.stop(ctx, false)
}

func (a *AS) stop(ctx context.Context, deregister bool) error {
	defer a.kill()

	stop := context.AfterFunc(ctx, a.kill)
	defer stop()

	a.reg.close()
	a.hss.close()

	if deregister {
		a.hss.deregisterAll(ctx)
	}

	submitted := make(chan struct{})

	go func() {
		a.work.Wait()
		close(submitted)
	}()

	select {
	case <-submitted:
	case <-ctx.Done():
	}

	return a.agent.Shutdown(ctx)
}

func (a *AS) onRequest(ctx context.Context, r *ua.Request) {
	switch r.Message().Method {
	case "REGISTER":
		a.reg.handleRegister(ctx, r)
	case "MESSAGE":
		a.handleMessage(ctx, r)
	case "OPTIONS":
		_ = r.Respond(ua.Reply{Code: 200, Header: a.agent.Capabilities()})
	default:
		_ = r.Reject(405, sip.Field{Name: "Allow", Value: a.agent.Capabilities().Get("Allow")})
	}
}

func (a *AS) isTrusted(addr netip.Addr) bool {
	addr = addr.Unmap()

	return slices.ContainsFunc(a.trusted, func(p netip.Prefix) bool { return p.Contains(addr) })
}

// TS 24.229 clause 4.4.1
func (a *AS) trustDomain(m sip.Message) error {
	if req, ok := m.(*sip.Request); ok {
		if !a.isTrusted(req.Flow.Remote.Addr()) {
			a.log.Warn("request from outside the trust domain", slog.String("method", req.Method),
				slog.String("source", req.Flow.Remote.String()))

			return &sip.StatusError{StatusCode: 403, Err: errUntrusted}
		}
	}

	return nil
}

var errUntrusted = errors.New("ipsmgw: from outside the IMS")

func (a *AS) logTransaction(ctx context.Context, req *sip.Request, res *sip.Response, elapsed time.Duration) {
	if !a.log.Enabled(ctx, slog.LevelDebug) {
		return
	}

	attrs := []any{
		slog.String("method", req.Method), slog.String("call_id", req.Header.CallID()),
		slog.Duration("duration", elapsed),
	}

	if res != nil {
		attrs = append(attrs, slog.Int("status", res.StatusCode))
	}

	a.log.DebugContext(ctx, "SIP transaction", attrs...)
}
