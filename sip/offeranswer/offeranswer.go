package offeranswer

import "bytes"

type Side uint8

const (
	A Side = iota
	B
)

func (s Side) Other() Side {
	return 1 - s
}

type Body struct {
	From Side
	Data []byte
}

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

type Negotiation struct {
	KeepFirstAnswer bool

	offer, answer Body
	tx            Key
	pending       bool
	prev          *Negotiation
}

// RFC 3261 section 13.2.1
// RFC 3262 section 5
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

// RFC 3261 section 14.1
// RFC 3311 section 5.2
func (n *Negotiation) Response(key Key, code int, sdp []byte) Role {
	from := key.From.Other()
	role := None

	switch {
	case sdp == nil || code >= 300:
	case n.tx == key && n.offer.From != from && !n.pending && n.KeepFirstAnswer:
	case n.tx == key && n.offer.From != from:
		role = n.answered(Body{From: from, Data: sdp})
	case n.tx == key:
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

// RFC 3261 section 14.1
// RFC 3311 section 5.2
func (n *Negotiation) Fail(key Key) {
	if !n.pending || n.tx != key {
		return
	}

	keep := n.KeepFirstAnswer

	if n.prev != nil {
		*n = *n.prev
	} else {
		*n = Negotiation{}
	}

	n.KeepFirstAnswer = keep
}

func (n *Negotiation) offered(key Key, body Body) {
	prev := n.prev
	if !n.pending {
		done := *n
		done.prev = nil
		prev = &done
	}

	*n = Negotiation{KeepFirstAnswer: n.KeepFirstAnswer, offer: body, tx: key, pending: true, prev: prev}
}

// RFC 6337 section 3.1
func (n *Negotiation) answered(body Body) Role {
	fresh := n.pending || !bytes.Equal(n.answer.Data, body.Data)

	n.answer = body
	n.pending = false

	if !fresh {
		return None
	}

	return Answer
}

func (n *Negotiation) Pending() (Body, Key, bool) {
	if !n.pending {
		return Body{}, Key{}, false
	}

	return n.offer, n.tx, true
}

func (n *Negotiation) Session() (offer, answer Body, ok bool) {
	switch {
	case !n.pending && n.answer.Data != nil:
		return n.offer, n.answer, true
	case n.pending && n.prev != nil && n.prev.answer.Data != nil:
		return n.prev.offer, n.prev.answer, true
	}

	return Body{}, Body{}, false
}

func (n *Negotiation) Last() (offer, answer Body) {
	return n.offer, n.answer
}

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
