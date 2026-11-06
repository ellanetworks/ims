package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
)

const (
	defaultIBCFPort         = 5085
	defaultIBCFMediaMinPort = 10000
	defaultIBCFMediaMaxPort = 19999
	minIBCFMediaPort        = 1024
)

// RFC 3261 section 18.2.1
const (
	IBCFExternalPort = 5060
	IBCFTLSPort      = 5061
)

type IBCF struct {
	Port     int          `yaml:"port"`
	External IBCFExternal `yaml:"external"`
	Media    IBCFMedia    `yaml:"media"`
}

type IBCFExternal struct {
	Addresses []IBCFAddress `yaml:"addresses"`
	TLS       *IBCFTLS      `yaml:"tls"`
}

type IBCFAddress struct {
	Address   netip.Addr `yaml:"address"`
	Advertise netip.Addr `yaml:"advertise"`
}

type IBCFTLS struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type IBCFMedia struct {
	MinPort int `yaml:"min_port"`
	MaxPort int `yaml:"max_port"`
}

func (e IBCFExternal) Addrs() []netip.Addr {
	out := make([]netip.Addr, 0, len(e.Addresses))
	for _, a := range e.Addresses {
		out = append(out, a.Address)
	}

	return out
}

func (e IBCFExternal) Advertised() map[netip.Addr]netip.Addr {
	out := map[netip.Addr]netip.Addr{}

	for _, a := range e.Addresses {
		if a.Advertise.IsValid() {
			out[a.Address] = a.Advertise
		}
	}

	return out
}

func (e IBCFExternal) Certificate() bool {
	return e.TLS != nil && e.TLS.Cert != ""
}

func (b *IBCF) applyDefaults() {
	if b.Port == 0 {
		b.Port = defaultIBCFPort
	}

	if b.Media.MinPort == 0 {
		b.Media.MinPort = defaultIBCFMediaMinPort
	}

	if b.Media.MaxPort == 0 {
		b.Media.MaxPort = defaultIBCFMediaMaxPort
	}

	for i, a := range b.External.Addresses {
		b.External.Addresses[i] = IBCFAddress{Address: a.Address.Unmap(), Advertise: a.Advertise.Unmap()}
	}
}

func (c Config) validateIBCF(b IBCF) error {
	ext := b.External

	switch n := len(ext.Addresses); {
	case n == 0:
		return errors.New("ibcf.external.addresses needs at least one address")
	case n > 2:
		return errors.New("ibcf.external.addresses lists at most one IPv4 and one IPv6 address")
	}

	for i, e := range ext.Addresses {
		a := e.Address

		switch {
		case !a.IsValid():
			return errors.New("ibcf.external.addresses: an address is empty")
		case a.IsUnspecified():
			return fmt.Errorf("ibcf.external.addresses: %s must be a specific address, since it is given to SIP trunks", a)
		case a.Zone() != "":
			return fmt.Errorf("ibcf.external.addresses: %s must not have a zone", a)
		case slices.ContainsFunc(ext.Addresses[:i], func(o IBCFAddress) bool { return o.Address.Is4() == a.Is4() }):
			return errors.New("ibcf.external.addresses lists at most one IPv4 and one IPv6 address")
		case slices.Contains(c.SIP.Addresses, a):
			return fmt.Errorf("ibcf.external.addresses: %s is in sip.addresses: carriers must not reach the CSCFs", a)
		case slices.ContainsFunc(c.SIP.TrustedNetworks, func(p netip.Prefix) bool { return p.Contains(a) }):
			return fmt.Errorf("ibcf.external.addresses: %s is in sip.trusted_networks", a)
		}

		switch adv := e.Advertise; {
		case !adv.IsValid():
		case adv.IsUnspecified():
			return fmt.Errorf("ibcf.external.addresses: advertise %s must be a specific address, since carriers send to it", adv)
		case adv.Zone() != "":
			return fmt.Errorf("ibcf.external.addresses: advertise %s must not have a zone", adv)
		case adv.Is4() != a.Is4():
			return fmt.Errorf("ibcf.external.addresses: advertise %s is not of the family of %s", adv, a)
		}
	}

	if err := ext.validateTLS(); err != nil {
		return err
	}

	if err := b.Media.validate(); err != nil {
		return err
	}

	addrs := ext.Addrs()

	taken := []portOn{
		{"api.port", []netip.Addr{c.API.Address.Unmap()}, c.API.Port},
		{"diameter.port", []netip.Addr{c.Diameter.Address.Unmap()}, c.Diameter.Port},
	}

	if m := c.MCPTT; m != nil {
		taken = append(taken, portOn{"mcptt.media.control_port", []netip.Addr{m.Media.Address}, m.Media.ControlPort})
	}

	for _, t := range taken {
		for _, a := range addrs {
			for _, p := range externalPorts() {
				if t.port == p.port && slices.ContainsFunc(t.addrs, func(b netip.Addr) bool { return overlaps(a, b) }) {
					return fmt.Errorf("%s and %s are both %d on %s", p.name, t.name, t.port, a)
				}
			}
		}
	}

	if m := c.MCPTT; m != nil {
		for _, p := range externalPorts() {
			if p.port < m.Media.MinPort || p.port > m.Media.MaxPort {
				continue
			}

			for _, a := range addrs {
				if overlaps(a, m.Media.Address) {
					return fmt.Errorf("%s %d is in the range of the legs' ports of mcptt.media on %s", p.name, p.port, a)
				}
			}
		}
	}

	return c.ibcfMediaPortsFree(b)
}

func externalPorts() []namedPort {
	return []namedPort{{"the IBCF SIP port", IBCFExternalPort}, {"the IBCF SIP TLS port", IBCFTLSPort}}
}

type portOn struct {
	name  string
	addrs []netip.Addr
	port  int
}

// RFC 3550 section 11
func (m IBCFMedia) validate() error {
	switch {
	case m.MinPort < minIBCFMediaPort || m.MinPort > 65535 || m.MaxPort > 65535:
		return fmt.Errorf("ibcf.media.min_port %d and ibcf.media.max_port %d must be between %d and 65535", m.MinPort, m.MaxPort, minIBCFMediaPort)
	case m.MinPort%2 != 0:
		return fmt.Errorf("ibcf.media.min_port %d is odd: RTP takes even ports (RFC 3550 §11)", m.MinPort)
	case m.MaxPort <= m.MinPort:
		return fmt.Errorf("ibcf.media.max_port %d leaves no pair of ports above ibcf.media.min_port %d", m.MaxPort, m.MinPort)
	}

	return nil
}

func (c Config) ibcfMediaPortsFree(b IBCF) error {
	m := b.Media
	ext := b.External.Addrs()

	ports := []portOn{
		{"api.port", []netip.Addr{c.API.Address.Unmap()}, c.API.Port},
		{"diameter.port", []netip.Addr{c.Diameter.Address.Unmap()}, c.Diameter.Port},
	}

	for _, p := range externalPorts() {
		ports = append(ports, portOn{p.name, ext, p.port})
	}

	for _, p := range c.sipPorts() {
		ports = append(ports, portOn{p.name, c.SIP.Addresses, p.port})
	}

	ports = append(ports, portOn{"the IP-SM-GW port", c.SIP.Addresses, IPSMGWPort})

	if c.MCPTT != nil {
		ports = append(ports, portOn{"mcptt.media.control_port", []netip.Addr{c.MCPTT.Media.Address}, c.MCPTT.Media.ControlPort})
	}

	if c.N5 != nil && c.N5.Notify.Port != 0 {
		ports = append(ports, portOn{"n5.notify.port", []netip.Addr{c.N5.Notify.Address}, c.N5.Notify.Port})
	}

	for _, a := range slices.Concat(c.SIP.Addresses, ext) {
		for _, p := range ports {
			if p.port >= m.MinPort && p.port <= m.MaxPort && slices.ContainsFunc(p.addrs, func(b netip.Addr) bool { return overlaps(a, b) }) {
				return fmt.Errorf("%s %d is in the range of ibcf.media ports on %s", p.name, p.port, a)
			}
		}

		if mc := c.MCPTT; mc != nil && overlaps(a, mc.Media.Address) && mc.Media.MinPort <= m.MaxPort && m.MinPort <= mc.Media.MaxPort {
			return fmt.Errorf("the ranges of ibcf.media ports and mcptt.media ports overlap on %s", a)
		}
	}

	return nil
}

// SIPconnect 2.0 section 17.2
func (e IBCFExternal) validateTLS() error {
	t := e.TLS

	switch {
	case t == nil:
		return nil
	case (t.Cert == "") != (t.Key == ""):
		return errors.New("ibcf.external.tls.cert and ibcf.external.tls.key must both be set, or neither")
	}

	for name, path := range map[string]string{"ibcf.external.tls.cert": t.Cert, "ibcf.external.tls.key": t.Key} {
		if path == "" {
			continue
		}

		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	return nil
}
