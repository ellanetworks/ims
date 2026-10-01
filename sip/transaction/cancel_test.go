package transaction_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

func TestCancelWaitsForProvisional(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("INVITE", sip.UDP)

	if err := tx.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}

	h.peer.RecvNone(quiet)

	h.reply(sent, 180)
	wantCode(t, rs.Next(), 180)

	cancel := h.wantRequest("CANCEL")
	wantCancel(t, cancel, tx.Request())

	h.reply(cancel, 200)
	h.reply(sent, 487)
	wantCode(t, rs.Next(), 487)
	h.wantRequest("ACK")
	rs.None(quiet)
}

func TestCancelAfterFinalIsNoop(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("INVITE", sip.UDP)

	if err := tx.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}

	h.reply(sent, 486)
	wantCode(t, rs.Next(), 486)
	h.wantRequest("ACK")

	if err := tx.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}

	h.peer.RecvNone(quiet)
}

func TestCancelInProceeding(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("INVITE", sip.UDP)

	h.reply(sent, 183)
	wantCode(t, rs.Next(), 183)

	ctx := context.Background()
	if err := tx.Cancel(ctx); err != nil {
		t.Fatal(err)
	}

	wantCancel(t, h.wantRequest("CANCEL"), tx.Request())

	if err := tx.Cancel(ctx); err != nil {
		t.Fatal(err)
	}

	h.peer.RecvNone(quiet)

	h.advance(t1)
	h.wantRequest("CANCEL")

	h.advance(64*t1 - t1)

	if err := rs.NextError(); !errors.Is(err, transaction.ErrTimeout) {
		t.Fatalf("error %v, want timeout", err)
	}

	wantTerminated(t, tx, transaction.ErrTimeout)
}

func TestCancelNonInvite(t *testing.T) {
	h := newHarness(t)
	tx, _, _ := h.request("MESSAGE", sip.UDP)

	if err := tx.Cancel(context.Background()); !errors.Is(err, transaction.ErrNotInvite) {
		t.Errorf("Cancel: %v", err)
	}
}

func wantCancel(t *testing.T, cancel, invite *sip.Request) {
	t.Helper()

	if cancel.URI.String() != invite.URI.String() {
		t.Errorf("CANCEL Request-URI %s", cancel.URI)
	}

	if got, want := cancel.Header.Get("Via"), topVia(t, invite).String(); got != want {
		t.Errorf("CANCEL Via %q, want %q", got, want)
	}

	ic, _ := invite.Header.CSeq()
	if cc, _ := cancel.Header.CSeq(); cc != (sip.CSeq{Seq: ic.Seq, Method: "CANCEL"}) {
		t.Errorf("CANCEL CSeq %v", cc)
	}
}

func TestLargeRequestIsReliable(t *testing.T) {
	h := newHarness(t)

	req := h.newRequest("INVITE", sip.UDP)
	pad(req, 1400)

	tx, rs, sent := h.send(req)

	if f := tx.Request().Flow; f.Transport != sip.TCP {
		t.Fatalf("large INVITE sent over %s", f.Transport)
	}

	if v := topVia(t, sent); v.Transport != sip.TCP {
		t.Fatalf("large INVITE Via %s", v)
	}

	if n := h.clock.Pending(); n != 1 {
		t.Fatalf("%d timers, want only Timer B", n)
	}

	h.reply(sent, 180)
	wantCode(t, rs.Next(), 180)

	if err := tx.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}

	cancel, f := h.peer.RecvRequest()
	if cancel.Method != "CANCEL" || f.Transport != sip.TCP {
		t.Fatalf("got %q over %s", cancel.StartLine(), f.Transport)
	}

	wantCancel(t, cancel, tx.Request())
	h.reply(cancel, 200)
	busy := h.reply(sent, 487)

	wantCode(t, rs.Next(), 487)

	ack, f := h.peer.RecvRequest()
	if ack.Method != "ACK" || f.Transport != sip.TCP {
		t.Fatalf("got %q over %s", ack.StartLine(), f.Transport)
	}

	wantAck(t, ack, tx.Request(), busy)
	wantTerminated(t, tx, nil)
}
