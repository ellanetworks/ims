package transaction_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

func TestServerInviteProceeding(t *testing.T) {
	h := newHarness(t)
	tx, req := h.serve("INVITE", sip.UDP)
	wantState(t, tx, transaction.Proceeding)

	h.peer.Send(sip.UDP, h.local, req)
	h.wantResponse(100)
	h.tu.None(quiet)

	h.respond(tx, 180)
	h.wantResponse(180)

	h.peer.Send(sip.UDP, h.local, req)
	h.wantResponse(180)
	h.tu.None(quiet)

	h.advance(time.Minute)
	h.peer.RecvNone(quiet)
	wantState(t, tx, transaction.Proceeding)
}

func TestServerInviteCompleted(t *testing.T) {
	h := newHarness(t)
	tx, req := h.serve("INVITE", sip.UDP)

	h.respond(tx, 486)
	busy := h.wantResponse(486)

	wantState(t, tx, transaction.Completed)

	h.advance(t1 - time.Millisecond)
	h.peer.RecvNone(quiet)
	h.advance(time.Millisecond)

	if again := h.wantResponse(486); again.String() != busy.String() {
		t.Fatalf("retransmission differs:\n%s\nwant\n%s", again, busy)
	}

	h.peer.Send(sip.UDP, h.local, req)
	h.wantResponse(486)

	if err := tx.Respond(sip.NewResponse(req, 500, "")); !errors.Is(err, transaction.ErrFinalResponseSent) {
		t.Errorf("second final response: %v", err)
	}

	ack, err := sip.NewAck(req, busy)
	if err != nil {
		t.Fatal(err)
	}

	h.peer.Send(sip.UDP, h.local, ack)
	waitState(t, tx, transaction.Confirmed)

	h.peer.Send(sip.UDP, h.local, ack)
	h.peer.Send(sip.UDP, h.local, req)
	h.serverBarrier()
	h.peer.RecvNone(0)
	h.tu.None(0)

	h.advance(t4)
	wantTerminated(t, tx, nil)
	h.peer.RecvNone(quiet)
}

func TestServerInviteTimerGH(t *testing.T) {
	h := newHarness(t)
	tx, _ := h.serve("INVITE", sip.UDP)
	h.respond(tx, 404)
	h.wantResponse(404)

	n := 0

	for interval := t1; h.clock.Now()+interval < 64*t1; interval = min(2*interval, t2) {
		h.advance(interval - time.Millisecond)
		h.peer.RecvNone(quiet)
		h.advance(time.Millisecond)
		h.wantResponse(404)

		n++
	}

	if n != 10 {
		t.Fatalf("%d retransmissions, want 10", n)
	}

	h.advance(64*t1 - h.clock.Now())
	wantTerminated(t, tx, transaction.ErrTimeout)

	if e := h.tu.NextError(); e.Tx != tx || !errors.Is(e.Err, transaction.ErrTimeout) {
		t.Fatalf("HandleTransactionError(%p, %v)", e.Tx, e.Err)
	}

	h.peer.RecvNone(quiet)
}

func TestServerInviteConfirmedTimerI(t *testing.T) {
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) {
			h := newHarness(t)
			tx, req := h.serve("INVITE", tr)
			h.respond(tx, 603)
			declined := h.wantResponse(603)

			ack, _ := sip.NewAck(req, declined)
			h.peer.Send(tr, h.local, ack)

			if tr == sip.TCP {
				waitState(t, tx, transaction.Terminated)
				wantTerminated(t, tx, nil)

				return
			}

			waitState(t, tx, transaction.Confirmed)
			h.advance(t4 - time.Millisecond)
			wantState(t, tx, transaction.Confirmed)
			h.advance(time.Millisecond)
			wantTerminated(t, tx, nil)
		})
	}
}

func TestServerInviteAccepted(t *testing.T) {
	h := newHarness(t)
	tx, req := h.serve("INVITE", sip.UDP)

	ok := h.respond(tx, 200)
	h.wantResponse(200)
	wantState(t, tx, transaction.Accepted)

	h.peer.Send(sip.UDP, h.local, req)
	h.peer.RecvNone(quiet)

	if err := tx.Respond(ok); err != nil {
		t.Fatalf("2xx retransmission: %v", err)
	}

	h.wantResponse(200)

	if err := tx.Respond(sip.NewResponse(req, 486, "")); !errors.Is(err, transaction.ErrFinalResponseSent) {
		t.Errorf("486 after 200: %v", err)
	}

	ack := sip.NewRequest("ACK", req.URI)
	ack.Header = nil

	for _, name := range []string{"Via", "From", "Call-ID"} {
		ack.Header.Add(name, req.Header.Get(name))
	}

	ack.Header.Add("To", ok.Header.Get("To"))
	ack.Header.Add("CSeq", "1 ACK")
	ack.Header.Add("Content-Length", "0")

	h.peer.Send(sip.UDP, h.local, ack)

	if got := h.tu.NextAck(); got.Header.CallID() != req.Header.CallID() {
		t.Errorf("ACK %q", got.StartLine())
	}

	via := topVia(t, ack)
	via.Params.Set("branch", sip.NewBranch())
	_ = ack.Header.SetTopVia(via)
	h.peer.Send(sip.UDP, h.local, ack)
	h.tu.NextAck()

	h.advance(64*t1 - time.Millisecond)
	wantState(t, tx, transaction.Accepted)
	h.advance(time.Millisecond)
	wantTerminated(t, tx, nil)

	if err := tx.Respond(ok); !errors.Is(err, transaction.ErrTerminated) {
		t.Errorf("Respond after Timer L: %v", err)
	}
}

func TestServerNonInvite(t *testing.T) {
	h := newHarness(t)
	tx, req := h.serve("OPTIONS", sip.UDP)
	wantState(t, tx, transaction.Trying)

	h.peer.Send(sip.UDP, h.local, req)
	h.peer.RecvNone(quiet)
	h.tu.None(quiet)

	h.advance(trying100 - time.Millisecond)
	h.peer.RecvNone(quiet)
	h.advance(time.Millisecond)
	h.wantResponse(100)
	wantState(t, tx, transaction.Proceeding)

	h.peer.Send(sip.UDP, h.local, req)
	h.wantResponse(100)

	h.respond(tx, 200)
	h.wantResponse(200)
	wantState(t, tx, transaction.Completed)

	end := h.clock.Now() + 64*t1

	h.advance(time.Minute / 2)
	h.peer.RecvNone(quiet)

	h.peer.Send(sip.UDP, h.local, req)
	h.wantResponse(200)
	h.tu.None(quiet)

	if err := tx.Respond(sip.NewResponse(req, 500, "")); !errors.Is(err, transaction.ErrFinalResponseSent) {
		t.Errorf("second final response: %v", err)
	}

	h.advance(end - h.clock.Now() - time.Millisecond)
	wantState(t, tx, transaction.Completed)
	h.advance(time.Millisecond)
	wantTerminated(t, tx, nil)

	h.peer.Send(sip.UDP, h.local, req)
	h.tu.NextRequest()
}

func TestServerNonInviteRFC4320(t *testing.T) {
	h := newHarness(t)
	udp, req := h.serve("SUBSCRIBE", sip.UDP)

	for _, code := range []int{100, 180, 183, 408} {
		if err := udp.Respond(sip.NewResponse(req, code, "")); !errors.Is(err, transaction.ErrInvalidResponse) {
			t.Errorf("%d over UDP at once: %v", code, err)
		}
	}

	tcp, _ := h.serve("MESSAGE", sip.TCP)
	h.respond(tcp, 100)
	h.wantResponse(100)

	idle, _ := h.serve("OPTIONS", sip.TCP)

	h.advance(trying100)
	h.wantResponse(100)
	h.wantResponse(100)
	wantState(t, udp, transaction.Proceeding)
	wantState(t, idle, transaction.Proceeding)

	h.respond(udp, 100)
	h.wantResponse(100)

	if err := udp.Respond(sip.NewResponse(req, 408, "")); !errors.Is(err, transaction.ErrInvalidResponse) {
		t.Errorf("408: %v", err)
	}

	h.respond(udp, 200)
	h.wantResponse(200)
}

func TestServerNonInviteTCP(t *testing.T) {
	h := newHarness(t)
	tx, _ := h.serve("REGISTER", sip.TCP)

	h.respond(tx, 401)

	_, f := h.peer.RecvResponse()
	if f.Transport != sip.TCP {
		t.Fatalf("401 over %s", f.Transport)
	}

	wantTerminated(t, tx, nil)

	if n := h.clock.Pending(); n != 0 {
		t.Errorf("%d timers left", n)
	}
}

func TestServerRespondsOnRequestFlow(t *testing.T) {
	h := newHarness(t)
	tx, _ := h.serve("MESSAGE", sip.TCP)

	res := sip.NewResponse(tx.Request(), 202, "")
	res.Flow = sip.Flow{}

	if err := tx.Respond(res); err != nil {
		t.Fatal(err)
	}

	_, f := h.peer.RecvResponse()
	if f.Transport != sip.TCP || f.Remote != h.local {
		t.Errorf("response flow %+v", f)
	}

	if h.peer.Opened() != 1 {
		t.Errorf("%d connections, want the request's", h.peer.Opened())
	}
}

func TestServerCancel(t *testing.T) {
	h := newHarness(t)
	tx, req := h.serve("INVITE", sip.UDP)
	h.respond(tx, 180)

	ringing := h.wantResponse(180)
	if to, _ := ringing.Header.To(); to.Tag() != tx.ToTag() {
		t.Fatalf("180 To tag %q, want the transaction's %q", to.Tag(), tx.ToTag())
	}

	cancel, err := sip.NewCancel(req)
	if err != nil {
		t.Fatal(err)
	}

	h.peer.Send(sip.UDP, h.local, cancel)

	ok := h.wantResponse(200)
	if cs, _ := ok.Header.CSeq(); cs.Method != "CANCEL" {
		t.Fatalf("200 for %s", cs.Method)
	}

	to, _ := ok.Header.To()
	if want, _ := ringing.Header.To(); to.Tag() != want.Tag() {
		t.Errorf("To tag %q, want %q", to.Tag(), want.Tag())
	}

	c := h.tu.NextCancel()
	if c.Tx != tx || c.Req.Method != "CANCEL" {
		t.Fatalf("HandleCancel(%p, %s)", c.Tx, c.Req.Method)
	}

	wantState(t, tx, transaction.Proceeding)

	h.peer.Send(sip.UDP, h.local, cancel)
	h.wantResponse(200)
	h.tu.None(quiet)

	h.respond(tx, 487)
	terminated := h.wantResponse(487)

	ack, _ := sip.NewAck(req, terminated)
	h.peer.Send(sip.UDP, h.local, ack)
	waitState(t, tx, transaction.Confirmed)
	h.tu.None(quiet)
}

// RFC 3261 §9.2
func TestServerCancelTagOfARelayedResponse(t *testing.T) {
	h := newHarness(t)
	tx, req := h.serve("INVITE", sip.UDP)

	ringing := sip.NewResponse(req, 180, "")
	_ = ringing.Header.SetToTag("callee")

	if err := tx.Relay(ringing); err != nil {
		t.Fatal(err)
	}

	h.wantResponse(180)

	cancel, err := sip.NewCancel(req)
	if err != nil {
		t.Fatal(err)
	}

	h.peer.Send(sip.UDP, h.local, cancel)

	if to, _ := h.wantResponse(200).Header.To(); to.Tag() != "callee" {
		t.Errorf("200 to CANCEL To tag %q, want the relayed 180's %q", to.Tag(), "callee")
	}
}

func TestServerCancelWithoutInvite(t *testing.T) {
	h := newHarness(t)

	cancel, _ := sip.NewCancel(siptestInvite(h))
	h.peer.Send(sip.UDP, h.local, cancel)

	res := h.wantResponse(481)
	if to, _ := res.Header.To(); to.Tag() == "" {
		t.Error("481 without a To tag")
	}

	h.tu.None(quiet)
}

func TestServerCancelAfterFinal(t *testing.T) {
	h := newHarness(t)
	tx, req := h.serve("INVITE", sip.UDP)
	h.respond(tx, 486)
	h.wantResponse(486)

	cancel, _ := sip.NewCancel(req)
	h.peer.Send(sip.UDP, h.local, cancel)
	h.wantResponse(200)
	h.serverBarrier()
	h.tu.None(0)
}

func siptestInvite(h *harness) *sip.Request {
	req := h.newRequest("INVITE", sip.UDP)
	via := topVia(h.t, req)
	via.Host, via.Port = h.peer.Addr().Addr().String(), h.peer.Addr().Port()
	_ = req.Header.SetTopVia(via)

	return req
}

func waitState(t *testing.T, tx stater, want transaction.State) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for tx.State() != want {
		if time.Now().After(deadline) {
			t.Fatalf("state %s, want %s", tx.State(), want)
		}

		time.Sleep(time.Millisecond)
	}
}

func TestServerTransportError(t *testing.T) {
	h := newHarness(t)
	tx, _ := h.serve("INVITE", sip.TCP)

	h.peer.Close()

	for range 2 {
		if err := tx.Respond(sip.NewResponse(tx.Request(), 180, "")); err != nil {
			t.Fatal(err)
		}

		time.Sleep(quiet)
	}

	if e := h.tu.NextError(); e.Tx != tx || e.Err == nil {
		t.Fatalf("HandleTransactionError(%p, %v)", e.Tx, e.Err)
	}

	wantState(t, tx, transaction.Proceeding)

	if err := tx.Err(); err != nil {
		t.Errorf("Err() = %v after a failed send", err)
	}
}
