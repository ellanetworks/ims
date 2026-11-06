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
	MaxTrunkPins           = 32
	MaxTrunkIdentities     = 16
	MaxTrunkCertificateDER = 16 << 10
)

// SIPconnect 2.0 section 8.1
// SIPconnect 2.0 section 17.2
// SIPconnect 2.0 section 17.5
type TrunkTLS struct {
	Required       bool
	CACertificates [][]byte
	Pins           [][sha256.Size]byte
	Identities     []string
}

func (t TrunkTLS) Trusts() bool {
	return len(t.CACertificates) > 0 || len(t.Pins) > 0
}

func (t TrunkTLS) configured() bool {
	return t.Trusts() || t.Required || len(t.Identities) > 0
}

// SIPconnect 2.0 section 17.2
func (t TrunkTLS) Validate() error {
	switch {
	case len(t.CACertificates) > MaxTrunkCertificates:
		return invalidf("tls.ca_certificates must hold at most %d certificates", MaxTrunkCertificates)
	case len(t.Pins) > MaxTrunkPins:
		return invalidf("tls.pins must list at most %d pins", MaxTrunkPins)
	case len(t.Identities) > MaxTrunkIdentities:
		return invalidf("tls.identities must list at most %d identities", MaxTrunkIdentities)
	}

	if err := validateCertificates("tls.ca_certificates", t.CACertificates); err != nil {
		return err
	}

	for i, fp := range t.Pins {
		if slices.Contains(t.Pins[:i], fp) {
			return invalidf("tls.pins lists %s twice", FormatFingerprint(fp))
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
		return invalidf("tls must have ca_certificates or pins")
	case len(t.CACertificates) > 0 && len(t.Identities) == 0:
		return invalidf("tls.identities must list the carrier's domain names when tls.ca_certificates is set and the trunk has none")
	}

	return nil
}

func validateCertificates(field string, ders [][]byte) error {
	for i, der := range ders {
		if len(der) > MaxTrunkCertificateDER {
			return invalidf("%s must hold certificates of at most %d bytes", field, MaxTrunkCertificateDER)
		}

		c, err := x509.ParseCertificate(der)
		if err != nil {
			return invalidf("%s holds a certificate that does not parse: %v", field, err)
		}

		if !c.BasicConstraintsValid || !c.IsCA {
			return invalidf("%s must hold CA certificates, and %s is not one: pin it in tls.pins instead",
				field, c.Subject)
		}

		if slices.ContainsFunc(ders[:i], func(o []byte) bool { return bytes.Equal(o, der) }) {
			return invalidf("%s holds %s twice", field, c.Subject)
		}
	}

	return nil
}

func (t TrunkTLS) checkCurrent(prev TrunkTLS, now time.Time) error {
	for _, der := range t.CACertificates {
		if slices.ContainsFunc(prev.CACertificates, func(o []byte) bool { return bytes.Equal(o, der) }) {
			continue
		}

		c, err := x509.ParseCertificate(der)
		if err != nil {
			continue
		}

		if now.After(c.NotAfter) {
			return invalidf("tls.ca_certificates holds %s, which expired on %s", c.Subject, c.NotAfter.UTC().Format(time.RFC3339))
		}
	}

	return nil
}

func ParseCertificatesPEM(field, data string) ([][]byte, error) {
	var ders [][]byte

	rest := bytes.TrimSpace([]byte(data))

	for len(rest) > 0 {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN ")) {
			return nil, invalidf("%s must be PEM-encoded certificates", field)
		}

		var b *pem.Block

		b, rest = pem.Decode(rest)
		if b == nil {
			return nil, invalidf("%s must be PEM-encoded certificates", field)
		}

		rest = bytes.TrimSpace(rest)

		if b.Type != "CERTIFICATE" {
			return nil, invalidf("%s must hold only CERTIFICATE PEM blocks, not %s", field, b.Type)
		}

		if len(b.Headers) > 0 {
			return nil, invalidf("%s must hold PEM blocks without headers", field)
		}

		ders = append(ders, b.Bytes)
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

func ParsePin(s string, now time.Time) ([sha256.Size]byte, error) {
	if !strings.HasPrefix(strings.TrimSpace(s), "-----BEGIN") {
		fp, err := transport.ParseFingerprint(s)
		if err != nil {
			return fp, invalidf("tls.pins must hold PEM certificates or SHA-256 fingerprints of %d hex bytes, not %q", sha256.Size, s)
		}

		return fp, nil
	}

	ders, err := ParseCertificatesPEM("tls.pins", s)
	if err != nil {
		return [sha256.Size]byte{}, err
	}

	if len(ders) != 1 {
		return [sha256.Size]byte{}, invalidf("tls.pins must hold one certificate per pin, not %d", len(ders))
	}

	if len(ders[0]) > MaxTrunkCertificateDER {
		return [sha256.Size]byte{}, invalidf("tls.pins must hold PEM certificates of at most %d bytes", MaxTrunkCertificateDER)
	}

	c, err := x509.ParseCertificate(ders[0])
	if err != nil {
		return [sha256.Size]byte{}, invalidf("tls.pins holds a certificate that does not parse: %v", err)
	}

	if now.After(c.NotAfter) {
		return [sha256.Size]byte{}, invalidf("tls.pins holds %s, which expired on %s", c.Subject, c.NotAfter.UTC().Format(time.RFC3339))
	}

	return transport.Fingerprint(c), nil
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

	if isSIPHostname(serverName) {
		o.Identities = nil
	}

	return o, nil
}

// SIPconnect 2.0 section 17.2
// SIPconnect 2.0 section 17.5
func (t TrunkTLS) InboundOptions() (transport.TLSOptions, error) {
	o, err := t.options()
	if err != nil {
		return transport.TLSOptions{}, err
	}

	o.RequireClientCertificate = t.Required

	return o, nil
}

func (t TrunkTLS) options() (transport.TLSOptions, error) {
	if !t.Trusts() {
		return transport.TLSOptions{}, invalidf("tls must have ca_certificates or pins")
	}

	o := transport.TLSOptions{
		PinnedFingerprints: slices.Clone(t.Pins),
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

	return o, nil
}
