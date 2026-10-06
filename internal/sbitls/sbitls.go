// Package sbitls is TLS on a service-based interface: HTTP/2 over mutually authenticated TLS, with the 3GPP TLS
// profile (TS 33.501 §13.1.0, TS 33.210 §6.2).
package sbitls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"slices"
	"time"
)

// TS 33.210 §6.2.3: TLS 1.2 cipher suites with ECDHE and AEAD only. TLS 1.3 suites are all AEAD and ECDHE
// (RFC 8446 §9.1), and Go supports neither TLS_SHA256_SHA256 nor TLS_SHA384_SHA384 (TS 33.210 §6.2.2). Go's
// defaults cover the rest of the profile: no FFDHE, no psk_ke, no renegotiation, and no SHA-1 signatures in TLS
// 1.2 unless GODEBUG=tlssha1=1, which must not be set.
var cipherSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

// Files are PEM files: the certificates of the CAs that issue the peers' certificates, and the certificate chain
// and private key of this end, which it presents both as a client and as a server.
type Files struct {
	CA   string
	Cert string
	Key  string
}

// Credentials are the trust anchors and the certificate of this end.
type Credentials struct {
	cert tls.Certificate
	pool *x509.CertPool
}

// Load reads the files. They must hold at least one CA certificate, and a certificate that is valid now, that
// matches the key, that may serve both TLS clients and servers, and that may sign (TS 33.310 §6.1.3c.3). The
// extensions may also be absent, which RFC 5280 §4.2.1.3 and §4.2.1.12 read as no restriction.
func Load(f Files) (*Credentials, error) {
	ca, err := os.ReadFile(f.CA)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("%s: no CA certificate", f.CA)
	}

	cert, err := tls.LoadX509KeyPair(f.Cert, f.Key)
	if err != nil {
		return nil, err
	}

	leaf := cert.Leaf
	now := time.Now()

	switch {
	case now.Before(leaf.NotBefore):
		return nil, fmt.Errorf("%s: certificate not valid before %s", f.Cert, leaf.NotBefore.UTC().Format(time.RFC3339))
	case now.After(leaf.NotAfter):
		return nil, fmt.Errorf("%s: certificate expired at %s", f.Cert, leaf.NotAfter.UTC().Format(time.RFC3339))
	case !usableFor(leaf, x509.ExtKeyUsageServerAuth) || !usableFor(leaf, x509.ExtKeyUsageClientAuth):
		return nil, fmt.Errorf("%s: the extended key usage must allow both serverAuth and clientAuth", f.Cert)
	case leaf.KeyUsage != 0 && leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0:
		return nil, fmt.Errorf("%s: the key usage must allow digitalSignature", f.Cert)
	}

	return &Credentials{cert: cert, pool: pool}, nil
}

// RFC 5280 §4.2.1.12: without the extension, the certificate may be used for any purpose.
func usableFor(leaf *x509.Certificate, u x509.ExtKeyUsage) bool {
	return len(leaf.ExtKeyUsage) == 0 && len(leaf.UnknownExtKeyUsage) == 0 ||
		slices.Contains(leaf.ExtKeyUsage, u) || slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageAny)
}

// Certificate returns the certificate of this end.
func (c *Credentials) Certificate() *x509.Certificate {
	return c.cert.Leaf
}

// profile is the configuration common to clients and servers.
func profile() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		CipherSuites: cipherSuites,
		// TS 29.500 §5.2.1: HTTP/2, which RFC 9113 §3.2 negotiates with ALPN.
		NextProtos: []string{"h2"},
	}
}

// Client returns the configuration of a connection to serverName, a domain name or an IP address. The server
// must present a certificate for it, issued by one of the CAs, and the client presents its own (TS 33.501
// §13.1.0). With a domain name, the client sends it in SNI. The client presents its certificate even when the
// server names other CAs, so that a server that does not trust it says so rather than that it got none.
func (c *Credentials) Client(serverName string) *tls.Config {
	cfg := profile()
	cfg.ServerName = serverName
	cfg.RootCAs = c.pool
	cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &c.cert, nil }

	return cfg
}

// Server returns the configuration of a server that requires clients to present a certificate issued by one of
// the CAs (TS 33.501 §13.1.0).
func (c *Credentials) Server() *tls.Config {
	cfg := profile()
	cfg.Certificates = []tls.Certificate{c.cert}
	cfg.ClientCAs = c.pool
	cfg.ClientAuth = tls.RequireAndVerifyClientCert

	return cfg
}
