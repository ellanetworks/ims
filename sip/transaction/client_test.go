package transaction_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

func TestClientInviteTimerAB(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("INVITE", sip.UDP)
	wantState(t, tx, transaction.Calling)

	for interval := t1; interval < 32*time.Second; interval *= 2 {
		h.advance(interval - time.Millisecond)
		h.peer.RecvNone(quiet)
		h.advance(time.Millisecond)

		got := h.wantRequest("INVITE")
		if got.String() != sent.String() {
			t.Fatalf("retransmission differs:\n%s\nwant\n%s", got, sent)
		}
	}

	if now := h.clock.Now(); now != 31500*time.Millisecond {
		t.Fatalf("last retransmission at %s", now)
	}

	h.advance(500 * time.Millisecond)

	if err := rs.NextError(); !errors.Is(err, transaction.ErrTimeout) {
		t.Fatalf("error %v, want timeout", err)
	}

	wantTerminated(t, tx, transaction.ErrTimeout)
	rs.None(quiet)
	h.peer.RecvNone(quiet)

	if n := h.clock.Pending(); n != 0 {
		t.Errorf("%d timers left", n)
	}
}

func TestClientInviteProceeding(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("INVITE", sip.UDP)

	h.reply(sent, 100)
	wantCode(t, rs.Next(), 100)
	wantState(t, tx, transaction.Proceeding)

	if n := h.clock.Pending(); n != 0 {
		t.Fatalf("%d timers running in Proceeding", n)
	}

	h.reply(sent, 180)
	wantCode(t, rs.Next(), 180)
	h.reply(sent, 183)
	wantCode(t, rs.Next(), 183)

	h.advance(time.Minute)
	h.peer.RecvNone(quiet)
	rs.None(quiet)
	wantState(t, tx, transaction.Proceeding)
}

func TestClientInviteAccepted(t *testing.T) {
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) {
			h := newHarness(t)
			tx, rs, sent := h.request("INVITE", tr)

			ok := h.reply(sent, 200)

			wantCode(t, rs.Next(), 200)
			wantState(t, tx, transaction.Accepted)

			h.peer.Send(tr, h.local, ok)
			wantCode(t, rs.Next(), 200)
			h.peer.RecvNone(quiet)

			h.advance(64*t1 - time.Millisecond)
			wantState(t, tx, transaction.Accepted)
			h.advance(time.Millisecond)
			wantTerminated(t, tx, nil)

			h.peer.Send(tr, h.local, ok)
			rs.None(quiet)
			h.tu.None(quiet)
		})
	}
}

func TestClientInviteCompleted(t *testing.T) {
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) {
			h := newHarness(t)

			req := h.newRequest("INVITE", tr)
			req.Header.Add("Route", "<sip:p1;lr>, <sip:p2;lr>")

			tx, rs, sent := h.send(req)
			busy := h.reply(sent, 486)

			wantCode(t, rs.Next(), 486)

			ack, f := h.peer.RecvRequest()
			if f.Transport != tr {
				t.Fatalf("ACK over %s", f.Transport)
			}

			wantAck(t, ack, tx.Request(), busy)

			if tr == sip.TCP {
				wantTerminated(t, tx, nil)
				return
			}

			wantState(t, tx, transaction.Completed)

			h.peer.Send(tr, h.local, busy)

			if again := h.wantRequest("ACK"); again.String() != ack.String() {
				t.Fatalf("ACK retransmission differs:\n%s\nwant\n%s", again, ack)
			}

			rs.None(quiet)

			h.advance(32*time.Second - time.Millisecond)
			wantState(t, tx, transaction.Completed)
			h.advance(time.Millisecond)
			wantTerminated(t, tx, nil)
		})
	}
}

func wantAck(t *testing.T, ack, invite *sip.Request, res *sip.Response) {
	t.Helper()

	if ack.Method != "ACK" || ack.URI.String() != invite.URI.String() {
		t.Fatalf("got %q", ack.StartLine())
	}

	if got, want := ack.Header.Get("Via"), topVia(t, invite).String(); got != want {
		t.Errorf("ACK Via %q, want %q", got, want)
	}

	checks := []struct{ name, got, want string }{
		{"From", ack.Header.Get("From"), invite.Header.Get("From")},
		{"To", ack.Header.Get("To"), res.Header.Get("To")},
		{"Call-ID", ack.Header.CallID(), invite.Header.CallID()},
		{"Route", ack.Header.Get("Route"), invite.Header.Get("Route")},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("ACK %s %q, want %q", c.name, c.got, c.want)
		}
	}

	ic, _ := invite.Header.CSeq()
	if ac, _ := ack.Header.CSeq(); ac != (sip.CSeq{Seq: ic.Seq, Method: "ACK"}) {
		t.Errorf("ACK CSeq %v", ac)
	}
}

func TestClientNonInviteTimerEF(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("OPTIONS", sip.UDP)
	wantState(t, tx, transaction.Trying)

	n := 0

	for interval := t1; h.clock.Now()+interval < 64*t1; interval = min(2*interval, t2) {
		h.advance(interval - time.Millisecond)
		h.peer.RecvNone(quiet)
		h.advance(time.Millisecond)

		if got := h.wantRequest("OPTIONS"); got.String() != sent.String() {
			t.Fatalf("retransmission differs:\n%s\nwant\n%s", got, sent)
		}

		n++
	}

	if n != 10 {
		t.Fatalf("%d retransmissions, want 10", n)
	}

	h.advance(64*t1 - h.clock.Now())

	if err := rs.NextError(); !errors.Is(err, transaction.ErrTimeout) {
		t.Fatalf("error %v, want timeout", err)
	}

	wantTerminated(t, tx, transaction.ErrTimeout)
	h.peer.RecvNone(quiet)
	rs.None(quiet)
}

func TestClientNonInviteProceeding(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("OPTIONS", sip.UDP)

	h.advance(t1)
	h.wantRequest("OPTIONS")

	h.reply(sent, 100)
	wantCode(t, rs.Next(), 100)
	wantState(t, tx, transaction.Proceeding)

	h.advance(2*t1 - time.Millisecond)
	h.peer.RecvNone(quiet)
	h.advance(time.Millisecond)
	h.wantRequest("OPTIONS")

	h.advance(t2 - time.Millisecond)
	h.peer.RecvNone(quiet)
	h.advance(time.Millisecond)
	h.wantRequest("OPTIONS")

	ok := h.reply(sent, 200)

	wantCode(t, rs.Next(), 200)
	wantState(t, tx, transaction.Completed)

	h.peer.Send(sip.UDP, h.local, ok)
	rs.None(quiet)

	h.advance(t4 - time.Millisecond)
	wantState(t, tx, transaction.Completed)
	h.advance(time.Millisecond)
	wantTerminated(t, tx, nil)

	h.peer.Send(sip.UDP, h.local, ok)
	rs.None(quiet)
	h.peer.RecvNone(quiet)
}

func TestClientNonInviteTCP(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("REGISTER", sip.TCP)

	if n := h.clock.Pending(); n != 1 {
		t.Fatalf("%d timers, want only Timer F", n)
	}

	h.reply(sent, 401)
	wantCode(t, rs.Next(), 401)
	wantTerminated(t, tx, nil)
}

func TestClientMatchesAcrossFlows(t *testing.T) {
	h := newHarness(t)
	_, rs, sent := h.request("INVITE", sip.UDP)

	res := sip.NewResponse(sent, 183, "")
	_ = res.Header.SetToTag("peer")
	h.peer.Send(sip.TCP, h.local, res)

	got := rs.Next()
	wantCode(t, got, 183)

	if got.Flow.Transport != sip.TCP {
		t.Errorf("183 flow %+v", got.Flow)
	}
}

func TestClientDropsForeignSentBy(t *testing.T) {
	h := newHarness(t)
	_, rs, sent := h.request("INVITE", sip.UDP)

	res := sip.NewResponse(sent, 180, "")
	via := topVia(t, res)
	via.Host = "192.0.2.1"
	_ = res.Header.SetTopVia(via)
	h.peer.Send(sip.UDP, h.local, res)

	stray := sip.NewResponse(sent, 200, "")

	via.Params.Set("branch", sip.NewBranch())
	_ = stray.Header.SetTopVia(via)
	h.peer.Send(sip.UDP, h.local, stray)

	h.barrier()
	rs.None(0)
	h.tu.None(0)
}

func TestClientRejectsRequests(t *testing.T) {
	h := newHarness(t)

	if _, err := h.l.Request(h.newRequest("ACK", sip.UDP), nil); !errors.Is(err, transaction.ErrAck) {
		t.Errorf("ACK: %v", err)
	}

	old := h.newRequest("OPTIONS", sip.UDP)
	via := topVia(t, old)
	via.Params.Set("branch", "1234")
	_ = old.Header.SetTopVia(via)

	if _, err := h.l.Request(old, nil); err == nil {
		t.Error("accepted an RFC 2543 branch")
	}

	req := h.newRequest("OPTIONS", sip.UDP)
	if _, err := h.l.Request(req, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := h.l.Request(req, nil); !errors.Is(err, transaction.ErrExists) {
		t.Errorf("duplicate: %v", err)
	}

	unrouted := h.newRequest("OPTIONS", sip.UDP)
	unrouted.Flow.Local = netip.AddrPortFrom(loopback, 1)

	if _, err := h.l.Request(unrouted, nil); err == nil {
		t.Error("sent from a socket we do not listen on")
	}

	retry := unrouted.Clone()
	retry.Flow = req.Flow

	if _, err := h.l.Request(retry, nil); err != nil {
		t.Errorf("branch still held after a failed send: %v", err)
	}
}

func TestStatelessSends(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if err := h.l.SendAck(ctx, h.newRequest("BYE", sip.UDP)); err == nil {
		t.Error("SendAck sent a BYE")
	}

	if err := h.l.SendAck(ctx, h.newRequest("ACK", sip.UDP)); err != nil {
		t.Fatal(err)
	}

	h.wantRequest("ACK")

	req := h.peerRequest("MESSAGE", sip.UDP)
	stx := h.tu.NextRequest().Tx

	if err := h.l.SendResponse(ctx, &sip.Response{}); err == nil {
		t.Error("sent a response without a flow")
	}

	res := sip.NewResponse(req, 202, "")
	res.Flow = sip.Flow{Transport: sip.UDP, Local: h.local}

	if err := h.l.SendResponse(ctx, res); err != nil {
		t.Fatal(err)
	}

	h.wantResponse(202)

	wantState(t, stx, transaction.Trying)
}
