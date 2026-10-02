package scscf

import (
	"context"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

type subscriber struct {
	h       *harness
	sock    *siptest.Socket
	inbox   *siptest.Socket
	contact string
	impu    string
	pai     string
	accept  string
	route   string
	callID  string
	fromTag string
	toTag   string
	cseq    int
}

type fakeProxy struct {
	sock  *siptest.Socket
	inbox *siptest.Socket
	uri   string
}

func (h *harness) newPCSCF() *fakeProxy {
	inbox := siptest.NewSocket(h.t, netip.AddrPortFrom(loopback, 0))

	return &fakeProxy{
		sock:  siptest.NewSocket(h.t, netip.AddrPortFrom(loopback, 0)),
		inbox: inbox,
		uri:   "sip:flowtoken@127.0.0.1:" + strconv.Itoa(int(inbox.Addr().Port())) + ";lr",
	}
}

func (p *fakeProxy) path() string {
	return "<" + p.uri + ">"
}

func (h *harness) ueSubscriber(u *ue) *subscriber {
	return &subscriber{
		h:       h,
		sock:    u.sock,
		inbox:   u.inbox,
		contact: u.contact,
		impu:    testMSISDN,
		pai:     "<" + testMSISDN + ">",
		accept:  regevent.ContentType,
		callID:  sip.NewTag() + "@127.0.0.1",
		fromTag: sip.NewTag(),
	}
}

func (h *harness) pcscfSubscriber(p *fakeProxy) *subscriber {
	return &subscriber{
		h:       h,
		sock:    p.sock,
		inbox:   p.inbox,
		contact: "sip:127.0.0.1:" + strconv.Itoa(int(p.inbox.Addr().Port())),
		impu:    testMSISDN,
		pai:     p.path(),
		callID:  sip.NewTag() + "@127.0.0.1",
		fromTag: sip.NewTag(),
	}
}

func (s *subscriber) request(expires string) *sip.Request {
	s.cseq++

	req := siptest.NewRequest("SUBSCRIBE", s.impu, sip.UDP, s.sock.Addr())

	to := "<" + s.impu + ">"
	if s.toTag != "" {
		to += ";tag=" + s.toTag
	}

	req.Header.Set("To", to)
	req.Header.Set("From", s.pai+";tag="+s.fromTag)
	req.Header.Set("Call-ID", s.callID)
	req.Header.Set("CSeq", strconv.Itoa(s.cseq)+" SUBSCRIBE")
	req.Header.Set("Contact", "<"+s.contact+">")
	req.Header.Add("Event", "reg")
	req.Header.Add("P-Asserted-Identity", s.pai)

	if s.accept != "" {
		req.Header.Add("Accept", s.accept)
	}

	if s.route != "" && s.toTag == "" {
		req.Header.Add("Route", s.route)
	}

	if expires != "" {
		req.Header.Add("Expires", expires)
	}

	return req
}

func (s *subscriber) subscribe(expires string) *sip.Response {
	s.h.t.Helper()

	s.sock.Send(sip.UDP, s.h.scscf, s.request(expires))

	for {
		res, _ := s.sock.RecvResponse()
		if res.IsProvisional() {
			continue
		}

		if to, err := res.Header.To(); err == nil && res.IsSuccess() && s.toTag == "" {
			s.toTag = to.Tag()
		}

		return res
	}
}

type notification struct {
	req   *sip.Request
	state string
	cseq  uint32
	info  regevent.Reginfo
}

func (n notification) registration(t *testing.T, aor string) regevent.Registration {
	t.Helper()

	for _, r := range n.info.Registrations {
		if r.AOR == aor {
			return r
		}
	}

	t.Fatalf("no registration of %s in %+v", aor, n.info)

	return regevent.Registration{}
}

func (n notification) aors() []string {
	var out []string

	for _, r := range n.info.Registrations {
		out = append(out, r.AOR)
	}

	return out
}

func (n notification) contact(t *testing.T, aor, uri string) regevent.Contact {
	t.Helper()

	for _, c := range n.registration(t, aor).Contacts {
		if c.URI == uri {
			return c
		}
	}

	t.Fatalf("no contact %s under %s in %+v", uri, aor, n.info)

	return regevent.Contact{}
}

func (s *subscriber) recvNotify() notification {
	s.h.t.Helper()

	return s.answerNotify(200)
}

func (s *subscriber) answerNotify(code int) notification {
	t := s.h.t
	t.Helper()

	req, flow := s.inbox.RecvRequest()

	n := s.checkNotify(req)

	s.inbox.Send(flow.Transport, flow.Remote, sip.NewResponse(req, code, ""))

	return n
}

func (s *subscriber) checkNotify(req *sip.Request) notification {
	t := s.h.t
	t.Helper()

	if req.Method != "NOTIFY" {
		t.Fatalf("got %q, want a NOTIFY", req.StartLine())
	}

	from, _ := req.Header.From()
	to, _ := req.Header.To()

	cseq, err := req.Header.CSeq()
	if err != nil {
		t.Fatalf("CSeq: %v", err)
	}

	switch {
	case req.URI.String() != s.contact:
		t.Fatalf("Request-URI = %s, want %s", req.URI, s.contact)
	case req.Header.CallID() != s.callID || to.Tag() != s.fromTag || from.Tag() != s.toTag:
		t.Fatalf("NOTIFY of another dialog: %s", req)
	case cseq.Method != "NOTIFY":
		t.Fatalf("CSeq = %+v", cseq)
	case req.Header.Get("Event") != "reg":
		t.Fatalf("Event = %q", req.Header.Get("Event"))
	case req.Header.ContentType() != regevent.ContentType:
		t.Fatalf("Content-Type = %q", req.Header.ContentType())
	case req.Header.Get("Contact") != s.h.contactURI():
		t.Fatalf("Contact = %q, want %q", req.Header.Get("Contact"), s.h.contactURI())
	}

	info, err := regevent.Decode(req.Body)
	if err != nil {
		t.Fatalf("decode %q: %v", req.Body, err)
	}

	if info.State != regevent.Full {
		t.Fatalf("reginfo state = %q, want full", info.State)
	}

	return notification{req: req, state: req.Header.Get("Subscription-State"), cseq: cseq.Seq, info: info}
}

func (s *subscriber) noNotify() {
	s.h.t.Helper()

	s.inbox.RecvNone(200 * time.Millisecond)
}

func (h *harness) contactURI() string {
	return "<sip:127.0.0.1:" + strconv.Itoa(int(h.scscf.Port())) + ">"
}

func (h *harness) subscriptions() []db.RegSubscription {
	h.t.Helper()

	subs, err := h.db.ListRegSubscriptions(context.Background(), testIMPI)
	if err != nil {
		h.t.Fatalf("ListRegSubscriptions: %v", err)
	}

	return subs
}

func (h *harness) waitSubscriptions(n int) []db.RegSubscription {
	h.t.Helper()

	deadline := time.Now().Add(siptest.Timeout)

	for {
		subs := h.subscriptions()
		if len(subs) == n {
			return subs
		}

		if time.Now().After(deadline) {
			h.t.Fatalf("subscriptions = %+v, want %d", subs, n)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func (s *subscriber) subscribed() notification {
	s.h.t.Helper()

	wantStatus(s.h.t, s.subscribe("600"), 200)

	n := s.recvNotify()
	if n.info.Version != 0 || n.state != "active;expires=600" {
		s.h.t.Fatalf("first NOTIFY: version %d, Subscription-State %q", n.info.Version, n.state)
	}

	return n
}

func wantState(t *testing.T, n notification, state string, version uint64) {
	t.Helper()

	if n.state != state || n.info.Version != version {
		t.Fatalf("NOTIFY: Subscription-State %q version %d, want %q version %d", n.state, n.info.Version, state, version)
	}
}

func wantContact(t *testing.T, c regevent.Contact, state string, event regevent.Event) {
	t.Helper()

	if c.State != state || c.Event != event {
		t.Fatalf("contact %s: %s/%s, want %s/%s", c.URI, c.State, c.Event, state, event)
	}
}

func TestSubscribe(t *testing.T) {
	for _, tt := range []struct {
		name string
		want db.Subscriber
	}{
		{"ue", db.SubscriberUE},
		{"pcscf", db.SubscriberPCSCF},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()
			p := h.newPCSCF()
			u.path = p.path()

			u.register(registerOptions{})
			h.hss.nextSAR(t)

			s := h.ueSubscriber(u)
			if tt.want == db.SubscriberPCSCF {
				s = h.pcscfSubscriber(p)
			}

			res := s.subscribe("600")
			wantStatus(t, res, 200)

			if got := res.Header.Get("Expires"); got != "600" {
				t.Errorf("Expires = %q, want 600", got)
			}

			if got := res.Header.Get("Contact"); got != h.contactURI() {
				t.Errorf("Contact = %q, want %q", got, h.contactURI())
			}

			if s.toTag == "" {
				t.Fatalf("To = %q, want a tag", res.Header.Get("To"))
			}

			n := s.recvNotify()
			wantState(t, n, "active;expires=600", 0)

			subs := h.subscriptions()
			if len(subs) != 1 || subs[0].Subscriber != tt.want || subs[0].IMPU != testMSISDN || subs[0].IMPI != testIMPI ||
				!subs[0].ExpiresAt.Equal(testEpoch.Add(600*time.Second)) {
				t.Fatalf("subscriptions = %+v", subs)
			}
		})
	}
}

func TestSubscribeRejected(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()
	u.path = p.path()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	for _, tt := range []struct {
		name   string
		change func(s *subscriber)
		code   int
	}{
		{"unregistered IMPU of the user", func(s *subscriber) { s.impu = secondIMPU }, 480},
		{"unknown IMPU", func(s *subscriber) { s.impu = unknownIMPU }, 480},
		{"stranger", func(s *subscriber) { s.pai = "<sip:mallory@" + homeDomain + ">" }, 403},
		{"barred IMPU", func(s *subscriber) { s.pai = "<" + testIMPU + ">" }, 403},
		{"Path without the flow token", func(s *subscriber) {
			s.pai = "<sip:127.0.0.1:" + strconv.Itoa(int(p.inbox.Addr().Port())) + ";lr>"
		}, 403},
		{"no P-Asserted-Identity", func(s *subscriber) { s.pai = "" }, 403},
		{"Accept without reginfo", func(s *subscriber) { s.accept = "application/sdp" }, 406},
	} {
		s := h.ueSubscriber(u)
		tt.change(s)

		req := s.request("600")
		if s.pai == "" {
			req.Header.Del("P-Asserted-Identity")
			req.Header.Set("From", "<sip:anonymous@anonymous.invalid>;tag="+s.fromTag)
		}

		s.sock.Send(sip.UDP, h.scscf, req)

		res, _ := s.sock.RecvResponse()
		for res.IsProvisional() {
			res, _ = s.sock.RecvResponse()
		}

		if res.StatusCode != tt.code {
			t.Errorf("%s: got %q, want %d", tt.name, res.StartLine(), tt.code)
		}

		if tt.code == 406 && res.Header.Get("Accept") != regevent.ContentType {
			t.Errorf("%s: Accept = %q", tt.name, res.Header.Get("Accept"))
		}
	}

	u.inbox.RecvNone(200 * time.Millisecond)

	if subs := h.subscriptions(); len(subs) != 0 {
		t.Fatalf("subscriptions = %+v, want none", subs)
	}
}

func TestSubscriptionExpires(t *testing.T) {
	for _, tt := range []struct {
		name    string
		expires string
		want    string
	}{
		{"missing", "", "3761"},
		{"capped", "2000000", "1000000"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()

			u.register(registerOptions{})
			h.hss.nextSAR(t)

			s := h.ueSubscriber(u)

			res := s.subscribe(tt.expires)
			wantStatus(t, res, 200)

			if got := res.Header.Get("Expires"); got != tt.want {
				t.Errorf("Expires = %q, want %s", got, tt.want)
			}

			wantState(t, s.recvNotify(), "active;expires="+tt.want, 0)
		})
	}

	t.Run("fetch", func(t *testing.T) {
		h := newHarness(t)
		u := h.newUE()

		u.register(registerOptions{})
		h.hss.nextSAR(t)

		s := h.ueSubscriber(u)

		res := s.subscribe("0")
		wantStatus(t, res, 200)

		if got := res.Header.Get("Expires"); got != "0" {
			t.Errorf("Expires = %q, want 0", got)
		}

		n := s.recvNotify()
		wantState(t, n, "terminated;reason=timeout", 0)

		if r := n.registration(t, testMSISDN); r.State != regevent.Active {
			t.Errorf("registration = %+v", r)
		}

		if subs := h.subscriptions(); len(subs) != 0 {
			t.Fatalf("subscriptions = %+v, want none", subs)
		}
	})
}

func TestSubscribeReplacesDuplicate(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	old := h.ueSubscriber(u)
	old.subscribed()

	s := h.ueSubscriber(u)
	s.subscribed()

	if subs := h.subscriptions(); len(subs) != 1 || subs[0].CallID != s.callID {
		t.Fatalf("subscriptions = %+v, want only %s", subs, s.callID)
	}

	wantStatus(t, old.subscribe("600"), 481)
	old.noNotify()
}

func TestRefreshSubscription(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	s := h.ueSubscriber(u)
	first := s.subscribed()

	h.clock.Advance(100 * time.Second)

	res := s.subscribe("1200")
	wantStatus(t, res, 200)

	if got := res.Header.Get("Expires"); got != "1200" {
		t.Errorf("Expires = %q, want 1200", got)
	}

	n := s.recvNotify()
	wantState(t, n, "active;expires=1200", 1)

	if n.cseq != first.cseq+1 {
		t.Errorf("CSeq = %d, want %d", n.cseq, first.cseq+1)
	}

	if subs := h.subscriptions(); len(subs) != 1 || !subs[0].ExpiresAt.Equal(h.clock.Now().Add(1200*time.Second)) {
		t.Fatalf("subscriptions = %+v", subs)
	}

	cseq := s.cseq
	s.cseq = 0

	wantStatus(t, s.subscribe("600"), 500)
	s.noNotify()

	s.cseq = cseq

	res = s.subscribe("0")
	wantStatus(t, res, 200)

	if got := res.Header.Get("Expires"); got != "0" {
		t.Errorf("Expires = %q, want 0", got)
	}

	n = s.recvNotify()
	wantState(t, n, "terminated;reason=timeout", 2)

	if n.cseq != first.cseq+2 {
		t.Errorf("CSeq = %d, want %d", n.cseq, first.cseq+2)
	}

	h.waitSubscriptions(0)
	wantStatus(t, s.subscribe("600"), 481)
}

func TestRefreshAfterDeregistration(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	u.impu = secondIMPU
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
	h.hss.nextSAR(t)

	s := h.ueSubscriber(u)
	s.impu = secondIMPU
	s.subscribed()

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "0"}), 200)
	h.hss.wantSAR(t, cx.AssignmentUserDeregistration)

	n := s.recvNotify()
	wantState(t, n, "active;expires=600", 1)

	if r := n.registration(t, secondIMPU); r.State != regevent.Terminated || len(r.Contacts) != 1 {
		t.Fatalf("registration = %+v", r)
	}

	wantContact(t, n.contact(t, secondIMPU, u.contact), regevent.Terminated, regevent.Unregistered)

	wantStatus(t, s.subscribe("600"), 200)

	n = s.recvNotify()
	wantState(t, n, "active;expires=600", 2)

	if got, want := n.aors(), []string{testMSISDN, testTel, testAlias}; !reflect.DeepEqual(got, want) {
		t.Fatalf("registrations = %v, want %v", got, want)
	}
}

func TestNotifyBody(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	u.impu = testMSISDN

	contact := "<" + u.contact + ">;q=0.5;+sip.instance=" + testInstance + ";+g.3gpp.smsip;expires=600"

	u.register(registerOptions{contact: contact})
	h.hss.nextSAR(t)

	s := h.ueSubscriber(u)
	first := s.subscribed()

	if got, want := first.aors(), []string{testMSISDN, testTel, testAlias}; !reflect.DeepEqual(got, want) {
		t.Fatalf("registrations = %v, want %v without the barred %s", got, want, testIMPU)
	}

	wantParams := []regevent.UnknownParam{{Name: "+sip.instance", Value: testInstance}, {Name: "+g.3gpp.smsip"}}

	checkIDs(t, first)

	for _, aor := range first.aors() {
		r := first.registration(t, aor)
		if r.State != regevent.Active || len(r.Contacts) != 1 {
			t.Fatalf("registration = %+v", r)
		}

		event := regevent.Created
		if aor == testMSISDN {
			event = regevent.Registered
		}

		c := r.Contacts[0]
		wantContact(t, c, regevent.Active, event)

		if c.URI != u.contact || c.CallID != u.callID || c.CSeq == nil || *c.CSeq != 2 || c.Expires == nil || *c.Expires != 600 ||
			c.Q != "0.5" || !reflect.DeepEqual(c.UnknownParams, wantParams) {
			t.Errorf("contact = %+v (cseq %v, expires %v)", c, deref(c.CSeq), deref(c.Expires))
		}
	}

	h.clock.Advance(time.Minute)

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), contact: contact}), 200)
	h.hss.noCx(t)

	n := s.recvNotify()
	wantState(t, n, "active;expires=540", 1)

	if !reflect.DeepEqual(n.aors(), first.aors()) {
		t.Fatalf("registrations = %v, want %v", n.aors(), first.aors())
	}

	for _, aor := range n.aors() {
		r, before := n.registration(t, aor), first.registration(t, aor)
		if r.ID != before.ID || len(r.Contacts) != 1 || r.Contacts[0].ID != before.Contacts[0].ID {
			t.Fatalf("ids of %s changed: %+v, was %+v", aor, r, before)
		}

		c := r.Contacts[0]
		wantContact(t, c, regevent.Active, regevent.Refreshed)

		if *c.CSeq != 3 || *c.Expires != 600 {
			t.Errorf("contact = %+v (cseq %d, expires %d)", c, *c.CSeq, *c.Expires)
		}
	}
}

func deref(p *uint32) any {
	if p == nil {
		return nil
	}

	return *p
}

func checkIDs(t *testing.T, n notification) {
	t.Helper()

	seen := map[string]bool{}

	for _, r := range n.info.Registrations {
		if r.ID == "" || seen[r.ID] {
			t.Fatalf("registration id %q repeated or empty in %+v", r.ID, n.info)
		}

		seen[r.ID] = true

		for _, c := range r.Contacts {
			if c.ID == "" || seen[c.ID] {
				t.Fatalf("contact id %q repeated or empty in %+v", c.ID, n.info)
			}

			seen[c.ID] = true
		}
	}
}

func TestNotifySharedIMPU(t *testing.T) {
	h := newHarness(t)

	phone := h.newUE()
	phone.register(registerOptions{})
	h.hss.nextSAR(t)

	s := h.ueSubscriber(phone)
	s.impu = testAlias
	s.subscribed()

	tablet := h.newUE()
	tablet.impi = "tablet@" + homeDomain
	tablet.impu = testAlias
	tablet.contact = "sip:tablet@127.0.0.1:5090"
	tablet.register(registerOptions{})
	h.hss.nextSAR(t)

	wantStatus(t, s.subscribe("600"), 200)

	n := s.recvNotify()
	checkIDs(t, n)

	if r := n.registration(t, testAlias); len(r.Contacts) != 2 {
		t.Fatalf("registration = %+v, want both contacts", r)
	}

	n.contact(t, testAlias, phone.contact)
	n.contact(t, testAlias, tablet.contact)
}

func (h *harness) subscribeBoth(u *ue, p *fakeProxy, o registerOptions) (ueSub, pcscfSub *subscriber) {
	h.t.Helper()

	u.path = p.path()
	u.register(o)
	h.hss.nextSAR(h.t)

	ueSub = h.ueSubscriber(u)
	ueSub.subscribed()

	pcscfSub = h.pcscfSubscriber(p)
	pcscfSub.subscribed()

	return ueSub, pcscfSub
}

func TestUEDeregistrationNotify(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()

	port := strconv.Itoa(int(u.inbox.Addr().Port()))
	a, b := "sip:a@127.0.0.1:"+port, "sip:b@127.0.0.1:"+port
	u.contact = a

	ueSub, pcscfSub := h.subscribeBoth(u, p, registerOptions{contact: "<" + a + ">, <" + b + ">"})

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), contact: "<" + a + ">;expires=0"}), 200)
	h.hss.noCx(t)

	for _, s := range []*subscriber{pcscfSub, ueSub} {
		n := s.recvNotify()
		wantState(t, n, "active;expires=600", 1)

		if r := n.registration(t, testMSISDN); r.State != regevent.Active || len(r.Contacts) != 2 {
			t.Fatalf("registration = %+v", r)
		}

		wantContact(t, n.contact(t, testMSISDN, a), regevent.Terminated, regevent.Unregistered)
		wantContact(t, n.contact(t, testMSISDN, b), regevent.Active, regevent.Created)
	}

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), contact: "<" + b + ">;expires=0"}), 200)
	h.hss.wantSAR(t, cx.AssignmentUserDeregistration)

	n := pcscfSub.recvNotify()
	wantState(t, n, "terminated;reason=noresource", 2)

	for _, aor := range []string{testMSISDN, testTel, testAlias} {
		if r := n.registration(t, aor); r.State != regevent.Terminated || len(r.Contacts) != 1 {
			t.Fatalf("registration = %+v", r)
		}

		wantContact(t, n.contact(t, aor, b), regevent.Terminated, regevent.Unregistered)
	}

	ueSub.noNotify()
	h.waitSubscriptions(0)
}

func TestExpiryNotify(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()

	ueSub, pcscfSub := h.subscribeBoth(u, p, registerOptions{expires: "300"})

	h.clock.Advance(300*time.Second - time.Millisecond)
	pcscfSub.noNotify()

	h.clock.Advance(time.Millisecond)

	if sar := h.hss.wantSAR(t, cx.AssignmentTimeoutDeregistration); !reflect.DeepEqual(sar.PublicIdentities, []string{testIMPU}) {
		t.Fatalf("SAR = %+v", sar)
	}

	for _, s := range []*subscriber{pcscfSub, ueSub} {
		n := s.recvNotify()
		wantState(t, n, "terminated;reason=noresource", 1)

		r := n.registration(t, testMSISDN)
		if r.State != regevent.Terminated || len(r.Contacts) != 1 {
			t.Fatalf("registration = %+v", r)
		}

		wantContact(t, r.Contacts[0], regevent.Terminated, regevent.Expired)
	}

	h.wantUnregistered()
	h.waitSubscriptions(0)
}

func TestContactReplacementNotify(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()
	u.path = p.path()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	u.impu = secondIMPU
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
	h.hss.nextSAR(t)

	ueSub := h.ueSubscriber(u)
	ueSub.subscribed()

	pcscfSub := h.pcscfSubscriber(p)
	pcscfSub.subscribed()

	old := u.contact

	u.impu = testIMPU
	u.contact = "sip:001010000000001@127.0.0.2:5060"
	u.callID = "new@127.0.0.1"
	u.register(registerOptions{})

	h.hss.wantSAR(t, cx.AssignmentReRegistration)
	h.hss.wantSAR(t, cx.AssignmentAdministrativeDeregistration)

	n := ueSub.recvNotify()
	wantState(t, n, "terminated;reason=noresource", 1)
	wantContact(t, n.contact(t, testMSISDN, old), regevent.Terminated, regevent.Unregistered)

	n = pcscfSub.recvNotify()
	wantState(t, n, "active;expires=600", 1)
	checkIDs(t, n)

	if r := n.registration(t, testMSISDN); r.State != regevent.Active || len(r.Contacts) != 2 {
		t.Fatalf("registration = %+v", r)
	}

	wantContact(t, n.contact(t, testMSISDN, old), regevent.Terminated, regevent.Unregistered)
	wantContact(t, n.contact(t, testMSISDN, u.contact), regevent.Active, regevent.Created)

	if r := n.registration(t, secondIMPU); r.State != regevent.Terminated || len(r.Contacts) != 1 {
		t.Fatalf("registration = %+v", r)
	}

	wantContact(t, n.contact(t, secondIMPU, old), regevent.Terminated, regevent.Unregistered)

	if subs := h.waitSubscriptions(1); subs[0].Subscriber != db.SubscriberPCSCF {
		t.Fatalf("subscriptions = %+v, want the P-CSCF's", subs)
	}
}

func TestRTRNotify(t *testing.T) {
	for _, tt := range []struct {
		reason cx.ReasonCode
		event  regevent.Event
		state  string
	}{
		{cx.ReasonPermanentTermination, regevent.Rejected, "terminated;reason=rejected"},
		{cx.ReasonServerChange, regevent.Deactivated, "terminated;reason=deactivated"},
	} {
		t.Run(tt.reason.String(), func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()
			p := h.newPCSCF()

			ueSub, pcscfSub := h.subscribeBoth(u, p, registerOptions{})

			impis, err := h.reg.Terminate(t.Context(), cx.RegistrationTerminationRequest{
				PrivateIdentity: testIMPI,
				Reason:          cx.DeregistrationReason{Code: tt.reason},
			})
			if err != nil || len(impis) != 0 {
				t.Fatalf("Terminate = %v, %v", impis, err)
			}

			h.wantUnregistered()

			for _, s := range []*subscriber{pcscfSub, ueSub} {
				n := s.recvNotify()
				wantState(t, n, tt.state, 1)

				for _, aor := range []string{testMSISDN, testTel, testAlias} {
					if r := n.registration(t, aor); r.State != regevent.Terminated || len(r.Contacts) != 1 {
						t.Fatalf("registration = %+v", r)
					}

					wantContact(t, n.contact(t, aor, u.contact), regevent.Terminated, tt.event)
				}
			}

			h.waitSubscriptions(0)
			h.hss.noCx(t)
		})
	}
}

func TestRTRNewServerAssigned(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()

	ueSub, pcscfSub := h.subscribeBoth(u, p, registerOptions{})

	if _, err := h.reg.Terminate(t.Context(), cx.RegistrationTerminationRequest{
		PrivateIdentity: testIMPI,
		Reason:          cx.DeregistrationReason{Code: cx.ReasonNewServerAssigned},
	}); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	h.wantUnregistered()

	if subs := h.subscriptions(); len(subs) != 0 {
		t.Fatalf("subscriptions = %+v, want none", subs)
	}

	pcscfSub.noNotify()
	ueSub.noNotify()
	h.hss.noCx(t)
}

func TestRTRPublicIdentities(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()
	u.path = p.path()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	u.impu = secondIMPU
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
	h.hss.nextSAR(t)

	s := h.pcscfSubscriber(p)
	s.subscribed()

	rtr := cx.RegistrationTerminationRequest{
		PrivateIdentity:  testIMPI,
		PublicIdentities: []string{secondIMPU},
		Reason:           cx.DeregistrationReason{Code: cx.ReasonPermanentTermination},
	}
	if _, err := h.reg.Terminate(t.Context(), rtr); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	if regs := h.registrations(testIMPI); len(regs) != 1 || regs[0].IMPU != testIMPU {
		t.Fatalf("registrations = %+v, want the first set only", regs)
	}

	n := s.recvNotify()
	wantState(t, n, "active;expires=600", 1)
	wantContact(t, n.contact(t, secondIMPU, u.contact), regevent.Terminated, regevent.Rejected)
	wantContact(t, n.contact(t, testMSISDN, u.contact), regevent.Active, regevent.Created)

	rtr.PublicIdentities = nil
	if _, err := h.reg.Terminate(t.Context(), rtr); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	h.wantUnregistered()

	n = s.recvNotify()
	wantState(t, n, "terminated;reason=rejected", 2)

	if got, want := n.aors(), []string{testMSISDN, testTel, testAlias}; !reflect.DeepEqual(got, want) {
		t.Fatalf("registrations = %v, want %v", got, want)
	}

	h.hss.noCx(t)
}

func TestRTRAssociatedIdentities(t *testing.T) {
	h := newHarness(t)

	phone := h.newUE()
	phone.register(registerOptions{})
	h.hss.nextSAR(t)

	tablet := h.newUE()
	tablet.impi = "tablet@" + homeDomain
	tablet.impu = testAlias
	tablet.contact = "sip:tablet@127.0.0.1:5090"
	tablet.register(registerOptions{})
	h.hss.nextSAR(t)

	impis, err := h.reg.Terminate(t.Context(), cx.RegistrationTerminationRequest{
		PrivateIdentity:      testIMPI,
		AssociatedIdentities: []string{tablet.impi, testIMPI},
		Reason:               cx.DeregistrationReason{Code: cx.ReasonPermanentTermination},
	})
	if err != nil || !reflect.DeepEqual(impis, []string{tablet.impi}) {
		t.Fatalf("Terminate = %v, %v", impis, err)
	}

	h.wantUnregistered()

	if regs := h.registrations(tablet.impi); len(regs) != 0 {
		t.Fatalf("tablet registrations = %+v, want none", regs)
	}

	h.hss.noCx(t)
}

func TestRTRUnknownIdentity(t *testing.T) {
	h := newHarness(t)

	impis, err := h.reg.Terminate(t.Context(), cx.RegistrationTerminationRequest{
		PrivateIdentity:  "nobody@" + homeDomain,
		PublicIdentities: []string{unknownIMPU},
		Reason:           cx.DeregistrationReason{Code: cx.ReasonPermanentTermination},
	})
	if err != nil || len(impis) != 0 {
		t.Fatalf("Terminate = %v, %v", impis, err)
	}

	h.hss.noCx(t)
}

func TestRTRRemoveSCSCF(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()

	ueSub, pcscfSub := h.subscribeBoth(u, p, registerOptions{})

	if _, err := h.reg.Terminate(t.Context(), cx.RegistrationTerminationRequest{
		PrivateIdentity: testIMPI,
		Reason:          cx.DeregistrationReason{Code: cx.ReasonRemoveSCSCF},
	}); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	h.registration(testIMPU)
	h.waitSubscriptions(2)
	pcscfSub.noNotify()
	ueSub.noNotify()
	h.hss.noCx(t)
}

func TestNotifyFailure(t *testing.T) {
	t.Run("481", func(t *testing.T) {
		h := newHarness(t)
		u := h.newUE()

		u.register(registerOptions{})
		h.hss.nextSAR(t)

		s := h.ueSubscriber(u)
		wantStatus(t, s.subscribe("600"), 200)
		s.answerNotify(481)

		h.waitSubscriptions(0)
		wantStatus(t, s.subscribe("600"), 481)
	})

	t.Run("500", func(t *testing.T) {
		h := newHarness(t)
		u := h.newUE()

		u.register(registerOptions{})
		h.hss.nextSAR(t)

		s := h.ueSubscriber(u)
		wantStatus(t, s.subscribe("600"), 200)
		s.answerNotify(500)

		time.Sleep(100 * time.Millisecond)
		h.waitSubscriptions(1)

		wantStatus(t, s.subscribe("600"), 200)
		wantState(t, s.recvNotify(), "active;expires=600", 1)
	})

	t.Run("timeout", func(t *testing.T) {
		h := newHarness(t)
		u := h.newUE()

		u.register(registerOptions{})
		h.hss.nextSAR(t)

		s := h.ueSubscriber(u)
		wantStatus(t, s.subscribe("600"), 200)

		req, _ := s.inbox.RecvRequest()
		s.checkNotify(req)

		h.sipClock.Advance(64 * transaction.DefaultT1)

		h.waitSubscriptions(0)
	})
}

func TestSubscriptionExpiry(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	s := h.ueSubscriber(u)
	wantStatus(t, s.subscribe("120"), 200)
	wantState(t, s.recvNotify(), "active;expires=120", 0)

	h.clock.Advance(120*time.Second - time.Millisecond)
	s.noNotify()

	h.clock.Advance(time.Millisecond)

	n := s.recvNotify()
	wantState(t, n, "terminated;reason=timeout", 1)

	if r := n.registration(t, testMSISDN); r.State != regevent.Active {
		t.Fatalf("registration = %+v", r)
	}

	h.waitSubscriptions(0)
	h.registration(testIMPU)
	h.hss.noCx(t)
}

func TestNotifyAfterRestart(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	s := h.ueSubscriber(u)
	first := s.subscribed()

	h.restart()

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)

	n := s.recvNotify()
	wantState(t, n, "active;expires=600", 1)

	if n.cseq != first.cseq+1 {
		t.Fatalf("CSeq = %d, want %d", n.cseq, first.cseq+1)
	}

	wantContact(t, n.contact(t, testMSISDN, u.contact), regevent.Active, regevent.Refreshed)
}

func TestMixedRegisterReportsTheRemovedContact(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()

	port := strconv.Itoa(int(u.inbox.Addr().Port()))
	a, b := "sip:a@127.0.0.1:"+port, "sip:b@127.0.0.1:"+port
	u.contact = a

	_, pcscfSub := h.subscribeBoth(u, p, registerOptions{contact: "<" + a + ">, <" + b + ">"})

	res := u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), contact: "<" + a + ">;expires=0, <" + b + ">"})
	wantStatus(t, res, 200)

	if got := contacts(t, res); got[a] != "0" || got[b] != "3600" {
		t.Fatalf("Contact = %v, want %s with expires 0 and %s refreshed", got, a, b)
	}

	n := pcscfSub.recvNotify()
	wantState(t, n, "active;expires=600", 1)
	wantContact(t, n.contact(t, testMSISDN, a), regevent.Terminated, regevent.Unregistered)
	wantContact(t, n.contact(t, testMSISDN, b), regevent.Active, regevent.Refreshed)
}

func TestRegisterReportsExpiredContacts(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()

	port := strconv.Itoa(int(u.inbox.Addr().Port()))
	a, b := "sip:a@127.0.0.1:"+port, "sip:b@127.0.0.1:"+port
	u.contact = a

	_, pcscfSub := h.subscribeBoth(u, p, registerOptions{contact: "<" + a + ">;expires=300, <" + b + ">"})

	if !h.reg.tryLock(testIMPI) {
		t.Fatal("IMPI busy")
	}

	h.clock.Advance(300 * time.Second)
	h.reg.unlock(testIMPI)

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), contact: "<" + b + ">"}), 200)

	n := pcscfSub.recvNotify()
	wantContact(t, n.contact(t, testMSISDN, a), regevent.Terminated, regevent.Expired)
	wantContact(t, n.contact(t, testMSISDN, b), regevent.Active, regevent.Refreshed)
}

func TestSharedIMPUNotifiesTheOtherUsers(t *testing.T) {
	h := newHarness(t)
	p := h.newPCSCF()

	phone := h.newUE()
	phone.path = p.path()
	phone.register(registerOptions{})
	h.hss.nextSAR(t)

	s := h.pcscfSubscriber(p)
	s.impu = testAlias
	s.subscribed()

	tablet := h.newUE()
	tablet.impi = "tablet@" + homeDomain
	tablet.impu = testAlias
	tablet.contact = "sip:tablet@127.0.0.1:5090"
	tablet.register(registerOptions{})
	h.hss.nextSAR(t)

	n := s.recvNotify()
	wantContact(t, n.contact(t, testAlias, tablet.contact), regevent.Active, regevent.Registered)

	wantStatus(t, phone.send(registerOptions{auth: phone.protected(testNonce(), testVector.XRES), contact: "*", expires: "0"}), 200)
	h.hss.nextSAR(t)

	n = s.recvNotify()
	if !strings.HasPrefix(n.state, "active") {
		t.Fatalf("Subscription-State %q, want active while the shared identity is registered", n.state)
	}

	if r := n.registration(t, testAlias); r.State != regevent.Active {
		t.Fatalf("shared registration = %+v, want active", r)
	}

	wantContact(t, n.contact(t, testAlias, phone.contact), regevent.Terminated, regevent.Unregistered)
}

func TestSubscribeUserFromTheServiceRoute(t *testing.T) {
	h := newHarness(t)

	phone := h.newUE()
	phone.register(registerOptions{})
	h.hss.nextSAR(t)

	tablet := h.newUE()
	tablet.impi = "tablet@" + homeDomain
	tablet.impu = testAlias
	tablet.contact = "sip:tablet@127.0.0.1:" + strconv.Itoa(int(tablet.inbox.Addr().Port()))
	res := tablet.register(registerOptions{})

	h.hss.nextSAR(t)

	s := h.ueSubscriber(tablet)
	s.impu = testAlias
	s.pai = "<" + testAlias + ">"
	s.route = res.Header.Get("Service-Route")
	s.subscribed()

	if subs := h.subscriptions(); len(subs) != 0 {
		t.Fatalf("phone subscriptions = %+v, want none", subs)
	}

	subs, err := h.db.ListRegSubscriptions(t.Context(), tablet.impi)
	if err != nil || len(subs) != 1 {
		t.Fatalf("tablet subscriptions = %+v, %v; want one", subs, err)
	}
}

func TestNewBindingGetsANewContactID(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()

	_, s := h.subscribeBoth(u, p, registerOptions{})

	u.impu = secondIMPU
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
	h.hss.nextSAR(t)

	first := s.recvNotify().contact(t, secondIMPU, u.contact)

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "0"}), 200)
	h.hss.nextSAR(t)
	wantContact(t, s.recvNotify().contact(t, secondIMPU, u.contact), regevent.Terminated, regevent.Unregistered)

	h.clock.Advance(time.Second)
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
	h.hss.nextSAR(t)

	again := s.recvNotify().contact(t, secondIMPU, u.contact)
	if again.ID == first.ID || again.Event != regevent.Registered {
		t.Fatalf("new binding: id %s event %s, want a new id than %s and registered", again.ID, again.Event, first.ID)
	}
}

func TestUnchangedBindingKeepsItsEvent(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()

	port := strconv.Itoa(int(u.inbox.Addr().Port()))
	other := "sip:other@127.0.0.1:" + port

	u.impu = testMSISDN
	_, s := h.subscribeBoth(u, p, registerOptions{contact: "<" + u.contact + ">, <" + other + ">"})

	u.impu = testAlias
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), contact: "<" + other + ">"}), 200)

	n := s.recvNotify()
	wantContact(t, n.contact(t, testMSISDN, u.contact), regevent.Active, regevent.Registered)
	wantContact(t, n.contact(t, testAlias, u.contact), regevent.Active, regevent.Created)
	wantContact(t, n.contact(t, testMSISDN, other), regevent.Active, regevent.Refreshed)
}

func TestSubscribeAcceptsMediaRanges(t *testing.T) {
	for _, accept := range []string{"*/*", "application/*", "text/plain, application/reginfo+xml;q=0.5"} {
		t.Run(accept, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()
			u.register(registerOptions{})
			h.hss.nextSAR(t)

			s := h.ueSubscriber(u)
			s.accept = accept
			s.subscribed()
		})
	}
}

func TestSubscribeWithoutNotifyIsRefused(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	u.register(registerOptions{})
	h.hss.nextSAR(t)

	h.cfg.Listeners = nil
	h.restart()

	wantStatus(t, h.ueSubscriber(u).subscribe("600"), 500)
	h.waitSubscriptions(0)
}

func TestRTRServerChangeDeregistersEverything(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	u.impu = secondIMPU
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
	h.hss.nextSAR(t)

	if _, err := h.reg.Terminate(t.Context(), cx.RegistrationTerminationRequest{
		PrivateIdentity:  testIMPI,
		PublicIdentities: []string{secondIMPU},
		Reason:           cx.DeregistrationReason{Code: cx.ReasonServerChange},
	}); err != nil {
		t.Fatal(err)
	}

	h.wantUnregistered()
}
