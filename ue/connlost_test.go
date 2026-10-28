package ue

import (
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/uetest"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// registerTCP registers u over TCP, and returns the flow of its protected REGISTER, as the P-CSCF sees it.
func registerTCP(t *testing.T, n *network, u *testUE) sip.Flow {
	t.Helper()

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	n.OK(req, f, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	return f
}

// flowFailedWithin waits for the deregistration of u's failed flow.
func flowFailedWithin(t *testing.T, u *testUE, d time.Duration) Event {
	t.Helper()

	timeout := time.After(d)

	for {
		select {
		case e := <-u.Events():
			if e.Deregistered == "" {
				continue
			}

			if e.Deregistered != DeregisteredFlowFailed || u.State().Registered {
				t.Fatalf("deregistered %q, state %+v; want the flow failed", e.Deregistered, u.State())
			}

			return e
		case <-timeout:
			t.Fatal("no deregistration event")
		}
	}
}

// TS 24.229 §5.1.2A.1.6, RFC 3261 §17.1.4: a reregistration whose connection is lost before its response fails at once
// with a transport error, leaving the contact bound to nothing: the UE is to register again.
func TestReregistrationConnectionLost(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Transport: sip.TCP})
	registerTCP(t, n, u)

	done := uetest.Start(u.Reregister)

	_, f := n.Recv(n.PS)
	n.PS.CloseConn(f.Remote)

	if err := uetest.Wait(t, done); !errors.Is(err, transaction.ErrTransport) {
		t.Fatalf("Reregister = %v, want a transport error", err)
	}

	if e := flowFailedWithin(t, u, time.Second); !errors.Is(e.Err, transaction.ErrTransport) {
		t.Fatalf("flow failed with %v, want a transport error", e.Err)
	}
}

// TS 24.229 §5.1.1.4.1: a reregistration timer F ends leaves the contact bound to nothing: the UE is to register again.
func TestReregistrationTimeout(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{T1: 10 * time.Millisecond})
	n.Register(u)

	if err := u.Reregister(t.Context()); !errors.Is(err, transaction.ErrTimeout) {
		t.Fatalf("Reregister = %v, want a timeout", err)
	}

	if e := flowFailedWithin(t, u, time.Second); !errors.Is(e.Err, transaction.ErrTimeout) {
		t.Fatalf("flow failed with %v, want a timeout", e.Err)
	}
}

// RFC 5626 §3, §4.4: the TCP connection of an outbound registration is its flow; its loss is the flow's failure.
func TestOutboundConnectionLost(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Transport: sip.TCP, RegID: 1})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	okOutbound(n, req, f, 600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	n.PS.CloseConn(f.Remote)

	flowFailedWithin(t, u, time.Second)
}

// Without outbound, the registration outlives its connection: the P-CSCF reaches the UE on one of its own.
func TestConnectionLostWithoutOutbound(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Transport: sip.TCP})
	f := registerTCP(t, n, u)

	n.PS.CloseConn(f.Remote)

	select {
	case e := <-u.Events():
		if e.Deregistered != "" {
			t.Fatalf("deregistered %q on the loss of a connection", e.Deregistered)
		}
	case <-time.After(200 * time.Millisecond):
	}

	if !u.State().Registered {
		t.Fatal("not registered after the loss of a connection")
	}
}
