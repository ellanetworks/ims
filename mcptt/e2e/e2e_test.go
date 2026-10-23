//go:build e2e && linux

// Package e2e runs the MCPTT scenarios through Ella IMS and Open5GS (compose.yaml): each user an MCPTT client on the
// agent of a testue UE, in the network namespace of a UE of the RAN simulator, which registers it with the IMS, and
// the IMS the MCPTT server's by its iFCs (provision.js). Run through run.sh, after up.sh.
package e2e

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/testue"
	imsue "github.com/ellanetworks/ims/ue"
	"golang.org/x/sys/unix"
)

var pcscf = netip.MustParseAddrPort("10.80.0.5:5060")

// subscriber is a subscriber of provision.js, in the UE container whose PID is in pidEnv.
type subscriber struct {
	imsi, msisdn, imei string
	pidEnv             string
}

var subscribers = []subscriber{
	{"001010000000001", "15550000001", "353490069873319", "E2E_UE1_PID"},
	{"001010000000002", "15550000002", "353490069873327", "E2E_UE2_PID"},
	{"001010000000003", "15550000003", "353490069873335", "E2E_UE3_PID"},
}

var (
	k   = mustHex("465b5ce8b199b49faa5f0a2ee238a6bc")
	opc = mustHex("cd63cb71954a9f4e48a5994e37a02baf")
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}

	return b
}

// netns runs functions on a thread in the network namespace of a process: the sockets they open are there.
type netns struct {
	work chan func()
}

func enter(t *testing.T, pid int) *netns {
	t.Helper()

	fd, err := unix.Open(fmt.Sprintf("/proc/%d/ns/net", pid), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open netns of pid %d: %v", pid, err)
	}

	n := &netns{work: make(chan func())}
	errc := make(chan error, 1)

	go func() {
		// Never unlocked: the thread, left in the namespace, ends with the goroutine.
		runtime.LockOSThread()

		err := unix.Setns(fd, unix.CLONE_NEWNET)
		_ = unix.Close(fd)

		errc <- err

		if err != nil {
			return
		}

		for f := range n.work {
			f()
		}
	}()

	if err := <-errc; err != nil {
		t.Fatalf("setns to pid %d: %v", pid, err)
	}

	t.Cleanup(func() { close(n.work) })

	return n
}

func (n *netns) Do(f func()) {
	done := make(chan struct{})

	n.work <- func() {
		defer close(done)

		f()
	}

	<-done
}

// tunAddr is the IPv4 address of the UE's PDU session or PDN connection, on E2E_TUN.
func tunAddr() (netip.Addr, error) {
	tunDevice := os.Getenv("E2E_TUN")

	ifc, err := net.InterfaceByName(tunDevice)
	if err != nil {
		return netip.Addr{}, err
	}

	addrs, err := ifc.Addrs()
	if err != nil {
		return netip.Addr{}, err
	}

	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Is4() {
			return p.Addr(), nil
		}
	}

	return netip.Addr{}, errors.New("no IPv4 address on " + tunDevice)
}

// ue is the test UE of a subscriber, in its UE container: ns its network namespace, local its address there, invites
// the initial INVITEs it received.
type ue struct {
	*testue.UE

	ns      *netns
	local   netip.Addr
	invites atomic.Int32
}

// newUE makes the test UE of subscriber i, a dedicated MC UE (no MMTel, no SMS) hosting the services.
func newUE(t *testing.T, i int, services ...imsue.Service) *ue {
	t.Helper()

	sub := subscribers[i]

	pid, err := strconv.Atoi(os.Getenv(sub.pidEnv))
	if err != nil {
		t.Skipf("%s not set: run through e2e/run.sh", sub.pidEnv)
	}

	ns := enter(t, pid)

	var (
		local netip.Addr
		xfrm  testue.XFRM
	)

	ns.Do(func() {
		if local, err = tunAddr(); err == nil {
			xfrm, err = testue.OpenXFRM()
		}
	})

	if err != nil {
		t.Fatalf("UE %d: %v", i+1, err)
	}

	t.Cleanup(func() { _ = xfrm.Close() })

	out := &ue{ns: ns, local: local}

	u, err := testue.New(testue.Config{
		IMSI: sub.imsi, IMEI: sub.imei, K: k, OPc: opc,
		PCSCF: pcscf, Local: local, Transport: sip.UDP,
		NoMMTel: true, NoSMS: true, Services: services,
		Kernel: xfrm, Do: ns.Do,
		Trace: func(m sip.Message, _ sip.Flow, sent bool) {
			if req, ok := m.(*sip.Request); ok && !sent && req.Method == "INVITE" {
				if to, err := req.Header.To(); err == nil && to.Tag() == "" {
					out.invites.Add(1)
				}
			}
		},
		Logger: slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelDebug})).With("ue", i+1),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_ = u.Deregister(ctx)
		_ = u.Close()
	})

	out.UE = u

	return out
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)

	return c
}

func register(t *testing.T, u *ue) {
	t.Helper()

	if err := u.Register(ctx(t)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	s := u.State()
	t.Logf("registered %s: default IMPU %s, associated %v", u.IMPI(), s.DefaultIMPU, s.AssociatedURIs)
}
