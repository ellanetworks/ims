// Package uetest is the network of UE tests: a P-CSCF on sockets that challenges a UE with IMS AKA (TS 33.203 §6.1),
// agrees ipsec-3gpp with it (§7.2) and registers it, on behalf of the S-CSCF.
package uetest

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"net/netip"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/milenage"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

// The subscriber of the network: its USIM's identities, key and OPc, the IMEI of its UE.
const (
	IMSI   = "001010123456789"
	Domain = "ims.mnc001.mcc001.3gppnetwork.org"
	IMPI   = IMSI + "@" + Domain
	IMPU   = "sip:" + IMPI
	MSISDN = "sip:+15550001@" + Domain
	IMEI   = "35693803564380"
)

var (
	Loopback = netip.MustParseAddr("127.0.0.1")
	K        = []byte("0123456789abcdef")
	OPc      = []byte("fedcba9876543210")
)

// Network is the network of a UE under test: a P-CSCF with its unprotected socket PCSCF, its protected server port
// PS and its two protected client ports PCs, PC the one of the latest SAs, which challenges with vectors of K and SQN
// and registers the UE.
type Network struct {
	T      *testing.T
	SQN    uint64
	K      []byte
	PCSCF  *siptest.Socket
	PS     *siptest.Socket
	PCs    [2]*siptest.Socket
	PC     *siptest.Socket
	SPIC   uint32
	SPIS   uint32
	Vector milenage.Vector
	Nonce  string
	Seen   map[string]bool

	Addr       netip.Addr
	Ealg       string
	Associated string
	RegEvent   bool
}

// New is a network on the loopback address.
func New(t *testing.T) *Network {
	return NewAt(t, Loopback)
}

// NewAt is a network on addr.
func NewAt(t *testing.T, addr netip.Addr) *Network {
	return &Network{
		T:     t,
		SQN:   100,
		K:     K,
		PCSCF: siptest.NewSocket(t, netip.AddrPortFrom(addr, 0)),
		PS:    siptest.NewSocket(t, netip.AddrPortFrom(addr, 0)),
		PCs: [2]*siptest.Socket{
			siptest.NewSocket(t, netip.AddrPortFrom(addr, 0)),
			siptest.NewSocket(t, netip.AddrPortFrom(addr, 0)),
		},
		SPIC:       1000,
		SPIS:       1001,
		Seen:       make(map[string]bool),
		Addr:       addr,
		Ealg:       "null",
		Associated: "<" + MSISDN + ">, <tel:+15550001>",
	}
}

func (n *Network) Recv(s *siptest.Socket) (*sip.Request, sip.Flow) {
	n.T.Helper()

	for {
		req, f := s.RecvRequest()

		via, _ := req.Header.TopVia()
		if n.Seen[via.Branch()] {
			continue
		}

		n.Seen[via.Branch()] = true

		return req, f
	}
}

func (n *Network) WWWAuthenticate() string {
	return `Digest realm="` + Domain + `", nonce="` + n.Nonce + `", algorithm=AKAv1-MD5, qop="auth"`
}

func (n *Network) Challenge(req *sip.Request, f sip.Flow, from *siptest.Socket, server bool) {
	n.T.Helper()

	n.NewVector()

	res := sip.NewResponse(req, 401, "")
	res.Header.Add("WWW-Authenticate", n.WWWAuthenticate())
	n.SecurityServer(res, server)
	n.Reply(from, f, res)
}

func (n *Network) NewVector() {
	n.T.Helper()

	n.SQN++

	r := make([]byte, 16)
	r[0] = byte(n.SQN)

	v, err := milenage.GenerateVector(n.K, OPc, r, n.SQN, []byte{0, 0})
	if err != nil {
		n.T.Fatal(err)
	}

	n.Vector = v
	n.Nonce = base64.StdEncoding.EncodeToString(append(slices.Clone(v.RAND), v.AUTN...))
}

func (n *Network) SecurityServer(res *sip.Response, server bool) {
	if server {
		n.SPIC, n.SPIS = n.SPIC+2, n.SPIS+2

		if n.PC == n.PCs[0] {
			n.PC = n.PCs[1]
		} else {
			n.PC = n.PCs[0]
		}

		for _, v := range n.PCSCFServer(n.T) {
			res.Header.Add("Security-Server", v)
		}
	}
}

func (n *Network) Reply(from *siptest.Socket, f sip.Flow, res *sip.Response) {
	n.T.Helper()

	to := f.Remote

	switch {
	case f.Transport == sip.TCP:
		for _, s := range []*siptest.Socket{n.PCSCF, n.PS, n.PCs[0], n.PCs[1]} {
			if s.Addr() == f.Local {
				from = s
			}
		}
	case from == n.PCs[0] || from == n.PCs[1]:
		via, _ := res.Header.TopVia()
		to = netip.AddrPortFrom(n.Addr, via.Port)
	}

	from.Send(f.Transport, to, res)
}

func (n *Network) OK(req *sip.Request, f sip.Flow, from *siptest.Socket, expires int) {
	n.T.Helper()

	res := sip.NewResponse(req, 200, "")

	contacts, _ := req.Header.Contacts()
	for _, c := range contacts {
		res.Header.Add("Contact", "<"+c.URI.String()+">;expires="+strconv.Itoa(expires))
	}

	res.Header.Add("Service-Route", "<sip:orig@scscf."+Domain+":6060;lr>")
	res.Header.Add("P-Associated-URI", n.Associated)

	n.Reply(from, f, res)
}

// AuthParams are the parameters of the Authorization of req.
func AuthParams(t *testing.T, req *sip.Request) map[string]string {
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

// Verify reports whether the Authorization parameters ps answer with xres (RFC 3310 §3.4).
func Verify(ps map[string]string, xres []byte) bool {
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

// Start runs f in a goroutine with a timeout, its error on the channel.
func Start(f func(ctx context.Context) error) chan error {
	done := make(chan error, 1)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		done <- f(ctx)
	}()

	return done
}

// Wait waits for the error of Start.
func Wait(t *testing.T, done chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the UE")
	}

	return nil
}

// Register registers u: a challenge on the unprotected port, the 200 on the protected one.
func (n *Network) Register(u interface{ Register(context.Context) error }) {
	n.T.Helper()

	done := Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	n.OK(req, f, n.PC, 3600)

	if err := Wait(n.T, done); err != nil {
		n.T.Fatal(err)
	}
}

func (n *Network) PCSCFServer(t *testing.T) []string {
	t.Helper()

	var out []string

	for _, alg := range []string{"hmac-md5-96", "hmac-sha-1-96"} {
		out = append(out, "ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c="+strconv.Itoa(int(n.SPIC))+
			";spi-s="+strconv.Itoa(int(n.SPIS))+";port-c="+strconv.Itoa(int(n.PC.Addr().Port()))+
			";port-s="+strconv.Itoa(int(n.PS.Addr().Port()))+";alg="+alg+";ealg="+n.Ealg)
	}

	return out
}

func (n *Network) Options(port uint16) *sip.Request {
	n.T.Helper()

	r := siptest.NewRequest("OPTIONS", "sip:"+sip.FormatHost(n.Addr)+":"+strconv.Itoa(int(port)), sip.UDP, n.PS.Addr())

	via, _ := r.Header.TopVia()
	via.Params.Del("rport")
	_ = r.Header.SetTopVia(via)

	return r
}

func (n *Network) Request(from *siptest.Socket, set ipsec.Set) {
	n.T.Helper()

	from.Send(sip.UDP, netip.AddrPortFrom(n.Addr, set.Local.PortS), n.Options(set.Local.PortS))

	res, f := n.PS.RecvResponse()
	if res.StatusCode != 200 || f.Remote.Port() != set.Local.PortC {
		n.T.Fatalf("got %q from %s, want 200 from port_uc %d", res.StartLine(), f.Remote, set.Local.PortC)
	}
}

// Eventually waits for f to hold, failing t after 10 s.
func Eventually(t *testing.T, what string, f func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(10 * time.Millisecond)
	}
}
