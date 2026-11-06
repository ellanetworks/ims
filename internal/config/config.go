package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultAPIPort      = 5020
	defaultDiameterPort = 3868
	defaultPCSCFPort    = 5060
	defaultICSCFPort    = 5070
	defaultSCSCFPort    = 5080

	IPSMGWPort         = 5100
	defaultIPsecServer = 5063
)

var defaultIPsecClients = []int{5064, 5065}

// RFC 3261 section 17.1.1.2
const maxASTimeout = 32 * time.Second

type Config struct {
	Logging   Logging   `yaml:"logging"`
	DB        DB        `yaml:"db"`
	API       API       `yaml:"api"`
	SIP       SIP       `yaml:"sip"`
	PCSCF     PCSCF     `yaml:"pcscf"`
	ICSCF     ICSCF     `yaml:"icscf"`
	SCSCF     SCSCF     `yaml:"scscf"`
	Diameter  Diameter  `yaml:"diameter"`
	N5        *N5       `yaml:"n5"`
	MCPTT     *MCPTT    `yaml:"mcptt"`
	IBCF      *IBCF     `yaml:"ibcf"`
	TrGW      *TrGW     `yaml:"trgw"`
	Telemetry Telemetry `yaml:"telemetry"`
}

type Telemetry struct {
	Enabled      bool   `yaml:"enabled"`
	OTLPEndpoint string `yaml:"otlp_endpoint"`
}

type Logging struct {
	Level slog.Level `yaml:"level"`
}

type DB struct {
	Path string `yaml:"path"`
}

type API struct {
	Address netip.Addr `yaml:"address"`
	Port    int        `yaml:"port"`
	TLS     *APITLS    `yaml:"tls"`
}

type APITLS struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type SIP struct {
	Addresses []netip.Addr `yaml:"addresses"`

	TrustedNetworks []netip.Prefix `yaml:"trusted_networks"`
}

type PCSCF struct {
	Port  int   `yaml:"port"`
	IPsec IPsec `yaml:"ipsec"`
}

type N5 struct {
	Notify N5Notify `yaml:"notify"`
	TLS    *TLS     `yaml:"tls"`
}

type N5Notify struct {
	Address netip.Addr `yaml:"address"`
	Port    int        `yaml:"port"`
}

func (c Config) N5NotifyURI() string {
	if c.N5 == nil {
		return ""
	}

	return c.N5NotifyURIAt(uint16(c.N5.Notify.Port))
}

func (c Config) N5NotifyURIAt(port uint16) string {
	n := c.N5
	if n == nil {
		return ""
	}

	scheme := "http"
	if n.TLS != nil {
		scheme = "https"
	}

	return scheme + "://" + netip.AddrPortFrom(n.Notify.Address, port).String()
}

type TLS struct {
	CA   string `yaml:"ca"`
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type IPsec struct {
	ServerPort  int   `yaml:"server_port"`
	ClientPorts []int `yaml:"client_ports"`
}

type ICSCF struct {
	Port int `yaml:"port"`
}

type SCSCF struct {
	Port int `yaml:"port"`

	ASTimeout time.Duration `yaml:"as_timeout"`
}

type Diameter struct {
	Address netip.Addr `yaml:"address"`
	Port    int        `yaml:"port"`
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	if cfg.API.Port == 0 {
		cfg.API.Port = defaultAPIPort
	}

	if cfg.PCSCF.Port == 0 {
		cfg.PCSCF.Port = defaultPCSCFPort
	}

	if cfg.PCSCF.IPsec.ServerPort == 0 {
		cfg.PCSCF.IPsec.ServerPort = defaultIPsecServer
	}

	if len(cfg.PCSCF.IPsec.ClientPorts) == 0 {
		cfg.PCSCF.IPsec.ClientPorts = slices.Clone(defaultIPsecClients)
	}

	if cfg.ICSCF.Port == 0 {
		cfg.ICSCF.Port = defaultICSCFPort
	}

	if cfg.SCSCF.Port == 0 {
		cfg.SCSCF.Port = defaultSCSCFPort
	}

	for i, a := range cfg.SIP.Addresses {
		cfg.SIP.Addresses[i] = a.Unmap()
	}

	if cfg.Diameter.Port == 0 {
		cfg.Diameter.Port = defaultDiameterPort
	}

	if cfg.MCPTT != nil {
		cfg.MCPTT.applyDefaults()
	}

	if cfg.IBCF != nil {
		cfg.IBCF.applyDefaults()
	}

	cfg.applyTrGWDefaults()

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) validate() error {
	switch {
	case c.DB.Path == "":
		return errors.New("db.path is required")
	case !c.API.Address.IsValid():
		return errors.New("api.address is required")
	case c.API.Port < 1 || c.API.Port > 65535:
		return fmt.Errorf("api.port %d is out of range", c.API.Port)
	case c.Telemetry.Enabled && c.Telemetry.OTLPEndpoint == "":
		return errors.New("telemetry.otlp_endpoint is required when telemetry.enabled is true")
	}

	if tls := c.API.TLS; tls != nil {
		if tls.Cert == "" || tls.Key == "" {
			return errors.New("api.tls.cert and api.tls.key must both be set")
		}

		for name, path := range map[string]string{"api.tls.cert": tls.Cert, "api.tls.key": tls.Key} {
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}

	if err := c.SIP.validate(); err != nil {
		return err
	}

	if err := c.validatePorts(); err != nil {
		return err
	}

	if t := c.SCSCF.ASTimeout; t < 0 || t >= maxASTimeout {
		return fmt.Errorf("scscf.as_timeout %s must be positive and under %s", t, maxASTimeout)
	}

	if err := c.Diameter.validate(); err != nil {
		return err
	}

	if c.MCPTT != nil {
		if err := c.validateMCPTT(*c.MCPTT); err != nil {
			return err
		}
	}

	if c.IBCF != nil {
		if err := c.validateIBCF(*c.IBCF); err != nil {
			return err
		}
	}

	if err := c.validateTrGW(); err != nil {
		return err
	}

	if c.N5 == nil {
		return nil
	}

	return c.validateN5(*c.N5)
}

func (c Config) validateN5(n N5) error {
	if n.TLS != nil {
		switch {
		case n.TLS.CA == "":
			return errors.New("n5.tls.ca is required")
		case n.TLS.Cert == "":
			return errors.New("n5.tls.cert is required")
		case n.TLS.Key == "":
			return errors.New("n5.tls.key is required")
		}
	}

	a := n.Notify.Address

	switch {
	case !a.IsValid():
		return errors.New("n5.notify.address is required")
	case a.IsUnspecified():
		return fmt.Errorf("n5.notify.address must be a specific address, not %s, since the PCF sends to it", a)
	case a.Zone() != "" || a.Is4In6():
		return fmt.Errorf("n5.notify.address %s must be a plain IPv4 or IPv6 address", a)
	case n.Notify.Port < 0 || n.Notify.Port > 65535:
		return fmt.Errorf("n5.notify.port %d is out of range", n.Notify.Port)
	case n.Notify.Port == 0:
		return nil
	}

	taken := []struct {
		name string
		addr []netip.Addr
		port int
	}{
		{"api.port", []netip.Addr{c.API.Address.Unmap()}, c.API.Port},
		{"diameter.port", []netip.Addr{c.Diameter.Address.Unmap()}, c.Diameter.Port},
		{"pcscf.port", c.SIP.Addresses, c.PCSCF.Port},
		{"pcscf.ipsec.server_port", c.SIP.Addresses, c.PCSCF.IPsec.ServerPort},
		{"icscf.port", c.SIP.Addresses, c.ICSCF.Port},
		{"scscf.port", c.SIP.Addresses, c.SCSCF.Port},
	}

	for i, p := range c.PCSCF.IPsec.ClientPorts {
		taken = append(taken, struct {
			name string
			addr []netip.Addr
			port int
		}{fmt.Sprintf("pcscf.ipsec.client_ports[%d]", i), c.SIP.Addresses, p})
	}

	if c.MCPTT != nil {
		taken = append(taken, struct {
			name string
			addr []netip.Addr
			port int
		}{"mcptt.port", c.SIP.Addresses, c.MCPTT.Port})
	}

	if c.IBCF != nil {
		taken = append(taken, struct {
			name string
			addr []netip.Addr
			port int
		}{"ibcf.port", c.SIP.Addresses, c.IBCF.Port}, struct {
			name string
			addr []netip.Addr
			port int
		}{"ibcf.external.port", c.IBCF.External.Addresses, c.IBCF.External.Port})

		if t := c.IBCF.External.TLS; t != nil {
			taken = append(taken, struct {
				name string
				addr []netip.Addr
				port int
			}{"ibcf.external.tls.port", c.IBCF.External.Addresses, t.Port})
		}
	}

	for _, t := range taken {
		if t.port == n.Notify.Port && slices.ContainsFunc(t.addr, func(b netip.Addr) bool { return overlaps(a, b) }) {
			return fmt.Errorf("n5.notify.port and %s are both %d on %s", t.name, t.port, a)
		}
	}

	return nil
}

func overlaps(a, b netip.Addr) bool {
	switch {
	case b == netip.IPv6Unspecified():
		return true
	case b == netip.IPv4Unspecified():
		return a.Is4()
	}

	return a == b
}

type namedPort struct {
	name string
	port int
}

func (c Config) sipPorts() []namedPort {
	ports := []namedPort{
		{"pcscf.port", c.PCSCF.Port},
		{"icscf.port", c.ICSCF.Port},
		{"scscf.port", c.SCSCF.Port},
		{"pcscf.ipsec.server_port", c.PCSCF.IPsec.ServerPort},
	}

	for i, p := range c.PCSCF.IPsec.ClientPorts {
		ports = append(ports, namedPort{fmt.Sprintf("pcscf.ipsec.client_ports[%d]", i), p})
	}

	if c.MCPTT != nil {
		ports = append(ports, namedPort{"mcptt.port", c.MCPTT.Port})
	}

	if c.IBCF != nil {
		ports = append(ports, namedPort{"ibcf.port", c.IBCF.Port})
	}

	return ports
}

func (c Config) validatePorts() error {
	if len(c.PCSCF.IPsec.ClientPorts) != 2 {
		return errors.New("pcscf.ipsec.client_ports must list 2 ports")
	}

	ports := c.sipPorts()

	shared := c.apiSharesSIPAddress()

	for i, p := range ports {
		if p.port < 1 || p.port > 65535 {
			return fmt.Errorf("%s %d is out of range", p.name, p.port)
		}

		for _, q := range ports[:i] {
			if q.port == p.port {
				return fmt.Errorf("%s and %s are both %d", q.name, p.name, p.port)
			}
		}

		if p.port == IPSMGWPort {
			return fmt.Errorf("%s %d is the port of the IP-SM-GW", p.name, p.port)
		}

		if strings.HasPrefix(p.name, "pcscf.ipsec.") && (p.port == 5060 || p.port == 5061) {
			return fmt.Errorf("%s %d is a standard SIP port", p.name, p.port)
		}

		if shared && p.port == c.API.Port {
			return fmt.Errorf("%s and api.port are both %d on %s", p.name, p.port, c.API.Address)
		}
	}

	return nil
}

func (c Config) apiSharesSIPAddress() bool {
	api := c.API.Address.Unmap()

	return slices.ContainsFunc(c.SIP.Addresses, func(a netip.Addr) bool {
		switch {
		case api == netip.IPv6Unspecified():
			return true
		case api == netip.IPv4Unspecified():
			return a.Is4()
		default:
			return a == api
		}
	})
}

func (s SIP) validate() error {
	if len(s.Addresses) == 0 {
		return errors.New("sip.addresses needs at least one address")
	}

	seen := make(map[netip.Addr]bool, len(s.Addresses))

	for _, a := range s.Addresses {
		switch {
		case !a.IsValid():
			return errors.New("sip.addresses: an address is empty")
		case a.IsUnspecified():
			return fmt.Errorf("sip.addresses: %s must be a specific address, since it is given to UEs", a)
		case a.Zone() != "":
			return fmt.Errorf("sip.addresses: %s must not have a zone", a)
		case seen[a]:
			return fmt.Errorf("sip.addresses: %s is listed twice", a)
		}

		seen[a] = true
	}

	for _, p := range s.TrustedNetworks {
		switch {
		case !p.IsValid():
			return errors.New("sip.trusted_networks: a network is empty")
		case p.Bits() == 0, p.Masked() == netip.MustParsePrefix("::ffff:0.0.0.0/96"):
			return fmt.Errorf("sip.trusted_networks: %s covers every address of a family, the UEs' with them", p)
		}
	}

	return nil
}

func (d Diameter) validate() error {
	switch {
	case !d.Address.IsValid():
		return errors.New("diameter.address is required")
	case d.Address.IsUnspecified():
		return errors.New("diameter.address must be a specific address, not 0.0.0.0 or ::, since it is advertised to peers")
	case d.Port < 1 || d.Port > 65535:
		return fmt.Errorf("diameter.port %d is out of range", d.Port)
	}

	return nil
}
