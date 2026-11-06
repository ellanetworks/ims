package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
)

type apiTestCert struct {
	der []byte
	key *ecdsa.PrivateKey
}

func newAPITestCert(t *testing.T, cn string, ca bool, notAfter time.Time) apiTestCert {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"Telnyx"}},
		NotBefore:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     notAfter,
		IsCA:         ca, BasicConstraintsValid: ca,
	}

	if !ca {
		tmpl.DNSNames = []string{cn}
		tmpl.URIs = []*url.URL{{Scheme: "sip", Opaque: cn}}
		tmpl.IPAddresses = []net.IP{net.ParseIP("192.0.2.1")}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	return apiTestCert{der: der, key: key}
}

func (c apiTestCert) pem() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.der}))
}

func tlsTrunkBody(t *testing.T, addresses []map[string]any, tls map[string]any) string {
	t.Helper()

	b, err := json.Marshal(map[string]any{
		"name": "Telnyx", "addresses": addresses, "inbound_cidrs": []string{"192.76.120.10/32"},
		"domain": "sip.telnyx.com", "enabled": true, "tls": tls,
	})
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func TestSIPTrunkTLS(t *testing.T) {
	cfg := Config{Settings: newFakeSettings()}

	notAfter := time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC)
	ca := newAPITestCert(t, "Telnyx CA", true, notAfter)
	pinned := newAPITestCert(t, "sip.telnyx.com", false, notAfter)
	fp := sha256.Sum256([]byte("another certificate"))

	tlsOnly := []map[string]any{{"host": "sip.telnyx.com", "transport": "tls"}, {"host": "192.0.2.1", "port": 5061, "transport": "tls"}}

	created := createTrunk(t, cfg, tlsTrunkBody(t, tlsOnly, map[string]any{
		"required":        true,
		"ca_certificates": "\n" + ca.pem(),
		"pins":            []string{pinned.pem(), strings.ToLower(settings.FormatFingerprint(fp))},
	}))

	wantTLS := SIPTrunkTLS{
		SIPTrunkTLSParams: SIPTrunkTLSParams{
			Required:       true,
			CACertificates: ca.pem(),
			Pins:           []string{settings.FormatFingerprint(sha256.Sum256(pinned.der)), settings.FormatFingerprint(fp)},
			Identities:     []string{"sip.telnyx.com"},
		},
		CACertificateDetails: []CertificateSummary{{
			Subject: "CN=Telnyx CA,O=Telnyx", Issuer: "CN=Telnyx CA,O=Telnyx", DNSNames: []string{}, URIs: []string{},
			IPAddresses: []string{}, Identities: []string{}, CA: true,
			NotBefore: "2026-01-01T00:00:00.000Z", NotAfter: "2036-01-01T00:00:00.000Z",
			SHA256Fingerprint: settings.FormatFingerprint(sha256.Sum256(ca.der)),
		}},
	}
	if !reflect.DeepEqual(created.TLS, wantTLS) {
		t.Fatalf("tls = %+v, want %+v", created.TLS, wantTLS)
	}

	if created.Addresses[0].Transport != "tls" || created.SIPTrunkParams.TLS != nil {
		t.Fatalf("created = %+v, want tls addresses and tls at the top level", created)
	}

	stored, _ := cfg.Settings.Get().Trunk(created.ID)
	if !stored.TLS.Required || len(stored.TLS.CACertificates) != 1 || len(stored.TLS.Pins) != 2 ||
		stored.TLS.Pins[0] != sha256.Sum256(pinned.der) || stored.TLS.Pins[1] != fp {
		t.Fatalf("stored = %+v", stored.TLS)
	}

	code, body := serve(t, cfg, http.MethodGet, "/api/v1/sip-trunks/"+created.ID, "")
	if got := decodeResult[SIPTrunk](t, body); code != http.StatusOK || !reflect.DeepEqual(got, created) {
		t.Fatalf("GET = %d %s, want %+v", code, body, created)
	}

	if !strings.Contains(string(body), `"pins":["`+settings.FormatFingerprint(sha256.Sum256(pinned.der))+`"`) {
		t.Fatalf("GET = %s, want the fingerprint of the pinned certificate", body)
	}

	code, body = serve(t, cfg, http.MethodPut, "/api/v1/sip-trunks/"+created.ID,
		tlsTrunkBody(t, []map[string]any{{"host": "sip.telnyx.com", "transport": "tcp"}}, nil))
	if got := decodeResult[SIPTrunk](t, body); code != http.StatusOK || got.TLS.Required || got.TLS.CACertificates != "" ||
		len(got.TLS.Pins) != 0 || len(got.TLS.CACertificateDetails) != 0 {
		t.Fatalf("PUT without tls = %d %s, want TLS removed", code, body)
	}

	if stored, _ := cfg.Settings.Get().Trunk(created.ID); !reflect.DeepEqual(stored.TLS, settings.TrunkTLS{}) {
		t.Fatalf("stored = %+v, want no TLS", stored.TLS)
	}
}

func TestSIPTrunkTLSRejected(t *testing.T) {
	notAfter := time.Now().Add(24 * time.Hour)
	leaf := newAPITestCert(t, "sip.telnyx.com", false, notAfter)
	expired := newAPITestCert(t, "old.telnyx.com", false, time.Now().Add(-time.Hour))

	keyDER, err := x509.MarshalPKCS8PrivateKey(leaf.key)
	if err != nil {
		t.Fatal(err)
	}

	key := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	tlsOnly := []map[string]any{{"host": "sip.telnyx.com", "transport": "tls"}}
	tcp := []map[string]any{{"host": "sip.telnyx.com", "transport": "tcp"}}

	tests := []struct {
		name      string
		addresses []map[string]any
		tls       map[string]any
		want      string
	}{
		{"bad PEM", tlsOnly, map[string]any{"ca_certificates": "MIIB"}, "tls.ca_certificates must be PEM-encoded certificates"},
		{"bad PEM pin", tlsOnly, map[string]any{"pins": []string{"-----BEGIN CERTIFICATE-----\nMIIB"}}, "tls.pins must be PEM-encoded certificates"},
		{"private key", tlsOnly, map[string]any{"pins": []string{leaf.pem() + key}}, "tls.pins must hold only CERTIFICATE PEM blocks, not PRIVATE KEY"},
		{"bad DER", tlsOnly, map[string]any{"ca_certificates": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")})), "identities": []string{"sip.telnyx.com"}}, "tls.ca_certificates holds a certificate that does not parse"},
		{"bad fingerprint", tlsOnly, map[string]any{"pins": []string{"AB:CD"}}, `tls.pins must hold PEM certificates or SHA-256 fingerprints of 32 hex bytes, not "AB:CD"`},
		{"pin twice", tlsOnly, map[string]any{"pins": []string{leaf.pem(), settings.FormatFingerprint(sha256.Sum256(leaf.der))}}, "tls.pins lists "},
		{"leaf as CA", tlsOnly, map[string]any{"ca_certificates": leaf.pem(), "identities": []string{"sip.telnyx.com"}}, "tls.ca_certificates must hold CA certificates"},
		{"required with tcp", tcp, map[string]any{"required": true, "pins": []string{leaf.pem()}}, "addresses must all have tls as transport when tls.required is set, not sip.telnyx.com over tcp"},
		{"tls without trust", tlsOnly, map[string]any{"identities": []string{"sip.telnyx.com"}}, "tls must have ca_certificates or pins"},
		{"expired", tlsOnly, map[string]any{"pins": []string{leaf.pem(), expired.pem()}}, "tls.pins holds CN=old.telnyx.com,O=Telnyx, which expired on "},
		{"details on input", tlsOnly, map[string]any{"pins": []string{leaf.pem()}, "ca_certificate_details": []any{}}, "Invalid request data"},
		{"removed field", tlsOnly, map[string]any{"pinned_fingerprints": []string{}}, "Invalid request data"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Settings: newFakeSettings()}

			code, body := serve(t, cfg, http.MethodPost, "/api/v1/sip-trunks", tlsTrunkBody(t, tt.addresses, tt.tls))
			if code != http.StatusBadRequest || !strings.HasPrefix(decodeError(t, body), tt.want) {
				t.Fatalf("POST = %d %s, want 400 %q", code, body, tt.want)
			}

			if n := len(cfg.Settings.Get().Trunks); n != 0 {
				t.Fatalf("%d trunks, want none", n)
			}
		})
	}
}
