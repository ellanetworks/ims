package transaction_test

import (
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

func (h *harness) barrier() {
	h.t.Helper()

	_, rs, sent := h.request("OPTIONS", sip.UDP)
	h.reply(sent, 200)
	wantCode(h.t, rs.Next(), 200)
}

func (h *harness) serverBarrier() {
	h.t.Helper()

	h.peerRequest("MESSAGE", sip.UDP)

	r := h.tu.NextRequest()
	if r.Req.Method != "MESSAGE" {
		h.t.Fatalf("TU got %s before the barrier", r.Req.Method)
	}

	h.respond(r.Tx, 200)
	h.wantResponse(200)
}

func TestSentByAliases(t *testing.T) {
	h := newHarnessConfig(t, transaction.Config{Aliases: []string{"PCSCF.ims.example", "192.0.2.10"}})

	tests := []struct {
		host string
		port uint16
		ours bool
	}{
		{"pcscf.ims.example", h.local.Port(), true},
		{"192.0.2.10", h.local.Port(), true},
		{"pcscf.ims.example", h.local.Port() + 1, false},
		{"other.ims.example", h.local.Port(), false},
		{"127.0.0.1", 0, false},
	}

	for _, tt := range tests {
		req := h.newRequest("OPTIONS", sip.UDP)
		via := topVia(t, req)
		via.Host, via.Port = tt.host, tt.port
		_ = req.Header.SetTopVia(via)

		_, rs, sent := h.send(req)
		h.reply(sent, 200)

		if tt.ours {
			wantCode(t, rs.Next(), 200)
			continue
		}

		h.barrier()
		rs.None(0)
	}
}

func TestSentByNormalized(t *testing.T) {
	h := newHarness(t)

	invite := siptest.NewRequest("INVITE", "sip:pcscf@"+h.local.String(), sip.UDP, h.peer.Addr())
	via := topVia(t, invite)
	via.Port = 0
	_ = invite.Header.SetTopVia(via)
	h.peer.Send(sip.UDP, h.local, invite)

	h.wantResponse(100)
	tx := h.tu.NextRequest().Tx

	cancel, _ := sip.NewCancel(invite)
	via.Port = 5060
	_ = cancel.Header.SetTopVia(via)
	h.peer.Send(sip.UDP, h.local, cancel)

	h.wantResponse(200)

	if c := h.tu.NextCancel(); c.Tx != tx {
		t.Fatal("CANCEL with an explicit default port did not match the INVITE")
	}
}

func TestCancelNonInviteAnswered(t *testing.T) {
	h := newHarness(t)
	tx, req := h.serve("MESSAGE", sip.UDP)

	cancel := req.Clone()
	cancel.Method = "CANCEL"
	cancel.Header.Set("CSeq", "1 CANCEL")
	h.peer.Send(sip.UDP, h.local, cancel)

	if res := h.wantResponse(200); res.Header.Get("CSeq") != "1 CANCEL" {
		t.Fatalf("got %q for %s", res.StartLine(), res.Header.Get("CSeq"))
	}

	h.respond(tx, 200)
	h.wantResponse(200)
	h.tu.None(quiet)
}

var errUnprotected = errors.New("unprotected")

func TestFilter(t *testing.T) {
	var refusedAt atomic.Pointer[netip.AddrPort]

	h := newHarnessConfig(t, transaction.Config{
		Filter: func(m sip.Message) error {
			req, ok := m.(*sip.Request)
			refused := refusedAt.Load()

			switch {
			case !ok && m.Env().Header.Get("X-Drop") != "":
				return errUnprotected
			case ok && refused != nil && m.Env().Flow.Local == *refused && req.Method == "INVITE":
				return &sip.StatusError{StatusCode: 403, Err: errUnprotected}
			case ok && refused != nil && m.Env().Flow.Local == *refused:
				return errUnprotected
			}

			return nil
		},
	})
	refused := siptest.ListenLayer(t, h.l, loopback)
	refusedAt.Store(&refused)

	t.Run("request rejected before the transaction layer acts", func(t *testing.T) {
		invite := siptest.NewRequest("INVITE", "sip:pcscf@"+refused.String(), sip.UDP, h.peer.Addr())
		h.peer.Send(sip.UDP, refused, invite)

		res, _ := h.peer.RecvResponse()
		wantCode(t, res, 403)

		ack, _ := sip.NewAck(invite, res)
		h.peer.Send(sip.UDP, refused, ack)

		cancel, _ := sip.NewCancel(invite)
		h.peer.Send(sip.UDP, refused, cancel)

		h.peerRequest("MESSAGE", sip.UDP)

		if r := h.tu.NextRequest(); r.Req.Method != "MESSAGE" {
			t.Fatalf("TU got %s", r.Req.Method)
		}

		h.peer.RecvNone(quiet)
		h.tu.None(0)
	})

	t.Run("response dropped before the transaction sees it", func(t *testing.T) {
		tx, rs, sent := h.request("INVITE", sip.UDP)

		dropped := sip.NewResponse(sent, 486, "")
		dropped.Header.Add("X-Drop", "1")
		h.peer.Send(sip.UDP, h.local, dropped)
		h.barrier()

		rs.None(0)
		wantState(t, tx, transaction.Calling)

		h.reply(sent, 180)
		wantCode(t, rs.Next(), 180)
	})
}
