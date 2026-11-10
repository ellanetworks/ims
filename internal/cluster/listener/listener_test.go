package listener

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/cluster/pki"
)

func newTestListener(t *testing.T, pinned map[string]bool) (*Listener, *tls.Certificate) {
	t.Helper()

	cert, err := pki.GenerateCert(pki.NewID(), pki.NewID(), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	l := New(Config{
		Address: "127.0.0.1:0",
		Cert:    func() *tls.Certificate { return &cert },
		Pins: func(fp string) (string, bool) {
			return AnyNode, pinned[fp]
		},
	})

	if err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(l.Close)

	return l, &cert
}

func echo(s *Stream) {
	for {
		c, err := s.Accept()
		if err != nil {
			return
		}

		go func() {
			defer func() { _ = c.Close() }()

			_, _ = io.Copy(c, c)
		}()
	}
}

func TestRevalidateKeepsJoinConnections(t *testing.T) {
	server, serverCert := newTestListener(t, nil)
	client, _ := newTestListener(t, nil)

	join := server.Stream(ALPNJoin, "")

	t.Cleanup(func() { _ = join.Close() })

	go echo(join)

	serverFP := pki.Fingerprint(serverCert.Leaf)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	conn, _, err := client.Dial(ctx, server.Addr().String(), ALPNJoin, func(fp string) (string, bool) {
		return AnyNode, fp == serverFP
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	roundTrip(t, conn, "before")

	server.Revalidate()

	roundTrip(t, conn, "after")
}

func roundTrip(t *testing.T, conn net.Conn, msg string) {
	t.Helper()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("%s revalidating: %v", msg, err)
	}
}

func TestRemovedCertificateIsFenced(t *testing.T) {
	cert, err := pki.GenerateCert(pki.NewID(), pki.NewID(), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	serverFP := pki.Fingerprint(cert.Leaf)
	client, clientCert := newTestListener(t, map[string]bool{serverFP: true})
	removedFP := pki.Fingerprint(clientCert.Leaf)

	server := New(Config{
		Address: "127.0.0.1:0",
		Cert:    func() *tls.Certificate { return &cert },
		Pins:    func(string) (string, bool) { return "", false },
		Removed: func(fp string) bool { return fp == removedFP },
	})

	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(server.Close)

	fenced := make(chan bool, 1)
	httpStream := server.Stream(ALPNHTTP, "")
	raftStream := server.Stream(ALPNRaft, "")

	t.Cleanup(func() { _ = httpStream.Close(); _ = raftStream.Close() })

	go func() {
		c, err := httpStream.Accept()
		if err != nil {
			return
		}

		fenced <- Fenced(c)

		_ = c.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	conn, _, err := client.Dial(ctx, server.Addr().String(), ALPNHTTP, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	select {
	case f := <-fenced:
		if !f {
			t.Fatal("a removed node's connection is not fenced")
		}
	case <-ctx.Done():
		t.Fatal("the removed node's HTTP connection was not accepted")
	}

	raft, _, err := client.Dial(ctx, server.Addr().String(), ALPNRaft, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = raft.Close() })

	_ = raft.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := raft.Read(make([]byte, 1)); err == nil {
		t.Fatal("a removed node's raft connection was served")
	}

	other, _ := newTestListener(t, map[string]bool{serverFP: true})

	unknown, _, err := other.Dial(ctx, server.Addr().String(), ALPNHTTP, nil)
	if err != nil {
		return
	}

	t.Cleanup(func() { _ = unknown.Close() })

	_ = unknown.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := unknown.Read(make([]byte, 1)); err == nil {
		t.Fatal("an unknown certificate was accepted")
	}
}
