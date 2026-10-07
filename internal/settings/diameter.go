package settings

import (
	"net/netip"
	"slices"
)

type Application string

const (
	ApplicationCx Application = "cx"
	ApplicationRx Application = "rx"
)

type Transport string

const (
	TransportTCP  Transport = "tcp"
	TransportSCTP Transport = "sctp"
)

const DefaultDiameterPort = 3868

// Peer is a Diameter peer of the IMS: the HSS serves cx, and the PCRF serves rx.
type Peer struct {
	ID           string
	Host         string
	Realm        string
	Address      netip.Addr
	Port         int
	Transport    Transport
	Applications []Application
}

func (p Peer) Serves(app Application) bool {
	return slices.Contains(p.Applications, app)
}

func (p Peer) Validate() error {
	switch {
	case p.Host == "":
		return invalidf("host is required")
	case p.Realm == "":
		return invalidf("realm is required")
	case !p.Address.IsValid():
		return invalidf("address is required")
	case p.Address.IsUnspecified() || p.Address.Zone() != "":
		return invalidf("address must be a specific IPv4 or IPv6 address")
	case p.Port < 1 || p.Port > 65535:
		return invalidf("port must be between 1 and 65535")
	case p.Transport != TransportTCP && p.Transport != TransportSCTP:
		return invalidf("transport must be tcp or sctp")
	case len(p.Applications) == 0:
		return invalidf("applications must list cx, rx or both")
	}

	for i, a := range p.Applications {
		switch {
		case a != ApplicationCx && a != ApplicationRx:
			return invalidf("applications must list cx, rx or both, not %q", a)
		case slices.Contains(p.Applications[:i], a):
			return invalidf("applications lists %s twice", a)
		}
	}

	return nil
}

func (p Peer) equal(q Peer) bool {
	return p.ID == q.ID && p.Host == q.Host && p.Realm == q.Realm && p.Address == q.Address && p.Port == q.Port &&
		p.Transport == q.Transport && slices.Equal(p.Applications, q.Applications)
}

// PeerServing returns the peer that serves an application, if there is one.
func (s Settings) PeerServing(app Application) (Peer, bool) {
	for _, p := range s.Peers {
		if p.Serves(app) {
			return p, true
		}
	}

	return Peer{}, false
}

func (s Settings) peer(id string) (int, bool) {
	i := slices.IndexFunc(s.Peers, func(p Peer) bool { return p.ID == id })
	return i, i >= 0
}

func (s Settings) validatePeers() error {
	for i, p := range s.Peers {
		if err := p.Validate(); err != nil {
			return err
		}

		for _, q := range s.Peers[:i] {
			if q.Host == p.Host {
				return conflictf("A Diameter peer already has host %s", p.Host)
			}

			for _, a := range p.Applications {
				if q.Serves(a) {
					return conflictf("A Diameter peer already serves %s", a)
				}
			}
		}
	}

	return nil
}
