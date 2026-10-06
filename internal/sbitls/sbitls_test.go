package sbitls_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/sbitls"
	"github.com/ellanetworks/ims/internal/sbitls/sbitlstest"
)

func load(t *testing.T, f sbitls.Files) *sbitls.Credentials {
	t.Helper()

	c, err := sbitls.Load(f, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestLoadRejects(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")
	other := sbitlstest.NewCA(t, "other")
	good := ca.Issue(t, "good", sbitlstest.Leaf{Hosts: []string{"localhost"}})
	foreign := other.Issue(t, "foreign", sbitlstest.Leaf{})
	serverOnly := ca.Issue(t, "server", sbitlstest.Leaf{ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	expired := ca.Issue(t, "expired", sbitlstest.Leaf{NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: time.Now().Add(-time.Hour)})
	future := ca.Issue(t, "future", sbitlstest.Leaf{NotBefore: time.Now().Add(time.Hour)})

	tests := []struct {
		name  string
		files sbitls.Files
		want  string
	}{
		{"missing CA file", sbitls.Files{CA: good.Files.CA + ".missing", Cert: good.Files.Cert, Key: good.Files.Key}, "no such file"},
		{"no CA certificate", sbitls.Files{CA: good.Files.Key, Cert: good.Files.Cert, Key: good.Files.Key}, "no CA certificate"},
		{"key of another certificate", sbitls.Files{CA: good.Files.CA, Cert: good.Files.Cert, Key: foreign.Files.Key}, "private key does not match"},
		{"server only", serverOnly.Files, "serverAuth and clientAuth"},
		{"expired", expired.Files, "certificate expired"},
		{"not yet valid", future.Files, "not valid before"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sbitls.Load(tt.files, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

func TestLoadAcceptsNoExtKeyUsage(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")
	any := ca.Issue(t, "any", sbitlstest.Leaf{ExtKeyUsage: []x509.ExtKeyUsage{}})

	load(t, any.Files)
}

func TestCovers(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")
	c := load(t, ca.Issue(t, "pcscf", sbitlstest.Leaf{Hosts: []string{"pcscf.example.org", "10.0.0.5"}}).Files)

	for _, h := range []string{"pcscf.example.org", "PCSCF.example.org", "10.0.0.5"} {
		if err := c.Covers(h); err != nil {
			t.Errorf("Covers(%q) = %v", h, err)
		}
	}

	for _, h := range []string{"pcf.example.org", "10.0.0.6"} {
		if err := c.Covers(h); err == nil {
			t.Errorf("Covers(%q) = nil", h)
		}
	}
}

// handshake connects a client to a server over loopback, and returns the error each side ends with.
func handshake(t *testing.T, client, server *tls.Config) (clientErr, serverErr error) {
	t.Helper()

	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = ln.Close() }()

	done := make(chan error, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}

		defer func() { _ = conn.Close() }()

		tc := tls.Server(conn, server)
		err = tc.HandshakeContext(t.Context())

		if err == nil {
			// TLS 1.3 clients finish before the server has checked their certificate: a read surfaces its verdict.
			if _, err = tc.Read(make([]byte, 1)); errors.Is(err, io.EOF) {
				err = nil
			}
		}

		done <- err
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	d := tls.Dialer{Config: client}

	conn, err := d.DialContext(ctx, "tcp", ln.Addr().String())
	if err == nil {
		// Read the server's alert, if any, or time out.
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, err = conn.Read(make([]byte, 1))

		if errors.Is(err, os.ErrDeadlineExceeded) {
			err = nil
		}

		_ = conn.Close()
	}

	return err, <-done
}

func TestMutualTLS(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")
	other := sbitlstest.NewCA(t, "other")
	ims := load(t, ca.Issue(t, "ims", sbitlstest.Leaf{Hosts: []string{"ims.example.org", "127.0.0.1"}}).Files)
	pcf := ca.Issue(t, "pcf", sbitlstest.Leaf{Hosts: []string{"pcf.example.org", "127.0.0.1"}})
	stranger := other.Issue(t, "stranger", sbitlstest.Leaf{Hosts: []string{"pcf.example.org", "127.0.0.1"}})

	t.Run("client to a PCF by name", func(t *testing.T) {
		cerr, serr := handshake(t, ims.Client("pcf.example.org"), sbitlstest.Peer(pcf.Cert, ca))
		if cerr != nil || serr != nil {
			t.Fatalf("client: %v, server: %v", cerr, serr)
		}
	})

	t.Run("client to a PCF by address", func(t *testing.T) {
		cerr, serr := handshake(t, ims.Client("127.0.0.1"), sbitlstest.Peer(pcf.Cert, ca))
		if cerr != nil || serr != nil {
			t.Fatalf("client: %v, server: %v", cerr, serr)
		}
	})

	t.Run("client to a PCF of another name", func(t *testing.T) {
		cerr, _ := handshake(t, ims.Client("other.example.org"), sbitlstest.Peer(pcf.Cert, ca))

		var v *tls.CertificateVerificationError
		if !errors.As(cerr, &v) {
			t.Fatalf("client: %v, want a verification error", cerr)
		}
	})

	t.Run("client to a PCF of another CA", func(t *testing.T) {
		cerr, _ := handshake(t, ims.Client("pcf.example.org"), sbitlstest.Peer(stranger.Cert, ca))

		var v *tls.CertificateVerificationError
		if !errors.As(cerr, &v) {
			t.Fatalf("client: %v, want a verification error", cerr)
		}
	})

	t.Run("server to a PCF", func(t *testing.T) {
		cfg := sbitlstest.Peer(pcf.Cert, ca)
		cfg.ServerName = "ims.example.org"

		cerr, serr := handshake(t, cfg, ims.Server())
		if cerr != nil || serr != nil {
			t.Fatalf("client: %v, server: %v", cerr, serr)
		}
	})

	t.Run("server to a client without a certificate", func(t *testing.T) {
		cfg := sbitlstest.Peer(pcf.Cert, ca)
		cfg.ServerName = "ims.example.org"
		cfg.Certificates = nil

		if _, serr := handshake(t, cfg, ims.Server()); serr == nil || !strings.Contains(serr.Error(), "didn't provide a certificate") {
			t.Fatalf("server: %v, want a missing certificate", serr)
		}
	})

	t.Run("server to a client of another CA", func(t *testing.T) {
		cfg := sbitlstest.Peer(stranger.Cert, ca)
		cfg.ServerName = "ims.example.org"
		// Present the certificate even though the server does not list its CA.
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &stranger.Cert, nil }

		var v *tls.CertificateVerificationError
		if _, serr := handshake(t, cfg, ims.Server()); !errors.As(serr, &v) {
			t.Fatalf("server: %v, want a verification error", serr)
		}
	})
}

// TS 33.210 §6.2.1, §6.2.3
func TestProfile(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")
	ims := load(t, ca.Issue(t, "ims", sbitlstest.Leaf{Hosts: []string{"ims.example.org"}}).Files)
	pcf := ca.Issue(t, "pcf", sbitlstest.Leaf{Hosts: []string{"ims.example.org"}})

	tests := []struct {
		name   string
		mutate func(*tls.Config)
	}{
		{"TLS 1.1", func(c *tls.Config) { c.MinVersion, c.MaxVersion = tls.VersionTLS11, tls.VersionTLS11 }},
		{"TLS 1.2 with CBC", func(c *tls.Config) {
			c.MaxVersion = tls.VersionTLS12
			c.CipherSuites = []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA}
		}},
		{"ALPN without h2", func(c *tls.Config) { c.NextProtos = []string{"spdy/3"} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := sbitlstest.Peer(pcf.Cert, ca)
			cfg.ServerName = "ims.example.org"
			tt.mutate(cfg)

			if cerr, serr := handshake(t, cfg, ims.Server()); cerr == nil || serr == nil {
				t.Fatalf("client: %v, server: %v, want both to fail", cerr, serr)
			}
		})
	}

	t.Run("TLS 1.2 with GCM", func(t *testing.T) {
		cfg := sbitlstest.Peer(pcf.Cert, ca)
		cfg.ServerName = "ims.example.org"
		cfg.MaxVersion = tls.VersionTLS12
		cfg.NextProtos = []string{"h2"}

		if cerr, serr := handshake(t, cfg, ims.Server()); cerr != nil || serr != nil {
			t.Fatalf("client: %v, server: %v", cerr, serr)
		}
	})
}

func TestReload(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")
	first := ca.Issue(t, "first", sbitlstest.Leaf{Hosts: []string{"ims.example.org"}})
	second := ca.Issue(t, "second", sbitlstest.Leaf{Hosts: []string{"ims.example.org"}})

	c := load(t, first.Files)

	serial := func() string { return c.Certificate().SerialNumber.String() }

	want := serial()

	copyFile(t, second.Files.Cert, first.Files.Cert)
	copyFile(t, second.Files.Key, first.Files.Key)

	if got := serial(); got != want {
		t.Fatal("reloaded within the reload interval")
	}

	sbitls.Advance(c, 2*time.Second)

	if got := serial(); got != second.Cert.Leaf.SerialNumber.String() {
		t.Fatalf("serial %s after the files changed, want the new one", got)
	}

	// A certificate without its key does not load: the previous one stays.
	want = serial()

	copyFile(t, first.Files.Key, second.Files.Cert)
	sbitls.Advance(c, 2*time.Second)

	if got := serial(); got != want {
		t.Fatalf("serial %s after a broken update, want the previous one", got)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()

	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
