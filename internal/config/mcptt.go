package config

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
)

const (
	defaultMCPTTPort        = 5090
	defaultMCPTTMinPort     = 20000
	defaultMCPTTMaxPort     = 20999
	defaultMCPTTControlPort = 21000
)

// MCPTT is the built-in MCPTT application server (TS 24.379): the participating and controlling functions of one
// MCPTT system. Without it, the IMS runs none, and opens none of its ports.
type MCPTT struct {
	// Mode is how clients reach it: through the IMS, as its application server, the default; or directly, as the
	// registrar of its clients, for the lab and tests only.
	Mode MCPTTMode `yaml:"mode"`
	// Port is its SIP port, UDP and TCP, on each of sip.addresses: the I-CSCF and the S-CSCF send it their requests
	// there, and in direct mode its clients theirs.
	Port  int        `yaml:"port"`
	Media MCPTTMedia `yaml:"media"`
}

type MCPTTMode string

const (
	// MCPTTModeIMS: the AS is told of registrations by third-party REGISTERs (TS 24.229 §5.4.1.7).
	MCPTTModeIMS MCPTTMode = "ims"
	// MCPTTModeDirect: the AS is the registrar of its clients, without the IMS.
	MCPTTModeDirect MCPTTMode = "direct"
)

// MCPTTMedia is the media relay of the calls: voice and the floor control channels.
type MCPTTMedia struct {
	// Address is the address its sockets bind; Advertise the one of the SDP c= lines, required if Address is
	// unspecified.
	Address   netip.Addr `yaml:"address"`
	Advertise netip.Addr `yaml:"advertise"`
	// MinPort and MaxPort are the range of the ports of the legs, MinPort even: audio on an even port, its RTCP on the
	// next (RFC 3550 §11). ControlPort is the port the control channels share (TS 24.380 §4.3.3.1), outside the range.
	MinPort     int `yaml:"min_port"`
	MaxPort     int `yaml:"max_port"`
	ControlPort int `yaml:"control_port"`
}

func (m *MCPTT) applyDefaults() {
	if m.Mode == "" {
		m.Mode = MCPTTModeIMS
	}

	if m.Port == 0 {
		m.Port = defaultMCPTTPort
	}

	md := &m.Media
	md.Address, md.Advertise = md.Address.Unmap(), md.Advertise.Unmap()

	if md.MinPort == 0 {
		md.MinPort = defaultMCPTTMinPort
	}

	if md.MaxPort == 0 {
		md.MaxPort = defaultMCPTTMaxPort
	}

	if md.ControlPort == 0 {
		md.ControlPort = defaultMCPTTControlPort
	}
}

func (c Config) validateMCPTT(m MCPTT) error {
	switch m.Mode {
	case MCPTTModeIMS, MCPTTModeDirect:
	default:
		return fmt.Errorf("mcptt.mode %q is neither %q nor %q", m.Mode, MCPTTModeIMS, MCPTTModeDirect)
	}

	md := m.Media

	switch a := md.Address; {
	case !a.IsValid():
		return errors.New("mcptt.media.address is required")
	case a.Zone() != "":
		return fmt.Errorf("mcptt.media.address %s must not have a zone", a)
	}

	switch a := md.Advertise; {
	case !a.IsValid() && md.Address.IsUnspecified():
		return fmt.Errorf("mcptt.media.advertise is required, since %s cannot be in the SDP", md.Address)
	case !a.IsValid():
	case a.IsUnspecified():
		return fmt.Errorf("mcptt.media.advertise %s cannot be in the SDP", a)
	case a.Zone() != "":
		return fmt.Errorf("mcptt.media.advertise %s must not have a zone", a)
	case md.Address != netip.IPv6Unspecified() && md.Address.Is4() != a.Is4():
		// The media to the SDP's address reach the sockets: of its family, unless they bind :: (RFC 3493 §3.7).
		return fmt.Errorf("mcptt.media.advertise %s is not of the family of mcptt.media.address %s", a, md.Address)
	}

	for _, p := range []struct {
		name string
		port int
	}{{"mcptt.media.min_port", md.MinPort}, {"mcptt.media.max_port", md.MaxPort}, {"mcptt.media.control_port", md.ControlPort}} {
		if p.port < 1 || p.port > 65535 {
			return fmt.Errorf("%s %d is out of range", p.name, p.port)
		}
	}

	switch {
	case md.MinPort%2 != 0:
		return fmt.Errorf("mcptt.media.min_port %d is odd: audio takes even ports (RFC 3550 §11)", md.MinPort)
	case md.MaxPort <= md.MinPort:
		return fmt.Errorf("mcptt.media.max_port %d leaves no pair of ports above mcptt.media.min_port %d", md.MaxPort, md.MinPort)
	case md.MinPort <= md.ControlPort && md.ControlPort <= md.MaxPort:
		return fmt.Errorf("mcptt.media.control_port %d is in the range of the legs' ports", md.ControlPort)
	}

	// The media sockets are UDP, as SIP's: on an address of SIP, none takes a port of SIP.
	if !slices.ContainsFunc(c.SIP.Addresses, func(a netip.Addr) bool { return overlaps(a, md.Address) }) {
		return nil
	}

	for _, p := range c.sipPorts() {
		switch {
		case p.port == md.ControlPort:
			return fmt.Errorf("mcptt.media.control_port and %s are both %d on %s", p.name, p.port, md.Address)
		case md.MinPort <= p.port && p.port <= md.MaxPort:
			return fmt.Errorf("%s %d is in the range of the legs' ports of mcptt.media on %s", p.name, p.port, md.Address)
		}
	}

	return nil
}
