package server

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

func startServer(t *testing.T) *Server {
	t.Helper()

	srv := &Server{Config: testConfig(t), Logger: slog.New(slog.DiscardHandler)}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { srv.Shutdown(context.Background()) })

	return srv
}

// sipListener returns the SIP listener of the address family of a.
func sipListener(t *testing.T, srv *Server, a netip.Addr) netip.AddrPort {
	t.Helper()

	for _, l := range srv.sip.Listeners() {
		if l.Addr() == a {
			return l
		}
	}

	t.Fatalf("no SIP listener on %s in %v", a, srv.sip.Listeners())

	return netip.AddrPort{}
}

func wantResponse(t *testing.T, ue *siptest.Socket, code int, method string) *sip.Response {
	t.Helper()

	res, _ := ue.RecvResponse()

	cseq, err := res.Header.CSeq()
	if err != nil {
		t.Fatal(err)
	}

	if res.StatusCode != code || cseq.Method != method {
		t.Fatalf("got %q to %s, want %d to %s", res.StartLine(), cseq.Method, code, method)
	}

	return res
}

func TestSIPPlaceholder(t *testing.T) {
	srv := startServer(t)

	for _, addr := range []netip.Addr{loopback, loopback6} {
		for _, tr := range []sip.Transport{sip.UDP, sip.TCP} {
			t.Run(addr.String()+"/"+string(tr), func(t *testing.T) {
				pcscf := sipListener(t, srv, addr)
				ue := siptest.NewSocket(t, netip.AddrPortFrom(addr, 0))
				target := "sip:" + imsRealm

				t.Run("OPTIONS", func(t *testing.T) {
					ue.Send(tr, pcscf, siptest.NewRequest("OPTIONS", target, tr, ue.Addr()))

					res := wantResponse(t, ue, 200, "OPTIONS")
					if got := res.Header.Get("Allow"); got != placeholderAllow {
						t.Fatalf("Allow = %q, want %q", got, placeholderAllow)
					}
				})

				t.Run("REGISTER", func(t *testing.T) {
					ue.Send(tr, pcscf, siptest.NewRequest("REGISTER", target, tr, ue.Addr()))
					wantResponse(t, ue, 501, "REGISTER")
				})

				t.Run("INVITE CANCEL", func(t *testing.T) {
					invite := siptest.NewRequest("INVITE", "sip:+15551230002@"+imsRealm, tr, ue.Addr())
					ue.Send(tr, pcscf, invite)
					wantResponse(t, ue, 100, "INVITE")

					cancel, err := sip.NewCancel(invite)
					if err != nil {
						t.Fatal(err)
					}

					ue.Send(tr, pcscf, cancel)

					// The two transactions answer independently, in any order.
					got := map[string]*sip.Response{}

					for range 2 {
						res, _ := ue.RecvResponse()

						cseq, err := res.Header.CSeq()
						if err != nil {
							t.Fatal(err)
						}

						got[cseq.Method] = res
					}

					if res := got["CANCEL"]; res == nil || res.StatusCode != 200 {
						t.Fatalf("response to CANCEL = %v, want 200", res)
					}

					res := got["INVITE"]
					if res == nil || res.StatusCode != 487 {
						t.Fatalf("response to INVITE = %v, want 487", res)
					}

					ack, err := sip.NewAck(invite, res)
					if err != nil {
						t.Fatal(err)
					}

					ue.Send(tr, pcscf, ack)
				})
			})
		}
	}
}

func TestSIPShutdownClosesListeners(t *testing.T) {
	srv := &Server{Config: testConfig(t), Logger: slog.New(slog.DiscardHandler)}

	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	listeners := srv.sip.Listeners()
	if len(listeners) != 2 {
		t.Fatalf("listeners = %v, want one per address", listeners)
	}

	srv.Shutdown(ctx)

	for _, l := range listeners {
		var d net.Dialer

		if c, err := d.DialContext(ctx, "tcp", l.String()); err == nil {
			_ = c.Close()

			t.Fatalf("TCP connect to %s succeeded after Shutdown", l)
		}

		// The UDP port is free again.
		var lc net.ListenConfig

		pc, err := lc.ListenPacket(ctx, "udp", l.String())
		if err != nil {
			t.Fatalf("UDP port %s still held after Shutdown: %v", l, err)
		}

		_ = pc.Close()
	}
}

func TestSIPListenFailureFailsStart(t *testing.T) {
	taken := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	cfg := testConfig(t)
	cfg.SIP.Port = int(taken.Addr().Port())
	// ::1 binds first, so the failure on 127.0.0.1 must release it.
	cfg.SIP.Addresses = []netip.Addr{loopback6, loopback}

	srv := &Server{Config: cfg, Logger: slog.New(slog.DiscardHandler)}

	ctx := context.Background()

	err := srv.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "start SIP") {
		srv.Shutdown(ctx)
		t.Fatalf("Start = %v, want a SIP listen error", err)
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", netip.AddrPortFrom(loopback6, taken.Addr().Port()).String())
	if err != nil {
		t.Fatalf("[::1]:%d still held after the failed Start: %v", taken.Addr().Port(), err)
	}

	_ = ln.Close()

	taken.Close()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start after the port is free: %v", err)
	}

	srv.Shutdown(ctx)
}

func TestSIPPlaceholderRefusesUncancelledInvite(t *testing.T) {
	h := newPlaceholderHandler(slog.New(slog.DiscardHandler))
	h.inviteTimeout = 50 * time.Millisecond

	layer, _ := siptest.NewLayer(t, transaction.Config{Handler: h, Logger: slog.New(slog.DiscardHandler)})
	pcscf := siptest.ListenLayer(t, layer, loopback)
	ue := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	ue.Send(sip.UDP, pcscf, siptest.NewRequest("INVITE", "sip:+15551230002@"+imsRealm, sip.UDP, ue.Addr()))
	wantResponse(t, ue, 100, "INVITE")
	wantResponse(t, ue, 501, "INVITE")
}
