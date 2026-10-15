package offeranswer_test

import (
	"testing"

	"github.com/ellanetworks/ims/sip/offeranswer"
)

type step struct {
	from   offeranswer.Side
	seq    uint32
	method string
	code   int
	sdp    string
	role   offeranswer.Role
}

func (s step) apply(t *testing.T, n *offeranswer.Negotiation) {
	t.Helper()

	var body []byte
	if s.sdp != "" {
		body = []byte(s.sdp)
	}

	key := offeranswer.Key{From: s.from, Seq: s.seq, Method: s.method}

	var role offeranswer.Role
	if s.code == 0 {
		role = n.Request(key, body)
	} else {
		role = n.Response(key, s.code, body)
	}

	if role != s.role {
		t.Fatalf("%s %d %d %q: %s, want %s", s.method, s.seq, s.code, s.sdp, role, s.role)
	}
}

const (
	a = offeranswer.A
	b = offeranswer.B
)

var (
	none   = offeranswer.None
	offer  = offeranswer.Offer
	answer = offeranswer.Answer
)

func TestNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		steps         []step
		offer, answer string
		pending       bool
	}{
		{
			name:  "offer in the INVITE, answer in the 2xx",
			steps: []step{{a, 1, "INVITE", 0, "X", offer}, {a, 1, "INVITE", 200, "x", answer}},
			offer: "X", answer: "x",
		},
		{
			name: "offer in the 2xx, answer in the ACK (RFC 3261 §13.2.1)",
			steps: []step{
				{a, 1, "INVITE", 0, "", none}, {a, 1, "INVITE", 200, "O", offer}, {a, 1, "ACK", 0, "a", answer},
			},
			offer: "O", answer: "a",
		},
		{
			name: "answer in a reliable 1xx, repeated in the 2xx (RFC 6337 §3.1)",
			steps: []step{
				{a, 1, "INVITE", 0, "X", offer}, {a, 1, "INVITE", 183, "x", answer}, {a, 1, "INVITE", 200, "x", none},
			},
			offer: "X", answer: "x",
		},
		{
			name: "answer changed in the 2xx",
			steps: []step{
				{a, 1, "INVITE", 0, "X", offer}, {a, 1, "INVITE", 183, "x", answer}, {a, 1, "INVITE", 200, "y", answer},
			},
			offer: "X", answer: "y",
		},
		{
			name: "offer in a reliable 1xx, answer in the PRACK, offer repeated in the 2xx (RFC 3262 §5)",
			steps: []step{
				{a, 1, "INVITE", 0, "", none},
				{a, 1, "INVITE", 183, "O", offer},
				{a, 2, "PRACK", 0, "a", answer},
				{a, 2, "PRACK", 200, "", none},
				{a, 1, "INVITE", 200, "O", none},
			},
			offer: "O", answer: "a",
		},
		{
			name: "new offer in a PRACK, answered in its 2xx",
			steps: []step{
				{a, 1, "INVITE", 0, "X", offer},
				{a, 1, "INVITE", 183, "x", answer},
				{a, 2, "PRACK", 0, "P", offer},
				{a, 2, "PRACK", 200, "p", answer},
			},
			offer: "P", answer: "p",
		},
		{
			name:  "PRACK cannot offer while an offer is pending",
			steps: []step{{a, 1, "INVITE", 0, "", none}, {a, 1, "INVITE", 183, "O", offer}, {b, 7, "UPDATE", 0, "", none}},
			offer: "O", pending: true,
		},
		{
			name:  "ACK cannot offer",
			steps: []step{{a, 1, "INVITE", 0, "X", offer}, {a, 1, "INVITE", 200, "x", answer}, {a, 1, "ACK", 0, "Z", none}},
			offer: "X", answer: "x",
		},
		{
			name: "failed UPDATE restores the session (RFC 3311 §5.2)",
			steps: []step{
				{a, 1, "INVITE", 0, "X", offer},
				{a, 1, "INVITE", 200, "x", answer},
				{b, 7, "UPDATE", 0, "U", offer},
				{b, 7, "UPDATE", 488, "", none},
			},
			offer: "X", answer: "x",
		},
		{
			name: "re-INVITE glare: both 491 (RFC 3261 §14.1)",
			steps: []step{
				{a, 1, "INVITE", 0, "X", offer},
				{a, 1, "INVITE", 200, "x", answer},
				{a, 2, "INVITE", 0, "A", offer},
				{b, 9, "INVITE", 0, "B", offer},
				{b, 9, "INVITE", 491, "", none},
				{a, 2, "INVITE", 491, "", none},
			},
			offer: "X", answer: "x",
		},
		{
			name: "re-INVITE without an offer: offer in the 2xx, answer in the ACK",
			steps: []step{
				{a, 1, "INVITE", 0, "X", offer},
				{a, 1, "INVITE", 200, "x", answer},
				{b, 4, "INVITE", 0, "", none},
				{b, 4, "INVITE", 200, "O", offer},
				{b, 4, "ACK", 0, "o", answer},
			},
			offer: "O", answer: "o",
		},
		{
			name:  "pending offer",
			steps: []step{{a, 1, "INVITE", 0, "X", offer}, {a, 1, "INVITE", 180, "", none}},
			offer: "X", pending: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n offeranswer.Negotiation

			for _, s := range tc.steps {
				s.apply(t, &n)
			}

			o, ans := n.Last()
			_, _, pending := n.Pending()

			if string(o.Data) != tc.offer || string(ans.Data) != tc.answer && !tc.pending || pending != tc.pending {
				t.Errorf("last %q, %q, pending %v; want %q, %q, %v", o.Data, ans.Data, pending, tc.offer, tc.answer, tc.pending)
			}
		})
	}
}

func TestSessionAndDescribed(t *testing.T) {
	var n offeranswer.Negotiation

	if _, _, ok := n.Session(); ok {
		t.Fatal("session before any exchange")
	}

	step{a, 1, "INVITE", 0, "X", offer}.apply(t, &n)
	step{a, 1, "INVITE", 200, "x", answer}.apply(t, &n)
	step{b, 3, "UPDATE", 0, "U", offer}.apply(t, &n)

	// The session stays the agreed one while an offer is pending.
	o, ans, ok := n.Session()
	if !ok || string(o.Data) != "X" || string(ans.Data) != "x" {
		t.Errorf("session %q, %q, %v", o.Data, ans.Data, ok)
	}

	if d, ok := n.Described(a); !ok || string(d) != "X" {
		t.Errorf("A described %q", d)
	}

	if d, ok := n.Described(b); !ok || string(d) != "U" {
		t.Errorf("B described %q", d)
	}

	if p, key, ok := n.Pending(); !ok || p.From != b || key.Method != "UPDATE" {
		t.Errorf("pending %+v %+v %v", p, key, ok)
	}

	n.Fail(offeranswer.Key{From: b, Seq: 3, Method: "UPDATE"})

	if d, _ := n.Described(b); string(d) != "x" {
		t.Errorf("B described %q after the failed UPDATE", d)
	}
}
