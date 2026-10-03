package testue

import (
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

const (
	scscfContact = "<sip:scscf." + domain + ":6060>"
	pcscfRR      = "<sip:token@pcscf." + domain + ";lr>"
	telIMPU      = "tel:+15550001"
)

type notifier struct {
	n      *network
	u      *UE
	sub    *sip.Request
	sf     sip.Flow
	toTag  string
	cseq   int
	ver    uint64
	portUS uint16
}

func newRegEventNetwork(t *testing.T) *network {
	n := newNetwork(t)
	n.regEvent = true

	return n
}

func (n *network) registerSubscribing(u *UE) *notifier {
	n.t.Helper()

	done := start(u.Register)

	req, f := n.recv(n.pcscf)
	n.challenge(req, f, n.pcscf, true)

	req, f = n.recv(n.ps)
	n.ok(req, f, n.pc, 3600)

	sub, sf := n.recv(n.ps)
	if sub.Method != "SUBSCRIBE" {
		n.t.Fatalf("got %s after the registration, want SUBSCRIBE", sub.Method)
	}

	no := &notifier{n: n, u: u, sub: sub, sf: sf, toTag: sip.NewTag(), portUS: u.SAs()[0].Set.Local.PortS}
	no.accept(sub, sf)

	if err := wait(n.t, done); err != nil {
		n.t.Fatal(err)
	}

	return no
}

func (no *notifier) accept(req *sip.Request, f sip.Flow) {
	no.n.t.Helper()

	res := sip.NewResponse(req, 200, "")
	if to, _ := req.Header.To(); to.Tag() == "" {
		_ = res.Header.SetToTag(no.toTag)
	}

	res.Header.Add("Record-Route", pcscfRR)
	res.Header.Add("Contact", scscfContact)
	res.Header.Add("Expires", "600000")
	no.n.reply(no.n.pc, f, res)
}

func (no *notifier) contact() string {
	return sip.URI{Scheme: "sip", User: no.u.user, Host: sip.FormatHost(no.n.addr), Port: no.portUS}.String()
}

func (no *notifier) reginfo(regState, contactState string, event regevent.Event, expires uint32) regevent.Reginfo {
	info := regevent.Reginfo{Version: no.ver, State: regevent.Full}
	no.ver++

	for i, aor := range []string{msisdn, telIMPU} {
		info.Registrations = append(info.Registrations, regevent.Registration{
			AOR: aor, ID: "r" + strconv.Itoa(i), State: regState,
			Contacts: []regevent.Contact{{
				ID: "c" + strconv.Itoa(i), State: contactState, Event: event, Expires: &expires, URI: no.contact(),
			}},
		})
	}

	return info
}

func (no *notifier) notify(state string, info regevent.Reginfo) *sip.Response {
	no.n.t.Helper()

	no.send(no.request(state, info))

	res, _ := no.n.ps.RecvResponse()

	return res
}

func (no *notifier) notifyAndRefresh(state string, info regevent.Reginfo) (*sip.Response, *sip.Request, sip.Flow) {
	no.n.t.Helper()

	no.send(no.request(state, info))

	var (
		res *sip.Response
		req *sip.Request
		f   sip.Flow
	)

	for res == nil || req == nil {
		r := no.n.ps.Recv()

		switch m := r.Msg.(type) {
		case *sip.Response:
			res = m
		case *sip.Request:
			via, _ := m.Header.TopVia()
			if no.n.seen[via.Branch()] {
				continue
			}

			no.n.seen[via.Branch()] = true
			req, f = m, r.Flow
		}
	}

	return res, req, f
}

func (no *notifier) request(state string, info regevent.Reginfo) *sip.Request {
	no.n.t.Helper()

	no.cseq++

	body, err := regevent.Encode(info)
	if err != nil {
		no.n.t.Fatal(err)
	}

	req := siptest.NewRequest("NOTIFY", no.contact(), sip.UDP, no.n.pc.Addr())

	via, _ := req.Header.TopVia()
	via.Params.Del("rport")
	_ = req.Header.SetTopVia(via)

	req.Header.Set("From", "<"+msisdn+">;tag="+no.toTag)
	req.Header.Set("To", no.sub.Header.Get("From"))
	req.Header.Set("Call-ID", no.sub.Header.CallID())
	req.Header.Set("CSeq", strconv.Itoa(no.cseq)+" NOTIFY")
	req.Header.Set("Contact", scscfContact)
	req.Header.Add("Event", "reg")
	req.Header.Add("Subscription-State", state)
	req.SetBody(regevent.ContentType, body)

	return req
}

func (no *notifier) send(req *sip.Request) {
	no.n.pc.Send(sip.UDP, netip.AddrPortFrom(no.n.addr, no.portUS), req)
}

func (no *notifier) active() {
	no.n.t.Helper()

	if res := no.notify("active;expires=600000", no.reginfo(regevent.Active, regevent.Active, regevent.Registered, 3600)); res.StatusCode != 200 {
		no.n.t.Fatalf("NOTIFY answered %q", res.StartLine())
	}
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func nextReginfo(t *testing.T, u *UE) *regevent.Reginfo {
	t.Helper()

	timeout := time.After(10 * time.Second)

	for {
		select {
		case e := <-u.Events():
			if e.Reginfo != nil {
				return e.Reginfo
			}
		case <-timeout:
			t.Fatal("no reg event NOTIFY reported")
		}
	}
}

func TestSubscribeAfterRegistration(t *testing.T) {
	n := newRegEventNetwork(t)
	u, _ := n.newUE(Config{})

	no := n.registerSubscribing(u)
	req := no.sub

	set := u.SAs()[0].Set

	switch {
	case req.URI.String() != msisdn:
		t.Fatalf("Request-URI %s, want the default IMPU %s", req.URI, msisdn)
	case req.Header.Get("To") != "<"+msisdn+">":
		t.Fatalf("To = %q", req.Header.Get("To"))
	case !slices.Equal(req.Header.Elements("Route"), []string{
		"<sip:" + n.ps.Addr().String() + ";lr>", "<sip:orig@scscf." + domain + ":6060;lr>",
	}):
		t.Fatalf("Route = %q, want the P-CSCF's protected server port and the Service-Route", req.Header.Values("Route"))
	case req.Header.Get("Event") != "reg" || req.Header.Get("Accept") != regevent.ContentType ||
		req.Header.Get("Expires") != "600000":
		t.Fatalf("SUBSCRIBE:\n%s", req)
	case req.Header.Get("Contact") != "<"+no.contact()+">":
		t.Fatalf("Contact = %q, want %s", req.Header.Get("Contact"), no.contact())
	case no.sf.Remote.Port() != set.Local.PortC:
		t.Fatalf("SUBSCRIBE from %s, want port_uc %d", no.sf.Remote, set.Local.PortC)
	case !slices.Equal(req.Header.Values("Security-Verify"), n.pcscfServer(t)) || req.Header.Get("P-Access-Network-Info") == "":
		t.Fatalf("SUBSCRIBE without the sec-agree headers:\n%s", req)
	}

	if from, _ := req.Header.From(); from.URI.String() != msisdn || from.Tag() == "" {
		t.Fatalf("From = %q", req.Header.Get("From"))
	}

	if s := u.Subscription(); !s.Active || time.Until(s.Expires) < 599000*time.Second || s.ID.RemoteTag != no.toTag {
		t.Fatalf("subscription = %+v", s)
	}

	no.active()

	info := nextReginfo(t, u)
	if len(info.Registrations) != 2 {
		t.Fatalf("reginfo = %+v", info)
	}

	if st := u.State(); !slices.Equal(st.IMPUs, []string{msisdn, telIMPU}) {
		t.Fatalf("IMPUs = %v", st.IMPUs)
	}
}

func TestNotifyBeforeTheSubscribeResponse(t *testing.T) {
	n := newRegEventNetwork(t)
	u, _ := n.newUE(Config{})

	done := start(u.Register)

	req, f := n.recv(n.pcscf)
	n.challenge(req, f, n.pcscf, true)

	req, f = n.recv(n.ps)
	n.ok(req, f, n.pc, 3600)

	sub, sf := n.recv(n.ps)
	no := &notifier{n: n, u: u, sub: sub, sf: sf, toTag: sip.NewTag(), portUS: u.SAs()[0].Set.Local.PortS}

	if res := no.notify("active;expires=500", no.reginfo(regevent.Active, regevent.Active, regevent.Registered, 3600)); res.StatusCode != 200 {
		t.Fatalf("NOTIFY answered %q", res.StartLine())
	}

	if s := u.Subscription(); !s.Active || time.Until(s.Expires) > 500*time.Second {
		t.Fatalf("subscription = %+v, want it from the NOTIFY", s)
	}

	no.accept(sub, sf)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	if s := u.Subscription(); !s.Active || s.ID.RemoteTag != no.toTag {
		t.Fatalf("subscription = %+v", s)
	}
}

func TestNotifyOutsideTheSubscription(t *testing.T) {
	n := newRegEventNetwork(t)
	u, _ := n.newUE(Config{})

	no := n.registerSubscribing(u)

	req := no.request("active;expires=600", no.reginfo(regevent.Active, regevent.Active, regevent.Registered, 3600))
	req.Header.Set("Call-ID", "other@127.0.0.1")
	no.send(req)

	if res, _ := n.ps.RecvResponse(); res.StatusCode != 481 {
		t.Fatalf("NOTIFY of another dialog answered %q, want 481", res.StartLine())
	}

	req = no.request("active;expires=600", no.reginfo(regevent.Active, regevent.Active, regevent.Registered, 3600))
	req.Header.Set("Event", "presence")
	no.send(req)

	if res, _ := n.ps.RecvResponse(); res.StatusCode != 489 {
		t.Fatalf("NOTIFY for presence answered %q, want 489", res.StartLine())
	}
}

func TestResubscribe(t *testing.T) {
	n := newRegEventNetwork(t)
	u, _ := n.newUE(Config{})

	no := n.registerSubscribing(u)
	no.active()

	done := start(u.Resubscribe)

	req, f := n.recv(n.ps)
	if to, _ := req.Header.To(); to.Tag() != no.toTag || req.URI.String() != "sip:scscf."+domain+":6060" ||
		req.Header.Get("Route") != pcscfRR || req.Header.CallID() != no.sub.Header.CallID() {
		t.Fatalf("refresh:\n%s", req)
	}

	if cseq, _ := req.Header.CSeq(); cseq.Seq != 2 {
		t.Fatalf("CSeq = %+v, want 2", cseq)
	}

	no.accept(req, f)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	done = start(u.Resubscribe)

	req, f = n.recv(n.ps)
	n.reply(n.pc, f, sip.NewResponse(req, 481, ""))

	req, f = n.recv(n.ps)
	if to, _ := req.Header.To(); to.Tag() != "" || req.Header.CallID() == no.sub.Header.CallID() || req.URI.String() != msisdn {
		t.Fatalf("SUBSCRIBE after the 481:\n%s", req)
	}

	no.sub, no.toTag = req, sip.NewTag()
	no.accept(req, f)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	if s := u.Subscription(); !s.Active || s.ID.CallID != req.Header.CallID() {
		t.Fatalf("subscription = %+v, want the new one", s)
	}

	done = start(u.Resubscribe)

	req, f = n.recv(n.ps)
	n.reply(n.pc, f, sip.NewResponse(req, 500, ""))

	var re *ResponseError
	if err := wait(t, done); !errors.As(err, &re) || re.Response.StatusCode != 500 {
		t.Fatalf("Resubscribe = %v, want the 500", err)
	}

	if s := u.Subscription(); !s.Active {
		t.Fatal("subscription dropped after a non-481 failure")
	}
}

func TestShortened(t *testing.T) {
	n := newRegEventNetwork(t)
	u, _ := n.newUE(Config{})

	no := n.registerSubscribing(u)
	no.active()
	nextReginfo(t, u)

	if res := no.notify("active;expires=600000", no.reginfo(regevent.Active, regevent.Active, regevent.Shortened, 2)); res.StatusCode != 200 {
		t.Fatalf("NOTIFY answered %q", res.StartLine())
	}

	if st := u.State(); time.Until(st.Expires) > 2*time.Second {
		t.Fatalf("expires %s, want the shortened 2 s", st.Expires)
	}

	req, f := n.recv(n.ps)
	if req.Method != "REGISTER" || req.Header.Get("Expires") != "600000" {
		t.Fatalf("got\n%s\nwant a re-REGISTER", req)
	}

	n.challenge(req, f, n.pc, true)

	req, f = n.recv(n.ps)
	n.ok(req, f, n.pc, 3600)

	eventually(t, "the re-authentication", func() bool { return time.Until(u.State().Expires) > time.Hour-time.Minute })
}

func TestDeactivated(t *testing.T) {
	n := newRegEventNetwork(t)
	u, kernel := n.newUE(Config{T1: 10 * time.Millisecond})

	no := n.registerSubscribing(u)
	no.active()

	old := u.SAs()[0].Set

	res := no.notify("terminated;reason=deactivated", no.reginfo(regevent.Terminated, regevent.Terminated, regevent.Deactivated, 0))
	if res.StatusCode != 200 {
		t.Fatalf("NOTIFY answered %q", res.StartLine())
	}

	if u.State().Registered || u.Subscription().Active {
		t.Fatalf("state %+v and subscription %+v after deactivation", u.State(), u.Subscription())
	}

	req, f := n.recv(n.pcscf)

	if !slices.Contains(kernel.Removed(), old) {
		t.Fatalf("removed %v, want the SAs deleted before the new registration", kernel.Removed())
	}

	if req.Method != "REGISTER" || req.Header.Has("Security-Verify") {
		t.Fatalf("got\n%s\nwant an initial REGISTER", req)
	}

	n.challenge(req, f, n.pcscf, true)

	req, f = n.recv(n.ps)
	n.ok(req, f, n.pc, 3600)

	sub, sf := n.recv(n.ps)
	if sub.Method != "SUBSCRIBE" || sub.Header.CallID() == no.sub.Header.CallID() {
		t.Fatalf("got\n%s\nwant a new SUBSCRIBE", sub)
	}

	no.sub, no.toTag = sub, sip.NewTag()
	no.accept(sub, sf)

	eventually(t, "the new registration", func() bool { return u.State().Registered && u.Subscription().Active })
}

func TestRejected(t *testing.T) {
	n := newRegEventNetwork(t)
	u, kernel := n.newUE(Config{T1: 10 * time.Millisecond})

	no := n.registerSubscribing(u)
	no.active()

	old := u.SAs()[0].Set

	res := no.notify("terminated;reason=rejected", no.reginfo(regevent.Terminated, regevent.Terminated, regevent.Rejected, 0))
	if res.StatusCode != 200 {
		t.Fatalf("NOTIFY answered %q", res.StartLine())
	}

	if len(kernel.Removed()) != 0 {
		t.Fatal("SAs deleted before the NOTIFY transaction ended")
	}

	eventually(t, "the SAs to be deleted", func() bool { return slices.Contains(kernel.Removed(), old) && len(u.SAs()) == 0 })

	if u.State().Registered || u.Subscription().Active {
		t.Fatalf("state %+v and subscription %+v after rejection", u.State(), u.Subscription())
	}

	n.pcscf.RecvNone(200 * time.Millisecond)
}

func TestOneIMPUTerminated(t *testing.T) {
	n := newRegEventNetwork(t)
	u, kernel := n.newUE(Config{T1: 10 * time.Millisecond})

	no := n.registerSubscribing(u)
	no.active()
	nextReginfo(t, u)

	info := no.reginfo(regevent.Active, regevent.Active, regevent.Registered, 3600)
	info.Registrations[1].State = regevent.Terminated
	info.Registrations[1].Contacts[0].State = regevent.Terminated
	info.Registrations[1].Contacts[0].Event = regevent.Unregistered

	if res := no.notify("active;expires=600000", info); res.StatusCode != 200 {
		t.Fatalf("NOTIFY answered %q", res.StartLine())
	}

	st := u.State()
	if !st.Registered || !slices.Equal(st.IMPUs, []string{msisdn}) || slices.Contains(st.AssociatedURIs, telIMPU) {
		t.Fatalf("state = %+v, want %s forgotten", st, telIMPU)
	}

	time.Sleep(100 * time.Millisecond)

	if len(kernel.Removed()) != 0 || len(u.SAs()) != 1 {
		t.Fatalf("SAs removed %v with an IMPU still registered", kernel.Removed())
	}
}

func TestDeregistrationEndsTheSubscription(t *testing.T) {
	n := newRegEventNetwork(t)
	u, _ := n.newUE(Config{})

	no := n.registerSubscribing(u)
	no.active()

	done := start(u.Deregister)

	req, f := n.recv(n.ps)
	n.reply(n.pc, f, sip.NewResponse(req, 200, ""))

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	if u.Subscription().Active {
		t.Fatal("subscription active after deregistration")
	}
}

func TestSubscriptionTerminatedByTimeout(t *testing.T) {
	n := newRegEventNetwork(t)
	u, _ := n.newUE(Config{})

	no := n.registerSubscribing(u)
	no.active()

	res, req, _ := no.notifyAndRefresh("terminated;reason=timeout",
		no.reginfo(regevent.Active, regevent.Active, regevent.Refreshed, 3600))
	if res.StatusCode != 200 {
		t.Fatalf("NOTIFY answered %q", res.StartLine())
	}

	if to, _ := req.Header.To(); req.Method != "SUBSCRIBE" || to.Tag() != "" {
		t.Fatalf("got\n%s\nwant a new SUBSCRIBE", req)
	}

	if !u.State().Registered {
		t.Fatal("registration forgotten with the subscription")
	}
}

func TestNoNotifyWithinTimerN(t *testing.T) {
	n := newRegEventNetwork(t)
	u, _ := n.newUE(Config{T1: 10 * time.Millisecond})

	n.registerSubscribing(u)

	if !u.Subscription().Active {
		t.Fatal("no subscription after the 200")
	}

	eventually(t, "Timer N", func() bool { return !u.Subscription().Active })

	for {
		e := <-u.Events()
		if errors.Is(e.Err, ErrNoNotify) {
			break
		}
	}

	if !u.State().Registered {
		t.Fatal("registration forgotten with the subscription")
	}
}

func TestReginfoVersions(t *testing.T) {
	n := newRegEventNetwork(t)
	u, _ := n.newUE(Config{})

	no := n.registerSubscribing(u)
	no.active()
	nextReginfo(t, u)

	no.ver = 0

	stale := no.reginfo(regevent.Active, regevent.Active, regevent.Shortened, 2)
	if res := no.notify("active;expires=600000", stale); res.StatusCode != 200 {
		t.Fatalf("stale NOTIFY answered %q", res.StartLine())
	}

	if time.Until(u.State().Expires) < time.Hour-time.Minute {
		t.Fatalf("expires %s: the stale document was applied", u.State().Expires)
	}

	no.ver = 5

	res, req, f := no.notifyAndRefresh("active;expires=600000",
		no.reginfo(regevent.Active, regevent.Active, regevent.Refreshed, 3600))
	if res.StatusCode != 200 {
		t.Fatalf("NOTIFY answered %q", res.StartLine())
	}

	if to, _ := req.Header.To(); req.Method != "SUBSCRIBE" || to.Tag() != no.toTag {
		t.Fatalf("got\n%s\nwant a refresh for the full state after the version gap", req)
	}

	no.accept(req, f)
}

func TestFullStateReplacesIMPUs(t *testing.T) {
	n := newRegEventNetwork(t)
	u, _ := n.newUE(Config{})

	no := n.registerSubscribing(u)
	no.active()
	nextReginfo(t, u)

	info := no.reginfo(regevent.Active, regevent.Active, regevent.Refreshed, 3600)
	info.Registrations = info.Registrations[:1]

	if res := no.notify("active;expires=600000", info); res.StatusCode != 200 {
		t.Fatalf("NOTIFY answered %q", res.StartLine())
	}

	if st := u.State(); !slices.Equal(st.IMPUs, []string{msisdn}) {
		t.Fatalf("IMPUs = %v after a full document listing only %s", st.IMPUs, msisdn)
	}

	info = no.reginfo(regevent.Active, regevent.Active, regevent.Refreshed, 3600)
	info.State = regevent.Partial
	info.Registrations = info.Registrations[1:]

	if res := no.notify("active;expires=600000", info); res.StatusCode != 200 {
		t.Fatalf("NOTIFY answered %q", res.StartLine())
	}

	if st := u.State(); !slices.Equal(st.IMPUs, []string{msisdn, telIMPU}) {
		t.Fatalf("IMPUs = %v after a partial document adding %s", st.IMPUs, telIMPU)
	}
}

func TestRefreshIn(t *testing.T) {
	for _, tc := range []struct {
		remaining, duration, want time.Duration
	}{
		{600000 * time.Second, 600000 * time.Second, 599400 * time.Second},
		{1000 * time.Second, 600000 * time.Second, 400 * time.Second},
		{1200 * time.Second, 1200 * time.Second, 600 * time.Second},
		{100 * time.Second, 1200 * time.Second, 0},
	} {
		got := refreshIn(time.Now().Add(tc.remaining), tc.duration)
		if d := got - tc.want; d > time.Second || d < -time.Second {
			t.Errorf("refreshIn(%s, %s) = %s, want %s", tc.remaining, tc.duration, got, tc.want)
		}
	}
}
