package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
)

// resolveInterfaces sets the addresses given as the name of a network interface, such as a pod's eth0, whose
// addresses are not known in advance: SIP takes all the interface's addresses, the others its first, IPv4 first.
func (c *Config) resolveInterfaces() error {
	if name := c.SIP.Name; name != "" {
		if len(c.SIP.Addresses) > 0 {
			return errors.New("sip.name and sip.addresses cannot both be set")
		}

		addrs, err := interfaceAddrs(name)
		if err != nil {
			return fmt.Errorf("sip.name %s: %w", name, err)
		}

		c.SIP.Addresses = addrs
	}

	if err := resolveInterface("diameter", c.Diameter.Name, &c.Diameter.Address); err != nil {
		return err
	}

	if c.N5 != nil {
		if err := resolveInterface("n5.notify", c.N5.Notify.Name, &c.N5.Notify.Address); err != nil {
			return err
		}
	}

	if c.MCPTT != nil {
		if err := resolveInterface("mcptt.media", c.MCPTT.Media.Name, &c.MCPTT.Media.Address); err != nil {
			return err
		}
	}

	return nil
}

// resolveInterface sets addr, of the key prefix, to the first address of the interface name, if given.
func resolveInterface(prefix, name string, addr *netip.Addr) error {
	if name == "" {
		return nil
	}

	if addr.IsValid() {
		return fmt.Errorf("%s.name and %s.address cannot both be set", prefix, prefix)
	}

	addrs, err := interfaceAddrs(name)
	if err != nil {
		return fmt.Errorf("%s.name %s: %w", prefix, name, err)
	}

	*addr = addrs[0]

	return nil
}

// interfaceAddrs are the addresses of the interface name that peers can reach, IPv4 first: not its link-local ones.
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

	slices.SortStableFunc(addrs, func(a, b netip.Addr) int {
		switch {
		case a.Is4() == b.Is4():
			return 0
		case a.Is4():
			return -1
		default:
			return 1
		}
	})

	return addrs, nil
}
