package testue

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/milenage"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

const (
	imsi   = "001010123456789"
	domain = "ims.mnc001.mcc001.3gppnetwork.org"
	impi   = imsi + "@" + domain
	impu   = "sip:" + impi
	msisdn = "sip:+15550001@" + domain
	imei   = "35693803564380"
)

var (
	loopback = netip.MustParseAddr("127.0.0.1")
	testK    = []byte("0123456789abcdef")
	testOPc  = []byte("fedcba9876543210")
)

// network plays the P-CSCF and the S-CSCF: an unprotected socket, and a
// protected server port and two client ports as if SAs were in place. Like
// the P-CSCF, it offers each new set on the client port the last one did not
// use; pc is the one of the last set.
type network struct {
	t      *testing.T
	sqn    uint64
	k      []byte
	pcscf  *siptest.Socket
	ps     *siptest.Socket
	pcs    [2]*siptest.Socket
	pc     *siptest.Socket
	spiC   uint32
	spiS   uint32
	vector milenage.Vector
	nonce  string
	seen   map[string]bool
}

func newNetwork(t *testing.T) *network {
	return &network{
		t:     t,
		sqn:   100,
		k:     testK,
		pcscf: siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
		ps:    siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
		pcs: [2]*siptest.Socket{
			siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
			siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
		},
		spiC: 1000,
		spiS: 1001,
		seen: make(map[string]bool),
	}
}

func (n *network) newUE(cfg Config) (*UE, *ipsectest.Kernel) {
	n.t.Helper()

	kernel := ipsectest.NewKernel()

	cfg.IMSI, cfg.IMEI = imsi, imei
	cfg.PCSCF, cfg.Local = n.pcscf.Addr(), loopback

	if cfg.K == nil {
		cfg.K, cfg.OPc = testK, testOPc
	}

	if !cfg.Plain {
		cfg.Kernel = kernel
	}

	u, err := New(cfg)
	if err != nil {
		n.t.Fatal(err)
	}

	n.t.Cleanup(func() { _ = u.Close() })

	return u, kernel
}

// recv takes the next request on the socket, skipping retransmissions.
func (n *network) recv(s *siptest.Socket) (*sip.Request, sip.Flow) {
	n.t.Helper()

	for {
		req, f := s.RecvRequest()

		via, _ := req.Header.TopVia()
		if n.seen[via.Branch()] {
			continue
		}

		n.seen[via.Branch()] = true

		return req, f
	}
}

func (n *network) wwwAuthenticate() string {
	return `Digest realm="` + domain + `", nonce="` + n.nonce + `", algorithm=AKAv1-MD5, qop="auth"`
}

func (n *network) challenge(req *sip.Request, f sip.Flow, from *siptest.Socket, server bool) {
	n.t.Helper()

	n.newVector()

	res := sip.NewResponse(req, 401, "")
	res.Header.Add("WWW-Authenticate", n.wwwAuthenticate())
	n.securityServer(res, server)
	n.reply(from, f, res)
}

func (n *network) newVector() {
	n.t.Helper()

	n.sqn++

	r := make([]byte, 16)
	r[0] = byte(n.sqn)

	v, err := milenage.GenerateVector(n.k, testOPc, r, n.sqn, []byte{0, 0})
	if err != nil {
		n.t.Fatal(err)
	}

	n.vector = v
	n.nonce = base64.StdEncoding.EncodeToString(append(slices.Clone(v.RAND), v.AUTN...))
}

func (n *network) securityServer(res *sip.Response, server bool) {
	if server {
		n.spiC, n.spiS = n.spiC+2, n.spiS+2

		if n.pc == n.pcs[0] {
			n.pc = n.pcs[1]
		} else {
			n.pc = n.pcs[0]
		}

		for _, alg := range []string{"hmac-md5-96", "hmac-sha-1-96"} {
			res.Header.Add("Security-Server", "ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c="+strconv.Itoa(int(n.spiC))+
				";spi-s="+strconv.Itoa(int(n.spiS))+";port-c="+strconv.Itoa(int(n.pc.Addr().Port()))+
				";port-s="+strconv.Itoa(int(n.ps.Addr().Port()))+";alg="+alg+";ealg=null")
		}
	}
}

// reply answers on the request's connection over TCP and, over UDP, from
// the given socket: the protected client port sends to the UE's protected
// server port.
func (n *network) reply(from *siptest.Socket, f sip.Flow, res *sip.Response) {
	n.t.Helper()

	to := f.Remote

	switch {
	case f.Transport == sip.TCP:
		for _, s := range []*siptest.Socket{n.pcscf, n.ps, n.pcs[0], n.pcs[1]} {
			if s.Addr() == f.Local {
				from = s
			}
		}
	case from == n.pcs[0] || from == n.pcs[1]:
		via, _ := res.Header.TopVia()
		to = netip.AddrPortFrom(loopback, via.Port)
	}

	from.Send(f.Transport, to, res)
}

func (n *network) ok(req *sip.Request, f sip.Flow, from *siptest.Socket, expires int) {
	n.t.Helper()

	res := sip.NewResponse(req, 200, "")

	contacts, _ := req.Header.Contacts()
	for _, c := range contacts {
		res.Header.Add("Contact", "<"+c.URI.String()+">;expires="+strconv.Itoa(expires))
	}

	res.Header.Add("Service-Route", "<sip:orig@scscf."+domain+":6060;lr>")
	res.Header.Add("P-Associated-URI", "<"+msisdn+">, <tel:+15550001>")

	n.reply(from, f, res)
}

func authParams(t *testing.T, req *sip.Request) map[string]string {
	t.Helper()

	a, err := sip.ParseAuth(req.Header.Get("Authorization"))
	if err != nil {
		t.Fatal(err)
	}

	out := make(map[string]string)
	for _, p := range a.Params {
		out[p.Name] = sip.Unquote(p.Value)
	}

	return out
}

// verify checks the response the way the S-CSCF does.
func verify(ps map[string]string, xres []byte) bool {
	h := func(parts ...[]byte) string {
		m := md5.New()
		for _, p := range parts {
			m.Write(p)
		}

		return hex.EncodeToString(m.Sum(nil))
	}

	ha1 := h([]byte(ps["username"]+":"+ps["realm"]+":"), xres)
	ha2 := h([]byte("REGISTER:" + ps["uri"]))

	return ps["qop"] == "auth" && h([]byte(ha1+":"+ps["nonce"]+":"+ps["nc"]+":"+ps["cnonce"]+":auth:"+ha2)) == ps["response"]
}

func start(f func(ctx context.Context) error) chan error {
	done := make(chan error, 1)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		done <- f(ctx)
	}()

	return done
}

func wait(t *testing.T, done chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the UE")
	}

	return nil
}

// register runs an initial registration through to the 200.
func (n *network) register(u *UE) {
	n.t.Helper()

	done := start(u.Register)

	req, f := n.recv(n.pcscf)
	n.challenge(req, f, n.pcscf, true)

	req, f = n.recv(n.ps)
	n.ok(req, f, n.pc, 3600)

	if err := wait(n.t, done); err != nil {
		n.t.Fatal(err)
	}
}

func TestInitialRegistration(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{SQN: 50})

	done := start(u.Register)

	req, f := n.recv(n.pcscf)

	if req.URI.String() != "sip:"+domain || req.Header.Get("To") != "<"+impu+">" ||
		!strings.HasPrefix(req.Header.Get("From"), "<"+impu+">;tag=") {
		t.Fatalf("REGISTER %s from %q to %q", req.URI, req.Header.Get("From"), req.Header.Get("To"))
	}

	contacts, err := req.Header.Contacts()
	if err != nil || len(contacts) != 1 {
		t.Fatalf("Contact = %q", req.Header.Values("Contact"))
	}

	c := contacts[0]
	instance, _ := c.Params.Get("+sip.instance")
	icsi, _ := c.Params.Get("+g.3gpp.icsi-ref")

	if instance != `"<urn:gsma:imei:35693803-564380-0>"` || icsi != `"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"` ||
		!c.Params.Has("+g.3gpp.smsip") || !c.Params.Has("audio") || len(c.URI.User) != 36 || c.URI.User[14] != '1' {
		t.Fatalf("Contact = %s", c)
	}

	via, _ := req.Header.TopVia()
	if _, rport := via.RPort(); !rport || f.Remote != u.Unprotected() {
		t.Fatalf("Via = %s from %s, want rport from the unprotected port %s", via, f.Remote, u.Unprotected())
	}

	ps := authParams(t, req)
	if ps["username"] != impi || ps["realm"] != domain || ps["uri"] != "sip:"+domain || ps["nonce"] != "" ||
		ps["response"] != "" || ps["algorithm"] != "AKAv1-MD5" {
		t.Fatalf("Authorization = %q", req.Header.Get("Authorization"))
	}

	clients, _ := req.Header.SecurityMechanisms("Security-Client")

	offers, err := ipsec.ParseOffers(clients)
	if err != nil || len(offers) != 2 || offers[0].Integrity != ipsec.HMACSHA196 || offers[1].Integrity != ipsec.HMACMD596 ||
		offers[0].Endpoint != offers[1].Endpoint {
		t.Fatalf("Security-Client = %q", req.Header.Values("Security-Client"))
	}

	if via.Port != u.Unprotected().Port() || c.URI.Port != u.Unprotected().Port() {
		t.Fatalf("Via %s and Contact %s on the unprotected REGISTER, want the unprotected port %d", via, c, u.Unprotected().Port())
	}

	for name, want := range map[string]string{
		"Expires": "600000", "Supported": "path", "Require": "sec-agree", "Proxy-Require": "sec-agree", "P-Access-Network-Info": "",
	} {
		if got := req.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	n.challenge(req, f, n.pcscf, true)

	protected, pf := n.recv(n.ps)

	installed := kernel.Installed()
	if len(installed) != 1 {
		t.Fatalf("installed %v, want one set", installed)
	}

	for set, keys := range installed {
		want := ipsec.Set{
			Local:      offers[0].Endpoint,
			Remote:     ipsec.Endpoint{Addr: loopback, PortC: n.pc.Addr().Port(), PortS: n.ps.Addr().Port(), SPIC: n.spiC, SPIS: n.spiS},
			Integrity:  ipsec.HMACMD596,
			Encryption: ipsec.EncryptionNull,
		}
		want.Local.Addr = loopback

		if set != want || !bytes.Equal(keys.CK, n.vector.CK) || !bytes.Equal(keys.IK, n.vector.IK) {
			t.Fatalf("installed %s, want %s with the vector's keys", set, want)
		}
	}

	pvia, _ := protected.Header.TopVia()
	pcontacts, _ := protected.Header.Contacts()

	if pvia.Port != offers[0].Endpoint.PortS || len(pcontacts) != 1 || pcontacts[0].URI.Port != offers[0].Endpoint.PortS ||
		pcontacts[0].URI.User != c.URI.User {
		t.Fatalf("Via %s and Contact %q on the protected REGISTER, want port-s %d", pvia, protected.Header.Values("Contact"),
			offers[0].Endpoint.PortS)
	}

	if pf.Remote.Port() != offers[0].Endpoint.PortC || protected.Header.CallID() != req.Header.CallID() {
		t.Fatalf("protected REGISTER from %s with Call-ID %q, want from port-c %d and Call-ID %q",
			pf.Remote, protected.Header.CallID(), offers[0].Endpoint.PortC, req.Header.CallID())
	}

	if cseq, _ := protected.Header.CSeq(); cseq.Seq != 2 {
		t.Fatalf("CSeq = %d, want 2", cseq.Seq)
	}

	ps = authParams(t, protected)
	if ps["nonce"] != n.nonce || ps["nc"] != "00000001" || ps["cnonce"] == "" || !verify(ps, n.vector.XRES) {
		t.Fatalf("Authorization = %q, want a response with RES", protected.Header.Get("Authorization"))
	}

	if !slices.Equal(protected.Header.Values("Security-Client"), req.Header.Values("Security-Client")) ||
		!slices.Equal(protected.Header.Values("Security-Verify"), n.pcscfServer(t)) ||
		protected.Header.Get("P-Access-Network-Info") != "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001"+"0000001" {
		t.Fatalf("protected REGISTER:\n%s", protected)
	}

	n.ok(protected, pf, n.pc, 3600)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	st := u.State()
	if !st.Registered || st.DefaultIMPU != msisdn || !st.Barred || len(st.ServiceRoute) != 1 ||
		time.Until(st.Expires) < 3590*time.Second || u.SQN() != n.sqn {
		t.Fatalf("state = %+v, SQN %d", st, u.SQN())
	}

	sas := u.SAs()
	if len(sas) != 1 || sas[0].State != Established || time.Until(sas[0].Expires) < 3620*time.Second {
		t.Fatalf("SAs = %+v, want one established for the registration and 30 s", sas)
	}
}

func (n *network) pcscfServer(t *testing.T) []string {
	t.Helper()

	var out []string

	for _, alg := range []string{"hmac-md5-96", "hmac-sha-1-96"} {
		out = append(out, "ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c="+strconv.Itoa(int(n.spiC))+
			";spi-s="+strconv.Itoa(int(n.spiS))+";port-c="+strconv.Itoa(int(n.pc.Addr().Port()))+
			";port-s="+strconv.Itoa(int(n.ps.Addr().Port()))+";alg="+alg+";ealg=null")
	}

	return out
}

func TestResync(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{SQN: 5000})

	done := start(u.Register)

	req, f := n.recv(n.pcscf)
	n.challenge(req, f, n.pcscf, true)

	resync, f := n.recv(n.pcscf)

	ps := authParams(t, resync)

	auts, err := base64.StdEncoding.DecodeString(ps["auts"])
	if err != nil {
		t.Fatal(err)
	}

	sqnMS, err := milenage.Resync(testK, testOPc, n.vector.RAND, auts)
	if err != nil || sqnMS != 5000 || ps["response"] == "" || ps["nonce"] != n.nonce {
		t.Fatalf("resync REGISTER with AUTS for %d (%v), Authorization %q", sqnMS, err, resync.Header.Get("Authorization"))
	}

	if resync.Header.CallID() != req.Header.CallID() {
		t.Fatal("resync REGISTER with a new Call-ID")
	}

	before, _ := req.Header.SecurityMechanisms("Security-Client")
	after, _ := resync.Header.SecurityMechanisms("Security-Client")
	o1, _ := ipsec.ParseOffer(before[0])
	o2, _ := ipsec.ParseOffer(after[0])

	if o1.Endpoint.SPIC == o2.Endpoint.SPIC || o1.Endpoint.SPIS == o2.Endpoint.SPIS || o1.Endpoint.PortC == o2.Endpoint.PortC ||
		o1.Endpoint.PortS != o2.Endpoint.PortS {
		t.Fatalf("Security-Client %s then %s, want new spi-c, spi-s and port-c", before[0], after[0])
	}

	n.sqn = sqnMS
	n.challenge(resync, f, n.pcscf, true)

	protected, pf := n.recv(n.ps)
	if pf.Remote.Port() != o2.Endpoint.PortC {
		t.Fatalf("protected REGISTER from %s, want port-c %d", pf.Remote, o2.Endpoint.PortC)
	}

	n.ok(protected, pf, n.pc, 3600)

	if err := wait(t, done); err != nil || u.SQN() != 5001 {
		t.Fatalf("Register = %v with SQN %d, want SQN 5001", err, u.SQN())
	}
}

func TestMACFailure(t *testing.T) {
	n := newNetwork(t)
	n.k = []byte("the wrong K!!!!!")
	u, kernel := n.newUE(Config{})

	done := start(u.Register)

	req, f := n.recv(n.pcscf)
	n.challenge(req, f, n.pcscf, true)

	failure, f := n.recv(n.pcscf)

	ps := authParams(t, failure)
	if ps["response"] != "" || ps["auts"] != "" || ps["nonce"] != n.nonce {
		t.Fatalf("Authorization = %q, want an empty response without auts", failure.Header.Get("Authorization"))
	}

	n.reply(n.pcscf, f, sip.NewResponse(failure, 403, ""))

	err := wait(t, done)

	var re *ResponseError
	if !errors.Is(err, ErrNetworkAuthentication) || !errors.As(err, &re) || re.Response.StatusCode != 403 {
		t.Fatalf("Register = %v, want a network authentication failure ending in 403", err)
	}

	if len(kernel.Installed()) != 0 || u.State().Registered {
		t.Fatal("SAs or a registration after a MAC failure")
	}
}

func TestTwoInvalidChallenges(t *testing.T) {
	n := newNetwork(t)
	n.k = []byte("the wrong K!!!!!")
	u, _ := n.newUE(Config{})

	done := start(u.Register)

	for range 3 {
		req, f := n.recv(n.pcscf)
		n.challenge(req, f, n.pcscf, true)
	}

	if err := wait(t, done); !errors.Is(err, ErrNetworkAuthentication) {
		t.Fatalf("Register = %v, want ErrNetworkAuthentication after the third invalid challenge", err)
	}

	n.pcscf.RecvNone(100 * time.Millisecond)
}

func TestNoSecurityServer(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})

	done := start(u.Register)

	first, f := n.recv(n.pcscf)
	n.challenge(first, f, n.pcscf, false)

	again, f := n.recv(n.pcscf)
	if again.Header.CallID() == first.Header.CallID() || authParams(t, again)["nonce"] != "" {
		t.Fatalf("REGISTER after a 401 without Security-Server: Call-ID %q, Authorization %q",
			again.Header.CallID(), again.Header.Get("Authorization"))
	}

	n.challenge(again, f, n.pcscf, false)

	if err := wait(t, done); !errors.Is(err, ErrNoSecurityServer) {
		t.Fatalf("Register = %v, want ErrNoSecurityServer", err)
	}
}

func TestIntervalTooBrief(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Expires: 30 * time.Second})

	done := start(u.Register)

	req, f := n.recv(n.pcscf)

	res := sip.NewResponse(req, 423, "")
	res.Header.Add("Min-Expires", "60")
	n.reply(n.pcscf, f, res)

	req, f = n.recv(n.pcscf)
	if req.Header.Get("Expires") != "60" {
		t.Fatalf("Expires = %q after a 423, want 60", req.Header.Get("Expires"))
	}

	n.challenge(req, f, n.pcscf, true)

	req, f = n.recv(n.ps)
	n.ok(req, f, n.pc, 60)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestForbidden(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})

	done := start(u.Register)

	req, f := n.recv(n.pcscf)
	n.challenge(req, f, n.pcscf, true)

	req, f = n.recv(n.ps)
	n.reply(n.pc, f, sip.NewResponse(req, 403, ""))

	var re *ResponseError
	if err := wait(t, done); !errors.As(err, &re) || re.Response.StatusCode != 403 {
		t.Fatalf("Register = %v, want a 403", err)
	}

	if len(kernel.Installed()) != 0 || len(kernel.Removed()) != 1 || len(u.SAs()) != 0 {
		t.Fatalf("installed %v and removed %v, want the temporary SAs deleted", kernel.Installed(), kernel.Removed())
	}
}

func TestReRegistrationWithoutChallenge(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})
	n.register(u)

	established := u.SAs()[0]
	auth := ""

	done := start(u.Reregister)

	req, f := n.recv(n.ps)

	if f.Remote.Port() != established.Set.Local.PortC {
		t.Fatalf("re-REGISTER from %s, want the established port-c %d", f.Remote, established.Set.Local.PortC)
	}

	clients, _ := req.Header.SecurityMechanisms("Security-Client")
	o, _ := ipsec.ParseOffer(clients[0])

	if o.Endpoint.PortS != established.Set.Local.PortS || o.Endpoint.PortC == established.Set.Local.PortC ||
		o.Endpoint.SPIC == established.Set.Local.SPIC || o.Endpoint.SPIS == established.Set.Local.SPIS {
		t.Fatalf("Security-Client %s, want a new port-c, spi-c and spi-s with port-s %d", clients[0], established.Set.Local.PortS)
	}

	if ps := authParams(t, req); ps["nonce"] != n.nonce || !verify(ps, n.vector.XRES) {
		t.Fatalf("Authorization = %q, want the last one", req.Header.Get("Authorization"))
	}

	auth = req.Header.Get("Authorization")

	if !slices.Equal(req.Header.Values("Security-Verify"), n.pcscfServer(t)) || req.Header.Get("P-Access-Network-Info") == "" {
		t.Fatalf("re-REGISTER:\n%s", req)
	}

	n.ok(req, f, n.pc, 1200)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	sas := u.SAs()
	if len(sas) != 1 || sas[0].Set != established.Set || len(kernel.Installed()) != 1 {
		t.Fatalf("SAs = %+v, want the established set kept", sas)
	}

	done = start(u.Reregister)

	req, f = n.recv(n.ps)
	if req.Header.Get("Authorization") != auth {
		t.Fatalf("Authorization = %q, want %q", req.Header.Get("Authorization"), auth)
	}

	n.ok(req, f, n.pc, 1200)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestReAuthentication(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})
	n.register(u)

	old := u.SAs()[0]

	done := start(u.Reregister)

	req, f := n.recv(n.ps)
	n.challenge(req, f, n.pc, true)

	protected, pf := n.recv(n.ps)

	clients, _ := req.Header.SecurityMechanisms("Security-Client")
	o, _ := ipsec.ParseOffer(clients[0])

	if pf.Remote.Port() != o.Endpoint.PortC {
		t.Fatalf("REGISTER after the re-authentication from %s, want the new port-c %d", pf.Remote, o.Endpoint.PortC)
	}

	if ps := authParams(t, protected); ps["nonce"] != n.nonce || !verify(ps, n.vector.XRES) {
		t.Fatalf("Authorization = %q", protected.Header.Get("Authorization"))
	}

	if len(kernel.Installed()) != 2 {
		t.Fatalf("installed %v, want the old and the new sets", kernel.Installed())
	}

	if n.pc.Addr().Port() == old.Set.Remote.PortC {
		t.Fatal("the new set has the old one's port-c")
	}

	n.ok(protected, pf, n.pc, 3600)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	// The 200 came through the new set, but the old one stays until a further
	// message does (TS 33.203 §7.4.1a).
	sas := u.SAs()
	if len(sas) != 2 || sas[0].State != Old || sas[0].Set != old.Set || sas[1].State != Established ||
		sas[1].Set.Local.PortC != o.Endpoint.PortC || len(kernel.Installed()) != 2 {
		t.Fatalf("SAs = %+v, want the old set kept beside the new one", sas)
	}

	// A request through the old set is still taken.
	n.request(n.pcs[0], old.Set)

	if len(u.SAs()) != 2 {
		t.Fatalf("SAs = %+v after a request through the old set, want both", u.SAs())
	}

	n.request(n.pc, sas[1].Set)

	sas = u.SAs()
	if len(sas) != 1 || sas[0].State != Established || !slices.Contains(kernel.Removed(), old.Set) {
		t.Fatalf("SAs = %+v, removed %v, want the old set deleted", sas, kernel.Removed())
	}
}

// options is a request as the P-CSCF sends it to the UE's protected server
// port: its Via has the protected server port, without rport.
func (n *network) options(port uint16) *sip.Request {
	n.t.Helper()

	r := siptest.NewRequest("OPTIONS", "sip:"+loopback.String()+":"+strconv.Itoa(int(port)), sip.UDP, n.ps.Addr())

	via, _ := r.Header.TopVia()
	via.Params.Del("rport")
	_ = r.Header.SetTopVia(via)

	return r
}

// request sends a request from a P-CSCF client port through the set, and
// checks that the 200 comes back from the UE's port_uc to port_ps (TS 33.203
// §7.1).
func (n *network) request(from *siptest.Socket, set ipsec.Set) {
	n.t.Helper()

	from.Send(sip.UDP, netip.AddrPortFrom(loopback, set.Local.PortS), n.options(set.Local.PortS))

	res, f := n.ps.RecvResponse()
	if res.StatusCode != 200 || f.Remote.Port() != set.Local.PortC {
		n.t.Fatalf("got %q from %s, want 200 from port_uc %d", res.StartLine(), f.Remote, set.Local.PortC)
	}
}

func TestDeregistration(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})
	n.register(u)

	done := start(u.Deregister)

	req, f := n.recv(n.ps)
	if req.Header.Get("Expires") != "0" {
		t.Fatalf("Expires = %q, want 0", req.Header.Get("Expires"))
	}

	n.reply(n.pc, f, sip.NewResponse(req, 200, ""))

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	if u.State().Registered || len(u.SAs()) != 0 || len(kernel.Installed()) != 0 {
		t.Fatalf("state %+v and SAs %v after deregistration", u.State(), kernel.Installed())
	}

	if err := u.Reregister(t.Context()); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("Reregister = %v, want ErrNotRegistered", err)
	}
}

func TestPlainRegistration(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Plain: true})

	done := start(u.Register)

	req, f := n.recv(n.pcscf)
	if req.Header.Has("Security-Client") || req.Header.Has("Require") || req.Header.Has("Proxy-Require") {
		t.Fatalf("plain REGISTER:\n%s", req)
	}

	n.challenge(req, f, n.pcscf, false)

	req, f = n.recv(n.pcscf)
	if f.Remote != u.Unprotected() || !verify(authParams(t, req), n.vector.XRES) {
		t.Fatalf("answer from %s:\n%s", f.Remote, req)
	}

	n.ok(req, f, n.pcscf, 3600)

	if err := wait(t, done); err != nil || !u.State().Registered {
		t.Fatalf("Register = %v, state %+v", err, u.State())
	}

	done = start(u.Reregister)

	req, f = n.recv(n.pcscf)
	n.ok(req, f, n.pcscf, 3600)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestAutomaticReRegistration(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})

	done := start(u.Register)

	req, f := n.recv(n.pcscf)
	n.challenge(req, f, n.pcscf, true)

	req, f = n.recv(n.ps)
	n.ok(req, f, n.pc, 2)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	req, f = n.recv(n.ps)
	if req.Header.Get("Expires") != "600000" {
		t.Fatalf("automatic re-REGISTER:\n%s", req)
	}

	n.ok(req, f, n.pc, 3600)

	deadline := time.Now().Add(5 * time.Second)
	for time.Until(u.State().Expires) < time.Hour-time.Minute {
		if time.Now().After(deadline) {
			t.Fatalf("state %+v, want the refreshed expiry", u.State())
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func TestExpiry(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})
	u.SetAutoReregister(false)

	done := start(u.Register)

	req, f := n.recv(n.pcscf)
	n.challenge(req, f, n.pcscf, true)

	req, f = n.recv(n.ps)
	n.ok(req, f, n.pc, 1)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1500 * time.Millisecond)

	if u.State().Registered {
		t.Fatal("registered after the expiry")
	}

	n.ps.RecvNone(100 * time.Millisecond)
}

func TestIdentities(t *testing.T) {
	for _, tc := range []struct {
		cfg  Config
		want identities
	}{
		{
			Config{IMSI: "310150123456789", MNCLength: 3},
			identities{
				impi:   "310150123456789@ims.mnc150.mcc310.3gppnetwork.org",
				impu:   "sip:310150123456789@ims.mnc150.mcc310.3gppnetwork.org",
				domain: "ims.mnc150.mcc310.3gppnetwork.org", mcc: "310", mnc: "150",
			},
		},
		{
			Config{IMSI: "234150999999999"},
			identities{
				impi:   "234150999999999@ims.mnc015.mcc234.3gppnetwork.org",
				impu:   "sip:234150999999999@ims.mnc015.mcc234.3gppnetwork.org",
				domain: "ims.mnc015.mcc234.3gppnetwork.org", mcc: "234", mnc: "15",
			},
		},
		{
			Config{IMPI: "alice@example.org", IMPU: "sip:alice@example.org", HomeDomain: "example.org"},
			identities{impi: "alice@example.org", impu: "sip:alice@example.org", domain: "example.org"},
		},
	} {
		if got, err := deriveIdentities(tc.cfg); err != nil || got != tc.want {
			t.Errorf("identities of %+v = %+v, %v, want %+v", tc.cfg, got, err, tc.want)
		}
	}

	if _, err := deriveIdentities(Config{IMSI: "12a"}); err == nil {
		t.Error("a bad IMSI was accepted")
	}
}

func TestRequestsOutsideTheSAsAreDropped(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})
	n.register(u)

	sa := u.SAs()[0]
	stranger := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	stranger.Send(sip.UDP, u.Unprotected(), n.options(u.Unprotected().Port()))
	stranger.Send(sip.UDP, netip.AddrPortFrom(loopback, sa.Set.Local.PortS), n.options(sa.Set.Local.PortS))
	stranger.RecvNone(200 * time.Millisecond)

	n.ps.RecvNone(200 * time.Millisecond)

	n.request(n.pc, sa.Set)

	for {
		select {
		case e := <-u.Events():
			if e.Request != nil && e.Request.Method == "OPTIONS" {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("no OPTIONS event")
		}
	}
}

func TestClientPortsAreReused(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})
	n.register(u)

	ports := map[uint16]bool{u.SAs()[0].Set.Local.PortC: true}

	for range 5 {
		done := start(u.Reregister)

		req, f := n.recv(n.ps)

		clients, _ := req.Header.SecurityMechanisms("Security-Client")
		o, _ := ipsec.ParseOffer(clients[0])

		if o.Endpoint.PortC == f.Remote.Port() {
			t.Fatalf("Security-Client port-c %d is the established set's", o.Endpoint.PortC)
		}

		ports[o.Endpoint.PortC] = true

		n.ok(req, f, n.pc, 3600)

		if err := wait(t, done); err != nil {
			t.Fatal(err)
		}
	}

	if len(ports) != 2 || len(u.clientPorts) != 2 {
		t.Fatalf("port-c values %v and listeners %v over five re-registrations, want two", ports, u.clientPorts)
	}
}

func TestProtectedRegisterChallengedAgain(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})

	done := start(u.Register)

	req, f := n.recv(n.pcscf)
	n.challenge(req, f, n.pcscf, true)

	first, f := n.recv(n.ps)
	n.challenge(first, f, n.pc, true)

	second, f := n.recv(n.ps)

	if !slices.Equal(second.Header.Values("Security-Client"), req.Header.Values("Security-Client")) ||
		!slices.Equal(second.Header.Values("Security-Verify"), n.pcscfServer(t)) || authParams(t, second)["nonce"] != n.nonce {
		t.Fatalf("REGISTER after the second challenge:\n%s", second)
	}

	n.ok(second, f, n.pc, 3600)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	sas := u.SAs()
	if len(sas) != 1 || len(kernel.Installed()) != 1 || len(kernel.Removed()) != 1 || sas[0].Set.Remote.SPIC != n.spiC {
		t.Fatalf("SAs = %+v, removed %v, want the second set alone", sas, kernel.Removed())
	}

	// The established set owns its SPIs: new ones are allocated around them.
	c, err := u.newClient(0)
	if err != nil || c.spiC == sas[0].Set.Local.SPIC || c.spiS == sas[0].Set.Local.SPIS ||
		c.spiC == sas[0].Set.Local.SPIS || c.spiS == sas[0].Set.Local.SPIC {
		t.Fatalf("new client %+v, %v beside %s", c, err, sas[0].Set)
	}
}

func TestUnusableSecurityServer(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})

	done := start(u.Register)

	for range 2 {
		req, f := n.recv(n.pcscf)

		n.newVector()

		res := sip.NewResponse(req, 401, "")
		res.Header.Add("WWW-Authenticate", n.wwwAuthenticate())
		res.Header.Add("Security-Server", "ipsec-3gpp;prot=esp;mod=trans;alg=hmac-md5-96")
		n.reply(n.pcscf, f, res)
	}

	if err := wait(t, done); !errors.Is(err, ErrNoSecurityServer) {
		t.Fatalf("Register = %v, want ErrNoSecurityServer after two unusable Security-Servers", err)
	}
}

func TestAccessNetworkInfoWithoutIMSI(t *testing.T) {
	_, err := New(Config{
		IMPI: "alice@example.org", IMPU: "sip:alice@example.org", HomeDomain: "example.org", IMEI: imei,
		PCSCF: netip.AddrPortFrom(loopback, 5060), Local: loopback, Kernel: ipsectest.NewKernel(),
	})
	if err == nil {
		t.Fatal("New without an IMSI nor AccessNetworkInfo succeeded")
	}
}

func TestCloseDuringRegistration(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})

	done := start(u.Register)

	req, f := n.recv(n.pcscf)
	n.challenge(req, f, n.pcscf, true)
	n.recv(n.ps)

	if err := u.Close(); err != nil {
		t.Fatal(err)
	}

	if err := wait(t, done); err == nil {
		t.Fatal("Register succeeded after Close")
	}

	if len(kernel.Installed()) != 0 || len(u.SAs()) != 0 {
		t.Fatalf("installed %v after Close", kernel.Installed())
	}
}

func TestZeroExpiry(t *testing.T) {
	n := newNetwork(t)
	u, kernel := n.newUE(Config{})

	done := start(u.Register)

	req, f := n.recv(n.pcscf)
	n.challenge(req, f, n.pcscf, true)

	req, f = n.recv(n.ps)
	n.ok(req, f, n.pc, 0)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	if u.State().Registered || len(kernel.Installed()) != 0 {
		t.Fatalf("state %+v and SAs %v after a 200 granting no time", u.State(), kernel.Installed())
	}

	n.ps.RecvNone(200 * time.Millisecond)
}
