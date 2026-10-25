package client_test

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/client/clienttest"
	"github.com/ellanetworks/ims/mcptt/controlchannel"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/floorctl"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/mcptt/media/mediatest"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

const (
	domain = "mcptt.example.com"
	psi    = "sip:mcptt-participating@mcptt.example.com"
	fire   = "sip:fire@mcptt.example.com"
)

// env is the SIP and media networks of a test, on one clock and scheduler.
type env struct {
	t *testing.T
	n *siptest.Net
	m *mediatest.Net
}

func newEnv(t *testing.T) *env {
	t.Helper()

	n := siptest.NewNet(t)

	return &env{t: t, n: n, m: mediatest.New(n.Scheduler())}
}

func mustURI(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

// agent is an agent on the net at addr: its requests go to outbound if valid, else to the address of their
// Request-URI.
func (e *env) agent(addr, outbound netip.AddrPort, configure func(*ua.Config)) *ua.Agent {
	e.t.Helper()

	cfg := ua.Config{
		Transaction: e.n.Config(transaction.Config{}),
		Contact:     sip.Address{URI: sip.URI{Scheme: "sip", Host: addr.Addr().String(), Port: addr.Port()}},
		Prepare: func(req *sip.Request) error {
			to := outbound
			if !to.IsValid() {
				a, err := netip.ParseAddr(req.URI.Host)
				if err != nil {
					return err
				}

				to = netip.AddrPortFrom(a, req.URI.Port)
			}

			req.Flow = sip.Flow{Transport: sip.UDP, Local: addr, Remote: to}
			req.Header.Prepend("Via", sip.NewVia(sip.UDP, addr).String())

			return nil
		},
	}

	if configure != nil {
		configure(&cfg)
	}

	a, err := ua.New(cfg)
	if err != nil {
		e.t.Fatal(err)
	}

	if err := a.Layer().Serve(addr); err != nil {
		e.t.Fatal(err)
	}

	e.t.Cleanup(func() {
		e.n.Await(func() { _ = a.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1)
	})

	return a
}

// events records the events of a client.
type events struct {
	mu sync.Mutex
	es []client.Event
}

func (r *events) add(e client.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.es = append(r.es, e)
}

// take returns the events since the last take.
func (r *events) take() []client.Event {
	r.mu.Lock()
	defer r.mu.Unlock()

	es := r.es
	r.es = nil

	return es
}

// user is a client of a test, on an agent of its own.
type user struct {
	*client.Client

	id     string
	agent  *ua.Agent
	addr   netip.AddrPort
	events *events

	// mute keeps the speaker of the tests quiet: the test sends the frames.
	mute atomic.Bool
}

var timers = floor.ParticipantTimers{T100: time.Second, T101: time.Second}

// user makes the client of name on the net, its media at 192.0.2.i; its requests go to outbound if valid.
func (e *env) user(name string, i int, outbound netip.AddrPort, configure func(*client.Config)) *user {
	e.t.Helper()

	u := &user{id: "sip:" + name + "@" + domain, addr: e.n.Addr(), events: &events{}}

	var c *client.Client

	u.agent = e.agent(u.addr, outbound, func(cfg *ua.Config) {
		cfg.OnInvite = func(r *ua.Request) ua.Handler { return c.OnInvite(r) }
	})

	speaker := clienttest.NewSpeaker(u.agent.Layer().Clock())

	cfg := client.Config{
		Agent:            u.agent,
		IMPU:             mustURI(e.t, "sip:"+name+"@ims.example.com"),
		MCPTTID:          mustURI(e.t, u.id),
		ClientID:         fmt.Sprintf("urn:uuid:00000000-0000-1000-8000-%012d", i),
		Contact:          client.FixedContact(sip.Address{URI: sip.URI{Scheme: "sip", User: name, Host: u.addr.Addr().String(), Port: u.addr.Port()}}),
		Registrar:        mustURI(e.t, "sip:"+domain),
		ParticipatingPSI: mustURI(e.t, psi),
		Media:            e.m,
		Addr:             netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}),
		MinPort:          30000,
		MaxPort:          30099,
		Floor:            timers,
		OnEvent: func(e client.Event) {
			if !u.mute.Load() {
				speaker.Event(e)
			}

			u.events.add(e)
		},
	}

	if configure != nil {
		configure(&cfg)
	}

	c, err := client.New(cfg)
	if err != nil {
		e.t.Fatal(err)
	}

	u.Client = c

	return u
}

// kinds are the kinds of events, the floor notices by theirs.
func kinds(es []client.Event) []string {
	var out []string

	for _, e := range es {
		s := e.Kind.String()

		switch {
		case e.Kind == client.EventFloor:
			s += ":" + notices[e.Floor.Kind]
		case e.Kind == client.EventTransmit && e.Transmitting:
			s += ":on"
		case e.Kind == client.EventTransmit:
			s += ":off"
		}

		out = append(out, s)
	}

	return out
}

var notices = map[floor.NoticeKind]string{
	floor.NoticeGranted: "granted", floor.NoticeDenied: "denied", floor.NoticeTaken: "taken", floor.NoticeIdle: "idle",
	floor.NoticeRevoked: "revoked", floor.NoticeRequestTimeout: "timeout", floor.NoticeMediaEnded: "media-ended",
}

func (u *user) expect(t *testing.T, want ...string) []client.Event {
	t.Helper()

	es := u.events.take()
	if got := kinds(es); !slices.Equal(got, want) {
		t.Fatalf("%s: events %v, want %v", u.id, got, want)
	}

	return es
}

// expectGrant checks the events of a call placed with the floor granted in its 200 (mc_granted): the Floor Granted
// the server sends as it takes the floor (TS 24.380 §6.3.4.4.2), its Duration told in a second grant notice, races
// the other events and is left out.
func (u *user) expectGrant(t *testing.T, want ...string) {
	t.Helper()

	es := u.events.take()
	grants := 0
	es = slices.DeleteFunc(es, func(e client.Event) bool {
		if e.Kind == client.EventFloor && e.Floor.Kind == floor.NoticeGranted {
			grants++
			return grants > 1
		}

		return false
	})

	if got := kinds(es); !slices.Equal(got, want) {
		t.Fatalf("%s: events %v, want %v", u.id, got, want)
	}
}

// expectUnordered checks the events, of an order the network does not define: of messages of the server that race.
func (u *user) expectUnordered(t *testing.T, want ...string) []client.Event {
	t.Helper()

	es := u.events.take()
	got := kinds(es)

	slices.Sort(got)

	if want = slices.Sorted(slices.Values(want)); !slices.Equal(got, want) {
		t.Fatalf("%s: events %v, want %v in any order", u.id, kinds(es), want)
	}

	return es
}

// standIn is a minimal controlling function for the client's tests: it answers an INVITE once the members it
// invites accepted, with the control channel of TS 24.380 §14.3, and runs the floor control server of the call over
// the media relay (TS 24.380 §6.3).
type standIn struct {
	t     *testing.T
	agent *ua.Agent
	addr  netip.AddrPort
	st    *ua.Strand
	relay *media.Relay
	call  *media.Call
	fs    *floor.Server
	legs  map[*floor.Member]*media.Leg

	// members are invited, at their contacts, with the Answer-Mode given.
	members    []invitee
	answerMode string
	// grant is the mc_granted of the originator's 200; lateFloor has the floor control start after the 200 is sent,
	// not before as the controlling function does;
	// badAudio answers the originator's speech in a payload type it did not offer; noImplicit does not accept its
	// implicit floor request (TS 24.380 §14.3.5).
	grant, lateFloor, badAudio, noImplicit bool

	origin       *ua.Request
	originLeg    *media.Leg
	originParams controlchannel.Params
	originID     string
	originMember *floor.Member
	// invite is the originator's INVITE; ok the 200 of the last member that accepted.
	inviteReq *sip.Request
	ok        *sip.Response
	sessions  []*ua.Session
	pending   int
	// declined are the final responses of the members that did not accept.
	declined []*sip.Response
	// updates are the UPDATEs the stand-in received on the members' sessions; forget, that it answers them with a
	// 481, as a server whose leg the network released (TS 24.229 §5.2.8.1.2).
	updates atomic.Int32
	forget  atomic.Bool
}

type invitee struct {
	id      string
	contact sip.URI
}

func (e *env) standIn(members ...*user) *standIn {
	e.t.Helper()

	s := &standIn{t: e.t, addr: e.n.Addr(), legs: map[*floor.Member]*media.Leg{}, answerMode: sip.AnswerModeAuto}

	for _, m := range members {
		s.members = append(s.members, invitee{id: m.id, contact: sip.URI{
			Scheme: "sip", User: "x", Host: m.addr.Addr().String(), Port: m.addr.Port(),
		}})
	}

	s.agent = e.agent(s.addr, netip.AddrPort{}, func(cfg *ua.Config) {
		cfg.StrandFor = func(*sip.Request) *ua.Strand { return s.st }
		cfg.OnInvite = s.onInvite
	})
	s.st = s.agent.NewStrand()

	r, err := media.New(media.Config{
		Network: e.m, Clock: e.n.Clock(), Addr: netip.AddrFrom4([4]byte{192, 0, 2, 100}),
		MinPort: 20000, MaxPort: 20999, ControlPort: 21000,
	})
	if err != nil {
		e.t.Fatal(err)
	}

	e.t.Cleanup(func() { _ = r.Close() })

	s.relay, s.call = r, r.NewCall()

	t := floor.DefaultServerTimers
	t.T7 = time.Hour

	s.fs, err = floor.NewServer(floor.ServerConfig{Clock: standInClock{s.st, e.n.Clock()}, Out: s, Timers: t})
	if err != nil {
		e.t.Fatal(err)
	}

	return s
}

type standInClock struct {
	st *ua.Strand
	c  *siptest.Clock
}

func (c standInClock) AfterFunc(d time.Duration, f func()) floor.Timer { return c.st.AfterFunc(d, f) }

func (c standInClock) Now() time.Time { return c.c.Now() }

func (s *standIn) Send(m *floor.Member, msg floorctl.Message) { _ = s.legs[m].SendFloor(msg) }

func (s *standIn) Forward(m *floor.Member, on bool) { s.call.Forward(s.legs[m], on) }

func (s *standIn) Inactive() {}

func (s *standIn) Disconnect(*floor.Member) {}

// leg adds a leg whose floor messages and media go to the member *m, on the strand.
func (s *standIn) leg(m **floor.Member, shared bool) *media.Leg {
	l, err := s.call.AddLeg(media.LegConfig{
		// The payload type of the client's offers and of the stand-in's.
		SharedControl: shared, PayloadType: 96,
		OnFloor: func(msg floorctl.Message) {
			s.st.Post(func() {
				if *m != nil {
					(*m).Receive(msg)
				}
			})
		},
		OnMedia: func() {
			s.st.Post(func() {
				if *m != nil {
					(*m).Media()
				}
			})
		},
	})
	if err != nil {
		s.t.Fatal(err)
	}

	return l
}

func (s *standIn) sdp(l *media.Leg, control controlchannel.Params) []byte {
	return s.sdpOf(l, control, 96)
}

func (s *standIn) sdpOf(l *media.Leg, control controlchannel.Params, pt int) []byte {
	b := []byte("v=0\r\no=- 1 1 IN IP4 192.0.2.100\r\ns=-\r\nc=IN IP4 192.0.2.100\r\nt=0 0\r\n" +
		"m=audio " + strconv.Itoa(int(l.Audio().Port())) + " RTP/AVP " + strconv.Itoa(pt) + "\r\ni=speech\r\n" +
		"a=rtpmap:" + strconv.Itoa(pt) + " AMR-WB/16000/1\r\n")

	ss, err := sdp.Parse(b)
	if err != nil {
		s.t.Fatal(err)
	}

	ss.Media = append(ss.Media, controlchannel.NewMedia(l.Control().Port(), control))

	return ss.Bytes()
}

// remote are the media addresses and control parameters of a client's SDP.
func remote(t *testing.T, b []byte) (media.Remote, controlchannel.Params) {
	t.Helper()

	ss, err := sdp.Parse(b)
	if err != nil {
		t.Fatal(err)
	}

	ci := controlchannel.Channel(ss)
	a, _ := ss.RTPEndpoint(0)
	c, _ := ss.RTPEndpoint(ci)
	p, _ := controlchannel.MediaParams(ss.Media[ci])

	return media.Remote{
		Audio: netip.AddrPortFrom(a.Addr, a.Port), Control: netip.AddrPortFrom(c.Addr, c.Port),
	}, p
}

// onInvite takes the originator's INVITE, and invites the members.
func (s *standIn) onInvite(r *ua.Request) ua.Handler {
	offer, _ := r.Offer()
	rem, params := remote(s.t, offer)

	l := s.leg(&s.originMember, params.HasFloorSSRC)
	l.SetRemote(rem)

	s.inviteReq = r.Message().Clone()
	from, _ := r.Message().Header.From()
	s.origin, s.originLeg, s.originParams, s.originID = r, l, params, "sip:"+from.URI.User+"@"+domain

	for _, inv := range s.members {
		s.invite(inv)
	}

	s.pending = len(s.members)

	return ua.Handler{Ended: func(*ua.Session, ua.End) {
		// The originator left: the call released (TS 24.379 §6.3.8.1).
		for _, ss := range s.sessions {
			_ = ss.Bye(ua.Change{}, nil)
		}

		s.fs.Release()
		s.call.Close()
	}}
}

func (s *standIn) invite(inv invitee) {
	var m *floor.Member

	l := s.leg(&m, true)
	ssrc, _ := l.FloorSSRC()

	req := sip.NewRequest("INVITE", inv.contact)
	req.Header.Set("To", "<"+inv.id+">")
	req.Header.Set("From", "<"+fire+">")
	req.Header.Set("Contact", "<sip:fire@"+s.addr.String()+">")
	req.Header.Set("Answer-Mode", s.answerMode)

	info, err := mcpttinfo.Encode(mcpttinfo.Info{Params: &mcpttinfo.Params{
		SessionType: mcpttinfo.SessionPrearranged, CallingUserID: mcpttinfo.URI(s.originID), CallingGroupID: mcpttinfo.URI(fire),
	}})
	if err != nil {
		s.t.Fatal(err)
	}

	offer := controlchannel.Offer(controlchannel.Params{FloorSSRC: ssrc, HasFloorSSRC: true})
	req.SetBodies(ua.SDP(s.sdp(l, offer)), sip.NewBodyPart(mcpttinfo.ContentType, info))

	_, err = s.agent.Invite(req, ua.Handler{
		Established: func(ss *ua.Session, msg sip.Message) {
			s.ok, _ = msg.(*sip.Response)
			_, b, _ := ss.Negotiated()
			rem, answer := remote(s.t, b)
			l.SetRemote(rem)
			l.SetMultiplexed(answer.HasFloorSSRC)

			m = s.fs.Join(floor.MemberConfig{
				ID: inv.id, AudioSSRC: l.AudioSSRC(), FloorSSRC: answer.FloorSSRC, HasFloorSSRC: answer.HasFloorSSRC,
			}, false)
			s.legs[m] = l
			s.sessions = append(s.sessions, ss)
			s.accepted()
		},
		Failed: func(_ *ua.Outgoing, res *sip.Response, _ error) {
			s.declined = append(s.declined, res)

			l.Close()
		},
		Ended: func(*ua.Session, ua.End) {
			if m != nil {
				m.Leave()
			}
		},
		Request: func(_ *ua.Session, r *ua.Request) {
			if r.Message().Method == "UPDATE" {
				s.updates.Add(1)
			}

			switch {
			case s.forget.Load():
				_ = r.Reject(481)
			case r.Refresh():
				_ = r.AnswerUnchanged(ua.Change{})
			default:
				_ = r.Reject(488)
			}
		},
	}, ua.On(s.st))
	if err != nil {
		s.t.Fatal(err)
	}
}

// accepted starts the floor control once the first member accepted (TS 24.380 §6.3.4.2.2), and answers the
// originator.
func (s *standIn) accepted() {
	r := s.origin
	if r == nil {
		return
	}

	s.origin = nil
	l := s.originLeg
	ssrc, _ := l.FloorSSRC()

	answer, err := controlchannel.Answer(s.originParams, controlchannel.Params{
		ImplicitRequest: !s.noImplicit, Granted: s.grant, SSRC: l.AudioSSRC(), HasSSRC: true, FloorSSRC: ssrc, HasFloorSSRC: true,
	})
	if err != nil {
		s.t.Fatal(err)
	}

	pt := 96
	if s.badAudio {
		pt = 97
	}

	respond := func() {
		if err := r.Respond(ua.Reply{Code: 200, Bodies: []sip.BodyPart{ua.SDP(s.sdpOf(l, answer, pt))}}); err != nil {
			s.t.Fatal(err)
		}
	}

	if s.lateFloor {
		respond()
	}

	s.originMember = s.fs.Join(floor.MemberConfig{
		ID: s.originID, AudioSSRC: l.AudioSSRC(), FloorSSRC: s.originParams.FloorSSRC, HasFloorSSRC: s.originParams.HasFloorSSRC,
	}, answer.ImplicitRequest)
	s.legs[s.originMember] = l

	s.fs.Start(answer.Granted)

	if !s.lateFloor {
		respond()
	}
}
