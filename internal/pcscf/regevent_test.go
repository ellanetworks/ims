package pcscf

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	testIMPU = "sip:+15551230001@" + homeDomain + ";user=phone"
	testTel  = "tel:+15551230001"
)

var (
	ueAddr    = netip.MustParseAddr("127.0.0.2")
	testEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

type fakeClock struct {
	*siptest.Clock
}

func (c fakeClock) Now() time.Time {
	return testEpoch.Add(c.Clock.Now())
}

type regScene struct {
	t     *testing.T
	icscf *siptest.Socket
	scscf *siptest.Socket
	ue    *siptest.Socket
	pcscf netip.AddrPort
	clock fakeClock
	store *db.DB
	p     *PCSCF

	callID string
	cseq   int
}

func newRegScene(t *testing.T) *regScene {
	t.Helper()

	s := &regScene{
		t:      t,
		icscf:  siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
		scscf:  siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
		ue:     siptest.NewSocket(t, netip.AddrPortFrom(ueAddr, 0)),
		clock:  fakeClock{siptest.NewClock()},
		store:  openStore(t),
		callID: sip.NewTag(),
	}

	late := &lateHandler{}
	layer, fallback := siptest.NewLayer(t, transaction.Config{Handler: late, Logger: slog.New(slog.DiscardHandler)})
	s.pcscf = siptest.ListenLayer(t, layer, loopback)

	s.p = New(Config{
		Layer:      layer,
		Proxy:      proxy.New(proxy.Config{Layer: layer, Port: s.pcscf.Port()}),
		Port:       s.pcscf.Port(),
		ICSCFPort:  s.icscf.Addr().Port(),
		HomeDomain: homeDomain,
		SCSCF: SCSCF{
			Name:      sip.URI{Scheme: "sip", Host: "scscf." + homeDomain},
			Listeners: []netip.AddrPort{s.scscf.Addr()},
		},
		Registrations: s.store,
		Fallback:      fallback,
		Clock:         s.clock,
		Logger:        slog.New(slog.DiscardHandler),
	})
	t.Cleanup(s.p.Close)

	if err := s.p.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}

	late.h.Store(s.p)

	return s
}

func (s *regScene) contact() string {
	return "<sip:ue@" + s.ue.Addr().String() + ">"
}

func (s *regScene) register(edit func(*sip.Request)) (*sip.Request, sip.Flow) {
	s.t.Helper()

	s.cseq++

	r := siptest.NewRequest("REGISTER", "sip:"+homeDomain, sip.UDP, s.ue.Addr())
	r.Header.Set("To", "<sip:"+testIMPI+">")
	r.Header.Set("From", "<sip:"+testIMPI+">;tag="+sip.NewTag())
	r.Header.Set("Call-ID", s.callID)
	r.Header.Set("CSeq", strconv.Itoa(s.cseq)+" REGISTER")
	r.Header.Set("Contact", s.contact())
	r.Header.Set("Expires", "600")

	if edit != nil {
		edit(r)
	}

	s.ue.Send(sip.UDP, s.pcscf, r)

	return s.icscf.RecvRequest()
}

func (s *regScene) registered(expires int) (sip.URI, *sip.Request, sip.Flow) {
	s.t.Helper()

	req, f := s.register(nil)
	path := onlyPath(s.t, req)

	answerRegister(s.icscf, s.scscf.Addr(), req, f, expires)
	wantStatus(s.t, first(s.ue.RecvResponse()), 200)

	sub, sf := s.icscf.RecvRequest()
	if sub.Method != "SUBSCRIBE" {
		s.t.Fatalf("I-CSCF got %s, want the P-CSCF's SUBSCRIBE", sub.Method)
	}

	return path, sub, sf
}

func onlyPath(t *testing.T, req *sip.Request) sip.URI {
	t.Helper()

	paths, err := req.Header.Addresses("Path")
	if err != nil || len(paths) != 1 {
		t.Fatalf("Path = %q, %v; want one entry", req.Header.Values("Path"), err)
	}

	return paths[0].URI
}

func answerRegister(icscf *siptest.Socket, scscf netip.AddrPort, req *sip.Request, f sip.Flow, expires int) {
	res := sip.NewResponse(req, 200, "")
	_ = res.Header.SetToTag(sip.NewTag())
	res.Header.Add("Service-Route", "<sip:orig@"+scscf.String()+";lr>")
	res.Header.Add("P-Associated-URI", "<"+testIMPU+">, <"+testTel+">")

	for _, c := range req.Header.Values("Contact") {
		res.Header.Add("Contact", c+";expires="+strconv.Itoa(expires))
	}

	icscf.Send(f.Transport, f.Remote, res)
}

type ownSub struct {
	sub   *sip.Request
	tag   string
	cseq  int
	from  *siptest.Socket
	pcscf netip.AddrPort
}

func answerSubscribe(icscf, scscf *siptest.Socket, sub *sip.Request, f sip.Flow, expires int) *ownSub {
	res := sip.NewResponse(sub, 200, "")
	tag := sip.NewTag()
	_ = res.Header.SetToTag(tag)
	res.Header.Add("Contact", "<sip:"+scscf.Addr().String()+">")
	res.Header.Add("Expires", strconv.Itoa(expires))
	icscf.Send(f.Transport, f.Remote, res)

	return &ownSub{sub: sub, tag: tag, from: scscf, pcscf: f.Remote}
}

func (o *ownSub) notify(t *testing.T, state string, info *regevent.Reginfo) *sip.Response {
	t.Helper()

	o.cseq++

	contacts, err := o.sub.Header.Contacts()
	if err != nil || len(contacts) != 1 {
		t.Fatalf("SUBSCRIBE Contact = %q", o.sub.Header.Get("Contact"))
	}

	to, _ := o.sub.Header.To()

	n := siptest.NewRequest("NOTIFY", contacts[0].URI.String(), sip.UDP, o.from.Addr())
	n.Header.Set("From", "<"+to.URI.String()+">;tag="+o.tag)
	n.Header.Set("To", o.sub.Header.Get("From"))
	n.Header.Set("Call-ID", o.sub.Header.CallID())
	n.Header.Set("CSeq", strconv.Itoa(o.cseq)+" NOTIFY")
	n.Header.Set("Contact", "<sip:"+o.from.Addr().String()+">")
	n.Header.Add("Event", "reg")
	n.Header.Add("Subscription-State", state)

	if info != nil {
		body, err := regevent.Encode(*info)
		if err != nil {
			t.Fatal(err)
		}

		n.SetBody(regevent.ContentType, body)
	}

	o.from.Send(sip.UDP, o.pcscf, n)

	res, _ := o.from.RecvResponse()

	return res
}

func u32(v uint32) *uint32 { return &v }

func reginfo(version uint64, impuState, telState string, contacts map[string]string) *regevent.Reginfo {
	reg := func(aor, id, state string) regevent.Registration {
		r := regevent.Registration{AOR: aor, ID: id, State: state}

		uris := make([]string, 0, len(contacts))
		for uri := range contacts {
			uris = append(uris, uri)
		}

		slices.Sort(uris)

		for i, uri := range uris {
			c := regevent.Contact{ID: id + strconv.Itoa(i), State: contacts[uri], URI: uri, Event: regevent.Registered}
			if contacts[uri] == regevent.Active {
				c.Expires = u32(600)
			} else {
				c.Event = regevent.Unregistered
			}

			r.Contacts = append(r.Contacts, c)
		}

		return r
	}

	return &regevent.Reginfo{Version: version, State: regevent.Full, Registrations: []regevent.Registration{
		reg(testIMPU, "a", impuState), reg(testTel, "b", telState),
	}}
}

func hasParam(u sip.URI, name string) bool {
	return u.Params.Has(name)
}

func TestRegisterPath(t *testing.T) {
	s := newRegScene(t)

	req, f := s.register(nil)
	path := onlyPath(t, req)

	if path.User == "" || path.Host != "127.0.0.1" || path.Port != s.pcscf.Port() || !hasParam(path, "lr") ||
		hasParam(path, "ob") {
		t.Fatalf("Path = %s, want <sip:TOKEN@127.0.0.1:%d;lr>", path, s.pcscf.Port())
	}

	if !slices.Contains(req.Header.Elements("Require"), "path") {
		t.Errorf("Require = %q, want path", req.Header.Values("Require"))
	}

	if v := req.Header.Get("P-Visited-Network-ID"); v != homeDomain {
		t.Errorf("P-Visited-Network-ID = %q, want %q", v, homeDomain)
	}

	answerRegister(s.icscf, s.scscf.Addr(), req, f, 600)
	wantStatus(t, first(s.ue.RecvResponse()), 200)

	sub, sf := s.icscf.RecvRequest()
	answerSubscribe(s.icscf, s.scscf, sub, sf, 600000)

	again, f := s.register(nil)
	if p := onlyPath(t, again); p.User != path.User {
		t.Fatalf("flow token %q on re-registration, want %q", p.User, path.User)
	}

	answerRegister(s.icscf, s.scscf.Addr(), again, f, 600)
	wantStatus(t, first(s.ue.RecvResponse()), 200)
	s.icscf.RecvNone(quiet)

	dereg, f := s.register(func(r *sip.Request) { r.Header.Set("Expires", "0") })
	if p := onlyPath(t, dereg); p.User != path.User {
		t.Fatalf("flow token %q on deregistration, want %q", p.User, path.User)
	}

	answerRegister(s.icscf, s.scscf.Addr(), dereg, f, 0)
	wantStatus(t, first(s.ue.RecvResponse()), 200)

	eventually(t, "the registration to be removed", func() bool {
		_, ok := s.p.regs.get(testIMPI, ueAddr)
		return !ok
	})
}

func TestRegisterPathOutbound(t *testing.T) {
	s := newRegScene(t)

	req, _ := s.register(func(r *sip.Request) {
		r.Header.Set("Contact", s.contact()+`;reg-id=1;+sip.instance="<urn:gsma:imei:35209900-176148-1>"`)
	})

	if path := onlyPath(t, req); !hasParam(path, "ob") || !hasParam(path, "lr") {
		t.Fatalf("Path = %s, want ob with a reg-id Contact", path)
	}
}

func TestOwnSubscribe(t *testing.T) {
	s := newRegScene(t)

	path, sub, f := s.registered(600)

	if sub.URI.String() != testIMPU {
		t.Errorf("Request-URI = %s, want %s", sub.URI, testIMPU)
	}

	if to, _ := sub.Header.To(); to.URI.String() != testIMPU || to.Tag() != "" {
		t.Errorf("To = %q, want <%s>", sub.Header.Get("To"), testIMPU)
	}

	ip := sip.URI{Scheme: "sip", Host: "127.0.0.1", Port: s.pcscf.Port()}

	if from, _ := sub.Header.From(); !from.URI.Equivalent(ip) || from.Tag() == "" {
		t.Errorf("From = %q, want <%s> with a tag", sub.Header.Get("From"), ip)
	}

	if contacts, _ := sub.Header.Contacts(); len(contacts) != 1 || !contacts[0].URI.Equivalent(ip) {
		t.Errorf("Contact = %q, want <%s>", sub.Header.Get("Contact"), ip)
	}

	if pai, err := sub.Header.Addresses("P-Asserted-Identity"); err != nil || len(pai) != 1 || pai[0].URI.String() != path.String() {
		t.Errorf("P-Asserted-Identity = %q, want <%s>", sub.Header.Get("P-Asserted-Identity"), path)
	}

	for name, want := range map[string]string{"Event": "reg", "Accept": regevent.ContentType, "Expires": "600000"} {
		if v := sub.Header.Get(name); v != want {
			t.Errorf("%s = %q, want %q", name, v, want)
		}
	}

	if f.Remote != s.pcscf {
		t.Errorf("SUBSCRIBE from %s, want the P-CSCF port %s", f.Remote, s.pcscf)
	}

	answerSubscribe(s.icscf, s.scscf, sub, f, 600000)
	eventually(t, "the subscription to be stored", func() bool {
		subs, _ := s.store.ListPCSCFSubscriptions(context.Background())
		return len(subs) == 1 && subs[0].IMPI == testIMPI
	})
}

func TestOwnSubscriptionRefresh(t *testing.T) {
	s := newRegScene(t)

	_, sub, f := s.registered(7200)
	answerSubscribe(s.icscf, s.scscf, sub, f, 3000)

	eventually(t, "the refresh to be scheduled", func() bool { return s.clock.Pending() == 1 })

	s.clock.Advance(2399 * time.Second)
	s.scscf.RecvNone(quiet)

	s.clock.Advance(time.Second)

	refresh, rf := s.scscf.RecvRequest()
	if refresh.Method != "SUBSCRIBE" || toTag(refresh) == "" || refresh.Header.CallID() != sub.Header.CallID() {
		t.Fatalf("got %s, want an in-dialog SUBSCRIBE", refresh.StartLine())
	}

	if cseq, _ := refresh.Header.CSeq(); cseq.Seq != 2 {
		t.Errorf("CSeq = %d, want 2", cseq.Seq)
	}

	if refresh.URI.String() != "sip:"+s.scscf.Addr().String() {
		t.Errorf("Request-URI = %s, want the S-CSCF's Contact", refresh.URI)
	}

	if v := refresh.Header.Get("Expires"); v != "600000" {
		t.Errorf("Expires = %q, want 600000", v)
	}

	if !refresh.Header.Has("P-Asserted-Identity") || refresh.Header.Get("Event") != "reg" {
		t.Errorf("refresh without P-Asserted-Identity or Event: %s", refresh)
	}

	res := sip.NewResponse(refresh, 200, "")
	res.Header.Add("Expires", "1000")
	s.scscf.Send(rf.Transport, rf.Remote, res)

	eventually(t, "the next refresh to be scheduled", func() bool { return s.clock.Pending() == 1 })

	s.clock.Advance(499 * time.Second)
	s.scscf.RecvNone(quiet)

	s.clock.Advance(time.Second)

	refresh, rf = s.scscf.RecvRequest()
	if cseq, _ := refresh.Header.CSeq(); refresh.Method != "SUBSCRIBE" || cseq.Seq != 3 {
		t.Fatalf("got %s %v, want the second refresh", refresh.StartLine(), cseq)
	}

	s.scscf.Send(rf.Transport, rf.Remote, sip.NewResponse(refresh, 481, ""))

	initial, _ := s.icscf.RecvRequest()
	if initial.Method != "SUBSCRIBE" || toTag(initial) != "" || initial.Header.CallID() == sub.Header.CallID() ||
		initial.URI.String() != testIMPU {
		t.Fatalf("got %s with To %q, want a new initial SUBSCRIBE", initial.StartLine(), initial.Header.Get("To"))
	}
}

func TestOwnSubscriptionNotRefreshedWithoutRegistration(t *testing.T) {
	s := newRegScene(t)

	_, sub, f := s.registered(1000)
	answerSubscribe(s.icscf, s.scscf, sub, f, 3000)

	eventually(t, "the refresh to be scheduled", func() bool { return s.clock.Pending() == 1 })

	s.clock.Advance(2400 * time.Second)
	s.scscf.RecvNone(quiet)
	s.icscf.RecvNone(0)

	if !s.p.subs.has(testIMPI) {
		t.Fatal("subscription removed before its expiry")
	}

	s.clock.Advance(600 * time.Second)
	s.scscf.RecvNone(quiet)

	if s.p.subs.has(testIMPI) {
		t.Fatal("subscription kept after its expiry")
	}
}

func TestOwnNotifyRemovesContactsAndIdentities(t *testing.T) {
	s := newRegScene(t)

	other := "sip:ue2@" + ueAddr.String() + ":5070"
	mine := "sip:ue@" + s.ue.Addr().String()

	req, f := s.register(func(r *sip.Request) { r.Header.Add("Contact", "<"+other+">") })
	answerRegister(s.icscf, s.scscf.Addr(), req, f, 600)
	wantStatus(t, first(s.ue.RecvResponse()), 200)

	sub, sf := s.icscf.RecvRequest()
	o := answerSubscribe(s.icscf, s.scscf, sub, sf, 600000)

	get := func() db.PCSCFRegistration {
		t.Helper()

		r, ok := s.p.regs.get(testIMPI, ueAddr)
		if !ok {
			t.Fatal("registration removed")
		}

		return r
	}

	wantStatus(t, o.notify(t, "active;expires=600000",
		reginfo(0, regevent.Active, regevent.Active, map[string]string{mine: regevent.Active, other: regevent.Active})), 200)

	if r := get(); len(r.Contacts) != 2 || len(r.AssociatedURIs) != 2 {
		t.Fatalf("registration = %+v, want both contacts and identities", r)
	}

	wantStatus(t, o.notify(t, "active;expires=600000",
		reginfo(1, regevent.Active, regevent.Active, map[string]string{mine: regevent.Active, other: regevent.Terminated})), 200)

	if r := get(); !slices.Equal(r.Contacts, []string{mine}) {
		t.Fatalf("contacts = %q, want %q", r.Contacts, mine)
	}

	impuGone := reginfo(2, regevent.Terminated, regevent.Active, map[string]string{mine: regevent.Active})
	impuGone.Registrations[0].Contacts[0].State = regevent.Terminated
	impuGone.Registrations[0].Contacts[0].Event = regevent.Unregistered
	impuGone.Registrations[0].Contacts[0].Expires = nil
	wantStatus(t, o.notify(t, "active;expires=600000", impuGone), 200)

	if r := get(); !slices.Equal(r.AssociatedURIs, []string{testTel}) || !slices.Equal(r.Contacts, []string{mine}) {
		t.Fatalf("registration = %+v, want the tel URI and the contact left", r)
	}

	if stored, _ := s.store.ListPCSCFRegistrations(context.Background()); len(stored) != 1 ||
		!slices.Equal(stored[0].AssociatedURIs, []string{testTel}) {
		t.Fatalf("stored = %+v, want the tel URI left", stored)
	}

	wantStatus(t, o.notify(t, "active;expires=600000",
		reginfo(3, regevent.Terminated, regevent.Terminated, map[string]string{mine: regevent.Terminated})), 200)

	if _, ok := s.p.regs.get(testIMPI, ueAddr); ok {
		t.Fatal("registration kept without identities")
	}

	if stored, _ := s.store.ListPCSCFRegistrations(context.Background()); len(stored) != 0 {
		t.Fatalf("stored = %+v, want none", stored)
	}
}

func TestOwnNotifyLowerVersionIgnored(t *testing.T) {
	s := newRegScene(t)

	_, sub, f := s.registered(600)
	o := answerSubscribe(s.icscf, s.scscf, sub, f, 600000)

	mine := "sip:ue@" + s.ue.Addr().String()

	wantStatus(t, o.notify(t, "active;expires=600000",
		reginfo(5, regevent.Active, regevent.Active, map[string]string{mine: regevent.Active})), 200)
	wantStatus(t, o.notify(t, "active;expires=600000",
		reginfo(4, regevent.Terminated, regevent.Terminated, map[string]string{mine: regevent.Terminated})), 200)

	if r, ok := s.p.regs.get(testIMPI, ueAddr); !ok || len(r.AssociatedURIs) != 2 {
		t.Fatalf("registration = %+v, %v; want it untouched by the older version", r, ok)
	}
}

func TestOwnNotifyUnknownDialog(t *testing.T) {
	s := newRegScene(t)

	_, sub, f := s.registered(600)
	o := answerSubscribe(s.icscf, s.scscf, sub, f, 600000)

	o.sub = o.sub.Clone()
	o.sub.Header.Set("Call-ID", sip.NewTag())

	wantStatus(t, o.notify(t, "active;expires=600000", nil), 481)
}

func newIPsecRegScene(t *testing.T) (*ipsecScene, *ue) {
	t.Helper()

	s := newIPsecScene(t, ipsec.DefaultPolicy())
	s.ue = siptest.NewSocket(t, netip.AddrPortFrom(ueAddr, 0))

	return s, newUEAt(t, ueAddr, 25656)
}

func (s *ipsecScene) registerOverIPsec(u *ue) (string, *ownSub) {
	s.t.Helper()

	server := s.challenge(u)

	u.uc.Send(sip.UDP, s.ps, u.register(s.t, u.us.Addr(), "c4c4", func(r *sip.Request) {
		r.Header.Add("Security-Verify", server.String())
		r.Header.Set("Contact", ueContact(u))
	}))

	req, f, _ := s.forwarded()
	path := onlyPath(s.t, req)

	answerRegister(s.icscf, s.scscf.Addr(), req, f, 600)
	wantStatus(s.t, first(u.us.RecvResponse()), 200)

	sub, sf := s.icscf.RecvRequest()
	if sub.Method != "SUBSCRIBE" {
		s.t.Fatalf("I-CSCF got %s, want the P-CSCF's SUBSCRIBE", sub.Method)
	}

	return path.User, answerSubscribe(s.icscf, s.scscf, sub, sf, 600000)
}

func ueContact(u *ue) string {
	return "<sip:ue@" + u.us.Addr().String() + ">"
}

func ueSubscribe(t *testing.T, u *ue, to netip.AddrPort, edit func(*sip.Request)) {
	t.Helper()

	r := siptest.NewRequest("SUBSCRIBE", testIMPU, sip.UDP, u.us.Addr())
	r.Header.Set("Contact", ueContact(u))
	r.Header.Set("From", "<"+testIMPU+">;tag="+sip.NewTag())
	r.Header.Add("Event", "reg")
	r.Header.Add("Accept", regevent.ContentType)
	r.Header.Add("Expires", "600000")

	if edit != nil {
		edit(r)
	}

	u.uc.Send(sip.UDP, to, r)
}

func TestUESubscribe(t *testing.T) {
	s, u := newIPsecRegScene(t)
	token, _ := s.registerOverIPsec(u)

	serviceRoute := "<sip:orig@" + s.scscf.Addr().String() + ";lr>"

	for _, tc := range []struct {
		name      string
		preferred string
		asserted  string
	}{
		{"default identity", "", testIMPU},
		{"preferred tel URI", "<" + testTel + ">", testTel},
		{"preferred identity not associated", "<sip:mallory@" + homeDomain + ">", testIMPU},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ueSubscribe(t, u, s.ps, func(r *sip.Request) {
				r.Header.Add("Route", "<sip:"+s.ps.String()+";lr>, <sip:attacker@192.0.2.1;lr>")
				r.Header.Add("P-Asserted-Identity", "<sip:bob@"+homeDomain+">")

				if tc.preferred != "" {
					r.Header.Add("P-Preferred-Identity", tc.preferred)
				}
			})

			sub, f := s.scscf.RecvRequest()

			if routes := sub.Header.Elements("Route"); !slices.Equal(routes, []string{serviceRoute}) {
				t.Errorf("Route = %q, want the Service-Route %s", routes, serviceRoute)
			}

			if pai := sub.Header.Values("P-Asserted-Identity"); !slices.Equal(pai, []string{"<" + tc.asserted + ">"}) {
				t.Errorf("P-Asserted-Identity = %q, want <%s>", pai, tc.asserted)
			}

			if sub.Header.Has("P-Preferred-Identity") {
				t.Error("P-Preferred-Identity forwarded")
			}

			rr, err := sub.Header.RecordRoutes()
			if err != nil || len(rr) != 2 {
				t.Fatalf("Record-Route = %q, %v; want two entries", sub.Header.Values("Record-Route"), err)
			}

			for i, port := range []uint16{s.pcscf.Port(), s.ps.Port()} {
				if u := rr[i].URI; u.User != token || u.Host != "127.0.0.1" || u.Port != port || !hasParam(u, "lr") {
					t.Errorf("Record-Route[%d] = %s, want the flow token on port %d", i, u, port)
				}
			}

			if f.Remote != s.pcscf {
				t.Errorf("SUBSCRIBE from %s, want the P-CSCF port %s", f.Remote, s.pcscf)
			}

			res := sip.NewResponse(sub, 200, "")
			_ = res.Header.SetToTag(sip.NewTag())
			res.Header.Add("Expires", "600000")
			s.scscf.Send(f.Transport, f.Remote, res)
			wantStatus(t, first(u.us.RecvResponse()), 200)
		})
	}
}

type ueDialog struct {
	sub      *sip.Request
	ueTag    string
	scscfTag string
	rr       []string
}

func (s *ipsecScene) subscribeUE(t *testing.T, u *ue) *ueDialog {
	t.Helper()

	ueSubscribe(t, u, s.ps, nil)

	sub, f := s.scscf.RecvRequest()

	d := &ueDialog{sub: sub, scscfTag: sip.NewTag(), rr: sub.Header.Elements("Record-Route")}
	from, _ := sub.Header.From()
	d.ueTag = from.Tag()

	res := sip.NewResponse(sub, 200, "")
	_ = res.Header.SetToTag(d.scscfTag)
	res.Header.Add("Contact", "<sip:"+s.scscf.Addr().String()+">")

	for _, rr := range d.rr {
		res.Header.Add("Record-Route", rr)
	}

	res.Header.Add("Expires", "600000")
	s.scscf.Send(f.Transport, f.Remote, res)

	ok, _ := u.us.RecvResponse()
	wantStatus(t, ok, 200)

	if got := ok.Header.Elements("Record-Route"); !slices.Equal(got, d.rr) {
		t.Fatalf("Record-Route to the UE = %q, want %q", got, d.rr)
	}

	return d
}

func (d *ueDialog) notify(t *testing.T, s *ipsecScene, cseq int, routes []string) {
	t.Helper()

	contacts, _ := d.sub.Header.Contacts()

	n := siptest.NewRequest("NOTIFY", contacts[0].URI.String(), sip.UDP, s.scscf.Addr())
	n.Header.Set("From", "<"+testIMPU+">;tag="+d.scscfTag)
	n.Header.Set("To", "<"+testIMPU+">;tag="+d.ueTag)
	n.Header.Set("Call-ID", d.sub.Header.CallID())
	n.Header.Set("CSeq", strconv.Itoa(cseq)+" NOTIFY")
	n.Header.Set("Contact", "<sip:"+s.scscf.Addr().String()+">")
	n.Header.Add("Route", strings.Join(routes, ", "))
	n.Header.Add("Event", "reg")
	n.Header.Add("Subscription-State", "active;expires=600000")

	body, err := regevent.Encode(*reginfo(0, regevent.Active, regevent.Active,
		map[string]string{"sip:ue@" + contacts[0].URI.HostPort(): regevent.Active}))
	if err != nil {
		t.Fatal(err)
	}

	n.SetBody(regevent.ContentType, body)
	s.scscf.Send(sip.UDP, s.pcscf, n)
}

func TestInDialogThroughThePCSCF(t *testing.T) {
	s, u := newIPsecRegScene(t)
	token, _ := s.registerOverIPsec(u)
	d := s.subscribeUE(t, u)

	sets := s.installed()
	if len(sets) != 1 {
		t.Fatalf("installed = %v, want one set", sets)
	}

	d.notify(t, s, 1, d.rr)

	notify, f := u.us.RecvRequest()
	if notify.Method != "NOTIFY" || notify.Header.Has("Route") {
		t.Fatalf("UE got %s with Route %q, want the NOTIFY with no Route", notify.StartLine(), notify.Header.Values("Route"))
	}

	if want := netip.AddrPortFrom(loopback, sets[0].Local.PortC); f.Remote != want {
		t.Fatalf("NOTIFY from %s, want the protected client port %s", f.Remote, want)
	}

	via, err := notify.Header.TopVia()
	if err != nil || via.Port != s.ps.Port() {
		t.Fatalf("NOTIFY Via %s, %v; want the protected server port %d", via, err, s.ps.Port())
	}

	u.uc.Send(sip.UDP, s.ps, sip.NewResponse(notify, 200, ""))
	wantStatus(t, first(s.scscf.RecvResponse()), 200)

	routes := slices.Clone(d.rr)
	slices.Reverse(routes)

	r := siptest.NewRequest("SUBSCRIBE", "sip:"+s.scscf.Addr().String(), sip.UDP, u.us.Addr())
	r.Header.Set("From", "<"+testIMPU+">;tag="+d.ueTag)
	r.Header.Set("To", "<"+testIMPU+">;tag="+d.scscfTag)
	r.Header.Set("Call-ID", d.sub.Header.CallID())
	r.Header.Set("CSeq", "2 SUBSCRIBE")
	r.Header.Set("Contact", ueContact(u))
	r.Header.Add("Route", strings.Join(routes, ", "))
	r.Header.Add("Event", "reg")
	r.Header.Add("Expires", "600000")
	u.uc.Send(sip.UDP, s.ps, r)

	refresh, rf := s.scscf.RecvRequest()
	if refresh.Method != "SUBSCRIBE" || refresh.Header.Has("Route") || refresh.URI.String() != "sip:"+s.scscf.Addr().String() {
		t.Fatalf("S-CSCF got %s with Route %q, want the re-SUBSCRIBE to its Contact without Route",
			refresh.StartLine(), refresh.Header.Values("Route"))
	}

	s.scscf.Send(rf.Transport, rf.Remote, sip.NewResponse(refresh, 200, ""))
	wantStatus(t, first(u.us.RecvResponse()), 200)

	forged := slices.Clone(routes)
	for i, e := range forged {
		forged[i] = strings.Replace(e, token+"@", "otherflow@", 1)
	}

	r.Header.Set("CSeq", "3 SUBSCRIBE")
	r.Header.Set("Route", strings.Join(forged, ", "))
	_ = r.Header.SetTopVia(sip.NewVia(sip.UDP, u.us.Addr()))
	u.uc.Send(sip.UDP, s.ps, r)
	wantStatus(t, first(u.us.RecvResponse()), 403)
	s.scscf.RecvNone(quiet)

	unknown := make([]string, len(d.rr))
	for i, e := range d.rr {
		unknown[i] = strings.Replace(e, token+"@", "nosuchflow@", 1)
	}

	d.notify(t, s, 2, unknown)
	wantStatus(t, first(s.scscf.RecvResponse()), 480)
	u.us.RecvNone(quiet)
}

func TestOwnNotifyTerminatesEverything(t *testing.T) {
	s, u := newIPsecRegScene(t)
	_, o := s.registerOverIPsec(u)

	mine := "sip:ue@" + u.us.Addr().String()

	info := reginfo(0, regevent.Terminated, regevent.Terminated, map[string]string{mine: regevent.Terminated})
	for i := range info.Registrations {
		info.Registrations[i].Contacts[0].Event = regevent.Deactivated
	}

	wantStatus(t, o.notify(t, "terminated;reason=deactivated", info), 200)

	if _, ok := s.p.regs.get(testIMPI, ueAddr); ok {
		t.Fatal("registration kept")
	}

	if s.p.subs.has(testIMPI) {
		t.Fatal("subscription kept after Subscription-State terminated")
	}

	eventually(t, "the sets to go after the grace", func() bool { return len(s.installed()) == 0 })
}

func TestOwnSubscriptionTerminatedKeepsRegistrations(t *testing.T) {
	s, u := newIPsecRegScene(t)
	_, o := s.registerOverIPsec(u)

	mine := "sip:ue@" + u.us.Addr().String()

	wantStatus(t, o.notify(t, "terminated;reason=timeout",
		reginfo(0, regevent.Active, regevent.Active, map[string]string{mine: regevent.Active})), 200)

	if s.p.subs.has(testIMPI) {
		t.Fatal("subscription kept after Subscription-State terminated")
	}

	time.Sleep(3 * testGrace)

	if _, ok := s.p.regs.get(testIMPI, ueAddr); !ok {
		t.Fatal("registration removed")
	}

	if sets := s.installed(); len(sets) != 1 {
		t.Fatalf("installed = %v, want the set kept", sets)
	}

	if stored := s.stored(); len(stored) != 1 || time.Until(stored[0].ExpiresAt) < 500*time.Second {
		t.Fatalf("stored = %+v, want the set for the registration", stored)
	}
}

func TestRegistrationsAndSubscriptionSurviveRestart(t *testing.T) {
	s, u := newIPsecRegScene(t)
	token, o := s.registerOverIPsec(u)
	d := s.subscribeUE(t, u)

	eventually(t, "the subscription to be stored", func() bool {
		subs, _ := s.store.ListPCSCFSubscriptions(context.Background())
		return len(subs) == 1
	})

	restart(t, s)

	r, ok := s.p.regs.get(testIMPI, ueAddr)
	if !ok || r.FlowToken != token || !slices.Equal(r.AssociatedURIs, []string{testIMPU, testTel}) {
		t.Fatalf("registration after restart = %+v, %v", r, ok)
	}

	if !s.p.subs.has(testIMPI) {
		t.Fatal("subscription lost on restart")
	}

	mine := "sip:ue@" + u.us.Addr().String()

	wantStatus(t, o.notify(t, "active;expires=600000",
		reginfo(0, regevent.Active, regevent.Active, map[string]string{mine: regevent.Active})), 200)

	d.notify(t, s, 1, d.rr)

	notify, f := u.us.RecvRequest()
	if notify.Method != "NOTIFY" {
		t.Fatalf("UE got %s, want the NOTIFY", notify.StartLine())
	}

	u.us.Send(sip.UDP, f.Remote, sip.NewResponse(notify, 200, ""))
	wantStatus(t, first(s.scscf.RecvResponse()), 200)
}

func TestOwnSubscriptionFollowsTheRegistrations(t *testing.T) {
	s := newRegScene(t)

	_, sub, f := s.registered(600)
	o := answerSubscribe(s.icscf, s.scscf, sub, f, 3000)

	req, rf := s.register(nil)
	answerRegister(s.icscf, s.scscf.Addr(), req, rf, 600)
	wantStatus(t, first(s.ue.RecvResponse()), 200)
	s.icscf.RecvNone(quiet)

	req, rf = s.register(func(r *sip.Request) { r.Header.Set("Expires", "0") })
	answerRegister(s.icscf, s.scscf.Addr(), req, rf, 0)
	wantStatus(t, first(s.ue.RecvResponse()), 200)

	if s.p.subs.has(testIMPI) {
		t.Fatal("subscription kept without a registration")
	}

	wantStatus(t, o.notify(t, "active;expires=3000", reginfo(1, regevent.Active, regevent.Active, nil)), 481)

	s.registered(600)
}

func TestNewRegistrationSubscribesAgain(t *testing.T) {
	s := newRegScene(t)

	_, sub, f := s.registered(600)
	answerSubscribe(s.icscf, s.scscf, sub, f, 3000)

	s.p.regs.remove(testIMPI, s.ue.Addr().Addr())

	_, again, _ := s.registered(600)
	if again.Header.CallID() == sub.Header.CallID() {
		t.Fatal("the new registration reused the old subscription's dialog")
	}
}

func TestRefreshFailureAfterExpirySubscribesAgain(t *testing.T) {
	s := newRegScene(t)

	_, sub, f := s.registered(7200)
	answerSubscribe(s.icscf, s.scscf, sub, f, 1000)

	eventually(t, "the refresh to be scheduled", func() bool { return s.clock.Pending() == 1 })
	s.clock.Advance(1000 * time.Second)

	refresh, rf := s.scscf.RecvRequest()
	s.scscf.Send(rf.Transport, rf.Remote, sip.NewResponse(refresh, 500, ""))

	initial, _ := s.icscf.RecvRequest()
	if initial.Method != "SUBSCRIBE" || toTag(initial) != "" || initial.Header.CallID() == sub.Header.CallID() {
		t.Fatalf("got %s, want a new initial SUBSCRIBE once the subscription expired", initial.StartLine())
	}

	s.scscf.RecvNone(quiet)
}

func TestChallengedRegistrationKeepsItsFlowToken(t *testing.T) {
	s := newRegScene(t)

	req, f := s.register(nil)
	path := onlyPath(t, req)

	res := sip.NewResponse(req, 401, "")
	_ = res.Header.SetToTag(sip.NewTag())
	s.icscf.Send(f.Transport, f.Remote, res)
	wantStatus(t, first(s.ue.RecvResponse()), 401)

	again, _ := s.register(nil)
	if p := onlyPath(t, again); p.User != path.User {
		t.Fatalf("flow token %q after the challenge, want %q", p.User, path.User)
	}
}

func answerRegisterWith(s *regScene, req *sip.Request, f sip.Flow, associated ...string) {
	res := sip.NewResponse(req, 200, "")
	_ = res.Header.SetToTag(sip.NewTag())
	res.Header.Add("Service-Route", "<sip:orig@"+s.scscf.Addr().String()+";lr>")

	var uris []string
	for _, a := range associated {
		uris = append(uris, "<"+a+">")
	}

	res.Header.Add("P-Associated-URI", strings.Join(uris, ", "))

	expires := "600"
	if req.Header.Get("Expires") == "0" {
		expires = "0"
	}

	for _, c := range req.Header.Values("Contact") {
		res.Header.Add("Contact", c+";expires="+expires)
	}

	s.icscf.Send(f.Transport, f.Remote, res)
	wantStatus(s.t, first(s.ue.RecvResponse()), 200)
}

func TestDeregisteringOneSetKeepsTheOthers(t *testing.T) {
	s := newRegScene(t)

	const other = "sip:other@" + homeDomain

	req, f := s.register(nil)
	answerRegisterWith(s, req, f, testIMPU, testTel)

	sub, sf := s.icscf.RecvRequest()
	answerSubscribe(s.icscf, s.scscf, sub, sf, 600000)

	setTo := func(r *sip.Request) {
		r.Header.Set("To", "<"+other+">")
		r.Header.Set("Authorization", `Digest username="`+testIMPI+`", realm="`+homeDomain+`", uri="sip:`+homeDomain+
			`", nonce="", response=""`)
	}

	req, f = s.register(setTo)
	answerRegisterWith(s, req, f, other)

	if r, ok := s.p.regs.get(testIMPI, ueAddr); !ok || !slices.Equal(r.AssociatedURIs, []string{testIMPU, testTel, other}) {
		t.Fatalf("registration = %+v, want both sets", r)
	}

	req, f = s.register(func(r *sip.Request) {
		setTo(r)
		r.Header.Set("Expires", "0")
	})
	answerRegisterWith(s, req, f)

	r, ok := s.p.regs.get(testIMPI, ueAddr)
	if !ok || !slices.Equal(r.AssociatedURIs, []string{testIMPU, testTel}) {
		t.Fatalf("registration = %+v, %v; want the first set kept", r, ok)
	}

	if !s.p.subs.has(testIMPI) {
		t.Fatal("subscription dropped while a set is registered")
	}
}

func TestNotifyOvertakingTheRegisterResponse(t *testing.T) {
	s := newRegScene(t)

	_, sub, f := s.registered(600)
	o := answerSubscribe(s.icscf, s.scscf, sub, f, 600000)

	mine := "sip:ue@" + s.ue.Addr().String()
	moved := "sip:ue@" + netip.AddrPortFrom(ueAddr, 6000).String()

	info := reginfo(1, regevent.Active, regevent.Active, map[string]string{mine: regevent.Terminated, moved: regevent.Active})
	info.Registrations = append(info.Registrations, regevent.Registration{
		AOR: "sip:alias@" + homeDomain, ID: "r3", State: regevent.Active,
		Contacts: []regevent.Contact{{ID: "c9", State: regevent.Active, Event: regevent.Created, URI: moved, Expires: u32(600)}},
	})

	wantStatus(t, o.notify(t, "active;expires=600000", info), 200)

	r, ok := s.p.regs.get(testIMPI, ueAddr)
	if !ok {
		t.Fatal("registration removed by a NOTIFY that overtook the 200")
	}

	if !slices.Contains(r.AssociatedURIs, "sip:alias@"+homeDomain) {
		t.Errorf("associated URIs = %v, want the identity the NOTIFY reported active", r.AssociatedURIs)
	}
}

func TestRegistrationsFromOneAddress(t *testing.T) {
	rs := newRegistrations(nil, fakeClock{siptest.NewClock()}, time.Minute, slog.New(slog.DiscardHandler))

	a := netip.AddrPortFrom(ueAddr, 5060)
	b := netip.AddrPortFrom(ueAddr, 5070)

	for i, src := range []netip.AddrPort{a, b} {
		rs.save(db.PCSCFRegistration{
			IMPI: "ue" + strconv.Itoa(i) + "@" + homeDomain, FlowToken: "t" + strconv.Itoa(i), UEAddress: src,
			PCSCFAddress: loopback, ExpiresAt: testEpoch.Add(time.Hour),
		})
	}

	for i, src := range []netip.AddrPort{a, b} {
		if r, ok := rs.fromSource(src); !ok || r.IMPI != "ue"+strconv.Itoa(i)+"@"+homeDomain {
			t.Errorf("fromSource(%s) = %+v, %v", src, r, ok)
		}
	}

	if _, ok := rs.fromSource(netip.AddrPortFrom(ueAddr, 5080)); ok {
		t.Error("fromSource matched another port")
	}
}

func TestRequestsUseTheOldSetUntilTheNewOneIsUsed(t *testing.T) {
	a := newAssociations(IPsec{Kernel: ipsectest.NewKernel()}, slog.New(slog.DiscardHandler))
	t.Cleanup(a.close)

	set := func(portC uint16, state saState, inUse bool) *saSet {
		return &saSet{
			impi: testIMPI, state: state, inUse: inUse, expires: time.Now().Add(time.Hour),
			set: ipsec.Set{
				Local:  ipsec.Endpoint{Addr: loopback, PortC: portC, PortS: 5100},
				Remote: ipsec.Endpoint{Addr: ueAddr, PortC: portC + 1000, PortS: portC + 2000},
			},
		}
	}

	a.mu.Lock()
	a.add(set(5101, old, true))
	a.add(set(5102, established, false))
	a.mu.Unlock()

	if f, ok := a.requestFlow(testIMPI, ueAddr, sip.UDP); !ok || f.Local.Port() != 5101 {
		t.Fatalf("requestFlow = %v, %v; want the old set until the new one is used", f, ok)
	}

	for s := range a.sets {
		if s.state == established {
			a.received(s)
		}
	}

	if f, ok := a.requestFlow(testIMPI, ueAddr, sip.UDP); !ok || f.Local.Port() != 5102 {
		t.Fatalf("requestFlow = %v, %v; want the new set once used", f, ok)
	}
}

func TestDefaultIdentityIsTheFirstAssociatedURI(t *testing.T) {
	for _, tt := range []struct {
		associated []string
		want       string
	}{
		{[]string{testIMPU, testTel}, testIMPU},
		{[]string{testTel, testIMPU}, testTel},
		{nil, ""},
	} {
		if got := defaultIdentity(tt.associated); got != tt.want {
			t.Errorf("defaultIdentity(%q) = %q, want %q", tt.associated, got, tt.want)
		}
	}
}
