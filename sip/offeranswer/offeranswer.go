// Package offeranswer tracks the offer/answer exchanges of the session of a dialog between two parties (RFC 3264):
// which message carries an offer, which one its answer, and what the session is once they have (RFC 3261 §13.2.1,
// §14; RFC 3262 §5; RFC 3311 §5.1; RFC 6337).
package offeranswer

import "bytes"

// Side is one of the two parties of the dialog.
type Side uint8

const (
	A Side = iota
	B
)

func (s Side) Other() Side {
	return 1 - s
}

// Body is a session description and the party that sent it.
type Body struct {
	From Side
	Data []byte
}

// Key identifies the transaction a message belongs to: the party that sent the request, its CSeq number and method.
type Key struct {
	From   Side
	Seq    uint32
	Method string
}

type Role int

const (
	None Role = iota
	Offer
	Answer
)

func (r Role) String() string {
	switch r {
	case Offer:
		return "offer"
	case Answer:
		return "answer"
	}

	return "none"
}

// Negotiation is the state of the session: the last offer and answer, and whether an offer awaits its answer. Its zero
// value is a session without an exchange yet. Copying it copies the state, as an early dialog of a forked INVITE
// starts from the state of the INVITE.
type Negotiation struct {
	offer, answer Body
	tx            Key
	pending       bool
	prev          *Negotiation
}

// Request records a request carrying a session description, or none, and reports its role. An ACK or a PRACK with a
// session description answers a pending offer of the other party; an ACK cannot offer (RFC 3261 §13.2.1), and a
// PRACK offers only once nothing is pending (RFC 3262 §5).
func (n *Negotiation) Request(key Key, sdp []byte) Role {
	if sdp == nil {
		return None
	}

	if n.pending && n.offer.From != key.From && (key.Method == "PRACK" || key.Method == "ACK") {
		return n.answered(Body{From: key.From, Data: sdp})
	}

	if key.Method == "ACK" || key.Method == "PRACK" && n.pending {
		return None
	}

	n.offered(key, Body{From: key.From, Data: sdp})

	return Offer
}

// Response records a response, sent by the party other than key.From, carrying a session description or none, and
// reports its role. A failure response ends the offer of its transaction, the session going back to its state before
// the offer (RFC 3261 §14.1, RFC 3311 §5.2).
func (n *Negotiation) Response(key Key, code int, sdp []byte) Role {
	from := key.From.Other()
	role := None

	switch {
	case sdp == nil || code >= 300:
	case n.tx == key && n.offer.From != from:
		// The answer, or the answer of a reliable 1xx repeated in the 2xx.
		role = n.answered(Body{From: from, Data: sdp})
	case n.tx == key:
		// The offer of a reliable 1xx repeated: in another reliable 1xx before the answer, in the 2xx after.
		if n.pending {
			n.offer.Data = sdp
		}
	case !n.pending && key.Method == "INVITE":
		n.offered(key, Body{From: from, Data: sdp})

		role = Offer
	}

	if code >= 300 {
		n.Fail(key)
	}

	return role
}

// Fail ends the offer of a transaction that failed, or ended without a response: the session goes back to its state
// before the offer (RFC 3261 §14.1, RFC 3311 §5.2).
func (n *Negotiation) Fail(key Key) {
	if !n.pending || n.tx != key {
		return
	}

	if n.prev != nil {
		*n = *n.prev
	} else {
		*n = Negotiation{}
	}
}

func (n *Negotiation) offered(key Key, body Body) {
	prev := n.prev
	if !n.pending {
		done := *n
		done.prev = nil
		prev = &done
	}

	*n = Negotiation{offer: body, tx: key, pending: true, prev: prev}
}

// RFC 6337 §3.1: an answer repeated unchanged, as the one of a reliable 1xx in the 2xx, is no new exchange.
func (n *Negotiation) answered(body Body) Role {
	fresh := n.pending || !bytes.Equal(n.answer.Data, body.Data)

	n.answer = body
	n.pending = false

	if !fresh {
		return None
	}

	return Answer
}

// Pending is the offer awaiting an answer, and the transaction it came in.
func (n *Negotiation) Pending() (Body, Key, bool) {
	if !n.pending {
		return Body{}, Key{}, false
	}

	return n.offer, n.tx, true
}

// Session is the last completed exchange: the session the parties agreed on. ok is false before the first one.
func (n *Negotiation) Session() (offer, answer Body, ok bool) {
	switch {
	case !n.pending && n.answer.Data != nil:
		return n.offer, n.answer, true
	case n.pending && n.prev != nil && n.prev.answer.Data != nil:
		return n.prev.offer, n.prev.answer, true
	}

	return Body{}, Body{}, false
}

// Last is the last offer and answer, the answer missing while the offer is pending.
func (n *Negotiation) Last() (offer, answer Body) {
	return n.offer, n.answer
}

// Described is the last session description a party sent, in an offer or in an answer.
func (n *Negotiation) Described(s Side) ([]byte, bool) {
	for c := n; c != nil; c = c.prev {
		switch {
		case c.answer.Data != nil && c.answer.From == s:
			return c.answer.Data, true
		case c.offer.Data != nil && c.offer.From == s:
			return c.offer.Data, true
		}
	}

	return nil, false
}
