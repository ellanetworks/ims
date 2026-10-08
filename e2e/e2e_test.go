//go:build e2e && linux

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
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/testue"
	"github.com/ellanetworks/ims/sip"
	"golang.org/x/sys/unix"
)

const homeDomain = "ims.mnc001.mcc001.3gppnetwork.org"

var pcscf = netip.MustParseAddrPort("10.80.0.5:5060")

type subscriber struct {
	imsi, msisdn, imei string
	pidEnv             string
}

var subscribers = []subscriber{
	{"001010000000001", "15550000001", "353490069873319", "E2E_UE1_PID"},
	{"001010000000002", "15550000002", "353490069873327", "E2E_UE2_PID"},
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

func newUE(t *testing.T, i int, tr sip.Transport) *testue.UE {
	t.Helper()

	sub := subscribers[i]

	pid, err := strconv.Atoi(os.Getenv(sub.pidEnv))
	if err != nil {
		t.Skipf("%s not set: run through e2e/run.sh", sub.pidEnv)
	}

	ns := enter(t, pid)

	var (
		local netip.Addr
		xfrm  *ipsec.XFRM
	)

	ns.Do(func() {
		if local, err = tunAddr(); err == nil {
			xfrm, err = ipsec.Open()
		}
	})

	if err != nil {
		t.Fatalf("UE %d: %v", i+1, err)
	}

	t.Cleanup(func() { _ = xfrm.Close() })

	u, err := testue.New(testue.Config{
		IMSI:        sub.imsi,
		IMEI:        sub.imei,
		K:           k,
		OPc:         opc,
		PCSCF:       pcscf,
		Local:       local,
		Transport:   tr,
		AcceptCalls: true,
		Kernel:      xfrm,
		Do:          ns.Do,
		Logger:      slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelDebug})).With("ue", i+1),
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

	return u
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)

	return c
}

func register(t *testing.T, u *testue.UE) {
	t.Helper()

	if err := u.Register(ctx(t)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	s := u.State()
	t.Logf("registered %s: default IMPU %s, associated %v", u.IMPI(), s.DefaultIMPU, s.AssociatedURIs)
}

func incoming(t *testing.T, u *testue.UE) *testue.Call {
	t.Helper()

	select {
	case c := <-u.Calls():
		return c
	case <-time.After(15 * time.Second):
		t.Fatal("no incoming call")
	}

	return nil
}

func ended(t *testing.T, c *testue.Call, want testue.EndReason) {
	t.Helper()

	select {
	case <-c.Done():
	case <-time.After(15 * time.Second):
		t.Fatalf("call still %s, want it ended by %s", c.State(), want)
	}

	if got := c.End(); got != want {
		t.Fatalf("call ended by %s, want %s", got, want)
	}
}

func connect(t *testing.T, a, b *testue.UE, target string, opts testue.CallOptions) (*testue.Call, *testue.Call) {
	t.Helper()

	c := ctx(t)

	ac, err := a.Invite(target, opts)
	if err != nil {
		t.Fatalf("Invite %s: %v", target, err)
	}

	bc := incoming(t, b)

	if err := bc.Ring(c); err != nil {
		t.Fatalf("Ring: %v", err)
	}

	done := make(chan error, 1)

	go func() { done <- bc.Answer(c) }()

	if res, err := ac.Wait(c); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v, want 200", res, err)
	}

	if err := <-done; err != nil {
		t.Fatalf("Answer: %v", err)
	}

	return ac, bc
}

func pair(t *testing.T, tr sip.Transport) (*testue.UE, *testue.UE) {
	a, b := newUE(t, 0, tr), newUE(t, 1, tr)
	register(t, a)
	register(t, b)

	return a, b
}

func TestRegister(t *testing.T) {
	for _, tr := range []sip.Transport{sip.UDP, sip.TCP} {
		t.Run(string(tr), func(t *testing.T) {
			pair(t, tr)
		})
	}
}

func TestCall(t *testing.T) {
	// The Open5GS HSS provisions numbers as tel:<digits>, without the "+"
	// TS 23.003 §13.4 requires, so calls by number cannot reach the callee.
	targets := map[string]string{
		"sip-msisdn": "sip:" + subscribers[1].msisdn + "@" + homeDomain,
	}

	core := enter(t, pidOf(t, "E2E_OPEN5GS_PID"))

	for _, tr := range []sip.Transport{sip.UDP, sip.TCP} {
		t.Run(string(tr), func(t *testing.T) {
			a, b := pair(t, tr)

			for name, target := range targets {
				t.Run(name, func(t *testing.T) {
					ac, bc := connect(t, a, b, target, testue.CallOptions{})

					if err := ac.Bye(ctx(t)); err != nil {
						t.Fatalf("Bye: %v", err)
					}

					ended(t, ac, testue.LocalBye)
					ended(t, bc, testue.RemoteBye)
					wantCallRecord(t, core, ac, "caller")
				})
			}

			t.Run("preconditions", func(t *testing.T) {
				ac, bc := connect(t, a, b, targets["sip-msisdn"], testue.CallOptions{Preconditions: true})

				if err := bc.Bye(ctx(t)); err != nil {
					t.Fatalf("Bye: %v", err)
				}

				ended(t, bc, testue.LocalBye)
				ended(t, ac, testue.RemoteBye)
				wantCallRecord(t, core, ac, "callee")
			})
		})
	}
}
