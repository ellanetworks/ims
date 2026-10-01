package transaction_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

func TestServerNonInviteExpires(t *testing.T) {
	h := newHarness(t)
	tx, _ := h.serve("MESSAGE", sip.UDP)

	h.advance(64*t1 - time.Millisecond)
	h.wantResponse(100)
	wantState(t, tx, transaction.Proceeding)

	h.advance(time.Millisecond)
	wantTerminated(t, tx, transaction.ErrTimeout)

	if e := h.tu.NextError(); e.Tx != tx || !errors.Is(e.Err, transaction.ErrTimeout) {
		t.Errorf("TU error %v for %p, want ErrTimeout for %p", e.Err, e.Tx, tx)
	}

	h.peer.RecvNone(quiet)
}

func TestServerNonInviteAnsweredDoesNotExpire(t *testing.T) {
	h := newHarness(t)
	tx, _ := h.serve("MESSAGE", sip.UDP)

	h.respond(tx, 200)
	h.wantResponse(200)

	h.advance(64 * t1)
	wantTerminated(t, tx, nil)
	h.tu.None(quiet)
}

func TestServerRelay(t *testing.T) {
	h := newHarness(t)
	tx, req := h.serve("INVITE", sip.UDP)

	if err := tx.Relay(sip.NewResponse(req, 180, "")); err != nil {
		t.Fatal(err)
	}

	if to, _ := h.wantResponse(180).Header.To(); to.Tag() != "" {
		t.Errorf("relayed 180 got To tag %q", to.Tag())
	}

	if err := tx.Relay(sip.NewResponse(req, 486, "")); err != nil {
		t.Fatal(err)
	}

	if to, _ := h.wantResponse(486).Header.To(); to.Tag() != tx.ToTag() {
		t.Errorf("relayed tagless 486 has To tag %q, want %q", to.Tag(), tx.ToTag())
	}

	ok := sip.NewResponse(req, 200, "")
	_ = ok.Header.SetToTag("downstream")

	if err := tx.Relay(ok); err != nil {
		t.Fatalf("2xx after a non-2xx final: %v", err)
	}

	if to, _ := h.wantResponse(200).Header.To(); to.Tag() != "downstream" {
		t.Errorf("late 2xx To tag %q", to.Tag())
	}

	wantState(t, tx, transaction.Completed)

	if err := tx.Relay(sip.NewResponse(req, 500, "")); !errors.Is(err, transaction.ErrFinalResponseSent) {
		t.Errorf("second non-2xx: %v", err)
	}
}

func TestClientCancelExtraFields(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("INVITE", sip.UDP)

	h.reply(sent, 180)
	wantCode(t, rs.Next(), 180)

	if err := tx.Cancel(sip.Field{Name: "Reason", Value: "SIP;cause=200"}); err != nil {
		t.Fatal(err)
	}

	cancel := h.wantRequest("CANCEL")
	if cancel.Header.Get("Reason") != "SIP;cause=200" || cancel.Header.Values("Content-Length")[0] != "0" {
		t.Errorf("CANCEL:\n%s", cancel)
	}

	if last := cancel.Header[len(cancel.Header)-1]; last.Name != "Content-Length" {
		t.Errorf("last field %s, want Content-Length", last.Name)
	}
}

func TestLayerGo(t *testing.T) {
	h := newHarness(t)

	ran := make(chan error, 1)

	if err := h.l.Go(func(ctx context.Context) { <-ctx.Done(); ran <- ctx.Err() }); err != nil {
		t.Fatal(err)
	}

	if err := h.l.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-ran:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ctx.Err() = %v", err)
		}
	default:
		t.Fatal("Close returned before the goroutine ended")
	}

	if err := h.l.Go(func(context.Context) {}); !errors.Is(err, transaction.ErrClosed) {
		t.Errorf("Go after Close: %v", err)
	}
}

func TestOnTerminated(t *testing.T) {
	h := newHarness(t)
	tx, _ := h.serve("MESSAGE", sip.UDP)

	ended := make(chan string, 2)

	tx.OnTerminated(func() { ended <- "before" })

	h.respond(tx, 200)
	h.wantResponse(200)

	select {
	case <-ended:
		t.Fatal("OnTerminated ran before the transaction ended")
	default:
	}

	h.advance(64 * t1)
	wantTerminated(t, tx, nil)

	if got := <-ended; got != "before" {
		t.Fatalf("got %s", got)
	}

	tx.OnTerminated(func() { ended <- "after" })

	select {
	case got := <-ended:
		if got != "after" {
			t.Fatalf("got %s", got)
		}
	default:
		t.Fatal("OnTerminated on an ended transaction did not run")
	}
}
