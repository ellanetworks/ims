// Package server is the wiring of the MCPTT server on a sip/ua agent: the functions of the requests, by the
// distinction of TS 24.379 §6.3.1, and their strands. S1 has registration and service authorisation (§7.3), the
// affiliation PUBLISH (§9.2.2.2.3), and the on-demand prearranged group calls (§10.1.1) of the participating and
// controlling functions, co-located [PM 11]; the conference event package comes in S2.
package server

import (
	"context"
	"errors"
	"hash/maphash"
	"log/slog"
	"net/netip"
	"slices"
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

// strands is the number of strands the requests outside calls share: those of an IMS public user identity go on one,
// in order.
const strands = 64

type Config struct {
	// UA configures the agent; New sets its StrandFor, OnInvite and OnRequest, adds REGISTER, PUBLISH and SUBSCRIBE
	// to its Allow, and, of its SessionTimer, the refresher "uac" of the 200s of the calls (TS 24.379 §6.3.3.2.3.2
	// step 2) and their interval of 1800 s (RFC 4028 §4) when it has none. Through the IMS, its Contact is that of the
	// server's subscriptions to the reg event package, which go to the S-CSCF of the third-party REGISTERs by their
	// Route, as the INVITEs to the members do (participating.IMS): Prepare sends a request to its first Route, and
	// in direct mode to the host of the Request-URI, the contact of a client (Prepare). Through the IMS, the
	// transaction layer the agent makes of UA.Transaction is filtered by the trust domain: UA.Layer is not allowed.
	// UA.Logger is the log of the server too, and of the layer of UA.Transaction without one; slog.Default() if nil.
	// New has the layer log each server transaction once it ends (logTransaction), after its OnServerDone if any.
	UA     ua.Config
	Source settings.Source
	Mode   registrar.Mode
	// TrustedNetworks are the IMS core's nodes, the S-CSCFs among them: the trust domain of TS 24.229 §4.4.1, whose
	// requests only the server takes through the IMS, required then; unused in direct mode [PM 14]. They must not
	// cover the UEs.
	TrustedNetworks []netip.Prefix
	// Tokens checks the access tokens; registrar.Unvalidated if nil.
	Tokens registrar.TokenCheck
	// Media is the media plane of the calls, closed by Shutdown; without one, the server takes no call: an INVITE gets
	// a 500.
	Media *media.Relay
	// State, if any, keeps the registrations and the affiliations across restarts, through the IMS: Restore puts them
	// back.
	State State
}

// State keeps the bindings of the registrar and the affiliations of the participating function.
type State interface {
	registrar.Store
	affiliation.Store
}

// Server is the MCPTT server: its agent, the participating and controlling functions, co-located [PM 11].
type Server struct {
	cfg   Config
	agent *ua.Agent
	log   *slog.Logger

	reg         *registrar.Registrar
	controlling *affiliation.Controlling
	part        *affiliation.Participating

	// calls are the participating and controlling functions of the calls.
	partCalls *participating.Participating
	ctrlCalls *controlling.Controlling

	seed    maphash.Seed
	strands [strands]*ua.Strand
}

// New makes the server and its agent, which the caller serves.
func New(cfg Config) (*Server, error) {
	if cfg.Mode == registrar.ThroughIMS {
		switch {
		case len(cfg.TrustedNetworks) == 0:
			return nil, errors.New("server: through the IMS, sip.trusted_networks is required: no S-CSCF is trusted")
		case cfg.UA.Layer != nil:
			return nil, errors.New("server: through the IMS, the agent makes its transaction layer, filtered by the trust domain")
		}
	}

	s := &Server{cfg: cfg, seed: maphash.MakeSeed(), log: cfg.UA.Logger}
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
		u.SessionTimer.Interval = defaultSessionInterval
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
	s.part = affiliation.NewParticipating(s.controlling, s.log)

	var store registrar.Store
	if cfg.State != nil && cfg.Mode == registrar.ThroughIMS {
		store = cfg.State
		s.part.SetStore(cfg.State)
	}

	s.reg = registrar.New(registrar.Config{
		Source: cfg.Source, Mode: cfg.Mode, Store: store, StrandOf: s.impuStrand, Trusted: s.trusted, Agent: a,
		Tokens: cfg.Tokens, Affiliations: s.part, Clock: a.Layer().Clock(), Logger: s.log,
	})

	// Direct mode reaches the clients at their contacts [PM 14]; through the IMS, at their S-CSCF.
	var router participating.Router = participating.IMS{Source: cfg.Source}
	if cfg.Mode == registrar.Direct {
		router = participating.Direct{}
	}

	s.partCalls = participating.New(participating.Config{
		Source: cfg.Source, Bindings: s.reg, Affiliations: s.part, Router: router, Controlling: hop{s},
		Clock: a.Layer().Clock(), Logger: s.log,
	})
	s.ctrlCalls = controlling.New(controlling.Config{
		Agent: a, Source: cfg.Source, Affiliations: s.controlling, Media: cfg.Media, Participating: s.partCalls,
		Logger: s.log,
	})

	return s, nil
}

// hop is MCPTT-3 in process: the participating function's requests to the controlling function, the SIP hop elided
// [PM 11].
type hop struct{ s *Server }

func (h hop) Group(o *participating.Origination) { h.s.ctrlCalls.Group(o) }

func (h hop) GroupCalls(user sip.URI) []sip.URI { return h.s.ctrlCalls.GroupCalls(user) }

// defaultSessionInterval is the session interval of the calls without one configured (RFC 4028 §4: 1800 s
// recommended).
const defaultSessionInterval = 1800 * time.Second

// bindings are the registrar's, as the participating function reads them.
type bindings struct{ *registrar.Registrar }

func (b bindings) Resolve(req *sip.Request, ids []sip.URI, client string) (sip.URI, bool) {
	r, ok := b.Registrar.Resolve(req, ids, client)
	return r.MCPTTID, ok
}

// Agent is the agent of the server.
func (s *Server) Agent() *ua.Agent { return s.agent }

// Restore puts back the registrations and affiliations of State, before the agent serves: the bindings registrar
// Restore keeps, and the affiliations of their clients, published to the controlling function. Resubscribe follows,
// once the agent serves.
func (s *Server) Restore(registrations []registrar.Record, affiliations []affiliation.Record) {
	restored := s.reg.Restore(registrations)

	bound := map[[2]string]bool{}
	for _, b := range restored {
		bound[[2]string{settings.Key(b.MCPTTID), b.ClientID}] = true
	}

	now := s.agent.Layer().Clock().Now()

	s.part.Restore(now, affiliations, func(id sip.URI, client string) bool {
		return bound[[2]string{settings.Key(id), client}]
	})

	// The implicit affiliations of the settings as they are now (TS 24.379 §9.2.2.2.15): of a group configured while
	// the server was down, or of a write lost.
	snap := s.cfg.Source.Current()
	for _, b := range restored {
		if u := snap.User(b.MCPTTID); u != nil {
			s.part.ImplicitConfigured(now, u, b.ClientID)
		}
	}

	s.log.Info("registrations restored", slog.Int("bindings", len(bound)))
}

// Revoke applies a change of the settings to the registrations and affiliations at once: the affiliations to the
// groups no longer of their users removed, and the clients of the users no longer authorised at their IMPUs logged
// off (registrar.Revoke).
func (s *Server) Revoke() {
	s.controlling.Revoke()
	s.part.Revoke(s.agent.Layer().Clock().Now(), s.controlling.Eligible)
	s.reg.Revoke()
}

// Resubscribe subscribes to the reg event package of the IMPUs of the bindings restored, once the agent serves
// (registrar.Resubscribe).
func (s *Server) Resubscribe() { s.reg.Resubscribe() }

// Shutdown stops the server: the timers of the registrar, so that no binding expires nor subscription is made while
// the agent closes, then the agent, ending its sessions and subscriptions until ctx is done, and the media relay last,
// which the handlers use while the agent closes.
func (s *Server) Shutdown(ctx context.Context) error {
	s.reg.Close()

	err := s.agent.Shutdown(ctx)

	if s.cfg.Media != nil {
		err = errors.Join(err, s.cfg.Media.Close())
	}

	return err
}

// Registrar is the registrar and service authorisation.
func (s *Server) Registrar() *registrar.Registrar { return s.reg }

// Participating and Controlling are the affiliation states of the functions.
func (s *Server) Participating() *affiliation.Participating { return s.part }

func (s *Server) Controlling() *affiliation.Controlling { return s.controlling }

// strandFor is the strand of the IMS public user identity of a REGISTER or a PUBLISH outside a dialog (the
// architecture's strands of the user, by its identity known before its body is read): To of a REGISTER, the
// originator of a PUBLISH. An INVITE is on the strand of the call of its group, of the <mcptt-request-uri> of its
// mcptt-info, or of the session of the MCPTT session identity of its Request-URI (TS 24.379 §4.5, §10.1.1.4.2 step 9).
// The others get one of their own.
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

// impuStrand is the strand of the IMS public user identity of key k, of its REGISTERs and PUBLISHes.
func (s *Server) impuStrand(k string) *ua.Strand {
	return s.strands[maphash.String(s.seed, k)%strands]
}

// callStrand is the strand of the call of an initial INVITE, nil if none.
func (s *Server) callStrand(req *sip.Request) *ua.Strand {
	if st, ok := s.ctrlCalls.SessionStrand(req.URI); ok {
		return st
	}

	if s.target(req.URI) != targetParticipating {
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

// target is what a Request-URI identifies (TS 24.379 §6.3.1): a public service identity of a function, a group, an
// MCPTT session identity, or none of the server's (§6.3.7.1).
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
		// An MCPTT session identity, a "gr" of the controlling PSI, of no session: not allocated, or released (TS
		// 24.379 §6.3.7.1; §10.1.1.4.5.1 step 2: "not present").
		if _, ok := u.Params.Get(controlling.SessionParam); ok {
			return targetNone
		}

		return targetControlling
	case snap.Group(u) != nil:
		return targetGroup
	}

	return targetNone
}

// onInvite distinguishes the initial INVITEs (TS 24.379 §6.3.1.1). One to the participating PSI is a "SIP INVITE
// request for originating participating MCPTT function". The controlling function takes only what the participating
// function hands it in process: an INVITE from outside to the controlling PSI or a group, of another participating
// function or a non-controlling function [PM 11], gets a 403, as one to the session identity of a session, a rejoin
// (§10.1.1.4.5), not supported yet; one to a session identity of no session (§10.1.1.4.5.1 step 2) or another
// Request-URI a 404 (§6.3.7.1). Through the IMS, an INVITE comes from the S-CSCF, over
// ISC by the originating iFC of the MCPTT AS (TS 24.229 §5.4.3.2 step 5): one from outside the trust domain gets a
// 403 (trustDomain). The server answers it
// as a UA (§5.7.2), the INVITEs it sends the members its own (§5.7.5.1: an initiating B2BUA): they do not carry the
// original dialog identifier of the S-CSCF in its Route, which §5.7.5.1 makes optional, and its Record-Route, copied into the
// responses (RFC 3261 §12.1.1), routes the requests of the dialog.
func (s *Server) onInvite(r *ua.Request) ua.Handler {
	req := r.Message()

	switch s.target(req.URI) {
	case targetParticipating:
		s.partCalls.Originate(r, s.originator(req))
	case targetNone:
		_ = r.Reject(404)
	default:
		_ = r.Reject(403)
	}

	return ua.Handler{}
}

// originator are the IMS public user identities of the originator of a request: of P-Asserted-Identity through the
// IMS, from the trust domain only (TS 24.229 §5.7.1.4 b i; RFC 3325 §5), of From in direct mode [PM 14], where
// nothing asserts one.
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

// onRequest handles the requests outside dialogs. Through the IMS, they come from the trust domain only
// (trustDomain): the originator of a PUBLISH is the one its P-Asserted-Identity asserts (TS 24.379 §9.2.2.2.3 step
// 2).
func (s *Server) onRequest(r *ua.Request) {
	req := r.Message()

	switch req.Method {
	case "REGISTER":
		s.reg.Handle(r)
	case "PUBLISH":
		s.part.HandlePublish(affiliation.Request{
			Request: r, Config: s.cfg.Source.Current(), Now: s.agent.Layer().Clock().Now(), Originator: s.originator(req),
		}, bindings{s.reg})
	case "SUBSCRIBE":
		// §6.3.1.4, §6.3.7.1: the conference event package comes in S2, and no other package is served.
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

// trusted reports whether a is of the trust domain (TS 24.229 §4.4.1): of TrustedNetworks.
func (s *Server) trusted(a netip.Addr) bool {
	a = a.Unmap()

	return slices.ContainsFunc(s.cfg.TrustedNetworks, func(p netip.Prefix) bool { return p.Contains(a) })
}

// trustDomain is the filter of the transaction layer through the IMS, after next if any: a request from outside the
// trust domain gets a 403, an ACK is dropped (TS 24.229 §4.4.1). Every request of the server's users comes through
// the S-CSCF, those within a dialog too, on the route it recorded (RFC 3261 §12.2.1.1): one from elsewhere would
// bypass the P-CSCF and S-CSCF (their policy, charging and checks of SDP), as a re-INVITE of a UE sent to the
// server's address to redirect its media. A third-party REGISTER is of an S-CSCF (§5.4.1.7), the asserted identity
// of a request, its served user, is of the trust domain only (§5.7.1.4 b i; RFC 3325 §5), and the server, without
// credentials of the users, cannot challenge a request as §5.7.1.4 b ii has an AS do. Responses pass: a transaction
// matches only the requests the server sent.
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

// logTransaction logs each server transaction once it ends, after next if any, as Ella IMS does: of the requests of
// a call, at info, the others at debug. Its request and response are summarised, their bodies never logged.
func (s *Server) logTransaction(next func(*sip.Request, *sip.Response, time.Duration)) func(*sip.Request, *sip.Response, time.Duration) {
	return func(req *sip.Request, res *sip.Response, elapsed time.Duration) {
		if next != nil {
			next(req, res, elapsed)
		}

		level := slog.LevelDebug

		switch req.Method {
		case "INVITE", "BYE", "CANCEL", "UPDATE":
			level = slog.LevelInfo
		}

		if !s.log.Enabled(context.Background(), level) {
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
			s.log.LogAttrs(context.Background(), level, "SIP transaction ended without a final response", attrs...)
			return
		}

		attrs = append(attrs, slog.Int("status", res.StatusCode), slog.String("reason", res.Reason))
		s.log.LogAttrs(context.Background(), level, "SIP transaction", attrs...)
	}
}

func (s *Server) allow() string {
	h := s.agent.Capabilities()
	return h.Get("Allow")
}
