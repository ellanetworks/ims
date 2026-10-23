package server_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/server"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/client/clienttest"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/mcptt/media/mediatest"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

const (
	domain = "mcptt.example.com"
	psi    = "sip:mcptt-participating@mcptt.example.com"
	fire   = "sip:fire@mcptt.example.com"
	// police releases its calls when the initiator leaves (TS 24.379 §6.3.8.1 3); short has a TNG3 of a minute, and
	// a T4 longer; rescue and hazmat require carol, proceeding and abandoning on TNG1.
	police = "sip:police@mcptt.example.com"
	short  = "sip:short@mcptt.example.com"
	rescue = "sip:rescue@mcptt.example.com"
	hazmat = "sip:hazmat@mcptt.example.com"
	// small has at most two participants; quorum needs four members affiliated, escort dave affiliated; two needs two
	// members to accept.
	small  = "sip:small@mcptt.example.com"
	quorum = "sip:quorum@mcptt.example.com"
	escort = "sip:escort@mcptt.example.com"
	two    = "sip:two@mcptt.example.com"
)

// every are the groups of callConfiguration.
var every = []string{fire, police, short, rescue, hazmat, small, quorum, escort, two}

// callConfiguration has alice, bob and carol affiliated to every group on registration, dave a member of fire not
// affiliated to it; carol in at most one group call at once (N6).
func callConfiguration() settings.Settings {
	const (
		alice = "sip:alice@mcptt.example.com"
		bob   = "sip:bob@mcptt.example.com"
		carol = "sip:carol@mcptt.example.com"
		dave  = "sip:dave@mcptt.example.com"
	)

	members := func(es ...settings.Entry) settings.List { return settings.List{Entries: es} }
	m := func(uri string) settings.Entry { return settings.Entry{URI: uri} }
	group := func(uri string, list settings.List) settings.Group {
		return settings.Group{URI: uri, InviteMembers: true, TNG3: 10 * time.Minute, List: list}
	}

	g := []settings.Group{
		group(fire, members(m(alice), m(bob), m(carol), m(dave))),
		group(police, members(m(alice), m(bob), m(carol))),
		group(short, members(m(alice), m(bob), m(carol))),
		group(rescue, members(m(alice), m(bob), settings.Entry{URI: carol, Required: true})),
		group(hazmat, members(m(alice), m(bob), settings.Entry{URI: carol, Required: true})),
		group(small, members(m(alice), m(bob), m(carol))),
		group(quorum, members(m(alice), m(bob), m(carol), m(dave))),
		group(escort, members(m(alice), m(bob), m(carol), settings.Entry{URI: dave, AffiliationRequired: true})),
		group(two, members(m(alice), m(bob), m(carol))),
	}
	g[1].ReleaseOnInitiatorLeave = true
	g[2].TNG3, g[2].T4 = time.Minute, new(10*time.Minute)
	g[3].TNG1, g[3].TNG1Action = 5*time.Second, settings.Proceed
	g[4].TNG1, g[4].TNG1Action = 5*time.Second, settings.Abandon
	g[5].MaxParticipants = 2
	g[6].MinimumAffiliated = 4
	g[8].MinimumToStart = 2

	return settings.Settings{
		System: settings.System{ParticipatingPSI: psi, ControllingPSI: "sip:mcptt-controlling@mcptt.example.com"},
		ServiceConfiguration: settings.ServiceConfiguration{
			Domain: domain,
			OnNetwork: settings.OnNetwork{
				TransmitTime:     settings.TransmitTime{TimeLimit: new(20 * time.Second)},
				FCTimersCounters: settings.FCTimersCounters{T7: new(2 * time.Second), C7: new(0)},
			},
		},
		Users: []settings.User{
			{MCPTTUserID: alice, IMPUs: []string{"sip:alice@ims.example.com"}, ImplicitAffiliations: every},
			{MCPTTUserID: bob, IMPUs: []string{"sip:bob@ims.example.com"}, ImplicitAffiliations: every},
			{MCPTTUserID: carol, IMPUs: []string{"sip:carol@ims.example.com"}, ImplicitAffiliations: every, MaxSimultaneousCallsN6: new(1)},
			{MCPTTUserID: dave, IMPUs: []string{"sip:dave@ims.example.com"}},
		},
		Groups: g,
	}
}

type source struct{ s *settings.Snapshot }

func (f source) Current() *settings.Snapshot { return f.s }

// reloadable are settings a test changes.
type reloadable struct {
	p atomic.Pointer[settings.Snapshot]
}

func (r *reloadable) Current() *settings.Snapshot { return r.p.Load() }

// trace is the messages of a test in the order sent: the SIP requests and responses of the server and the clients,
// and the floor notices of the clients.
type trace struct {
	mu    sync.Mutex
	names map[netip.AddrPort]string
	lines []string
}

func (tr *trace) add(format string, a ...any) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	tr.lines = append(tr.lines, fmt.Sprintf(format, a...))
}

func (tr *trace) name(a netip.AddrPort) string {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	return tr.names[a]
}

func (tr *trace) take() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	out := tr.lines
	tr.lines = nil

	return out
}

// world is the server in direct mode [PM 14] and its clients, on one SIP and media network and clock.
type world struct {
	t     *testing.T
	n     *siptest.Net
	m     *mediatest.Net
	s     *server.Server
	addr  netip.AddrPort
	trace *trace
	src   *reloadable
	users map[string]*user
	// last are the events of alice at the setup of the last call.
	last []client.Event
	// logs are the server's, in JSON from debug.
	logs *logs
}

// user is a client of a test: alice at 192.0.2.1, bob at .2, carol at .3, dave at .4.
type user struct {
	*client.Client

	name   string
	id     string
	agent  *ua.Agent
	addr   netip.AddrPort
	mu     sync.Mutex
	events []client.Event
	// onInvite, if set, takes the INVITEs of the client instead of it.
	onInvite func(r *ua.Request) ua.Handler
}

var floorTimers = floor.ParticipantTimers{T100: time.Second, T101: time.Second}

func newWorld(t *testing.T, configure func(name string, cfg *client.Config)) *world {
	t.Helper()

	snap, err := settings.New(callConfiguration())
	if err != nil {
		t.Fatal(err)
	}

	n := siptest.NewNet(t)
	w := &world{t: t, n: n, m: mediatest.New(n.Scheduler()), addr: n.Addr(), trace: &trace{names: map[netip.AddrPort]string{}}, src: &reloadable{}, users: map[string]*user{}, logs: &logs{}}
	w.src.p.Store(snap)

	relay, err := media.New(media.Config{
		Network: w.m, Clock: n.Clock(), Addr: netip.AddrFrom4([4]byte{192, 0, 2, 100}),
		MinPort: 20000, MaxPort: 20999, ControlPort: 21000,
	})
	if err != nil {
		t.Fatal(err)
	}

	w.s, err = server.New(server.Config{
		UA: ua.Config{
			Transaction: n.Config(transaction.Config{}),
			Contact:     sip.Address{URI: sip.URI{Scheme: "sip", User: "mcptt", Host: w.addr.Addr().String(), Port: w.addr.Port()}},
			Prepare: func(req *sip.Request) error {
				tr, to, err := sip.NextHop(req)
				if err != nil {
					return err
				}

				req.Flow = sip.Flow{Transport: tr, Local: w.addr, Remote: to}
				req.Header.Prepend("Via", sip.NewVia(tr, w.addr).String())
				w.trace.add("server>%s %s", w.trace.name(to), req.Method)

				return nil
			},
			Respond: func(req *sip.Request, res *sip.Response) {
				w.trace.add("server>%s %d %s", w.trace.name(req.Flow.Remote), res.StatusCode, req.Method)
			},
			Logger: slog.New(slog.NewJSONHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		},
		Source: w.src,
		Mode:   registrar.Direct,
		Media:  relay,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := w.s.Agent().Layer().Serve(w.addr); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		n.Await(func() { _ = w.s.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1)
	})

	for i, name := range []string{"alice", "bob", "carol", "dave"} {
		w.user(name, i+1, configure)
	}

	return w
}

func (w *world) user(name string, i int, configure func(string, *client.Config)) {
	t := w.t
	u := &user{name: name, id: "sip:" + name + "@" + domain, addr: w.n.Addr()}
	w.trace.names[u.addr] = name
	w.users[name] = u

	cfg := ua.Config{
		Transaction: w.n.Config(transaction.Config{}),
		Contact:     sip.Address{URI: sip.URI{Scheme: "sip", User: name, Host: u.addr.Addr().String(), Port: u.addr.Port()}},
		// The server is the outbound proxy of the client in direct mode: the registrar of its domain.
		Prepare: func(req *sip.Request) error {
			req.Flow = sip.Flow{Transport: sip.UDP, Local: u.addr, Remote: w.addr}
			req.Header.Prepend("Via", sip.NewVia(sip.UDP, u.addr).String())
			w.trace.add("%s>server %s", name, req.Method)

			return nil
		},
		Respond: func(req *sip.Request, res *sip.Response) {
			w.trace.add("%s>server %d %s", name, res.StatusCode, req.Method)
		},
		OnInvite: func(r *ua.Request) ua.Handler {
			if u.onInvite != nil {
				return u.onInvite(r)
			}

			return u.OnInvite(r)
		},
	}

	a, err := ua.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Layer().Serve(u.addr); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		w.n.Await(func() { _ = a.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1)
	})

	u.agent = a

	speaker := clienttest.NewSpeaker(a.Layer().Clock())

	ccfg := client.Config{
		Agent:            a,
		IMPU:             mustURI(t, "sip:"+name+"@ims.example.com"),
		MCPTTID:          mustURI(t, u.id),
		ClientID:         fmt.Sprintf("urn:uuid:00000000-0000-1000-8000-%012d", i),
		Contact:          client.FixedContact(cfg.Contact),
		Registrar:        mustURI(t, "sip:"+domain),
		ParticipatingPSI: mustURI(t, psi),
		Media:            w.m,
		Addr:             netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}),
		MinPort:          30000,
		MaxPort:          30099,
		Floor:            floorTimers,
		OnEvent: func(e client.Event) {
			speaker.Event(e)

			if e.Kind == client.EventFloor {
				w.trace.add("%s floor:%s", name, notices[e.Floor.Kind])
			}

			u.mu.Lock()
			u.events = append(u.events, e)
			u.mu.Unlock()
		},
	}

	if configure != nil {
		configure(name, &ccfg)
	}

	c, err := client.New(ccfg)
	if err != nil {
		t.Fatal(err)
	}

	u.Client = c

	var res *sip.Response

	if err := c.Register(time.Hour, func(r *sip.Response, _ error) { res = r }); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	if res == nil || res.StatusCode != 200 {
		t.Fatalf("%s: REGISTER %v", name, res)
	}

	u.take()
	w.trace.take()
}

func mustURI(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

var notices = map[floor.NoticeKind]string{
	floor.NoticeGranted: "granted", floor.NoticeDenied: "denied", floor.NoticeTaken: "taken", floor.NoticeIdle: "idle",
	floor.NoticeRevoked: "revoked", floor.NoticeRequestTimeout: "timeout", floor.NoticeMediaEnded: "media-ended",
}

// take returns the events since the last take.
func (u *user) take() []client.Event {
	u.mu.Lock()
	defer u.mu.Unlock()

	es := u.events
	u.events = nil

	return es
}

func kinds(es []client.Event) []string {
	var out []string

	for _, e := range es {
		s := e.Kind.String()
		if e.Kind == client.EventFloor {
			s += ":" + notices[e.Floor.Kind]
		}

		out = append(out, s)
	}

	return out
}

// expect checks that the events since the last take have want, in order, and returns them; floor:idle repeated (the
// C7 Floor Idles) aside.
func (u *user) expect(t *testing.T, want ...string) []client.Event {
	t.Helper()

	es := u.take()
	if !subsequence(kinds(es), want) {
		t.Fatalf("%s: events %v, want %v in order", u.name, kinds(es), want)
	}

	return es
}

// expectAll checks that the events since the last take have want, in any order: the first media of a talker races
// the Floor Taken announcing it (TS 24.380 §6.2.4.4.2 step 7).
func (u *user) expectAll(t *testing.T, want ...string) {
	t.Helper()

	got := kinds(u.take())
	for _, k := range want {
		if !slices.Contains(got, k) {
			t.Fatalf("%s: events %v, want %v", u.name, got, want)
		}
	}
}

// expectNot checks that the events since the last take have none of kinds.
func (u *user) expectNot(t *testing.T, not ...string) {
	t.Helper()

	got := kinds(u.take())
	for _, k := range not {
		if slices.Contains(got, k) {
			t.Fatalf("%s: events %v, with %s", u.name, got, k)
		}
	}
}

func subsequence(got, want []string) bool {
	i := 0

	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}

	return i == len(want)
}

func event(es []client.Event, k client.EventKind) *client.Event {
	for i := range es {
		if es[i].Kind == k {
			return &es[i]
		}
	}

	return nil
}

// call places a call of alice to group, answered once the others accepted: the calls of alice, bob and carol.
func (w *world) call(group string) (a, b, c *client.Call) {
	t := w.t
	t.Helper()

	ca, err := w.users["alice"].Call(mustURI(t, group))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	es := w.users["alice"].expect(t, "established")
	w.last = es

	if e := event(es, client.EventEstablished); e.Response == nil || e.Response.StatusCode != 200 {
		t.Fatalf("200 of alice %+v", e)
	}

	calls := make([]*client.Call, 2)

	for i, name := range []string{"bob", "carol"} {
		es := w.users[name].expect(t, "incoming", "established")
		calls[i] = event(es, client.EventIncoming).Call

		if calls[i].Group().String() != group || calls[i].Caller().String() != w.users["alice"].id || calls[i].Manual() {
			t.Errorf("%s: invited to %s by %s, manual %v", name, calls[i].Group(), calls[i].Caller(), calls[i].Manual())
		}
	}

	return ca, calls[0], calls[1]
}

// talk presses PTT on call, and holds it for d.
func (w *world) talk(call *client.Call, d time.Duration) {
	call.PressPTT()
	w.n.Advance(d)
}

func warned(res *sip.Response, text string) bool {
	ws, err := res.Header.Warnings()
	return err == nil && slices.ContainsFunc(ws, func(w sip.Warning) bool { return w.Code == 399 && w.Text == text })
}

// TS 23.379 §10.6.2.3.1.1.2, TS 24.379 §10.1.1.3.1.1, §10.1.1.4.2, §6.3.2.2.5.2, TS 24.380 §6.3.4.2.2: alice calls
// fire with an implicit floor request; bob and carol, invited, answer automatically; alice's 200 follows their
// accepting, and the floor is granted to her, taken for the others. The steps in order: the group call request (2),
// the group call requests to the members (4a, 4b), their responses (5), the response to alice (6), the floor
// granted and taken (8): the floor control starts on the first acceptance (TS 24.380 §6.3.4.2.2), before the
// response, so that alice's media is forwarded from her first packet; she takes its Floor Granted once established
// (§6.2.4.2.2 step 2), the others their Floor Taken as it comes.
func TestGroupCallSetup(t *testing.T) {
	w := newWorld(t, func(name string, c *client.Config) { c.ImplicitRequest = name == "alice" })

	ca, _, _ := w.call(fire)
	got := w.trace.take()
	at := func(line string) int {
		i := slices.Index(got, line)
		if i < 0 {
			t.Fatalf("no %q in %s", line, strings.Join(got, "\n"))
		}

		return i
	}

	invite := at("alice>server INVITE")
	invites := []int{at("server>bob INVITE"), at("server>carol INVITE")}
	oks := []int{at("bob>server 200 INVITE"), at("carol>server 200 INVITE")}
	ok := at("server>alice 200 INVITE")
	granted, taken := at("alice floor:granted"), []int{at("bob floor:taken"), at("carol floor:taken")}

	// Each member answers its own request; the response to alice follows the requests to the members (NOTE 4) and,
	// without media buffering, the first member accepting (TS 24.380 §6.3.2.2); her floor follows her response.
	inOrder := invite < slices.Min(invites) && invites[0] < oks[0] && invites[1] < oks[1] &&
		slices.Max(invites) < ok && slices.Min(oks) < ok && ok < granted &&
		oks[0] < taken[0] && oks[1] < taken[1]
	if !inOrder {
		t.Errorf("steps out of order:\n%s", strings.Join(got, "\n"))
	}

	// Automatic commencement: no 180 or 183 from the members, invited with Answer-Mode Auto.
	for _, l := range got {
		if strings.HasPrefix(l, "bob>server 18") || strings.HasPrefix(l, "carol>server 18") {
			t.Errorf("provisional response %s", l)
		}
	}

	ca.Leave()
	w.n.Quiesce()
}

// TS 24.380 §6.3.4, §6.3.5, Annex A.3: the floor exchange of a call of three: Request, Granted, Taken, Release, Idle;
// a request while the floor is taken denied; the talker beyond T2 (Stop talking) revoked, and its media stopped by
// T3 (Stop talking grace).
func TestGroupCallFloor(t *testing.T) {
	w := newWorld(t, nil)
	alice, bob, carol := w.users["alice"], w.users["bob"], w.users["carol"]

	ca, cb, cc := w.call(fire)
	w.n.Quiesce()

	for _, u := range []*user{alice, bob, carol} {
		u.take()
	}

	// Request, Granted, Taken.
	w.talk(cb, time.Second)
	bob.expect(t, "floor:granted")
	alice.expectAll(t, "floor:taken", "heard")
	carol.expectAll(t, "floor:taken", "heard")

	if h := ca.Heard(); len(h) != 1 || h[0].Party != bob.id || h[0].Packets == 0 {
		t.Errorf("alice heard %+v", h)
	}

	// Deny: carol asks while bob talks.
	cc.PressPTT()
	w.n.Advance(100 * time.Millisecond)
	carol.expect(t, "floor:denied")
	cc.ReleasePTT()

	// Release, Idle.
	cb.ReleasePTT()
	w.n.Advance(time.Second)

	for _, u := range []*user{alice, bob, carol} {
		u.expect(t, "floor:idle")
	}

	// Revoke after T2 (20 s), the grace of T3 (3 s) then the floor idle.
	w.talk(ca, 21*time.Second)
	alice.expect(t, "floor:granted", "floor:revoked")

	w.n.Advance(5 * time.Second)
	bob.expect(t, "floor:taken", "floor:idle")

	ca.ReleasePTT()
	ca.Leave()
	cb.Leave()
	cc.Leave()
	w.n.Quiesce()
}

// TS 24.379 §6.3.8.1 3: police releases its call when its initiator leaves.
func TestReleaseInitiatorLeaves(t *testing.T) {
	w := newWorld(t, nil)

	ca, _, _ := w.call(police)

	ca.Leave()
	w.n.Quiesce()

	w.users["alice"].expect(t, "ended")
	w.users["bob"].expect(t, "ended")
	w.users["carol"].expect(t, "ended")
}

// TS 24.379 §6.3.3.2.4, §6.3.8.1 2: a member's BYE removes its leg, and the call goes on while two participants are
// left: alice leaving fire does not release it; carol leaving then leaves bob alone, and releases it.
func TestReleaseOneParticipantLeft(t *testing.T) {
	w := newWorld(t, nil)
	bob, carol := w.users["bob"], w.users["carol"]

	ca, cb, cc := w.call(fire)
	w.n.Quiesce()

	ca.Leave()
	w.n.Quiesce()

	w.users["alice"].expect(t, "ended")
	bob.expectNot(t, "ended")
	carol.expectNot(t, "ended")

	// The floor between the two left.
	w.talk(cb, time.Second)
	bob.expect(t, "floor:granted")
	carol.expectAll(t, "floor:taken", "heard")
	cb.ReleasePTT()
	w.n.Advance(time.Second)

	cc.Leave()
	w.n.Quiesce()

	carol.expect(t, "ended")
	bob.expect(t, "ended")
}

// TS 24.379 §6.3.8.1 1, TS 24.380 §6.3.4.3.5: the floor idle for T4 (Inactivity, 30 s by default) releases the call.
func TestReleaseT4(t *testing.T) {
	w := newWorld(t, nil)

	w.call(fire)
	w.n.Advance(29 * time.Second)

	w.users["bob"].expectNot(t, "ended")

	w.n.Advance(2 * time.Second)

	for _, name := range []string{"alice", "bob", "carol"} {
		w.users[name].expect(t, "ended")
	}
}

// TS 24.379 §6.3.3.5, §6.3.8.1 5: TNG3 (group call timer) of short, a minute, releases its call however active.
func TestReleaseTNG3(t *testing.T) {
	w := newWorld(t, nil)

	_, cb, _ := w.call(short)

	w.talk(cb, 50*time.Second)
	cb.ReleasePTT()
	w.n.Advance(5 * time.Second)
	w.users["alice"].expectNot(t, "ended")

	w.n.Advance(6 * time.Second)

	for _, name := range []string{"alice", "bob", "carol"} {
		w.users[name].expect(t, "ended")
	}
}

// silent has u take its INVITEs without answering them, a provisional response aside.
func silent(u *user) {
	u.onInvite = func(r *ua.Request) ua.Handler {
		_ = r.Progress(ua.Reply{Code: 180})
		return ua.Handler{}
	}
}

// TS 24.379 §6.3.3.3 1: carol, required in rescue, does not answer; on TNG1's expiry the call proceeds, alice's 200
// with the warning text 111.
func TestTNG1Proceed(t *testing.T) {
	w := newWorld(t, nil)
	alice := w.users["alice"]
	silent(w.users["carol"])

	ca, err := alice.Call(mustURI(t, rescue))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Advance(4 * time.Second)
	alice.expectNot(t, "established")
	w.users["bob"].expect(t, "incoming", "established")

	w.n.Advance(2 * time.Second)

	es := alice.expect(t, "established")
	if res := event(es, client.EventEstablished).Response; !warned(res, identity.WarnProceededWithoutRequired) {
		t.Errorf("200 warnings %v", res.Header.Values("Warning"))
	}

	ca.Leave()
	w.n.Quiesce()
}

// TS 24.379 §6.3.3.3 2: carol, required in hazmat, does not answer; on TNG1's expiry the call is abandoned: alice gets
// a 480 with the warning text 112, bob, who accepted, a BYE, carol a CANCEL, which its 487 tells.
func TestTNG1Abandon(t *testing.T) {
	w := newWorld(t, nil)
	alice, bob := w.users["alice"], w.users["bob"]
	silent(w.users["carol"])

	if _, err := alice.Call(mustURI(t, hazmat)); err != nil {
		t.Fatal(err)
	}

	w.n.Advance(6 * time.Second)

	es := alice.expect(t, "failed")
	if res := event(es, client.EventFailed).Response; res == nil || res.StatusCode != 480 || !warned(res, identity.WarnAbandonedRequired) {
		t.Fatalf("alice's response %v", res)
	}

	bob.expect(t, "incoming", "established", "ended")

	if got := w.trace.take(); !slices.Contains(got, "carol>server 487 INVITE") || !slices.Contains(got, "server>bob BYE") {
		t.Errorf("no CANCEL to carol, or BYE to bob:\n%s", strings.Join(got, "\n"))
	}
}

// TS 24.379 §10.1.1.4.2 step 14 a, §6.3.6: dave, a member of fire not affiliated to it, gets a 403 with the warning
// text 120; no member is invited.
func TestNotAffiliated(t *testing.T) {
	w := newWorld(t, nil)
	dave := w.users["dave"]

	if _, err := dave.Call(mustURI(t, fire)); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	es := dave.expect(t, "failed")
	if res := event(es, client.EventFailed).Response; res == nil || res.StatusCode != 403 || !warned(res, identity.WarnNotAffiliated) {
		t.Fatalf("dave's response %v", res)
	}

	w.users["bob"].expectNot(t, "incoming")
}

// TS 24.379 §6.3.7.1, §6.3.1.4: an INVITE to a public service identity the server does not have gets a 404; a
// SUBSCRIBE to the conference event package of a session gets a 489 until S2, and to an unknown identity a 404.
func TestRequestDistinction(t *testing.T) {
	w := newWorld(t, nil)
	p := w.n.Peer()

	send := func(method, target string, header ...string) int {
		req := siptest.NewRequest(method, target, sip.UDP, p.Addr())
		req.Header.Set("From", "<sip:alice@ims.example.com>;tag="+sip.NewTag())
		req.Header.Set("To", "<"+target+">")
		req.Header.Set("Call-ID", sip.NewTag()+"@peer")
		req.Header.Set("CSeq", "1 "+method)
		req.Header.Set("Contact", "<sip:peer@"+p.Addr().String()+">")

		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}

		p.Send(sip.UDP, w.addr, req)

		for {
			res, _ := p.RecvResponse()
			if res.StatusCode != 100 {
				return res.StatusCode
			}
		}
	}

	if code := send("INVITE", "sip:nobody@"+domain); code != 404 {
		t.Errorf("INVITE to an unknown PSI: %d", code)
	}

	ca, _, _ := w.call(fire)
	w.n.Quiesce()

	cs, _ := event(w.last, client.EventEstablished).Response.Header.Contacts()
	if len(cs) != 1 {
		t.Fatal("no session identity")
	}

	session := cs[0].URI
	if fs, _ := cs[0].Features(); !fs.Has(identity.FocusTag) || !fs.Has("g.3gpp.mcptt") {
		t.Errorf("Contact %s", cs[0])
	}

	if code := send("SUBSCRIBE", session.String(), "Event", "conference", "Expires", "600"); code != 489 {
		t.Errorf("conference SUBSCRIBE: %d", code)
	}

	if code := send("SUBSCRIBE", "sip:nobody@"+domain, "Event", "conference", "Expires", "600"); code != 404 {
		t.Errorf("SUBSCRIBE to an unknown identity: %d", code)
	}

	ca.Leave()
	w.n.Quiesce()
}

// TS 24.379 §6.3.3.3 (NOTE 3): carol, required in hazmat, refuses before TNG1 expires; the call is abandoned by the
// policy of hazmat, her response forwarded to alice with the warning text 112.
func TestTNG1RequiredRefuses(t *testing.T) {
	w := newWorld(t, nil)
	alice := w.users["alice"]
	w.users["carol"].onInvite = func(r *ua.Request) ua.Handler {
		_ = r.Reject(486)
		return ua.Handler{}
	}

	if _, err := alice.Call(mustURI(t, hazmat)); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	es := alice.expect(t, "failed")
	if res := event(es, client.EventFailed).Response; res == nil || res.StatusCode != 486 || !warned(res, identity.WarnAbandonedRequired) {
		t.Fatalf("alice's response %v", res)
	}
}

// TS 24.379 §10.1.1.4.2 step 15: dave, affiliated once fire's call is ongoing, calls fire and joins it: his 200 at
// once, with the warning text 123, the floor's state told; he talks to the others.
func TestJoinOngoing(t *testing.T) {
	w := newWorld(t, nil)
	dave := w.users["dave"]

	ca, _, _ := w.call(fire)

	var res *sip.Response

	if err := dave.Affiliate([]sip.URI{mustURI(t, fire)}, func(r *sip.Response, _ error) { res = r }); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	if res == nil || res.StatusCode != 200 {
		t.Fatalf("PUBLISH %v", res)
	}

	cd, err := dave.Call(mustURI(t, fire))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	es := dave.expect(t, "established", "floor:idle")
	if res := event(es, client.EventEstablished).Response; !warned(res, identity.WarnSessionExists) {
		t.Errorf("200 warnings %v", res.Header.Values("Warning"))
	}

	w.users["bob"].expectNot(t, "incoming")

	w.talk(cd, time.Second)
	dave.expect(t, "floor:granted")
	w.users["alice"].expectAll(t, "floor:taken", "heard")

	cd.ReleasePTT()
	cd.Leave()
	ca.Leave()
	w.n.Quiesce()
}

// TS 24.380 §6.3.5.2.2 3: dave, pressing his PTT for fire while its call is ongoing and its floor idle, calls fire
// with an implicit floor request: he joins it with the floor granted, as a Floor Request (§6.3.4.3.3), without
// mc_granted; the others are told it taken.
func TestJoinOngoingImplicit(t *testing.T) {
	w := newWorld(t, func(name string, c *client.Config) { c.ImplicitRequest = name == "dave" })
	dave := w.users["dave"]

	ca, _, _ := w.call(fire)

	if err := dave.Affiliate([]sip.URI{mustURI(t, fire)}, func(*sip.Response, error) {}); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	cd, err := dave.Call(mustURI(t, fire))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	es := dave.expect(t, "established", "floor:granted")
	res := event(es, client.EventEstablished).Response

	if !warned(res, identity.WarnSessionExists) {
		t.Errorf("200 warnings %v", res.Header.Values("Warning"))
	}

	// The implicit request accepted in the answer (§14.3.5), not only the client's Floor Request after it.
	if part, ok, err := res.Content().Find("application/sdp"); err != nil || !ok || !strings.Contains(string(part.Body), "mc_implicit_request") {
		t.Errorf("200 SDP without mc_implicit_request: %v %v", ok, err)
	}

	w.users["alice"].expectAll(t, "floor:taken")

	cd.ReleasePTT()
	cd.Leave()
	ca.Leave()
	w.n.Quiesce()
}
