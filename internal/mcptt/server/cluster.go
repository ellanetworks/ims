package server

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/controlling"
	"github.com/ellanetworks/ims/internal/mcptt/participating"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/ua"
)

// TS 23.379 clause 7.4.2.3.2
type Cluster interface {
	affiliation.Shared
	controlling.Peers

	Peer(a netip.Addr) bool
	GroupCall(ctx context.Context, group sip.URI) (string, bool, error)
	Claim(ctx context.Context, group sip.URI, claim string) (string, error)
	Release(group sip.URI, claim string)
}

func (s *Server) peer(a netip.Addr) bool {
	return s.cfg.Cluster != nil && s.cfg.Cluster.Peer(a.Unmap())
}

// TS 23.379 clause 7.4.2.3.2
// TS 24.379 clause 10.1.1.4.2
func (s *Server) control(ctx context.Context, o *participating.Origination, relayed bool) {
	c := s.cfg.Cluster
	group, ok := o.Group()

	if c == nil || !ok || s.cfg.Source.Current().Group(group) == nil || s.ctrlCalls.Active(group) {
		s.ctrlCalls.Group(ctx, o)
		return
	}

	self := c.Self()

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedTimeout)
	defer cancel()

	log := s.log.With(slog.String("call_id", o.Request.Message().Header.CallID()), slog.String("group", group.String()))

	if owner, ok, err := c.GroupCall(sctx, group); err == nil && ok && owner != self && !relayed {
		s.relay(ctx, o, owner)
		return
	}

	claim := rand.Text()

	owner, err := c.Claim(sctx, group, claim)

	switch {
	case err != nil:
		log.WarnContext(ctx, "group call not claimed", slog.Any("error", err))

		_ = o.Request.Respond(o.Reply(ua.Reply{Code: 503}))
	case owner != self && relayed:
		log.DebugContext(ctx, "group call controlled by another node", slog.String("node", owner))

		_ = o.Request.Respond(o.Reply(ua.Reply{Code: 480}))
	case owner != self:
		s.relay(ctx, o, owner)
	default:
		s.claimsMu.Lock()
		s.claims[settings.Key(group)] = claim
		s.claimsMu.Unlock()

		s.ctrlCalls.Group(ctx, o)

		if !s.ctrlCalls.Active(group) {
			s.released(group)
		}
	}
}

const (
	sharedTimeout = 5 * time.Second
	saveInterval  = 5 * time.Second
)

func (s *Server) saveAffiliations(life context.Context) {
	t := time.NewTicker(saveInterval)
	defer t.Stop()

	for {
		select {
		case <-life.Done():
			return
		case <-t.C:
		}

		if s.controlling.Unsaved() == 0 {
			continue
		}

		if err := s.controlling.Save(life); err != nil {
			s.log.WarnContext(life, "group affiliations not shared yet", slog.Int("unsaved", s.controlling.Unsaved()), slog.Any("error", err))
		}
	}
}

func (s *Server) released(group sip.URI) {
	k := settings.Key(group)

	s.claimsMu.Lock()
	claim, ok := s.claims[k]
	delete(s.claims, k)
	s.claimsMu.Unlock()

	if ok && s.cfg.Cluster != nil {
		s.cfg.Cluster.Release(group, claim)
	}
}

// TS 24.379 clause 6.3.2.1.3
// TS 24.229 clause 5.7.5.1
func (s *Server) relay(ctx context.Context, o *participating.Origination, owner string) {
	route, local, ok := s.cfg.Cluster.Route(owner)
	if !ok {
		s.log.WarnContext(ctx, "no MCPTT address of the node controlling the group call", slog.String("node", owner))

		_ = o.Request.Respond(o.Reply(ua.Reply{Code: 503}))

		return
	}

	snap := s.cfg.Source.Current()

	out := o.Invite.Clone()
	out.Header.Set("Route", sip.Address{URI: route}.String())

	if cs, err := out.Header.Contacts(); err == nil && len(cs) == 1 {
		out.Header.Set("Contact", participating.At(cs[0], local).String())
	}

	call := b2bua.NewCall(s.agent, o.Request.Strand(), b2bua.CallHandler{Error: s.legError})

	_, err := b2bua.Bridge(ctx, call, o.Request, out, b2bua.RelayConfig{
		Headers: b2bua.Headers{Sticky: []string{}, Field: crossing},
		Request: func(_, to *b2bua.Leg, _ *sip.Request, c *ua.Change) {
			if !to.Outgoing() {
				participating.Charged(&c.Header, o.Charging, snap.ServiceConfiguration().Domain, false)
			}
		},
		Response: func(from, to *b2bua.Leg, res *sip.Response, rp *ua.Reply) {
			if to.Outgoing() {
				return
			}

			*rp = o.Reply(*rp)

			if cs, err := res.Header.Contacts(); err == nil && len(cs) == 1 {
				rp.Header.Set("Contact", participating.Named(cs[0], snap.ControllingPSI()).String())
			}
		},
	})
	if err != nil {
		s.log.DebugContext(ctx, "group call not relayed to the controlling node", slog.String("node", owner), slog.Any("error", err))
	}
}

// TS 24.229 clause 5.7.5.1
var fromClient = []string{"Reason", "Priority"}

func crossing(from, _ *b2bua.Leg, m sip.Message, f sip.Field) bool {
	if _, ok := m.(*sip.Request); !ok || from.Outgoing() {
		return true
	}

	return slices.ContainsFunc(fromClient, func(n string) bool { return strings.EqualFold(n, f.Name) })
}

func (s *Server) legError(_ *b2bua.Call, err error) {
	s.log.Debug("bridged call leg error", slog.Any("error", err))
}

// TS 24.379 clause 6.3.1.1
func (s *Server) terminating(req *sip.Request) bool {
	return s.peer(req.Flow.Remote.Addr()) && participating.Focus(req)
}

// TS 24.379 clause 10.1.1.3.2
// TS 24.379 clause 6.3.2.2.5.2
func (s *Server) terminate(ctx context.Context, r *ua.Request) {
	snap := s.cfg.Source.Current()
	req := r.Message().Clone()

	if cs, err := req.Header.Contacts(); err == nil && len(cs) == 1 {
		req.Header.Set("Contact", participating.Named(cs[0], snap.ControllingPSI()).String())
	}

	t := s.partCalls.Terminate(req)
	if len(t.Invites) == 0 {
		_ = r.Respond(ua.Reply{Code: t.Code, Header: t.Header})
		return
	}

	f := &fork{s: s, contact: identity.WithFeatures(participating.At(sip.Address{URI: snap.ParticipatingPSI()}, r.Message().Flow.Local),
		sip.FeatureSet{sip.NewFeature(identity.FeatureTag)})}
	f.call = b2bua.NewCall(s.agent, r.Strand(), b2bua.CallHandler{Error: s.legError})

	in, err := f.call.Incoming(r, b2bua.LegHandler{Established: f.confirmed, Ended: f.inEnded})
	if err != nil {
		_ = r.Reject(500)
		return
	}

	f.in = in

	for _, inv := range t.Invites {
		l, err := f.call.Dial(ctx, inv, b2bua.LegHandler{Established: f.accepted, Failed: f.failed, Ended: f.ended})
		if err != nil {
			f.failures = append(f.failures, b2bua.Failure{Err: err})
			continue
		}

		f.branches = append(f.branches, l)
	}

	f.pending = len(f.branches)
	if f.pending == 0 {
		_ = in.Respond(b2bua.Best(f.failures))
	}
}

// RFC 3261 section 16.7
type fork struct {
	s        *Server
	contact  sip.Address
	call     *b2bua.Call
	in       *b2bua.Leg
	branches []*b2bua.Leg
	won      *b2bua.Leg
	pending  int
	failures []b2bua.Failure
}

func (f *fork) accepted(ctx context.Context, l *b2bua.Leg, _ sip.Message) {
	f.pending--

	if f.won != nil || f.in.State() == ua.Terminated {
		_ = l.End(ctx, ua.Reply{})
		return
	}

	f.won = l

	for _, o := range f.branches {
		if o != l {
			_ = o.End(ctx, ua.Reply{})
		}
	}

	_, answer, _ := l.Session().Negotiated()

	err := f.in.Respond(ua.Reply{
		Code: 200, Header: sip.Header{{Name: "Contact", Value: f.contact.String()}}, Bodies: []sip.BodyPart{ua.SDP(answer)},
	})
	if err != nil {
		_ = l.End(ctx, ua.Reply{})
	}
}

func (f *fork) confirmed(_ context.Context, in *b2bua.Leg, _ sip.Message) {
	if f.won == nil || f.won.State() != ua.Confirmed {
		return
	}

	if _, err := b2bua.NewRelay(in, f.won, b2bua.RelayConfig{Headers: b2bua.Headers{Sticky: []string{}}}); err != nil {
		f.s.log.Debug("terminating call not bridged", slog.Any("error", err))
	}
}

func (f *fork) failed(ctx context.Context, _ *b2bua.Leg, res *sip.Response, err error) {
	f.pending--
	f.failures = append(f.failures, b2bua.Failure{Response: res, Err: err})

	if f.won == nil && f.pending == 0 && f.in.State() != ua.Terminated {
		_ = f.in.Respond(b2bua.Best(f.failures))
	}
}

func (f *fork) ended(ctx context.Context, l *b2bua.Leg, _ ua.End) {
	if l == f.won && f.in.State() != ua.Terminated {
		_ = f.in.End(ctx, ua.Reply{})
	}
}

func (f *fork) inEnded(ctx context.Context, _ *b2bua.Leg, _ ua.End) {
	for _, l := range f.branches {
		_ = l.End(ctx, ua.Reply{})
	}
}
