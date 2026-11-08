package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
)

func (c *Config) resolveInterfaces() error {
	switch {
	case c.SIP.Name != "" && len(c.SIP.Addresses) > 0:
		return errors.New("sip.name and sip.addresses cannot both be set")
	case c.Diameter.Name != "" && c.Diameter.Address.IsValid():
		return errors.New("diameter.name and diameter.address cannot both be set")
	}

	if c.SIP.Name != "" {
		addrs, err := interfaceAddrs(c.SIP.Name)
		if err != nil {
			return fmt.Errorf("sip.name %s: %w", c.SIP.Name, err)
		}

		c.SIP.Addresses = addrs
	}

	if c.Diameter.Name != "" {
		addrs, err := interfaceAddrs(c.Diameter.Name)
		if err != nil {
			return fmt.Errorf("diameter.name %s: %w", c.Diameter.Name, err)
		}

		c.Diameter.addrs = addrs
	}

	return nil
}

func interfaceAddrs(name string) ([]netip.Addr, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}

	ifaddrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}

	var addrs []netip.Addr

	for _, ifaddr := range ifaddrs {
		prefix, err := netip.ParsePrefix(ifaddr.String())
		if err != nil {
			continue
		}

		if a := prefix.Addr().Unmap(); !a.IsUnspecified() && !a.IsMulticast() && !a.IsLinkLocalUnicast() {
			addrs = append(addrs, a)
		}
	}

	if len(addrs) == 0 {
		return nil, errors.New("the interface has no usable address")
	}

	return addrs, nil
}
