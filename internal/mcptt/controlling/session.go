package controlling

import (
	"cmp"
	"context"
	"log/slog"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/participating"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/mcptt/controlchannel"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/floorctl"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
	"go.opentelemetry.io/otel/trace"
)

// TS 24.379 clause 10.1.1.4.2
type session struct {
	c    *Controlling
	grp  *group
	snap *settings.Snapshot
	g    *settings.Group
	call *b2bua.Call

	id    sip.URI
	gr    string
	log   *slog.Logger
	cause trace.SpanContext

	sp      speech
	media   *media.Call
	fs      *floor.Server
	floors  map[*floor.Member]*member
	joining *member

	origin  *member
	orig    *participating.Origination
	info    mcpttinfo.Info
	members []*member

	charging map[*b2bua.Leg]identity.Charging

	setup    setup
	answered bool
	created  time.Time
	started  time.Time
	tng3     *ua.Timer
	released bool
}

// TS 24.379 clause 10.1.1.4.2
// TS 24.379 clause 6.3.3.3
type setup struct {
	inviting  bool
	oks       int
	truncated bool
	ack       ack
	tng1      *ua.Timer
	failures  []b2bua.Failure
}

// TS 24.379 clause 6.3.3.3
type ack int

const (
	ackNone ack = iota
	ackWaiting
	ackDone
	ackExpired
	ackProceeded
)

type member struct {
	s        *session
	id       sip.URI
	entry    *settings.Entry
	required bool
	state    state
	acked    bool

	leg         *b2bua.Leg
	branches    []*b2bua.Leg
	pending     int
	live        int
	failures    []b2bua.Failure
	media       *media.Leg
	fm          *floor.Member
	offered     controlchannel.Params
	offerParams controlchannel.Params
	answerSDP   []byte
}

type state int

const (
	invited state = iota
	joined
	failed
	gone
)

// TS 24.379 clause 10.1.1.4.2
func (c *Controlling) Group(ctx context.Context, o *participating.Origination) {
	snap := c.cfg.Source.Current()
	r, req := o.Request, o.Invite
	host := snap.ControllingPSI().Host
	log := c.cfg.Logger.With(slog.String("call_id", r.Message().Header.CallID()))

	refuse := func(code int, warn, reason string) {
		if warn != "" {
			reason = warn
		}

		log.DebugContext(ctx, "group call refused", slog.Int("code", code), slog.String("reason", reason))

		rp := ua.Reply{Code: code}
		if warn != "" {
			rp.Header = sip.Header{identity.Warning(host, warn)}
		}

		_ = r.Respond(o.Reply(rp))
	}

	info, ok := infoOf(req)
	gid, okG := uriOf(info.Params.RequestURI)
	caller, okC := uriOf(info.Params.CallingUserID)
	client, _ := textOf(info.Params.ClientID)
	g := snap.Group(gid)

	switch {
	case !ok || !okG || !okC:
		refuse(403, "", "no group or calling user")
		return
	case g == nil:
		refuse(404, "", "group not configured")
		return
	case c.cfg.Media == nil:
		refuse(500, "", "no media plane")
		return
	}

	log = log.With(slog.String("group", gid.String()))

	grp := c.group(gid)
	if r.Strand() != grp.st {
		refuse(500, "", "group configured since the request came")
		return
	}

	offer, err := sdp.Parse(sessionSDP(req))
	if err != nil {
		refuse(488, "", "invalid SDP offer")
		return
	}

	sp, ai, ci, err := chooseSpeech(offer, g.Encodings)
	if err != nil {
		refuse(488, "", "no speech of the group's encodings")
		return
	}

	if !acceptsMCPTT(req) {
		refuse(403, "", "no MCPTT Accept-Contact")
		return
	}

	now := c.now()

	if !c.cfg.Affiliations.Affiliated(now, gid, caller, client) {
		refuse(403, identity.WarnNotAffiliated, "")
		return
	}

	entry := g.Entry(caller)

	if s := grp.sess; s != nil {
		if entry == nil {
			refuse(403, identity.WarnNotAuthorisedToJoin, "")
			return
		}

		s.join(ctx, o, entry, offer)

		return
	}

	if entry == nil {
		refuse(403, identity.WarnNotAuthorisedToInitiate, "")
		return
	}

	s := &session{
		c: c, grp: grp, snap: snap, g: g, sp: sp, orig: o, info: info, floors: map[*floor.Member]*member{},
		charging: map[*b2bua.Leg]identity.Charging{}, setup: setup{ack: ackNone}, log: log,
	}

	invitees, code := s.invitees(c.cfg.Affiliations.Members(now, gid), caller)
	if code != 0 {
		refuse(code, identity.WarnAbandonedRequired, "")
		return
	}

	if len(invitees) == 0 {
		refuse(480, "", "no member to invite")
		return
	}

	if err := s.start(ctx, entry, offer, ai, ci); err != nil {
		log.WarnContext(ctx, "group session not created", slog.Any("error", err))
		refuse(500, "", err.Error())

		return
	}

	if slices.ContainsFunc(invitees, func(m *member) bool { return m.required }) {
		s.setup.ack = ackWaiting
		s.setup.tng1 = grp.st.AfterFunc(g.TNG1, func() { s.expireTNG1(ctx) })
	}

	s.setup.inviting = true

	for _, m := range invitees {
		s.invite(ctx, m)
	}

	s.setup.inviting = false
	s.maybeAnswer(ctx)
}

// TS 24.379 clause 10.1.1.4.2
// TS 24.379 clause 6.3.3.5
func (s *session) start(ctx context.Context, entry *settings.Entry, offer *sdp.Session, ai, ci int) error {
	c := s.c

	fs, err := floor.NewServer(floor.ServerConfig{
		Clock: strandClock{st: s.grp.st, c: c}, Out: floorOut{s}, Timers: s.snap.ServerTimers(s.g), SSRC: rand.Uint32(),
	})
	if err != nil {
		return err
	}

	s.fs, s.media = fs, c.cfg.Media.NewCall()
	s.call = b2bua.NewCall(c.cfg.Agent, s.grp.st, b2bua.CallHandler{Ended: func(ctx context.Context, _ *b2bua.Call) { s.release(ctx, "ended", ua.Reply{}) }})
	s.call.SetValue(s)

	m, err := s.participant(s.orig, entry, offer, s.sp.origin, ai, ci, false)
	if err != nil {
		s.media.Close()
		return err
	}

	s.origin = m

	s.created = c.now()
	s.cause = trace.SpanContextFromContext(ctx)
	c.allocate(s, s.snap.ControllingPSI())
	s.grp.sess = s
	s.log = s.log.With(slog.String("session", s.id.String()))

	if s.g.TNG3 > 0 {
		s.tng3 = s.grp.st.AfterFunc(s.g.TNG3, func() {
			ctx, span := s.detach("group call maximum duration")
			defer span.End()

			s.release(ctx, "maximum duration", ua.Reply{})
		})
	}

	return nil
}

// TS 24.379 clause 6.3.5.5
// TS 24.379 clause 10.1.1.4.2
func (s *session) invitees(affiliated []affiliation.Member, caller sip.URI) ([]*member, int) {
	g := s.g

	in := map[string]bool{settings.Key(caller): true}
	for _, a := range affiliated {
		in[settings.Key(a.ID)] = true
	}

	if len(in) < g.MinimumAffiliated {
		return nil, 480
	}

	for i := range g.List.Entries {
		if e := &g.List.Entries[i]; e.AffiliationRequired && !in[settings.Key(e.ID())] {
			return nil, 480
		}
	}

	var out []*member

	for _, a := range affiliated {
		if e := g.Entry(a.ID); e != nil && settings.Key(a.ID) != settings.Key(caller) {
			out = append(out, &member{s: s, id: a.ID, entry: e, required: e.Required})
		}
	}

	slices.SortFunc(out, func(a, b *member) int {
		if a.required != b.required {
			if a.required {
				return -1
			}

			return 1
		}

		return cmp.Compare(settings.Key(a.id), settings.Key(b.id))
	})

	if g.MaxParticipants > 0 && len(out) > g.MaxParticipants-1 {
		out, s.setup.truncated = out[:g.MaxParticipants-1], true
	}

	return out, 0
}

func (s *session) participant(o *participating.Origination, entry *settings.Entry, offer *sdp.Session, st stream, ai, ci int, joining bool) (*member, error) {
	params, _ := controlchannel.MediaParams(offer.Media[ci])
	mux := offer.Media[ai].RTCPMux()

	m := &member{s: s, id: entry.ID(), entry: entry, state: invited}

	ml, err := s.media.AddLeg(s.legConfig(m, st.pt, mux, params.HasFloorSSRC))
	if err != nil {
		return nil, err
	}

	rem, err := remoteOf(offer, ai, ci)
	if err != nil {
		ml.Close()
		return nil, err
	}

	ml.SetRemote(rem)
	m.media = ml

	floorSSRC, _ := ml.FloorSSRC()
	local := controlchannel.Params{
		ImplicitRequest: true, Granted: !joining && !entry.ReceiveOnly, Priority: s.priority(entry),
		SSRC: ml.AudioSSRC(), HasSSRC: true, FloorSSRC: floorSSRC, HasFloorSSRC: params.HasFloorSSRC,
	}

	answer, err := controlchannel.Answer(params, local)
	if err == nil {
		m.offered = answer

		var b []byte
		if b, err = s.sp.answer(offer, st, ai, ci, ml, rand.Uint64()>>1, mux, answer); err == nil {
			m.answerSDP, m.offerParams = b, params
		}
	}

	if err != nil {
		ml.Close()
		return nil, err
	}

	l, err := s.call.Incoming(o.Request, b2bua.LegHandler{Ended: func(ctx context.Context, _ *b2bua.Leg, _ ua.End) { s.left(ctx, m) }, Decorate: s.decorate})
	if err != nil {
		ml.Close()
		return nil, err
	}

	l.SetValue(m)
	m.leg = l
	s.charging[l] = o.Charging
	s.add(m)

	return m, nil
}

// TS 24.379 clause 10.1.1.3.1.1
func (s *session) add(m *member) {
	s.members = append(s.members, m)
	s.c.enter(s, m.id)
}

func (s *session) set(m *member, st state) {
	live := func(st state) bool { return st == invited || st == joined }

	if live(m.state) && !live(st) {
		s.c.exit(s, m.id)
	}

	m.state = st
}

// TS 24.380 clause 14.3.3
func (s *session) priority(e *settings.Entry) uint8 {
	if e.ReceiveOnly {
		return 0
	}

	return s.userPriority(e)
}

// TS 24.380 clause 14.2.3
// TS 24.380 clause 14.3.3
// TS 24.380 clause 12.1.2.3
func (s *session) userPriority(e *settings.Entry) uint8 {
	return uint8(min(e.UserPriority, *s.snap.ServiceConfiguration().OnNetwork.NumLevelsPriorityHierarchy, controlchannel.MaxPriority))
}

func (s *session) legConfig(m *member, pt uint8, mux, shared bool) media.LegConfig {
	st := s.grp.st

	return media.LegConfig{
		PayloadType: pt, Mux: mux, SharedControl: shared,
		OnFloor: func(msg floorctl.Message) {
			st.Post(func() {
				if m.fm != nil && m.state == joined {
					m.fm.Receive(msg)
				}
			})
		},
		OnMedia: func() {
			st.Post(func() {
				if m.fm != nil && m.state == joined {
					m.fm.Media()
				}
			})
		},
	}
}

// TS 24.379 clause 10.1.1.4.2
// RFC 3264 section 6.1
// TS 24.380 clause 6.3.5.2.2
func (s *session) join(ctx context.Context, o *participating.Origination, entry *settings.Entry, offer *sdp.Session) {
	host := s.snap.ControllingPSI().Host
	joiner := memberAttr(entry.ID(), o.Request.Message().Header.CallID())

	refuse := func(rp ua.Reply, reason string) {
		s.log.DebugContext(ctx, "participant not joined", joiner, slog.Int("code", rp.Code), slog.String("reason", reason))
		_ = o.Request.Respond(o.Reply(rp))
	}

	st, ai, ci, err := s.sp.joining(offer)
	if err != nil {
		refuse(ua.Reply{Code: 488}, "no speech of the call")
		return
	}

	if s.g.MaxParticipants > 0 && s.count(joined)+s.count(invited) >= s.g.MaxParticipants {
		refuse(ua.Reply{Code: 486, Header: sip.Header{identity.Warning(host, identity.WarnTooManyParticipants)}}, identity.WarnTooManyParticipants)
		return
	}

	m, err := s.participant(o, entry, offer, st, ai, ci, true)
	if err != nil {
		s.log.WarnContext(ctx, "participant not added", joiner, slog.Any("error", err))
		refuse(ua.Reply{Code: 500}, err.Error())

		return
	}

	if s.respond(ctx, m, o, sip.Header{identity.Warning(host, identity.WarnSessionExists)}) {
		s.logMember(ctx, slog.LevelDebug, "participant joined", m)
		s.joinFloor(m, m.offerParams.FloorSSRC, m.offerParams.HasFloorSSRC, m.offered.Priority, m.offered.ImplicitRequest)
		s.supersede(ctx, m)
		s.maybeAnswer(ctx)
	}
}

func (s *session) supersede(ctx context.Context, m *member) {
	k := settings.Key(m.id)

	for _, o := range s.members {
		if o == m || o == s.origin || o.state != invited || o.leg != nil || settings.Key(o.id) != k {
			continue
		}

		o.acked = true
		s.set(o, gone)
		s.logMember(ctx, slog.LevelDebug, "invitation superseded", o)

		for _, l := range o.branches {
			_ = l.End(ctx, ua.Reply{})
		}

		if o.media != nil {
			o.media.Close()
		}
	}
}

// TS 24.379 clause 6.3.3.2.3.2
func (s *session) respond(ctx context.Context, m *member, o *participating.Origination, header sip.Header) bool {
	psi := s.snap.ControllingPSI()

	h := sip.Header{
		{Name: "Contact", Value: identity.WithFeatures(sip.Address{URI: s.id}, identity.Features(true)).String()},
		{Name: "P-Asserted-Identity", Value: sip.Address{URI: psi}.String()},
	}

	err := m.leg.Request().Respond(o.Reply(ua.Reply{Code: 200, Header: append(h, header...), Bodies: []sip.BodyPart{ua.SDP(m.answerSDP)}}))
	if err != nil {
		_ = m.leg.End(ctx, ua.Reply{})
		return false
	}

	return true
}

// TS 24.380 clause 6.3.5.2.2
func (s *session) joinFloor(m *member, floorSSRC uint32, has bool, priority uint8, implicit bool) {
	m.state = joined
	s.joining = m

	defer func() { s.joining = nil }()

	m.fm = s.fs.Join(floor.MemberConfig{
		ID: m.id.String(), FloorSSRC: floorSSRC, HasFloorSSRC: has, AudioSSRC: m.media.AudioSSRC(), Priority: priority,
		ReceiveOnly: m.entry.ReceiveOnly,
	}, implicit)
	s.floors[m.fm] = m
}

func (s *session) count(st state) int {
	n := 0

	for _, m := range s.members {
		if m.state == st {
			n++
		}
	}

	return n
}

// TS 24.379 clause 10.1.1.4.1.1
// RFC 3261 section 13.2.2.4
// RFC 3261 section 16.7
func (s *session) invite(ctx context.Context, m *member) {
	if s.released {
		return
	}

	s.add(m)

	ml, err := s.media.AddLeg(s.legConfig(m, s.sp.codec.Payload, false, true))
	if err != nil {
		s.log.WarnContext(ctx, "member not invited: no media", m.attr(), slog.Any("error", err))
		s.declined(ctx, m, failure(500, nil))

		return
	}

	m.media = ml

	floorSSRC, _ := ml.FloorSSRC()
	m.offered = controlchannel.Offer(controlchannel.Params{
		FloorSSRC: floorSSRC, HasFloorSSRC: true, Priority: s.userPriority(m.entry),
	})

	req, err := s.memberInvite(m, ml)
	if err != nil {
		s.log.WarnContext(ctx, "member not invited", m.attr(), slog.Any("error", err))
		ml.Close()
		s.declined(ctx, m, failure(500, nil))

		return
	}

	t := s.c.cfg.Participating.Terminate(req)
	if len(t.Invites) == 0 {
		ml.Close()
		s.declined(ctx, m, failure(t.Code, t.Header))

		return
	}

	for _, inv := range t.Invites {
		l, err := s.call.Dial(ctx, inv, b2bua.LegHandler{
			Established: func(ctx context.Context, l *b2bua.Leg, _ sip.Message) { s.branchAccepted(ctx, m, l) },
			Failed: func(ctx context.Context, l *b2bua.Leg, res *sip.Response, err error) {
				s.branchFailed(ctx, m, res, err)
			},
			Ended:    func(ctx context.Context, l *b2bua.Leg, _ ua.End) { s.branchEnded(ctx, m, l) },
			Decorate: s.decorate,
		})
		if err != nil {
			m.failures = append(m.failures, b2bua.Failure{Err: err})
			continue
		}

		l.SetValue(m)
		s.charging[l] = identity.ChargingOf(inv.Header)
		m.branches = append(m.branches, l)
		m.pending++
		m.live++
	}

	if len(m.branches) == 0 {
		ml.Close()
		s.declined(ctx, m, m.failures...)
	}
}

func (s *session) branchAccepted(ctx context.Context, m *member, l *b2bua.Leg) {
	m.pending--

	if s.released || m.state != invited || m.leg != nil {
		_ = l.End(ctx, ua.Reply{})
		return
	}

	m.leg = l

	for _, o := range m.branches {
		if o != l {
			_ = o.End(ctx, ua.Reply{})
		}
	}

	s.accepted(ctx, m)
}

func (s *session) branchFailed(ctx context.Context, m *member, res *sip.Response, err error) {
	m.pending--

	if m.leg != nil {
		return
	}

	m.failures = append(m.failures, b2bua.Failure{Response: res, Err: err})

	if m.pending == 0 {
		s.declined(ctx, m, m.failures...)
	}
}

func (s *session) branchEnded(ctx context.Context, m *member, l *b2bua.Leg) {
	m.live--

	if l == m.leg || m.leg == nil && m.live == 0 {
		s.left(ctx, m)
	}
}

// TS 24.379 clause 6.3.3.1.2
// TS 24.379 clause 10.1.1.4.1.1
func (s *session) memberInvite(m *member, ml *media.Leg) (*sip.Request, error) {
	psi := s.snap.ControllingPSI()

	info := s.info
	p := *info.Params
	p.RequestURI = mcpttinfo.URI(m.id.String())
	p.CallingGroupID = mcpttinfo.URI(s.g.ID().String())
	p.ClientID = nil
	info.Params = &p

	body, err := mcpttinfo.Encode(info)
	if err != nil {
		return nil, err
	}

	offer, err := s.sp.offer(ml, rand.Uint64()>>1, m.offered)
	if err != nil {
		return nil, err
	}

	req := sip.NewRequest("INVITE", s.snap.ParticipatingPSI())
	req.Header.Set("From", sip.Address{URI: psi}.String())
	req.Header.Set("To", sip.Address{URI: m.id}.String())
	req.Header.Set("Contact", identity.WithFeatures(sip.Address{URI: s.id}, identity.Features(true)).String())

	for _, f := range identity.Features(false) {
		v, _ := sip.CallerPreference{Features: sip.FeatureSet{f}, Require: true, Explicit: true}.Value()
		req.Header.Add("Accept-Contact", v)
	}

	req.Header.Add("P-Asserted-Service", identity.ICSI)
	req.Header.Add("P-Asserted-Identity", sip.Address{URI: psi}.String())
	req.Header.Add("Supported", "timer")

	for _, n := range []string{"Answer-Mode", "Priv-Answer-Mode"} {
		if v := s.orig.Invite.Header.Get(n); v != "" {
			req.Header.Add(n, v)
		}
	}

	req.SetBodies(ua.SDP(offer), sip.NewBodyPart(mcpttinfo.ContentType, body))

	return req, nil
}

// TS 24.379 clause 10.1.1.4.1.1
// TS 24.380 clause 6.3
func (s *session) accepted(ctx context.Context, m *member) {
	if s.released || m.state != invited {
		return
	}

	_, b, _ := m.leg.Session().Negotiated()

	rem, params, err := s.sp.takeAnswer(b, m.offered)
	if err != nil {
		s.declined(ctx, m, failure(488, nil))
		_ = m.leg.End(ctx, ua.Reply{})

		return
	}

	m.media.SetRemote(rem)
	m.media.SetMultiplexed(params.HasFloorSSRC)
	s.joinFloor(m, params.FloorSSRC, params.HasFloorSSRC, params.Priority, false)
	m.acked = true
	s.setup.oks++
	s.logMember(ctx, slog.LevelDebug, "member joined", m)

	s.maybeAnswer(ctx)
}

func (s *session) declined(ctx context.Context, m *member, fs ...b2bua.Failure) {
	if s.released || m.state != invited {
		return
	}

	s.set(m, failed)

	if s.log.Enabled(ctx, slog.LevelDebug) {
		attrs := []slog.Attr{slog.Int("code", b2bua.Best(fs).Code)}
		if i := slices.IndexFunc(fs, func(f b2bua.Failure) bool { return f.Err != nil }); i >= 0 {
			attrs = append(attrs, slog.Any("error", fs[i].Err))
		}

		s.logMember(ctx, slog.LevelDebug, "member declined", m, attrs...)
	}

	if s.answered {
		return
	}

	s.setup.failures = append(s.setup.failures, fs...)

	if m.required && s.setup.ack == ackWaiting && s.g.TNG1Action == settings.Abandon {
		s.release(ctx, "required member declined", s.final(fs, identity.WarnAbandonedRequired))
		return
	}

	s.maybeAnswer(ctx)
}

// TS 24.379 clause 10.1.1.4.2
// RFC 3261 section 16.7
func (s *session) final(failures []b2bua.Failure, warn ...string) ua.Reply {
	rp := ua.Reply{Code: 480}
	if len(failures) > 0 {
		rp = b2bua.Best(failures)
	}

	for _, w := range warn {
		rp.Header = append(rp.Header, identity.Warning(s.snap.ControllingPSI().Host, w))
	}

	return rp
}

func failure(code int, header sip.Header) b2bua.Failure {
	if code < 300 {
		code = 500
	}

	res := &sip.Response{StatusCode: code}
	res.Header = header.Clone()

	return b2bua.Failure{Response: res}
}

// TS 24.379 clause 10.1.1.4.2
// TS 24.379 clause 6.3.3.3
func (s *session) maybeAnswer(ctx context.Context) {
	st := &s.setup
	if s.answered || s.released || st.inviting {
		return
	}

	pending := func(required bool) bool {
		return slices.ContainsFunc(s.members, func(m *member) bool {
			return m != s.origin && m.state == invited && (m.required || !required)
		})
	}

	switch st.ack {
	case ackWaiting:
		if pending(true) {
			return
		}

		refused := slices.ContainsFunc(s.members, func(m *member) bool { return m.required && !m.acked })

		if refused && pending(false) {
			return
		}

		st.tng1.Stop()

		st.ack = ackDone
		if refused {
			st.ack = ackProceeded
		}
	case ackExpired:
		if st.oks < s.g.MinimumToStart {
			break
		}

		if s.g.TNG1Action == settings.Abandon {
			s.release(ctx, "required members not acknowledged", s.final(nil, identity.WarnAbandonedRequired))
			return
		}

		st.ack = ackProceeded
	}

	present := slices.ContainsFunc(s.members, func(m *member) bool { return m != s.origin && m.state == joined })

	if st.ack != ackExpired && st.oks >= max(s.g.MinimumToStart, 1) && present {
		s.answer(ctx)
		return
	}

	if !pending(false) {
		s.release(ctx, "no member accepted", s.final(st.failures))
	}
}

// TS 24.379 clause 10.1.1.4.2
// TS 24.379 clause 6.3.3.2.3.2
// TS 24.380 clause 6.3.4.2.2
// TS 24.380 clause 14.3.4
func (s *session) answer(ctx context.Context) {
	var warn []string

	if s.setup.ack == ackProceeded {
		warn = append(warn, identity.WarnProceededWithoutRequired)
	}

	if s.setup.truncated {
		warn = append(warn, identity.WarnTooManyParticipants)
	}

	o := s.origin
	s.joinFloor(o, o.offerParams.FloorSSRC, o.offerParams.HasFloorSSRC, o.offered.Priority, o.offered.ImplicitRequest)
	s.fs.Start(o.offered.Granted)

	if !s.respond(ctx, o, s.orig, s.final(nil, warn...).Header) {
		return
	}

	s.answered, s.started = true, s.c.now()
	s.log.InfoContext(ctx, "group call started", slog.String("originator", o.id.String()), slog.Int("participants", s.count(joined)),
		slog.Int("invited", len(s.members)-1))
}

// TS 24.379 clause 6.3.3.3
func (s *session) expireTNG1(ctx context.Context) {
	if s.answered || s.released || s.setup.ack != ackWaiting {
		return
	}

	s.setup.ack = ackExpired
	s.maybeAnswer(ctx)
}

// TS 24.379 clause 6.3.3.2.4
// TS 24.379 clause 6.3.8.1
func (s *session) left(ctx context.Context, m *member) {
	if m.state == gone {
		return
	}

	participant := m.state == joined
	s.set(m, gone)

	if participant {
		s.logMember(ctx, slog.LevelDebug, "participant left", m)
	}

	if m.fm != nil {
		m.fm.Leave()
		delete(s.floors, m.fm)
	}

	if m.media != nil {
		m.media.Close()
	}

	switch {
	case s.released:
	case m == s.origin && !s.answered:
		s.release(ctx, "cancelled", ua.Reply{})
	case !s.answered:
		s.maybeAnswer(ctx)
	case !participant:
	case m == s.origin && s.g.ReleaseOnInitiatorLeave:
		s.release(ctx, "initiator left", ua.Reply{})
	case s.count(joined) <= 1:
		s.release(ctx, "too few participants", ua.Reply{})
	}
}

// TS 24.379 clause 10.1.1.4.4.2
// TS 24.379 clause 10.1.1.4.4.3
// TS 23.280 clause 10.2.8
func (s *session) revoke(ctx context.Context) {
	if s.released {
		return
	}

	g := s.c.cfg.Source.Current().Group(s.g.ID())
	if g == nil {
		s.release(ctx, "group deleted", ua.Reply{})
		return
	}

	for _, m := range slices.Clone(s.members) {
		if (m.state == invited || m.state == joined) && g.Entry(m.id) == nil {
			s.remove(ctx, m, "no longer a member")

			if s.released {
				return
			}
		}
	}
}

// TS 24.379 clause 10.1.1.4.4.3
// TS 24.379 clause 6.3.3.1.5
func (s *session) remove(ctx context.Context, m *member, cause string) {
	s.logMember(ctx, slog.LevelInfo, "participant removed", m, slog.String("cause", cause))

	if m == s.origin && !s.answered {
		s.release(ctx, "originator "+cause, ua.Reply{})
		return
	}

	if m.leg != nil {
		_ = m.leg.End(ctx, ua.Reply{})
	}

	for _, l := range m.branches {
		if l != m.leg {
			_ = l.End(ctx, ua.Reply{})
		}
	}

	s.left(ctx, m)
}

// TS 24.379 clause 6.3.3.1.5
// TS 24.379 clause 10.1.1.4.4.2
func (s *session) release(ctx context.Context, cause string, rp ua.Reply) {
	if s.released {
		return
	}

	s.released = true

	if s.answered {
		s.log.InfoContext(ctx, "group call released", slog.String("cause", cause), slog.Duration("duration", s.c.now().Sub(s.started)))
	} else {
		s.log.InfoContext(ctx, "group call not started", slog.String("cause", cause))
	}

	for _, t := range []*ua.Timer{s.setup.tng1, s.tng3} {
		if t != nil {
			t.Stop()
		}
	}

	s.fs.Release()

	if !s.answered && s.origin.state == invited {
		if rp.Code == 0 {
			rp.Code = 480
		}

		_ = s.origin.leg.Respond(s.orig.Reply(rp))
	}

	s.call.End(ctx, ua.Reply{})
	s.media.Close()

	s.c.free(s)

	if s.grp.sess == s {
		s.grp.sess = nil
	}
}

// TS 24.379 clause 6.3.2.2.8.1
// TS 24.229 clause 5.7.2
// TS 24.229 clause 5.7.3
// TS 24.229 clause 5.7.1.9
func (s *session) decorate(l *b2bua.Leg, msg sip.Message) {
	req, isReq := msg.(*sip.Request)
	if isReq && req.Method == "BYE" && req.Header.Get("P-Asserted-Identity") == "" {
		req.Header.Add("P-Asserted-Identity", sip.Address{URI: s.snap.ParticipatingPSI()}.String())
	}

	if !l.Outgoing() && ofTargetRefresh(msg) {
		msg.Env().Header.Add("Feature-Caps", identity.ServiceCaps())
	}

	c, ok := s.charging[l]
	if !ok {
		return
	}

	if l.Outgoing() {
		c.OrigIOI = ""
	}

	participating.Charged(&msg.Env().Header, c, s.snap.ServiceConfiguration().Domain, !isReq)
}

// RFC 3261 section 12.2
// RFC 3311 section 5.1
func ofTargetRefresh(msg sip.Message) bool {
	method := func(m string) bool { return m == "INVITE" || m == "UPDATE" }

	if req, ok := msg.(*sip.Request); ok {
		return method(req.Method)
	}

	res, ok := msg.(*sip.Response)
	if !ok || res.StatusCode <= 100 || res.StatusCode >= 300 {
		return false
	}

	cseq, err := res.Header.CSeq()

	return err == nil && method(cseq.Method)
}

type floorOut struct{ s *session }

func (o floorOut) Send(fm *floor.Member, msg floorctl.Message) {
	if m := o.member(fm); m != nil {
		_ = m.media.SendFloor(msg)
	}
}

func (o floorOut) member(fm *floor.Member) *member {
	if m := o.s.floors[fm]; m != nil {
		return m
	}

	return o.s.joining
}

func (o floorOut) Forward(fm *floor.Member, on bool) {
	if m := o.member(fm); m != nil {
		msg := "floor granted"
		if !on {
			msg = "floor released"
		}

		o.s.logMember(context.Background(), slog.LevelDebug, msg, m)
		o.s.media.Forward(m.media, on)
	}
}

// TS 24.379 clause 6.3.8.1
func (o floorOut) Inactive() {
	ctx, span := o.s.detach("group call inactivity")
	defer span.End()

	o.s.release(ctx, "inactivity", ua.Reply{})
}

// TS 24.380 clause 6.3.5.7.3
// TS 24.379 clause 10.1.1.4.4.3
func (o floorOut) Disconnect(fm *floor.Member) {
	if m := o.s.floors[fm]; m != nil {
		ctx, span := o.s.detach("group call media not revoked")

		o.s.logMember(ctx, slog.LevelInfo, "participant disconnected: media not revoked", m)
		tracing.End(span, m.leg.End(ctx, ua.Reply{}))
	}
}

func (s *session) detach(name string) (context.Context, trace.Span) {
	return s.c.detached.Start(s.c.cfg.Agent.Layer().Lifetime(), name, s.cause)
}

// TS 24.379 clause 10.1.1.4.2
func acceptsMCPTT(req *sip.Request) bool {
	acs, err := req.Header.AcceptContact()
	if err != nil {
		return false
	}

	has := func(tag, token string) bool {
		return slices.ContainsFunc(acs, func(p sip.CallerPreference) bool {
			return p.Features.Has(tag) && (token == "" || slices.Contains(p.Features.Tokens(tag), token))
		})
	}

	return has("g.3gpp.mcptt", "") && has("g.3gpp.icsi-ref", identity.ICSI)
}

func infoOf(req *sip.Request) (mcpttinfo.Info, bool) {
	part, ok, err := req.Content().Find(mcpttinfo.ContentType)
	if !ok || err != nil {
		return mcpttinfo.Info{Params: &mcpttinfo.Params{}}, false
	}

	info, err := mcpttinfo.Decode(part.Body)
	if err != nil || info.Params == nil {
		return mcpttinfo.Info{Params: &mcpttinfo.Params{}}, false
	}

	return info, true
}

func sessionSDP(req *sip.Request) []byte {
	part, ok, err := req.Content().Find(sdp.ContentType)
	if !ok || err != nil {
		return nil
	}

	return part.Body
}

func uriOf(c *mcpttinfo.Content) (sip.URI, bool) {
	if c == nil {
		return sip.URI{}, false
	}

	v, ok := c.URI()
	if !ok {
		return sip.URI{}, false
	}

	u, err := sip.ParseURI(v)

	return u, err == nil
}

func textOf(c *mcpttinfo.Content) (string, bool) {
	if c == nil {
		return "", false
	}

	return c.Text()
}

func (s *session) logMember(ctx context.Context, level slog.Level, msg string, m *member, attrs ...slog.Attr) {
	if !s.log.Enabled(ctx, level) {
		return
	}

	s.log.LogAttrs(ctx, level, msg, append(attrs, m.attr())...)
}

func (m *member) attr() slog.Attr {
	var callID string

	switch l := m.leg; {
	case l != nil && l.Outgoing():
		callID = l.Invite().Request().Header.CallID()
	case l != nil:
		callID = l.Request().Message().Header.CallID()
	case len(m.branches) > 0:
		callID = m.branches[0].Invite().Request().Header.CallID()
	}

	return memberAttr(m.id, callID)
}

func memberAttr(id sip.URI, callID string) slog.Attr {
	if callID == "" {
		return slog.Group("member", slog.String("mcptt_id", id.String()))
	}

	return slog.Group("member", slog.String("mcptt_id", id.String()), slog.String("call_id", callID))
}
