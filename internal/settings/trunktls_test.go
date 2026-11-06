package settings

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip/transport"
)

type testCert struct {
	der []byte
	key *ecdsa.PrivateKey
}

func newTestCert(t *testing.T, cn string, ca bool, parent *testCert, notAfter time.Time, dns ...string) testCert {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     notAfter,
		DNSNames:     dns,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	if ca {
		tmpl.IsCA = true
		tmpl.BasicConstraintsValid = true
		tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature
		tmpl.ExtKeyUsage = nil
	}

	signer, signerKey := tmpl, key

	if parent != nil {
		p, err := x509.ParseCertificate(parent.der)
		if err != nil {
			t.Fatal(err)
		}

		signer, signerKey = p, parent.key
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}

	return testCert{der: der, key: key}
}

func (c testCert) tls(chain ...testCert) tls.Certificate {
	out := tls.Certificate{Certificate: [][]byte{c.der}, PrivateKey: c.key}
	for _, p := range chain {
		out.Certificate = append(out.Certificate, p.der)
	}

	return out
}

func inAYear() time.Time { return time.Now().Add(365 * 24 * time.Hour) }

func tlsTrunk(tlsCfg TrunkTLS) Trunk {
	tr := telnyx()
	tr.Addresses = []TrunkAddress{{Host: "sip.telnyx.com", Transport: SIPTransportTLS}, {Host: "192.0.2.1", Port: 5061, Transport: SIPTransportTLS}}
	tr.TLS = tlsCfg

	return tr
}

func TestTrunkTLSValidate(t *testing.T) {
	ca := newTestCert(t, "Carrier CA", true, nil, inAYear())
	leaf := newTestCert(t, "sip.telnyx.com", false, &ca, inAYear(), "sip.telnyx.com")
	other := newTestCert(t, "sbc.example.org", false, nil, inAYear(), "sbc.example.org")
	expired := newTestCert(t, "old.telnyx.com", false, nil, time.Now().Add(-time.Hour))
	fp := sha256.Sum256(leaf.der)

	for name, tr := range map[string]Trunk{
		"CA":                    tlsTrunk(TrunkTLS{CACertificates: [][]byte{ca.der}, Identities: []string{"sip.telnyx.com"}}),
		"pinned certificate":    tlsTrunk(TrunkTLS{PinnedCertificates: [][]byte{leaf.der, other.der}}),
		"pinned fingerprint":    tlsTrunk(TrunkTLS{PinnedFingerprints: [][sha256.Size]byte{fp}, Required: true}),
		"client certificate":    tlsTrunk(TrunkTLS{PinnedFingerprints: [][sha256.Size]byte{fp}, RequireClientCertificate: true}),
		"expired, already kept": tlsTrunk(TrunkTLS{PinnedCertificates: [][]byte{expired.der}}),
		"mixed transports": func() Trunk {
			tr := tlsTrunk(TrunkTLS{PinnedFingerprints: [][sha256.Size]byte{fp}})
			tr.Addresses = append(tr.Addresses, TrunkAddress{Host: "sip.telnyx.com", Transport: SIPTransportTCP})

			return tr
		}(),
		"inbound only": func() Trunk {
			tr := telnyx()
			tr.TLS = TrunkTLS{PinnedFingerprints: [][sha256.Size]byte{fp}, RequireClientCertificate: true}

			return tr
		}(),
	} {
		if err := tr.Validate(); err != nil {
			t.Errorf("Validate(%s): %v", name, err)
		}
	}

	many := func(n int) [][]byte {
		var ders [][]byte
		for range n {
			ders = append(ders, newTestCert(t, "x", false, nil, inAYear()).der)
		}

		return ders
	}

	tests := map[string]struct {
		trunk Trunk
		want  string
	}{
		"bad certificate": {
			tlsTrunk(TrunkTLS{PinnedCertificates: [][]byte{[]byte("not DER")}}),
			"tls.pinned_certificates holds a certificate that does not parse",
		},
		"leaf as CA": {
			tlsTrunk(TrunkTLS{CACertificates: [][]byte{leaf.der}, Identities: []string{"sip.telnyx.com"}}),
			"tls.ca_certificates must hold CA certificates, and CN=sip.telnyx.com is not one: pin it in tls.pinned_certificates instead",
		},
		"CA without identities": {
			tlsTrunk(TrunkTLS{CACertificates: [][]byte{ca.der}}),
			"tls.identities must list the carrier's domain names when tls.ca_certificates is set",
		},
		"identity is URI": {
			tlsTrunk(TrunkTLS{CACertificates: [][]byte{ca.der}, Identities: []string{"sip:sip.telnyx.com"}}),
			`tls.identities must be domain names, not "sip:sip.telnyx.com"`,
		},
		"identity twice": {
			tlsTrunk(TrunkTLS{CACertificates: [][]byte{ca.der}, Identities: []string{"sip.telnyx.com", "SIP.telnyx.com"}}),
			"tls.identities lists SIP.telnyx.com twice",
		},
		"certificate twice": {
			tlsTrunk(TrunkTLS{PinnedCertificates: [][]byte{leaf.der, leaf.der}}),
			"tls.pinned_certificates holds CN=sip.telnyx.com twice",
		},
		"fingerprint twice": {
			tlsTrunk(TrunkTLS{PinnedFingerprints: [][sha256.Size]byte{fp, fp}}),
			"tls.pinned_fingerprints lists " + FormatFingerprint(fp) + " twice",
		},
		"required with tcp": {
			func() Trunk {
				tr := tlsTrunk(TrunkTLS{PinnedFingerprints: [][sha256.Size]byte{fp}, Required: true})
				tr.Addresses = append(tr.Addresses, TrunkAddress{Host: "sip.telnyx.com", Port: 5060, Transport: SIPTransportTCP})

				return tr
			}(),
			"addresses must all have tls as transport when tls.required is set, not sip.telnyx.com:5060 over tcp",
		},
		"required without transport": {
			func() Trunk {
				tr := tlsTrunk(TrunkTLS{PinnedFingerprints: [][sha256.Size]byte{fp}, Required: true})
				tr.Addresses = []TrunkAddress{{Host: "sip.telnyx.com"}}

				return tr
			}(),
			"addresses must all have tls as transport when tls.required is set, not sip.telnyx.com",
		},
		"tls without trust": {
			tlsTrunk(TrunkTLS{}),
			"addresses over tls need tls.ca_certificates, tls.pinned_certificates or tls.pinned_fingerprints",
		},
		"required without trust": {
			tlsTrunk(TrunkTLS{Required: true}),
			"tls must have ca_certificates, pinned_certificates or pinned_fingerprints",
		},
		"client certificate without trust": {
			func() Trunk {
				tr := telnyx()
				tr.TLS = TrunkTLS{RequireClientCertificate: true}

				return tr
			}(),
			"tls must have ca_certificates, pinned_certificates or pinned_fingerprints",
		},
		"too many certificates": {
			tlsTrunk(TrunkTLS{PinnedCertificates: many(MaxTrunkCertificates + 1)}),
			"tls.pinned_certificates must hold at most 32 certificates",
		},
		"too many fingerprints": {
			tlsTrunk(TrunkTLS{PinnedFingerprints: make([][sha256.Size]byte, MaxTrunkFingerprints+1)}),
			"tls.pinned_fingerprints must list at most 32 fingerprints",
		},
		"too many identities": {
			tlsTrunk(TrunkTLS{CACertificates: [][]byte{ca.der}, Identities: strings.Split(strings.Repeat("a.example.org,", MaxTrunkIdentities+1), ",")}),
			"tls.identities must list at most 16 identities",
		},
		"oversized certificate": {
			tlsTrunk(TrunkTLS{PinnedCertificates: [][]byte{make([]byte, MaxTrunkCertificateDER+1)}}),
			"tls.pinned_certificates must hold certificates of at most 16384 bytes",
		},
	}

	for name, tt := range tests {
		err := tt.trunk.Validate()
		if err == nil || !errors.Is(err, ErrInvalid) || !strings.HasPrefix(err.Error(), tt.want) {
			t.Errorf("%s: Validate = %v, want %q", name, err, tt.want)
		}
	}
}

func TestParseCertificatesPEM(t *testing.T) {
	ca := newTestCert(t, "Carrier CA", true, nil, inAYear())
	leaf := newTestCert(t, "sip.telnyx.com", false, &ca, inAYear())

	bundle := "\n" + EncodeCertificatesPEM([][]byte{ca.der, leaf.der}) + "\n"

	ders, err := ParseCertificatesPEM("tls.ca_certificates", bundle)
	if err != nil || len(ders) != 2 || string(ders[0]) != string(ca.der) || string(ders[1]) != string(leaf.der) {
		t.Fatalf("ParseCertificatesPEM = %d certificates, %v", len(ders), err)
	}

	if ders, err := ParseCertificatesPEM("tls.ca_certificates", ""); err != nil || ders != nil {
		t.Fatalf("ParseCertificatesPEM of nothing = %v, %v, want nil", ders, err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(leaf.key)
	if err != nil {
		t.Fatal(err)
	}

	withKey := bundle + string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	withHeaders := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: leaf.der}))

	for in, want := range map[string]string{
		withKey:            "tls.pinned_certificates must hold only CERTIFICATE PEM blocks, not PRIVATE KEY",
		bundle + "garbage": "tls.pinned_certificates must be PEM-encoded certificates",
		"MIIB":             "tls.pinned_certificates must be PEM-encoded certificates",
		withHeaders:        "tls.pinned_certificates must hold PEM blocks without headers",
	} {
		if _, err := ParseCertificatesPEM("tls.pinned_certificates", in); !errors.Is(err, ErrInvalid) || err.Error() != want {
			t.Errorf("ParseCertificatesPEM = %v, want %q", err, want)
		}
	}
}

func TestParseFingerprint(t *testing.T) {
	leaf := newTestCert(t, "sip.telnyx.com", false, nil, inAYear())
	fp := sha256.Sum256(leaf.der)
	formatted := FormatFingerprint(fp)

	if len(formatted) != 95 || strings.ToUpper(formatted) != formatted {
		t.Fatalf("FormatFingerprint = %q, want 32 upper-case hex bytes separated by colons", formatted)
	}

	for _, s := range []string{formatted, strings.ToLower(strings.ReplaceAll(formatted, ":", "")), " " + formatted + " "} {
		if got, err := ParseFingerprint(s); err != nil || got != fp {
			t.Errorf("ParseFingerprint(%q) = %x, %v", s, got, err)
		}
	}

	for _, s := range []string{"", "AB:CD", formatted + ":00", strings.Replace(formatted, "A", "G", 1)} {
		if _, err := ParseFingerprint(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseFingerprint(%q) = %v, want invalid", s, err)
		}
	}
}

func TestLiveTrunkTLSExpiry(t *testing.T) {
	current := newTestCert(t, "sip.telnyx.com", false, nil, inAYear(), "sip.telnyx.com")
	expired := newTestCert(t, "old.telnyx.com", false, nil, time.Now().Add(-time.Hour))
	expiredCA := newTestCert(t, "Old CA", true, nil, time.Now().Add(-time.Hour))

	live := NewLive(&fakeStore{}, validSettings(), nil)

	for _, tt := range []struct {
		tls  TrunkTLS
		want string
	}{
		{TrunkTLS{PinnedCertificates: [][]byte{current.der, expired.der}}, "tls.pinned_certificates holds CN=old.telnyx.com, which expired on "},
		{TrunkTLS{CACertificates: [][]byte{expiredCA.der}, Identities: []string{"sip.telnyx.com"}}, "tls.ca_certificates holds CN=Old CA, which expired on "},
	} {
		if _, err := live.CreateTrunk(t.Context(), tlsTrunk(tt.tls)); !errors.Is(err, ErrInvalid) || !strings.HasPrefix(err.Error(), tt.want) {
			t.Errorf("CreateTrunk with an expired certificate = %v, want %q", err, tt.want)
		}
	}

	if n := len(live.Get().Trunks); n != 0 {
		t.Fatalf("%d trunks, want none", n)
	}

	kept := tlsTrunk(TrunkTLS{PinnedCertificates: [][]byte{expired.der}})
	kept.ID = "0192a000-0000-7000-8000-000000000011"

	initial := validSettings()
	initial.Trunks = []Trunk{kept}
	live = NewLive(&fakeStore{}, initial, nil)

	kept.Enabled = false

	if err := live.UpdateTrunk(t.Context(), kept); err != nil {
		t.Fatalf("UpdateTrunk keeping an expired certificate: %v", err)
	}

	kept.TLS.PinnedCertificates = append(kept.TLS.PinnedCertificates, expiredCA.der)

	if err := live.UpdateTrunk(t.Context(), kept); !errors.Is(err, ErrInvalid) ||
		!strings.HasPrefix(err.Error(), "tls.pinned_certificates holds CN=Old CA, which expired on ") {
		t.Fatalf("UpdateTrunk adding an expired certificate = %v, want invalid", err)
	}

	kept.TLS.PinnedCertificates = [][]byte{current.der}

	if err := live.UpdateTrunk(t.Context(), kept); err != nil {
		t.Fatalf("UpdateTrunk replacing the expired certificate: %v", err)
	}
}

func TestTrunkTLSOptions(t *testing.T) {
	ca := newTestCert(t, "Carrier CA", true, nil, inAYear())
	carrier := newTestCert(t, "sip.telnyx.com", false, &ca, inAYear(), "sip.telnyx.com")
	impostor := newTestCert(t, "evil.example.org", false, &ca, inAYear(), "evil.example.org")
	selfSigned := newTestCert(t, "sbc.example.org", false, nil, inAYear(), "sbc.example.org")
	ella := newTestCert(t, "ims.example.org", false, nil, inAYear(), "ims.example.org")

	byCA := TrunkTLS{CACertificates: [][]byte{ca.der}, Identities: []string{"sip.telnyx.com"}, RequireClientCertificate: true}
	byPin := TrunkTLS{PinnedCertificates: [][]byte{selfSigned.der}}
	byFingerprint := TrunkTLS{PinnedFingerprints: [][sha256.Size]byte{sha256.Sum256(selfSigned.der)}, Identities: []string{"sbc.example.org"}}

	o, err := byCA.ClientOptions("sip.telnyx.com")
	if err != nil || o.Roots == nil || o.ServerName != "sip.telnyx.com" || len(o.Identities) != 1 || o.RequireClientCertificate || len(o.Certificates) != 0 {
		t.Fatalf("ClientOptions = %+v, %v", o, err)
	}

	in, err := byCA.InboundOptions()
	if err != nil || in.Roots == nil || in.ServerName != "" || !in.RequireClientCertificate {
		t.Fatalf("InboundOptions = %+v, %v", in, err)
	}

	if _, err := (TrunkTLS{}).ClientOptions("sip.telnyx.com"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ClientOptions without trust = %v, want invalid", err)
	}

	for _, tt := range []struct {
		name   string
		trunk  TrunkTLS
		server testCert
		chain  []testCert
		ok     bool
	}{
		{"CA", byCA, carrier, []testCert{ca}, true},
		{"CA, another identity", byCA, impostor, []testCert{ca}, false},
		{"CA, self-signed", byCA, selfSigned, nil, false},
		{"pinned certificate", byPin, selfSigned, nil, true},
		{"pinned certificate, another", byPin, carrier, []testCert{ca}, false},
		{"pinned fingerprint", byFingerprint, selfSigned, nil, true},
	} {
		client, err := tt.trunk.ClientOptions("sip.telnyx.com")
		if err != nil {
			t.Fatal(err)
		}

		if tt.trunk.Identities == nil {
			client.ServerName = "sbc.example.org"
		}

		client.Certificates = []tls.Certificate{ella.tls()}

		cc, err := transport.NewTLSClientConfig(client)
		if err != nil {
			t.Fatalf("%s: NewTLSClientConfig: %v", tt.name, err)
		}

		inbound, err := tt.trunk.InboundOptions()
		if err != nil {
			t.Fatal(err)
		}

		inbound.Certificates = []tls.Certificate{ella.tls()}

		if _, err := transport.NewTLSServerConfig(inbound); err != nil {
			t.Fatalf("%s: NewTLSServerConfig: %v", tt.name, err)
		}

		sc := &tls.Config{Certificates: []tls.Certificate{tt.server.tls(tt.chain...)}, MinVersion: tls.VersionTLS12}

		if err := handshake(t, cc, sc); (err == nil) != tt.ok {
			t.Errorf("%s: handshake = %v, want success %v", tt.name, err, tt.ok)
		}
	}
}

func TestTrunkTLSInboundHandshake(t *testing.T) {
	ca := newTestCert(t, "Carrier CA", true, nil, inAYear())
	carrier := newTestCert(t, "sip.telnyx.com", false, &ca, inAYear(), "sip.telnyx.com")
	impostor := newTestCert(t, "evil.example.org", false, &ca, inAYear(), "evil.example.org")
	ella := newTestCert(t, "ims.example.org", false, nil, inAYear(), "ims.example.org")
	ellaFP := sha256.Sum256(ella.der)

	trunk := TrunkTLS{CACertificates: [][]byte{ca.der}, Identities: []string{"sip.telnyx.com"}, RequireClientCertificate: true}

	in, err := trunk.InboundOptions()
	if err != nil {
		t.Fatal(err)
	}

	in.Certificates = []tls.Certificate{ella.tls()}

	sc, err := transport.NewTLSServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name   string
		client *tls.Certificate
		ok     bool
	}{
		{"carrier", new(carrier.tls(ca)), true},
		{"impostor", new(impostor.tls(ca)), false},
		{"no certificate", nil, false},
	} {
		cc, err := transport.NewTLSClientConfig(transport.TLSOptions{PinnedFingerprints: [][sha256.Size]byte{ellaFP}})
		if err != nil {
			t.Fatal(err)
		}

		if tt.client != nil {
			cc.Certificates = []tls.Certificate{*tt.client}
			cc.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return tt.client, nil }
		}

		if err := handshake(t, cc, sc); (err == nil) != tt.ok {
			t.Errorf("%s: handshake = %v, want success %v", tt.name, err, tt.ok)
		}
	}
}

func handshake(t *testing.T, client, server *tls.Config) error {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = l.Close() }()

	done := make(chan error, 1)

	go func() {
		nc, err := l.Accept()
		if err != nil {
			done <- err
			return
		}

		defer func() { _ = nc.Close() }()

		s := tls.Server(nc, server)

		err = s.HandshakeContext(t.Context())
		if err == nil {
			_, err = s.Write([]byte{0})
		}

		done <- err
	}()

	nc, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = nc.Close() }()

	c := tls.Client(nc, client)

	err = c.HandshakeContext(t.Context())
	if err == nil {
		_, err = c.Read(make([]byte, 1))
	}

	if serr := <-done; err == nil {
		err = serr
	}

	return err
}
