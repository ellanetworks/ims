//go:build e2e && linux

package e2e

import (
	"context"
	"fmt"
	"maps"
	"net/netip"
	"net/url"
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

const (
	domain = "mcptt.example.com"
	psi    = "sip:mcptt-participating@mcptt.ims.mnc001.mcc001.3gppnetwork.org"
	fire   = "sip:fire@" + domain
	police = "sip:police@" + domain
	rescue = "sip:rescue@" + domain
	quiet  = "sip:quiet@" + domain
	brief  = "sip:brief@" + domain
)

type mcUE struct {
	*testue.UE

	ns      *netns
	local   netip.Addr
	invites atomic.Int32
}

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

const mcpttService = "mcptt"

// TS 24.379 clause 7.2.1
// TS 24.379 clause 10.1.1.2.1.1
func service(c *atomic.Pointer[client.Client]) imsue.Service {
	var features sip.FeatureSet

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

	prefs, _ := accept.AcceptContact()

	return imsue.Service{
		Name:          mcpttService,
		ICSI:          identity.ICSI,
		Features:      features,
		AcceptContact: prefs,
		Allow:         []string{"INVITE", "UPDATE", "PRACK"},
		Match: func(req *sip.Request) bool {
			part, ok, err := req.Content().Find("application/sdp")
			if err != nil || !ok {
				return false
			}

			s, err := sdp.Parse(part.Body)

			return err == nil && controlchannel.Channel(s) >= 0
		},
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

type nsNetwork struct{ ns *netns }

func (n nsNetwork) Listen(addr netip.AddrPort, h func(b []byte, from netip.AddrPort)) (media.Conn, error) {
	var (
		c   media.Conn
		err error
	)

	n.ns.Do(func() { c, err = media.UDP{}.Listen(addr, h) })

	return c, err
}

type user struct {
	*client.Client

	name   string
	id     string
	sub    int
	client string
	ue     *mcUE
	events chan client.Event
}

var floorTimers = floor.ParticipantTimers{T100: time.Second, T101: time.Second}

// TS 24.379 clause 7.2.1
// TS 24.229 clause 5.1.1.4.1
// TS 24.229 clause 5.4.1.7
func newUser(t *testing.T, name string, i int) *user {
	t.Helper()

	var c atomic.Pointer[client.Client]

	u := &user{
		name: name, id: "sip:" + name + "@" + domain, sub: i,
		client: fmt.Sprintf("urn:uuid:00000000-0000-1000-8000-%012d", i+1), events: make(chan client.Event, 1024),
	}
	u.ue = newMCUE(t, i, service(&c))
	register(t, u.ue.UE)

	impu, err := sip.ParseURI(u.ue.State().DefaultIMPU)
	if err != nil {
		t.Fatalf("default IMPU: %v", err)
	}

	speaker := clienttest.NewSpeaker(u.ue.Agent().Layer().Clock())

	var lastIdle bool

	cl, err := client.New(client.Config{
		Agent:     u.ue.Agent(),
		Originate: func(req *sip.Request) error { return u.ue.Originate(req, mcpttService) },
		IMPU:      impu,
		MCPTTID:   mustURI(t, u.id),
		ClientID:  u.client,
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

			if e.Kind == client.EventFloor {
				idle := e.Floor.Kind == floor.NoticeIdle
				if idle && lastIdle {
					return
				}

				lastIdle = idle
			}

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

func users(t *testing.T) (alice, bob, carol *user) {
	return newUser(t, "alice", 0), newUser(t, "bob", 1), newUser(t, "carol", 2)
}

type registration struct {
	ClientID string   `json:"client_id"`
	IMPU     string   `json:"impu"`
	IMPUs    []string `json:"impus"`
}

type affiliation struct {
	GroupID  string `json:"group_id"`
	Status   string `json:"status"`
	Implicit bool   `json:"implicit"`
}

func (u *user) path(list string) string {
	return "/api/v1/mcptt/" + list + "?mcptt_id=" + url.QueryEscape(u.id)
}

func wantRegistered(t *testing.T, core *netns, u *user) {
	t.Helper()

	p := poll(t, core, u.path("registrations"), func(p page[registration]) bool { return len(p.Items) > 0 })

	impu := "sip:" + subscribers[u.sub].msisdn + "@" + homeDomain

	if r := p.Items; len(r) != 1 || r[0].ClientID != u.client || !slices.Contains(r[0].IMPUs, r[0].IMPU) ||
		!slices.Contains(r[0].IMPUs, impu) {
		t.Fatalf("%s: registrations %+v, want one of %s, served at %s", u.name, r, u.client, impu)
	}
}

// TS 24.379 clause 9.2.2.2.11
// TS 24.379 clause 9.2.2.2.7
func wantAffiliated(t *testing.T, core *netns, u *user, want map[string]bool) {
	t.Helper()

	poll(t, core, u.path("affiliations"), func(p page[affiliation]) bool {
		n := 0

		for _, a := range p.Items {
			implicit, ok := want[a.GroupID]

			switch {
			case ok && a.Status == "affiliated" && a.Implicit == implicit:
				n++
			case a.Status == "affiliating" || a.Status == "affiliated":
				return false
			}
		}

		return n == len(want)
	})
}

// TS 23.280 clause 5.2.5
func wantLoggedOff(t *testing.T, core *netns, u *user) {
	t.Helper()

	none := func(p page[struct{}]) bool { return len(p.Items) == 0 }

	poll(t, core, u.path("registrations"), none)
	poll(t, core, u.path("affiliations"), none)
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

const wait = 15 * time.Second

const (
	t2   = 5 * time.Second
	t4   = 3 * time.Second
	tng3 = 4 * time.Second
)

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

const silence = 2 * time.Second

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

func call(t *testing.T, group string, a, b, c *user) (ca, cb, cc *client.Call) {
	t.Helper()

	calls := callOf(t, group, a, b, c)

	return calls[0], calls[1], calls[2]
}

func callOf(t *testing.T, group string, a *user, members ...*user) []*client.Call {
	t.Helper()

	ca, err := a.Call(context.Background(), mustURI(t, group))
	if err != nil {
		t.Fatal(err)
	}

	calls := []*client.Call{ca}

	t.Cleanup(func() {
		for _, c := range calls {
			c.Leave(context.Background())
		}

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

// TS 24.379 clause 7.2.1
// TS 24.379 clause 7.3.2
// TS 24.379 clause 9.2.1.2
// TS 24.379 clause 9.2.2.2
// TS 24.379 clause 9.2.2.2.15
// TS 24.379 clause 9.2.2.2.3
func TestRegisterAffiliate(t *testing.T) {
	core := enter(t, pidOf(t, "E2E_OPEN5GS_PID"))
	alice, bob, carol := users(t)

	for _, c := range []struct {
		u                 *user
		implicit, publish map[string]bool
	}{
		{
			alice,
			map[string]bool{fire: true, police: true, quiet: true, brief: true},
			map[string]bool{fire: false, rescue: false},
		},
		{bob, map[string]bool{fire: true, police: true, quiet: true, brief: true}, map[string]bool{fire: false}},
		{carol, map[string]bool{fire: true, police: true}, map[string]bool{fire: false}},
	} {
		u := c.u

		wantRegistered(t, core, u)
		wantAffiliated(t, core, u, c.implicit)

		var groups []sip.URI
		for _, g := range slices.Sorted(maps.Keys(c.publish)) {
			groups = append(groups, mustURI(t, g))
		}

		res := make(chan *sip.Response, 1)

		err := u.Affiliate(context.Background(), groups, func(r *sip.Response, err error) {
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

		wantAffiliated(t, core, u, c.publish)
	}
}

// TS 24.379 clause 10.1.1
// TS 24.380 clause 6.2.4
// TS 24.380 clause 6.3.4
// TS 24.380 clause 6.3.5
// TS 24.380 clause A.3
// TS 24.379 clause 6.3.8.1
func TestGroupCall(t *testing.T) {
	alice, bob, carol := users(t)
	ca, cb, cc := call(t, fire, alice, bob, carol)

	cb.PressPTT()
	bob.expect(t, "floor:granted")
	alice.expectAll(t, "floor:taken", "heard")
	carol.expectAll(t, "floor:taken", "heard")

	if h := ca.Heard(); len(h) != 1 || h[0].Party != bob.id || h[0].Packets == 0 {
		t.Errorf("alice heard %+v", h)
	}

	cc.PressPTT()
	carol.expect(t, "floor:denied")
	cc.ReleasePTT()

	cb.ReleasePTT()

	for _, u := range []*user{alice, bob, carol} {
		u.expect(t, "floor:idle")
	}

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

	ca.Leave(context.Background())
	alice.expect(t, "ended")
	bob.expectNot(t, "ended")

	cc.Leave(context.Background())
	carol.expect(t, "ended")
	bob.expect(t, "ended")
}

// TS 24.379 clause 7.2.1
// TS 24.379 clause 7.3.2
// TS 24.229 clause 5.4.1.7
// TS 24.379 clause 9.2.2.2.15
// TS 23.280 clause 5.2.5
func TestLogOff(t *testing.T) {
	core := enter(t, pidOf(t, "E2E_OPEN5GS_PID"))
	alice, bob, carol := users(t)

	end := func(calls []*client.Call, members ...*user) {
		calls[0].Leave(context.Background())
		alice.expect(t, "ended")

		for _, u := range members {
			u.expect(t, "ended")
		}
	}

	if err := carol.ue.Disable(ctx(t), mcpttService); err != nil {
		t.Fatalf("carol: log off: %v", err)
	}

	wantLoggedOff(t, core, carol)

	before := carol.ue.invites.Load()

	end(callOf(t, police, alice, bob), bob)
	carol.expectNot(t, "incoming")

	if n := carol.ue.invites.Load() - before; n != 0 {
		t.Errorf("carol's UE logged off got %d INVITEs", n)
	}

	if err := carol.ue.Enable(ctx(t), mcpttService); err != nil {
		t.Fatalf("carol: log on: %v", err)
	}

	wantRegistered(t, core, carol)
	wantAffiliated(t, core, carol, map[string]bool{fire: true, police: true})

	end(callOf(t, police, alice, bob, carol), bob, carol)

	if err := carol.ue.Deregister(ctx(t)); err != nil {
		t.Fatalf("carol: deregister: %v", err)
	}

	wantLoggedOff(t, core, carol)

	before = carol.ue.invites.Load()

	end(callOf(t, police, alice, bob), bob)
	carol.expectNot(t, "incoming")

	if n := carol.ue.invites.Load() - before; n != 0 {
		t.Errorf("carol's UE deregistered got %d INVITEs", n)
	}
}

// TS 24.380 clause 6.3.4.3.2
// TS 24.380 clause 6.3.4.3.5
// TS 24.379 clause 6.3.8.1
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

// TS 24.379 clause 6.3.3.5.1
// TS 24.379 clause 6.3.8.1
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

// TS 24.379 clause 6.3.8.1
func TestReleaseInitiatorLeaves(t *testing.T) {
	alice, bob, carol := users(t)
	ca, _, _ := call(t, police, alice, bob, carol)

	ca.Leave(context.Background())

	for _, u := range []*user{alice, bob, carol} {
		u.expect(t, "ended")
	}
}

// TS 24.379 clause 10.1.1.4.2
// TS 24.379 clause 6.3.6
func TestNotAffiliated(t *testing.T) {
	alice, _, carol := users(t)

	if _, err := carol.Call(context.Background(), mustURI(t, rescue)); err != nil {
		t.Fatal(err)
	}

	es := carol.expect(t, "failed")
	if res := event(es, client.EventFailed).Response; res == nil || res.StatusCode != 403 || !warned(res, identity.WarnNotAffiliated) {
		t.Fatalf("carol's response %v", res)
	}

	alice.expectNot(t, "incoming")
}

// TS 23.379 clause 5.7.3
// TS 29.514 clause 4.2.2.2
// TS 29.513 clause 7.2.3
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

	ca, _, _ := call(t, police, alice, bob, carol)

	waitQoSFlows(t, core, before+3*2)

	qis := fiveQIs(t, core)
	for i, u := range []*user{alice, bob, carol} {
		if got := qis["imsi-"+subscribers[i].imsi]; !slices.Contains(got, 65) || !slices.Contains(got, 69) {
			t.Errorf("5QIs of %s's QoS flows %v, want 65 and 69 among them", u.name, got)
		}
	}

	ca.Leave(context.Background())

	for _, u := range []*user{alice, bob, carol} {
		u.expect(t, "ended")
	}

	waitQoSFlows(t, core, before)
}
