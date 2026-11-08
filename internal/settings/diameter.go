package settings

import (
	"cmp"
	"net/netip"
	"slices"
	"strings"

	"github.com/ellanetworks/core/diameter"
)

type Application string

const (
	ApplicationCx  Application = "cx"
	ApplicationRx  Application = "rx"
	ApplicationSh  Application = "sh"
	ApplicationS6c Application = "s6c"
	ApplicationSGd Application = "sgd"
)

var Applications = []Application{ApplicationCx, ApplicationRx, ApplicationSh, ApplicationS6c, ApplicationSGd}

var RoutedApplications = []Application{ApplicationCx, ApplicationRx, ApplicationSh, ApplicationS6c}

var hssApplications = []Application{ApplicationSh, ApplicationS6c}

func (a Application) Routed() bool { return slices.Contains(RoutedApplications, a) }

type Transport string

const (
	TransportTCP  Transport = "tcp"
	TransportSCTP Transport = "sctp"
)

const (
	DefaultDiameterPort = 3868
	DefaultPriority     = 10
	maxPriority         = 65535
	// ITU-T E.164 section 6
	maxE164Digits = 15
)

// RFC 6733 section 2.7
// RFC 6733 section 5.1
type Peer struct {
	ID           string
	Host         string
	Address      string
	Port         int
	Transport    Transport
	Applications []Application
	Priority     int
}

// RFC 6733 section 6.1.6
type Route struct {
	Application Application
	Realm       string
}

func (p Peer) name() string {
	if p.Host != "" {
		return p.Host
	}

	return p.Address
}

func (p Peer) Serves(app Application) bool {
	return slices.Contains(p.Applications, app)
}

func (p Peer) Validate() error {
	switch {
	case p.Host != "" && !isFQDN(p.Host):
		return invalidf("host must be a domain name")
	case p.Address == "":
		return invalidf("address is required")
	case !CanonicalPeerAddress(p.Address):
		return invalidf("address must be a specific IPv4 or IPv6 address or a lowercase host name")
	case p.Port < 1 || p.Port > 65535:
		return invalidf("port must be between 1 and 65535")
	case p.Transport != TransportTCP && p.Transport != TransportSCTP:
		return invalidf("transport must be tcp or sctp")
	case p.Priority < 0 || p.Priority > maxPriority:
		return invalidf("priority must be between 0 and %d", maxPriority)
	case len(p.Applications) == 0:
		return invalidf("applications must list at least one of cx, rx, sh, s6c and sgd")
	}

	for i, a := range p.Applications {
		switch {
		case !slices.Contains(Applications, a):
			return invalidf("applications must list cx, rx, sh, s6c or sgd, not %q", a)
		case slices.Contains(p.Applications[:i], a):
			return invalidf("applications lists %s twice", a)
		}
	}

	return nil
}

func CanonicalPeerAddress(address string) bool {
	if a, err := netip.ParseAddr(address); err == nil {
		return a.Zone() == "" && !a.IsUnspecified() && a.Unmap().String() == address
	}

	return diameter.IsValidHostname(address) && strings.ToLower(address) == address
}

func (r Route) Validate() error {
	switch {
	case !r.Application.Routed():
		return invalidf("application must be cx, rx, sh or s6c, not %q", r.Application)
	case r.Realm != "" && !isFQDN(r.Realm):
		return invalidf("realm must be a domain name")
	}

	return nil
}

func (p Peer) equal(q Peer) bool {
	return p.ID == q.ID && strings.EqualFold(p.Host, q.Host) && p.Address == q.Address && p.Port == q.Port &&
		p.Transport == q.Transport && slices.Equal(p.Applications, q.Applications) && p.Priority == q.Priority
}

// RFC 6733 section 4.3.1
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

func (s Settings) Realm(app Application) string {
	for _, r := range s.Routes {
		if r.Application == app && r.Realm != "" {
			return r.Realm
		}
	}

	if slices.Contains(hssApplications, app) {
		return s.Realm(ApplicationCx)
	}

	return s.Operator.HomeDomain()
}

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
			case p.Host != "" && strings.EqualFold(q.Host, p.Host):
				return conflictf("A Diameter peer already has host %s", p.Host)
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
