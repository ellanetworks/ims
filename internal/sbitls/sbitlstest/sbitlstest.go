// Package sbitlstest issues certificates for tests: a CA, and end entity certificates written to files that
// sbitls loads.
package sbitlstest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/sbitls"
)

type CA struct {
	Cert *x509.Certificate
	key  crypto.Signer
	dir  string
	// File holds the CA certificate.
	File string
}

func NewCA(t testing.TB, name string) *CA {
	t.Helper()

	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	ca := &CA{Cert: cert, key: key, dir: t.TempDir()}
	ca.File = filepath.Join(ca.dir, name+"-ca.crt")
	writePEM(t, ca.File, "CERTIFICATE", der)

	return ca
}

// Leaf is an end entity certificate to issue.
type Leaf struct {
	// Hosts are the domain names and IP addresses in subjectAltName.
	Hosts []string
	// ExtKeyUsage defaults to serverAuth and clientAuth (TS 33.310 §6.1.3c.3).
	ExtKeyUsage []x509.ExtKeyUsage
	// NotBefore and NotAfter default to an hour ago and a day from now.
	NotBefore, NotAfter time.Time
}

// Issued is a certificate, as files and as what crypto/tls takes.
type Issued struct {
	sbitls.Files
	Cert tls.Certificate
}

// Issue writes a certificate and its key to files named after name. Files.CA is the CA's file.
func (ca *CA) Issue(t testing.TB, name string, l Leaf) Issued {
	t.Helper()

	if l.ExtKeyUsage == nil {
		l.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	}

	if l.NotBefore.IsZero() {
		l.NotBefore = time.Now().Add(-time.Hour)
	}

	if l.NotAfter.IsZero() {
		l.NotAfter = time.Now().Add(24 * time.Hour)
	}

	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: serial(t),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    l.NotBefore,
		NotAfter:     l.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  l.ExtKeyUsage,
	}

	for _, h := range l.Hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, key.Public(), ca.key)
	if err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	out := Issued{Files: sbitls.Files{
		CA:   ca.File,
		Cert: filepath.Join(ca.dir, name+".crt"),
		Key:  filepath.Join(ca.dir, name+".key"),
	}}

	writePEM(t, out.Files.Cert, "CERTIFICATE", der)
	writePEM(t, out.Files.Key, "PRIVATE KEY", keyDER)

	if out.Cert, err = tls.LoadX509KeyPair(out.Files.Cert, out.Files.Key); err != nil {
		t.Fatal(err)
	}

	return out
}

// Pool holds the certificates of the CAs.
func Pool(cas ...*CA) *x509.CertPool {
	p := x509.NewCertPool()
	for _, ca := range cas {
		p.AddCert(ca.Cert)
	}

	return p
}

// Peer returns the configuration of a peer, as client or server, that presents cert, trusts the CAs, and requires
// a client certificate when it serves.
func Peer(cert tls.Certificate, cas ...*CA) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		RootCAs:      Pool(cas...),
		ClientCAs:    Pool(cas...),
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	return key
}

func serial(t testing.TB) *big.Int {
	t.Helper()

	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		t.Fatal(err)
	}

	return n.Add(n, big.NewInt(1))
}

func writePEM(t testing.TB, path, kind string, der []byte) {
	t.Helper()

	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}
