package floor

import (
	"errors"
	"fmt"
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
)

// TS 24.380 clause 11.1.1
// TS 24.380 clause 11.2.1
type ParticipantTimers struct {
	T100, T101, T103 time.Duration
	C100, C101       int
}

func (t ParticipantTimers) withDefaults() (ParticipantTimers, error) {
	if t.T100 <= 0 || t.T101 <= 0 {
		return t, errors.New("floor: T100 (Floor Release) or T101 (Floor Request) not configured")
	}

	if t.C100 <= 0 {
		t.C100 = 3
	}

	if t.C101 <= 0 {
		t.C101 = 3
	}

	if time.Duration(t.C100)*t.T100 >= maxRetransmission {
		return t, fmt.Errorf("floor: %d Floor Release messages every %s not within %s", t.C100, t.T100, maxRetransmission)
	}

	return t, nil
}

// TS 24.380 clause 6.2.4
type Notice struct {
	Kind      NoticeKind
	Cause     floorctl.Cause
	Party     string
	Indicator floorctl.Indicator
	Duration  time.Duration
}

type NoticeKind int

const (
	// TS 24.380 clause 6.2.4.4.2
	NoticeGranted NoticeKind = iota + 1
	// TS 24.380 clause 6.2.4.4.4
	NoticeDenied
	// TS 24.380 clause 6.2.4.3.3
	NoticeTaken
	// TS 24.380 clause 6.2.4.3.2
	NoticeIdle
	// TS 24.380 clause 6.2.4.5.4
	NoticeRevoked
	// TS 24.380 clause 6.2.4.4.6
	NoticeRequestTimeout
	// TS 24.380 clause 6.2.4.3.6
	NoticeMediaEnded
)

type ParticipantOut interface {
	Send(msg floorctl.Message)
	Transmit(on bool, ssrc uint32)
	Notify(n Notice)
}

type ParticipantConfig struct {
	Clock     Clock
	Out       ParticipantOut
	Timers    ParticipantTimers
	SSRC      uint32
	Priority  uint8
	Indicator floorctl.Indicator
}

type participantState int

const (
	pStartStop participantState = iota
	pNoPermission
	pPendingRequest
	pHasPermission
	pPendingRelease
	pReleasing
)

// TS 24.380 clause 6.2.4
type Participant struct {
	cfg   ParticipantConfig
	state participantState

	early []floorctl.Message
	ssrc  uint32

	t100, t101, t103 timer
	c100, c101       int
}

// TS 24.380 clause 11.1.1
const maxRetransmission = 6 * time.Second

// TS 24.380 clause 6.2.4.2.1
func NewParticipant(cfg ParticipantConfig) (*Participant, error) {
	t, err := cfg.Timers.withDefaults()
	if err != nil {
		return nil, err
	}

	cfg.Timers = t

	return &Participant{cfg: cfg}, nil
}

// TS 24.380 clause 6.2.4.2.2
type Origination struct {
	Implicit  bool
	Granted   bool
	AudioSSRC uint32
}

// TS 24.380 clause 6.2.4.2.2
func (p *Participant) Originate(o Origination) {
	if p.state != pStartStop {
		return
	}

	if !o.Implicit {
		p.Join()
		return
	}

	early := p.early
	p.early = nil
	p.state = pPendingRequest
	p.c101 = 1
	p.t101.start(p.cfg.Clock, p.cfg.Timers.T101, p.expireT101)

	for _, msg := range early {
		p.receive(msg)
	}

	if o.Granted {
		p.receive(floorctl.Message{Type: floorctl.Granted, GrantedSSRC: &o.AudioSSRC})
	}
}

// TS 24.380 clause 6.2.4.2.3
// TS 24.380 clause 6.2.4.2.2
func (p *Participant) Join() {
	if p.state != pStartStop {
		return
	}

	early := p.early
	p.early = nil
	p.state = pNoPermission

	for _, msg := range early {
		p.receive(msg)
	}
}

// TS 24.380 clause 6.2.4.3.5
func (p *Participant) Press() {
	if p.state != pNoPermission {
		return
	}

	p.send(p.request())
	p.c101 = 1
	p.t101.start(p.cfg.Clock, p.cfg.Timers.T101, p.expireT101)
	p.state = pPendingRequest
}

// TS 24.380 clause 6.2.4.4.8
// TS 24.380 clause 6.2.4.5.3
func (p *Participant) Unpress() {
	switch p.state {
	case pPendingRequest:
		p.t101.stop()
	case pHasPermission:
		p.cfg.Out.Transmit(false, p.ssrc)
	default:
		return
	}

	p.release(floorctl.Message{Type: floorctl.Release})
}

func (p *Participant) release(msg floorctl.Message) {
	p.send(msg)
	p.c100 = 1
	p.t100.start(p.cfg.Clock, p.cfg.Timers.T100, p.expireT100)
	p.state = pPendingRelease
}

// TS 24.380 clause 6.2.4.3.4
// TS 24.380 clause 6.2.4.4.7
// TS 24.380 clause 6.2.4.6.6
func (p *Participant) Media() {
	switch p.state {
	case pNoPermission, pPendingRequest, pPendingRelease:
		p.startT103()
	}
}

func (p *Participant) Receive(msg floorctl.Message) {
	switch p.state {
	case pStartStop:
		p.early = append(p.early, msg)
	case pReleasing:
	default:
		p.receive(msg)
	}
}

func (p *Participant) receive(msg floorctl.Message) {
	switch msg.Type {
	case floorctl.Granted:
		p.receiveGranted(msg)
	case floorctl.Deny:
		if p.state != pPendingRequest {
			return
		}

		p.ack(msg)
		p.t101.stop()
		p.state = pNoPermission
		p.notify(NoticeDenied, msg)
	case floorctl.Taken:
		p.receiveTaken(msg)
	case floorctl.Idle:
		switch p.state {
		case pNoPermission:
			p.ack(msg)
			p.t103.stop()
			p.notify(NoticeIdle, msg)
		case pPendingRelease:
			p.ack(msg)
			p.t100.stop()
			p.state = pNoPermission
			p.notify(NoticeIdle, msg)
		}
	case floorctl.Revoke:
		switch p.state {
		case pHasPermission:
			p.cfg.Out.Transmit(false, p.ssrc)
			p.notify(NoticeRevoked, msg)
			p.release(floorctl.Message{Type: floorctl.Release, Indicator: ptr(p.cfg.Indicator &^ floorctl.IndicatorDualFloor)})
		case pPendingRelease:
			p.notify(NoticeRevoked, msg)
		}
	}
}

func (p *Participant) receiveGranted(msg floorctl.Message) {
	switch p.state {
	case pPendingRequest:
		p.ack(msg)
		p.t103.stop()
		p.t101.stop()

		if msg.GrantedSSRC != nil {
			p.ssrc = *msg.GrantedSSRC
		}

		p.state = pHasPermission
		p.notify(NoticeGranted, msg)
		p.cfg.Out.Transmit(true, p.ssrc)
	case pHasPermission:
		p.ack(msg)

		if msg.GrantedSSRC != nil && *msg.GrantedSSRC != p.ssrc {
			p.ssrc = *msg.GrantedSSRC
			p.cfg.Out.Transmit(true, p.ssrc)
		}

		if msg.Duration != nil {
			p.notify(NoticeGranted, msg)
		}
	case pPendingRelease:
		p.ack(msg)
	}
}

func (p *Participant) receiveTaken(msg floorctl.Message) {
	switch p.state {
	case pNoPermission, pPendingRequest, pPendingRelease:
		p.ack(msg)

		if msg.GrantedSSRC != nil {
			p.ssrc = *msg.GrantedSSRC
		}

		p.t101.stop()
		p.t100.stop()
		p.startT103()
		p.state = pNoPermission
		p.notify(NoticeTaken, msg)
	}
}

// TS 24.380 clause 6.2.2
// TS 24.380 clause 6.2.4.7.2
// TS 24.380 clause 6.2.4.8.2
func (p *Participant) Release() {
	if p.state == pHasPermission {
		p.cfg.Out.Transmit(false, p.ssrc)
	}

	p.state = pReleasing
	p.early = nil

	for _, t := range []*timer{&p.t100, &p.t101, &p.t103} {
		t.stop()
	}
}

// TS 24.380 clause 6.2.4.6.2
// TS 24.380 clause 6.2.4.6.3
func (p *Participant) expireT100() {
	p.t100.fired()

	if p.state != pPendingRelease {
		return
	}

	if p.c100 >= p.cfg.Timers.C100 {
		p.state = pNoPermission
		return
	}

	p.c100++
	p.send(floorctl.Message{Type: floorctl.Release})
	p.t100.start(p.cfg.Clock, p.cfg.Timers.T100, p.expireT100)
}

// TS 24.380 clause 6.2.4.4.5
// TS 24.380 clause 6.2.4.4.6
func (p *Participant) expireT101() {
	p.t101.fired()

	if p.state != pPendingRequest {
		return
	}

	if p.c101 >= p.cfg.Timers.C101 {
		p.state = pNoPermission
		p.cfg.Out.Notify(Notice{Kind: NoticeRequestTimeout})

		return
	}

	p.c101++
	p.send(p.request())
	p.t101.start(p.cfg.Clock, p.cfg.Timers.T101, p.expireT101)
}

func (p *Participant) startT103() {
	if p.cfg.Timers.T103 > 0 {
		p.t103.start(p.cfg.Clock, p.cfg.Timers.T103, p.expireT103)
	}
}

// TS 24.380 clause 6.2.4.3.6
func (p *Participant) expireT103() {
	p.t103.fired()

	if p.state == pNoPermission {
		p.cfg.Out.Notify(Notice{Kind: NoticeMediaEnded})
	}
}

// TS 24.380 clause 6.2.4.3.5
func (p *Participant) request() floorctl.Message {
	msg := floorctl.Message{
		Type:  floorctl.Request,
		Other: []floorctl.Field{{ID: floorctl.FieldLocation, Value: []byte{locationNotProvided}}},
	}

	if p.cfg.Priority != 0 {
		msg.Priority = ptr(p.cfg.Priority)
	}

	if p.cfg.Indicator != 0 {
		msg.Indicator = ptr(p.cfg.Indicator)
	}

	return msg
}

// TS 24.380 clause 8.2.3.21
const locationNotProvided = 0

// TS 24.380 clause 6.2.4.3.2
func (p *Participant) ack(msg floorctl.Message) {
	if a, ok := ack(msg, floorctl.SourceParticipant, p.cfg.SSRC); ok {
		p.cfg.Out.Send(a)
	}
}

func (p *Participant) send(msg floorctl.Message) {
	msg.SSRC = p.cfg.SSRC
	p.cfg.Out.Send(msg)
}

func (p *Participant) notify(kind NoticeKind, msg floorctl.Message) {
	n := Notice{Kind: kind, Party: msg.GrantedPartyID}
	if msg.RejectCause != nil {
		n.Cause = msg.RejectCause.Cause
	}

	if msg.Indicator != nil {
		n.Indicator = *msg.Indicator
	}

	if msg.Duration != nil {
		n.Duration = time.Duration(*msg.Duration) * time.Second
	}

	p.cfg.Out.Notify(n)
}
