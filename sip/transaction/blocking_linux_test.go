package transaction_test

import (
	"context"
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

func stalledListener(t *testing.T) netip.AddrPort {
	t.Helper()

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = syscall.Close(fd) })

	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}

	if err := syscall.Listen(fd, 0); err != nil {
		t.Fatal(err)
	}

	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}

	addr := netip.AddrPortFrom(loopback, uint16(sa.(*syscall.SockaddrInet4).Port))

	d := net.Dialer{Timeout: time.Second}

	filler, err := d.DialContext(context.Background(), "tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = filler.Close() })

	return addr
}

func TestReaderNeverBlocksOnSend(t *testing.T) {
	stalled := stalledListener(t)

	h := newHarnessConfig(t, transaction.Config{
		ResponseFlow: func(req *sip.Request, _ *sip.Response) (sip.Flow, bool, error) {
			if req.Method != "INVITE" {
				return sip.Flow{}, false, nil
			}

			return sip.Flow{Transport: sip.TCP, Local: req.Flow.Local, Remote: stalled}, true, nil
		},
	})

	h.peerRequest("INVITE", sip.UDP)
	invite := h.tu.NextRequest()

	start := time.Now()

	if err := invite.Tx.Respond(sip.NewResponse(invite.Req, 180, "")); err != nil {
		t.Fatal(err)
	}

	if _, err := h.l.Request(h.newRequest("OPTIONS", sip.TCP), nil); err != nil {
		t.Fatal(err)
	}

	h.peerRequest("MESSAGE", sip.UDP)

	if r := h.tu.NextRequest(); r.Req.Method != "MESSAGE" {
		t.Fatalf("got %s", r.Req.Method)
	}

	if d := time.Since(start); d > time.Second {
		t.Fatalf("calls blocked for %s behind a stalled connect", d)
	}
}
