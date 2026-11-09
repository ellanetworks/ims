package server

import (
	"context"
	"errors"
	"hash/maphash"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/controlling"
	"github.com/ellanetworks/ims/internal/mcptt/participating"
	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/ua"
)

const strands = 64

type Config struct {
	UA              ua.Config
	Source          settings.Source
	Mode            registrar.Mode
	TrustedNetworks []netip.Prefix
	HomeDomain      string
	Tokens          registrar.TokenCheck
	Media           *media.Relay
	State           State
	Cluster         Cluster
}

type State interface {
	registrar.Store
	affiliation.Store
}

type Server struct {
	cfg   Config
	agent *ua.Agent
	log   *slog.Logger

	reg         *registrar.Registrar
	controlling *affiliation.Controlling
	part        *affiliation.Participating

	partCalls *participating.Participating
	ctrlCalls *controlling.Controlling

	seed    maphash.Seed
	strands [strands]*ua.Strand

	claimsMu sync.Mutex
	claims   map[string]string
}

func New(cfg Config) (*Server, error) {
	if cfg.Mode == registrar.ThroughIMS {
		switch {
		case len(cfg.TrustedNetworks) == 0:
			return nil, errors.New("server: through the IMS, sip.trusted_networks is required: no S-CSCF is trusted")
		case cfg.UA.Layer != nil:
			return nil, errors.New("server: through the IMS, the agent makes its transaction layer, filtered by the trust domain")
		}
	}

	if cfg.Mode != registrar.ThroughIMS {
		cfg.Cluster = nil
	}

	s := &Server{cfg: cfg, seed: maphash.MakeSeed(), log: cfg.UA.Logger, claims: map[string]string{}}
	if s.log == nil {
		s.log = slog.Default()
	}

	u := cfg.UA
	u.Logger = s.log

	if u.Transaction.Logger == nil {
		u.Transaction.Logger = s.log
	}

	if cfg.Mode == registrar.ThroughIMS {
		u.Transaction.Filter = s.trustDomain(u.Transaction.Filter)
		u.Transaction.TraceContext = func(f sip.Flow) bool { return s.trusted(f.Remote.Addr()) }
	}

	u.Transaction.OnServerDone = s.logTransaction(u.Transaction.OnServerDone)

	u.Allow = slices.Clone(u.Allow)
	if u.Allow == nil {
		u.Allow = slices.Clone(ua.Methods)
	}

	for _, m := range []string{"REGISTER", "PUBLISH", "SUBSCRIBE"} {
		if !slices.Contains(u.Allow, m) {
			u.Allow = append(u.Allow, m)
		}
	}

	if u.SessionTimer.Refresher == "" {
		u.SessionTimer.Refresher = sip.RefresherUAC
	}

	if u.SessionTimer.Interval == 0 {
		u.SessionTimer.Interval = DefaultSessionInterval
	}

	u.StrandFor = s.strandFor
	u.OnInvite = s.onInvite
	u.OnRequest = s.onRequest

	a, err := ua.New(u)
	if err != nil {
		return nil, err
	}

	s.agent = a

	for i := range s.strands {
		s.strands[i] = a.NewStrand()
	}

	s.controlling = affiliation.NewControlling(cfg.Source)
	if cfg.Cluster != nil {
		s.controlling = affiliation.NewSharedControlling(cfg.Source, cfg.Cluster)
	}

	s.part = affiliation.NewParticipating(s.controlling, s.log)

	var store registrar.Store
	if cfg.State != nil && cfg.Mode == registrar.ThroughIMS {
		store = cfg.State
		s.part.SetStore(a.Layer().Lifetime(), cfg.State)
	}

	s.reg = registrar.New(registrar.Config{
		Source: cfg.Source, Mode: cfg.Mode, Store: store, StrandOf: s.impuStrand, Trusted: s.trusted, Agent: a,
		HomeDomain: cfg.HomeDomain, Tokens: cfg.Tokens, Affiliations: s.part, Clock: a.Layer().Clock(), Logger: s.log,
		TracerProvider: a.Layer().TracerProvider(),
	})

	var router participating.Router = participating.IMS{Source: cfg.Source}
	if cfg.Mode == registrar.Direct {
		router = participating.Direct{}
	}

	s.partCalls = participating.New(participating.Config{
		Source: cfg.Source, Bindings: s.reg, Affiliations: s.part, Router: router, Controlling: hop{s},
		Clock: a.Layer().Clock(), Logger: s.log,
	})
	ccfg := controlling.Config{
		Agent: a, Source: cfg.Source, Affiliations: s.controlling, Media: cfg.Media, Participating: s.partCalls,
		Logger: s.log,
	}

	if cfg.Cluster != nil {
		ccfg.Peers, ccfg.Released = cfg.Cluster, s.released

		go s.saveAffiliations(a.Layer().Lifetime())
	}

	s.ctrlCalls = controlling.New(ccfg)

	return s, nil
}

type hop struct{ s *Server }

func (h hop) Group(ctx context.Context, o *participating.Origination) { h.s.control(ctx, o, false) }

func (h hop) GroupCalls(user sip.URI) []sip.URI { return h.s.ctrlCalls.GroupCalls(user) }

// RFC 4028 section 4
const DefaultSessionInterval = 1800 * time.Second

type bindings struct{ *registrar.Registrar }

func (b bindings) Resolve(req *sip.Request, ids []sip.URI, client string) (sip.URI, bool) {
	r, ok := b.Registrar.Resolve(req, ids, client)
	return r.MCPTTID, ok
}

func (s *Server) Agent() *ua.Agent { return s.agent }

func (s *Server) Restore(ctx context.Context, registrations []registrar.Record, affiliations []affiliation.Record) {
	restored := s.reg.Restore(ctx, registrations)

	bound := map[[2]string]bool{}
	for _, b := range restored {
		bound[[2]string{settings.Key(b.MCPTTID), b.ClientID}] = true
	}

	now := s.agent.Layer().Clock().Now()

	s.part.Restore(ctx, now, affiliations, func(id sip.URI, client string) bool {
		return bound[[2]string{settings.Key(id), client}]
	})

	if s.cfg.Cluster != nil {
		if err := s.controlling.Retain(ctx, now, s.part.Snapshot(now)); err != nil {
			s.log.WarnContext(ctx, "stale group affiliations of this node not removed", slog.Any("error", err))
		}
	}

	s.log.InfoContext(ctx, "registrations restored", slog.Int("bindings", len(bound)))
}

// TS 24.379 clause 9.2.2.2.6
func (s *Server) Republish(ctx context.Context) {
	if s.cfg.Cluster == nil {
		return
	}

	now := s.agent.Layer().Clock().Now()

	s.part.Republish(ctx, now)

	if err := s.controlling.Retain(ctx, now, s.part.Snapshot(now)); err != nil {
		s.log.WarnContext(ctx, "stale group affiliations of this node not removed", slog.Any("error", err))
	}
}

func (s *Server) Revoke(ctx context.Context, before, after *settings.Snapshot) {
	s.controlling.Revoke(ctx)
	s.part.Revoke(ctx, s.agent.Layer().Clock().Now(), s.controlling.Eligible)
	s.reg.Revoke(ctx, before, after)
	s.ctrlCalls.Revoke(ctx)
}

func (s *Server) Resubscribe(ctx context.Context) { s.reg.Resubscribe(ctx) }

func (s *Server) Shutdown(ctx context.Context) error {
	s.reg.Close()

	err := s.agent.Shutdown(ctx)

	if s.cfg.Media != nil {
		err = errors.Join(err, s.cfg.Media.Close())
	}

	return err
}

func (s *Server) Registrar() *registrar.Registrar { return s.reg }

func (s *Server) Participating() *affiliation.Participating { return s.part }

func (s *Server) Controlling() *affiliation.Controlling { return s.controlling }

func (s *Server) Calls() *controlling.Controlling { return s.ctrlCalls }

func (s *Server) Now() time.Time { return s.agent.Layer().Clock().Now() }

func (s *Server) strandFor(req *sip.Request) *ua.Strand {
	var id sip.URI

	switch req.Method {
	case "INVITE":
		return s.callStrand(req)
	case "REGISTER":
		to, err := req.Header.To()
		if err != nil {
			return nil
		}

		id = to.URI
	case "PUBLISH":
		ids := s.originator(req)
		if len(ids) == 0 {
			return nil
		}

		id = ids[0]
	default:
		return nil
	}

	return s.impuStrand(settings.Key(id))
}

func (s *Server) impuStrand(k string) *ua.Strand {
	return s.strands[maphash.String(s.seed, k)%strands]
}

func (s *Server) callStrand(req *sip.Request) *ua.Strand {
	if st, ok := s.ctrlCalls.SessionStrand(req.URI); ok {
		return st
	}

	if t := s.target(req.URI); t != targetParticipating && (t != targetControlling || !s.peer(req.Flow.Remote.Addr())) {
		return nil
	}

	part, ok, err := req.Content().Find(mcpttinfo.ContentType)
	if !ok || err != nil {
		return nil
	}

	info, err := mcpttinfo.Decode(part.Body)
	if err != nil || info.Params == nil || info.Params.RequestURI == nil {
		return nil
	}

	v, ok := info.Params.RequestURI.URI()
	if !ok {
		return nil
	}

	group, err := sip.ParseURI(v)
	if err != nil {
		return nil
	}

	return s.ctrlCalls.Strand(group)
}

// TS 24.379 clause 6.3.1
// TS 24.379 clause 6.3.7.1
type target int

const (
	targetNone target = iota
	targetParticipating
	targetControlling
	targetGroup
	targetSession
)

func (s *Server) target(u sip.URI) target {
	snap := s.cfg.Source.Current()

	if _, ok := s.ctrlCalls.SessionStrand(u); ok {
		return targetSession
	}

	switch k := settings.Key(u); {
	case k == "":
		return targetNone
	case k == settings.Key(snap.ParticipatingPSI()):
		return targetParticipating
	case k == settings.Key(snap.ControllingPSI()):
		if _, ok := u.Params.Get(controlling.SessionParam); ok {
			return targetNone
		}

		return targetControlling
	case snap.Group(u) != nil:
		return targetGroup
	}

	return targetNone
}

// TS 24.379 clause 6.3.1.1
// TS 24.379 clause 6.3.7.1
func (s *Server) onInvite(ctx context.Context, r *ua.Request) ua.Handler {
	req := r.Message()

	switch s.target(req.URI) {
	case targetParticipating:
		if s.terminating(req) {
			s.terminate(ctx, r)
			break
		}

		s.partCalls.Originate(ctx, r, s.originator(req))
	case targetControlling:
		if !s.peer(req.Flow.Remote.Addr()) {
			_ = r.Reject(403)
			break
		}

		s.control(ctx, participating.Relayed(s.cfg.Source.Current(), r), true)
	case targetNone:
		_ = r.Reject(404)
	default:
		_ = r.Reject(403)
	}

	return ua.Handler{}
}

// TS 24.229 clause 5.7.1.4
// RFC 3325 section 5
func (s *Server) originator(req *sip.Request) []sip.URI {
	if s.cfg.Mode == registrar.Direct {
		from, err := req.Header.From()
		if err != nil {
			return nil
		}

		return []sip.URI{from.URI}
	}

	if !s.trusted(req.Flow.Remote.Addr()) {
		return nil
	}

	as, err := req.Header.Addresses("P-Asserted-Identity")
	if err != nil {
		return nil
	}

	var out []sip.URI
	for _, a := range as {
		out = append(out, a.URI)
	}

	return out
}

func (s *Server) onRequest(ctx context.Context, r *ua.Request) {
	req := r.Message()

	switch req.Method {
	case "REGISTER":
		s.reg.Handle(ctx, r)
	case "PUBLISH":
		s.part.HandlePublish(ctx, affiliation.Request{
			Request: r, Config: s.cfg.Source.Current(), Now: s.agent.Layer().Clock().Now(), Originator: s.originator(req),
		}, bindings{s.reg})
	case "SUBSCRIBE":
		if s.target(req.URI) == targetNone {
			_ = r.Reject(404)
			return
		}

		_ = r.Reject(489)
	case "OPTIONS":
		_ = r.Respond(ua.Reply{Code: 200, Header: s.agent.Capabilities()})
	default:
		_ = r.Reject(405, sip.Field{Name: "Allow", Value: s.allow()})
	}
}

// TS 24.229 clause 4.4.1
func (s *Server) trusted(a netip.Addr) bool {
	a = a.Unmap()

	return slices.ContainsFunc(s.cfg.TrustedNetworks, func(p netip.Prefix) bool { return p.Contains(a) }) || s.peer(a)
}

// TS 24.229 clause 4.4.1
func (s *Server) trustDomain(next func(sip.Message) error) func(sip.Message) error {
	return func(m sip.Message) error {
		if req, ok := m.(*sip.Request); ok && !s.trusted(req.Flow.Remote.Addr()) {
			s.log.Warn("request from outside the trust domain", slog.String("method", req.Method),
				slog.String("source", req.Flow.Remote.String()))

			return &sip.StatusError{StatusCode: 403, Err: errUntrusted}
		}

		if next != nil {
			return next(m)
		}

		return nil
	}
}

var errUntrusted = errors.New("server: from outside sip.trusted_networks")

func (s *Server) logTransaction(
	next func(context.Context, *sip.Request, *sip.Response, time.Duration),
) func(context.Context, *sip.Request, *sip.Response, time.Duration) {
	return func(ctx context.Context, req *sip.Request, res *sip.Response, elapsed time.Duration) {
		if next != nil {
			next(ctx, req, res, elapsed)
		}

		level := slog.LevelDebug

		switch req.Method {
		case "INVITE", "BYE", "CANCEL", "UPDATE":
			level = slog.LevelInfo
		}

		if !s.log.Enabled(ctx, level) {
			return
		}

		attrs := []slog.Attr{slog.String("method", req.Method), slog.String("call_id", req.Header.CallID())}

		if from, err := req.Header.From(); err == nil {
			attrs = append(attrs, slog.String("from", from.URI.String()))
		}

		if to, err := req.Header.To(); err == nil {
			attrs = append(attrs, slog.String("to", to.URI.String()))
		}

		attrs = append(attrs, slog.String("remote", req.Flow.Remote.String()), slog.Duration("duration", elapsed))

		if res == nil {
			s.log.LogAttrs(ctx, level, "SIP transaction ended without a final response", attrs...)
			return
		}

		attrs = append(attrs, slog.Int("status", res.StatusCode), slog.String("reason", res.Reason))
		s.log.LogAttrs(ctx, level, "SIP transaction", attrs...)
	}
}

func (s *Server) allow() string {
	h := s.agent.Capabilities()
	return h.Get("Allow")
}
