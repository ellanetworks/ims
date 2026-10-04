package proxy

import (
	"testing"

	"github.com/ellanetworks/ims/sip"
)

type sdpStep struct {
	from   Side
	seq    uint32
	method string
	code   int
	sdp    string
}

func (s sdpStep) apply(d *Dialog) {
	var e sip.Envelope
	if s.sdp != "" {
		e.SetBody("application/sdp", []byte(s.sdp))
	}

	if s.code == 0 {
		d.requestBody(&d.sdp, s.from, txKey{from: s.from, seq: s.seq, method: s.method}, e)
		return
	}

	res := &sip.Response{StatusCode: s.code, Envelope: e}
	d.responseBody(&d.sdp, s.from.other(), txKey{from: s.from, seq: s.seq, method: s.method}, res)
}

func TestNegotiation(t *testing.T) {
	negotiated := []sdpStep{
		{from: Caller, seq: 1, method: "INVITE", sdp: "X"},
		{from: Caller, seq: 1, method: "INVITE", code: 200, sdp: "x"},
	}

	for _, tc := range []struct {
		name          string
		steps         []sdpStep
		offer, answer string
		answered      bool
	}{
		{name: "offer in the INVITE", steps: negotiated, offer: "X", answer: "x", answered: true},
		{
			name: "offer in the 2xx, answer in the ACK",
			steps: []sdpStep{
				{from: Caller, seq: 1, method: "INVITE"},
				{from: Caller, seq: 1, method: "INVITE", code: 200, sdp: "O"},
				{from: Caller, seq: 1, method: "ACK", sdp: "a"},
			},
			offer: "O", answer: "a", answered: true,
		},
		{
			name: "answer in a reliable 183, repeated in the 2xx",
			steps: []sdpStep{
				{from: Caller, seq: 1, method: "INVITE", sdp: "X"},
				{from: Caller, seq: 1, method: "INVITE", code: 183, sdp: "x"},
				{from: Caller, seq: 1, method: "INVITE", code: 200, sdp: "x2"},
			},
			offer: "X", answer: "x2", answered: true,
		},
		{
			name: "offer in a reliable 183, answer in the PRACK, offer repeated in the 2xx",
			steps: []sdpStep{
				{from: Caller, seq: 1, method: "INVITE"},
				{from: Caller, seq: 1, method: "INVITE", code: 183, sdp: "O"},
				{from: Caller, seq: 2, method: "PRACK", sdp: "a"},
				{from: Caller, seq: 2, method: "PRACK", code: 200},
				{from: Caller, seq: 1, method: "INVITE", code: 200, sdp: "O"},
			},
			offer: "O", answer: "a", answered: true,
		},
		{
			name: "pending offer",
			steps: append(negotiated[:1:1],
				sdpStep{from: Caller, seq: 1, method: "INVITE", code: 100}),
			offer: "X", answered: false,
		},
		{
			name: "failed UPDATE",
			steps: append(negotiated[:2:2],
				sdpStep{from: Callee, seq: 7, method: "UPDATE", sdp: "U"},
				sdpStep{from: Callee, seq: 7, method: "UPDATE", code: 488}),
			offer: "X", answer: "x", answered: true,
		},
		{
			name: "re-INVITE glare, caller's 491 first",
			steps: append(negotiated[:2:2],
				sdpStep{from: Caller, seq: 2, method: "INVITE", sdp: "A"},
				sdpStep{from: Callee, seq: 9, method: "INVITE", sdp: "B"},
				sdpStep{from: Caller, seq: 2, method: "INVITE", code: 491},
				sdpStep{from: Callee, seq: 9, method: "INVITE", code: 491}),
			offer: "X", answer: "x", answered: true,
		},
		{
			name: "re-INVITE glare, callee's 491 first",
			steps: append(negotiated[:2:2],
				sdpStep{from: Caller, seq: 2, method: "INVITE", sdp: "A"},
				sdpStep{from: Callee, seq: 9, method: "INVITE", sdp: "B"},
				sdpStep{from: Callee, seq: 9, method: "INVITE", code: 491},
				sdpStep{from: Caller, seq: 2, method: "INVITE", code: 491}),
			offer: "X", answer: "x", answered: true,
		},
		{
			name: "hold by re-INVITE",
			steps: append(negotiated[:2:2],
				sdpStep{from: Callee, seq: 9, method: "INVITE", sdp: "H"},
				sdpStep{from: Callee, seq: 9, method: "INVITE", code: 200, sdp: "h"},
				sdpStep{from: Callee, seq: 9, method: "ACK"}),
			offer: "H", answer: "h", answered: true,
		},
		{
			name: "offer in a PRACK",
			steps: append(negotiated[:1:1],
				sdpStep{from: Caller, seq: 1, method: "INVITE", code: 183, sdp: "x"},
				sdpStep{from: Caller, seq: 2, method: "PRACK", sdp: "P"},
				sdpStep{from: Caller, seq: 2, method: "PRACK", code: 200, sdp: "p"}),
			offer: "P", answer: "p", answered: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dialog{}

			for _, s := range tc.steps {
				s.apply(d)
			}

			offer, answer, answered := d.Session()
			if string(offer.Data) != tc.offer || string(answer.Data) != tc.answer || answered != tc.answered {
				t.Errorf("session %q, %q, %v; want %q, %q, %v", offer.Data, answer.Data, answered, tc.offer, tc.answer, tc.answered)
			}
		})
	}
}

// RFC 3264, RFC 6337 §3.1: a repeated answer is the same exchange, a changed one is new.
func TestExchangeSequence(t *testing.T) {
	d := &Dialog{}

	steps := []sdpStep{
		{from: Caller, seq: 1, method: "INVITE", sdp: "X"},
		{from: Caller, seq: 1, method: "INVITE", code: 183, sdp: "x"},
	}

	for _, s := range steps {
		s.apply(d)
	}

	first := d.sdp.seq

	sdpStep{from: Caller, seq: 1, method: "INVITE", code: 200, sdp: "x"}.apply(d)

	if d.sdp.seq != first {
		t.Fatalf("seq %d after a repeated answer, want %d", d.sdp.seq, first)
	}

	sdpStep{from: Caller, seq: 1, method: "INVITE", code: 200, sdp: "y"}.apply(d)

	if d.sdp.seq == first || string(d.sdp.answer.Data) != "y" {
		t.Fatalf("seq %d answer %q after a changed answer, want a new exchange", d.sdp.seq, d.sdp.answer.Data)
	}

	sdpStep{from: Callee, seq: 2, method: "UPDATE", sdp: "U"}.apply(d)
	sdpStep{from: Callee, seq: 2, method: "UPDATE", code: 200, sdp: "u"}.apply(d)

	if d.sdp.seq <= first+1 {
		t.Fatalf("seq %d after an UPDATE exchange, want it to grow", d.sdp.seq)
	}
}

// RFC 3262 §5, RFC 3264: only a PRACK or ACK from the other side answers a pending offer.
func TestPendingOffer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		steps   []sdpStep
		from    Side
		method  string
		body    bool
		pending bool
	}{
		{"offer in a reliable 183, answer in the PRACK", []sdpStep{
			{from: Caller, seq: 1, method: "INVITE"},
			{from: Caller, seq: 1, method: "INVITE", code: 183, sdp: "O"},
		}, Caller, "PRACK", true, true},
		{"PRACK without a body", []sdpStep{
			{from: Caller, seq: 1, method: "INVITE"},
			{from: Caller, seq: 1, method: "INVITE", code: 183, sdp: "O"},
		}, Caller, "PRACK", false, false},
		{"offer from the same side", []sdpStep{
			{from: Caller, seq: 1, method: "INVITE", sdp: "O"},
		}, Caller, "PRACK", true, false},
		{"offer in the 2xx, answer in the ACK", []sdpStep{
			{from: Caller, seq: 1, method: "INVITE"},
			{from: Caller, seq: 1, method: "INVITE", code: 200, sdp: "O"},
		}, Caller, "ACK", true, true},
		{"UPDATE is not an answer", []sdpStep{
			{from: Caller, seq: 1, method: "INVITE"},
			{from: Caller, seq: 1, method: "INVITE", code: 183, sdp: "O"},
		}, Caller, "UPDATE", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dialog{answerTag: "callee"}
			d.caller.addr = mustAddress(t, "<sip:a@x>;tag=caller")

			for _, s := range tc.steps {
				s.apply(d)
			}

			req := &sip.Request{Method: tc.method}
			req.Header.Add("From", "<sip:a@x>;tag=caller")
			req.Header.Add("To", "<sip:b@x>;tag=callee")

			if tc.body {
				req.SetBody("application/sdp", []byte("A"))
			}

			if _, got := d.PendingOffer(req); got != tc.pending {
				t.Fatalf("PendingOffer = %v, want %v", got, tc.pending)
			}
		})
	}
}

func mustAddress(t *testing.T, s string) sip.Address {
	t.Helper()

	as, err := sip.ParseAddressList(s)
	if err != nil || len(as) != 1 {
		t.Fatalf("ParseAddressList(%q): %v", s, err)
	}

	return as[0]
}
