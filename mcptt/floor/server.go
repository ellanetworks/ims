package floor

import (
	"errors"
	"fmt"
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
)

// ServerTimers are the timers and counters of the floor control server (§11.1.3, §11.2.3), as the service
// configuration (TS 24.484 <fc-timers-counters>, <transmit-time>) and the group document (TS 24.481) set them, their
// defaults resolved by the configuration: DefaultServerTimers.
type ServerTimers struct {
	// T1 (End of RTP media), T2 (Stop talking), T3 (Stop talking grace), T4 (Inactivity), T7 (Floor Idle), T8
	// (Floor Revoke).
	T1, T2, T3, T4, T7, T8 time.Duration
	// C7 (Floor Idle) is the number of Floor Idle messages a floor idle period is announced with.
	C7 int
}

// DefaultServerTimers are the defaults of Table 11.1.3-1 and Table 11.2.3-1: T3 of a group not configured for audio
// cut-in. T7 depends on the radio access network and has none.
var DefaultServerTimers = ServerTimers{
	T1: 4 * time.Second, T2: 30 * time.Second, T3: 3 * time.Second, T4: 30 * time.Second, T8: time.Second, C7: 10,
}

// check reports timers the server cannot run: T1, T2, T7 and T8 of zero, which mean nothing, T1 above its maximum
// (Table 11.1.3-1) or below MinT1, negative ones. T3 of zero is a group's with audio cut-in, T4 of zero releases a call as soon as
// the floor is idle, C7 of zero announces it once.
func (t ServerTimers) check() error {
	if t.T7 <= 0 {
		return errors.New("floor: T7 (Floor Idle) not configured")
	}

	if t.T1 > MaxT1 {
		return fmt.Errorf("floor: T1 (End of RTP media) of %s above its maximum of %s", t.T1, MaxT1)
	}

	for _, d := range []struct {
		name string
		v    time.Duration
		zero bool
	}{{"T1", t.T1, false}, {"T2", t.T2, false}, {"T3", t.T3, true}, {"T4", t.T4, true}, {"T8", t.T8, false}} {
		if d.v < 0 || d.v == 0 && !d.zero {
			return fmt.Errorf("floor: %s of %s", d.name, d.v)
		}
	}

	if t.T1 < MinT1 {
		return fmt.Errorf("floor: T1 (End of RTP media) of %s below %s", t.T1, MinT1)
	}

	if t.C7 < 0 {
		return fmt.Errorf("floor: C7 of %d", t.C7)
	}

	return nil
}

// MaxT1 is the maximum of T1 (Table 11.1.3-1).
const MaxT1 = 6 * time.Second

// MinT1 is the least T1 the server runs, which Table 11.1.3-1 does not bound: media is reported to it at a coarser
// rate than per packet (Member.Media; 200 ms by the media plane), and a T1 near that rate would end the grant of a
// participant that talks.
const MinT1 = time.Second

// ServerOut is what the floor control server does outside its machines.
type ServerOut interface {
	// Send sends msg to the floor participant of m.
	Send(m *Member, msg floorctl.Message)
	// Forward has the media distributor distribute the RTP media of m to the other participants, or no longer
	// (§6.3.4, §6.3.5): m has permission to send media, or is in its grace period.
	Forward(m *Member, on bool)
	// Inactive tells the call that timer T4 (Inactivity) expired (§6.3.4.3.5): the call may release itself, by
	// Release; the server restarts T4 otherwise.
	Inactive()
	// Disconnect tells the call that the server gave up revoking the media of m, which kept sending without
	// permission, as §6.3.5.7.3 NOTE recommends: the call disconnects m from the call.
	Disconnect(m *Member)
}

// ServerConfig is the configuration of a floor control server.
type ServerConfig struct {
	Clock  Clock
	Out    ServerOut
	Timers ServerTimers
	// SSRC is the RTCP SSRC of the server (§6.3.2.2), in its messages to a participant that did not assign it one
	// (§8.1.2, §14.3.8).
	SSRC uint32
	// Drain is how long the media of a member that released the floor is still distributed, its last packets
	// (Annex A.3.2 steps 9 to 12), before the floor is idle; 200 ms if zero. It also absorbs a report of media that
	// the media plane sent before the Floor Release and the call received after.
	Drain time.Duration
	// Revokes is how many Floor Revokes in a row a participant without permission keeps sending media after before
	// the server gives up on it, an implementation option (§6.3.5.7.3 NOTE); 3 if zero. In the grace period the
	// server does not give up: T3 or a Floor Release ends it (§6.3.5.6.3 NOTE, Table 11.1.3-1: their T8).
	Revokes int
}

// MemberConfig is a floor participant of a call, as negotiated at its session establishment.
type MemberConfig struct {
	// ID is the MCPTT ID of the participant's user, its Granted Party's Identity (§8.2.3.6).
	ID string
	// FloorSSRC is the SSRC the participant assigned the server, in its mc_floor_ssrc (§14.2.7, §14.3.8): the
	// SSRC of the server's messages to it. HasFloorSSRC tells it did.
	FloorSSRC    uint32
	HasFloorSSRC bool
	// AudioSSRC is the SSRC the server assigns the participant's RTP media when it has the floor (§6.3.4.4.2 1f,
	// §14.3.6), announced in Floor Granted and Floor Taken.
	AudioSSRC uint32
	// Priority is the highest floor priority the participant may request (§14.3.3); 0, the default priority.
	Priority uint8
	// ReceiveOnly is the <on-network-recvonly> of the participant's group entry (TS 24.481): it may not talk.
	ReceiveOnly bool
}

type generalState int

const (
	gStartStop generalState = iota
	gIdle
	gTaken
	gPendingRevoke
	gReleasing
)

type memberState int

const (
	uNotPermittedIdle memberState = iota
	uNotPermittedTaken
	uPermitted
	uPendingRevoke
	uNotPermittedSendsMedia
	uReleasing
)

// Server is the floor control server of one call (§6.3): its 'general floor control operation' (§6.3.4), and its
// 'basic floor control operation towards the floor participant' for each member (§6.3.5). Of a call that is not a
// broadcast group call, system call, emergency or imminent peril call: its messages have no Floor Indicator
// (§8.2.3.15).
type Server struct {
	cfg     ServerConfig
	state   generalState
	members []*Member

	// granted is the member with permission to send media, in 'G: Floor Taken' and 'G: pending Floor Revoke'.
	granted  *Member
	priority uint8
	// location is the Location field of the granted member's request, for the Floor Taken (§6.3.4.4.2 3a iii).
	location floorctl.Field
	seq      uint16
	// pending is the originator whose implicit request waits for another member (§6.3.2.2), after Start.
	pending *Member

	t1, t2, t3, t4, t7 timer
	c7                 int
	// drain is the time the media of the member that released the floor is still distributed.
	drain timer
}

// Member is a floor participant of the call, and the server's interface towards it (§6.3.5).
type Member struct {
	s     *Server
	cfg   MemberConfig
	state memberState

	// implicit is an implicit floor request the member's session made, before the call started.
	implicit bool
	// released is a Floor Release received in 'U: permitted' (§6.3.5.3.8), until the member leaves 'U: not
	// permitted and Floor Idle'.
	released bool
	// revoke is the Floor Revoke the member was last sent, T8 resending it (§6.3.5.6.3, §6.3.5.7.3); sentMedia
	// tells media came since; unheeded counts the Floor Revokes in a row media came after, in 'U: not permitted but
	// sends media'.
	revoke    floorctl.Message
	sentMedia bool
	unheeded  int
	t8        timer
}

// NewServer is the floor control server of a call, its general machine in 'Start-stop' (§6.3.4.2.1).
func NewServer(cfg ServerConfig) (*Server, error) {
	if err := cfg.Timers.check(); err != nil {
		return nil, err
	}

	if cfg.Drain <= 0 {
		cfg.Drain = 200 * time.Millisecond
	}

	if cfg.Revokes <= 0 {
		cfg.Revokes = 3
	}

	return &Server{cfg: cfg}, nil
}

// Config is the configuration of m.
func (m *Member) Config() MemberConfig { return m.cfg }

// Join adds the floor participant of a session established in the call (§6.3.5.2.2): the 200 the originator was
// sent, or the 200 of an invited member. implicit is the session's implicit floor request (§14.3.5). Before Start,
// a member waits in 'U: not permitted and Floor Idle'; after, it is told the floor's state, an implicit request
// granted if the floor is idle.
func (s *Server) Join(cfg MemberConfig, implicit bool) *Member {
	m := &Member{s: s, cfg: cfg}
	if s.state == gReleasing {
		m.state = uReleasing
		return m
	}

	s.members = append(s.members, m)

	switch {
	case s.pending != nil:
		// §6.3.2.2: the originator's implicit request granted when the first invited member accepts.
		p := s.pending
		s.pending = nil
		s.request(p, implicitRequest(p))
	case s.state == gStartStop:
		// §6.3.5.2.2 2b: the general machine starts with the call.
		m.implicit = implicit
	case s.state == gIdle && implicit:
		// §6.3.5.2.2 3a: as a Floor Request.
		s.request(m, implicitRequest(m))
	case s.state == gIdle:
		// §6.3.5.2.2 2a, 4b.
		s.send(m, s.idle())
	default:
		// §6.3.5.2.2 2c, 3c, 4a.
		s.send(m, s.taken())
	}

	return m
}

// Start initialises the general machine (§6.3.4.2.2), once the call is initiated as TS 24.379 says: with a
// confirmed indication required, when an invited member accepted; otherwise at once. granted is the mc_granted of
// the originator's 200 (§14.3.4), which grants its implicit floor request: the call gives it only when it buffers
// media or another member accepted (§6.3.2.2). Without it, the request is taken as a Floor Request (§6.3.4.3.3),
// once another member is in the call (§6.3.2.2). Without an implicit request the floor is idle.
func (s *Server) Start(granted bool) {
	if s.state != gStartStop {
		return
	}

	var originator *Member

	for _, m := range s.members {
		if m.implicit {
			originator, m.implicit = m, false

			break
		}
	}

	s.state = gIdle

	switch {
	case originator != nil && granted:
		s.grant(originator, s.requested(originator, implicitRequest(originator)), notProvided)
	case originator != nil && len(s.members) == 1:
		s.pending = originator
	case originator != nil && s.request(originator, implicitRequest(originator)):
	default:
		// No implicit request, or one denied (§6.3.4.3.3 2b): the floor idle.
		s.enterIdle()
	}
}

// Release releases the floor control of the call (§6.3.4.6.2, §6.3.5.8.2, §6.3.4.7.2): no more messages or media.
func (s *Server) Release() {
	if s.state == gReleasing {
		return
	}

	if s.granted != nil {
		s.cfg.Out.Forward(s.granted, false)
	}

	s.state, s.granted, s.pending = gReleasing, nil, nil
	s.stopTimers()

	for _, m := range s.members {
		m.state = uReleasing
		m.t8.stop()
	}
}

func (s *Server) stopTimers() {
	for _, t := range []*timer{&s.t1, &s.t2, &s.t3, &s.t4, &s.t7, &s.drain} {
		t.stop()
	}
}

// Receive is a floor control message from the floor participant of m.
func (m *Member) Receive(msg floorctl.Message) {
	s := m.s
	if m.state == uReleasing || s.state == gStartStop {
		return
	}

	switch msg.Type {
	case floorctl.Request:
		m.receiveRequest(msg)
	case floorctl.Release:
		m.receiveRelease(msg)
	}
	// Floor Ack: what to do of one, or of none, is an implementation option (§6.3.5.3.3 NOTE); other messages
	// have no procedure in basic operation (§6.3.5.1): discarded.
}

func (m *Member) receiveRequest(msg floorctl.Message) {
	s := m.s

	switch m.state {
	case uNotPermittedIdle:
		// §6.3.5.3.4: to the arbitration logic.
		if s.state == gIdle {
			s.request(m, msg)
		}
	case uNotPermittedTaken:
		// §6.3.5.4.4: no queueing, no pre-emption.
		cause := floorctl.DenyAnotherHasPermission
		if m.cfg.ReceiveOnly {
			cause = floorctl.DenyReceiveOnly
		}

		s.send(m, s.deny(cause))
	case uPermitted:
		// After its release, while its last media is distributed: a new grant, as of an idle floor.
		if s.granted == m && s.drain.running() {
			s.drain.stop()
			s.grant(m, s.requested(m, msg), location(msg))

			return
		}

		// §6.3.5.5.7, §6.3.4.4.8: the grant again, with the time left to talk.
		if s.granted == m {
			g := s.grantedMessage(m)
			if s.t2.running() {
				g.Duration = ptr(seconds(s.t2.remaining(s.cfg.Clock)))
			}

			s.send(m, g)
		}
	}
}

func (m *Member) receiveRelease(msg floorctl.Message) {
	s := m.s

	if a, ok := ack(msg, floorctl.SourceControlling, s.ssrcFor(m)); ok {
		s.cfg.Out.Send(m, a)
	}

	switch m.state {
	case uNotPermittedIdle:
		// §6.3.5.3.7.
		s.send(m, s.idle())
	case uNotPermittedTaken:
		// §6.3.5.4.5.
		s.send(m, s.taken())
	case uPermitted:
		// §6.3.5.5.3, §6.3.4.4.6, after the last media of the member (A.3.2 steps 9 to 12).
		m.released = true
		if s.granted == m && s.state == gTaken && !s.drain.running() {
			s.t2.stop()
			s.drain.start(s.cfg.Clock, s.cfg.Drain, s.expireDrain)
		}
	case uPendingRevoke:
		// §6.3.5.6.5, §6.3.4.5.4.
		m.t8.stop()

		if s.granted == m {
			s.t3.stop()
			s.endGrant()
		}
	case uNotPermittedSendsMedia:
		// §6.3.5.7.4.
		m.t8.stop()

		if s.state == gIdle {
			m.state = uNotPermittedIdle
			s.send(m, s.idle())
		} else {
			m.state = uNotPermittedTaken
			s.send(m, s.taken())
		}
	}
}

// Media is RTP media received from the floor participant of m, of payload. The media layer may report it at a
// coarser rate than per packet, well below T1.
func (m *Member) Media() {
	s := m.s

	switch m.state {
	case uPermitted, uPendingRevoke:
		// §6.3.5.5.6, §6.3.5.6.4: distributed; §6.3.4.4.5, §6.3.4.5.3.
		if s.granted != m {
			return
		}

		if s.drain.running() {
			// Its last media, after its release.
			return
		}

		if s.state == gTaken && !s.t2.running() {
			s.t2.start(s.cfg.Clock, s.cfg.Timers.T2, s.expireT2)
		}

		s.t1.start(s.cfg.Clock, s.cfg.Timers.T1, s.expireT1)
	case uNotPermittedTaken:
		// §6.3.5.4.6.
		m.revokeNoPermission()
	case uNotPermittedSendsMedia:
		// §6.3.5.7.2: not distributed; counted towards giving up.
		m.sentMedia = true
	case uNotPermittedIdle:
		// §6.3.5.3.8: after a Floor Release from 'U: permitted'.
		if m.released {
			m.revokeNoPermission()
		}
	}
}

// revokeNoPermission revokes media sent without permission: not distributed (§6.3.5.4.6, §6.3.5.7.2).
func (m *Member) revokeNoPermission() {
	s := m.s
	m.state = uNotPermittedSendsMedia
	m.revoke, m.unheeded = s.revokeMessage(floorctl.RevokeNoPermission), 0
	s.send(m, m.revoke)
}

func (m *Member) expireT8() {
	m.t8.fired()

	switch m.state {
	case uPendingRevoke:
		// §6.3.5.6.3: resent, without giving up; T3 or a Floor Release stops T8 (Table 11.1.3-1).
	case uNotPermittedSendsMedia:
		// §6.3.5.7.3: resent; the number of times and the action on giving up are implementation options (NOTE):
		// given up on, with the disconnection recommended, only while the participant keeps sending media.
		if m.sentMedia {
			m.unheeded++
		} else {
			m.unheeded = 0
		}

		if m.unheeded >= m.s.cfg.Revokes {
			m.s.cfg.Out.Disconnect(m)
			return
		}
	default:
		return
	}

	m.s.send(m, m.revoke)
}

// Leave is the departure of the member's session from the call (§6.3.3, §6.3.5.8.2): no more messages or media,
// and the floor released if it had it (§6.3.4.4.11).
func (m *Member) Leave() {
	s := m.s
	if m.state == uReleasing {
		return
	}

	m.state = uReleasing
	m.t8.stop()

	if s.pending == m {
		s.pending = nil
	}

	for i, o := range s.members {
		if o == m {
			s.members = append(s.members[:i], s.members[i+1:]...)
			break
		}
	}

	if s.granted == m {
		s.cfg.Out.Forward(m, false)
		s.t3.stop()
		s.t2.stop()
		s.drain.stop()
		s.endGrant()
	}
}

// request is a Floor Request in 'G: Floor Idle' (§6.3.4.3.3), and reports whether it granted it.
func (s *Server) request(m *Member, msg floorctl.Message) bool {
	switch {
	case len(s.members) == 1:
		s.send(m, s.deny(floorctl.DenyOnlyOneParticipant))
		return false
	case m.cfg.ReceiveOnly:
		s.send(m, s.deny(floorctl.DenyReceiveOnly))
		return false
	}

	s.t4.stop()
	s.t7.stop()
	s.grant(m, s.requested(m, msg), location(msg))

	return true
}

// implicitRequest is the implicit floor request of m's session as a Floor Request (§6.3.4.2.2 3a i, §6.3.5.2.2 3a),
// of the maximum priority m negotiated (§6.3.5.2.2 3b i, NOTE 4), whether mc_granted grants it or not; of a Location
// "Not provided".
func implicitRequest(m *Member) floorctl.Message {
	return floorctl.Message{Type: floorctl.Request, Priority: ptr(m.cfg.Priority)}
}

// requested is the priority a Floor Request of m is granted (§6.3.5.4.4 1a): the lower of the priority requested and
// the one negotiated; the default without one.
func (s *Server) requested(m *Member, msg floorctl.Message) uint8 {
	if msg.Priority != nil && m.cfg.Priority != 0 {
		return min(*msg.Priority, m.cfg.Priority)
	}

	return 0
}

// notProvided is a Location field of the location type "Not provided" (Table 8.2.3.21-3).
var notProvided = floorctl.Field{ID: floorctl.FieldLocation, Value: []byte{0}}

// location is the Location field of a Floor Request, "Not provided" without one: of an implicit request.
func location(msg floorctl.Message) floorctl.Field {
	for _, f := range msg.Other {
		if f.ID == floorctl.FieldLocation && len(f.Value) > 0 {
			return f
		}
	}

	return notProvided
}

// grant enters 'G: Floor Taken' for m (§6.3.4.4.2), of its request's priority and location.
func (s *Server) grant(m *Member, priority uint8, loc floorctl.Field) {
	s.state, s.granted, s.priority, s.location = gTaken, m, priority, loc
	m.released = false

	s.send(m, s.grantedMessage(m))

	taken := s.taken()
	for _, o := range s.members {
		if o != m {
			s.send(o, taken)
		}
	}

	s.t1.start(s.cfg.Clock, s.cfg.Timers.T1, s.expireT1)
}

// endGrant ends the permission of the granted member: its media no longer distributed, the floor idle
// (§6.3.4.4.3, §6.3.4.4.6, §6.3.4.5.4, §6.3.4.5.5).
func (s *Server) endGrant() {
	m := s.granted
	if m.state != uReleasing {
		s.cfg.Out.Forward(m, false)
	}

	s.granted = nil
	s.t1.stop()
	s.drain.stop()
	s.enterIdle()
}

// expireDrain ends the grant of a member that released the floor, its last media distributed.
func (s *Server) expireDrain() {
	s.drain.fired()

	if s.granted != nil && s.state == gTaken {
		s.endGrant()
	}
}

// enterIdle enters 'G: Floor Idle' (§6.3.4.3.2), from 'G: Floor Taken' or 'G: pending Floor Revoke' or at start.
func (s *Server) enterIdle() {
	s.state = gIdle

	idle := s.idle()
	for _, m := range s.members {
		s.send(m, idle)
	}

	s.c7 = 1
	s.t7.start(s.cfg.Clock, s.cfg.Timers.T7, s.expireT7)
	s.t4.start(s.cfg.Clock, s.cfg.Timers.T4, s.expireT4)
}

func (s *Server) expireT1() {
	s.t1.fired()

	switch s.state {
	case gTaken:
		// §6.3.4.4.3.
		s.t2.stop()
		s.endGrant()
	case gPendingRevoke:
		// §6.3.4.5.6.
		s.t3.stop()
		s.endGrant()
	}
}

func (s *Server) expireT2() {
	s.t2.fired()

	if s.state != gTaken {
		return
	}

	// §6.3.4.4.4, §6.3.4.5.2: the Floor Revoke, and the grace period.
	s.t1.stop()
	s.state = gPendingRevoke

	m := s.granted
	m.revoke = s.revokeMessage(floorctl.RevokeMediaBurstTooLong)
	s.send(m, m.revoke)
	s.t3.start(s.cfg.Clock, s.cfg.Timers.T3, s.expireT3)
}

func (s *Server) expireT3() {
	s.t3.fired()

	if s.state != gPendingRevoke {
		return
	}

	// §6.3.4.5.5: no more media allowed; the Floor Idle stops T8 (Table 11.1.3-1).
	s.endGrant()
}

func (s *Server) expireT4() {
	s.t4.fired()

	if s.state != gIdle {
		return
	}

	// §6.3.4.3.5: the call may release itself.
	s.cfg.Out.Inactive()

	if s.state == gIdle && !s.t4.running() {
		s.t4.start(s.cfg.Clock, s.cfg.Timers.T4, s.expireT4)
	}
}

func (s *Server) expireT7() {
	s.t7.fired()

	// §6.3.4.3.4.
	if s.state != gIdle || s.c7 >= s.cfg.Timers.C7 {
		return
	}

	s.c7++
	s.t7.start(s.cfg.Clock, s.cfg.Timers.T7, s.expireT7)

	idle := s.idle()
	for _, m := range s.members {
		s.send(m, idle)
	}
}

// send sends msg from the arbitration logic to m through its interface, which moves on as §6.3.5 says.
func (s *Server) send(m *Member, msg floorctl.Message) {
	if m.state == uReleasing {
		return
	}

	switch msg.Type {
	case floorctl.Granted:
		if m.state != uPermitted {
			m.state = uPermitted
			s.cfg.Out.Forward(m, true)
		}
	case floorctl.Taken:
		switch m.state {
		case uNotPermittedIdle, uPermitted, uPendingRevoke:
			// §6.3.5.3.3, §6.3.5.5.9, §6.3.5.6.7.
			m.state, m.released = uNotPermittedTaken, false
			m.t8.stop()
		case uNotPermittedSendsMedia:
			// §6.3.5.7: no procedure but of multi-talker.
			return
		}
	case floorctl.Idle:
		switch m.state {
		case uNotPermittedTaken, uPermitted, uPendingRevoke:
			// §6.3.5.4.3, §6.3.5.5.4 (A.3.2 step 10: the released member told, as its 'U: pending Release'
			// waits for, §6.2.4.6.4), §6.3.5.6.6.
			m.state = uNotPermittedIdle
			m.t8.stop()
		case uNotPermittedSendsMedia:
			// §6.3.5.7: no procedure; the member stays until it releases.
			return
		}
	case floorctl.Revoke:
		if m.state == uPermitted {
			// §6.3.5.5.5.
			m.state = uPendingRevoke
		}

		m.sentMedia = false
		m.t8.start(s.cfg.Clock, s.cfg.Timers.T8, m.expireT8)
	}

	msg.SSRC = s.ssrcFor(m)
	s.cfg.Out.Send(m, msg)
}

func (s *Server) ssrcFor(m *Member) uint32 {
	if m.cfg.HasFloorSSRC {
		return m.cfg.FloorSSRC
	}

	return s.cfg.SSRC
}

// grantedMessage is the Floor Granted of m (§6.3.4.4.2 1).
func (s *Server) grantedMessage(m *Member) floorctl.Message {
	return floorctl.Message{
		Type:        floorctl.Granted,
		Duration:    ptr(seconds(s.cfg.Timers.T2)),
		Priority:    ptr(s.priority),
		GrantedSSRC: ptr(m.cfg.AudioSSRC),
	}
}

// taken is the Floor Taken of the granted member (§6.3.4.4.2 3), its sequence number the next.
func (s *Server) taken() floorctl.Message {
	s.seq++

	return floorctl.Message{
		Type:           floorctl.Taken,
		GrantedPartyID: s.granted.cfg.ID,
		// §6.3.4.4.2 3a: '1', of a call that is not a broadcast group call.
		Permission:     ptr(true),
		SequenceNumber: ptr(s.seq),
		GrantedSSRC:    ptr(s.granted.cfg.AudioSSRC),
		Other:          []floorctl.Field{s.location},
	}
}

// idle is a Floor Idle (§6.3.4.3.2 2a), its sequence number the next.
func (s *Server) idle() floorctl.Message {
	s.seq++

	return floorctl.Message{Type: floorctl.Idle, SequenceNumber: ptr(s.seq)}
}

func (s *Server) deny(cause floorctl.Cause) floorctl.Message {
	return floorctl.Message{Type: floorctl.Deny, RejectCause: &floorctl.RejectCause{Cause: cause}}
}

func (s *Server) revokeMessage(cause floorctl.Cause) floorctl.Message {
	return floorctl.Message{Type: floorctl.Revoke, RejectCause: &floorctl.RejectCause{Cause: cause}}
}

func ptr[T any](v T) *T { return &v }
