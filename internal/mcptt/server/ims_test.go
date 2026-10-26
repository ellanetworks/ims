package server_test

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/as"
	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/server"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/client/clienttest"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/mcptt/media/mediatest"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// core is a stand-in IM CN subsystem: one S-CSCF, P-CSCF of its UEs too, on sip/proxy, as a standard core behaves
// towards an AS, with the variance of the cores the server meets, by UE (imsUE). It sends the third-party REGISTERs
// of its UEs (TS 24.229 §5.4.1.7, §5.4.1.7A), of their registration tokens (§5.4.1.7 l) or none, the implicit
// registration set in the P-Associated-URI of the 200 to the UE's REGISTER (§5.4.1.2.2F b) or, as Kamailio, of the
// third-party REGISTER itself; its Contact an address, or, as Kamailio, its host name without a port, which the
// server resolves by its SRV records (RFC 3263 §4.2). It asserts the originating requests of a UE (§5.4.3.2 step 1)
// by its default public user identity, which need not be the To of its third-party REGISTER, and sends them to the
// AS over ISC by the originating iFC of MCPTT, with the AS's URI and an original dialog identifier in the Route,
// P-Served-User, a P-Charging-Vector, and the token in Feature-Caps if any (§5.4.3.2 step 5); a request of the AS
// without "orig" it serves as terminating, to the registered contact of its Request-URI (§5.4.3.3), the AS's
// terminating iFC not matching. It record-routes the initial INVITEs, and resolves the PSI domain to the server, as
// RFC 3263 would. It accepts the reg event subscriptions of the AS (§5.4.2.1.1), with a NOTIFY of the registrations
// of the user (§5.4.2.1.2), or, as Kamailio, refuses them (403); a network-initiated deregistration
// (§5.4.1.5) has it send a NOTIFY ending the subscription (RFC 6665 §4.4.1) and a third-party REGISTER of Expires 0
// (§5.4.1.7 f).
type core struct {
	t     *testing.T
	l     *transaction.Layer
	p     *proxy.Proxy
	addr  netip.AddrPort
	as    netip.AddrPort
	hosts map[string]netip.AddrPort

	mu sync.Mutex
	// ues are the registered UEs by contact address and by public user identity; fromAS and toAS the requests the
	// S-CSCF received from the AS and sent it, as they came and went; subs the reg event subscriptions of the AS, by
	// Call-ID, subscribed their responses.
	ues          map[netip.AddrPort]*imsUE
	impus        map[string]*imsUE
	fromAS, toAS []*sip.Request
	subs         map[string]*regSub
	subscribed   []*sip.Response
	icid         int

	// forge is the registration token of the UEs' requests to the AS, if set: of another registration.
	forge string
}

// scscfHost is the host name of the S-CSCF, without a port in the Contact of its third-party REGISTERs as Kamailio's.
const scscfHost = "scscf.ims.example.com"

// imsUE is a UE as the core registered it: the To of its third-party REGISTER, its implicit registration set of the
// P-Associated-URI, its default public user identity first, which the core asserts (TS 24.229 §5.2.6.3.1); its
// token; kamailio, the core is Kamailio's for it (no token, the set in the third-party REGISTER, its Contact by host
// name, the reg event subscription refused).
type imsUE struct {
	impu     string
	set      []string
	token    string
	contact  sip.URI
	kamailio bool
}

// regSub is a reg event subscription of the AS: of a UE, its dialog.
type regSub struct {
	ue                *imsUE
	callID, tag, from string
	target            sip.URI
	cseq              int
}

func newCore(t *testing.T, n *siptest.Net) *core {
	c := &core{
		t: t, hosts: map[string]netip.AddrPort{}, ues: map[netip.AddrPort]*imsUE{}, impus: map[string]*imsUE{},
		subs: map[string]*regSub{},
	}

	c.l = transaction.New(n.Config(transaction.Config{Handler: c, Aliases: []string{scscfHost}}))

	t.Cleanup(func() { _ = c.l.Close() })

	c.addr = n.Serve(c.l)
	c.p = proxy.New(proxy.Config{Layer: c.l, Port: c.addr.Port(), Clock: n.Clock()})

	return c
}

func (c *core) uri() sip.URI {
	return sip.URI{Scheme: "sip", Host: c.addr.Addr().String(), Port: c.addr.Port()}
}

// asURI is the URI of the AS in the iFC, the Request-URI of the third-party REGISTERs (TS 24.229 §5.4.1.7 a).
func (c *core) asURI() string { return "sip:" + c.as.String() }

func (c *core) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	if res := c.p.Check(req); res != nil {
		_ = tx.Respond(res)
		return
	}

	if req.Method == "SUBSCRIBE" {
		c.subscribe(tx, req)
		return
	}

	out, removed, err := c.p.Preprocess(req)
	if err != nil {
		c.t.Error(err)
		return
	}

	c.mu.Lock()
	ue, forge := c.ues[req.Flow.Remote], c.forge
	fromAS := req.Flow.Remote == c.as

	if fromAS {
		c.fromAS = append(c.fromAS, req.Clone())
	}
	c.mu.Unlock()

	to, _ := out.Header.To()

	var opts proxy.Options

	switch {
	case to.Tag() != "":
	case fromAS:
		c.mu.Lock()
		callee := c.impus[settings.Key(out.URI)]
		c.mu.Unlock()

		if callee == nil || out.Header.Has("Route") || slices.ContainsFunc(removed, func(u sip.URI) bool { return u.Params.Has("orig") }) {
			_ = tx.Respond(sip.NewResponse(req, 404, ""))
			return
		}

		out.URI = callee.contact
		opts.RecordRoute = &proxy.RecordRoute{User: "mt"}
	case ue != nil:
		odi := c.uri()
		odi.User = "odi-1"
		odi.Params.Set("lr", "")

		as := sip.URI{Scheme: "sip", Host: c.as.Addr().String(), Port: c.as.Port()}
		as.Params.Set("lr", "")

		c.mu.Lock()
		c.icid++
		icid := c.icid
		c.mu.Unlock()

		out.Header.Del("P-Preferred-Identity")
		out.Header.Del("Route")
		out.Header.Set("P-Asserted-Identity", "<"+ue.set[0]+">")
		out.Header.Set("P-Served-User", "<"+ue.impu+">;sescase=orig;regstate=reg")
		out.Header.Set("P-Charging-Vector", fmt.Sprintf("icid-value=core%d;orig-ioi=ims.example.com", icid))

		token := ue.token
		if forge != "" {
			token = forge
		}

		if token != "" {
			out.Header.Set("Feature-Caps", `*;+g.3gpp.registration-token="`+token+`"`)
		}

		out.Header.Prepend("Route", sip.Address{URI: odi}.String())
		out.Header.Prepend("Route", sip.Address{URI: as}.String())

		opts.RecordRoute = &proxy.RecordRoute{User: "mo"}
	default:
		_ = tx.Respond(sip.NewResponse(req, 403, ""))
		return
	}

	next := c.target(out)
	if next.Flow.Remote == c.as {
		c.mu.Lock()
		c.toAS = append(c.toAS, out.Clone())
		c.mu.Unlock()
	}

	if err := c.p.Forward(tx, out, next, opts); err != nil {
		c.t.Errorf("forwarding %s: %v", out.StartLine(), err)
	}
}

// subscribe answers a SUBSCRIBE of the AS to the reg event package (TS 24.229 §5.4.2.1.1), after checking it is as
// §5.7.1.1 a to f has it: within the dialog a refresh, or its end of Expires 0, a 481 once the subscription ended.
func (c *core) subscribe(tx *transaction.ServerTransaction, req *sip.Request) {
	to, _ := req.Header.To()
	from, _ := req.Header.From()
	pai, _ := req.Header.Addresses("P-Asserted-Identity")

	if ev := req.Header.Get("Event"); ev != "reg" || from.URI.String() != c.asURI() || len(pai) != 1 || pai[0].URI.String() != c.asURI() ||
		!strings.Contains(req.Header.Get("P-Charging-Vector"), "orig-ioi="+domain) || to.Tag() == "" && !to.URI.IsSIP() {
		c.t.Errorf("SUBSCRIBE %s", req)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	respond := func(code int, tag string) {
		res := sip.NewResponse(req, code, "")
		_ = res.Header.SetToTag(tag)

		if code == 200 {
			res.Header.Set("Expires", "3761")
			res.Header.Set("Contact", sip.Address{URI: c.uri()}.String())
		}

		c.subscribed = append(c.subscribed, res)
		_ = tx.Respond(res)
	}

	if to.Tag() != "" {
		sub := c.subs[req.Header.CallID()]
		if sub == nil {
			respond(481, to.Tag())
			return
		}

		respond(200, sub.tag)

		if req.Header.Get("Expires") == "0" {
			delete(c.subs, sub.callID)
			c.notify(sub, "terminated;reason=timeout", "terminated")
		}

		return
	}

	ue := c.impus[settings.Key(req.URI)]
	if ue == nil || ue.kamailio {
		respond(403, sip.NewTag())
		return
	}

	contacts, _ := req.Header.Contacts()
	sub := &regSub{ue: ue, callID: req.Header.CallID(), tag: sip.NewTag(), from: req.Header.Get("From"), target: contacts[0].URI}
	c.subs[sub.callID] = sub

	respond(200, sub.tag)
	c.notify(sub, "active;expires=3761", "active")
}

// notify sends the AS a NOTIFY of sub, of Subscription-State state, a full document of the registrations of the
// public user identities of the UE, each of state, the UE's contact in it (TS 24.229 §5.4.2.1.2 steps 3, 4). Called
// with c.mu held.
func (c *core) notify(sub *regSub, state, regState string) {
	sub.cseq++

	contact := "active"
	if regState != "active" {
		contact = "terminated"
	}

	var b strings.Builder

	fmt.Fprintf(&b, `<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="%d" state="full">`, sub.cseq-1)

	for i, impu := range sub.ue.set {
		fmt.Fprintf(&b, `<registration aor="%s" id="r%d" state="%s"><contact id="c%d" state="%s" event="registered"><uri>%s</uri></contact></registration>`,
			impu, i, regState, i, contact, sub.ue.contact)
	}

	b.WriteString(`</reginfo>`)

	n := siptest.NewRequest("NOTIFY", sub.target.String(), sip.UDP, c.addr)
	n.Header.Set("From", "<"+sub.ue.impu+">;tag="+sub.tag)
	n.Header.Set("To", sub.from)
	n.Header.Set("Call-ID", sub.callID)
	n.Header.Set("CSeq", fmt.Sprintf("%d NOTIFY", sub.cseq))
	n.Header.Set("Event", "reg")
	n.Header.Set("Subscription-State", state)
	n.Header.Set("Contact", sip.Address{URI: c.uri()}.String())
	n.SetBody("application/reginfo+xml", []byte(b.String()))
	n.Flow = sip.Flow{Transport: sip.UDP, Local: c.addr, Remote: c.as}

	if _, err := c.l.Request(n, &final{}); err != nil {
		c.t.Error(err)
	}
}

func (c *core) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	c.p.Cancel(tx, cancel)
}

func (c *core) HandleAck(ack *sip.Request) {
	out, removed, err := c.p.Preprocess(ack)
	if err != nil {
		c.t.Error(err)
		return
	}

	_ = c.p.ForwardAck(out, c.target(out), c.p.Dialog(removed))
}

func (c *core) HandleTransactionError(*transaction.ServerTransaction, error) {}

// target is the next hop of out: its top Route, or else its Request-URI, a domain of hosts resolved.
func (c *core) target(out *sip.Request) proxy.Target {
	u := out.URI
	if r, err := out.Header.TopRoute(); err == nil {
		u = r.URI
	}

	tr, to, err := sip.Destination(u)
	if a, ok := c.hosts[strings.ToLower(u.Host)]; ok {
		tr, to, err = sip.UDP, a, nil
	}

	if err != nil {
		c.t.Errorf("no next hop of %s: %v", out.StartLine(), err)
	}

	return proxy.Target{Flow: sip.Flow{Transport: tr, Local: c.addr, Remote: to}}
}

// register registers the UE of u, of the registration token, none as Kamailio's, and sends the AS its third-party
// REGISTER, with the REGISTER of the UE, logging on to MCPTT if logon (its mcptt-info), and its 200 (TS 24.229
// §5.4.1.7, §5.4.1.7A); the final response of the AS.
func (c *core) register(u *imsUser, token string, logon bool) *sip.Response {
	c.t.Helper()

	contact := identity.WithFeatures(sip.Address{URI: u.contact}, client.Features())

	reg := siptest.NewRequest("REGISTER", "sip:ims.example.com", sip.UDP, u.addr)
	reg.Header.Set("From", "<"+u.impu+">;tag="+sip.NewTag())
	reg.Header.Set("To", "<"+u.impu+">")
	reg.Header.Set("Contact", contact.String())
	reg.Header.Set("Expires", "600000")

	if logon {
		reg.SetBodies(u.RegisterBodies()...)
	}

	var pau []string
	for _, a := range u.set {
		pau = append(pau, "<"+a+">")
	}

	ok := sip.NewResponse(reg, 200, "")
	ok.Header.Set("Contact", contact.String()+";expires=3600")
	ok.Header.Set("P-Associated-URI", strings.Join(pau, ", "))

	scscf := sip.Address{URI: c.uri()}
	if u.kamailio {
		scscf = sip.Address{URI: sip.URI{Scheme: "sip", Host: scscfHost}}
	} else {
		p, _ := sip.NewStringFeature("+g.3gpp.registration-token", token).Param()
		scscf.Params = append(scscf.Params, p)
	}

	req := siptest.NewRequest("REGISTER", c.asURI(), sip.UDP, c.addr)
	req.Header.Set("From", "<sip:scscf.ims.example.com>;tag="+sip.NewTag())
	req.Header.Set("To", "<"+u.impu+">")
	req.Header.Set("Contact", scscf.String())
	req.Header.Set("Expires", "3600")
	req.Header.Set("P-Charging-Vector", "icid-value=4a2f;orig-ioi=ims.example.com")

	if u.kamailio {
		// Kamailio sends one body, the UE's REGISTER, and the implicit set in a header field of its own.
		req.Header.Set("P-Associated-URI", strings.Join(pau, ", "))
		req.SetBody("message/sip", reg.Bytes())
	} else {
		req.SetBodies(sip.NewBodyPart("message/sip", reg.Bytes()), sip.NewBodyPart("message/sip", ok.Bytes()))
	}

	req.Flow = sip.Flow{Transport: sip.UDP, Local: c.addr, Remote: c.as}

	ue := &imsUE{impu: u.impu, set: u.set, token: token, contact: u.contact, kamailio: u.kamailio}

	c.mu.Lock()
	c.ues[u.addr] = ue

	for _, a := range u.set {
		c.impus[settings.Key(mustURI(c.t, a))] = ue
	}
	c.mu.Unlock()

	return c.send(u.w.n, req)
}

// deregister deregisters the UE of u by the network (TS 24.229 §5.4.1.5): the NOTIFY of its registrations terminated
// ending the subscriptions of the AS (§5.4.2.1.2; RFC 6665 §4.4.1), then the third-party REGISTER of Expires 0, without
// body or token (§5.4.1.7 f); the final response of the AS.
func (c *core) deregister(u *imsUser) *sip.Response {
	c.t.Helper()

	c.mu.Lock()
	ue := c.ues[u.addr]
	delete(c.ues, u.addr)

	for _, a := range u.set {
		delete(c.impus, settings.Key(mustURI(c.t, a)))
	}

	for id, sub := range c.subs {
		if sub.ue == ue {
			delete(c.subs, id)
			c.notify(sub, "terminated;reason=noresource", "terminated")
		}
	}
	c.mu.Unlock()

	req := siptest.NewRequest("REGISTER", c.asURI(), sip.UDP, c.addr)
	req.Header.Set("From", "<sip:scscf.ims.example.com>;tag="+sip.NewTag())
	req.Header.Set("To", "<"+u.impu+">")
	req.Header.Set("Contact", sip.Address{URI: c.uri()}.String())
	req.Header.Set("Expires", "0")
	req.Flow = sip.Flow{Transport: sip.UDP, Local: c.addr, Remote: c.as}

	return c.send(u.w.n, req)
}

// send sends req to the AS, and returns its final response.
func (c *core) send(n *siptest.Net, req *sip.Request) *sip.Response {
	c.t.Helper()

	h := &final{}
	if _, err := c.l.Request(req, h); err != nil {
		c.t.Fatal(err)
	}

	n.Quiesce()

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.res == nil {
		c.t.Fatalf("no response to %s", req.StartLine())
	}

	return h.res
}

type final struct {
	mu  sync.Mutex
	res *sip.Response
}

func (f *final) HandleResponse(res *sip.Response) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if res.StatusCode >= 200 {
		f.res = res
	}
}

func (f *final) HandleError(error) {}

// requests are the requests of method the S-CSCF received from the AS, and sent it, since the last call.
func (c *core) requests(method string) (from, to []*sip.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	pick := func(rs []*sip.Request) []*sip.Request {
		var out []*sip.Request

		for _, r := range rs {
			if r.Method == method {
				out = append(out, r)
			}
		}

		return out
	}

	from, to = pick(c.fromAS), pick(c.toAS)
	c.fromAS, c.toAS = nil, nil

	return from, to
}

// subscriptions are the responses of the S-CSCF to the SUBSCRIBEs of the AS, since the last call.
func (c *core) subscriptions() []*sip.Response {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := c.subscribed
	c.subscribed = nil

	return out
}

// dns has the S-CSCF's host name at its address, by its SRV record over UDP (RFC 3263 §4.2).
type dns struct{ core netip.AddrPort }

func (d dns) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	if service == "sip" && proto == "udp" && name == scscfHost {
		return "", []*net.SRV{{Target: "scscf1.ims.example.com.", Port: d.core.Port()}}, nil
	}

	return "", nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (d dns) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if host == "scscf1.ims.example.com" {
		return []netip.Addr{d.core.Addr()}, nil
	}

	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// imsWorld is the server through the IMS: the core, and the clients on UEs behind it.
type imsWorld struct {
	t     *testing.T
	n     *siptest.Net
	m     *mediatest.Net
	s     *server.Server
	addr  netip.AddrPort
	core  *core
	users map[string]*imsUser
}

// imsUser is a client on a UE behind the core, which is its P-CSCF: its requests go there, completed as testue's
// UE.Originate does (TS 24.229 §5.1.2A.1.1: the Route of its Service-Route, P-Preferred-Identity). impu is the
// public user identity it registers, set its implicit registration set, its default public user identity first;
// kamailio, the core behaves as Kamailio's towards the AS for it.
type imsUser struct {
	*user

	w        *imsWorld
	impu     string
	set      []string
	kamailio bool
	contact  sip.URI
}

// newIMSWorld is the server through the core, which serves alice as Ella IMS does, her default public user identity a
// tel URI and not the To of her third-party REGISTER; bob as Kamailio does; carol as Ella IMS does, of one identity.
func newIMSWorld(t *testing.T) *imsWorld {
	t.Helper()

	n := siptest.NewNet(t)
	w := &imsWorld{t: t, n: n, m: mediatest.New(n.Scheduler()), addr: n.Addr(), users: map[string]*imsUser{}}
	w.core = newCore(t, n)
	w.core.as = w.addr
	w.core.hosts[domain] = w.addr

	snap, err := settings.New(callConfiguration())
	if err != nil {
		t.Fatal(err)
	}

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
			Prepare:     as.Prepare(context.Background(), []netip.AddrPort{w.addr}, &sip.Resolver{DNS: dns{w.core.addr}}),
		},
		Source:          source{snap},
		Mode:            registrar.ThroughIMS,
		TrustedNetworks: []netip.Prefix{netip.PrefixFrom(w.core.addr.Addr(), w.core.addr.Addr().BitLen())},
		Media:           relay,
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

	w.user("alice", 1, "tel:+15555550101", false)
	w.user("bob", 2, "", true)
	w.user("carol", 3, "", false)

	return w
}

// user registers the user of name, its default public user identity alias if not "", the core Kamailio's for it if
// kamailio.
func (w *imsWorld) user(name string, i int, alias string, kamailio bool) {
	t := w.t
	u := &imsUser{
		user: &user{name: name, id: "sip:" + name + "@" + domain, addr: w.n.Addr()}, w: w, impu: "sip:" + name + "@ims.example.com",
		kamailio: kamailio,
	}
	u.contact = sip.URI{Scheme: "sip", User: name, Host: u.addr.Addr().String(), Port: u.addr.Port()}

	u.set = []string{u.impu}
	if alias != "" {
		u.set = []string{alias, u.impu}
	}

	w.users[name] = u

	a, err := ua.New(ua.Config{
		Transaction: w.n.Config(transaction.Config{}),
		Contact:     sip.Address{URI: u.contact},
		Prepare: func(req *sip.Request) error {
			req.Flow = sip.Flow{Transport: sip.UDP, Local: u.addr, Remote: w.core.addr}
			req.Header.Prepend("Via", sip.NewVia(sip.UDP, u.addr).String())

			return nil
		},
		OnInvite: func(r *ua.Request) ua.Handler { return u.OnInvite(r) },
	})
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

	c, err := client.New(client.Config{
		Agent: a,
		Originate: func(req *sip.Request) error {
			route := w.core.uri()
			route.Params.Set("lr", "")
			req.Header.Set("Route", sip.Address{URI: route}.String())
			req.Header.Set("From", "<"+u.impu+">")
			req.Header.Set("P-Preferred-Identity", "<"+u.set[0]+">")

			return nil
		},
		IMPU:             mustURI(t, u.impu),
		MCPTTID:          mustURI(t, u.id),
		ClientID:         fmt.Sprintf("urn:uuid:00000000-0000-1000-8000-%012d", i),
		Contact:          client.FixedContact(sip.Address{URI: u.contact}),
		ParticipatingPSI: mustURI(t, psi),
		Media:            w.m,
		Addr:             netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}),
		MinPort:          30000,
		MaxPort:          30099,
		Floor:            floorTimers,
		OnEvent: func(e client.Event) {
			speaker.Event(e)

			u.mu.Lock()
			u.events = append(u.events, e)
			u.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	u.Client = c

	token := fmt.Sprint(10 + i)
	if kamailio {
		token = ""
	}

	if res := w.core.register(u, token, true); res.StatusCode != 200 {
		t.Fatalf("%s: third-party REGISTER %d", name, res.StatusCode)
	}
}

// TS 24.229 §5.4.1.7, §5.7.1.1, §5.7.3, §5.7.2; TS 24.379 §10.1.1.3.1.1, §6.3.2.2.5.2, §6.3.2.2.3: through the IMS,
// alice's INVITE reaches the participating function over ISC, her MCPTT ID of the binding of the implicit
// registration set her asserted identity, her default one, is of; the INVITEs of the members go to the S-CSCF of
// their third-party REGISTERs, bob's by its host name and SRV records (RFC 3263 §4.2), to their IMPU, the
// participating PSI asserted, without "orig", the ICSI asserted and a P-Charging-Vector of the server's domain. The
// 200 to alice has the P-Charging-Vector of a terminating UA (§5.7.2). The server subscribes to the reg event
// package of alice and carol (§5.7.1.1), bob's refused as Kamailio does. The requests of the dialogs follow the
// Record-Route of the S-CSCF: bob's BYE reaches the server, and the server's BYE reaches carol, of her INVITE's
// icid-value (§5.7.3), once alice, the initiator, leaves police.
func TestThroughIMS(t *testing.T) {
	w := newIMSWorld(t)
	alice, bob, carol := w.users["alice"], w.users["bob"], w.users["carol"]

	subs := w.core.subscriptions()
	codes := map[int]int{}

	for _, res := range subs {
		codes[res.StatusCode]++
	}

	if codes[200] != 2 || codes[403] != 1 || len(subs) != 3 {
		t.Errorf("reg event subscriptions: %v", codes)
	}

	ca, err := alice.Call(mustURI(t, police))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	es := alice.expect(t, "established")

	ok := event(es, client.EventEstablished).Response
	if ok == nil || ok.StatusCode != 200 {
		t.Fatalf("200 of alice %+v", ok)
	}

	if v := ok.Header.Get("P-Charging-Vector"); !strings.HasSuffix(v, ";orig-ioi=ims.example.com;term-ioi="+domain) || ok.Header.ICID() == "" {
		t.Errorf("P-Charging-Vector of the 200 %q", v)
	}

	// The ICSI the originating P-CSCF gives the policy function (TS 29.514 Annex B.1 NOTE 3), percent-encoded.
	if caps, err := ok.Header.FeatureCaps(); err != nil || len(caps) != 1 ||
		!slices.Equal(caps[0].Tokens("g.3gpp.icsi-ref"), []string{identity.ICSI}) ||
		ok.Header.Get("Feature-Caps") != `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt"` {
		t.Errorf("Feature-Caps of the 200 %q", ok.Header.Values("Feature-Caps"))
	}

	invites, isc := w.core.requests("INVITE")
	if len(isc) != 1 || isc[0].Header.Get("P-Asserted-Identity") != "<tel:+15555550101>" || isc[0].Header.ICID() != ok.Header.ICID() {
		t.Fatalf("INVITEs to the AS %v", isc)
	}

	if len(invites) != 2 {
		t.Fatalf("%d INVITEs of the AS", len(invites))
	}

	var called []string

	icids := map[string]string{}

	for _, inv := range invites {
		called = append(called, inv.URI.String())
		icids[inv.URI.String()] = inv.Header.ICID()

		scscf := w.core.addr.String()
		if inv.URI.String() == bob.impu {
			scscf = scscfHost
		}

		routes, _ := inv.Header.Routes()
		pai, _ := inv.Header.Addresses("P-Asserted-Identity")
		services, _ := inv.Header.AssertedServices()

		switch {
		case len(routes) != 1 || !routes[0].URI.IsLooseRouter() || routes[0].URI.Params.Has("orig") || routes[0].URI.HostPort() != scscf:
			t.Errorf("Route %v", inv.Header.Values("Route"))
		case len(pai) != 1 || pai[0].URI.String() != psi:
			t.Errorf("P-Asserted-Identity %v", inv.Header.Values("P-Asserted-Identity"))
		case !slices.Equal(services, []string{identity.ICSI}):
			t.Errorf("P-Asserted-Service %v", services)
		case !strings.Contains(inv.Header.Get("P-Charging-Vector"), "orig-ioi="+domain) || inv.Header.ICID() == "":
			t.Errorf("P-Charging-Vector %q", inv.Header.Get("P-Charging-Vector"))
		}
	}

	slices.Sort(called)

	if !slices.Equal(called, []string{bob.impu, carol.impu}) {
		t.Errorf("Request-URIs %v", called)
	}

	var cb *client.Call

	for _, u := range []*imsUser{bob, carol} {
		es := u.expect(t, "incoming", "established")
		if u == bob {
			cb = event(es, client.EventIncoming).Call
		}
	}

	// The floor through the media plane.
	ca.PressPTT()
	w.n.Advance(time.Second)
	alice.expect(t, "floor:granted")
	bob.expectAll(t, "floor:taken", "heard")
	ca.ReleasePTT()
	w.n.Advance(time.Second)

	// A member's BYE, through the S-CSCF to the session identity, which its domain resolves.
	w.core.requests("BYE")
	cb.Leave()
	w.n.Quiesce()
	bob.expect(t, "ended")
	alice.expectNot(t, "ended")

	if _, byes := w.core.requests("BYE"); len(byes) != 1 || !byes[0].URI.Params.Has("gr") {
		t.Fatalf("BYEs to the AS %v", byes)
	}

	// The initiator leaving releases police (TS 24.379 §6.3.8.1 3): the server's BYE to carol goes to the S-CSCF
	// on the route of her dialog.
	w.core.requests("BYE")
	ca.Leave()
	w.n.Quiesce()

	alice.expect(t, "ended")
	carol.expect(t, "ended")

	byes, _ := w.core.requests("BYE")
	if len(byes) != 1 {
		t.Fatalf("%d BYEs of the AS", len(byes))
	}

	if routes, _ := byes[0].Header.Routes(); len(routes) == 0 || routes[0].URI.HostPort() != w.core.addr.String() {
		t.Errorf("BYE of the AS: Route %v", byes[0].Header.Values("Route"))
	}

	if v := byes[0].Header.Get("P-Charging-Vector"); v != "icid-value="+icids[carol.impu]+";orig-ioi="+domain {
		t.Errorf("BYE of the AS: P-Charging-Vector %q", v)
	}
}

// TS 24.229 §4.4.1, §5.7.1.4, §5.7.1.3B; TS 24.379 §10.1.1.3.1.1 step 2a: through the IMS, a request from outside the
// trust domain gets a 403, whatever it asserts, an initial INVITE or REGISTER as a request within a dialog; an
// INVITE of the S-CSCF of a registration token not the binding's is not of the client's registration: its user is
// unknown.
func TestThroughIMSUntrusted(t *testing.T) {
	w := newIMSWorld(t)
	p := w.n.Peer()

	for _, tc := range []struct{ method, toTag string }{{"INVITE", ""}, {"REGISTER", ""}, {"BYE", "1"}, {"INVITE", "1"}} {
		req := siptest.NewRequest(tc.method, psi, sip.UDP, p.Addr())
		req.Header.Set("To", "<sip:alice@ims.example.com>")

		if tc.toTag != "" {
			req.Header.Set("To", "<sip:alice@ims.example.com>;tag="+tc.toTag)
		}

		req.Header.Set("P-Asserted-Identity", "<sip:alice@ims.example.com>")
		p.Send(sip.UDP, w.addr, req)

		for {
			res, _ := p.RecvResponse()
			if res.StatusCode == 100 {
				continue
			}

			if res.StatusCode != 403 {
				t.Errorf("%s from outside the trust domain: %d", tc.method, res.StatusCode)
			}

			break
		}
	}

	alice := w.users["alice"]

	w.core.mu.Lock()
	w.core.forge = "12"
	w.core.mu.Unlock()

	if _, err := alice.Call(mustURI(t, fire)); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	es := alice.expect(t, "failed")
	if res := event(es, client.EventFailed).Response; res == nil || res.StatusCode != 404 || !warned(res, identity.WarnUserUnknown) {
		t.Fatalf("INVITE of another registration: %v", res)
	}

	w.users["bob"].expectNot(t, "incoming")
}

// TS 24.229 §5.4.1.7 l, §5.7.1.3B: alice's UE registers again, of a new registration token, its REGISTER without
// mcptt-info (TS 33.180 §5.1.3.2.1): its binding takes the token, and her calls of it are served.
func TestThroughIMSNewToken(t *testing.T) {
	w := newIMSWorld(t)
	alice := w.users["alice"]

	if res := w.core.register(alice, "21", false); res.StatusCode != 200 {
		t.Fatalf("reregistration: %d", res.StatusCode)
	}

	ca, err := alice.Call(mustURI(t, fire))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()
	alice.expect(t, "established")

	if _, isc := w.core.requests("INVITE"); len(isc) != 1 || !strings.Contains(isc[0].Header.Get("Feature-Caps"), `"21"`) {
		t.Fatalf("INVITEs to the AS %v", isc)
	}

	ca.Leave()
	w.n.Quiesce()
}

// TS 24.229 §5.4.1.5, §5.4.1.7 f, §5.7.1.1 NOTE 5; RFC 6665 §4.4.1: carol deregistered by the network, of a NOTIFY
// ending the reg event subscription and a third-party REGISTER of Expires 0, is logged off: alice's call invites bob
// only, and the server sends no SUBSCRIBE to end the subscription ended.
func TestThroughIMSNetworkDeregistration(t *testing.T) {
	w := newIMSWorld(t)
	alice, bob, carol := w.users["alice"], w.users["bob"], w.users["carol"]

	w.core.subscriptions()

	if res := w.core.deregister(carol); res.StatusCode != 200 {
		t.Fatalf("deregistration: %d", res.StatusCode)
	}

	if subs := w.core.subscriptions(); len(subs) != 0 {
		t.Errorf("SUBSCRIBEs after the deregistration: %v", subs)
	}

	ca, err := alice.Call(mustURI(t, fire))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()
	alice.expect(t, "established")
	bob.expect(t, "incoming", "established")
	carol.expectNot(t, "incoming")

	if invites, _ := w.core.requests("INVITE"); len(invites) != 1 || invites[0].URI.String() != bob.impu {
		t.Errorf("INVITEs of the AS %v", invites)
	}

	ca.Leave()
	w.n.Quiesce()
}
