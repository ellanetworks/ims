package b2bua

import "github.com/ellanetworks/ims/sip/ua"

// RFC 3264
type Exchange struct {
	From, To *Leg

	Method  string
	Initial bool

	Offer []byte

	Value any

	ended bool
}

// RFC 7092 section 3.1
// RFC 7092 section 3.2
type Media interface {
	Offer(x *Exchange) ([]byte, error)

	Answer(x *Exchange, s *ua.Session, sdp []byte) ([]byte, error)

	End(x *Exchange, s *ua.Session)
}

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

func (p *Relay) answerOf(x *Exchange, s *ua.Session, sdp []byte) ([]byte, error) {
	if p.cfg.Media == nil {
		return sdp, nil
	}

	return p.cfg.Media.Answer(x, s, sdp)
}

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

func (p *Relay) endAll() {
	for len(p.open) > 0 {
		p.end(p.open[0], nil)
	}
}
