package settings

import (
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type SIPTransport string

const (
	SIPTransportUDP SIPTransport = "udp"
	SIPTransportTCP SIPTransport = "tcp"
	SIPTransportTLS SIPTransport = "tls"
)

const (
	DefaultKeepaliveInterval = 30 * time.Second
	MinKeepaliveInterval     = 5 * time.Second
	MaxKeepaliveInterval     = time.Hour
	maxTrunkName             = 64
)

// SIPconnect 2.0 section 17.1.1.2
// RFC 3263 section 4.1
// RFC 3263 section 4.2
type TrunkAddress struct {
	Host      string
	Port      int
	Transport SIPTransport
}

// TS 24.229 clause 4.4.1
// SIPconnect 2.0 section 10.2.1
// SIPconnect 2.0 section 17.4
type Trunk struct {
	ID                string
	Name              string
	Addresses         []TrunkAddress
	InboundCIDRs      []netip.Prefix
	Domain            string
	Trusted           bool
	KeepaliveInterval time.Duration
	Enabled           bool
	TLS               TrunkTLS
}

type SIPRoute struct {
	ID     string
	Prefix string
	Trunks []string
}

func (a TrunkAddress) String() string {
	s := a.Host
	if a.Port != 0 {
		s = net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
	}

	if a.Transport != "" {
		s += " over " + string(a.Transport)
	}

	return s
}

func (a TrunkAddress) Validate() error {
	switch {
	case !isSIPHost(a.Host):
		return invalidf("addresses must have a domain name or a specific IPv4 or IPv6 address as host, not %q", a.Host)
	case a.Port < 0 || a.Port > 65535:
		return invalidf("addresses must have a port between 1 and 65535, or none")
	case a.Transport != "" && a.Transport != SIPTransportUDP && a.Transport != SIPTransportTCP && a.Transport != SIPTransportTLS:
		return invalidf("addresses must have udp, tcp or tls as transport, or none, not %q", a.Transport)
	}

	return nil
}

func isSIPHost(host string) bool {
	if a, err := netip.ParseAddr(host); err == nil {
		return a.String() == host && !a.Is4In6() && !a.IsUnspecified() && a.Zone() == ""
	}

	return isSIPHostname(host)
}

// RFC 3261 section 25.1
func isSIPHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}

	labels := strings.Split(host, ".")

	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}

		for _, c := range label {
			if !isAlphanum(c) && c != '-' {
				return false
			}
		}
	}

	top := labels[len(labels)-1]

	return (top[0] >= 'a' && top[0] <= 'z') || (top[0] >= 'A' && top[0] <= 'Z')
}

func isAlphanum(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func (t Trunk) Validate() error {
	t = t.Effective()

	switch {
	case !validTrunkName(t.Name):
		return invalidf("name must be 1 to %d printable characters, without leading or trailing spaces", maxTrunkName)
	case len(t.Addresses) == 0:
		return invalidf("addresses must list at least one address")
	case t.Domain == "":
		return invalidf("domain is required when no address has a domain name as host")
	case !isSIPHost(t.Domain):
		return invalidf("domain must be a domain name or a specific IPv4 or IPv6 address")
	case t.KeepaliveInterval != 0 && (t.KeepaliveInterval < MinKeepaliveInterval ||
		t.KeepaliveInterval > MaxKeepaliveInterval || t.KeepaliveInterval%time.Second != 0):
		return errKeepalive()
	}

	for i, a := range t.Addresses {
		if err := a.Validate(); err != nil {
			return err
		}

		if slices.ContainsFunc(t.Addresses[:i], func(b TrunkAddress) bool { return b.same(a) }) {
			return invalidf("addresses lists %s twice", a)
		}
	}

	if err := t.validateTLS(); err != nil {
		return err
	}

	if i := slices.IndexFunc(t.Addresses, func(a TrunkAddress) bool { return a.Transport == SIPTransportUDP }); t.Trusted && i >= 0 {
		return invalidf("addresses must not use udp when trusted is set, not %s", t.Addresses[i])
	}

	for i, p := range t.InboundCIDRs {
		if err := validateInboundCIDR(p); err != nil {
			return err
		}

		for _, q := range t.InboundCIDRs[:i] {
			if q.Overlaps(p) {
				return invalidf("inbound_cidrs %s and %s overlap", q, p)
			}
		}
	}

	return nil
}

// SIPconnect 2.0 section 17.2
func (t Trunk) validateTLS() error {
	if err := t.TLS.Validate(); err != nil {
		return err
	}

	for _, a := range t.Addresses {
		switch {
		case t.TLS.Required && a.Transport != SIPTransportTLS:
			return invalidf("addresses must all have tls as transport when tls.required is set, not %s", a)
		case a.Transport == SIPTransportTLS && !t.TLS.Trusts():
			return invalidf("addresses over tls need tls.ca_certificates or tls.pins")
		}
	}

	return nil
}

func (t Trunk) Effective() Trunk {
	var names []string

	for _, a := range t.Addresses {
		if isSIPHostname(a.Host) && !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, a.Host) }) {
			names = append(names, a.Host)
		}
	}

	if t.Domain == "" && len(names) > 0 {
		t.Domain = names[0]
	}

	if len(t.InboundCIDRs) == 0 {
		t.InboundCIDRs = nil

		for _, a := range t.Addresses {
			ip, err := netip.ParseAddr(a.Host)
			if err != nil {
				continue
			}

			if p := netip.PrefixFrom(ip, ip.BitLen()); !slices.Contains(t.InboundCIDRs, p) {
				t.InboundCIDRs = append(t.InboundCIDRs, p)
			}
		}
	}

	if len(t.TLS.Identities) == 0 && t.TLS.Trusts() {
		t.TLS.Identities = nil

		if isSIPHostname(t.Domain) {
			t.TLS.Identities = append(t.TLS.Identities, t.Domain)
		}

		for _, n := range names {
			if !slices.ContainsFunc(t.TLS.Identities, func(id string) bool { return strings.EqualFold(id, n) }) {
				t.TLS.Identities = append(t.TLS.Identities, n)
			}
		}
	}

	return t
}

func errKeepalive() error {
	return invalidf("keepalive_interval must be 0, for no keepalive, or between %d and %d seconds",
		int(MinKeepaliveInterval.Seconds()), int(MaxKeepaliveInterval.Seconds()))
}

func KeepaliveSeconds(seconds int) (time.Duration, error) {
	if seconds < 0 || seconds > int(MaxKeepaliveInterval/time.Second) {
		return 0, errKeepalive()
	}

	return time.Duration(seconds) * time.Second, nil
}

func validTrunkName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > maxTrunkName || strings.TrimSpace(name) != name {
		return false
	}

	for _, r := range name {
		if !unicode.IsPrint(r) {
			return false
		}
	}

	return true
}

func validateInboundCIDR(p netip.Prefix) error {
	switch {
	case !p.IsValid() || p.Addr().Is4In6():
		return invalidf("inbound_cidrs must be IPv4 or IPv6 networks, not %s", p)
	case p.Masked() != p:
		return invalidf("inbound_cidrs must be network addresses: %s, not %s", p.Masked(), p)
	case p.Bits() == 0:
		return invalidf("inbound_cidrs must not cover every address, as %s does", p)
	}

	return nil
}

func (a TrunkAddress) same(b TrunkAddress) bool {
	return strings.EqualFold(a.Host, b.Host) && a.Port == b.Port && a.Transport == b.Transport
}

const (
	defaultSIPPort  = 5060
	defaultSIPSPort = 5061
)

// RFC 3263 section 4.1
// RFC 3263 section 4.2
func (a TrunkAddress) overlaps(b TrunkAddress) bool {
	if !strings.EqualFold(a.Host, b.Host) || a.Transport != "" && b.Transport != "" && a.Transport != b.Transport {
		return false
	}

	pa, pb := a.ports(b.Transport), b.ports(a.Transport)

	return pa == nil || pb == nil || slices.ContainsFunc(pa, func(p int) bool { return slices.Contains(pb, p) })
}

func (a TrunkAddress) ports(other SIPTransport) []int {
	switch {
	case a.Port != 0:
		return []int{a.Port}
	case !isIP(a.Host):
		return nil
	}

	tr := a.Transport
	if tr == "" {
		tr = other
	}

	switch tr {
	case SIPTransportTLS:
		return []int{defaultSIPSPort}
	case "":
		return []int{defaultSIPPort, defaultSIPSPort}
	}

	return []int{defaultSIPPort}
}

func isIP(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil
}

func (r SIPRoute) Validate() error {
	switch {
	case !validRoutePrefix(r.Prefix):
		return invalidf("prefix must be + followed by 1 to %d digits, not starting with 0, or empty for the default route",
			maxE164Digits)
	case len(r.Trunks) == 0:
		return invalidf("trunks must list at least one SIP trunk")
	}

	for i, id := range r.Trunks {
		if slices.Contains(r.Trunks[:i], id) {
			return invalidf("trunks lists %s twice", id)
		}
	}

	return nil
}

func validRoutePrefix(prefix string) bool {
	if prefix == "" {
		return true
	}

	digits, ok := strings.CutPrefix(prefix, "+")

	return ok && isDigits(digits) && len(digits) <= maxE164Digits && digits[0] != '0'
}

func (r SIPRoute) describe() string {
	if r.Prefix == "" {
		return "the default SIP route"
	}

	return "the SIP route of prefix " + r.Prefix
}

func (r SIPRoute) Uses(trunkID string) bool {
	return slices.Contains(r.Trunks, trunkID)
}

func (s Settings) Trunk(id string) (Trunk, bool) {
	i, ok := s.trunk(id)
	if !ok {
		return Trunk{}, false
	}

	return s.Trunks[i], true
}

func (s Settings) SIPRoute(id string) (SIPRoute, bool) {
	i, ok := s.sipRoute(id)
	if !ok {
		return SIPRoute{}, false
	}

	return s.SIPRoutes[i], true
}

func (s Settings) trunk(id string) (int, bool) {
	i := slices.IndexFunc(s.Trunks, func(t Trunk) bool { return t.ID == id })
	return i, i >= 0
}

func (s Settings) sipRoute(id string) (int, bool) {
	i := slices.IndexFunc(s.SIPRoutes, func(r SIPRoute) bool { return r.ID == id })
	return i, i >= 0
}

func (s Settings) trunkInUse(id string) error {
	for _, r := range s.SIPRoutes {
		if r.Uses(id) {
			t, _ := s.Trunk(id)
			return conflictf("SIP trunk %s is used by %s", t.Name, r.describe())
		}
	}

	return nil
}

func (s Settings) validateSIPTrunks() error {
	for i, t := range s.Trunks {
		if err := t.Validate(); err != nil {
			return err
		}

		t = t.Effective()

		for _, u := range s.Trunks[:i] {
			if strings.EqualFold(u.Name, t.Name) {
				return conflictf("A SIP trunk already has name %s", t.Name)
			}

			for _, a := range t.Addresses {
				if slices.ContainsFunc(u.Addresses, a.overlaps) {
					return conflictf("Address %s reaches the same server as an address of SIP trunk %s", a, u.Name)
				}
			}

			for _, p := range t.InboundCIDRs {
				for _, q := range u.Effective().InboundCIDRs {
					if p.Overlaps(q) {
						return conflictf("Inbound CIDR %s overlaps %s of SIP trunk %s", p, q, u.Name)
					}
				}
			}
		}
	}

	for i, r := range s.SIPRoutes {
		if err := r.Validate(); err != nil {
			return err
		}

		if slices.ContainsFunc(s.SIPRoutes[:i], func(q SIPRoute) bool { return q.Prefix == r.Prefix }) {
			if r.Prefix == "" {
				return conflictf("A default SIP route already exists")
			}

			return conflictf("A SIP route already has prefix %s", r.Prefix)
		}

		for _, id := range r.Trunks {
			if _, ok := s.trunk(id); !ok {
				return invalidf("trunks lists %s, which is not a SIP trunk", id)
			}
		}
	}

	return nil
}
