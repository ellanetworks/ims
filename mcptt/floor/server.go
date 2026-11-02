package floor

import (
	"errors"
	"fmt"
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
)

// TS 24.380 clause 11.1.3
// TS 24.380 clause 11.2.3
type ServerTimers struct {
	T1, T2, T3, T4, T7, T8 time.Duration
	C7                     int
}

// TS 24.380 clause 11.1.3
// TS 24.380 clause 11.2.3
var DefaultServerTimers = ServerTimers{
	T1: 4 * time.Second, T2: 30 * time.Second, T3: 3 * time.Second, T4: 30 * time.Second, T8: time.Second, C7: 10,
}

// TS 24.380 clause 11.1.3
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

// TS 24.380 clause 11.1.3
const MaxT1 = 6 * time.Second

const MinT1 = time.Second

type ServerOut interface {
	Send(m *Member, msg floorctl.Message)
	Forward(m *Member, on bool)
	Inactive()
	Disconnect(m *Member)
}

type ServerConfig struct {
	Clock   Clock
	Out     ServerOut
	Timers  ServerTimers
	SSRC    uint32
	Drain   time.Duration
	Revokes int
}

type MemberConfig struct {
	ID           string
	FloorSSRC    uint32
	HasFloorSSRC bool
	AudioSSRC    uint32
	Priority     uint8
	ReceiveOnly  bool
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

// TS 24.380 clause 6.3
// TS 24.380 clause 6.3.4
// TS 24.380 clause 6.3.5
type Server struct {
	cfg     ServerConfig
	state   generalState
	members []*Member

	granted  *Member
	since    time.Time
	priority uint8
	location floorctl.Field
	seq      uint16
	pending  *Member

	t1, t2, t3, t4, t7 timer
	c7                 int
	drain              timer
}

// TS 24.380 clause 6.3.5
type Member struct {
	s     *Server
	cfg   MemberConfig
	state memberState

	implicit  bool
	released  bool
	revoke    floorctl.Message
	sentMedia bool
	unheeded  int
	t8        timer
}

// TS 24.380 clause 6.3.4.2.1
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

func (m *Member) Config() MemberConfig { return m.cfg }

// TS 24.380 clause 6.3.5.2.2
func (s *Server) Join(cfg MemberConfig, implicit bool) *Member {
	m := &Member{s: s, cfg: cfg}
	if s.state == gReleasing {
		m.state = uReleasing
		return m
	}

	s.members = append(s.members, m)

	switch {
	case s.pending != nil:
		p := s.pending
		s.pending = nil
		s.request(p, implicitRequest(p))
	case s.state == gStartStop:
		m.implicit = implicit
	case s.state == gIdle && implicit:
		s.request(m, implicitRequest(m))
	case s.state == gIdle:
		s.send(m, s.idle())
	default:
		s.send(m, s.taken())
	}

	return m
}

// TS 24.380 clause 6.3.4.2.2
// TS 24.380 clause 6.3.2.2
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
		s.enterIdle()
	}
}

// TS 24.380 clause 6.3.4.6.2
// TS 24.380 clause 6.3.5.8.2
// TS 24.380 clause 6.3.4.7.2
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
}

func (m *Member) receiveRequest(msg floorctl.Message) {
	s := m.s

	switch m.state {
	case uNotPermittedIdle:
		if s.state == gIdle {
			s.request(m, msg)
		}
	case uNotPermittedTaken:
		cause := floorctl.DenyAnotherHasPermission
		if m.cfg.ReceiveOnly {
			cause = floorctl.DenyReceiveOnly
		}

		s.send(m, s.deny(cause))
	case uPermitted:
		if s.granted == m && s.drain.running() {
			s.drain.stop()
			s.grant(m, s.requested(m, msg), location(msg))

			return
		}

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
		s.send(m, s.idle())
	case uNotPermittedTaken:
		s.send(m, s.taken())
	case uPermitted:
		m.released = true
		if s.granted == m && s.state == gTaken && !s.drain.running() {
			s.t2.stop()
			s.drain.start(s.cfg.Clock, s.cfg.Drain, s.expireDrain)
		}
	case uPendingRevoke:
		m.t8.stop()

		if s.granted == m {
			s.t3.stop()
			s.endGrant()
		}
	case uNotPermittedSendsMedia:
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

func (m *Member) Media() {
	s := m.s

	switch m.state {
	case uPermitted, uPendingRevoke:
		if s.granted != m {
			return
		}

		if s.drain.running() {
			return
		}

		if s.state == gTaken && !s.t2.running() {
			s.t2.start(s.cfg.Clock, s.cfg.Timers.T2, s.expireT2)
		}

		s.t1.start(s.cfg.Clock, s.cfg.Timers.T1, s.expireT1)
	case uNotPermittedTaken:
		m.revokeNoPermission()
	case uNotPermittedSendsMedia:
		m.sentMedia = true
	case uNotPermittedIdle:
		if m.released {
			m.revokeNoPermission()
		}
	}
}

// TS 24.380 clause 6.3.5.4.6
// TS 24.380 clause 6.3.5.7.2
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
	case uNotPermittedSendsMedia:
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

// TS 24.380 clause 6.3.3
// TS 24.380 clause 6.3.5.8.2
// TS 24.380 clause 6.3.4.4.11
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

// TS 24.380 clause 6.3.4.3.3
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

// TS 24.380 clause 6.3.4.2.2
// TS 24.380 clause 6.3.5.2.2
func implicitRequest(m *Member) floorctl.Message {
	return floorctl.Message{Type: floorctl.Request, Priority: ptr(m.cfg.Priority)}
}

// TS 24.380 clause 6.3.5.4.4
func (s *Server) requested(m *Member, msg floorctl.Message) uint8 {
	if msg.Priority != nil && m.cfg.Priority != 0 {
		return min(*msg.Priority, m.cfg.Priority)
	}

	return 0
}

// TS 24.380 clause 8.2.3.21
var notProvided = floorctl.Field{ID: floorctl.FieldLocation, Value: []byte{0}}

func location(msg floorctl.Message) floorctl.Field {
	for _, f := range msg.Other {
		if f.ID == floorctl.FieldLocation && len(f.Value) > 0 {
			return f
		}
	}

	return notProvided
}

// TS 24.380 clause 6.3.4.4.2
func (s *Server) grant(m *Member, priority uint8, loc floorctl.Field) {
	s.state, s.granted, s.since, s.priority, s.location = gTaken, m, s.cfg.Clock.Now(), priority, loc
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

// TS 24.380 clause 6.3.4.4
// TS 24.380 clause 6.3.4.5
func (s *Server) Talker() (*Member, time.Time) {
	if s.granted == nil {
		return nil, time.Time{}
	}

	return s.granted, s.since
}

// TS 24.380 clause 6.3.4.4.3
// TS 24.380 clause 6.3.4.4.6
// TS 24.380 clause 6.3.4.5.4
// TS 24.380 clause 6.3.4.5.5
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

func (s *Server) expireDrain() {
	s.drain.fired()

	if s.granted != nil && s.state == gTaken {
		s.endGrant()
	}
}

// TS 24.380 clause 6.3.4.3.2
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
		s.t2.stop()
		s.endGrant()
	case gPendingRevoke:
		s.t3.stop()
		s.endGrant()
	}
}

// TS 24.380 clause 6.3.4.4.4
// TS 24.380 clause 6.3.4.5.2
func (s *Server) expireT2() {
	s.t2.fired()

	if s.state != gTaken {
		return
	}

	s.t1.stop()
	s.state = gPendingRevoke

	m := s.granted
	m.revoke = s.revokeMessage(floorctl.RevokeMediaBurstTooLong)
	s.send(m, m.revoke)
	s.t3.start(s.cfg.Clock, s.cfg.Timers.T3, s.expireT3)
}

// TS 24.380 clause 6.3.4.5.5
func (s *Server) expireT3() {
	s.t3.fired()

	if s.state != gPendingRevoke {
		return
	}

	s.endGrant()
}

// TS 24.380 clause 6.3.4.3.5
func (s *Server) expireT4() {
	s.t4.fired()

	if s.state != gIdle {
		return
	}

	s.cfg.Out.Inactive()

	if s.state == gIdle && !s.t4.running() {
		s.t4.start(s.cfg.Clock, s.cfg.Timers.T4, s.expireT4)
	}
}

// TS 24.380 clause 6.3.4.3.4
func (s *Server) expireT7() {
	s.t7.fired()

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

// TS 24.380 clause 6.3.5
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
			m.state, m.released = uNotPermittedTaken, false
			m.t8.stop()
		case uNotPermittedSendsMedia:
			return
		}
	case floorctl.Idle:
		switch m.state {
		case uNotPermittedTaken, uPermitted, uPendingRevoke:
			m.state = uNotPermittedIdle
			m.t8.stop()
		case uNotPermittedSendsMedia:
			return
		}
	case floorctl.Revoke:
		if m.state == uPermitted {
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

// TS 24.380 clause 6.3.4.4.2
func (s *Server) grantedMessage(m *Member) floorctl.Message {
	return floorctl.Message{
		Type:        floorctl.Granted,
		Duration:    ptr(seconds(s.cfg.Timers.T2)),
		Priority:    ptr(s.priority),
		GrantedSSRC: ptr(m.cfg.AudioSSRC),
	}
}

// TS 24.380 clause 6.3.4.4.2
func (s *Server) taken() floorctl.Message {
	s.seq++

	return floorctl.Message{
		Type:           floorctl.Taken,
		GrantedPartyID: s.granted.cfg.ID,
		Permission:     ptr(true),
		SequenceNumber: ptr(s.seq),
		GrantedSSRC:    ptr(s.granted.cfg.AudioSSRC),
		Other:          []floorctl.Field{s.location},
	}
}

// TS 24.380 clause 6.3.4.3.2
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
