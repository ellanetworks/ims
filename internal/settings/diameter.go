package settings

import (
	"cmp"
	"net/netip"
	"slices"
	"strings"
)

type Application string

const (
	ApplicationCx Application = "cx"
	ApplicationRx Application = "rx"
)

// Applications are the Diameter applications of the IMS, in the order the API lists their routes.
var Applications = []Application{ApplicationCx, ApplicationRx}

type Transport string

const (
	TransportTCP  Transport = "tcp"
	TransportSCTP Transport = "sctp"
)

const (
	DefaultDiameterPort = 3868
	DefaultPriority     = 10
	maxPriority         = 65535
)

// Peer is a Diameter peer of the IMS: an HSS or an SLF serves cx, a PCRF or a DRA serves rx. Requests go to the
// peers serving their application by priority, lowest first, and are shared between peers of the same priority
// (RFC 6733 §2.7, §5.1). The peer's realm is the one it gives in the capabilities exchange.
type Peer struct {
	ID string
	// Host is the Origin-Host the peer must give in the capabilities exchange. Empty takes the one it gives
	// (RFC 6733 §2.6, §12).
	Host         string
	Address      netip.Addr
	Port         int
	Transport    Transport
	Applications []Application
	Priority     int
}

// Route is where the requests of an application go: the realm of the HSS for cx, of the PCRF for rx (RFC 6733
// §6.1.6). Its peers are those serving the application.
type Route struct {
	Application Application
	// Realm is the Destination-Realm of the requests. Empty is the home domain.
	Realm string
}

func (p Peer) Serves(app Application) bool {
	return slices.Contains(p.Applications, app)
}

func (p Peer) Validate() error {
	switch {
	case p.Host != "" && !isFQDN(p.Host):
		return invalidf("host must be a domain name")
	case !p.Address.IsValid():
		return invalidf("address is required")
	case p.Address.IsUnspecified() || p.Address.Zone() != "":
		return invalidf("address must be a specific IPv4 or IPv6 address")
	case p.Port < 1 || p.Port > 65535:
		return invalidf("port must be between 1 and 65535")
	case p.Transport != TransportTCP && p.Transport != TransportSCTP:
		return invalidf("transport must be tcp or sctp")
	case p.Priority < 0 || p.Priority > maxPriority:
		return invalidf("priority must be between 0 and %d", maxPriority)
	case len(p.Applications) == 0:
		return invalidf("applications must list cx, rx or both")
	}

	for i, a := range p.Applications {
		switch {
		case !slices.Contains(Applications, a):
			return invalidf("applications must list cx, rx or both, not %q", a)
		case slices.Contains(p.Applications[:i], a):
			return invalidf("applications lists %s twice", a)
		}
	}

	return nil
}

func (r Route) Validate() error {
	switch {
	case !slices.Contains(Applications, r.Application):
		return invalidf("application must be cx or rx, not %q", r.Application)
	case r.Realm != "" && !isFQDN(r.Realm):
		return invalidf("realm must be a domain name")
	}

	return nil
}

func (p Peer) equal(q Peer) bool {
	return p.ID == q.ID && strings.EqualFold(p.Host, q.Host) && p.Address == q.Address && p.Port == q.Port &&
		p.Transport == q.Transport && slices.Equal(p.Applications, q.Applications) && p.Priority == q.Priority
}

// isFQDN reports whether s is a DiameterIdentity or a realm: a domain name (RFC 6733 §4.3.1).
func isFQDN(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}

	for label := range strings.SplitSeq(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}

		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}

	return true
}

// Realm is the Destination-Realm of an application's requests.
func (s Settings) Realm(app Application) string {
	for _, r := range s.Routes {
		if r.Application == app && r.Realm != "" {
			return r.Realm
		}
	}

	return s.Operator.HomeDomain()
}

// Serving returns the peers serving an application, in the order requests try them: by priority, then in the order
// they were created in.
func (s Settings) Serving(app Application) []Peer {
	var out []Peer

	for _, p := range s.Peers {
		if p.Serves(app) {
			out = append(out, p)
		}
	}

	slices.SortStableFunc(out, func(a, b Peer) int { return cmp.Compare(a.Priority, b.Priority) })

	return out
}

// Transports are the transports the peers use, which the IMS listens on.
func (s Settings) Transports() []Transport {
	var out []Transport

	for _, p := range s.Peers {
		if !slices.Contains(out, p.Transport) {
			out = append(out, p.Transport)
		}
	}

	slices.Sort(out)

	return out
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
			switch {
			// Diameter identities are case-insensitive.
			case p.Host != "" && strings.EqualFold(q.Host, p.Host):
				return conflictf("A Diameter peer already has host %s", p.Host)
			// Without a host, a peer is known by its address and transport.
			case p.Host == "" && q.Host == "" && p.Address == q.Address && p.Transport == q.Transport:
				return conflictf("A Diameter peer without a host already has address %s over %s", p.Address, p.Transport)
			}
		}
	}

	for _, r := range s.Routes {
		if err := r.Validate(); err != nil {
			return err
		}
	}

	return nil
}
