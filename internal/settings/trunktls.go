package settings

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/ims/sip/transport"
)

const (
	MaxTrunkCertificates   = 32
	MaxTrunkFingerprints   = 32
	MaxTrunkIdentities     = 16
	MaxTrunkCertificateDER = 16 << 10
)

// SIPconnect 2.0 section 8.1
// SIPconnect 2.0 section 17.2
// SIPconnect 2.0 section 17.5
type TrunkTLS struct {
	Required                 bool
	RequireClientCertificate bool
	CACertificates           [][]byte
	PinnedCertificates       [][]byte
	PinnedFingerprints       [][sha256.Size]byte
	Identities               []string
}

func (t TrunkTLS) Trusts() bool {
	return len(t.CACertificates) > 0 || len(t.PinnedCertificates) > 0 || len(t.PinnedFingerprints) > 0
}

func (t TrunkTLS) configured() bool {
	return t.Trusts() || t.Required || t.RequireClientCertificate || len(t.Identities) > 0
}

// SIPconnect 2.0 section 17.2
func (t TrunkTLS) Validate() error {
	switch {
	case len(t.CACertificates) > MaxTrunkCertificates:
		return invalidf("tls.ca_certificates must hold at most %d certificates", MaxTrunkCertificates)
	case len(t.PinnedCertificates) > MaxTrunkCertificates:
		return invalidf("tls.pinned_certificates must hold at most %d certificates", MaxTrunkCertificates)
	case len(t.PinnedFingerprints) > MaxTrunkFingerprints:
		return invalidf("tls.pinned_fingerprints must list at most %d fingerprints", MaxTrunkFingerprints)
	case len(t.Identities) > MaxTrunkIdentities:
		return invalidf("tls.identities must list at most %d identities", MaxTrunkIdentities)
	}

	if err := validateCertificates("tls.ca_certificates", t.CACertificates, true); err != nil {
		return err
	}

	if err := validateCertificates("tls.pinned_certificates", t.PinnedCertificates, false); err != nil {
		return err
	}

	for i, fp := range t.PinnedFingerprints {
		if slices.Contains(t.PinnedFingerprints[:i], fp) {
			return invalidf("tls.pinned_fingerprints lists %s twice", FormatFingerprint(fp))
		}
	}

	for i, id := range t.Identities {
		if !isSIPHostname(id) {
			return invalidf("tls.identities must be domain names, not %q", id)
		}

		if slices.ContainsFunc(t.Identities[:i], func(o string) bool { return strings.EqualFold(o, id) }) {
			return invalidf("tls.identities lists %s twice", id)
		}
	}

	switch {
	case t.configured() && !t.Trusts():
		return invalidf("tls must have ca_certificates, pinned_certificates or pinned_fingerprints")
	case len(t.CACertificates) > 0 && len(t.Identities) == 0:
		return invalidf("tls.identities must list the carrier's domain names when tls.ca_certificates is set")
	}

	return nil
}

func validateCertificates(field string, ders [][]byte, ca bool) error {
	for i, der := range ders {
		if len(der) > MaxTrunkCertificateDER {
			return invalidf("%s must hold certificates of at most %d bytes", field, MaxTrunkCertificateDER)
		}

		c, err := x509.ParseCertificate(der)
		if err != nil {
			return invalidf("%s holds a certificate that does not parse: %v", field, err)
		}

		if ca && (!c.BasicConstraintsValid || !c.IsCA) {
			return invalidf("%s must hold CA certificates, and %s is not one: pin it in tls.pinned_certificates instead",
				field, c.Subject)
		}

		if slices.ContainsFunc(ders[:i], func(o []byte) bool { return bytes.Equal(o, der) }) {
			return invalidf("%s holds %s twice", field, c.Subject)
		}
	}

	return nil
}

func (t TrunkTLS) checkCurrent(prev TrunkTLS, now time.Time) error {
	for _, l := range []struct {
		field     string
		ders, old [][]byte
	}{
		{"tls.ca_certificates", t.CACertificates, prev.CACertificates},
		{"tls.pinned_certificates", t.PinnedCertificates, prev.PinnedCertificates},
	} {
		for _, der := range l.ders {
			if slices.ContainsFunc(l.old, func(o []byte) bool { return bytes.Equal(o, der) }) {
				continue
			}

			c, err := x509.ParseCertificate(der)
			if err != nil {
				continue
			}

			if now.After(c.NotAfter) {
				return invalidf("%s holds %s, which expired on %s", l.field, c.Subject,
					c.NotAfter.UTC().Format(time.RFC3339))
			}
		}
	}

	return nil
}

func ParseCertificatesPEM(field, data string) ([][]byte, error) {
	var ders [][]byte

	rest := []byte(data)

	for {
		var b *pem.Block

		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}

		if b.Type != "CERTIFICATE" {
			return nil, invalidf("%s must hold only CERTIFICATE PEM blocks, not %s", field, b.Type)
		}

		if len(b.Headers) > 0 {
			return nil, invalidf("%s must hold PEM blocks without headers", field)
		}

		ders = append(ders, b.Bytes)
	}

	if len(bytes.TrimSpace(rest)) > 0 {
		return nil, invalidf("%s must be PEM-encoded certificates", field)
	}

	return ders, nil
}

func EncodeCertificatesPEM(ders [][]byte) string {
	var b strings.Builder

	for _, der := range ders {
		_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}

	return b.String()
}

func ParseFingerprint(s string) ([sha256.Size]byte, error) {
	fp, err := transport.ParseFingerprint(s)
	if err != nil {
		return fp, invalidf("tls.pinned_fingerprints must be SHA-256 fingerprints of %d hex bytes, not %q", sha256.Size, s)
	}

	return fp, nil
}

func FormatFingerprint(fp [sha256.Size]byte) string {
	var b strings.Builder

	for i, c := range fp {
		if i > 0 {
			b.WriteByte(':')
		}

		fmt.Fprintf(&b, "%02X", c)
	}

	return b.String()
}

// SIPconnect 2.0 section 17.2
// RFC 5922 section 7.3
func (t TrunkTLS) ClientOptions(serverName string) (transport.TLSOptions, error) {
	o, err := t.options()
	if err != nil {
		return transport.TLSOptions{}, err
	}

	o.ServerName = serverName

	return o, nil
}

// SIPconnect 2.0 section 17.2
func (t TrunkTLS) InboundOptions() (transport.TLSOptions, error) {
	o, err := t.options()
	if err != nil {
		return transport.TLSOptions{}, err
	}

	o.RequireClientCertificate = t.RequireClientCertificate

	return o, nil
}

func (t TrunkTLS) options() (transport.TLSOptions, error) {
	if !t.Trusts() {
		return transport.TLSOptions{}, invalidf("tls must have ca_certificates, pinned_certificates or pinned_fingerprints")
	}

	o := transport.TLSOptions{
		PinnedFingerprints: slices.Clone(t.PinnedFingerprints),
		Identities:         slices.Clone(t.Identities),
	}

	if len(t.CACertificates) > 0 {
		o.Roots = x509.NewCertPool()

		for _, der := range t.CACertificates {
			c, err := x509.ParseCertificate(der)
			if err != nil {
				return transport.TLSOptions{}, fmt.Errorf("tls.ca_certificates: %w", err)
			}

			o.Roots.AddCert(c)
		}
	}

	for _, der := range t.PinnedCertificates {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return transport.TLSOptions{}, fmt.Errorf("tls.pinned_certificates: %w", err)
		}

		o.PinnedCertificates = append(o.PinnedCertificates, c)
	}

	return o, nil
}
