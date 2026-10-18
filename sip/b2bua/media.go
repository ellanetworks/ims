package b2bua

import "github.com/ellanetworks/ims/sip/ua"

// Exchange is one offer/answer exchange the relay carries from one leg to the other (RFC 3264): the offer of the
// peer of From, made into the offer sent on To, and the answers of the peer of To, made into the answers sent back
// to From. It ends once, having taken effect in one dialog of To, or in none.
type Exchange struct {
	// From is the leg whose peer offered, To the leg whose peer answers.
	From, To *Leg

	// Method is the request whose exchange it is (INVITE, UPDATE, PRACK); Initial marks the initial INVITE of the
	// call, whose offer may be answered in several early dialogs of To when its INVITE forks.
	Method  string
	Initial bool

	// Offer is the offer as the peer of From made it.
	Offer []byte

	// Value is the Media's, for the state of the exchange: the anchored media it allocated.
	Value any

	ended bool
}

// Media makes the session descriptions of a relay, one exchange at a time: the same ones for a signalling-only
// relay (RFC 7092 §3.1), or ones with the media the B2BUA anchors (§3.2). Its functions run on the strand of the
// call. An error rejects the request the exchange is in, or ends the call, with a 488.
type Media interface {
	// Offer is the offer to send on x.To, made from x.Offer.
	Offer(x *Exchange) ([]byte, error)

	// Answer is the answer to send to x.From, made from sdp, the answer of the peer of x.To in its dialog s: one
	// of several when the initial INVITE forked.
	Answer(x *Exchange, s *ua.Session, sdp []byte) ([]byte, error)

	// End reports the end of x: s is the dialog of x.To in which it took effect, the session of both legs changed;
	// nil when it did not, as when it was refused, cancelled, or answered in a dialog not chosen. What Offer and
	// Answer allocated for the other dialogs is released.
	End(x *Exchange, s *ua.Session)
}

// begin starts an exchange, and makes the offer to send.
func (p *Relay) begin(x *Exchange) ([]byte, error) {
	p.open = append(p.open, x)

	if p.cfg.Media == nil {
		return x.Offer, nil
	}

	sdp, err := p.cfg.Media.Offer(x)
	if err != nil {
		p.end(x, nil)
	}

	return sdp, err
}

// answerOf makes the answer to send back for x, from the answer of the peer of x.To in s.
func (p *Relay) answerOf(x *Exchange, s *ua.Session, sdp []byte) ([]byte, error) {
	if p.cfg.Media == nil {
		return sdp, nil
	}

	return p.cfg.Media.Answer(x, s, sdp)
}

// end ends x, once.
func (p *Relay) end(x *Exchange, s *ua.Session) {
	if x == nil || x.ended {
		return
	}

	x.ended = true

	for i, o := range p.open {
		if o == x {
			p.open = append(p.open[:i], p.open[i+1:]...)
			break
		}
	}

	if p.cfg.Media != nil {
		p.cfg.Media.End(x, s)
	}
}

// endAll ends the exchanges left, as the call ends.
func (p *Relay) endAll() {
	for len(p.open) > 0 {
		p.end(p.open[0], nil)
	}
}
