// Package ipsmgw is the built-in IP-SM-GW of the IMS (TS 23.204; TS 24.341 §5.3.3): an AS on ISC for transport
// layer interworking. Through the IMS, it is an AS like any other, which the S-CSCF reaches over SIP by the iFCs of the
// HSS: in memory, on the network it shares with the I-CSCF and S-CSCF, by the host name of the AS (Host). It
// registers itself at the HSS for the users able to receive short messages over IP (TS 24.341 §5.3.3.2).
package ipsmgw

import (
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"log/slog"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/as"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// Port is the SIP port of the IP-SM-GW on each of the IMS's SIP addresses, in memory.
const Port = config.IPSMGWPort

// Host is the host name of the IP-SM-GW of the IMS of homeDomain: its name in the iFCs. Under the home domain, it
// is the IMS's own: no other server is reached by it.
func Host(homeDomain string) string { return "ipsmgw." + homeDomain }

// Store keeps the registrations of the IP-SM-GW, and the users the HSS has it registered for, across restarts.
type Store interface {
	IPSMGWRegistrations(ctx context.Context) ([]db.IPSMGWRegistration, error)
	PutIPSMGWRegistration(ctx context.Context, r db.IPSMGWRegistration) error
	DeleteIPSMGWRegistration(ctx context.Context, impuKey string) error
	IPSMGWHSSRegistrations(ctx context.Context) ([]db.IPSMGWHSSRegistration, error)
	PutIPSMGWHSSRegistration(ctx context.Context, r db.IPSMGWHSSRegistration) error
	DeleteIPSMGWHSSRegistration(ctx context.Context, userKey string) error
}

type Config struct {
	// Addresses are the IMS's SIP addresses: the IP-SM-GW has its port on each, in memory, and takes requests from
	// them only, its S-CSCF's.
	Addresses []netip.Addr
	// Transaction is the base of the configuration of the transaction layer: its Network the in-memory network of the
	// IMS. Start sets its Logger, Filter and OnServerDone.
	Transaction transaction.Config
	// HomeDomain is the IMS's, which the host of the IP-SM-GW is under.
	HomeDomain string
	Store      Store
	HSS        HSS
	SMSCs      SMSCs
	Router     Router
	Nodes      Nodes
	Metrics    *Metrics
	// Number is the IP-SM-GW number of the settings, "" if unset.
	Number func() string
	Logger *slog.Logger
}

// AS is the running IP-SM-GW.
type AS struct {
	agent *ua.Agent
	reg   *registrar
	hss   *hssSync
	kill  context.CancelFunc
	// life is the lifetime of the AS, of the work it does past a request: until Shutdown's deadline. work tracks it.
	life    context.Context
	work    sync.WaitGroup
	locals  []netip.AddrPort
	trusted []netip.Prefix
	smscs   SMSCs
	router  Router
	nodes   Nodes
	number  func() string
	home    string
	// plmn is the MCC and MNC of the home domain, of 6 digits: the prefix of the MT Correlation IDs of the users of no
	// IMSI known.
	plmn    string
	corr    correlations
	pending pendingReports
	metrics *Metrics
	log     *slog.Logger
}

// Start starts the IP-SM-GW: it restores the registrations it had when it stopped, and checks the HSS registrations
// of their users; Resubscribe subscribes to their reg event packages once the core runs.
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
		plmn: plmnOf(cfg.HomeDomain), metrics: cfg.Metrics,
	}

	for _, addr := range cfg.Addresses {
		a.locals = append(a.locals, netip.AddrPortFrom(addr, Port))
		a.trusted = append(a.trusted, netip.PrefixFrom(addr, addr.BitLen()))
	}

	life, kill := context.WithCancel(context.Background())
	a.kill, a.life = kill, life

	g := &registrar{store: cfg.Store, home: cfg.HomeDomain, log: logger, seed: maphash.MakeSeed(), regs: map[string]*registration{}}
	a.reg = g

	tc := cfg.Transaction
	tc.Logger, tc.Filter, tc.OnServerDone = logger, a.trustDomain, a.logTransaction

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

	for i := range g.strands {
		g.strands[i] = agent.NewStrand()
	}

	a.hss = newHSSSync(cfg.HSS, cfg.Store, cfg.Number, g.want, told, logger)
	g.hss = a.hss

	defer func() {
		if err != nil {
			err = errors.Join(err, a.Shutdown(ctx))
		}
	}()

	g.restore(rows)

	for _, l := range a.locals {
		if err := agent.Layer().Serve(l); err != nil {
			return nil, fmt.Errorf("serve the IP-SM-GW in memory: %w", err)
		}
	}

	a.hss.start()

	logger.Info("IP-SM-GW started", slog.String("name", "sip:"+Host(cfg.HomeDomain)), slog.Int("registrations", len(g.regs)))

	return a, nil
}

// Servers are the addresses of the SIP of the IP-SM-GW, which its host resolves to: in memory through the IMS.
func (a *AS) Servers() []sip.Server {
	servers := make([]sip.Server, len(a.locals))
	for i, l := range a.locals {
		servers[i] = sip.Server{Transport: sip.UDP, Addr: l}
	}

	return servers
}

// Resubscribe subscribes to the reg event package of the registrations of no subscription: those restored; and
// checks the HSS registrations of the users at once, those that failed while the core was down. The IMS calls it once
// its core runs, which the SUBSCRIBEs and the updates go through.
func (a *AS) Resubscribe() {
	a.reg.resubscribeAll()
	a.hss.markAll(a.reg.userKeys())
}

// NumberChanged has the HSS registrations of the users updated to the IP-SM-GW number of the settings.
func (a *AS) NumberChanged() { a.hss.markAll(a.reg.userKeys()) }

// Shutdown stops the IP-SM-GW until ctx is done: its timers and its updates of the HSS; then it deregisters at the
// HSS for its users (hssSync.deregisterAll), and its agent ends its subscriptions. The registrations are kept for the
// next start, which registers the IP-SM-GW at the HSS again for the users available.
func (a *AS) Shutdown(ctx context.Context) error {
	defer a.kill()

	stop := context.AfterFunc(ctx, a.kill)
	defer stop()

	a.reg.close()
	a.hss.close()
	a.hss.deregisterAll(ctx)

	// The short messages being submitted get their submit reports, through the agent, before it closes.
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

// onRequest handles the requests outside dialogs, of the trust domain only (trustDomain): the third-party
// REGISTERs and the MESSAGEs of short messages of the S-CSCF.
func (a *AS) onRequest(r *ua.Request) {
	switch r.Message().Method {
	case "REGISTER":
		a.reg.handleRegister(r)
	case "MESSAGE":
		a.handleMessage(r)
	case "OPTIONS":
		_ = r.Respond(ua.Reply{Code: 200, Header: a.agent.Capabilities()})
	default:
		_ = r.Reject(405, sip.Field{Name: "Allow", Value: a.agent.Capabilities().Get("Allow")})
	}
}

// trustDomain lets in the requests of the trust domain only, the IMS's own addresses (TS 24.229 §4.4.1): a request
// from elsewhere gets a 403. Responses pass: a transaction matches only the requests the IP-SM-GW sent.
func (a *AS) trustDomain(m sip.Message) error {
	if req, ok := m.(*sip.Request); ok {
		addr := req.Flow.Remote.Addr().Unmap()
		if !slices.ContainsFunc(a.trusted, func(p netip.Prefix) bool { return p.Contains(addr) }) {
			a.log.Warn("request from outside the trust domain", slog.String("method", req.Method),
				slog.String("source", req.Flow.Remote.String()))

			return &sip.StatusError{StatusCode: 403, Err: errUntrusted}
		}
	}

	return nil
}

var errUntrusted = errors.New("ipsmgw: from outside the IMS")

// logTransaction logs each server transaction once it ends, at debug: its request and response summarised, their
// bodies never logged.
func (a *AS) logTransaction(req *sip.Request, res *sip.Response, elapsed time.Duration) {
	if !a.log.Enabled(context.Background(), slog.LevelDebug) {
		return
	}

	attrs := []any{
		slog.String("method", req.Method), slog.String("call_id", req.Header.CallID()),
		slog.Duration("duration", elapsed),
	}

	if res != nil {
		attrs = append(attrs, slog.Int("status", res.StatusCode))
	}

	a.log.Debug("SIP transaction", attrs...)
}

// homeRealm is a home domain of TS 23.003 §13.2.
var homeRealm = regexp.MustCompile(`^ims\.mnc([0-9]{3})\.mcc([0-9]{3})\.3gppnetwork\.org$`)

// plmnOf is the MCC and the 3-digit MNC of the home domain home, "000000" of another domain.
func plmnOf(home string) string {
	m := homeRealm.FindStringSubmatch(strings.ToLower(home))
	if m == nil {
		return "000000"
	}

	return m[2] + m[1]
}
