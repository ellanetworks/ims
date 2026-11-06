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
	defaultIBCFExternalPort = 5060
	defaultKeepaliveFails   = 2
	maxKeepaliveFails       = 10
)

// RFC 3261 section 18.2.1
const defaultIBCFTLSPort = 5061

type IBCF struct {
	Port      int           `yaml:"port"`
	External  IBCFExternal  `yaml:"external"`
	Keepalive IBCFKeepalive `yaml:"keepalive"`
}

type IBCFKeepalive struct {
	Failures int `yaml:"failures"`
}

type IBCFExternal struct {
	Addresses           []netip.Addr `yaml:"addresses"`
	AdvertisedAddresses []netip.Addr `yaml:"advertised_addresses"`
	Port                int          `yaml:"port"`
	TLS                 *IBCFTLS     `yaml:"tls"`
}

type IBCFTLS struct {
	Port int    `yaml:"port"`
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

func (e IBCFExternal) Advertised() map[netip.Addr]netip.Addr {
	if len(e.AdvertisedAddresses) == 0 || len(e.AdvertisedAddresses) != len(e.Addresses) {
		return nil
	}

	out := make(map[netip.Addr]netip.Addr, len(e.Addresses))
	for i, a := range e.Addresses {
		out[a] = e.AdvertisedAddresses[i]
	}

	return out
}

func (b *IBCF) applyDefaults() {
	if b.Port == 0 {
		b.Port = defaultIBCFPort
	}

	if b.External.Port == 0 {
		b.External.Port = defaultIBCFExternalPort
	}

	if b.External.TLS != nil && b.External.TLS.Port == 0 {
		b.External.TLS.Port = defaultIBCFTLSPort
	}

	if b.Keepalive.Failures == 0 {
		b.Keepalive.Failures = defaultKeepaliveFails
	}

	for i, a := range b.External.Addresses {
		b.External.Addresses[i] = a.Unmap()
	}

	for i, a := range b.External.AdvertisedAddresses {
		b.External.AdvertisedAddresses[i] = a.Unmap()
	}
}

func (c Config) validateIBCF(b IBCF) error {
	ext := b.External

	if len(ext.Addresses) == 0 {
		return errors.New("ibcf.external.addresses needs at least one address")
	}

	seen := make(map[netip.Addr]bool, len(ext.Addresses))

	for _, a := range ext.Addresses {
		switch {
		case !a.IsValid():
			return errors.New("ibcf.external.addresses: an address is empty")
		case a.IsUnspecified():
			return fmt.Errorf("ibcf.external.addresses: %s must be a specific address, since it is given to SIP trunks", a)
		case a.Zone() != "":
			return fmt.Errorf("ibcf.external.addresses: %s must not have a zone", a)
		case seen[a]:
			return fmt.Errorf("ibcf.external.addresses: %s is listed twice", a)
		case slices.Contains(c.SIP.Addresses, a):
			return fmt.Errorf("ibcf.external.addresses: %s is in sip.addresses: carriers must not reach the CSCFs", a)
		case slices.ContainsFunc(c.SIP.TrustedNetworks, func(p netip.Prefix) bool { return p.Contains(a) }):
			return fmt.Errorf("ibcf.external.addresses: %s is in sip.trusted_networks", a)
		}

		seen[a] = true
	}

	if err := ext.validateAdvertised(); err != nil {
		return err
	}

	if ext.Port < 1 || ext.Port > 65535 {
		return fmt.Errorf("ibcf.external.port %d is out of range", ext.Port)
	}

	if err := ext.validateTLS(); err != nil {
		return err
	}

	if f := b.Keepalive.Failures; f < 1 || f > maxKeepaliveFails {
		return fmt.Errorf("ibcf.keepalive.failures %d must be between 1 and %d", f, maxKeepaliveFails)
	}

	taken := []struct {
		name string
		addr netip.Addr
		port int
	}{
		{"api.port", c.API.Address.Unmap(), c.API.Port},
		{"diameter.port", c.Diameter.Address.Unmap(), c.Diameter.Port},
	}

	if m := c.MCPTT; m != nil {
		taken = append(taken, struct {
			name string
			addr netip.Addr
			port int
		}{"mcptt.media.control_port", m.Media.Address, m.Media.ControlPort})
	}

	for _, t := range taken {
		for _, a := range ext.Addresses {
			for _, p := range ext.ports() {
				if t.port == p.port && overlaps(a, t.addr) {
					return fmt.Errorf("%s and %s are both %d on %s", p.name, t.name, t.port, a)
				}
			}
		}
	}

	for _, p := range ext.ports() {
		if c.MCPTT != nil && p.port >= c.MCPTT.Media.MinPort && p.port <= c.MCPTT.Media.MaxPort {
			for _, a := range ext.Addresses {
				if overlaps(a, c.MCPTT.Media.Address) {
					return fmt.Errorf("%s %d is in the range of the legs' ports of mcptt.media on %s", p.name, p.port, a)
				}
			}
		}
	}

	return nil
}

// RFC 3261 section 18.2.1
func (e IBCFExternal) validateAdvertised() error {
	adv := e.AdvertisedAddresses

	switch {
	case len(adv) == 0:
		return nil
	case len(adv) != len(e.Addresses):
		return fmt.Errorf("ibcf.external.advertised_addresses lists %d addresses, want one for each of the %d of ibcf.external.addresses",
			len(adv), len(e.Addresses))
	}

	for i, a := range adv {
		switch {
		case !a.IsValid(), a.IsUnspecified():
			return fmt.Errorf("ibcf.external.advertised_addresses: %s must be a specific address, since carriers send to it", a)
		case a.Zone() != "":
			return fmt.Errorf("ibcf.external.advertised_addresses: %s must not have a zone", a)
		case a.Is4() != e.Addresses[i].Is4():
			return fmt.Errorf("ibcf.external.advertised_addresses: %s is not of the family of %s", a, e.Addresses[i])
		case slices.Contains(adv[:i], a):
			return fmt.Errorf("ibcf.external.advertised_addresses: %s is listed twice", a)
		}
	}

	return nil
}

func (e IBCFExternal) ports() []namedPort {
	out := []namedPort{{"ibcf.external.port", e.Port}}
	if e.TLS != nil {
		out = append(out, namedPort{"ibcf.external.tls.port", e.TLS.Port})
	}

	return out
}

// SIPconnect 2.0 section 17.2
func (e IBCFExternal) validateTLS() error {
	t := e.TLS

	switch {
	case t == nil:
		return nil
	case t.Port < 1 || t.Port > 65535:
		return fmt.Errorf("ibcf.external.tls.port %d is out of range", t.Port)
	case t.Port == e.Port:
		return fmt.Errorf("ibcf.external.tls.port and ibcf.external.port are both %d", t.Port)
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
