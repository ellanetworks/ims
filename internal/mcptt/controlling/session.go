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
)

// session is a group session of a prearranged group call (TS 24.379 §10.1.1.4.2 step 14e), the Value of its call.
type session struct {
	c    *Controlling
	grp  *group
	snap *settings.Snapshot
	g    *settings.Group
	call *b2bua.Call

	// id is the MCPTT session identity, gr its "gr".
	id sip.URI
	gr string
	// log is the function's, with the group, the session identity once allocated, and the Call-ID of the
	// originator's INVITE.
	log *slog.Logger

	sp     speech
	media  *media.Call
	fs     *floor.Server
	floors map[*floor.Member]*member
	// joining is the member whose floor participant joins the floor control server.
	joining *member

	origin *member
	orig   *participating.Origination
	// info is the mcptt-info of the originator's INVITE, the base of those of the members' (§6.3.3.1.2 step 11).
	info    mcpttinfo.Info
	members []*member

	// charging are the charging values of the legs (participating.Charged): of the INVITE of a participant, or of
	// the INVITE of a branch.
	charging map[*b2bua.Leg]identity.Charging

	// setup is the state of the setup, until the originator is answered; answered then.
	setup    setup
	answered bool
	// started is when the originator was answered.
	started  time.Time
	tng3     *ua.Timer
	released bool
}

// setup is the state of the setup of a session (TS 24.379 §10.1.1.4.2 step 14 g, §6.3.3.3), what the originator's
// final response waits for. The rules of the established session (the release policy of §6.3.8.1) do not apply to it.
type setup struct {
	// inviting is set while the members are invited: the setup is evaluated once each was (step 14 g v).
	inviting bool
	// oks is the counter of the 200s of the invited members (step 8); truncated tells not all members were invited
	// (§6.3.5.5).
	oks       int
	truncated bool
	ack       ack
	tng1      *ua.Timer
	// failures are the final responses of the invited members that did not accept, or the errors of their INVITEs.
	failures []b2bua.Failure
}

// ack is the state of the acknowledged call setup (TS 24.379 §6.3.3.3).
type ack int

const (
	// ackNone: no affiliated required member.
	ackNone ack = iota
	// ackWaiting: TNG1 running.
	ackWaiting
	// ackDone: the required members accepted.
	ackDone
	// ackExpired: TNG1 expired, the expiry procedures waiting for <on-network-minimum-number-to-start>.
	ackExpired
	// ackProceeded: the call proceeds without all required members.
	ackProceeded
)

// member is a participant of the session, or a member invited to it: the Value of its leg.
type member struct {
	s        *session
	id       sip.URI
	entry    *settings.Entry
	required bool
	state    state
	// acked tells the invited member accepted: its 200 counted (TS 24.379 §10.1.1.4.2 step 8), whatever came after.
	acked bool

	// leg is the leg of the member: of its INVITE, or, of a member invited, the branch that accepted. branches are
	// the legs of the INVITEs to its clients (participating.Router), pending those not answered yet, live those not
	// ended, and failures the final responses of those that failed.
	leg      *b2bua.Leg
	branches []*b2bua.Leg
	pending  int
	live     int
	failures []b2bua.Failure
	media    *media.Leg
	fm       *floor.Member
	// offered are the fmtp:MCPTT parameters of the server's offer to an invited member, or of its answer to a
	// participant's offer; offerParams those of the participant's offer, answerSDP the answer.
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

// Group is the "SIP INVITE request for controlling MCPTT function of an MCPTT group" of o (TS 24.379 §10.1.1.4.2),
// on the strand of its group.
func (c *Controlling) Group(o *participating.Origination) {
	snap := c.cfg.Source.Current()
	r, req := o.Request, o.Invite
	host := snap.ControllingPSI().Host
	log := c.cfg.Logger.With(slog.String("call_id", r.Message().Header.CallID()))

	// The refusals are logged with their reason, the warning text if any (the INVITE's transaction is logged with
	// its final response).
	refuse := func(code int, warn, reason string) {
		if warn != "" {
			reason = warn
		}

		log.Debug("group call refused", slog.Int("code", code), slog.String("reason", reason))

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
		// Step 6 b i: of a group that is not configured.
		refuse(404, "", "group not configured")
		return
	case c.cfg.Media == nil:
		// Step 1: without a media plane, no call.
		refuse(500, "", "no media plane")
		return
	}

	log = log.With(slog.String("group", gid.String()))

	grp := c.group(gid)
	if r.Strand() != grp.st {
		// A group configured since the request came: not on its strand.
		refuse(500, "", "group configured since the request came")
		return
	}

	// Step 2.
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

	// Step 3.
	if !acceptsMCPTT(req) {
		refuse(403, "", "no MCPTT Accept-Contact")
		return
	}

	now := c.now()

	// Steps 9, 14 a, 15 a: the affiliation check (§6.3.6) of the calling user at its client.
	if !c.cfg.Affiliations.Affiliated(now, gid, caller, client) {
		refuse(403, identity.WarnNotAffiliated, "")
		return
	}

	// Steps 14 b, 15 b: the members may initiate and join (§6.3.5.4, §6.3.5.3): the configuration has no rules.
	entry := g.Entry(caller)

	if s := grp.sess; s != nil {
		if entry == nil {
			refuse(403, identity.WarnNotAuthorisedToJoin, "")
			return
		}

		s.join(o, entry, offer)

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

	// Step 14 g i: the members to invite (§6.3.5.5).
	invitees, code := s.invitees(c.cfg.Affiliations.Members(now, gid), caller)
	if code != 0 {
		refuse(code, identity.WarnAbandonedRequired, "")
		return
	}

	if len(invitees) == 0 {
		// None to invite: no member can accept (TS 24.380 §6.3.2.2).
		refuse(480, "", "no member to invite")
		return
	}

	if err := s.start(entry, offer, ai, ci); err != nil {
		log.Warn("group session not created", slog.Any("error", err))
		refuse(500, "", err.Error())

		return
	}

	// Step 14 g ii, v: TNG1 before the invitations (§6.3.3.3); each member invited before the setup is evaluated, so
	// that one refused at once does not decide for those not invited yet.
	if slices.ContainsFunc(invitees, func(m *member) bool { return m.required }) {
		s.setup.ack = ackWaiting
		s.setup.tng1 = grp.st.AfterFunc(g.TNG1, s.expireTNG1)
	}

	s.setup.inviting = true

	for _, m := range invitees {
		s.invite(m)
	}

	s.setup.inviting = false
	s.maybeAnswer()
}

// start creates the session (TS 24.379 §10.1.1.4.2 step 14e), its MCPTT session identity, TNG3 (§6.3.3.5), the
// floor control server and the media distributor of the call (step 14 g vi), and the originator's leg.
func (s *session) start(entry *settings.Entry, offer *sdp.Session, ai, ci int) error {
	c := s.c

	fs, err := floor.NewServer(floor.ServerConfig{
		Clock: strandClock{st: s.grp.st, c: c}, Out: floorOut{s}, Timers: s.snap.ServerTimers(s.g), SSRC: rand.Uint32(),
	})
	if err != nil {
		return err
	}

	s.fs, s.media = fs, c.cfg.Media.NewCall()
	s.call = b2bua.NewCall(c.cfg.Agent, s.grp.st, b2bua.CallHandler{Ended: func(*b2bua.Call) { s.release("ended", ua.Reply{}) }})
	s.call.SetValue(s)

	m, err := s.participant(s.orig, entry, offer, s.sp.origin, ai, ci, false)
	if err != nil {
		s.media.Close()
		return err
	}

	s.origin = m

	c.allocate(s, s.snap.ControllingPSI())
	s.grp.sess = s
	s.log = s.log.With(slog.String("session", s.id.String()))

	// The group document requires <on-network-maximum-duration> of a prearranged group.
	if s.g.TNG3 > 0 {
		s.tng3 = s.grp.st.AfterFunc(s.g.TNG3, func() { s.release("maximum duration", ua.Reply{}) })
	}

	return nil
}

// invitees are the affiliated members to invite (TS 24.379 §6.3.5.5) but the caller, the required first, at most
// <on-network-max-participant-count> with the caller; or the code to refuse the call with when the group's
// affiliations do not allow it (§10.1.1.4.2 step 14 g i).
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

// participant is the leg of the INVITE of a participant that calls (the originator) or joins, and its media, its
// speech st: the answer to its offer prepared, sent once the session takes it.
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

	// TS 24.380 §14.3: the implicit floor request accepted (§14.3.5), granted in the 200 (§14.3.4) unless
	// receive-only or joining; the priority of §14.3.3. A participant joining by the group's INVITE "attempts to
	// initiate an already existing MCPTT call", its implicit request a Floor Request (§6.3.5.2.2 3); §14.3.5's
	// exception is the rejoin by the session identity (TS 24.379 §10.1.1.4.5), which this server does not serve.
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

	l, err := s.call.Incoming(o.Request, b2bua.LegHandler{Ended: func(*b2bua.Leg, ua.End) { s.left(m) }, Decorate: s.decorate})
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

// add adds m to the members, its user's calls one more (TS 24.379 §10.1.1.3.1.1 step 5).
func (s *session) add(m *member) {
	s.members = append(s.members, m)
	s.c.enter(s, m.id)
}

// set sets the state of m, its user's calls one fewer once it is no longer invited or a participant.
func (s *session) set(m *member, st state) {
	live := func(st state) bool { return st == invited || st == joined }

	if live(m.state) && !live(st) {
		s.c.exit(s, m.id)
	}

	m.state = st
}

// priority is the highest floor priority a member is granted (TS 24.380 §14.3.3): its userPriority; none
// receive-only.
func (s *session) priority(e *settings.Entry) uint8 {
	if e.ReceiveOnly {
		return 0
	}

	return s.userPriority(e)
}

// userPriority is the mc_priority of a member: its <user-priority> (§14.2.3, §14.3.3), within
// <num-levels-priority-hierarchy> and the ABNF's two digits (§12.1.2.3).
func (s *session) userPriority(e *settings.Entry) uint8 {
	return uint8(min(e.UserPriority, *s.snap.ServiceConfiguration().OnNetwork.NumLevelsPriorityHierarchy, controlchannel.MaxPriority))
}

// legConfig is the media of a member, of payload type pt: its floor control messages and media to its floor
// control server participant, on the strand.
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

// join is the INVITE of a member joining the ongoing session (TS 24.379 §10.1.1.4.2 step 15): answered at once from
// its own offer, refused with a 488 without the call's speech (RFC 3264 §6.1), with the warning text 123, its floor
// participant told the floor's state, or its implicit floor request taken as a Floor Request (TS 24.380 §6.3.5.2.2 3).
func (s *session) join(o *participating.Origination, entry *settings.Entry, offer *sdp.Session) {
	host := s.snap.ControllingPSI().Host
	joiner := memberAttr(entry.ID(), o.Request.Message().Header.CallID())

	refuse := func(rp ua.Reply, reason string) {
		s.log.Debug("participant not joined", joiner, slog.Int("code", rp.Code), slog.String("reason", reason))
		_ = o.Request.Respond(o.Reply(rp))
	}

	st, ai, ci, err := s.sp.joining(offer)
	if err != nil {
		refuse(ua.Reply{Code: 488}, "no speech of the call")
		return
	}

	// Step 15 d.
	if s.g.MaxParticipants > 0 && s.count(joined)+s.count(invited) >= s.g.MaxParticipants {
		refuse(ua.Reply{Code: 486, Header: sip.Header{identity.Warning(host, identity.WarnTooManyParticipants)}}, identity.WarnTooManyParticipants)
		return
	}

	m, err := s.participant(o, entry, offer, st, ai, ci, true)
	if err != nil {
		s.log.Warn("participant not added", joiner, slog.Any("error", err))
		refuse(ua.Reply{Code: 500}, err.Error())

		return
	}

	if s.respond(m, o, sip.Header{identity.Warning(host, identity.WarnSessionExists)}) {
		s.logMember(slog.LevelDebug, "participant joined", m)
		s.joinFloor(m, m.offerParams.FloorSSRC, m.offerParams.HasFloorSSRC, m.offered.Priority, m.offered.ImplicitRequest)
		s.supersede(m)
		// During the setup, a participant to answer the originator's call into (§6.3.3.3).
		s.maybeAnswer()
	}
}

// supersede ends the invitations of the user of participant m that none of its clients answered yet: the user in the
// call by its own INVITE (§10.1.1.4.2 step 15), as when it invited the server at the time the server invited it. The
// specs leave a user's two legs in a call open; one leg a user, as of its clients invited (participating.Router), the
// first to accept the member. Each is acknowledged, its user being in the call (step 8, §6.3.3.3).
func (s *session) supersede(m *member) {
	k := settings.Key(m.id)

	for _, o := range s.members {
		if o == m || o == s.origin || o.state != invited || o.leg != nil || settings.Key(o.id) != k {
			continue
		}

		o.acked = true
		s.set(o, gone)
		s.logMember(slog.LevelDebug, "invitation superseded", o)

		for _, l := range o.branches {
			_ = l.End(ua.Reply{})
		}

		if o.media != nil {
			o.media.Close()
		}
	}
}

// respond sends the 200 of the INVITE of a participant (TS 24.379 §6.3.3.2.3.2): the session identity in its Contact
// with the feature tags of the controlling function, the controlling PSI asserted, the SDP answer prepared.
func (s *session) respond(m *member, o *participating.Origination, header sip.Header) bool {
	psi := s.snap.ControllingPSI()

	h := sip.Header{
		{Name: "Contact", Value: identity.WithFeatures(sip.Address{URI: s.id}, identity.Features(true)).String()},
		{Name: "P-Asserted-Identity", Value: sip.Address{URI: psi}.String()},
	}

	err := m.leg.Request().Respond(o.Reply(ua.Reply{Code: 200, Header: append(h, header...), Bodies: []sip.BodyPart{ua.SDP(m.answerSDP)}}))
	if err != nil {
		_ = m.leg.End(ua.Reply{})
		return false
	}

	return true
}

// joinFloor adds the floor participant of m to the floor control server (TS 24.380 §6.3.5.2.2).
func (s *session) joinFloor(m *member, floorSSRC uint32, has bool, priority uint8, implicit bool) {
	m.state = joined
	// What Join sends goes to m, before Join returns its floor participant.
	s.joining = m

	defer func() { s.joining = nil }()

	m.fm = s.fs.Join(floor.MemberConfig{
		ID: m.id.String(), FloorSSRC: floorSSRC, HasFloorSSRC: has, AudioSSRC: m.media.AudioSSRC(), Priority: priority,
		ReceiveOnly: m.entry.ReceiveOnly,
	}, implicit)
	s.floors[m.fm] = m
}

// count is the number of members in state st.
func (s *session) count(st state) int {
	n := 0

	for _, m := range s.members {
		if m.state == st {
			n++
		}
	}

	return n
}

// invite invites a member (TS 24.379 §10.1.1.4.1.1) through the participating function serving it, which invites
// each of its affiliated clients (participating.Terminate): one branch per INVITE, the member one participant. The
// first branch to accept joins the member, the others are ended: a CANCEL to those not answered, a BYE to one that
// answered too, as a UAC ends the dialogs of the 2xx to a forked INVITE it does not keep (RFC 3261 §13.2.2.4). The
// member declines once every branch failed, as by the best of their final responses (RFC 3261 §16.7 step 6).
//
// A member that cannot be invited is declined as by the final response it would have had (§6.3.3.3): the server
// failing, a 500, or the participating function's refusal, its code and Warning header fields
// (§10.1.1.3.1.1: "shall include Warning header field(s) that were received").
func (s *session) invite(m *member) {
	if s.released {
		return
	}

	s.add(m)

	ml, err := s.media.AddLeg(s.legConfig(m, s.sp.codec.Payload, false, true))
	if err != nil {
		s.log.Warn("member not invited: no media", m.attr(), slog.Any("error", err))
		s.declined(m, failure(500, nil))

		return
	}

	m.media = ml

	floorSSRC, _ := ml.FloorSSRC()
	m.offered = controlchannel.Offer(controlchannel.Params{
		FloorSSRC: floorSSRC, HasFloorSSRC: true, Priority: s.userPriority(m.entry),
	})

	req, err := s.memberInvite(m, ml)
	if err != nil {
		s.log.Warn("member not invited", m.attr(), slog.Any("error", err))
		ml.Close()
		s.declined(m, failure(500, nil))

		return
	}

	t := s.c.cfg.Participating.Terminate(req)
	if len(t.Invites) == 0 {
		ml.Close()
		s.declined(m, failure(t.Code, t.Header))

		return
	}

	// t.Unconfirmed is an unconfirmed indication: without media buffering, it starts nothing (§10.1.1.4.2).
	for _, inv := range t.Invites {
		l, err := s.call.Dial(inv, b2bua.LegHandler{
			Established: func(l *b2bua.Leg, _ sip.Message) { s.branchAccepted(m, l) },
			Failed:      func(l *b2bua.Leg, res *sip.Response, err error) { s.branchFailed(m, res, err) },
			Ended:       func(l *b2bua.Leg, _ ua.End) { s.branchEnded(m, l) },
			Decorate:    s.decorate,
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
		s.declined(m, m.failures...)
	}
}

// branchAccepted is the 2xx of branch l of an invited member: the member's leg if the first, ended otherwise.
func (s *session) branchAccepted(m *member, l *b2bua.Leg) {
	m.pending--

	if s.released || m.state != invited || m.leg != nil {
		_ = l.End(ua.Reply{})
		return
	}

	m.leg = l

	for _, o := range m.branches {
		if o != l {
			_ = o.End(ua.Reply{})
		}
	}

	s.accepted(m)
}

// branchFailed is the failure of a branch of an invited member: once every branch failed, the member declined.
func (s *session) branchFailed(m *member, res *sip.Response, err error) {
	m.pending--

	if m.leg != nil {
		return
	}

	m.failures = append(m.failures, b2bua.Failure{Response: res, Err: err})

	if m.pending == 0 {
		s.declined(m, m.failures...)
	}
}

// branchEnded is the end of a branch of a member: the member leaves with its leg, or with its last branch when none
// accepted.
func (s *session) branchEnded(m *member, l *b2bua.Leg) {
	m.live--

	if l == m.leg || m.leg == nil && m.live == 0 {
		s.left(m)
	}
}

// memberInvite is the INVITE to the participating function of a member (TS 24.379 §6.3.3.1.2, §10.1.1.4.1.1 steps 1
// to 5): the session identity in its Contact, Accept-Contact of MCPTT and its ICSI, required and explicit, the ICSI
// asserted, timer supported, the Answer-Mode of the originator's INVITE, its mcptt-info for the member and the group,
// the SDP offer of the call.
func (s *session) memberInvite(m *member, ml *media.Leg) (*sip.Request, error) {
	psi := s.snap.ControllingPSI()

	info := s.info
	p := *info.Params
	p.RequestURI = mcpttinfo.URI(m.id.String())
	p.CallingGroupID = mcpttinfo.URI(s.g.ID().String())
	// Not the originator's client ID: step 11 copies the mcptt-info, but the member's client has no use of it.
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
		// Terms of identity.Features encode.
		v, _ := sip.CallerPreference{Features: sip.FeatureSet{f}, Require: true, Explicit: true}.Value()
		req.Header.Add("Accept-Contact", v)
	}

	req.Header.Add("P-Asserted-Service", identity.ICSI)
	req.Header.Add("P-Asserted-Identity", sip.Address{URI: psi}.String())
	req.Header.Add("Supported", "timer")

	// Steps 9, 10: with the hop elided, the participating function stripped them (§10.1.1.3.1.1 step 4): kept for a
	// SIP binding of MCPTT-3 [PM 11].
	for _, n := range []string{"Answer-Mode", "Priv-Answer-Mode"} {
		if v := s.orig.Invite.Header.Get(n); v != "" {
			req.Header.Add(n, v)
		}
	}

	req.SetBodies(ua.SDP(offer), sip.NewBodyPart(mcpttinfo.ContentType, body))

	return req, nil
}

// accepted is the 200 of an invited member (TS 24.379 §10.1.1.4.1.1): its media and floor participant added to the
// call (TS 24.380 §6.3), the counter of 200s incremented.
func (s *session) accepted(m *member) {
	if s.released || m.state != invited {
		return
	}

	_, b, _ := m.leg.Session().Negotiated()

	rem, params, err := s.sp.takeAnswer(b, m.offered)
	if err != nil {
		// Not the media of the call: the member removed (§6.3.3.1.5), as one refusing it with a 488 (Not Acceptable
		// Here), for the required members' policy too (§6.3.3.3).
		s.declined(m, failure(488, nil))
		_ = m.leg.End(ua.Reply{})

		return
	}

	m.media.SetRemote(rem)
	m.media.SetMultiplexed(params.HasFloorSSRC)
	s.joinFloor(m, params.FloorSSRC, params.HasFloorSSRC, params.Priority, false)
	m.acked = true
	s.setup.oks++
	s.logMember(slog.LevelDebug, "member joined", m)

	s.maybeAnswer()
}

// declined is the failure of the INVITE of an invited member: the final responses of its branches, or the errors of
// their INVITEs.
func (s *session) declined(m *member, fs ...b2bua.Failure) {
	if s.released || m.state != invited {
		return
	}

	s.set(m, failed)

	if s.log.Enabled(context.Background(), slog.LevelDebug) {
		attrs := []slog.Attr{slog.Int("code", b2bua.Best(fs).Code)}
		if i := slices.IndexFunc(fs, func(f b2bua.Failure) bool { return f.Err != nil }); i >= 0 {
			attrs = append(attrs, slog.Any("error", fs[i].Err))
		}

		s.logMember(slog.LevelDebug, "member declined", m, attrs...)
	}

	if s.answered {
		return
	}

	s.setup.failures = append(s.setup.failures, fs...)

	// §6.3.3.3: a required member refusing, the call abandoned by the policy of TNG1's expiry (NOTE 3), its response
	// forwarded with the warning text 112.
	if m.required && s.setup.ack == ackWaiting && s.g.TNG1Action == settings.Abandon {
		s.release("required member declined", s.final(fs, identity.WarnAbandonedRequired))
		return
	}

	s.maybeAnswer()
}

// final is the originator's final response for the failures of the members, with the warning texts of the
// controlling function warn: "the SIP final response" received, sent "according to 3GPP TS 24.229" (TS 24.379
// §10.1.1.4.2), the best of them as a forking proxy chooses it (RFC 3261 §16.7 step 6, b2bua.Best), with their
// Warning header fields. Without a failure, the members that accepted having left, a 480 (Temporarily Unavailable):
// no participant to answer the call into.
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

// failure is a final response of a member the function did not receive: code, for a member refused or failed
// before its INVITE was sent, with header, the participating function's Warning header fields.
func failure(code int, header sip.Header) b2bua.Failure {
	if code < 300 {
		code = 500
	}

	res := &sip.Response{StatusCode: code}
	res.Header = header.Clone()

	return b2bua.Failure{Response: res}
}

// maybeAnswer evaluates the setup (TS 24.379 §10.1.1.4.2, §6.3.3.3) once every member was invited, and again on each
// response of a member and each member leaving. It answers the originator once the call can start: without media
// buffering, once an invited member accepted, <on-network-minimum-number-to-start> did, and a participant is left to
// answer the call into; with required members, once they accepted, or TNG1 expired and the call proceeds. Once no
// invitation is left and the call cannot start, the originator gets the final response of the members.
func (s *session) maybeAnswer() {
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

		// A required member refused and the call proceeds without it once the others responded (§6.3.3.3 NOTE 4).
		if refused && pending(false) {
			return
		}

		st.tng1.Stop()

		st.ack = ackDone
		if refused {
			st.ack = ackProceeded
		}
	case ackExpired:
		// The expiry procedures wait for <on-network-minimum-number-to-start>.
		if st.oks < s.g.MinimumToStart {
			break
		}

		if s.g.TNG1Action == settings.Abandon {
			// §6.3.3.3 2.
			s.release("required members not acknowledged", s.final(nil, identity.WarnAbandonedRequired))
			return
		}

		st.ack = ackProceeded
	}

	present := slices.ContainsFunc(s.members, func(m *member) bool { return m != s.origin && m.state == joined })

	if st.ack != ackExpired && st.oks >= max(s.g.MinimumToStart, 1) && present {
		s.answer()
		return
	}

	if !pending(false) {
		s.release("no member accepted", s.final(st.failures))
	}
}

// answer sends the 200 of the originator (TS 24.379 §10.1.1.4.2, §6.3.3.2.3.2), with the warning text 111 of a
// call proceeding without its required members (§6.3.3.3 1 a ii) and 122 of members not invited, and starts the floor
// control: the originator's implicit floor request, granted in the 200 by mc_granted (TS 24.380 §6.3.4.2.2, §14.3.4).
// The session is established: the setup's rules end, the release policy of §6.3.8.1 starts.
func (s *session) answer() {
	var warn []string

	if s.setup.ack == ackProceeded {
		warn = append(warn, identity.WarnProceededWithoutRequired)
	}

	if s.setup.truncated {
		warn = append(warn, identity.WarnTooManyParticipants)
	}

	// The floor control first (TS 24.380 §6.3.4.2.2): granted by the 200 (mc_granted), the originator is in 'G: Floor
	// Taken' when it learns so, its media forwarded from its first packet; what the floor control sends it before the
	// 200, the participant takes once established (§6.2.4.2.2 step 2). A 200 not sent ends the leg, which releases the
	// session, not answered.
	o := s.origin
	s.joinFloor(o, o.offerParams.FloorSSRC, o.offerParams.HasFloorSSRC, o.offered.Priority, o.offered.ImplicitRequest)
	s.fs.Start(o.offered.Granted)

	if !s.respond(o, s.orig, s.final(nil, warn...).Header) {
		return
	}

	s.answered, s.started = true, s.c.now()
	s.log.Info("group call started", slog.String("originator", o.id.String()), slog.Int("participants", s.count(joined)),
		slog.Int("invited", len(s.members)-1))
}

// expireTNG1 is the expiry of TNG1 (TS 24.379 §6.3.3.3).
func (s *session) expireTNG1() {
	if s.answered || s.released || s.setup.ack != ackWaiting {
		return
	}

	s.setup.ack = ackExpired
	s.maybeAnswer()
}

// left is the end of the leg of a member: its BYE (§6.3.3.2.4), the end of its session, or the failure of its
// INVITE. Its media and floor participant are released. During the setup, the originator leaving (its CANCEL)
// releases the session, and another member leaving has the setup evaluated again (§6.3.3.3); once established, the
// session is released by its release policy (§6.3.8.1).
func (s *session) left(m *member) {
	if m.state == gone {
		return
	}

	participant := m.state == joined
	s.set(m, gone)

	if participant {
		s.logMember(slog.LevelDebug, "participant left", m)
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
		// The originator cancelled.
		s.release("cancelled", ua.Reply{})
	case !s.answered:
		s.maybeAnswer()
	case !participant:
	case m == s.origin && s.g.ReleaseOnInitiatorLeave:
		// §6.3.8.1 3.
		s.release("initiator left", ua.Reply{})
	case s.count(joined) <= 1:
		// §6.3.8.1 2.
		s.release("too few participants", ua.Reply{})
	}
}

// release releases the session (TS 24.379 §6.3.3.1.5, §10.1.1.4.4.2) for cause, as logged: its floor control and
// media released, a BYE to each participant, a CANCEL to each member not answered yet, the originator not answered
// rp; TNG1 and TNG3 stopped, the session identity freed.
func (s *session) release(cause string, rp ua.Reply) {
	if s.released {
		return
	}

	s.released = true

	if s.answered {
		s.log.Info("group call released", slog.String("cause", cause), slog.Duration("duration", s.c.now().Sub(s.started)))
	} else {
		s.log.Info("group call not started", slog.String("cause", cause))
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

	s.call.End(ua.Reply{})
	s.media.Close()

	s.c.free(s)

	if s.grp.sess == s {
		s.grp.sess = nil
	}
}

// decorate completes the messages of the legs: a BYE of the function has the participating function's PSI asserted
// (TS 24.379 §6.3.2.2.8.1 step 3): the BYE is the participating function's, the hop elided [PM 11]. Through the
// IMS, each message has the P-Charging-Vector of the AS (participating.Charged): of the INVITE of a participant, the
// server its terminating UA (TS 24.229 §5.7.2), of the INVITE of a branch, the server its originating UA (§5.7.3).
// A response to a request of the peer within the dialog has the icid-value of the dialog, the orig-ioi of the INVITE
// of a participant: Decorate does not see the request it answers.
//
// The dialog of a participant, of the server on behalf of an originating user, has the ICSI in a Feature-Caps
// (identity.ServiceCaps; TS 24.229 §5.7.1.9): each 1xx or 2xx response to its initial INVITE and to its target
// refresh requests, and the target refresh requests of the server.
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

// ofTargetRefresh reports whether msg is an INVITE or an UPDATE, a target refresh request (RFC 3261 §12.2, RFC 3311
// §5.1), or a 1xx or 2xx response to one, but a 100 (Trying), hop by hop.
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

// floorOut is the floor control server's output on the call.
type floorOut struct{ s *session }

func (o floorOut) Send(fm *floor.Member, msg floorctl.Message) {
	if m := o.member(fm); m != nil {
		_ = m.media.SendFloor(msg)
	}
}

// member is the member of floor participant fm.
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

		o.s.logMember(slog.LevelDebug, msg, m)
		o.s.media.Forward(m.media, on)
	}
}

// Inactive is the expiry of T4 (Inactivity), which releases the call (TS 24.379 §6.3.8.1 1).
func (o floorOut) Inactive() { o.s.release("inactivity", ua.Reply{}) }

// Disconnect removes a member whose media the server could not revoke (TS 24.380 §6.3.5.7.3 NOTE; TS 24.379
// §10.1.1.4.4.3).
func (o floorOut) Disconnect(fm *floor.Member) {
	if m := o.s.floors[fm]; m != nil {
		o.s.logMember(slog.LevelInfo, "participant disconnected: media not revoked", m)
		_ = m.leg.End(ua.Reply{})
	}
}

// acceptsMCPTT reports whether the Accept-Contact of req has the MCPTT feature tag and its ICSI (TS 24.379
// §10.1.1.4.2 step 3).
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

// logMember logs msg of m, with attrs, if level is enabled.
func (s *session) logMember(level slog.Level, msg string, m *member, attrs ...slog.Attr) {
	if !s.log.Enabled(context.Background(), level) {
		return
	}

	s.log.LogAttrs(context.Background(), level, msg, append(attrs, m.attr())...)
}

// attr is the member as logged: its MCPTT ID and the Call-ID of its leg, else of its first branch, if any.
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
