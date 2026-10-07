//go:build linux && (amd64 || arm64)

package integration

import (
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/pcscf"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/testue"
)

const fastT1 = 50 * time.Millisecond

type notification struct {
	req   *sip.Request
	state string
	info  *regevent.Reginfo
}

func nextNotify(t *testing.T, u *testue.UE) notification {
	t.Helper()

	timeout := time.After(15 * time.Second)

	for {
		select {
		case e := <-u.Events():
			switch {
			case e.Reginfo != nil:
				return notification{req: e.Request, state: e.Request.Header.Get("Subscription-State"), info: e.Reginfo}
			case e.Err != nil:
				t.Logf("UE event: %v", e.Err)
			}
		case <-timeout:
			t.Fatal("no reg event NOTIFY reached the UE")
		}
	}
}

func noNotify(t *testing.T, u *testue.UE, d time.Duration) {
	t.Helper()

	timeout := time.After(d)

	for {
		select {
		case e := <-u.Events():
			if e.Reginfo != nil {
				t.Fatalf("unexpected NOTIFY:\n%s", e.Request)
			}
		case <-timeout:
			return
		}
	}
}

func (n notification) contact(t *testing.T, aor string, ue netip.Addr) (string, regevent.Contact) {
	t.Helper()

	for _, r := range n.info.Registrations {
		if r.AOR != aor {
			continue
		}

		for _, c := range r.Contacts {
			if u, err := sip.ParseURI(c.URI); err == nil {
				if a, ok := u.Addr(); ok && a.Unmap() == ue {
					return r.State, c
				}
			}
		}
	}

	t.Fatalf("no contact of %s for %s in %+v", ue, aor, n.info)

	return "", regevent.Contact{}
}

func (n notification) want(t *testing.T, state, regState, contactState string, event regevent.Event) {
	t.Helper()

	if !strings.HasPrefix(n.state, state) {
		t.Fatalf("Subscription-State %q, want %s", n.state, state)
	}

	rs, c := n.contact(t, msisdn, ueAddrs[0].Addr())
	if rs != regState || c.State != contactState || c.Event != event {
		t.Fatalf("registration %s, contact %s/%s; want %s, %s/%s", rs, c.State, c.Event, regState, contactState, event)
	}
}

func (s *scene) subscriptions() []db.RegSubscription {
	s.t.Helper()

	d, err := db.Open(s.t.Context(), s.db)
	if err != nil {
		s.t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	subs, err := d.ListRegSubscriptions(s.t.Context(), impi)
	if err != nil {
		s.t.Fatal(err)
	}

	return subs
}

func (s *scene) subscribers() map[db.Subscriber]bool {
	out := map[db.Subscriber]bool{}
	for _, sub := range s.subscriptions() {
		out[sub.Subscriber] = true
	}

	return out
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)

	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

func (s *scene) subscribed(u *testue.UE) {
	s.t.Helper()

	s.register(u)

	if !u.Subscription().Active {
		s.t.Fatal("no reg event subscription after the registration")
	}

	n := nextNotify(s.t, u)
	n.want(s.t, "active", regevent.Active, regevent.Active, regevent.Created)

	eventually(s.t, "the UE's and the P-CSCF's subscriptions", func() bool {
		subs := s.subscribers()
		return subs[db.SubscriberUE] && subs[db.SubscriberPCSCF]
	})
}

func (s *scene) gone(set ipsec.Set) func() bool {
	return func() bool {
		ue := espPackets(s.t, s.ue)
		for _, spi := range spis(set) {
			if _, ok := ue[spi]; ok {
				return false
			}
		}

		return true
	}
}

func (s *scene) pcscfSAsShortened() {
	s.t.Helper()

	eventually(s.t, "the P-CSCF's SAs to be shortened", func() bool {
		sas := s.pcscfSAs()

		scheduled := true
		for _, sa := range sas {
			scheduled = scheduled && !sa.ExpiresAt.After(time.Now().Add(pcscf.DefaultGrace))
		}

		return scheduled
	})
}

func TestRegEventSubscription(t *testing.T) {
	for _, tc := range []struct {
		name string
		tr   sip.Transport
		v6   bool
	}{
		{"UDP/IPv4", sip.UDP, false},
		{"TCP/IPv6", sip.TCP, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScene(t)
			u := s.newUE(tc.v6, testue.Config{Transport: tc.tr})

			s.register(u)

			n := nextNotify(t, u)

			family := 0
			if tc.v6 {
				family = 1
			}

			if rs, c := n.contact(t, msisdn, ueAddrs[family].Addr()); !strings.HasPrefix(n.state, "active") ||
				rs != regevent.Active || c.State != regevent.Active || c.Event != regevent.Created {
				t.Fatalf("NOTIFY %q: %s %s/%s, want active created", n.state, rs, c.State, c.Event)
			}

			sa := s.established(u)
			if f := n.req.Flow; f.Local.Port() != sa.Set.Local.PortS || f.Remote.Port() != sa.Set.Remote.PortC {
				t.Fatalf("NOTIFY on %s, want it from port_pc %d to port_us %d", f, sa.Set.Remote.PortC, sa.Set.Local.PortS)
			}

			if st := u.State(); len(st.IMPUs) != 2 {
				t.Fatalf("IMPUs = %v, want the two unbarred identities", st.IMPUs)
			}

			eventually(t, "the UE's subscription at the S-CSCF", func() bool { return s.subscribers()[db.SubscriberUE] })

			if sub := u.Subscription(); !sub.Active || time.Until(sub.Expires) < 500000*time.Second {
				t.Fatalf("subscription = %+v", sub)
			}

			if err := u.Resubscribe(s.ctx()); err != nil {
				t.Fatal(err)
			}

			n = nextNotify(t, u)
			if !strings.HasPrefix(n.state, "active") {
				t.Fatalf("NOTIFY after the refresh %q", n.state)
			}
		})
	}
}

func TestRegEventRefreshed(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{})

	s.subscribed(u)

	if err := u.Reregister(s.ctx()); err != nil {
		t.Fatal(err)
	}

	nextNotify(t, u).want(t, "active", regevent.Active, regevent.Active, regevent.Refreshed)
}

func TestRegEventExpired(t *testing.T) {
	s := newSceneWith(t, func(srv *server.Server) { srv.MinExpires = time.Second })
	u := s.newUE(false, testue.Config{Expires: 2 * time.Second, T1: fastT1})
	u.SetAutoReregister(false)

	s.subscribed(u)

	sa := s.established(u)

	nextNotify(t, u).want(t, "terminated;reason=noresource", regevent.Terminated, regevent.Terminated, regevent.Expired)

	eventually(t, "the UE's SAs to be deleted", s.gone(sa.Set))

	if u.State().Registered || u.Subscription().Active || len(u.SAs()) != 0 {
		t.Fatalf("state %+v, subscription %+v, SAs %+v after the expiry", u.State(), u.Subscription(), u.SAs())
	}

	eventually(t, "the subscriptions to end", func() bool { return len(s.subscriptions()) == 0 })
	s.pcscfSAsShortened()

	eventually(t, "the HSS to see the timeout deregistration", func() bool {
		sub, _ := s.hss.Subscriber(impi)
		return sub.State == hsstest.NotRegistered && sub.ServerName == ""
	})
}

func TestRegEventDeactivated(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{T1: fastT1})

	s.subscribed(u)

	old := s.established(u)

	if _, err := s.hss.RTR(s.ctx(), cx.DeregistrationReason{Code: cx.ReasonServerChange}, impi); err != nil {
		t.Fatal(err)
	}

	n := nextNotify(t, u)
	n.want(t, "terminated;reason=deactivated", regevent.Terminated, regevent.Terminated, regevent.Deactivated)

	if f := n.req.Flow; f.Local.Port() != old.Set.Local.PortS || f.Remote.Port() != old.Set.Remote.PortC {
		t.Fatalf("NOTIFY on %s, want it over the SAs, before they go", f)
	}

	eventually(t, "the old SAs to be deleted", s.gone(old.Set))

	eventually(t, "the new registration", func() bool {
		return u.State().Registered && u.Subscription().Active
	})

	if sas := u.SAs(); len(sas) != 1 || sas[0].Set == old.Set {
		t.Fatalf("SAs = %+v, want a new established set", sas)
	}

	nextNotify(t, u).want(t, "active", regevent.Active, regevent.Active, regevent.Created)

	if sub, _ := s.hss.Subscriber(impi); sub.State != hsstest.Registered {
		t.Fatalf("HSS subscriber = %+v, want registered again", sub)
	}
}

func TestRegEventRejected(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{T1: fastT1})

	s.subscribed(u)

	sa := s.established(u)

	if _, err := s.hss.RTR(s.ctx(), cx.DeregistrationReason{Code: cx.ReasonPermanentTermination}, impi); err != nil {
		t.Fatal(err)
	}

	nextNotify(t, u).want(t, "terminated;reason=rejected", regevent.Terminated, regevent.Terminated, regevent.Rejected)

	eventually(t, "the UE's SAs to be deleted", s.gone(sa.Set))

	if u.State().Registered || u.Subscription().Active {
		t.Fatalf("state %+v and subscription %+v after the rejection", u.State(), u.Subscription())
	}

	s.hss.Drain()

	time.Sleep(time.Second)

	select {
	case r := <-s.hss.Requests():
		t.Fatalf("%s after the rejection, want no new registration", r)
	default:
	}

	eventually(t, "the subscriptions to end", func() bool { return len(s.subscriptions()) == 0 })
	s.pcscfSAsShortened()
}

func TestRegEventUEDeregistration(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{})

	s.subscribed(u)

	if err := u.Deregister(s.ctx()); err != nil {
		t.Fatal(err)
	}

	if u.Subscription().Active {
		t.Fatal("subscription active after deregistration")
	}

	eventually(t, "the P-CSCF's subscription to end with its NOTIFY", func() bool { return len(s.subscriptions()) == 0 })

	noNotify(t, u, 200*time.Millisecond)
}

func TestRegEventShortened(t *testing.T) {
	s := newSceneWith(t, func(srv *server.Server) { srv.ReauthExpires = 2 * time.Second })
	u := s.newUE(false, testue.Config{})

	s.subscribed(u)

	old := s.established(u)
	s.hss.Drain()

	if code := s.reauthenticate(); code != http.StatusAccepted {
		t.Fatalf("re-authentication request answered %d", code)
	}

	n := nextNotify(t, u)
	n.want(t, "active", regevent.Active, regevent.Active, regevent.Shortened)

	if _, c := n.contact(t, msisdn, ueAddrs[0].Addr()); c.Expires == nil || *c.Expires > 2 {
		t.Fatalf("contact expires %v, want 2 s at most", c.Expires)
	}

	nextNotify(t, u).want(t, "active", regevent.Active, regevent.Active, regevent.Refreshed)

	eventually(t, "the re-authentication", func() bool { return time.Until(u.State().Expires) > time.Hour-time.Minute })

	var mar bool

	for !mar {
		mar = s.hss.Next(t).MAR != nil
	}

	sas := u.SAs()
	if len(sas) != 2 || sas[0].Set != old.Set || sas[0].State != testue.Old || sas[1].State != testue.Established {
		t.Fatalf("SAs = %+v after the re-authentication, want the old set and a new one", sas)
	}

	if err := u.Reregister(s.ctx()); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the old SAs to be deleted", s.gone(old.Set))

	if sa := s.established(u); sa.Set != sas[1].Set {
		t.Fatalf("set %s, want %s", sa.Set, sas[1].Set)
	}
}

func (s *scene) reauthenticate() int {
	s.t.Helper()

	req, err := http.NewRequestWithContext(s.ctx(), http.MethodPost,
		"http://"+s.srv.APIAddr().String()+"/api/v1/registrations/"+impi+"/reauthenticate", nil)
	if err != nil {
		s.t.Fatal(err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}

	_ = res.Body.Close()

	return res.StatusCode
}

func (s *scene) putOperator(body string) {
	s.t.Helper()

	req, err := http.NewRequestWithContext(s.ctx(), http.MethodPut, "http://"+s.srv.APIAddr().String()+"/api/v1/operator",
		strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}

	_ = res.Body.Close()

	if res.StatusCode != http.StatusOK {
		s.t.Fatalf("PUT operator: %d", res.StatusCode)
	}
}

func TestReauthenticateUnregistered(t *testing.T) {
	s := newScene(t)

	if code := s.reauthenticate(); code != http.StatusNotFound {
		t.Fatalf("re-authentication of an unregistered user answered %d, want 404", code)
	}
}
