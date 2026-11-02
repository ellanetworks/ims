package sbitls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"slices"
	"time"
)

// TS 33.210 clause 6.2.3
// TS 33.210 clause 6.2.2
var cipherSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

type Files struct {
	CA   string
	Cert string
	Key  string
}

type Credentials struct {
	cert tls.Certificate
	pool *x509.CertPool
}

// TS 33.310 clause 6.1.3c.3
// RFC 5280 section 4.2.1.3
// RFC 5280 section 4.2.1.12
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

// RFC 5280 section 4.2.1.12
func usableFor(leaf *x509.Certificate, u x509.ExtKeyUsage) bool {
	return len(leaf.ExtKeyUsage) == 0 && len(leaf.UnknownExtKeyUsage) == 0 ||
		slices.Contains(leaf.ExtKeyUsage, u) || slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageAny)
}

func (c *Credentials) Certificate() *x509.Certificate {
	return c.cert.Leaf
}

func profile() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		CipherSuites: cipherSuites,
		NextProtos:   []string{"h2"},
	}
}

// TS 33.501 clause 13.1.0
func (c *Credentials) Client(serverName string) *tls.Config {
	cfg := profile()
	cfg.ServerName = serverName
	cfg.RootCAs = c.pool
	cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &c.cert, nil }

	return cfg
}

// TS 33.501 clause 13.1.0
func (c *Credentials) Server() *tls.Config {
	cfg := profile()
	cfg.Certificates = []tls.Certificate{c.cert}
	cfg.ClientCAs = c.pool
	cfg.ClientAuth = tls.RequireAndVerifyClientCert

	return cfg
}
