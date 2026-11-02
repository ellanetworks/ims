package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	domain  = "mcptt.example.com"
	alice   = "sip:alice@mcptt.example.com"
	bob     = "sip:bob@mcptt.example.com"
	fire    = "sip:fire@mcptt.example.com"
	police  = "sip:police@mcptt.example.com"
	aliceIM = "sip:alice@ims.example.com"
	bobIM   = "sip:bob@ims.example.com"
	bobTel  = "tel:+15555550102"
	client1 = "urn:uuid:00000000-0000-1000-8000-000000000001"
	client2 = "urn:uuid:00000000-0000-1000-8000-000000000002"
	psi     = "sip:mcptt-participating@mcptt.example.com"
)

func configuration() settings.Settings {
	group := func(uri string) settings.Group {
		return settings.Group{
			URI: uri, InviteMembers: true, TNG3: 10 * time.Minute,
			List: settings.List{Entries: []settings.Entry{{URI: alice}, {URI: bob}}},
		}
	}

	return settings.Settings{
		System: settings.System{ParticipatingPSI: psi, ControllingPSI: "sip:mcptt-controlling@mcptt.example.com"},
		ServiceConfiguration: settings.ServiceConfiguration{
			Domain:    domain,
			OnNetwork: settings.OnNetwork{FCTimersCounters: settings.FCTimersCounters{T7: new(2 * time.Second)}},
		},
		Users: []settings.User{
			{MCPTTUserID: alice, IMPUs: []string{aliceIM}, ImplicitAffiliations: []string{fire}},
			{MCPTTUserID: bob, IMPUs: []string{bobIM, bobTel}, ImplicitAffiliations: []string{fire}, MaxAffiliationsN2: new(1)},
		},
		Groups: []settings.Group{group(fire), group(police)},
	}
}

func prefix(a netip.Addr) netip.Prefix { return netip.PrefixFrom(a, a.BitLen()) }

type swappable struct {
	p atomic.Pointer[settings.Snapshot]
}

func (s *swappable) Current() *settings.Snapshot { return s.p.Load() }

type harness struct {
	t             *testing.T
	n             *siptest.Net
	s             *Server
	addr          netip.AddrPort
	mode          registrar.Mode
	src           *swappable
	tp            oteltrace.TracerProvider
	scscf, scscf2 *scscf
}

func newHarness(t *testing.T, mode registrar.Mode) *harness {
	return newHarnessOf(t, mode, nil)
}

func newHarnessOf(t *testing.T, mode registrar.Mode, state State) *harness {
	t.Helper()

	return newTracedHarness(t, mode, state, nil)
}

func newTracedHarness(t *testing.T, mode registrar.Mode, state State, tp oteltrace.TracerProvider) *harness {
	t.Helper()

	n := siptest.NewNet(t)
	h := &harness{t: t, n: n, addr: n.Addr(), mode: mode, src: &swappable{}, tp: tp}
	h.scscf = &scscf{h: h, p: n.Peer(), subs: map[string]*regSub{}, initial: map[string]string{}}
	h.scscf2 = &scscf{h: h, p: n.Peer(), subs: map[string]*regSub{}, initial: map[string]string{}}

	h.start(state)

	t.Cleanup(func() {
		n.Await(func() { _ = h.s.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1)
	})

	return h
}

func (h *harness) start(state State) {
	h.t.Helper()

	if h.src.Current() == nil {
		snap, err := settings.New(configuration())
		if err != nil {
			h.t.Fatal(err)
		}

		h.src.p.Store(snap)
	}

	s, err := New(Config{
		UA: ua.Config{
			Transaction: h.n.Config(transaction.Config{TracerProvider: h.tp}),
			Prepare:     h.prepare,
			Contact:     sip.Address{URI: sip.URI{Scheme: "sip", User: "as", Host: h.addr.Addr().String(), Port: h.addr.Port()}},
		},
		Source:          h.src,
		Mode:            h.mode,
		TrustedNetworks: []netip.Prefix{prefix(h.scscf.p.Addr().Addr()), prefix(h.scscf2.p.Addr().Addr())},
		HomeDomain:      "ims.example.com",
		State:           state,
	})
	if err != nil {
		h.t.Fatal(err)
	}

	if err := s.Agent().Layer().Serve(h.addr); err != nil {
		h.t.Fatal(err)
	}

	h.s = s
}

func (h *harness) now() time.Time { return h.s.Agent().Layer().Clock().Now() }

func (h *harness) prepare(req *sip.Request) error {
	tr, to, err := sip.NextHop(req)
	if err != nil {
		return err
	}

	req.Flow = sip.Flow{Transport: tr, Local: h.addr, Remote: to}
	req.Header.Prepend("Via", sip.NewVia(tr, h.addr).String())

	return nil
}

func uri(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

func token(id string) string {
	enc := base64.RawURLEncoding.EncodeToString

	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(`{"mcptt_id":"`+id+`","client_id":"c","scope":"openid 3gpp:mcptt:ptt_server"}`)) + ".c2ln"
}

// TS 24.379 clause 7.2.1
func info(token, client string) sip.BodyPart {
	var b strings.Builder

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:1.0"><mcptt-Params>`)

	if token != "" {
		b.WriteString(`<mcptt-access-token type="Normal"><mcpttString>` + token + `</mcpttString></mcptt-access-token>`)
	}

	if client != "" {
		b.WriteString(`<mcptt-client-id type="Normal"><mcpttString>` + client + `</mcpttString></mcptt-client-id>`)
	}

	b.WriteString(`</mcptt-Params></mcpttinfo>`)

	return sip.NewBodyPart(mcpttinfo.ContentType, []byte(b.String()))
}

// RFC 3261 section 10.2.4
// RFC 5626 section 4.1
// RFC 5626 section 4.2
type client struct {
	h        *harness
	p        *siptest.Peer
	impu     string
	callID   string
	cseq     int
	instance string
	regID    string
}

func (h *harness) client(impu string) *client {
	return &client{h: h, p: h.n.Peer(), impu: impu, callID: sip.NewTag() + "@ue"}
}

func (c *client) contact() string {
	v := fmt.Sprintf(`<sip:ue@%s>;+g.3gpp.mcptt;+g.3gpp.icsi-ref="urn%%3Aurn-7%%3A3gpp-service.ims.icsi.mcptt"`, c.p.Addr())
	if c.instance != "" {
		v += `;+sip.instance="<` + c.instance + `>"`
	}

	if c.regID != "" {
		v += ";reg-id=" + c.regID
	}

	return v
}

// TS 24.379 clause 7.2.1
func (c *client) register(target string, expires int, bodies ...sip.BodyPart) *sip.Request {
	c.cseq++

	req := siptest.NewRequest("REGISTER", target, sip.UDP, c.p.Addr())
	req.Header.Set("From", "<"+c.impu+">;tag="+sip.NewTag())
	req.Header.Set("To", "<"+c.impu+">")
	req.Header.Set("Call-ID", c.callID)
	req.Header.Set("CSeq", fmt.Sprintf("%d REGISTER", c.cseq))
	req.Header.Set("Contact", c.contact())

	if expires >= 0 {
		req.Header.Set("Expires", fmt.Sprint(expires))
	}

	req.SetBodies(bodies...)

	return req
}

func (c *client) send(req *sip.Request) *sip.Response {
	c.h.t.Helper()
	c.p.Send(sip.UDP, c.h.addr, req)

	for {
		res, _ := c.p.RecvResponse()
		if res.StatusCode != 100 {
			return res
		}
	}
}

func devices(t *testing.T, res *sip.Response) bool {
	t.Helper()

	part, ok, err := res.Content().Find(mcpttinfo.ContentType)
	if err != nil || !ok {
		return false
	}

	info, err := mcpttinfo.Decode(part.Body)
	if err != nil || info.Params == nil || len(info.Params.Ext) != 1 || info.Params.Ext[0].Name.Local != "multiple-devices-ind" {
		t.Fatalf("mcptt-info of the 200: %s", part.Body)
	}

	return strings.Contains(string(part.Body), "<mcpttBoolean>true</mcpttBoolean>")
}

func (h *harness) affiliated(user, client, group string) bool {
	h.t.Helper()

	p := h.s.Participating().Affiliated(h.now(), uri(h.t, user), client, uri(h.t, group))
	c := h.s.Controlling().Affiliated(h.now(), uri(h.t, group), uri(h.t, user), client)

	if p != c {
		h.t.Fatalf("%s at %s to %s: participating %v, controlling %v", user, client, group, p, c)
	}

	return p
}

// RFC 3261 section 10.3
// TS 24.379 clause 7.3.2
// TS 24.379 clause 9.2.2.2.15
func TestDirectRegisterRefreshExpiry(t *testing.T) {
	h := newHarness(t, registrar.Direct)
	c := h.client(aliceIM)

	res := c.send(c.register("sip:"+domain, 600, info(token(alice), client1)))
	if res.StatusCode != 200 || devices(t, res) {
		t.Fatalf("REGISTER: %d %s", res.StatusCode, res.Body)
	}

	contacts, _ := res.Header.Contacts()
	if len(contacts) != 1 || contacts[0].URI.User != "ue" {
		t.Fatalf("contacts of the 200 %v", contacts)
	}

	if e, _ := contacts[0].Params.Get("expires"); e != "600" {
		t.Errorf("contact expires %q", e)
	}

	bs := h.s.Registrar().Bindings(uri(t, alice))
	if len(bs) != 1 || bs[0].ClientID != client1 || len(bs[0].Contacts) != 1 || !bs[0].IMPU.Equivalent(uri(t, aliceIM)) {
		t.Fatalf("bindings %+v", bs)
	}

	if !h.affiliated(alice, client1, fire) || h.affiliated(alice, client1, police) {
		t.Error("implicit affiliation to fire only")
	}

	h.n.Advance(500 * time.Second)

	if res := c.send(c.register("sip:"+domain, 600)); res.StatusCode != 200 {
		t.Fatalf("refresh: %d", res.StatusCode)
	}

	c.cseq--
	if res := c.send(c.register("sip:"+domain, 600)); res.StatusCode != 500 {
		t.Fatalf("REGISTER out of order: %d", res.StatusCode)
	}

	h.n.Advance(500 * time.Second)

	if len(h.s.Registrar().Bindings(uri(t, alice))) != 1 || !h.affiliated(alice, client1, fire) {
		t.Fatal("refreshed binding expired")
	}

	h.n.Advance(101 * time.Second)

	if bs := h.s.Registrar().Bindings(uri(t, alice)); len(bs) != 0 {
		t.Fatalf("bindings after the expiry %+v", bs)
	}

	if h.affiliated(alice, client1, fire) {
		t.Error("affiliated after the expiry")
	}
}

func TestShutdownStopsExpiry(t *testing.T) {
	h := newHarness(t, registrar.Direct)
	c := h.client(aliceIM)

	if res := c.send(c.register("sip:"+domain, 600, info(token(alice), client1))); res.StatusCode != 200 {
		t.Fatalf("REGISTER: %d", res.StatusCode)
	}

	h.n.Await(func() {
		if err := h.s.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}, 2*64*transaction.DefaultT1)

	h.n.Advance(601 * time.Second)

	if len(h.s.Registrar().Bindings(uri(t, alice))) != 1 || !h.affiliated(alice, client1, fire) {
		t.Error("expired after the shutdown")
	}
}

// RFC 3261 section 10.3
func TestDirectDeregister(t *testing.T) {
	h := newHarness(t, registrar.Direct)
	c := h.client(aliceIM)

	if res := c.send(c.register("sip:"+domain, 600)); res.StatusCode != 403 {
		t.Fatalf("REGISTER without service authorisation: %d", res.StatusCode)
	}

	if res := c.send(c.register("sip:"+domain, 600, info(token(alice), client1))); res.StatusCode != 200 {
		t.Fatalf("REGISTER: %d", res.StatusCode)
	}

	res := c.send(c.register("sip:"+domain, 0))
	if contacts, _ := res.Header.Contacts(); res.StatusCode != 200 || len(contacts) != 0 {
		t.Fatalf("deregistration: %d %v", res.StatusCode, contacts)
	}

	if len(h.s.Registrar().Bindings(uri(t, alice))) != 0 || h.affiliated(alice, client1, fire) {
		t.Fatal("bound after the deregistration")
	}

	if res := c.send(c.register("sip:"+domain, 600, info(token(alice), client1))); res.StatusCode != 200 {
		t.Fatalf("REGISTER again: %d", res.StatusCode)
	}

	star := c.register("sip:"+domain, 0)
	star.Header.Set("Contact", "*")

	if res := c.send(star); res.StatusCode != 200 || len(h.s.Registrar().Bindings(uri(t, alice))) != 0 {
		t.Fatalf("Contact *: %d", res.StatusCode)
	}

	bad := c.register("sip:"+domain, 600)
	bad.Header.Set("Contact", "*")

	if res := c.send(bad); res.StatusCode != 400 {
		t.Errorf("Contact * of Expires 600: %d", res.StatusCode)
	}

	if res := c.send(c.register("sip:other.example.com", 600, info(token(alice), client1))); res.StatusCode != 404 {
		t.Errorf("REGISTER of another domain: %d", res.StatusCode)
	}
}

// TS 24.229 clause 5.4.1.7
// TS 24.229 clause 5.4.1.7A
type scscf struct {
	h                 *harness
	p                 *siptest.Peer
	regEvent          bool
	state             string
	pau               []string
	pauHeader         bool
	subs              map[string]*regSub
	unsubscribed      []string
	notified, pending []*sip.Response
	initial           map[string]string
	refusal           int
	subscribes        int
	refreshes         int
	silent            bool
}

type regSub struct {
	subscribe *sip.Request
	tag       string
	cseq      int
}

func (s *scscf) register(impu, regToken string, expires int, client *sip.Request) *sip.Request {
	req := siptest.NewRequest("REGISTER", "sip:as.mcptt.example.com", sip.UDP, s.p.Addr())
	req.Header.Set("From", "<sip:scscf.ims.example.com>;tag="+sip.NewTag())
	req.Header.Set("To", "<"+impu+">")

	req.Header.Set("Contact", s.contact(regToken).String())
	req.Header.Set("Expires", fmt.Sprint(expires))
	req.Header.Set("P-Charging-Vector", "icid-value=4a2f;orig-ioi=ims.example.com")

	if client != nil {
		ok := sip.NewResponse(client, 200, "")

		contact := client.Header.Get("Contact") + fmt.Sprintf(";expires=%d", expires)
		if c, err := client.Header.Contacts(); err == nil && c[0].Params.Has("+sip.instance") {
			v, _ := c[0].Params.Get("+sip.instance")
			contact += `;pub-gruu="` + impu + `;gr=` + strings.Trim(sip.Unquote(v), "<>") + `"`
		}

		ok.Header.Set("Contact", contact)

		var pau []string
		for _, u := range s.pau {
			pau = append(pau, "<"+u+">")
		}

		switch {
		case s.pauHeader:
			req.Header.Set("P-Associated-URI", strings.Join(pau, ", "))
			req.SetBody("message/sip", client.Bytes())
		default:
			if len(pau) > 0 {
				ok.Header.Set("P-Associated-URI", strings.Join(pau, ", "))
			}

			req.SetBodies(sip.NewBodyPart("message/sip", client.Bytes()), sip.NewBodyPart("message/sip", ok.Bytes()))
		}
	}

	return req
}

func (s *scscf) contact(regToken string) sip.Address {
	contact := sip.Address{URI: sip.URI{Scheme: "sip", Host: s.p.Addr().Addr().String(), Port: s.p.Addr().Port()}}

	if regToken != "" {
		p, err := sip.NewStringFeature("+g.3gpp.registration-token", regToken).Param()
		if err != nil {
			s.h.t.Fatal(err)
		}

		contact.Params = append(contact.Params, p)
	}

	return contact
}

func (s *scscf) send(req *sip.Request) *sip.Response {
	s.h.t.Helper()
	s.p.Send(sip.UDP, s.h.addr, req)

	for {
		if len(s.pending) > 0 {
			res := s.pending[0]
			s.pending = s.pending[1:]

			return res
		}

		r := s.p.Recv()

		switch m := r.Msg.(type) {
		case *sip.Request:
			s.handle(m)
		case *sip.Response:
			if !s.notifyResponse(m) && m.StatusCode != 100 {
				return m
			}
		}
	}
}

func (s *scscf) pump() {
	s.h.t.Helper()

	for {
		r, ok := s.p.RecvWithin(0)
		if !ok {
			return
		}

		switch m := r.Msg.(type) {
		case *sip.Request:
			s.handle(m)
		case *sip.Response:
			if !s.notifyResponse(m) && m.StatusCode != 100 {
				s.pending = append(s.pending, m)
			}
		}
	}
}

func (s *scscf) notifyResponse(res *sip.Response) bool {
	if cseq, err := res.Header.CSeq(); err != nil || cseq.Method != "NOTIFY" {
		return false
	}

	s.notified = append(s.notified, res)

	return true
}

// TS 24.229 clause 5.7.1.1
func (s *scscf) handle(req *sip.Request) {
	t := s.h.t
	t.Helper()

	if req.Method != "SUBSCRIBE" {
		t.Fatalf("%s of the server", req.Method)
	}

	if s.silent {
		return
	}

	to, _ := req.Header.To()
	from, _ := req.Header.From()
	pai, _ := req.Header.Addresses("P-Asserted-Identity")

	if ev := req.Header.Get("Event"); ev != "reg" || to.Tag() == "" && !to.URI.Equivalent(req.URI) || from.URI.String() != "sip:as.mcptt.example.com" ||
		len(pai) != 1 || !pai[0].URI.Equivalent(from.URI) || !strings.Contains(req.Header.Get("P-Charging-Vector"), "orig-ioi="+domain) {
		t.Fatalf("SUBSCRIBE %s", req)
	}

	res := sip.NewResponse(req, 200, "")

	if to.Tag() == "" {
		s.subscribes++
	}

	switch {
	case to.Tag() != "":
		if req.Header.Get("Expires") == "0" {
			s.unsubscribed = append(s.unsubscribed, to.URI.String())
		} else {
			s.refreshes++
		}
	case !s.regEvent:
		code := s.refusal
		if code == 0 {
			code = 403
		}

		res = sip.NewResponse(req, code, "")
		_ = res.Header.SetToTag(sip.NewTag())
	default:
		sub := &regSub{subscribe: req, tag: sip.NewTag()}
		s.subs[req.URI.String()] = sub

		_ = res.Header.SetToTag(sub.tag)
		res.Header.Set("Expires", "3761")
		res.Header.Set("Contact", s.contact("").String())
	}

	s.p.Send(sip.UDP, s.h.addr, res)

	if sub := s.subs[req.URI.String()]; sub != nil && to.Tag() == "" && s.regEvent {
		s.notify(req.URI.String(), 0, "full", s.initial[req.URI.String()])
	}
}

func (s *scscf) notify(impu string, version int, state, registration string) {
	sub := s.subs[impu]
	if sub == nil {
		s.h.t.Fatalf("no subscription of %s", impu)
	}

	contacts, _ := sub.subscribe.Header.Contacts()
	sub.cseq++

	n := siptest.NewRequest("NOTIFY", contacts[0].URI.String(), sip.UDP, s.p.Addr())
	n.Header.Set("From", "<"+impu+">;tag="+sub.tag)
	n.Header.Set("To", sub.subscribe.Header.Get("From"))
	n.Header.Set("Call-ID", sub.subscribe.Header.CallID())
	n.Header.Set("CSeq", fmt.Sprintf("%d NOTIFY", sub.cseq))
	n.Header.Set("Event", "reg")

	subState := s.state
	if subState == "" {
		subState = "active;expires=3761"
	}

	n.Header.Set("Subscription-State", subState)
	n.Header.Set("Contact", s.contact("").String())
	n.Header.Set("P-Charging-Vector", "icid-value=77;orig-ioi=ims.example.com")

	if registration == "" {
		registration = `<registration aor="` + impu + `" id="r1" state="active"/>`
	}

	n.SetBody("application/reginfo+xml", []byte(fmt.Sprintf(
		`<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="%d" state="%s">%s</reginfo>`, version, state, registration)))
	s.p.Send(sip.UDP, s.h.addr, n)
	s.pump()
}

// TS 24.379 clause 7.3.2
// TS 24.229 clause 5.7.1.1
func TestThirdParty(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	ue := h.client(aliceIM)

	res := s.send(s.register(aliceIM, "7", 600, ue.register("sip:ims.example.com", 600000, info(token(alice), client1))))
	if res.StatusCode != 200 || res.Header.Get("Expires") != "600" {
		t.Fatalf("third-party REGISTER: %d Expires %q", res.StatusCode, res.Header.Get("Expires"))
	}

	if v := res.Header.Get("P-Charging-Vector"); v != "icid-value=4a2f;orig-ioi=ims.example.com;term-ioi="+domain {
		t.Errorf("P-Charging-Vector %q", v)
	}

	b, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1)
	if !ok || b.MCPTTID.String() != alice || b.Token != "7" || b.SCSCF.HostPort() != s.p.Addr().String() || b.Charging.ICID != "4a2f" {
		t.Fatalf("binding %+v", b)
	}

	if !h.affiliated(alice, client1, fire) {
		t.Fatal("not implicitly affiliated")
	}

	h.n.Advance(500 * time.Second)

	if res := s.send(s.register(aliceIM, "7", 600, ue.register("sip:ims.example.com", 600000))); res.StatusCode != 200 {
		t.Fatalf("refresh: %d", res.StatusCode)
	}

	h.n.Advance(500 * time.Second)

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok {
		t.Fatal("refreshed binding expired")
	}

	h.n.Advance(101 * time.Second)

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); ok || h.affiliated(alice, client1, fire) {
		t.Fatal("binding or affiliation after the expiry")
	}

	if res := s.send(s.register(aliceIM, "8", 600, ue.register("sip:ims.example.com", 600000, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("REGISTER: %d", res.StatusCode)
	}

	if res := s.send(s.register(aliceIM, "", 0, nil)); res.StatusCode != 200 || res.Header.Get("Expires") != "0" {
		t.Fatalf("deregistration: %d", res.StatusCode)
	}

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); ok || h.affiliated(alice, client1, fire) {
		t.Fatal("binding or affiliation after the deregistration")
	}

	if res := s.send(s.register(aliceIM, "9", 600, ue.register("sip:ims.example.com", 600000, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("REGISTER: %d", res.StatusCode)
	}

	off := ue.register("sip:ims.example.com", 600000)
	off.Header.Set("Contact", "<sip:ue@192.0.2.1>")

	if res := s.send(s.register(aliceIM, "9", 600, off)); res.StatusCode != 200 {
		t.Fatalf("log off: %d", res.StatusCode)
	}

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); ok {
		t.Fatal("bound after logging off")
	}
}

// RFC 3261 section 10.3
// TS 24.379 clause 7.2.1
func TestThirdPartyOutOfOrder(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	ue := h.client(aliceIM)

	plain := func() *sip.Request {
		req := ue.register("sip:ims.example.com", 600000)
		req.Header.Set("Contact", "<sip:ue@"+ue.p.Addr().String()+">")

		return req
	}

	ims := plain()
	logon := ue.register("sip:ims.example.com", 600000, info(token(alice), client1))

	for _, reg := range []*sip.Request{logon, ims} {
		if res := s.send(s.register(aliceIM, "7", 600, reg)); res.StatusCode != 200 {
			t.Fatalf("CSeq %s: %d", reg.Header.Get("CSeq"), res.StatusCode)
		}
	}

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok || !h.affiliated(alice, client1, fire) {
		t.Fatal("logged off by the earlier IMS registration")
	}

	again := ue.register("sip:ims.example.com", 600000, info(token(alice), client1))
	off := plain()

	for _, reg := range []*sip.Request{off, again} {
		if res := s.send(s.register(aliceIM, "7", 600, reg)); res.StatusCode != 200 {
			t.Fatalf("CSeq %s: %d", reg.Header.Get("CSeq"), res.StatusCode)
		}
	}

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); ok || h.affiliated(alice, client1, fire) {
		t.Fatal("logged on again by the earlier log-on")
	}

	h.n.Advance(601 * time.Second)

	if res := s.send(s.register(aliceIM, "8", 600, again)); res.StatusCode != 200 {
		t.Fatalf("after the expiry: %d", res.StatusCode)
	}

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok {
		t.Fatal("the sequence of an expired registration kept")
	}
}

// TS 24.379 clause 7.3.2
// TS 24.379 clause 7.3.1A
// TS 24.379 clause 6.6.3.4
func TestAuthorisationFailures(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	ue := h.client(aliceIM)

	encrypted := sip.NewBodyPart(mcpttinfo.ContentType, []byte(`<mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:1.0"><mcptt-Params>`+
		`<mcptt-access-token type="Encrypted"><EncryptedData xmlns="http://www.w3.org/2001/04/xmlenc#"/></mcptt-access-token>`+
		`<mcptt-client-id type="Encrypted"><EncryptedData xmlns="http://www.w3.org/2001/04/xmlenc#"/></mcptt-client-id>`+
		`</mcptt-Params></mcpttinfo>`))
	half := sip.NewBodyPart(mcpttinfo.ContentType, []byte(`<mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:1.0"><mcptt-Params>`+
		`<mcptt-access-token type="Normal"><mcpttString>`+token(alice)+`</mcpttString></mcptt-access-token>`+
		`<mcptt-client-id type="Encrypted"><EncryptedData xmlns="http://www.w3.org/2001/04/xmlenc#"/></mcptt-client-id>`+
		`</mcptt-Params></mcpttinfo>`))
	signed := sip.NewBodyPart(mcpttinfo.ContentType, []byte(`<mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:1.0"><mcptt-Params>`+
		`<mcptt-access-token type="Normal"><mcpttString>`+token(alice)+`</mcpttString></mcptt-access-token>`+
		`<mcptt-client-id type="Normal"><mcpttString>`+client1+`</mcpttString></mcptt-client-id>`+
		`</mcptt-Params><Signature xmlns="http://www.w3.org/2000/09/xmldsig#"/></mcpttinfo>`))

	for _, tc := range []struct {
		name, impu string
		body       sip.BodyPart
		warning    string
	}{
		{"unknown user", aliceIM, info(token("sip:mallory@mcptt.example.com"), client1), identity.WarnServiceAuthorisationFailed},
		{"IMPU of another user", bobIM, info(token(alice), client1), identity.WarnServiceAuthorisationFailed},
		{"no access token", aliceIM, info("", client1), identity.WarnServiceAuthorisationFailed},
		{"no client ID", aliceIM, info(token(alice), ""), identity.WarnServiceAuthorisationFailed},
		{"not a token", aliceIM, info("opaque", client1), identity.WarnServiceAuthorisationFailed},
		{"encrypted", aliceIM, encrypted, identity.WarnUnableToDecrypt},
		{"client ID encrypted", aliceIM, half, identity.WarnUnableToDecrypt},
		{"integrity protected", aliceIM, signed, identity.WarnIntegrityCheckFailed},
	} {
		res := s.send(s.register(tc.impu, "1", 600, ue.register("sip:ims.example.com", 600000, tc.body)))

		ws, err := res.Header.Warnings()
		if res.StatusCode != 403 || err != nil || len(ws) != 1 || ws[0].Code != 399 || ws[0].Text != tc.warning {
			t.Errorf("%s: %d %v", tc.name, res.StatusCode, ws)
		}

		if v := res.Header.Get("P-Charging-Vector"); !strings.Contains(v, "term-ioi="+domain) {
			t.Errorf("%s: P-Charging-Vector %q", tc.name, v)
		}
	}

	bad := s.register(aliceIM, "1", 600, nil)
	bad.Header.Add("Contact", "<sip:other.ims.example.com>")

	if res := s.send(bad); res.StatusCode != 400 || !strings.Contains(res.Header.Get("P-Charging-Vector"), "term-ioi="+domain) {
		t.Errorf("two Contacts: %d %v", res.StatusCode, res.Header)
	}

	if len(h.s.Registrar().Bindings(uri(t, alice))) != 0 {
		t.Error("bound")
	}
}

// TS 24.379 clause 7.3.2
func TestSecondClient(t *testing.T) {
	h := newHarness(t, registrar.Direct)
	c1, c2 := h.client(aliceIM), h.client(aliceIM)

	if res := c1.send(c1.register("sip:"+domain, 600, info(token(alice), client1))); res.StatusCode != 200 || devices(t, res) {
		t.Fatalf("first client: %d", res.StatusCode)
	}

	res := c2.send(c2.register("sip:"+domain, 600, info(token(alice), client2)))
	if res.StatusCode != 200 || !devices(t, res) {
		t.Fatalf("second client: %d %s", res.StatusCode, res.Body)
	}

	if contacts, _ := res.Header.Contacts(); len(contacts) != 2 {
		t.Errorf("contacts of the address of record %v", contacts)
	}

	if len(h.s.Registrar().Bindings(uri(t, alice))) != 2 || !h.affiliated(alice, client1, fire) || !h.affiliated(alice, client2, fire) {
		t.Fatal("two bindings, affiliated at both")
	}

	if res := c1.send(c1.register("sip:"+domain, 0)); res.StatusCode != 200 {
		t.Fatalf("deregistration: %d", res.StatusCode)
	}

	if h.affiliated(alice, client1, fire) || !h.affiliated(alice, client2, fire) {
		t.Error("the other client logged off")
	}
}

// TS 24.379 clause 9.2.1.2
func (c *client) publish(id, clientID string, expires int, groups ...string) *sip.Request {
	req := siptest.NewRequest("PUBLISH", psi, sip.UDP, c.p.Addr())
	req.Header.Set("From", "<"+c.impu+">;tag="+sip.NewTag())
	req.Header.Set("Event", "presence")
	req.Header.Set("P-Preferred-Service", identity.ICSI)
	req.Header.Set("Expires", fmt.Sprint(expires))

	mi := sip.NewBodyPart(mcpttinfo.ContentType, []byte(`<mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:1.0"><mcptt-Params>`+
		`<mcptt-request-uri type="Normal"><mcpttURI>`+id+`</mcpttURI></mcptt-request-uri></mcptt-Params></mcpttinfo>`))

	if groups == nil {
		req.SetBodies(mi)
		return req
	}

	var b strings.Builder

	b.WriteString(`<presence xmlns="urn:ietf:params:xml:ns:pidf" xmlns:m="urn:3gpp:ns:mcpttPresInfo:1.0" entity="` + id + `">`)
	b.WriteString(`<tuple id="` + clientID + `"><status>`)

	for _, g := range groups {
		b.WriteString(`<m:affiliation group="` + g + `"/>`)
	}

	b.WriteString(`</status></tuple><m:p-id>` + sip.NewTag() + `</m:p-id></presence>`)
	req.SetBodies(mi, sip.NewBodyPart("application/pidf+xml", []byte(b.String())))

	return req
}

// TS 24.379 clause 9.2.2.2.3
// RFC 3903 section 6
func TestAffiliationPublish(t *testing.T) {
	h := newHarness(t, registrar.Direct)
	c := h.client(aliceIM)

	if res := c.send(c.register("sip:"+domain, 600, info(token(alice), client1))); res.StatusCode != 200 {
		t.Fatalf("REGISTER: %d", res.StatusCode)
	}

	res := c.send(c.publish(alice, client1, 4294967295, fire, police))
	if res.StatusCode != 200 || res.Header.Get("Expires") != "4294967295" || res.Header.Get("SIP-ETag") == "" {
		t.Fatalf("PUBLISH: %d %v", res.StatusCode, res.Header)
	}

	if !h.affiliated(alice, client1, fire) || !h.affiliated(alice, client1, police) {
		t.Fatal("not affiliated to both")
	}

	members := h.s.Controlling().Members(h.now(), uri(t, police))
	if len(members) != 1 || members[0].ID.String() != alice || len(members[0].Clients) != 1 {
		t.Errorf("members of police %+v", members)
	}

	if res := c.send(c.publish(alice, client1, 4294967295, police)); res.StatusCode != 200 {
		t.Fatalf("PUBLISH: %d", res.StatusCode)
	}

	if h.affiliated(alice, client1, fire) || !h.affiliated(alice, client1, police) {
		t.Fatal("fire left")
	}

	etag := c.send(c.publish(alice, client1, 4294967295, police)).Header.Get("SIP-ETag")

	end := c.publish(alice, client1, 0)
	end.Header.Set("SIP-If-Match", etag)

	res = c.send(end)
	if res.StatusCode != 200 || res.Header.Get("Expires") != "0" || res.Header.Get("SIP-ETag") == "" {
		t.Fatalf("PUBLISH Expires 0: %d", res.StatusCode)
	}

	if h.affiliated(alice, client1, police) {
		t.Fatal("still affiliated")
	}

	if gs := h.s.Participating().Groups(h.now(), uri(t, alice), client1); len(gs) != 0 {
		t.Errorf("groups %+v", gs)
	}

	stale := c.publish(alice, client1, 0)
	stale.Header.Set("SIP-If-Match", "nope")

	gone := c.publish(alice, client1, 4294967295)
	gone.Header.Set("SIP-If-Match", res.Header.Get("SIP-ETag"))

	other := h.client(bobIM)

	for _, tc := range []struct {
		name string
		req  *sip.Request
		code int
	}{
		{"too brief", c.publish(alice, client1, 3600, fire), 423},
		{"no Expires", without(c.publish(alice, client1, 1, fire), "Expires"), 423},
		{"malformed Expires", with(c.publish(alice, client1, 1, fire), "Expires", "soon"), 400},
		{"unknown entity-tag", stale, 412},
		{"entity-tag of Expires 0", gone, 412},
		{"another user's", other.publish(alice, client1, 4294967295, fire), 403},
		{"unknown user", c.publish("sip:mallory@mcptt.example.com", client1, 4294967295, fire), 404},
		{"another event", with(c.publish(alice, client1, 4294967295, fire), "Event", "poc-settings"), 489},
	} {
		if res := c.send(tc.req); res.StatusCode != tc.code {
			t.Errorf("%s: %d, want %d", tc.name, res.StatusCode, tc.code)
		}
	}

	if res := c.send(c.publish(alice, client1, 4294967295, fire)); res.StatusCode != 200 || !h.affiliated(alice, client1, fire) {
		t.Errorf("PUBLISH: %d", res.StatusCode)
	}

	if res := c.send(c.publish(alice, client2, 4294967295, fire)); res.StatusCode != 403 || h.affiliated(alice, client2, fire) {
		t.Errorf("PUBLISH of a client not registered: %d", res.StatusCode)
	}
}

// TS 24.379 clause 9.2.2.2.3
// TS 24.379 clause 9.2.2.2.15
func TestN2(t *testing.T) {
	h := newHarness(t, registrar.Direct)
	c := h.client(bobIM)

	if res := c.send(c.register("sip:"+domain, 600, info(token(bob), client1))); res.StatusCode != 200 {
		t.Fatalf("REGISTER: %d", res.StatusCode)
	}

	if res := c.send(c.publish(bob, client1, 4294967295, fire, police)); res.StatusCode != 200 {
		t.Fatalf("PUBLISH: %d", res.StatusCode)
	}

	if !h.affiliated(bob, client1, fire) || h.affiliated(bob, client1, police) {
		t.Fatal("N2 not enforced")
	}

	c2 := h.client(bobIM)

	if res := c2.send(c2.register("sip:"+domain, 600, info(token(bob), client2))); res.StatusCode != 200 {
		t.Fatalf("REGISTER: %d", res.StatusCode)
	}

	if res := c2.send(c2.publish(bob, client2, 4294967295, police)); res.StatusCode != 200 {
		t.Fatalf("PUBLISH: %d", res.StatusCode)
	}

	if h.affiliated(bob, client2, police) || h.affiliated(bob, client2, fire) || !h.affiliated(bob, client1, fire) {
		t.Fatal("N2 across clients")
	}
}

func without(req *sip.Request, name string) *sip.Request {
	req.Header.Del(name)
	return req
}

func with(req *sip.Request, name, value string) *sip.Request {
	req.Header.Set(name, value)
	return req
}

// TS 24.229 clause 5.4.3.2
func (s *scscf) forward(req *sip.Request, ids ...string) *sip.Request {
	req.Header.Set("Via", sip.NewVia(sip.UDP, s.p.Addr()).String())

	for _, id := range ids {
		req.Header.Add("P-Asserted-Identity", "<"+id+">")
	}

	return req
}

// TS 24.229 clause 4.4.1
// TS 24.229 clause 5.7.1.4
func TestTrustDomain(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	ue := h.client(aliceIM)
	rogue := &scscf{h: h, p: h.n.Peer()}

	if res := rogue.send(rogue.register(aliceIM, "", 600, ue.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 403 {
		t.Fatalf("third-party REGISTER from outside: %d", res.StatusCode)
	}

	if len(h.s.Registrar().Bindings(uri(t, alice))) != 0 {
		t.Fatal("bound from outside the trust domain")
	}

	if res := s.send(s.register(aliceIM, "1", 600, ue.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("third-party REGISTER: %d", res.StatusCode)
	}

	if res := rogue.send(rogue.forward(ue.publish(alice, client1, 4294967295, police), aliceIM)); res.StatusCode != 403 || h.affiliated(alice, client1, police) {
		t.Fatalf("PUBLISH of an asserted identity from outside: %d", res.StatusCode)
	}

	if res := s.send(s.forward(ue.publish(alice, client1, 4294967295, police))); res.StatusCode != 403 {
		t.Fatalf("PUBLISH without P-Asserted-Identity: %d", res.StatusCode)
	}

	if res := s.send(s.forward(ue.publish(alice, client1, 4294967295, police), aliceIM)); res.StatusCode != 200 || !h.affiliated(alice, client1, police) {
		t.Fatalf("PUBLISH: %d", res.StatusCode)
	}

	bue := h.client(bobTel)

	if res := s.send(s.register(bobTel, "2", 600, bue.register("sip:ims.example.com", 600, info(token(bob), client2)))); res.StatusCode != 200 {
		t.Fatalf("third-party REGISTER of a tel URI: %d", res.StatusCode)
	}

	if res := s.send(s.forward(bue.publish(bob, client2, 4294967295, police), "sip:+15555550102@ims.example.com;user=phone", bobTel)); res.StatusCode != 200 {
		t.Fatalf("PUBLISH of a tel URI: %d", res.StatusCode)
	}

	if h.affiliated(bob, client2, fire) || !h.affiliated(bob, client2, police) {
		t.Error("bob's publication not applied")
	}

	if res := s.send(s.register(bobTel, "", 0, nil)); res.StatusCode != 200 || len(h.s.Registrar().Bindings(uri(t, bob))) != 0 {
		t.Errorf("deregistration of a tel URI: %d", res.StatusCode)
	}
}

// TS 33.180 clause 5.1.3.2.1
// TS 24.379 clause 7.3.2
func TestRebind(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	a, b := h.client(aliceIM), h.client(bobIM)

	if res := s.send(s.register(aliceIM, "1", 600, a.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("alice: %d", res.StatusCode)
	}

	if res := s.send(s.register(bobIM, "2", 600, b.register("sip:ims.example.com", 600, info(token(bob), client1)))); res.StatusCode != 200 {
		t.Fatalf("bob: %d", res.StatusCode)
	}

	if len(h.s.Registrar().Bindings(uri(t, alice))) != 0 || h.affiliated(alice, client1, fire) {
		t.Fatal("alice still at the client")
	}

	if bs := h.s.Registrar().Bindings(uri(t, bob)); len(bs) != 1 || !h.affiliated(bob, client1, fire) {
		t.Fatalf("bob's bindings %+v", bs)
	}

	tel := h.client(bobTel)

	if res := s.send(s.register(bobTel, "3", 600, tel.register("sip:ims.example.com", 600, info(token(bob), client1)))); res.StatusCode != 200 {
		t.Fatalf("bob at his tel URI: %d", res.StatusCode)
	}

	bs := h.s.Registrar().Bindings(uri(t, bob))
	if len(bs) != 1 || !bs[0].IMPU.Equivalent(uri(t, bobTel)) || !h.affiliated(bob, client1, fire) {
		t.Fatalf("bob's bindings %+v", bs)
	}
}

// TS 24.229 clause 5.7.1.3B
// TS 24.229 clause 5.4.1.7
func TestTwoUEs(t *testing.T) {
	for _, tokens := range []bool{true, false} {
		t.Run(fmt.Sprint("tokens ", tokens), func(t *testing.T) {
			h := newHarness(t, registrar.ThroughIMS)
			s := h.scscf

			ues := []*client{h.client(aliceIM), h.client(aliceIM), h.client(aliceIM)}
			toks := []string{"", "", ""}

			for i, ue := range ues {
				ue.instance = fmt.Sprintf("urn:uuid:00000000-0000-1000-8000-00000000000%d", i+1)
				if tokens {
					toks[i] = fmt.Sprint("t", i+1)
				}
			}

			for i, c := range []string{client1, client2} {
				if res := s.send(s.register(aliceIM, toks[i], 600, ues[i].register("sip:ims.example.com", 600, info(token(alice), c)))); res.StatusCode != 200 {
					t.Fatalf("UE %d: %d", i, res.StatusCode)
				}
			}

			b, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client2)
			if !ok || b.Token != toks[1] {
				t.Fatalf("binding %+v", b)
			}

			h.n.Advance(500 * time.Second)

			if res := s.send(s.register(aliceIM, toks[1], 600, ues[1].register("sip:ims.example.com", 600))); res.StatusCode != 200 {
				t.Fatalf("refresh: %d", res.StatusCode)
			}

			h.n.Advance(101 * time.Second)

			if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); ok || h.affiliated(alice, client1, fire) {
				t.Fatal("the other UE's binding refreshed")
			}

			if !h.affiliated(alice, client2, fire) {
				t.Fatal("the refreshed binding expired")
			}

			plain := ues[2].register("sip:ims.example.com", 600)
			plain.Header.Set("Contact", fmt.Sprintf(`<sip:ue@%s>;+sip.instance="<%s>"`, ues[2].p.Addr(), ues[2].instance))

			if res := s.send(s.register(aliceIM, toks[2], 600, plain)); res.StatusCode != 200 || !h.affiliated(alice, client2, fire) {
				t.Fatalf("a UE without MCPTT: %d", res.StatusCode)
			}

			off := ues[1].register("sip:ims.example.com", 600)
			off.Header.Set("Contact", fmt.Sprintf(`<sip:ue@%s>;+sip.instance="<%s>"`, ues[1].p.Addr(), ues[1].instance))

			if res := s.send(s.register(aliceIM, toks[1], 600, off)); res.StatusCode != 200 || h.affiliated(alice, client2, fire) {
				t.Fatalf("log off: %d", res.StatusCode)
			}
		})
	}
}

// TS 24.229 clause 5.4.1.7A
func TestSingleBody(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	ue := h.client(aliceIM)

	req := s.register(aliceIM, "", 600, nil)
	req.Header.Del("Expires")
	req.Header.Set("Contact", s.contact("").String()+";expires=300")
	req.SetBody("message/sip", ue.register("sip:ims.example.com", 600, info(token(alice), client1)).Bytes())

	res := s.send(req)
	if res.StatusCode != 200 || res.Header.Get("Expires") != "300" {
		t.Fatalf("REGISTER: %d Expires %q", res.StatusCode, res.Header.Get("Expires"))
	}

	if b, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok || !b.Expires.Equal(h.now().Add(300*time.Second)) {
		t.Fatalf("binding %+v", b)
	}
}

// RFC 3680 section 5.1
func terminated(id, instance, callID string, cseq int) string {
	return terminatedFlow(id, instance, "", callID, cseq)
}

func terminatedFlow(id, instance, regID, callID string, cseq int) string {
	v := `<contact id="` + id + `" state="terminated" event="unregistered"`
	if callID != "" {
		v += fmt.Sprintf(` callid="%s" cseq="%d"`, callID, cseq)
	}

	v += `><uri>sip:ue@192.0.2.9</uri>`
	if instance != "" {
		v += `<unknown-param name="+sip.instance">"&lt;` + instance + `&gt;"</unknown-param>`
	}

	if regID != "" {
		v += `<unknown-param name="reg-id">` + regID + `</unknown-param>`
	}

	return v + `</contact>`
}

func active(contacts string) string {
	return `<registration aor="` + aliceIM + `" id="r1" state="active">` + contacts + `</registration>`
}

// TS 24.229 clause 5.7.1.1
// TS 24.229 clause 5.4.1.7
func TestRegEvent(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	s.regEvent = true

	ue1, ue2 := h.client(aliceIM), h.client(aliceIM)
	ue1.instance = "urn:uuid:00000000-0000-1000-8000-000000000001"

	if res := s.send(s.register(aliceIM, "t1", 600, ue1.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("UE 1: %d", res.StatusCode)
	}

	s.pump()

	if len(s.subs) != 1 || s.subs[aliceIM] == nil || len(s.notified) != 1 || s.notified[0].StatusCode != 200 ||
		!strings.Contains(s.notified[0].Header.Get("P-Charging-Vector"), "icid-value=77;orig-ioi=ims.example.com;term-ioi="+domain) {
		t.Fatalf("subscriptions %v, NOTIFY responses %v", s.subs, s.notified)
	}

	if res := s.send(s.register(aliceIM, "t2", 600, ue2.register("sip:ims.example.com", 600, info(token(alice), client2)))); res.StatusCode != 200 {
		t.Fatalf("UE 2: %d", res.StatusCode)
	}

	if res := s.send(s.register(aliceIM, "", 0, nil)); res.StatusCode != 200 || len(h.s.Registrar().Bindings(uri(t, alice))) != 2 {
		t.Fatalf("deregistration: %d", res.StatusCode)
	}

	s.notify(aliceIM, 1, "partial", active(terminated("c9", ue1.instance, "", 0)))

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); ok || h.affiliated(alice, client1, fire) || !h.affiliated(alice, client2, fire) {
		t.Fatal("UE 1 not alone deregistered")
	}

	s.notify(aliceIM, 1, "partial", active(terminated("t2", "", "", 0)))

	if !h.affiliated(alice, client2, fire) {
		t.Fatal("document of an old version applied")
	}

	if res := s.send(s.register(aliceIM, "t3", 600, ue1.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("UE 1 again: %d", res.StatusCode)
	}

	s.notify(aliceIM, 2, "partial", active(terminated("c9", ue1.instance, ue1.callID, ue1.cseq-1)))

	if !h.affiliated(alice, client1, fire) {
		t.Fatal("UE 1 deregistered by its former contact")
	}

	s.notify(aliceIM, 3, "partial", active(terminated("t2", "", "", 0)))

	if !h.affiliated(alice, client1, fire) || h.affiliated(alice, client2, fire) {
		t.Fatal("UE 2 not alone deregistered")
	}

	s.notify(aliceIM, 4, "full", `<registration aor="`+aliceIM+`" id="r1" state="terminated"/>`)

	if len(h.s.Registrar().Bindings(uri(t, alice))) != 0 {
		t.Fatal("bound after the registration terminated")
	}

	s.pump()

	if len(s.unsubscribed) != 1 || s.unsubscribed[0] != aliceIM {
		t.Errorf("unsubscribed %v", s.unsubscribed)
	}
}

// TS 24.229 clause 5.4.1.7
func TestDeregistrationFallback(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	ue1, ue2 := h.client(aliceIM), h.client(aliceIM)

	if res := s.send(s.register(aliceIM, "t1", 600, ue1.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("UE 1: %d", res.StatusCode)
	}

	if res := s.send(s.register(aliceIM, "t2", 600, ue2.register("sip:ims.example.com", 600, info(token(alice), client2)))); res.StatusCode != 200 {
		t.Fatalf("UE 2: %d", res.StatusCode)
	}

	s.pump()

	if res := s.send(s.register(aliceIM, "", 0, nil)); res.StatusCode != 200 || len(h.s.Registrar().Bindings(uri(t, alice))) != 0 {
		t.Fatalf("deregistration: %d", res.StatusCode)
	}

	if h.affiliated(alice, client1, fire) || h.affiliated(alice, client2, fire) {
		t.Error("affiliated after the deregistration")
	}
}
