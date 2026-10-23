//go:build e2e && linux

package e2e

import (
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/client/clienttest"
	"github.com/ellanetworks/ims/mcptt/controlchannel"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/testue"
	imsue "github.com/ellanetworks/ims/ue"
)

// The MCPTT system of mcptt/provision.sh.
const (
	domain = "mcptt.example.com"
	// psi is the participating PSI of the MCPTT AS of the IMS, of its home domain.
	psi    = "sip:mcptt-participating@mcptt.ims.mnc001.mcc001.3gppnetwork.org"
	fire   = "sip:fire@" + domain
	police = "sip:police@" + domain
	rescue = "sip:rescue@" + domain
	quiet  = "sip:quiet@" + domain
	brief  = "sip:brief@" + domain
)

// mcUE is the test UE of a subscriber, a dedicated MC UE (no MMTel, no SMS) hosting the MCPTT service: ns its network
// namespace, local its address there, invites the initial INVITEs it received.
type mcUE struct {
	*testue.UE

	ns      *netns
	local   netip.Addr
	invites atomic.Int32
}

// newMCUE makes the MC UE of subscriber i, hosting the services.
func newMCUE(t *testing.T, i int, services ...imsue.Service) *mcUE {
	t.Helper()

	out := &mcUE{}

	u, ns, local := newUEIn(t, i, sip.UDP, testue.Config{
		NoMMTel: true, NoSMS: true, Services: services,
		Trace: func(m sip.Message, _ sip.Flow, sent bool) {
			if req, ok := m.(*sip.Request); ok && !sent && req.Method == "INVITE" {
				if to, err := req.Header.To(); err == nil && to.Tag() == "" {
					out.invites.Add(1)
				}
			}
		},
	})

	out.UE, out.ns, out.local = u, ns, local

	return out
}

// mcpttService is the name of the MCPTT service of the UEs.
const mcpttService = "mcptt"

// service is internal/client as a service of a testue UE (TS 24.379 §7.2.1, §10.1.1.2.1.1): the client c holds
// once made, on the UE's agent, and disabled until then, the UE registered without it.
func service(c *atomic.Pointer[client.Client]) imsue.Service {
	var features sip.FeatureSet

	// The UE sets the ICSI's g.3gpp.icsi-ref itself.
	icsiRef := sip.NewFeature(identity.ICSITag).Tag

	for _, f := range client.Features() {
		if f.Tag != icsiRef {
			features = append(features, f)
		}
	}

	var accept sip.Header
	for _, v := range client.AcceptContact() {
		accept.Add("Accept-Contact", v)
	}

	// The client's values parse.
	prefs, _ := accept.AcceptContact()

	return imsue.Service{
		Name:          mcpttService,
		ICSI:          identity.ICSI,
		Features:      features,
		AcceptContact: prefs,
		// Its sessions: INVITE, their refreshes and reliable provisional responses.
		Allow: []string{"INVITE", "UPDATE", "PRACK"},
		// An offer with the control channel (TS 24.380 §4.3.3.1).
		Match: func(req *sip.Request) bool {
			part, ok, err := req.Content().Find("application/sdp")
			if err != nil || !ok {
				return false
			}

			s, err := sdp.Parse(part.Body)

			return err == nil && controlchannel.Channel(s) >= 0
		},
		// The mcptt-info of the service authorisation (TS 24.379 §7.2.1), in the REGISTER the third-party REGISTER
		// carries (§7.3.2).
		RegisterBodies: func() []sip.BodyPart {
			if cl := c.Load(); cl != nil {
				return cl.RegisterBodies()
			}

			return nil
		},
		Disabled: true,
		OnInvite: func(r *ua.Request) ua.Handler {
			if cl := c.Load(); cl != nil {
				return cl.OnInvite(r)
			}

			_ = r.Reject(480)

			return ua.Handler{}
		},
	}
}

// nsNetwork is the UDP network of a UE's namespace: the media sockets of its client.
type nsNetwork struct{ ns *netns }

func (n nsNetwork) Listen(addr netip.AddrPort, h func(b []byte, from netip.AddrPort)) (media.Conn, error) {
	var (
		c   media.Conn
		err error
	)

	n.ns.Do(func() { c, err = media.UDP{}.Listen(addr, h) })

	return c, err
}

// user is an MCPTT user of mcptt/provision.sh with its client, on the UE of its subscriber.
type user struct {
	*client.Client

	name   string
	id     string
	ue     *mcUE
	events chan client.Event
}

var floorTimers = floor.ParticipantTimers{T100: time.Second, T101: time.Second}

// newUser makes user name, of subscriber i: its UE registered with the IMS, then its client logged on, the UE
// re-registering with the MCPTT feature tags and the mcptt-info (TS 24.379 §7.2.1; TS 24.229 §5.1.1.4.1: the ICSIs
// it uses changed), which the S-CSCF tells the MCPTT server of in a third-party REGISTER (TS 24.229 §5.4.1.7).
func newUser(t *testing.T, name string, i int) *user {
	t.Helper()

	var c atomic.Pointer[client.Client]

	u := &user{name: name, id: "sip:" + name + "@" + domain, events: make(chan client.Event, 1024)}
	u.ue = newMCUE(t, i, service(&c))
	register(t, u.ue.UE)

	impu, err := sip.ParseURI(u.ue.State().DefaultIMPU)
	if err != nil {
		t.Fatalf("default IMPU: %v", err)
	}

	speaker := clienttest.NewSpeaker(u.ue.Agent().Layer().Clock())

	cl, err := client.New(client.Config{
		Agent:     u.ue.Agent(),
		Originate: func(req *sip.Request) error { return u.ue.Originate(req, mcpttService) },
		IMPU:      impu,
		MCPTTID:   mustURI(t, u.id),
		ClientID:  fmt.Sprintf("urn:uuid:00000000-0000-1000-8000-%012d", i+1),
		Contact: func() sip.Address {
			c, _ := u.ue.Contact(mcpttService)
			return c
		},
		ParticipatingPSI: mustURI(t, psi),
		Media:            nsNetwork{u.ue.ns},
		Addr:             u.ue.local,
		MinPort:          30000,
		MaxPort:          30099,
		Floor:            floorTimers,
		OnEvent: func(e client.Event) {
			speaker.Event(e)

			select {
			case u.events <- e:
			default:
				t.Errorf("%s: event %s dropped", name, e.Kind)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	u.Client = cl
	c.Store(cl)

	if err := u.ue.Enable(ctx(t), mcpttService); err != nil {
		t.Fatalf("%s: log on: %v", name, err)
	}

	return u
}

// users are alice, bob and carol, logged on.
func users(t *testing.T) (alice, bob, carol *user) {
	return newUser(t, "alice", 0), newUser(t, "bob", 1), newUser(t, "carol", 2)
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

func kind(e client.Event) string {
	if e.Kind == client.EventFloor {
		return "floor:" + notices[e.Floor.Kind]
	}

	return e.Kind.String()
}

// wait is how long an event may take: the SIP and floor control round trips through the IMS and the RAN simulator.
const wait = 15 * time.Second

// The timers of mcptt/provision.sh: T2 (Stop talking) of the service configuration, T4 (Inactivity) of quiet, TNG3
// (Group call timer) of brief.
const (
	t2   = 5 * time.Second
	t4   = 3 * time.Second
	tng3 = 4 * time.Second
)

// expect waits for the events want, in order, others between them aside, and returns those it read.
func (u *user) expect(t *testing.T, want ...string) []client.Event {
	t.Helper()

	var got []client.Event

	deadline := time.After(wait)

	for i := 0; i < len(want); {
		select {
		case e := <-u.events:
			got = append(got, e)

			if kind(e) == want[i] {
				i++
			}
		case <-deadline:
			t.Fatalf("%s: events %v, want %v in order", u.name, kinds(got), want)
		}
	}

	return got
}

// expectOr is expect, failing at once on the event fail: a call failing before it is established.
func (u *user) expectOr(t *testing.T, fail string, want ...string) []client.Event {
	t.Helper()

	var got []client.Event

	deadline := time.After(wait)

	for i := 0; i < len(want); {
		select {
		case e := <-u.events:
			got = append(got, e)

			switch kind(e) {
			case fail:
				if e.Response != nil {
					t.Fatalf("%s: %s:\n%s", u.name, fail, e.Response.Bytes())
				}

				t.Fatalf("%s: %s: %v", u.name, fail, e.Err)
			case want[i]:
				i++
			}
		case <-deadline:
			t.Fatalf("%s: events %v, want %v in order", u.name, kinds(got), want)
		}
	}

	return got
}

// expectAll waits for the events want, in any order: the first media of a talker races the Floor Taken announcing
// it (TS 24.380 §6.2.4.4.2 step 7).
func (u *user) expectAll(t *testing.T, want ...string) {
	t.Helper()

	var got []client.Event

	deadline := time.After(wait)

	for {
		ks := kinds(got)

		if !slices.ContainsFunc(want, func(k string) bool { return !slices.Contains(ks, k) }) {
			return
		}

		select {
		case e := <-u.events:
			got = append(got, e)
		case <-deadline:
			t.Fatalf("%s: events %v, want %v", u.name, ks, want)
		}
	}
}

// silence is how long expectNot watches for events that must not come.
const silence = 2 * time.Second

// expectNot checks that none of the events of the next silence are not.
func (u *user) expectNot(t *testing.T, not ...string) {
	t.Helper()

	deadline := time.After(silence)

	for {
		select {
		case e := <-u.events:
			if slices.Contains(not, kind(e)) {
				t.Fatalf("%s: event %s", u.name, kind(e))
			}
		case <-deadline:
			return
		}
	}
}

func kinds(es []client.Event) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = kind(e)
	}

	return out
}

func event(es []client.Event, k client.EventKind) *client.Event {
	for i := range es {
		if es[i].Kind == k {
			return &es[i]
		}
	}

	return nil
}

// call places a call of a to group, its members b and c invited and answering automatically: the calls of each.
func call(t *testing.T, group string, a, b, c *user) (ca, cb, cc *client.Call) {
	t.Helper()

	calls := callOf(t, group, a, b, c)

	return calls[0], calls[1], calls[2]
}

// callOf places a call of a to group, the members invited and answering automatically: the call of a, then of each
// member. The calls left at the end of the test are left, before the UEs deregister.
func callOf(t *testing.T, group string, a *user, members ...*user) []*client.Call {
	t.Helper()

	ca, err := a.Call(mustURI(t, group))
	if err != nil {
		t.Fatal(err)
	}

	calls := []*client.Call{ca}

	t.Cleanup(func() {
		for _, c := range calls {
			c.Leave()
		}

		// The BYEs before the deregistrations of the cleanups of newMCUE.
		time.Sleep(time.Second)
	})

	es := a.expectOr(t, "failed", "established")
	if e := event(es, client.EventEstablished); e == nil || e.Response == nil || e.Response.StatusCode != 200 {
		t.Fatalf("%s: 200 %+v", a.name, e)
	}

	for _, u := range members {
		es := u.expect(t, "incoming", "established")
		c := event(es, client.EventIncoming).Call
		calls = append(calls, c)

		if c.Group().String() != group || c.Caller().String() != a.id || c.Manual() {
			t.Errorf("%s: invited to %s by %s, manual %v", u.name, c.Group(), c.Caller(), c.Manual())
		}
	}

	return calls
}

func warned(res *sip.Response, text string) bool {
	ws, err := res.Header.Warnings()
	return err == nil && slices.ContainsFunc(ws, func(w sip.Warning) bool { return w.Code == 399 && w.Text == text })
}

// TS 24.379 §7.2.1, §7.3.2, §9.2.1.2, §9.2.2.2: the users log on through the IMS, the server authorising them from
// the mcptt-info of the REGISTER that the third-party REGISTER carries; each then publishes its affiliation to fire
// to the participating function, which takes it from a user it serves.
func TestRegisterAffiliate(t *testing.T) {
	for _, u := range []*user{newUser(t, "alice", 0), newUser(t, "bob", 1), newUser(t, "carol", 2)} {
		res := make(chan *sip.Response, 1)

		err := u.Affiliate([]sip.URI{mustURI(t, fire)}, func(r *sip.Response, err error) {
			if err != nil {
				t.Errorf("%s: PUBLISH: %v", u.name, err)
			}

			res <- r
		})
		if err != nil {
			t.Fatal(err)
		}

		select {
		case r := <-res:
			if r == nil || !r.IsSuccess() {
				t.Fatalf("%s: PUBLISH answered %v", u.name, r)
			}
		case <-time.After(wait):
			t.Fatalf("%s: PUBLISH unanswered", u.name)
		}
	}
}

// TS 24.379 §10.1.1, TS 24.380 §6.2.4, §6.3.4, §6.3.5, Annex A.3: alice calls fire, bob and carol answering
// automatically; the floor exchange: Request, Granted, Taken with bob's media heard, a request while the floor is
// taken denied, Release, Idle, a talker past T2 revoked; the call goes on when alice leaves, and ends when carol leaves
// bob alone in it (§6.3.8.1 2).
func TestGroupCall(t *testing.T) {
	alice, bob, carol := users(t)
	ca, cb, cc := call(t, fire, alice, bob, carol)

	// Request, Granted, Taken.
	cb.PressPTT()
	bob.expect(t, "floor:granted")
	alice.expectAll(t, "floor:taken", "heard")
	carol.expectAll(t, "floor:taken", "heard")

	if h := ca.Heard(); len(h) != 1 || h[0].Party != bob.id || h[0].Packets == 0 {
		t.Errorf("alice heard %+v", h)
	}

	// Deny: carol asks while bob talks.
	cc.PressPTT()
	carol.expect(t, "floor:denied")
	cc.ReleasePTT()

	// Release, Idle.
	cb.ReleasePTT()

	for _, u := range []*user{alice, bob, carol} {
		u.expect(t, "floor:idle")
	}

	// Revoke after T2, the floor idle after the grace of T3 (3 s).
	ca.PressPTT()
	alice.expect(t, "floor:granted")

	granted := time.Now()

	bob.expect(t, "floor:taken")
	alice.expect(t, "floor:revoked")

	if d := time.Since(granted); d < t2-time.Second {
		t.Errorf("alice revoked %v after the grant, T2 %v", d, t2)
	}

	bob.expect(t, "floor:idle")
	ca.ReleasePTT()

	// alice leaving does not release fire; carol leaving then leaves bob alone, and does.
	ca.Leave()
	alice.expect(t, "ended")
	bob.expectNot(t, "ended")

	cc.Leave()
	carol.expect(t, "ended")
	bob.expect(t, "ended")
}

// TS 24.379 §7.2.1 NOTE 1, §7.3.2; TS 24.229 §5.4.1.7: carol logs off, her UE re-registering without the MCPTT
// feature tags, and the S-CSCF tells the server in a third-party REGISTER: alice's call to police invites bob, not
// carol. carol logs on again and is invited to the next call. Then her UE deregisters, which the third-party REGISTER
// of Expires 0 tells (§5.4.1.7 f), and she is not invited to the next.
//
// The S-CSCF would not route an INVITE of the server to the contact of a logged-off carol either, as it lacks the
// MCPTT feature tags the INVITE requires (TS 24.379 §6.3.3.1.2 steps 2 and 4, §6.3.2.2.3 step 1; RFC 3841 §7.2): that
// the server drops the binding is checked by internal/server's tests.
func TestLogOff(t *testing.T) {
	alice, bob, carol := users(t)

	// police releases its calls when alice, their initiator, leaves (§6.3.8.1 3).
	end := func(calls []*client.Call, members ...*user) {
		calls[0].Leave()
		alice.expect(t, "ended")

		for _, u := range members {
			u.expect(t, "ended")
		}
	}

	if err := carol.ue.Disable(ctx(t), mcpttService); err != nil {
		t.Fatalf("carol: log off: %v", err)
	}

	before := carol.ue.invites.Load()

	end(callOf(t, police, alice, bob), bob)
	carol.expectNot(t, "incoming")

	if n := carol.ue.invites.Load() - before; n != 0 {
		t.Errorf("carol's UE logged off got %d INVITEs", n)
	}

	if err := carol.ue.Enable(ctx(t), mcpttService); err != nil {
		t.Fatalf("carol: log on: %v", err)
	}

	end(callOf(t, police, alice, bob, carol), bob, carol)

	if err := carol.ue.Deregister(ctx(t)); err != nil {
		t.Fatalf("carol: deregister: %v", err)
	}

	before = carol.ue.invites.Load()

	end(callOf(t, police, alice, bob), bob)
	carol.expectNot(t, "incoming")

	if n := carol.ue.invites.Load() - before; n != 0 {
		t.Errorf("carol's UE deregistered got %d INVITEs", n)
	}
}

// TS 24.380 §6.3.4.3.2, §6.3.4.3.5; TS 24.379 §6.3.8.1 1: a call of quiet, its floor idle for T4, is released.
func TestReleaseInactivity(t *testing.T) {
	alice, bob, _ := users(t)

	callOf(t, quiet, alice, bob)

	established := time.Now()

	alice.expect(t, "ended")
	bob.expect(t, "ended")

	if d := time.Since(established); d < t4-time.Second {
		t.Errorf("released %v after its setup, T4 %v", d, t4)
	}
}

// TS 24.379 §6.3.3.5.1, §6.3.8.1 5: a call of brief is released when TNG3 expires, though its floor is taken, before
// T2 would revoke it.
func TestReleaseMaximumDuration(t *testing.T) {
	alice, bob, _ := users(t)

	calls := callOf(t, brief, alice, bob)
	established := time.Now()

	calls[0].PressPTT()
	alice.expect(t, "floor:granted")

	alice.expect(t, "ended")
	bob.expect(t, "ended")

	if d := time.Since(established); d < tng3-time.Second {
		t.Errorf("released %v after its setup, TNG3 %v", d, tng3)
	}
}

// TS 24.379 §6.3.8.1 3: police releases its calls when its initiator leaves.
func TestReleaseInitiatorLeaves(t *testing.T) {
	alice, bob, carol := users(t)
	ca, _, _ := call(t, police, alice, bob, carol)

	ca.Leave()

	for _, u := range []*user{alice, bob, carol} {
		u.expect(t, "ended")
	}
}

// TS 24.379 §10.1.1.4.2 step 14 a, §6.3.6: carol, a member of rescue affiliated to it by no one, gets a 403 with the
// warning text 120, and alice no invitation.
func TestNotAffiliated(t *testing.T) {
	alice, _, carol := users(t)

	if _, err := carol.Call(mustURI(t, rescue)); err != nil {
		t.Fatal(err)
	}

	es := carol.expect(t, "failed")
	if res := event(es, client.EventFailed).Response; res == nil || res.StatusCode != 403 || !warned(res, identity.WarnNotAffiliated) {
		t.Fatalf("carol's response %v", res)
	}

	alice.expectNot(t, "incoming")
}

// TS 23.379 §5.7.3; TS 29.514 §4.2.2.2, TS 29.513 §7.2.3: each participant of a group call gets a QoS flow for the
// voice and one for the floor control channel, whose application media component the P-CSCF authorises with the
// PCF (open5gs/qos.patch), released with the call. In 4G the call itself shows that the PCRF authorised them: the
// P-CSCF releases a call whose media it refuses.
func TestMCPTTCallQoSFlows(t *testing.T) {
	if os.Getenv("E2E_RAT") != "5g" {
		t.Skip("QoS flows: run with E2E_RAT=5g")
	}

	pid, err := strconv.Atoi(os.Getenv("E2E_OPEN5GS_PID"))
	if err != nil {
		t.Skip("E2E_OPEN5GS_PID not set: run through e2e/run.sh")
	}

	core := enter(t, pid)
	alice, bob, carol := users(t)
	before := qosFlows(t, core)

	// police releases its calls when its initiator leaves.
	ca, _, _ := call(t, police, alice, bob, carol)

	waitQoSFlows(t, core, before+3*2)

	// TS 23.379 §5.7.3: voice on 65, floor control on 69 (open5gs/qos.patch), the caller's as the members': its
	// P-CSCF has the MCPTT ICSI from the Feature-Caps of the 200 (TS 29.514 Annex B.1 NOTE 3).
	qis := fiveQIs(t, core)
	for i, u := range []*user{alice, bob, carol} {
		if got := qis["imsi-"+subscribers[i].imsi]; !slices.Contains(got, 65) || !slices.Contains(got, 69) {
			t.Errorf("5QIs of %s's QoS flows %v, want 65 and 69 among them", u.name, got)
		}
	}

	ca.Leave()

	for _, u := range []*user{alice, bob, carol} {
		u.expect(t, "ended")
	}

	waitQoSFlows(t, core, before)
}
