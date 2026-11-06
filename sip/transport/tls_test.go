package transport_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"io"
	"math/big"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transport"
)

var rsaKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}

	return k
})

var serial atomic.Int64

type certSpec struct {
	cn   string
	dns  []string
	uris []string
	ips  []netip.Addr
	eku  []x509.ExtKeyUsage
	oids []asn1.ObjectIdentifier
	ca   bool
	old  bool
}

var sipDomain = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 20}

type issued struct {
	tls  tls.Certificate
	x509 *x509.Certificate
}

func issue(t *testing.T, spec certSpec, parent *issued) *issued {
	t.Helper()

	eku := spec.eku
	if eku == nil && spec.oids == nil && !spec.ca {
		eku = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial.Add(1)),
		Subject:               pkix.Name{CommonName: spec.cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           eku,
		UnknownExtKeyUsage:    spec.oids,
		DNSNames:              spec.dns,
		BasicConstraintsValid: true,
		IsCA:                  spec.ca,
	}

	if spec.old {
		tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	}

	if spec.ca {
		tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature
	}

	for _, u := range spec.uris {
		pu, err := url.Parse(u)
		if err != nil {
			t.Fatal(err)
		}

		tmpl.URIs = append(tmpl.URIs, pu)
	}

	for _, ip := range spec.ips {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip.AsSlice())
	}

	signer, signerCert := rsaKey(), tmpl
	if parent != nil {
		signerCert = parent.x509
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, &rsaKey().PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return &issued{tls: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: rsaKey(), Leaf: cert}, x509: cert}
}

func pool(certs ...*issued) *x509.CertPool {
	p := x509.NewCertPool()
	for _, c := range certs {
		p.AddCert(c.x509)
	}

	return p
}

type node struct {
	tr    *transport.Transport
	rec   *siptest.Recorder
	local netip.AddrPort
}

func newNode(t *testing.T, server *tls.Config, client func(f sip.Flow) (transport.TLSOptions, error)) *node {
	t.Helper()

	tr, rec := siptest.NewTransport(t, transport.Config{TLSClientOptions: client})

	local, err := tr.ListenTLS(t.Context(), netip.AddrPortFrom(lo, 0), server)
	if err != nil {
		t.Fatal(err)
	}

	return &node{tr: tr, rec: rec, local: local}
}

func serverConfig(t *testing.T, o transport.TLSOptions) *tls.Config {
	t.Helper()

	cfg, err := transport.NewTLSServerConfig(o)
	if err != nil {
		t.Fatal(err)
	}

	return cfg
}

func clientConfig(t *testing.T, o transport.TLSOptions) func(sip.Flow) (transport.TLSOptions, error) {
	t.Helper()

	if _, err := transport.NewTLSClientConfig(o); err != nil {
		t.Fatal(err)
	}

	return func(sip.Flow) (transport.TLSOptions, error) { return o, nil }
}

var ellaIdentity = []string{"ims.ella.example"}

func (n *node) send(t *testing.T, to netip.AddrPort) (*sip.Request, error) {
	t.Helper()

	req := siptest.NewRequest("OPTIONS", "sip:"+to.String(), sip.TLS, n.local)
	req.Flow = sip.Flow{Transport: sip.TLS, Local: n.local, Remote: to}

	ctx, cancel := context.WithTimeout(t.Context(), siptest.Timeout)
	defer cancel()

	return req, n.tr.Send(ctx, req)
}

type pki struct {
	ca, other, server, client *issued
}

func newPKI(t *testing.T) pki {
	t.Helper()

	ca := issue(t, certSpec{cn: "Ella test CA", ca: true}, nil)
	other := issue(t, certSpec{cn: "Other CA", ca: true}, nil)

	return pki{
		ca:     ca,
		other:  other,
		server: issue(t, certSpec{cn: "ignored", dns: []string{"sip.carrier.example"}}, ca),
		client: issue(t, certSpec{uris: []string{"sip:ims.ella.example"}}, ca),
	}
}

func TestTLSVersions(t *testing.T) {
	p := newPKI(t)

	for _, tc := range []struct {
		name    string
		max     uint16
		version uint16
	}{
		{"TLS 1.3", 0, tls.VersionTLS13},
		{"TLS 1.2 only peer", tls.VersionTLS12, tls.VersionTLS12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got atomic.Pointer[tls.ConnectionState]

			scfg := serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.server.tls}})
			scfg.MaxVersion = tc.max
			verify := scfg.VerifyConnection
			scfg.VerifyConnection = func(cs tls.ConnectionState) error {
				got.Store(&cs)
				return verify(cs)
			}

			server := newNode(t, scfg, nil)
			client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}),
				clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

			req, err := client.send(t, server.local)
			if err != nil {
				t.Fatal(err)
			}

			in := server.rec.NextRequest()
			if in.Flow.Transport != sip.TLS || in.Flow.Local != server.local {
				t.Fatalf("flow %s", in.Flow)
			}

			if cs := got.Load(); cs.Version != tc.version {
				t.Errorf("version %x, want %x", cs.Version, tc.version)
			} else if tc.version == tls.VersionTLS12 && cs.CipherSuite != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
				t.Errorf("cipher suite %s", tls.CipherSuiteName(cs.CipherSuite))
			}

			res := sip.NewResponse(in, 200, "")
			res.Flow = in.Flow

			if err := server.tr.Send(t.Context(), res); err != nil {
				t.Fatal(err)
			}

			if r := client.rec.NextResponse(); r.StatusCode != 200 || r.Header.Get("Call-ID") != req.Header.Get("Call-ID") {
				t.Errorf("got %s", r.StartLine())
			}
		})
	}
}

func dialRaw(t *testing.T, to netip.AddrPort, cfg *tls.Config) (*tls.Conn, error) {
	t.Helper()

	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: siptest.Timeout}, Config: cfg}

	c, err := d.DialContext(t.Context(), "tcp", to.String())
	if err != nil {
		return nil, err
	}

	t.Cleanup(func() { _ = c.Close() })

	_ = c.SetDeadline(time.Now().Add(siptest.Timeout))

	return c.(*tls.Conn), nil
}

func TestTLSServerCipherPreferenceAndMinimumVersion(t *testing.T) {
	p := newPKI(t)
	server := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.server.tls}}), nil)

	c, err := dialRaw(t, server.local, &tls.Config{
		RootCAs:    pool(p.ca),
		ServerName: "sip.carrier.example",
		MaxVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if cs := c.ConnectionState(); cs.CipherSuite != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
		t.Errorf("cipher suite %s", tls.CipherSuiteName(cs.CipherSuite))
	}

	if _, err := dialRaw(t, server.local, &tls.Config{RootCAs: pool(p.ca), ServerName: "sip.carrier.example", MaxVersion: tls.VersionTLS11}); err == nil {
		t.Error("TLS 1.1 accepted")
	}

	if _, err := dialRaw(t, server.local, &tls.Config{
		RootCAs: pool(p.ca), ServerName: "sip.carrier.example", MaxVersion: tls.VersionTLS12,
		CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA},
	}); err == nil {
		t.Error("CBC suite accepted")
	}
}

func TestTLSClientRejectsServer(t *testing.T) {
	p := newPKI(t)
	selfSigned := issue(t, certSpec{dns: []string{"sip.carrier.example"}}, nil)
	otherSelfSigned := issue(t, certSpec{dns: []string{"sip.carrier.example"}}, nil)
	expired := issue(t, certSpec{dns: []string{"sip.carrier.example"}, old: true}, nil)
	clientOnly := issue(t, certSpec{dns: []string{"sip.carrier.example"}, eku: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, p.ca)
	sipOnly := issue(t, certSpec{dns: []string{"sip.carrier.example"}, oids: []asn1.ObjectIdentifier{sipDomain}}, p.ca)
	anyUsage := issue(t, certSpec{dns: []string{"sip.carrier.example"}, eku: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}, p.ca)
	noUsage := issue(t, certSpec{dns: []string{"sip.carrier.example"}, eku: []x509.ExtKeyUsage{}}, p.ca)
	otherServer := issue(t, certSpec{dns: []string{"sbc2.carrier.example"}}, p.ca)
	restricted := issue(t, certSpec{cn: "Restricted CA", ca: true, eku: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, p.ca)
	underRestricted := issue(t, certSpec{dns: []string{"sip.carrier.example"}}, restricted)
	underRestricted.tls.Certificate = append(underRestricted.tls.Certificate, restricted.x509.Raw)
	allowed := issue(t, certSpec{cn: "Server CA", ca: true, eku: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, p.ca)
	underAllowed := issue(t, certSpec{dns: []string{"sip.carrier.example"}}, allowed)
	underAllowed.tls.Certificate = append(underAllowed.tls.Certificate, allowed.x509.Raw)

	for _, tc := range []struct {
		name   string
		server *issued
		trust  transport.TLSOptions
		ok     bool
	}{
		{"CA", p.server, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}, true},
		{"untrusted CA", p.server, transport.TLSOptions{Roots: pool(p.other), ServerName: "sip.carrier.example"}, false},
		{"wrong identity", p.server, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.other.example"}, false},
		{"identity case", p.server, transport.TLSOptions{Roots: pool(p.ca), Identities: []string{"SIP.Carrier.Example"}}, true},
		{"pinned certificate", selfSigned, transport.TLSOptions{PinnedCertificates: []*x509.Certificate{selfSigned.x509}}, true},
		{"pinned fingerprint", selfSigned, transport.TLSOptions{PinnedFingerprints: [][sha256.Size]byte{transport.Fingerprint(selfSigned.x509)}}, true},
		{"wrong pinned certificate", selfSigned, transport.TLSOptions{PinnedCertificates: []*x509.Certificate{otherSelfSigned.x509}}, false},
		{"wrong pinned fingerprint", selfSigned, transport.TLSOptions{PinnedFingerprints: [][sha256.Size]byte{transport.Fingerprint(otherSelfSigned.x509)}}, false},
		{"self-signed with CA trust", selfSigned, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}, false},
		{"pinned and CA", p.server, transport.TLSOptions{Roots: pool(p.ca), PinnedCertificates: []*x509.Certificate{otherSelfSigned.x509}, ServerName: "sip.carrier.example"}, true},
		{"client-only key usage", clientOnly, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}, false},
		{"SIP domain key usage", sipOnly, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}, true},
		{"any key usage", anyUsage, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}, true},
		{"no key usage extension", noUsage, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}, true},
		{"CA restricted to clients", underRestricted, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}, false},
		{"CA restricted to servers", underAllowed, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}, true},
		{"pinned SIP domain key usage", sipOnly, transport.TLSOptions{PinnedCertificates: []*x509.Certificate{sipOnly.x509}}, true},
		{"another server of the trunk", otherServer, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example", Identities: []string{"sip.carrier.example", "sbc2.carrier.example"}}, false},
		{"identities without a server name", otherServer, transport.TLSOptions{Roots: pool(p.ca), ServerName: "192.0.2.1", Identities: []string{"sip.carrier.example", "sbc2.carrier.example"}}, true},
		{"pinned without the server name", otherServer, transport.TLSOptions{PinnedCertificates: []*x509.Certificate{otherServer.x509}, ServerName: "sip.carrier.example"}, true},
		{"pinned expired", expired, transport.TLSOptions{PinnedCertificates: []*x509.Certificate{expired.x509}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{tc.server.tls}}), nil)
			client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}), clientConfig(t, tc.trust))

			_, err := client.send(t, server.local)

			switch {
			case tc.ok && err != nil:
				t.Fatal(err)
			case tc.ok:
				server.rec.NextRequest()
			case err == nil:
				t.Fatal("sent over an untrusted connection")
			default:
				server.rec.None(50 * time.Millisecond)
			}
		})
	}
}

func TestTLSClientConfigNeedsTrust(t *testing.T) {
	if _, err := transport.NewTLSClientConfig(transport.TLSOptions{ServerName: "sip.carrier.example"}); err == nil {
		t.Error("client without trust accepted")
	}

	if _, err := transport.NewTLSClientConfig(transport.TLSOptions{Roots: x509.NewCertPool(), ServerName: "192.0.2.1"}); err == nil {
		t.Error("CA trust without an identity accepted")
	}

	if _, err := transport.NewTLSServerConfig(transport.TLSOptions{}); err == nil {
		t.Error("server without a certificate accepted")
	}

	cert := issue(t, certSpec{cn: "x.example"}, nil)
	if _, err := transport.NewTLSServerConfig(transport.TLSOptions{Certificates: []tls.Certificate{cert.tls}, Roots: x509.NewCertPool()}); err == nil {
		t.Error("server CA trust without client identities accepted")
	}

	if _, err := transport.NewTLSServerConfig(transport.TLSOptions{Certificates: []tls.Certificate{cert.tls}, PinnedCertificates: []*x509.Certificate{cert.x509}}); err != nil {
		t.Errorf("pinned server trust without identities: %v", err)
	}
}

func TestTLSMutualAuthentication(t *testing.T) {
	p := newPKI(t)
	stranger := issue(t, certSpec{uris: []string{"sip:ims.ella.example"}}, p.other)
	serverOnly := issue(t, certSpec{uris: []string{"sip:ims.ella.example"}, eku: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, p.ca)
	codeSigning := issue(t, certSpec{uris: []string{"sip:ims.ella.example"}, eku: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}}, p.ca)
	sipOnly := issue(t, certSpec{uris: []string{"sip:ims.ella.example"}, oids: []asn1.ObjectIdentifier{sipDomain}}, p.ca)
	clientOnly := issue(t, certSpec{uris: []string{"sip:ims.ella.example"}, eku: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, p.ca)
	anyUsage := issue(t, certSpec{uris: []string{"sip:ims.ella.example"}, eku: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}, p.ca)
	noUsage := issue(t, certSpec{uris: []string{"sip:ims.ella.example"}, eku: []x509.ExtKeyUsage{}}, p.ca)

	for _, tc := range []struct {
		name   string
		client []tls.Certificate
		ids    []string
		ok     bool
	}{
		{"client certificate", []tls.Certificate{p.client.tls}, nil, true},
		{"client identity", []tls.Certificate{p.client.tls}, []string{"ims.ella.example"}, true},
		{"wrong client identity", []tls.Certificate{p.client.tls}, []string{"pbx.example"}, false},
		{"no client certificate", nil, nil, false},
		{"untrusted client certificate", []tls.Certificate{stranger.tls}, nil, false},
		{"server-only key usage", []tls.Certificate{serverOnly.tls}, nil, false},
		{"code-signing key usage", []tls.Certificate{codeSigning.tls}, nil, false},
		{"client-only key usage", []tls.Certificate{clientOnly.tls}, nil, true},
		{"SIP domain key usage", []tls.Certificate{sipOnly.tls}, nil, true},
		{"any key usage", []tls.Certificate{anyUsage.tls}, nil, true},
		{"no key usage extension", []tls.Certificate{noUsage.tls}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.ids == nil {
				tc.ids = ellaIdentity
			}

			server := newNode(t, serverConfig(t, transport.TLSOptions{
				Certificates: []tls.Certificate{p.server.tls}, Roots: pool(p.ca), Identities: tc.ids, RequireClientCertificate: true,
			}), nil)
			client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}),
				clientConfig(t, transport.TLSOptions{Certificates: tc.client, Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

			_, err := client.send(t, server.local)

			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}

				server.rec.NextRequest()

				return
			}

			server.rec.None(100 * time.Millisecond)
		})
	}
}

func TestTLSInboundConnectionReused(t *testing.T) {
	p := newPKI(t)
	server := newNode(t, serverConfig(t, transport.TLSOptions{
		Certificates: []tls.Certificate{p.server.tls}, Roots: pool(p.ca), Identities: ellaIdentity,
	}), clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), Identities: ellaIdentity}))

	raw, err := dialRaw(t, server.local, &tls.Config{
		RootCAs: pool(p.ca), ServerName: "sip.carrier.example", Certificates: []tls.Certificate{p.client.tls},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := raw.Write([]byte(options("SIP/2.0/TLS 127.0.0.1:5061;branch=z9hG4bK1"))); err != nil {
		t.Fatal(err)
	}

	in := server.rec.NextRequest()
	if in.Flow.Remote != netip.MustParseAddrPort(raw.LocalAddr().String()) {
		t.Fatalf("flow %s", in.Flow)
	}

	peer := netip.AddrPortFrom(lo, closedPort(t))

	if _, err := server.send(t, peer); err != nil {
		t.Fatal(err)
	}

	m, _, err := sip.NewStreamReader(raw, 0).Next()
	if err != nil {
		t.Fatal(err)
	}

	if req, ok := m.(*sip.Request); !ok || req.Method != "OPTIONS" || req.URI.Port != peer.Port() {
		t.Errorf("got %s", m.StartLine())
	}
}

func TestTLSUnauthenticatedInboundConnectionNotReused(t *testing.T) {
	p := newPKI(t)
	server := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.server.tls}}),
		clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

	raw, err := dialRaw(t, server.local, &tls.Config{RootCAs: pool(p.ca), ServerName: "sip.carrier.example"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := raw.Write([]byte(options("SIP/2.0/TLS 127.0.0.1:5061;branch=z9hG4bK1"))); err != nil {
		t.Fatal(err)
	}

	server.rec.NextRequest()

	if _, err := server.send(t, netip.AddrPortFrom(lo, closedPort(t))); err == nil {
		t.Error("sent over an unauthenticated inbound connection")
	}
}

func TestTLSOutboundConnectionReused(t *testing.T) {
	p := newPKI(t)
	a := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}),
		clientConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}, Roots: pool(p.ca), ServerName: "sip.carrier.example"}))
	b := newNode(t, serverConfig(t, transport.TLSOptions{
		Certificates: []tls.Certificate{p.server.tls}, Roots: pool(p.ca), Identities: ellaIdentity, RequireClientCertificate: true,
	}), clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), Identities: ellaIdentity}))

	if _, err := a.send(t, b.local); err != nil {
		t.Fatal(err)
	}

	in := b.rec.NextRequest()
	if in.Flow.Remote != a.local {
		t.Fatalf("flow %s", in.Flow)
	}

	if _, err := a.send(t, b.local); err != nil {
		t.Fatal(err)
	}

	if again := b.rec.NextRequest(); again.Flow != in.Flow {
		t.Errorf("second request on %s, first on %s", again.Flow, in.Flow)
	}

	for _, to := range []netip.AddrPort{a.local, netip.AddrPortFrom(lo, closedPort(t))} {
		if _, err := b.send(t, to); err != nil {
			t.Fatalf("send to %s: %v", to, err)
		}

		if got := a.rec.NextRequest(); got.Flow.Remote != b.local {
			t.Errorf("got on %s", got.Flow)
		}
	}
}

func closedPort(t *testing.T) uint16 {
	t.Helper()

	var lc net.ListenConfig

	l, err := lc.Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	return uint16(port)
}

type rawServer struct {
	l     net.Listener
	conns chan *tls.Conn
}

func newRawServer(t *testing.T, cert tls.Certificate) *rawServer {
	t.Helper()

	l, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}

	s := &rawServer{l: l, conns: make(chan *tls.Conn, 16)}

	t.Cleanup(func() { _ = l.Close() })

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}

			t.Cleanup(func() { _ = c.Close() })

			s.conns <- c.(*tls.Conn)
		}
	}()

	return s
}

func (s *rawServer) addr() netip.AddrPort {
	return netip.MustParseAddrPort(s.l.Addr().String())
}

func (s *rawServer) next(t *testing.T) *tls.Conn {
	t.Helper()

	select {
	case c := <-s.conns:
		_ = c.SetDeadline(time.Now().Add(siptest.Timeout))
		return c
	case <-time.After(siptest.Timeout):
		t.Fatal("no connection")
		return nil
	}
}

func readKeepalive(t *testing.T, c *tls.Conn) {
	t.Helper()

	buf := make([]byte, 4)
	if _, err := c.Read(buf); err != nil || string(buf) != "\r\n\r\n" {
		t.Fatalf("got %q, %v", buf, err)
	}
}

func TestTLSPersistentConnection(t *testing.T) {
	p := newPKI(t)
	peer := newRawServer(t, p.server.tls)
	client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}),
		clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

	f := sip.Flow{Transport: sip.TLS, Local: client.local, Remote: peer.addr()}

	stop, err := client.tr.Persist(targets(f), transport.KeepAlive{Interval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	first := peer.next(t)
	readKeepalive(t, first)
	readKeepalive(t, first)

	_ = first.Close()

	start := time.Now()
	second := peer.next(t)

	if d := time.Since(start); d > time.Second {
		t.Errorf("reconnected after %s", d)
	}

	readKeepalive(t, second)

	if _, err := client.send(t, peer.addr()); err != nil {
		t.Fatal(err)
	}

	stop()

	_ = second.Close()

	select {
	case <-peer.conns:
		t.Error("reconnected after stop")
	case <-time.After(200 * time.Millisecond):
	}
}

func targets(flows ...sip.Flow) transport.Targets {
	return func(context.Context) ([]sip.Flow, error) { return flows, nil }
}

func TestTLSPersistentConnectionPong(t *testing.T) {
	p := newPKI(t)
	peer := newRawServer(t, p.server.tls)
	client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}),
		clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

	stop, err := client.tr.Persist(targets(sip.Flow{Transport: sip.TLS, Local: client.local, Remote: peer.addr()}),
		transport.KeepAlive{Interval: 20 * time.Millisecond, PongTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	defer stop()

	first := peer.next(t)

	for range 3 {
		readKeepalive(t, first)
	}

	select {
	case <-peer.conns:
		t.Fatal("reconnected although the peer never answers keep-alives")
	default:
	}

	if _, err := first.Write([]byte("\r\n")); err != nil {
		t.Fatal(err)
	}

	readKeepalive(t, first)

	second := peer.next(t)
	readKeepalive(t, second)
}

func TestTLSPersistentConnectionFailover(t *testing.T) {
	p := newPKI(t)
	peer := newRawServer(t, p.server.tls)
	client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}),
		clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

	dead := sip.Flow{Transport: sip.TLS, Local: client.local, Remote: netip.AddrPortFrom(lo, closedPort(t))}
	udp := sip.Flow{Transport: sip.UDP, Local: client.local, Remote: peer.addr()}
	alive := sip.Flow{Transport: sip.TLS, Local: client.local, Remote: peer.addr()}

	var resolved atomic.Int64

	stop, err := client.tr.Persist(func(context.Context) ([]sip.Flow, error) {
		resolved.Add(1)
		return []sip.Flow{dead, udp, alive}, nil
	}, transport.KeepAlive{Interval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	first := peer.next(t)
	readKeepalive(t, first)

	n := resolved.Load()

	_ = first.Close()

	second := peer.next(t)
	readKeepalive(t, second)

	if resolved.Load() <= n {
		t.Error("servers not resolved again after the connection was lost")
	}

	stop()

	_ = second.SetReadDeadline(time.Now().Add(siptest.Timeout))

	if _, err := second.Read(make([]byte, 16)); !errors.Is(err, io.EOF) {
		t.Errorf("read after stop: %v, want the connection closed", err)
	}
}

func TestPersistWithoutTargets(t *testing.T) {
	tr, _ := siptest.NewTransport(t, transport.Config{})

	if _, err := tr.Persist(nil, transport.KeepAlive{}); err == nil {
		t.Error("persistent connection without targets accepted")
	}
}

func TestTLSWithoutClientConfig(t *testing.T) {
	p := newPKI(t)
	server := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.server.tls}}), nil)
	client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}), nil)

	if _, err := client.send(t, server.local); !errors.Is(err, transport.ErrNoTLSConfig) {
		t.Errorf("err = %v", err)
	}
}

func TestCertificateIdentities(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec certSpec
		want []string
	}{
		{"SIP URI preferred over DNS", certSpec{cn: "cn.example", dns: []string{"dns.example"}, uris: []string{"sip:sip.example", "SIP:Upper.example"}}, []string{"sip.example", "Upper.example"}},
		{"SIP URI with parameters", certSpec{uris: []string{"sip:sip.example:5061;transport=tls"}}, []string{"sip.example"}},
		{"SIP URI with user ignored", certSpec{dns: []string{"dns.example"}, uris: []string{"sip:alice@sip.example"}}, []string{"dns.example"}},
		{"SIPS and HTTPS URIs ignored", certSpec{dns: []string{"dns.example"}, uris: []string{"sips:sip.example", "https://web.example"}}, []string{"dns.example"}},
		{"DNS names", certSpec{cn: "cn.example", dns: []string{"a.example", "b.example"}}, []string{"a.example", "b.example"}},
		{"CN without SAN", certSpec{cn: "cn.example"}, []string{"cn.example"}},
		{"CN ignored with an IP SAN", certSpec{cn: "cn.example", ips: []netip.Addr{lo}}, nil},
		{"CN not a DNS name", certSpec{cn: "Carrier SBC"}, nil},
		{"wildcard CN", certSpec{cn: "*.example"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := transport.CertificateIdentities(issue(t, tc.spec, nil).x509)
			if !slices.Equal(got, tc.want) {
				t.Errorf("identities %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchIdentity(t *testing.T) {
	for _, tc := range []struct {
		have, want string
		ok         bool
	}{
		{"sip.example", "SIP.EXAMPLE", true},
		{"*.example", "sip.example", false},
		{"*.example", "*.example", true},
		{".example", "sip.example", false},
		{"example", "sip.example", false},
		{"sip.example", "example", false},
	} {
		if got := transport.MatchIdentity([]string{tc.have}, []string{tc.want}); got != tc.ok {
			t.Errorf("%s vs %s: %v", tc.have, tc.want, got)
		}
	}
}

func TestTLSCommonNameFallback(t *testing.T) {
	p := newPKI(t)

	for _, tc := range []struct {
		name string
		spec certSpec
		ok   bool
	}{
		{"CN only", certSpec{cn: "sip.carrier.example"}, true},
		{"CN with another DNS SAN", certSpec{cn: "sip.carrier.example", dns: []string{"web.carrier.example"}}, false},
		{"SIP URI SAN", certSpec{cn: "x", dns: []string{"web.carrier.example"}, uris: []string{"sip:sip.carrier.example"}}, true},
		{"DNS SAN beside a matching SIP URI", certSpec{dns: []string{"sip.carrier.example"}, uris: []string{"sip:other.example"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert := issue(t, tc.spec, p.ca)
			server := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{cert.tls}}), nil)
			client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}),
				clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

			_, err := client.send(t, server.local)
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v, want ok %v", err, tc.ok)
			}

			if !tc.ok && !errors.Is(err, transport.ErrIdentityMismatch) {
				t.Errorf("err = %v, want an identity mismatch", err)
			}
		})
	}
}

func TestParseFingerprint(t *testing.T) {
	cert := issue(t, certSpec{cn: "x.example"}, nil)
	fp := transport.Fingerprint(cert.x509)

	var colons []byte

	for i, b := range fp {
		if i > 0 {
			colons = append(colons, ':')
		}

		colons = append(colons, "0123456789ABCDEF"[b>>4], "0123456789ABCDEF"[b&15])
	}

	got, err := transport.ParseFingerprint(string(colons))
	if err != nil || got != fp {
		t.Errorf("ParseFingerprint = %x, %v", got, err)
	}

	if _, err := transport.ParseFingerprint("AB:CD"); err == nil {
		t.Error("short fingerprint accepted")
	}
}

func TestTLSInboundConnectionWithOtherIdentityNotReused(t *testing.T) {
	p := newPKI(t)
	evil := issue(t, certSpec{uris: []string{"sip:evil.example"}}, p.ca)
	server := newNode(t, serverConfig(t, transport.TLSOptions{
		Certificates: []tls.Certificate{p.server.tls}, Roots: pool(p.ca), Identities: []string{"evil.example", "ims.ella.example"},
	}), clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

	raw, err := dialRaw(t, server.local, &tls.Config{
		RootCAs: pool(p.ca), ServerName: "sip.carrier.example", Certificates: []tls.Certificate{evil.tls},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := raw.Write([]byte(options("SIP/2.0/TLS 127.0.0.1:5061;branch=z9hG4bK1"))); err != nil {
		t.Fatal(err)
	}

	server.rec.NextRequest()

	if _, err := server.send(t, netip.AddrPortFrom(lo, closedPort(t))); err == nil {
		t.Error("request sent over an inbound connection with another identity")
	}

	_ = raw.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

	if m, _, err := sip.NewStreamReader(raw, 0).Next(); err == nil {
		t.Errorf("peer received %s", m.StartLine())
	}
}

func TestTLSUnauthenticatedInboundExactFlowNotUsedForRequests(t *testing.T) {
	p := newPKI(t)
	server := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.server.tls}}),
		clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

	port := closedPort(t)
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)}, Timeout: siptest.Timeout}

	nc, err := d.DialContext(t.Context(), "tcp4", server.local.String())
	if err != nil {
		t.Fatal(err)
	}

	raw := tls.Client(nc, &tls.Config{RootCAs: pool(p.ca), ServerName: "sip.carrier.example"})

	t.Cleanup(func() { _ = raw.Close() })

	if err := raw.HandshakeContext(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := raw.Write([]byte(options("SIP/2.0/TLS 127.0.0.1:5061;branch=z9hG4bK1"))); err != nil {
		t.Fatal(err)
	}

	in := server.rec.NextRequest()

	_, err = server.send(t, netip.AddrPortFrom(lo, port))
	if !errors.Is(err, transport.ErrUnauthorizedConnection) {
		t.Errorf("err = %v, want ErrUnauthorizedConnection", err)
	}

	res := sip.NewResponse(in, 200, "")
	res.Flow = in.Flow

	if err := server.tr.Send(t.Context(), res); err != nil {
		t.Fatal(err)
	}

	_ = raw.SetReadDeadline(time.Now().Add(siptest.Timeout))

	m, _, err := sip.NewStreamReader(raw, 0).Next()
	if err != nil {
		t.Fatal(err)
	}

	if r, ok := m.(*sip.Response); !ok || r.StatusCode != 200 {
		t.Errorf("got %s, want the response", m.StartLine())
	}
}

func TestTLSPersistBacksOffWhenRejected(t *testing.T) {
	p := newPKI(t)

	l, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{p.server.tls},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool(p.other),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = l.Close() })

	var accepted atomic.Int64

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}

			accepted.Add(1)

			go func() {
				_ = c.(*tls.Conn).HandshakeContext(t.Context())
				_ = c.Close()
			}()
		}
	}()

	client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}),
		clientConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}, Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

	stop, err := client.tr.Persist(targets(sip.Flow{Transport: sip.TLS, Local: client.local, Remote: netip.MustParseAddrPort(l.Addr().String())}),
		transport.KeepAlive{Interval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Second)
	stop()

	if n := accepted.Load(); n < 2 || n > 5 {
		t.Errorf("%d connections in 1s, want a few with backoff", n)
	}
}

func TestTLSClientCipherOrder(t *testing.T) {
	p := newPKI(t)
	offered := make(chan []uint16, 1)

	cfg := serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.server.tls}})
	cfg.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		offered <- hello.CipherSuites
		return nil, nil
	}

	server := newNode(t, cfg, nil)
	client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}),
		clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

	if _, err := client.send(t, server.local); err != nil {
		t.Fatal(err)
	}

	var tls12 []uint16

	for _, s := range <-offered {
		if slices.ContainsFunc(tls.CipherSuites(), func(c *tls.CipherSuite) bool {
			return c.ID == s && !slices.Contains(c.SupportedVersions, tls.VersionTLS13)
		}) {
			tls12 = append(tls12, s)
		}
	}

	if len(tls12) == 0 || tls12[0] != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
		t.Errorf("TLS 1.2 suites offered %v, want TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 first", tls12)
	}
}

func TestTLSPinnedClientWithoutIdentity(t *testing.T) {
	p := newPKI(t)
	pinned := issue(t, certSpec{uris: []string{"sip:pbx.example"}, eku: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, nil)

	server := newNode(t, serverConfig(t, transport.TLSOptions{
		Certificates: []tls.Certificate{p.server.tls}, Roots: pool(p.ca), Identities: ellaIdentity,
		PinnedFingerprints: [][sha256.Size]byte{transport.Fingerprint(pinned.x509)}, RequireClientCertificate: true,
	}), nil)
	client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}),
		clientConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{pinned.tls}, Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

	if _, err := client.send(t, server.local); err != nil {
		t.Fatal(err)
	}

	server.rec.NextRequest()
}

func TestClientTLSReservesPort(t *testing.T) {
	p := newPKI(t)
	server := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.server.tls}}), nil)

	tr, _ := siptest.NewTransport(t, transport.Config{
		TLSClientOptions: clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}),
	})

	local, err := tr.ClientTLS(t.Context(), netip.AddrPortFrom(lo, 0))
	if err != nil {
		t.Fatal(err)
	}

	var lc net.ListenConfig

	if l, err := lc.Listen(t.Context(), "tcp4", local.String()); err == nil {
		_ = l.Close()

		t.Fatalf("%s not reserved", local)
	}

	req := siptest.NewRequest("OPTIONS", "sip:"+server.local.String(), sip.TLS, local)
	req.Flow = sip.Flow{Transport: sip.TLS, Local: local, Remote: server.local}

	if err := tr.Send(t.Context(), req); err != nil {
		t.Fatal(err)
	}

	if in := server.rec.NextRequest(); in.Flow.Remote != local {
		t.Errorf("request from %s, want %s", in.Flow.Remote, local)
	}
}

func TestTLSPersistBacksOffWhenClosedAfterAMessage(t *testing.T) {
	p := newPKI(t)

	l, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{p.server.tls}})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = l.Close() })

	var accepted atomic.Int64

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}

			accepted.Add(1)

			go func() {
				_, _ = c.Write([]byte("OPTIONS sip:pbx.example SIP/2.0\r\nContent-Length: 0\r\n\r\n"))
				_ = c.Close()
			}()
		}
	}()

	client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}),
		clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

	stop, err := client.tr.Persist(targets(sip.Flow{Transport: sip.TLS, Local: client.local, Remote: netip.MustParseAddrPort(l.Addr().String())}),
		transport.KeepAlive{Interval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Second)
	stop()

	if n := accepted.Load(); n < 2 || n > 5 {
		t.Errorf("%d connections in 1s, want a few with backoff", n)
	}
}

func TestStopPersistDuringTheHandshake(t *testing.T) {
	p := newPKI(t)

	var lc net.ListenConfig

	l, err := lc.Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = l.Close() })

	conns := make(chan net.Conn, 4)

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}

			t.Cleanup(func() { _ = c.Close() })

			conns <- c
		}
	}()

	client := newNode(t, serverConfig(t, transport.TLSOptions{Certificates: []tls.Certificate{p.client.tls}}),
		clientConfig(t, transport.TLSOptions{Roots: pool(p.ca), ServerName: "sip.carrier.example"}))

	stop, err := client.tr.Persist(targets(sip.Flow{Transport: sip.TLS, Local: client.local, Remote: netip.MustParseAddrPort(l.Addr().String())}),
		transport.KeepAlive{Interval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	var raw net.Conn

	select {
	case raw = <-conns:
	case <-time.After(siptest.Timeout):
		t.Fatal("no connection")
	}

	stop()

	server := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{p.server.tls}})
	_ = server.SetDeadline(time.Now().Add(siptest.Timeout))

	if err := server.HandshakeContext(t.Context()); err == nil {
		if _, err := server.Read(make([]byte, 16)); !errors.Is(err, io.EOF) {
			t.Errorf("read after stop: %v, want the connection closed", err)
		}
	}

	select {
	case <-conns:
		t.Error("reconnected after stop")
	case <-time.After(200 * time.Millisecond):
	}
}
