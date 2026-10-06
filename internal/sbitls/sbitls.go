// Package sbitls is TLS on a service-based interface: HTTP/2 over mutually authenticated TLS, with the 3GPP TLS
// profile (TS 33.501 §13.1.0, TS 33.210 §6.2). The credentials are read again from their files when these change,
// so that a renewed certificate is used without a restart.
package sbitls

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"
)

// reloadInterval bounds how often the files are read again: at most once per interval, at a handshake.
const reloadInterval = time.Second

// TS 33.210 §6.2.3: TLS 1.2 cipher suites with ECDHE and AEAD only. TLS 1.3 suites are all AEAD and ECDHE
// (RFC 8446 §9.1), and Go supports neither TLS_SHA256_SHA256 nor TLS_SHA384_SHA384 (TS 33.210 §6.2.2).
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

func (f Files) read() ([3][]byte, error) {
	var out [3][]byte

	for i, name := range []string{f.CA, f.Cert, f.Key} {
		b, err := os.ReadFile(name)
		if err != nil {
			return out, err
		}

		out[i] = b
	}

	return out, nil
}

// Credentials are the trust anchors and the certificate of this end, as last read from their files.
type Credentials struct {
	files Files
	log   *slog.Logger
	now   func() time.Time

	mu      sync.Mutex
	cur     *material
	checked time.Time
	// rejected is what the files held when they last failed to load, so that the failure is logged once.
	rejected [3][]byte
}

type material struct {
	raw  [3][]byte
	cert tls.Certificate
	pool *x509.CertPool
}

// Load reads the files. They must hold at least one CA certificate, and a certificate that is valid now, that
// matches the key, and that may serve both TLS clients and servers (TS 33.310 §6.1.3c.3).
func Load(f Files, logger *slog.Logger) (*Credentials, error) {
	if logger == nil {
		logger = slog.Default()
	}

	c := &Credentials{files: f, log: logger, now: time.Now}

	raw, err := f.read()
	if err != nil {
		return nil, err
	}

	if c.cur, err = c.parse(raw); err != nil {
		return nil, err
	}

	c.checked = c.now()

	return c, nil
}

func (c *Credentials) parse(raw [3][]byte) (*material, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw[0]) {
		return nil, fmt.Errorf("%s: no CA certificate", c.files.CA)
	}

	cert, err := tls.X509KeyPair(raw[1], raw[2])
	if err != nil {
		return nil, fmt.Errorf("%s, %s: %w", c.files.Cert, c.files.Key, err)
	}

	leaf := cert.Leaf
	now := c.now()

	switch {
	case now.Before(leaf.NotBefore):
		return nil, fmt.Errorf("%s: certificate not valid before %s", c.files.Cert, leaf.NotBefore.UTC().Format(time.RFC3339))
	case now.After(leaf.NotAfter):
		return nil, fmt.Errorf("%s: certificate expired at %s", c.files.Cert, leaf.NotAfter.UTC().Format(time.RFC3339))
	case !usableFor(leaf, x509.ExtKeyUsageServerAuth) || !usableFor(leaf, x509.ExtKeyUsageClientAuth):
		return nil, fmt.Errorf("%s: the extended key usage must allow both serverAuth and clientAuth", c.files.Cert)
	}

	return &material{raw: raw, cert: cert, pool: pool}, nil
}

// RFC 5280 §4.2.1.12: without the extension, the certificate may be used for any purpose.
func usableFor(leaf *x509.Certificate, u x509.ExtKeyUsage) bool {
	return len(leaf.ExtKeyUsage) == 0 && len(leaf.UnknownExtKeyUsage) == 0 ||
		slices.Contains(leaf.ExtKeyUsage, u) || slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageAny)
}

// material returns the credentials, after reading the files again if reloadInterval has passed. Files that do not
// load leave the previous credentials in use.
func (c *Credentials) material() *material {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if now.Sub(c.checked) < reloadInterval {
		return c.cur
	}

	c.checked = now

	raw, err := c.files.read()
	if err == nil && equal(raw, c.cur.raw) {
		return c.cur
	}

	if err == nil {
		var m *material
		if m, err = c.parse(raw); err == nil {
			c.cur, c.rejected = m, [3][]byte{}
			c.log.Info("TLS credentials reloaded", slog.String("cert", c.files.Cert),
				slog.Time("not_after", m.cert.Leaf.NotAfter))

			return c.cur
		}
	}

	if !equal(raw, c.rejected) {
		c.rejected = raw
		c.log.Warn("TLS credentials not reloaded: keeping the previous ones", slog.Any("error", err))
	}

	return c.cur
}

func equal(a, b [3][]byte) bool {
	return bytes.Equal(a[0], b[0]) && bytes.Equal(a[1], b[1]) && bytes.Equal(a[2], b[2])
}

// Certificate returns the certificate of this end.
func (c *Credentials) Certificate() *x509.Certificate {
	return c.material().cert.Leaf
}

// Covers checks that the certificate of this end is valid for host, a domain name or an IP address, so that the
// peers that connect to host accept it.
func (c *Credentials) Covers(host string) error {
	return c.Certificate().VerifyHostname(host)
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
// §13.1.0). With a domain name, the client sends it in SNI.
func (c *Credentials) Client(serverName string) *tls.Config {
	m := c.material()
	cfg := profile()
	cfg.ServerName = serverName
	cfg.RootCAs = m.pool
	cfg.Certificates = []tls.Certificate{m.cert}

	return cfg
}

// Server returns the configuration of a server that requires clients to present a certificate issued by one of
// the CAs (TS 33.501 §13.1.0). Each handshake takes the current credentials.
func (c *Credentials) Server() *tls.Config {
	cfg := profile()
	cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		m := c.material()
		cfg := profile()
		cfg.Certificates = []tls.Certificate{m.cert}
		cfg.ClientCAs = m.pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
		// A resumed session skips the verification of the client certificate, which the CAs may no longer
		// accept.
		cfg.SessionTicketsDisabled = true

		return cfg, nil
	}

	return cfg
}
