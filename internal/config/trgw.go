package config

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"
)

const (
	defaultTrGWMinPort      = 10000
	defaultTrGWMaxPort      = 19999
	defaultTrGWMediaTimeout = 60 * time.Second
	minTrGWPort             = 1024
)

type TrGW struct {
	PhoneAddresses []TrGWAddress  `yaml:"phone_addresses"`
	TrunkAddresses []TrGWAddress  `yaml:"trunk_addresses"`
	MinPort        int            `yaml:"min_port"`
	MaxPort        int            `yaml:"max_port"`
	MediaTimeout   *time.Duration `yaml:"media_timeout"`
}

type TrGWAddress struct {
	Address   netip.Addr `yaml:"address"`
	Advertise netip.Addr `yaml:"advertise"`
}

func (c *Config) applyTrGWDefaults() {
	if c.IBCF == nil {
		return
	}

	if c.TrGW == nil {
		c.TrGW = &TrGW{}
	}

	g := c.TrGW

	if len(g.PhoneAddresses) == 0 {
		for _, a := range c.SIP.Addresses {
			g.PhoneAddresses = append(g.PhoneAddresses, TrGWAddress{Address: a})
		}
	}

	if len(g.TrunkAddresses) == 0 {
		adv := c.IBCF.External.Advertised()

		for _, a := range c.IBCF.External.Addresses {
			g.TrunkAddresses = append(g.TrunkAddresses, TrGWAddress{Address: a, Advertise: adv[a]})
		}
	}

	for _, l := range [][]TrGWAddress{g.PhoneAddresses, g.TrunkAddresses} {
		for i := range l {
			l[i].Address, l[i].Advertise = l[i].Address.Unmap(), l[i].Advertise.Unmap()
		}
	}

	if g.MinPort == 0 {
		g.MinPort = defaultTrGWMinPort
	}

	if g.MaxPort == 0 {
		g.MaxPort = defaultTrGWMaxPort
	}

	if g.MediaTimeout == nil {
		d := defaultTrGWMediaTimeout
		g.MediaTimeout = &d
	}
}

// RFC 3550 section 11
func (c Config) validateTrGW() error {
	g := c.TrGW

	switch {
	case g == nil:
		return nil
	case c.IBCF == nil:
		return errors.New("trgw needs ibcf: the TrGW relays the media of SIP trunks")
	case g.MinPort < minTrGWPort || g.MinPort > 65535 || g.MaxPort > 65535:
		return fmt.Errorf("trgw.min_port %d and trgw.max_port %d must be between %d and 65535", g.MinPort, g.MaxPort, minTrGWPort)
	case g.MinPort%2 != 0:
		return fmt.Errorf("trgw.min_port %d is odd: RTP takes even ports (RFC 3550 §11)", g.MinPort)
	case g.MaxPort <= g.MinPort:
		return fmt.Errorf("trgw.max_port %d leaves no pair of ports above trgw.min_port %d", g.MaxPort, g.MinPort)
	case *g.MediaTimeout < 0:
		return fmt.Errorf("trgw.media_timeout %s must not be negative", *g.MediaTimeout)
	}

	seen := map[netip.Addr]string{}

	for _, side := range []struct {
		name  string
		addrs []TrGWAddress
	}{{"trgw.phone_addresses", g.PhoneAddresses}, {"trgw.trunk_addresses", g.TrunkAddresses}} {
		for _, a := range side.addrs {
			switch {
			case !a.Address.IsValid(), a.Address.IsUnspecified():
				return fmt.Errorf("%s: %s must be a specific address", side.name, a.Address)
			case a.Address.Zone() != "":
				return fmt.Errorf("%s: %s must not have a zone", side.name, a.Address)
			case a.Advertise.IsValid() && (a.Advertise.IsUnspecified() || a.Advertise.Is4() != a.Address.Is4()):
				return fmt.Errorf("%s: advertise %s must be a specific address of the family of %s", side.name, a.Advertise, a.Address)
			case seen[a.Address] != "":
				return fmt.Errorf("%s: %s is also in %s", side.name, a.Address, seen[a.Address])
			}

			seen[a.Address] = side.name
		}
	}

	return c.trgwPortsFree()
}

func (c Config) trgwPortsFree() error {
	g := c.TrGW

	type taken struct {
		name string
		addr []netip.Addr
		port int
	}

	ports := []taken{
		{"api.port", []netip.Addr{c.API.Address.Unmap()}, c.API.Port},
		{"diameter.port", []netip.Addr{c.Diameter.Address.Unmap()}, c.Diameter.Port},
	}

	for _, p := range c.IBCF.External.ports() {
		ports = append(ports, taken{p.name, c.IBCF.External.Addresses, p.port})
	}

	for _, p := range c.sipPorts() {
		ports = append(ports, taken{p.name, c.SIP.Addresses, p.port})
	}

	if c.MCPTT != nil {
		ports = append(ports, taken{"mcptt.media.control_port", []netip.Addr{c.MCPTT.Media.Address}, c.MCPTT.Media.ControlPort})
	}

	for _, a := range slices.Concat(g.PhoneAddresses, g.TrunkAddresses) {
		for _, p := range ports {
			if p.port >= g.MinPort && p.port <= g.MaxPort && slices.ContainsFunc(p.addr, func(b netip.Addr) bool { return overlaps(a.Address, b) }) {
				return fmt.Errorf("%s %d is in the range of trgw ports on %s", p.name, p.port, a.Address)
			}
		}

		if m := c.MCPTT; m != nil && overlaps(a.Address, m.Media.Address) && m.Media.MinPort <= g.MaxPort && g.MinPort <= m.Media.MaxPort {
			return fmt.Errorf("the ranges of trgw ports and mcptt.media ports overlap on %s", a.Address)
		}
	}

	return nil
}
