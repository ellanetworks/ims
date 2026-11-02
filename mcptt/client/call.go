package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/mcptt/amr"
	"github.com/ellanetworks/ims/mcptt/controlchannel"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/floorctl"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/mcptt/jitter"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/mcptt/rtp"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/ua"
)

const reportInterval = 200 * time.Millisecond

// RFC 4028 section 4
const sessionExpires = 90

type Call struct {
	c             *Client
	st            *ua.Strand
	incoming      bool
	manual        bool
	group, caller sip.URI

	audio, rtcp, control media.Conn
	ssrc                 uint32
	sdpID                uint64
	speech               *jitter.Buffer

	out                                                      *ua.Outgoing
	req                                                      *ua.Request
	sess                                                     *ua.Session
	local                                                    controlchannel.Params
	answer                                                   []byte
	n                                                        negotiated
	taken                                                    bool
	bad                                                      error
	part                                                     *floor.Participant
	early                                                    []floorctl.Message
	src                                                      speaker
	established, leaving, ended, failed, announced, reported bool

	sent            atomic.Int64
	placed, pressed time.Time
	released        bool

	mu         sync.Mutex
	closed     bool
	rx         *audioFormat
	cmr        uint8
	heard      map[uint32]*Heard
	order      []uint32
	parties    map[uint32]string
	dropped    map[string]bool
	lastReport time.Time
}

type Heard struct {
	SSRC                                       uint32
	Party                                      string
	Packets                                    int
	Bursts, InitialLost, TrailingLost, MidLost int

	seen    bool
	lastSeq uint16
	lastTS  uint32
}

// RFC 3550 section 5.1
func (h *Heard) track(p rtp.Packet, frames int) {
	if !h.seen {
		h.seen, h.Bursts = true, 1
		h.lastSeq, h.lastTS = p.Sequence, p.Timestamp

		return
	}

	gap := int(p.Sequence - h.lastSeq - 1)
	if gap > 1<<15 {
		return
	}

	step := uint32(max(frames, 1)) * (amr.WB.ClockRate / 50)
	silence := p.Timestamp-h.lastTS > uint32(gap+1)*step

	switch {
	case silence:
		h.Bursts++

		if p.Marker {
			h.TrailingLost += gap
		} else {
			h.InitialLost += gap
		}
	default:
		h.MidLost += gap
	}

	h.lastSeq, h.lastTS = p.Sequence, p.Timestamp
}

func (c *Client) newCall(st *ua.Strand, incoming bool) (*Call, error) {
	call := &Call{
		c: c, st: st, incoming: incoming, ssrc: c.ssrc(), sdpID: rand.Uint64() >> 1,
		speech: jitter.New(jitter.Config{Codec: amr.WB}), heard: map[uint32]*Heard{}, parties: map[uint32]string{},
		cmr: amr.NoModeRequest,
	}

	cs, err := c.listen(call.receiveAudio, func([]byte, netip.AddrPort) {}, call.receiveControl)
	if err != nil {
		c.freeSSRC(call.ssrc)
		return nil, err
	}

	call.audio, call.rtcp, call.control = cs[0], cs[1], cs[2]
	call.src = speaker{seq: uint16(rand.Uint32()), ts0: rand.Uint32(), epoch: c.clock.Now()}

	return call, nil
}

// TS 24.379 clause 10.1.1.2.1.1
// TS 24.379 clause 6.2.1
// TS 24.380 clause 14.2
func (c *Client) Call(ctx context.Context, group sip.URI) (*Call, error) {
	call, err := c.newCall(c.cfg.Agent.NewStrand(), false)
	if err != nil {
		return nil, err
	}

	call.group = group
	call.placed = c.clock.Now()

	if c.cfg.ImplicitRequest {
		call.pressed = call.placed
	}

	if !c.claim(call) {
		call.closeMedia()
		return nil, ErrInCall
	}

	req, err := call.invite()
	if err == nil {
		var out *ua.Outgoing

		out, err = c.cfg.Agent.Invite(ctx, req, call.uacHandler(), ua.On(call.st), ua.WithPolicy(ua.ClientPolicy))
		if err == nil {
			call.st.Post(func() { call.out = out })
			return call, nil
		}
	}

	call.closeMedia()

	return nil, err
}

// TS 24.379 clause 10.1.1.2.1.1
func (call *Call) invite() (*sip.Request, error) {
	c := call.c

	call.local = controlchannel.Params{
		ImplicitRequest: c.cfg.ImplicitRequest, Granted: c.cfg.ImplicitRequest && c.cfg.Granted, Priority: c.cfg.Priority,
		FloorSSRC: call.ssrc, HasFloorSSRC: true,
	}

	sdpOffer, err := offer(c.cfg.Addr, call.sdpID, call.audio.LocalAddr().Port(), call.control.LocalAddr().Port(), call.local)
	if err != nil {
		return nil, err
	}

	info, err := mcpttinfo.Encode(mcpttinfo.Info{Params: &mcpttinfo.Params{
		SessionType: mcpttinfo.SessionPrearranged,
		RequestURI:  mcpttinfo.URI(call.group.String()),
		ClientID:    mcpttinfo.Text(c.cfg.ClientID),
	}})
	if err != nil {
		return nil, err
	}

	req := sip.NewRequest("INVITE", c.cfg.ParticipatingPSI)
	req.Header.Set("To", sip.Address{URI: c.cfg.ParticipatingPSI}.String())
	req.Header.Set("Contact", c.contact().String())
	req.Header.Set("Session-Expires", sip.SessionExpires{Seconds: sessionExpires}.String())

	for _, v := range AcceptContact() {
		req.Header.Add("Accept-Contact", v)
	}

	req.SetBodies(ua.SDP(sdpOffer), sip.NewBodyPart(mcpttinfo.ContentType, info))

	if err := c.originate(req); err != nil {
		return nil, err
	}

	return req, nil
}

func (call *Call) uacHandler() ua.Handler {
	return ua.Handler{
		Answer: func(_ context.Context, s *ua.Session, b []byte) {
			call.sess = s
			call.takeAnswer(b)
		},
		Established: func(ctx context.Context, s *ua.Session, msg sip.Message) {
			call.sess = s
			if _, remote, ok := s.Negotiated(); ok {
				call.takeAnswer(remote)
			}

			res, _ := msg.(*sip.Response)
			call.originated(ctx, res)
		},
		Failed: func(_ context.Context, _ *ua.Outgoing, res *sip.Response, err error) {
			call.failed = true
			call.release()
			call.c.emit(Event{Kind: EventFailed, Call: call, Response: res, Err: err})
		},
		Ended: call.endedBy,
	}
}

// TS 24.380 clause 14.4
func (call *Call) takeAnswer(b []byte) {
	if call.taken || call.ended {
		return
	}

	call.taken = true

	n, err := takeAnswer(b, call.local)
	if err != nil {
		call.bad = err
		return
	}

	call.n = n
	call.hear(n.format)

	ssrc := call.ssrc
	if n.control.HasFloorSSRC {
		ssrc = n.control.FloorSSRC
	}

	if !call.newParticipant(ssrc, min(call.c.cfg.Priority, n.control.Priority)) {
		return
	}

	for _, msg := range call.early {
		call.part.Receive(msg)
	}

	call.early = nil
}

// TS 24.379 clause 10.1.1.2.1.1
// TS 24.380 clause 6.2.4.2.2
func (call *Call) originated(ctx context.Context, res *sip.Response) {
	switch {
	case call.ended:
		return
	case call.bad != nil || call.part == nil:
		err := call.bad
		if err == nil {
			err = errMedia
		}

		call.failed = true
		call.leave(ctx)
		call.c.emit(Event{Kind: EventFailed, Call: call, Response: res, Err: err})

		return
	case call.leaving:
		call.leave(ctx)
		return
	}

	call.established = true
	call.c.cfg.Logger.InfoContext(ctx, "call established", slog.Duration("setup", call.c.clock.Now().Sub(call.placed)))

	cc := call.n.control
	o := floor.Origination{Implicit: cc.ImplicitRequest, Granted: cc.Granted}

	if cc.HasSSRC {
		o.AudioSSRC = cc.SSRC
	}

	call.c.emit(Event{Kind: EventEstablished, Call: call, Response: res})
	call.part.Originate(o)

	switch {
	case call.released:
		call.released = false
		call.part.Unpress()
	case call.c.cfg.ImplicitRequest && !cc.ImplicitRequest && !call.pressed.IsZero():
		call.part.Press()
	}
}

// TS 24.380 clause 6.2.1
// TS 24.380 clause 14.3.3
func (call *Call) newParticipant(ssrc uint32, priority uint8) bool {
	p, err := floor.NewParticipant(floor.ParticipantConfig{
		Clock:    strandClock{st: call.st, clock: call.c.clock},
		Out:      participantOut{call},
		Timers:   call.c.cfg.Floor,
		SSRC:     ssrc,
		Priority: priority,
	})
	if err != nil {
		call.bad = err
		return false
	}

	call.part = p

	return true
}

// TS 24.379 clause 10.1.1.2.1.2
// TS 24.379 clause 6.2.3.1.2
// TS 24.379 clause 6.2.3.2.2
// TS 24.379 clause 6.2.2
func (c *Client) OnInvite(r *ua.Request) ua.Handler {
	call, err := c.newCall(r.Strand(), true)
	if err != nil {
		_ = r.Reject(480)
		return ua.Handler{}
	}

	h := ua.Handler{Established: call.answered, Ended: call.endedBy}
	call.req, call.sess = r, r.Session()
	call.sess.SetPolicy(ua.ClientPolicy)

	code := call.invited(r)
	if code == 0 && !c.claim(call) {
		code = 486
	}

	if code != 0 {
		call.req = nil
		call.release()

		_ = r.Reject(code)

		return h
	}

	call.announced = true
	c.emit(Event{Kind: EventIncoming, Call: call})

	if !call.manual {
		call.accept()
		return h
	}

	_ = r.Progress(ua.Reply{Code: 183, Header: sip.Header{{Name: "Contact", Value: c.contact().String()}}})

	return h
}

func (call *Call) invited(r *ua.Request) int {
	c := call.c
	req := r.Message()

	sdpOffer, ok := r.Offer()
	if !ok {
		return 488
	}

	local := controlchannel.Params{Priority: 0xff, FloorSSRC: call.ssrc, HasFloorSSRC: true}

	ans, n, err := answerOffer(sdpOffer, c.cfg.Addr, call.sdpID, call.audio.LocalAddr().Port(),
		call.control.LocalAddr().Port(), local)
	if err != nil {
		return 488
	}

	call.answer, call.n, call.taken = ans, n, true
	call.hear(n.format)

	ssrc := call.ssrc
	if n.offered.HasFloorSSRC {
		ssrc = n.offered.FloorSSRC
	}

	if !call.newParticipant(ssrc, min(call.c.cfg.Priority, n.control.Priority)) {
		return 500
	}

	if part, ok, err := req.Content().Find(mcpttinfo.ContentType); ok && err == nil {
		if info, _ := mcpttinfo.Decode(part.Body); info.Params != nil {
			call.group = contentURI(info.Params.CallingGroupID)
			call.caller = contentURI(info.Params.CallingUserID)
		}
	}

	call.manual = c.cfg.Manual

	if mode, ok, err := req.Header.AnswerMode(); ok && err == nil {
		switch {
		case mode.Is(sip.AnswerModeAuto):
			call.manual = false
		case mode.Is(sip.AnswerModeManual):
			call.manual = true
		}
	}

	return 0
}

func contentURI(c *mcpttinfo.Content) sip.URI {
	if c == nil {
		return sip.URI{}
	}

	v, ok := c.URI()
	if !ok {
		return sip.URI{}
	}

	u, _ := sip.ParseURI(v)

	return u
}

// TS 24.379 clause 6.2.3.1.1
// TS 24.380 clause 6.2.4.2.3
func (call *Call) accept() {
	r := call.req
	if r == nil || call.ended {
		return
	}

	call.req = nil

	h := r.Message().Header
	se := sip.SessionExpires{Seconds: sessionExpires, Refresher: sip.RefresherUAS}

	if asked, ok, err := h.SessionExpires(); ok && err == nil {
		se.Seconds = min(asked.Seconds, sessionExpires)
	}

	if least, ok, err := h.MinSE(); ok && err == nil {
		se.Seconds = max(se.Seconds, least)
	}

	err := r.Respond(ua.Reply{
		Code: 200,
		Header: sip.Header{
			{Name: "Contact", Value: call.c.contact().String()},
			{Name: "Session-Expires", Value: se.String()},
		},
		Bodies: []sip.BodyPart{ua.SDP(call.answer)},
	})
	if err != nil {
		call.release()
		return
	}

	call.part.Join()
}

// TS 24.379 clause 6.2.3.2.2
func (call *Call) decline() {
	r := call.req
	if r == nil || call.ended {
		return
	}

	call.req = nil
	call.release()
	_ = r.Reject(480, identity.Warning(call.c.contact().URI.Host, identity.WarnUserDeclined))
}

func (call *Call) answered(context.Context, *ua.Session, sip.Message) {
	if call.ended {
		return
	}

	call.established = true
	call.c.emit(Event{Kind: EventEstablished, Call: call})
}

// TS 24.379 clause 10.1.1.2.3.3
// TS 24.379 clause 6.2.6
func (call *Call) endedBy(_ context.Context, _ *ua.Session, e ua.End) {
	call.release()

	if call.failed || call.reported || call.incoming && !call.announced {
		return
	}

	call.reported = true
	call.c.emit(Event{Kind: EventEnded, Call: call, End: e})
}

// TS 24.380 clause 6.2.4.3.5
func (call *Call) PressPTT() {
	call.st.Post(func() {
		switch {
		case call.ended:
		case !call.established:
			call.released = false
		case call.part != nil:
			call.pressed = call.c.clock.Now()
			call.part.Press()
		}
	})
}

// TS 24.380 clause 6.2.4.4.8
// TS 24.380 clause 6.2.4.5.3
func (call *Call) ReleasePTT() {
	call.st.Post(func() {
		switch {
		case call.ended:
		case !call.established:
			call.released = true
		case call.part != nil:
			call.part.Unpress()
		}
	})
}

// TS 24.379 clause 6.2.3.2.2
func (call *Call) Accept() { call.st.Post(call.accept) }

// TS 24.379 clause 6.2.3.2.2
func (call *Call) Decline() { call.st.Post(call.decline) }

// TS 24.379 clause 10.1.1.2.3.1
// TS 24.379 clause 6.2.4.1
// TS 24.379 clause 6.2.5.1
func (call *Call) Leave(ctx context.Context) { call.st.Post(func() { call.leave(ctx) }) }

func (call *Call) leave(ctx context.Context) {
	switch {
	case call.ended:
	case call.req != nil:
		call.decline()
	case call.sess != nil && (call.established || call.incoming || call.sess.State() == ua.Confirmed):
		call.release()
		_ = call.sess.Bye(ctx, ua.Change{}, nil)
	case call.out != nil:
		call.leaving = true
		if call.out.Cancel(ctx, ua.Change{}) != nil {
			return
		}
	default:
		call.leaving = true
	}
}

// TS 24.380 clause 6.2.2
func (call *Call) release() {
	if call.ended {
		return
	}

	call.ended = true
	call.logMedia()

	if call.part != nil {
		call.part.Release()
	}

	call.stopSource()
	call.closeMedia()
}

func (call *Call) closeMedia() {
	call.mu.Lock()
	call.closed = true
	call.mu.Unlock()

	closeAll([]media.Conn{call.audio, call.rtcp, call.control})
	call.c.freeSSRC(call.ssrc)
	call.c.unclaim(call)
}

// TS 26.114 clause 8.2.2
func (call *Call) logMedia() {
	var heard, bursts, initial, trailing, mid int
	for _, h := range call.Heard() {
		heard += h.Packets
		bursts += h.Bursts
		initial += h.InitialLost
		trailing += h.TrailingLost
		mid += h.MidLost
	}

	st := call.speech.Stats()
	call.c.cfg.Logger.Info("call media", slog.Int("sent", call.Sent()), slog.Int("heard", heard),
		slog.Duration("jitter", call.speech.Jitter()), slog.Duration("delay", call.speech.Delay()),
		slog.Int("received", st.Received), slog.Int("late", st.Late), slog.Int("lost", st.Lost),
		slog.Int("duplicates", st.Duplicates), slog.Int("dropped", st.Dropped), slog.Int("bursts", bursts),
		slog.Int("initial_lost", initial), slog.Int("trailing_lost", trailing), slog.Int("mid_lost", mid))
}

func (call *Call) Incoming() bool { return call.incoming }

func (call *Call) Manual() bool { return call.manual }

// TS 24.379 clause F.1.3
func (call *Call) Group() sip.URI { return call.group }

func (call *Call) Caller() sip.URI { return call.caller }

func (call *Call) Sent() int { return int(call.sent.Load()) }

func (call *Call) Heard() []Heard {
	call.mu.Lock()
	defer call.mu.Unlock()

	out := make([]Heard, 0, len(call.order))

	for _, s := range call.order {
		h := *call.heard[s]
		h.Party = call.parties[s]
		out = append(out, h)
	}

	return out
}

type participantOut struct{ call *Call }

func (o participantOut) Send(msg floorctl.Message) { o.call.sendFloor(msg) }

func (o participantOut) Transmit(on bool, ssrc uint32) { o.call.transmit(on, ssrc) }

func (o participantOut) Notify(n floor.Notice) {
	switch n.Kind {
	case floor.NoticeRequestTimeout:
		o.call.probe()
	case floor.NoticeGranted:
		if call := o.call; !call.pressed.IsZero() {
			call.c.cfg.Logger.Info("floor granted", slog.Duration("access", call.c.clock.Now().Sub(call.pressed)))
			call.pressed = time.Time{}
		}
	}

	o.call.c.emit(Event{Kind: EventFloor, Call: o.call, Floor: n})
}

// TS 24.380 clause 6.2.4.4.6
// RFC 3311
// RFC 4028 section 7.4
func (call *Call) probe() {
	if call.ended || call.sess == nil || !call.established {
		return
	}

	if err := call.sess.Update(context.Background(), ua.Change{}, nil); err != nil {
		call.c.cfg.Logger.Debug("probe not sent", slog.Any("error", err))
	}
}

// TS 24.380 clause 8.1.1
func (call *Call) sendFloor(msg floorctl.Message) {
	if call.ended {
		return
	}

	app, err := msg.App()
	if err != nil {
		return
	}

	if b, err := rtp.AppendRTCP(nil, app); err == nil {
		_ = call.control.WriteTo(b, call.n.remote.control)
	}
}

// TS 24.380 clause 8.1.4
func (call *Call) receiveControl(b []byte, from netip.AddrPort) {
	ps, err := rtp.ParseRTCP(b)
	if err != nil {
		call.c.cfg.Logger.Debug("floor control datagram malformed", slog.String("from", from.String()), slog.Any("error", err))
	}

	for _, p := range ps {
		app, ok := p.(*rtp.App)
		if !ok || app.Name != floorctl.Name {
			continue
		}

		msg, err := floorctl.Decode(app)
		if err != nil && !errors.Is(err, floorctl.ErrField) {
			call.c.cfg.Logger.Debug("floor control message dropped", slog.String("from", from.String()),
				slog.Uint64("subtype", uint64(app.Subtype)), slog.Any("error", err))

			continue
		}

		if msg.Type == floorctl.Taken && msg.GrantedSSRC != nil {
			call.mu.Lock()
			call.parties[*msg.GrantedSSRC] = msg.GrantedPartyID
			call.mu.Unlock()

			call.speech.SetTalker(*msg.GrantedSSRC)
		}

		call.st.Post(func() { call.receiveFloor(msg) })
	}
}

func (call *Call) receiveFloor(msg floorctl.Message) {
	switch {
	case call.ended:
	case call.part == nil:
		call.early = append(call.early, msg)
	default:
		call.part.Receive(msg)
	}
}

func (call *Call) hear(f audioFormat) {
	call.mu.Lock()
	defer call.mu.Unlock()

	call.rx = &f
}

// RFC 4867 section 4.3
// RFC 4867 section 4.4
// RFC 4867 section 4.3.2
// TS 24.380 clause 6.2.4.3.4
func (call *Call) receiveAudio(b []byte, from netip.AddrPort) {
	p, err := rtp.Parse(b)
	if err != nil || len(p.Payload) == 0 {
		call.mu.Lock()
		call.dropAudio(from, "malformed RTP", err)
		call.mu.Unlock()

		return
	}

	now := call.c.clock.Now()

	call.mu.Lock()

	switch {
	case call.closed:
		call.mu.Unlock()
		return
	case call.rx == nil:
		call.dropAudio(from, "no payload type negotiated", nil)
		call.mu.Unlock()

		return
	case p.PayloadType != call.rx.pt:
		call.dropAudio(from, fmt.Sprintf("payload type %d, %d negotiated", p.PayloadType, call.rx.pt), nil)
		call.mu.Unlock()

		return
	}

	payload, err := amr.WB.ParsePayload(p.Payload, call.rx.octetAligned)
	if err != nil {
		call.dropAudio(from, "AMR-WB payload malformed", err)
		call.mu.Unlock()

		return
	}

	call.speech.Put(p.SSRC, p.Timestamp, payload.Frames, now)

	request := -1
	if payload.CMR != call.cmr && slices.Contains(call.rx.modes(), int(payload.CMR)) {
		call.cmr, request = payload.CMR, int(payload.CMR)
	}

	h := call.heard[p.SSRC]

	first := h == nil
	if first {
		h = &Heard{SSRC: p.SSRC}
		call.heard[p.SSRC] = h
		call.order = append(call.order, p.SSRC)
	}

	h.Packets++
	h.track(p, len(payload.Frames))

	report := call.lastReport.IsZero() || now.Sub(call.lastReport) >= reportInterval
	if report {
		call.lastReport = now
	}

	party := call.parties[p.SSRC]
	call.mu.Unlock()

	if !first && !report && request < 0 {
		return
	}

	ssrc := p.SSRC

	call.st.Post(func() {
		if call.ended {
			return
		}

		if report && call.part != nil {
			call.part.Media()
		}

		if first {
			call.c.emit(Event{Kind: EventHeard, Call: call, SSRC: ssrc, Party: party})
		}

		if request >= 0 {
			call.c.emit(Event{Kind: EventModeRequest, Call: call, Mode: request})
		}
	})
}

func (call *Call) dropAudio(from netip.AddrPort, why string, err error) {
	if call.dropped[why] {
		return
	}

	if call.dropped == nil {
		call.dropped = map[string]bool{}
	}

	call.dropped[why] = true
	call.c.cfg.Logger.Debug("audio dropped", slog.String("reason", why), slog.String("from", from.String()), slog.Any("error", err))
}

// RFC 4867 section 5.3
func (call *Call) NextSpeech() []byte {
	b, _ := amr.WB.AppendStorage(nil, call.speech.Get(call.c.clock.Now()))

	return b
}

func (call *Call) SpeechDelay() (time.Duration, jitter.Stats) {
	return call.speech.Delay(), call.speech.Stats()
}

// TS 24.380 clause 6.2.4.4.2
type speaker struct {
	on     bool
	ssrc   uint32
	seq    uint16
	ts0    uint32
	epoch  time.Time
	silent bool

	base    uint32
	n       int
	next    uint32
	sent    bool
	started time.Time
}

func (call *Call) transmit(on bool, ssrc uint32) {
	src := &call.src

	switch {
	case !on && !src.on:
		return
	case !on:
		src.on = false

		call.c.emit(Event{Kind: EventTransmit, Call: call, Transmitting: false})

		return
	}

	if ssrc == 0 {
		ssrc = call.ssrc
	}

	src.ssrc = ssrc

	if src.on {
		return
	}

	now := call.c.clock.Now()

	src.on, src.silent, src.n, src.started = true, true, 0, now
	src.base = src.ts0 + uint32(now.Sub(src.epoch)/(time.Second/clockRate))

	if src.sent && int32(src.base-src.next) < 0 {
		src.base = src.next
	}

	call.c.emit(Event{Kind: EventTransmit, Call: call, Transmitting: true, Mode: call.n.format.mode})
}

func (call *Call) stopSource() { call.src.on = false }

var (
	errSpeechFrame   = errors.New("client: not an AMR-WB frame of the mode-set")
	errNotNegotiated = errors.New("client: speech of a call not established")
)

// RFC 4867 section 5.3
// RFC 4867 section 4.3.2
// RFC 4867 section 4.1
// RFC 3550 section 5.1
// TS 26.179 clause 4.1.3
func (call *Call) SendSpeech(frame []byte) error {
	f, n, err := amr.WB.ParseStorage(frame)
	if err != nil || n != len(frame) || f.Type == amr.FrameSpeechLost {
		return errSpeechFrame
	}

	call.mu.Lock()
	format := call.rx
	call.mu.Unlock()

	switch {
	case format == nil:
		return errNotNegotiated
	case int(f.Type) < len(amr.WB.FrameBits) && !slices.Contains(format.modes(), int(f.Type)):
		return errSpeechFrame
	}

	call.st.Post(func() { call.sendSpeech(f) })

	return nil
}

func (call *Call) sendSpeech(f amr.Frame) {
	src := &call.src
	if !src.on || call.ended {
		return
	}

	ts := src.base + uint32(src.n)*frameSamples
	src.n++

	if src.n == 1 {
		call.c.cfg.Logger.Info("first frame", slog.Duration("after", call.c.clock.Now().Sub(src.started)))
	}

	speech := int(f.Type) < len(amr.WB.FrameBits)

	if f.Type == amr.FrameNoData {
		src.silent = true
		return
	}

	marker := speech && src.silent
	src.silent = !speech

	b, err := amr.WB.AppendPayload(nil, amr.Payload{CMR: amr.NoModeRequest, Frames: []amr.Frame{f}}, call.n.format.octetAligned)
	if err != nil {
		return
	}

	p := rtp.Packet{
		Marker:      marker,
		PayloadType: call.n.format.pt,
		Sequence:    src.seq,
		Timestamp:   ts,
		SSRC:        src.ssrc,
		Payload:     b,
	}

	src.seq++
	src.sent, src.next = true, ts+frameSamples

	if out, err := p.Append(nil); err == nil && call.audio.WriteTo(out, call.n.remote.audio) == nil {
		call.sent.Add(1)
	}
}
