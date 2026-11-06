package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/internal/listen"
	"github.com/ellanetworks/ims/sip/internal/sockopt"
)

var (
	ErrUntrustedCertificate = errors.New("sip/transport: untrusted certificate")

	ErrIdentityMismatch = errors.New("sip/transport: certificate identity mismatch")

	ErrNoPeerCertificate = errors.New("sip/transport: no peer certificate")

	ErrUnauthorizedConnection = errors.New("sip/transport: TLS connection not authorized for requests")
)

var (
	oidSubjectAltName = asn1.ObjectIdentifier{2, 5, 29, 17}
	oidSIPDomain      = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 20}
)

// SIPconnect 2.0 section 8.1.2
var clientCipherSuites = []uint16{
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
}

var serverCipherSuites = []uint16{
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
}

type TLSOptions struct {
	Certificates []tls.Certificate

	Roots *x509.CertPool

	PinnedCertificates []*x509.Certificate

	PinnedFingerprints [][sha256.Size]byte

	Identities []string

	ServerName string

	RequireClientCertificate bool
}

func (o TLSOptions) trusted() bool {
	return o.Roots != nil || len(o.PinnedCertificates) > 0 || len(o.PinnedFingerprints) > 0
}

// SIPconnect 2.0 section 8.1.2
// SIPconnect 2.0 section 17.2
// RFC 5922 section 7.3
func NewTLSClientConfig(o TLSOptions) (*tls.Config, error) {
	v, err := clientVerifier(o, x509.ExtKeyUsageServerAuth)
	if err != nil {
		return nil, err
	}

	cfg := baseConfig(clientCipherSuites)
	cfg.ServerName = o.ServerName
	cfg.InsecureSkipVerify = true
	cfg.VerifyConnection = v.verify

	if len(o.Certificates) > 0 {
		cert := o.Certificates[0]
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &cert, nil }
	}

	return cfg, nil
}

// SIPconnect 2.0 section 8.1.2
// SIPconnect 2.0 section 17.2
// RFC 5922 section 7.4
func NewTLSServerConfig(o TLSOptions) (*tls.Config, error) {
	if len(o.Certificates) == 0 {
		return nil, errors.New("sip/transport: TLS server without a certificate")
	}

	if o.RequireClientCertificate && !o.trusted() {
		return nil, errors.New("sip/transport: client certificate required without a CA or a pinned certificate")
	}

	if o.Roots != nil && len(o.Identities) == 0 {
		return nil, errors.New("sip/transport: TLS server trusting a CA without client identities to check")
	}

	v := verifier{opts: o, identities: o.Identities, usage: x509.ExtKeyUsageClientAuth, optional: !o.RequireClientCertificate}

	cfg := baseConfig(serverCipherSuites)
	cfg.Certificates = o.Certificates
	cfg.VerifyConnection = v.verify

	switch {
	case o.RequireClientCertificate:
		cfg.ClientAuth = tls.RequireAnyClientCert
	case o.trusted():
		cfg.ClientAuth = tls.RequestClientCert
	}

	if o.Roots != nil {
		cfg.ClientCAs = o.Roots
	}

	return cfg, nil
}

// RFC 5922 section 7.1
// RFC 5922 section 7.3
func clientVerifier(o TLSOptions, usage x509.ExtKeyUsage) (verifier, error) {
	if !o.trusted() {
		return verifier{}, errors.New("sip/transport: TLS client without a CA or a pinned certificate")
	}

	ids := o.Identities
	if _, err := netip.ParseAddr(o.ServerName); err != nil && isDNSName(o.ServerName) {
		ids = []string{o.ServerName}
	}

	if o.Roots != nil && len(ids) == 0 {
		return verifier{}, errors.New("sip/transport: TLS client trusting a CA without an identity to check")
	}

	return verifier{opts: o, identities: ids, usage: usage}, nil
}

func baseConfig(suites []uint16) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		CipherSuites: slices.Clone(suites),
	}
}

type verifier struct {
	opts       TLSOptions
	identities []string
	usage      x509.ExtKeyUsage
	optional   bool
}

func (v *verifier) accepts(c *conn) bool {
	if v == nil || len(c.peers) == 0 {
		return false
	}

	w := *v
	if !c.inbound {
		w.usage = x509.ExtKeyUsageServerAuth
	}

	return w.verify(tls.ConnectionState{PeerCertificates: c.peers}) == nil
}

func (t *Transport) reuseVerifier(f sip.Flow) *verifier {
	if t.tlsClient == nil {
		return nil
	}

	o, err := t.tlsClient(f)
	if err != nil {
		return nil
	}

	v, err := clientVerifier(o, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return nil
	}

	return &v
}

// SIPconnect 2.0 section 17.2
// RFC 5922 section 7.3
// RFC 5922 section 7.4
func (v verifier) verify(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		if v.optional {
			return nil
		}

		return ErrNoPeerCertificate
	}

	leaf := cs.PeerCertificates[0]

	pinned, err := v.trust(leaf, cs.PeerCertificates[1:])
	if err != nil {
		return err
	}

	if !pinned && len(v.identities) > 0 && !MatchIdentity(CertificateIdentities(leaf), v.identities) {
		return fmt.Errorf("%w: %v, want %v", ErrIdentityMismatch, CertificateIdentities(leaf), v.identities)
	}

	return nil
}

// SIPconnect 2.0 section 17.2
// RFC 5280 section 6
func (v verifier) trust(leaf *x509.Certificate, chain []*x509.Certificate) (bool, error) {
	opts := x509.VerifyOptions{KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}

	pinned := v.pinned(leaf)

	switch {
	case pinned:
		opts.Roots = x509.NewCertPool()
		opts.Roots.AddCert(leaf)
	case v.opts.Roots != nil:
		opts.Roots = v.opts.Roots
		opts.Intermediates = x509.NewCertPool()

		for _, c := range chain {
			opts.Intermediates.AddCert(c)
		}
	default:
		return false, fmt.Errorf("%w: %s is not pinned", ErrUntrustedCertificate, leaf.Subject)
	}

	chains, err := leaf.Verify(opts)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrUntrustedCertificate, err)
	}

	if !slices.ContainsFunc(chains, func(chain []*x509.Certificate) bool {
		return !slices.ContainsFunc(chain, func(c *x509.Certificate) bool { return !permits(c, v.usage) })
	}) {
		return false, fmt.Errorf("%w: %s: x509: certificate specifies an incompatible key usage", ErrUntrustedCertificate, leaf.Subject)
	}

	return pinned, nil
}

// RFC 5280 section 4.2.1.12
// RFC 5924 section 5
// TS 33.310 clause 6.1.3a
func permits(c *x509.Certificate, usage x509.ExtKeyUsage) bool {
	if len(c.ExtKeyUsage) == 0 && len(c.UnknownExtKeyUsage) == 0 {
		return true
	}

	if slices.Contains(c.ExtKeyUsage, x509.ExtKeyUsageAny) || slices.Contains(c.ExtKeyUsage, usage) {
		return true
	}

	return slices.ContainsFunc(c.UnknownExtKeyUsage, oidSIPDomain.Equal)
}

func (v verifier) pinned(leaf *x509.Certificate) bool {
	if slices.ContainsFunc(v.opts.PinnedCertificates, func(c *x509.Certificate) bool { return bytes.Equal(c.Raw, leaf.Raw) }) {
		return true
	}

	return slices.Contains(v.opts.PinnedFingerprints, Fingerprint(leaf))
}

func Fingerprint(cert *x509.Certificate) [sha256.Size]byte {
	return sha256.Sum256(cert.Raw)
}

func ParseFingerprint(s string) ([sha256.Size]byte, error) {
	var fp [sha256.Size]byte

	b, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(s), ":", ""))
	if err != nil || len(b) != sha256.Size {
		return fp, fmt.Errorf("sip/transport: SHA-256 fingerprint %q: want %d hex bytes", s, sha256.Size)
	}

	copy(fp[:], b)

	return fp, nil
}

// RFC 5922 section 7.1
// SIPconnect 2.0 section 8.1
func CertificateIdentities(cert *x509.Certificate) []string {
	if !slices.ContainsFunc(cert.Extensions, func(e pkix.Extension) bool { return e.Id.Equal(oidSubjectAltName) }) {
		if cn := cert.Subject.CommonName; isDNSName(cn) {
			return []string{cn}
		}

		return nil
	}

	var ids []string

	for _, u := range cert.URIs {
		if !strings.EqualFold(u.Scheme, "sip") {
			continue
		}

		su, err := sip.ParseURI(u.String())
		if err != nil || su.User != "" || strings.Contains(u.Opaque, "@") || su.Host == "" {
			continue
		}

		ids = append(ids, su.Host)
	}

	if len(ids) > 0 {
		return ids
	}

	return slices.Clone(cert.DNSNames)
}

// RFC 5922 section 7.2
func MatchIdentity(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if strings.EqualFold(h, w) {
				return true
			}
		}
	}

	return false
}

func isDNSName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}

	for label := range strings.SplitSeq(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}

		if strings.ContainsFunc(label, func(r rune) bool { return !isLetterDigitHyphen(r) }) {
			return false
		}
	}

	return true
}

func isLetterDigitHyphen(r rune) bool {
	return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '-'
}

// SIPconnect 2.0 section 8.1
func (t *Transport) ListenTLS(ctx context.Context, local netip.AddrPort, cfg *tls.Config) (netip.AddrPort, error) {
	if cfg == nil {
		return netip.AddrPort{}, ErrNoTLSConfig
	}

	local = unmap(local)
	if !local.Addr().IsValid() || local.Addr().IsUnspecified() || local.Addr().Zone() != "" {
		return netip.AddrPort{}, fmt.Errorf("sip/transport: listen on %s: a specific IP address is required", local)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return netip.AddrPort{}, ErrClosed
	}

	if _, ok := t.secure[local]; ok {
		return netip.AddrPort{}, fmt.Errorf("sip/transport: already listening for TLS on %s", local)
	}

	if local.Port() != 0 {
		if err := sockopt.CheckNoListener(local); err != nil {
			return netip.AddrPort{}, fmt.Errorf("sip/transport: listen for TLS on %s: %w", local, err)
		}
	}

	tl, err := listen.TCP(ctx, local, t.control)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("sip/transport: listen for TLS on %s: %w", local, err)
	}

	l := &listener{local: unmap(tl.Addr().(*net.TCPAddr).AddrPort()), tcp: tl, tls: cfg}
	t.secure[l.local] = l
	t.wg.Add(1)

	go t.accept(l)

	t.log.Debug("SIP listening for TLS", slog.String("local", l.local.String()))

	return l.local, nil
}

func (t *Transport) ClientTLS(ctx context.Context, local netip.AddrPort) (netip.AddrPort, error) {
	local = unmap(local)
	if !local.Addr().IsValid() || local.Addr().IsUnspecified() || local.Addr().Zone() != "" {
		return netip.AddrPort{}, fmt.Errorf("sip/transport: TLS from %s: a specific IP address is required", local)
	}

	var hold io.Closer

	if local.Port() == 0 {
		bound, h, err := sockopt.Reserve(local)
		if err != nil {
			return netip.AddrPort{}, fmt.Errorf("sip/transport: TLS from %s: %w", local, err)
		}

		local, hold = unmap(bound), h
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed || t.secure[local] != nil {
		if hold != nil {
			_ = hold.Close()
		}

		if t.closed {
			return netip.AddrPort{}, ErrClosed
		}

		return netip.AddrPort{}, fmt.Errorf("sip/transport: already using %s for TLS", local)
	}

	t.secure[local] = &listener{local: local, hold: hold}

	return local, nil
}

func (t *Transport) startHandshake(l *listener, f sip.Flow, nc *net.TCPConn) bool {
	t.mu.Lock()

	if t.closed {
		t.releaseLocked(f.Remote.Addr())
		t.mu.Unlock()

		_ = nc.Close()

		return false
	}

	if len(t.conns)+int(t.handshaking.Load()) >= t.maxConns {
		t.releaseLocked(f.Remote.Addr())
		t.mu.Unlock()
		t.log.Warn("SIP connection refused: too many connections", slog.String("flow", f.String()), slog.Int("limit", t.maxConns))

		_ = nc.SetLinger(0)
		_ = nc.Close()

		return true
	}

	t.handshaking.Add(1)
	t.wg.Add(1)
	t.mu.Unlock()

	go t.serverHandshake(l, f, nc)

	return true
}

func (t *Transport) serverHandshake(l *listener, f sip.Flow, nc *net.TCPConn) {
	defer t.wg.Done()

	tc := tls.Server(nc, l.tls)

	ctx, cancel := context.WithTimeout(t.ctx, min(t.firstTimeout, t.idleTimeout))
	err := tc.HandshakeContext(ctx)

	cancel()
	t.handshaking.Add(-1)

	if err != nil {
		t.log.Debug("SIP TLS handshake failed", slog.String("flow", f.String()), slog.Any("error", err))

		t.release(f.Remote.Addr())

		_ = nc.Close()

		return
	}

	t.register(f, nc, tc, tc.ConnectionState().PeerCertificates)
}

func (t *Transport) clientHandshake(nc net.Conn, f sip.Flow) (net.Conn, error) {
	if t.tlsClient == nil {
		_ = nc.Close()
		return nil, ErrNoTLSConfig
	}

	o, err := t.tlsClient(f)
	if err == nil {
		var cfg *tls.Config

		cfg, err = NewTLSClientConfig(o)
		if err == nil {
			return t.handshake(nc, f, cfg)
		}
	}

	_ = nc.Close()

	return nil, err
}

func (t *Transport) handshake(nc net.Conn, f sip.Flow, cfg *tls.Config) (net.Conn, error) {
	tc := tls.Client(nc, cfg)

	ctx, cancel := context.WithTimeout(t.ctx, t.dialTimeout)
	defer cancel()

	if err := tc.HandshakeContext(ctx); err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("TLS handshake with %s: %w", f.Remote, err)
	}

	return tc, nil
}
