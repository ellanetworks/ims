package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/sip"
	"gopkg.in/yaml.v3"
)

const (
	defaultAPIPort       = 5020
	defaultCallRetention = 90 * 24 * time.Hour
	defaultDiameterPort  = 3868
	defaultPCSCFPort     = 5060
	defaultICSCFPort     = 5070
	defaultSCSCFPort     = 5080
	defaultMinExpires    = 60
	defaultMaxExpires    = 3600
	defaultIPsecServer   = 5063
)

var defaultIPsecClients = []int{5064, 5065}

type Transport string

const (
	TransportTCP  Transport = "tcp"
	TransportSCTP Transport = "sctp"
)

type Application string

const (
	ApplicationCx Application = "cx"
	ApplicationRx Application = "rx"
)

type Config struct {
	DB          DB          `yaml:"db"`
	CallHistory CallHistory `yaml:"call_history"`
	API         API         `yaml:"api"`
	IMS         IMS         `yaml:"ims"`
	SIP         SIP         `yaml:"sip"`
	PCSCF       PCSCF       `yaml:"pcscf"`
	ICSCF       ICSCF       `yaml:"icscf"`
	SCSCF       SCSCF       `yaml:"scscf"`
	Diameter    Diameter    `yaml:"diameter"`
}

type DB struct {
	Path string `yaml:"path"`
}

type CallHistory struct {
	Retention time.Duration `yaml:"retention"`
}

type API struct {
	Address netip.Addr `yaml:"address"`
	Port    int        `yaml:"port"`
}

type IMS struct {
	MCC string `yaml:"mcc"`
	MNC string `yaml:"mnc"`

	HomeDomain      string         `yaml:"home_domain"`
	TrustedNetworks []netip.Prefix `yaml:"trusted_networks"`
}

type SIP struct {
	Addresses      []netip.Addr `yaml:"addresses"`
	Aliases        []string     `yaml:"aliases"`
	MaxConnections int          `yaml:"max_connections"`
}

type PCSCF struct {
	Port  int   `yaml:"port"`
	IPsec IPsec `yaml:"ipsec"`
}

type IPsec struct {
	ServerPort  int                    `yaml:"server_port"`
	ClientPorts []int                  `yaml:"client_ports"`
	Integrity   []ipsec.Integrity      `yaml:"integrity"`
	Encryption  ipsec.EncryptionPolicy `yaml:"encryption"`
}

func (i IPsec) Policy() ipsec.Policy {
	p := ipsec.DefaultPolicy()

	if len(i.Integrity) > 0 {
		p.Integrity = i.Integrity
	}

	if i.Encryption != "" {
		p.Encryption = i.Encryption
	}

	return p
}

type ICSCF struct {
	Port int `yaml:"port"`
}

type SCSCF struct {
	Port         int      `yaml:"port"`
	Name         string   `yaml:"name"`
	Capabilities []uint32 `yaml:"capabilities"`
	MinExpires   int      `yaml:"min_expires"`
	MaxExpires   int      `yaml:"max_expires"`

	ReauthInterval time.Duration `yaml:"reauth_interval"`
	ReauthExpires  time.Duration `yaml:"reauth_expires"`
}

func DefaultSCSCFName(homeDomain string, port int) string {
	return "sip:scscf." + homeDomain + ":" + strconv.Itoa(port)
}

func HomeDomain(mcc, mnc string) string {
	if len(mnc) == 2 {
		mnc = "0" + mnc
	}

	return "ims.mnc" + mnc + ".mcc" + mcc + ".3gppnetwork.org"
}

func (c Config) SIPAliases() []string {
	aliases := append([]string{c.IMS.HomeDomain}, c.SIP.Aliases...)

	name := c.SCSCF.Name
	if name == "" {
		name = DefaultSCSCFName(c.IMS.HomeDomain, c.SCSCF.Port)
	}

	u, err := sip.ParseURI(name)
	if err == nil && !isIPLiteral(u.Host) && !slices.Contains(aliases, strings.ToLower(u.Host)) {
		aliases = append(aliases, strings.ToLower(u.Host))
	}

	return aliases
}

type Diameter struct {
	OriginHost  string         `yaml:"origin_host"`
	OriginRealm string         `yaml:"origin_realm"`
	Address     netip.Addr     `yaml:"address"`
	Peers       []DiameterPeer `yaml:"peers"`
}

type DiameterPeer struct {
	ID           string        `yaml:"id"`
	Host         string        `yaml:"host"`
	Realm        string        `yaml:"realm"`
	Address      netip.Addr    `yaml:"address"`
	Port         int           `yaml:"port"`
	Transport    Transport     `yaml:"transport"`
	Applications []Application `yaml:"applications"`
}

func (p DiameterPeer) Serves(app Application) bool {
	return slices.Contains(p.Applications, app)
}

func (d Diameter) CxPeer() DiameterPeer {
	for _, p := range d.Peers {
		if p.Serves(ApplicationCx) {
			return p
		}
	}

	return DiameterPeer{}
}

func (d Diameter) RxPeer() (DiameterPeer, bool) {
	for _, p := range d.Peers {
		if p.Serves(ApplicationRx) {
			return p, true
		}
	}

	return DiameterPeer{}, false
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

	if cfg.CallHistory.Retention == 0 {
		cfg.CallHistory.Retention = defaultCallRetention
	}

	if cfg.API.Port == 0 {
		cfg.API.Port = defaultAPIPort
	}

	cfg.IMS.HomeDomain = strings.ToLower(cfg.IMS.HomeDomain)
	if cfg.IMS.HomeDomain == "" && isDigits(cfg.IMS.MCC) && isDigits(cfg.IMS.MNC) {
		cfg.IMS.HomeDomain = HomeDomain(cfg.IMS.MCC, cfg.IMS.MNC)
	}

	for i, p := range cfg.IMS.TrustedNetworks {
		if p.Addr().Is4In6() && p.Bits() < 96 {
			continue
		}

		cfg.IMS.TrustedNetworks[i] = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-unmappedBits(p)).Masked()
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

	policy := cfg.PCSCF.IPsec.Policy()
	cfg.PCSCF.IPsec.Integrity, cfg.PCSCF.IPsec.Encryption = policy.Integrity, policy.Encryption

	if cfg.ICSCF.Port == 0 {
		cfg.ICSCF.Port = defaultICSCFPort
	}

	if cfg.SCSCF.Port == 0 {
		cfg.SCSCF.Port = defaultSCSCFPort
	}

	cfg.SCSCF.Name = strings.ToLower(cfg.SCSCF.Name)
	if cfg.SCSCF.Name == "" {
		cfg.SCSCF.Name = DefaultSCSCFName(cfg.IMS.HomeDomain, cfg.SCSCF.Port)
	}

	for i, a := range cfg.SIP.Addresses {
		cfg.SIP.Addresses[i] = a.Unmap()
	}

	for i, a := range cfg.SIP.Aliases {
		cfg.SIP.Aliases[i] = strings.ToLower(a)
	}

	if cfg.SCSCF.MinExpires == 0 {
		cfg.SCSCF.MinExpires = defaultMinExpires
	}

	if cfg.SCSCF.MaxExpires == 0 {
		cfg.SCSCF.MaxExpires = defaultMaxExpires
	}

	for i := range cfg.Diameter.Peers {
		p := &cfg.Diameter.Peers[i]

		if p.Port == 0 {
			p.Port = defaultDiameterPort
		}

		if p.Transport == "" {
			p.Transport = TransportTCP
		}
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) validate() error {
	switch {
	case c.DB.Path == "":
		return errors.New("db.path is required")
	case c.CallHistory.Retention < 0:
		return errors.New("call_history.retention must be positive")
	case !c.API.Address.IsValid():
		return errors.New("api.address is required")
	case c.API.Port < 1 || c.API.Port > 65535:
		return fmt.Errorf("api.port %d is out of range", c.API.Port)
	}

	if err := c.IMS.validate(); err != nil {
		return err
	}

	if err := c.SIP.validate(c.IMS.HomeDomain); err != nil {
		return err
	}

	if err := c.validatePorts(); err != nil {
		return err
	}

	if err := c.SCSCF.validate(c.SIP, c.IMS.HomeDomain); err != nil {
		return err
	}

	if err := c.PCSCF.IPsec.Policy().Validate(); err != nil {
		return fmt.Errorf("pcscf.ipsec: %w", err)
	}

	return c.Diameter.validate()
}

func (c Config) validatePorts() error {
	ports := []struct {
		name string
		port int
	}{
		{"pcscf.port", c.PCSCF.Port},
		{"icscf.port", c.ICSCF.Port},
		{"scscf.port", c.SCSCF.Port},
		{"pcscf.ipsec.server_port", c.PCSCF.IPsec.ServerPort},
	}

	if len(c.PCSCF.IPsec.ClientPorts) != 2 {
		return errors.New("pcscf.ipsec.client_ports must list 2 ports")
	}

	for i, p := range c.PCSCF.IPsec.ClientPorts {
		ports = append(ports, struct {
			name string
			port int
		}{fmt.Sprintf("pcscf.ipsec.client_ports[%d]", i), p})
	}

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

func (i IMS) validate() error {
	switch {
	case len(i.MCC) != 3 || !isDigits(i.MCC):
		return fmt.Errorf("ims.mcc %q must be 3 digits", i.MCC)
	case len(i.MNC) != 2 && len(i.MNC) != 3 || !isDigits(i.MNC):
		return fmt.Errorf("ims.mnc %q must be 2 or 3 digits", i.MNC)
	case !isDomainName(i.HomeDomain):
		return fmt.Errorf("ims.home_domain %q is not a domain name", i.HomeDomain)
	}

	for _, p := range i.TrustedNetworks {
		switch {
		case !p.IsValid() || p.Addr().Zone() != "":
			return fmt.Errorf("ims.trusted_networks: %s is not a network", p)
		case p.Addr().Is4In6():
			return fmt.Errorf("ims.trusted_networks: %s is IPv4-mapped and must be /96 or longer", p)
		case p.Bits() == 0:
			return fmt.Errorf("ims.trusted_networks: %s would trust every address, UEs included", p)
		}
	}

	return nil
}

func (s SIP) validate(homeDomain string) error {
	switch {
	case len(s.Addresses) == 0:
		return errors.New("sip.addresses needs at least one address")
	case s.MaxConnections < 0:
		return fmt.Errorf("sip.max_connections %d must not be negative", s.MaxConnections)
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

	names := map[string]bool{homeDomain: true}

	for _, alias := range s.Aliases {
		switch {
		case alias == "":
			return errors.New("sip.aliases: an alias is empty")
		case !isDomainName(alias) && !isIPLiteral(alias):
			return fmt.Errorf("sip.aliases: %q is neither a domain name nor an IP address", alias)
		case names[alias]:
			return fmt.Errorf("sip.aliases: %s is listed twice or is the home domain", alias)
		}

		names[alias] = true
	}

	return nil
}

func (s SCSCF) validate(sipConfig SIP, homeDomain string) error {
	switch {
	case s.MinExpires < 1:
		return fmt.Errorf("scscf.min_expires %d must be positive", s.MinExpires)
	case s.MaxExpires < s.MinExpires:
		return fmt.Errorf("scscf.max_expires %d is below scscf.min_expires %d", s.MaxExpires, s.MinExpires)
	case s.ReauthInterval < 0:
		return fmt.Errorf("scscf.reauth_interval %s is negative", s.ReauthInterval)
	case s.ReauthExpires < 0:
		return fmt.Errorf("scscf.reauth_expires %s is negative", s.ReauthExpires)
	}

	u, err := sip.ParseURI(s.Name)

	switch {
	case err != nil || !strings.EqualFold(u.Scheme, "sip"):
		return fmt.Errorf("scscf.name %q is not a SIP URI", s.Name)
	case u.User != "" || len(u.Params) > 0 || u.Headers != "":
		return fmt.Errorf("scscf.name %q must have no user part, parameters or headers", s.Name)
	case int(u.Port) != s.Port:
		return fmt.Errorf("scscf.name %q must have the port of scscf.port %d", s.Name, s.Port)
	}

	if a, ok := sip.HostAddr(u.Host); ok {
		if !slices.Contains(sipConfig.Addresses, a.Unmap()) {
			return fmt.Errorf("scscf.name %q: %s is not one of sip.addresses", s.Name, a)
		}

		return nil
	}

	host := strings.ToLower(u.Host)

	switch {
	case !isDomainName(host):
		return fmt.Errorf("scscf.name %q: %q is not a domain name", s.Name, u.Host)
	case host != homeDomain && host != "scscf."+homeDomain && !slices.Contains(sipConfig.Aliases, host):
		return fmt.Errorf("scscf.name %q: %s must be the home domain, scscf.%s or one of sip.aliases", s.Name, u.Host, homeDomain)
	}

	return nil
}

func unmappedBits(p netip.Prefix) int {
	if p.Addr().Is4In6() && p.Bits() >= 96 {
		return 96
	}

	return 0
}

func isDomainName(s string) bool {
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

func isIPLiteral(s string) bool {
	a, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"))
	return err == nil && a.Zone() == ""
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}

	return s != ""
}

func (d Diameter) validate() error {
	switch {
	case d.OriginHost == "":
		return errors.New("diameter.origin_host is required")
	case d.OriginRealm == "":
		return errors.New("diameter.origin_realm is required")
	case !d.Address.IsValid():
		return errors.New("diameter.address is required")
	case d.Address.IsUnspecified():
		return errors.New("diameter.address must be a specific address, not 0.0.0.0 or ::, since it is advertised to peers")
	}

	ids := make(map[string]bool, len(d.Peers))

	var cx, rx int

	for _, p := range d.Peers {
		if err := p.validate(); err != nil {
			return err
		}

		if ids[p.ID] {
			return fmt.Errorf("diameter peer %q is defined twice", p.ID)
		}

		ids[p.ID] = true

		if p.Serves(ApplicationCx) {
			cx++
		}

		if p.Serves(ApplicationRx) {
			rx++
		}
	}

	switch {
	case cx != 1:
		return fmt.Errorf("exactly one diameter peer must serve cx, found %d", cx)
	case rx > 1:
		return fmt.Errorf("at most one diameter peer may serve rx, found %d", rx)
	}

	return nil
}

func (p DiameterPeer) validate() error {
	switch {
	case p.ID == "":
		return errors.New("every diameter peer needs an id")
	case p.Host == "":
		return fmt.Errorf("diameter peer %q: host is required", p.ID)
	case p.Realm == "":
		return fmt.Errorf("diameter peer %q: realm is required", p.ID)
	case !p.Address.IsValid():
		return fmt.Errorf("diameter peer %q: address is required", p.ID)
	case p.Address.IsUnspecified():
		return fmt.Errorf("diameter peer %q: address must be a specific address, not %s", p.ID, p.Address)
	case p.Port < 1 || p.Port > 65535:
		return fmt.Errorf("diameter peer %q: port %d is out of range", p.ID, p.Port)
	case p.Transport != TransportTCP && p.Transport != TransportSCTP:
		return fmt.Errorf("diameter peer %q: unknown transport %q, want tcp or sctp", p.ID, p.Transport)
	case len(p.Applications) == 0:
		return fmt.Errorf("diameter peer %q: at least one application is required", p.ID)
	}

	seen := make(map[Application]bool, len(p.Applications))

	for _, app := range p.Applications {
		switch {
		case app != ApplicationCx && app != ApplicationRx:
			return fmt.Errorf("diameter peer %q: unknown application %q, want cx or rx", p.ID, app)
		case seen[app]:
			return fmt.Errorf("diameter peer %q: application %s is listed twice", p.ID, app)
		}

		seen[app] = true
	}

	return nil
}
