package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
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
	defaultAPIPort      = 5020
	defaultDiameterPort = 3868
	defaultPCSCFPort    = 5060
	defaultICSCFPort    = 5070
	defaultSCSCFPort    = 5080
	defaultMinExpires   = 60
	defaultMaxExpires   = 3600
	defaultIPsecServer  = 5063
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
	Logging  Logging  `yaml:"logging"`
	DB       DB       `yaml:"db"`
	API      API      `yaml:"api"`
	IMS      IMS      `yaml:"ims"`
	SIP      SIP      `yaml:"sip"`
	PCSCF    PCSCF    `yaml:"pcscf"`
	ICSCF    ICSCF    `yaml:"icscf"`
	SCSCF    SCSCF    `yaml:"scscf"`
	Diameter Diameter `yaml:"diameter"`
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
}

type IMS struct {
	MCC string `yaml:"mcc"`
	MNC string `yaml:"mnc"`

	HomeDomain      string         `yaml:"home_domain"`
	TrustedNetworks []netip.Prefix `yaml:"trusted_networks"`

	Numbering Numbering `yaml:"numbering"`
}

type Numbering struct {
	CountryCode         string `yaml:"country_code"`
	NationalPrefix      string `yaml:"national_prefix"`
	InternationalPrefix string `yaml:"international_prefix"`
}

type SIP struct {
	Addresses      []netip.Addr `yaml:"addresses"`
	Aliases        []string     `yaml:"aliases"`
	MaxConnections int          `yaml:"max_connections"`
}

type PCSCF struct {
	Port  int   `yaml:"port"`
	IPsec IPsec `yaml:"ipsec"`

	NoAnswerTimeout time.Duration `yaml:"no_answer_timeout"`

	MediaLossTimeout time.Duration `yaml:"media_loss_timeout"`

	Policy Policy `yaml:"policy"`
}

// Policy names the policy function of the P-CSCF: a PCRF over Rx, or a PCF over N5. Without one, the P-CSCF opens
// no policy sessions.
type Policy struct {
	// Rx is the ID of the diameter peer that serves rx.
	Rx string `yaml:"rx"`
	N5 *N5    `yaml:"n5"`
}

type N5 struct {
	// PCFURI is the API root of the PCF, http://host[:port][/prefix], or https://host[:port][/prefix] for N5 over
	// TLS. The host is a domain name or an IP address.
	PCFURI string   `yaml:"pcf_uri"`
	Notify N5Notify `yaml:"notify"`
	// TLS is required over https, and not allowed over http.
	TLS *TLS `yaml:"tls"`
}

// N5Notify is where the PCF sends notifications: the P-CSCF listens on the address and port, and the PCF
// connects to the URI.
type N5Notify struct {
	// URI is the root of the notification URIs, scheme://host[:port] with the scheme of the PCF URI. Over https,
	// the certificate must be valid for its host. It defaults to the address and port.
	URI     string     `yaml:"uri"`
	Address netip.Addr `yaml:"address"`
	Port    int        `yaml:"port"`
}

// NotifyURI is the root of the notification URIs.
func (n N5) NotifyURI() string {
	if n.Notify.URI != "" {
		return strings.TrimSuffix(n.Notify.URI, "/")
	}

	scheme := "http"
	if n.TLS != nil {
		scheme = "https"
	}

	return scheme + "://" + netip.AddrPortFrom(n.Notify.Address, uint16(n.Notify.Port)).String()
}

// TLS are PEM files: the CA certificates that the peers' certificates must chain to, and the certificate and
// private key of the IMS, which it presents both as a client and as a server.
type TLS struct {
	CA   string `yaml:"ca"`
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
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
	Port        int            `yaml:"port"`
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

// RxPeer returns the diameter peer of the Rx policy function, if there is one.
func (c Config) RxPeer() (DiameterPeer, bool) {
	if c.PCSCF.Policy.Rx == "" {
		return DiameterPeer{}, false
	}

	for _, p := range c.Diameter.Peers {
		if p.ID == c.PCSCF.Policy.Rx {
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

	if cfg.Diameter.Port == 0 {
		cfg.Diameter.Port = defaultDiameterPort
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

	if c.PCSCF.NoAnswerTimeout < 0 {
		return fmt.Errorf("pcscf.no_answer_timeout %s is negative", c.PCSCF.NoAnswerTimeout)
	}

	if c.PCSCF.MediaLossTimeout < 0 {
		return fmt.Errorf("pcscf.media_loss_timeout %s is negative", c.PCSCF.MediaLossTimeout)
	}

	if err := c.PCSCF.IPsec.Policy().Validate(); err != nil {
		return fmt.Errorf("pcscf.ipsec: %w", err)
	}

	if err := c.Diameter.validate(); err != nil {
		return err
	}

	return c.validatePolicy()
}

func (c Config) validatePolicy() error {
	pol := c.PCSCF.Policy

	if pol.Rx != "" && pol.N5 != nil {
		return errors.New("pcscf.policy: set rx or n5, not both")
	}

	for _, p := range c.Diameter.Peers {
		if p.Serves(ApplicationRx) && p.ID != pol.Rx {
			return fmt.Errorf("diameter peer %q serves rx, but pcscf.policy.rx does not name it", p.ID)
		}
	}

	if pol.Rx != "" {
		if p, ok := c.RxPeer(); !ok || !p.Serves(ApplicationRx) {
			return fmt.Errorf("pcscf.policy.rx %q is not a diameter peer that serves rx", pol.Rx)
		}
	}

	if pol.N5 == nil {
		return nil
	}

	return c.validateN5(*pol.N5)
}

func (c Config) validateN5(n N5) error {
	u, err := url.Parse(n.PCFURI)

	switch {
	case n.PCFURI == "":
		return errors.New("pcscf.policy.n5.pcf_uri is required")
	case err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "":
		return fmt.Errorf("pcscf.policy.n5.pcf_uri %q: want http[s]://host[:port][/prefix]", n.PCFURI)
	case u.Scheme == "https" && n.TLS == nil:
		return errors.New("pcscf.policy.n5.tls is required with an https pcf_uri")
	case u.Scheme == "http" && n.TLS != nil:
		return errors.New("pcscf.policy.n5.tls needs an https pcf_uri")
	}

	if n.TLS != nil {
		switch {
		case n.TLS.CA == "":
			return errors.New("pcscf.policy.n5.tls.ca is required")
		case n.TLS.Cert == "":
			return errors.New("pcscf.policy.n5.tls.cert is required")
		case n.TLS.Key == "":
			return errors.New("pcscf.policy.n5.tls.key is required")
		}
	}

	if n.Notify.URI != "" {
		nu, err := url.Parse(n.Notify.URI)
		if err != nil || nu.Scheme != u.Scheme || nu.Host == "" || nu.Hostname() == "" || nu.User != nil ||
			strings.Trim(nu.Path, "/") != "" || nu.RawQuery != "" || nu.Fragment != "" {
			return fmt.Errorf("pcscf.policy.n5.notify.uri %q: want %s://host[:port], with the scheme of pcf_uri", n.Notify.URI, u.Scheme)
		}
	}

	a := n.Notify.Address

	switch {
	case !a.IsValid():
		return errors.New("pcscf.policy.n5.notify.address is required")
	case a.IsUnspecified():
		return fmt.Errorf("pcscf.policy.n5.notify.address must be a specific address, not %s, since the PCF sends to it", a)
	case a.Zone() != "" || a.Is4In6():
		return fmt.Errorf("pcscf.policy.n5.notify.address %s must be a plain IPv4 or IPv6 address", a)
	case n.Notify.Port < 1 || n.Notify.Port > 65535:
		return fmt.Errorf("pcscf.policy.n5.notify.port %d is out of range", n.Notify.Port)
	}

	// TCP ports on the same address: the API, SIP over TCP, and Diameter.
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

	for _, t := range taken {
		if t.port == n.Notify.Port && slices.ContainsFunc(t.addr, func(b netip.Addr) bool { return overlaps(a, b) }) {
			return fmt.Errorf("pcscf.policy.n5.notify.port and %s are both %d on %s", t.name, t.port, a)
		}
	}

	return nil
}

// overlaps reports whether a listener on b takes a's port, b being a specific or an unspecified address.
func overlaps(a, b netip.Addr) bool {
	switch {
	case b == netip.IPv6Unspecified():
		return true
	case b == netip.IPv4Unspecified():
		return a.Is4()
	}

	return a == b
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

	if err := i.Numbering.validate(); err != nil {
		return err
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

func (n Numbering) validate() error {
	switch {
	case n.CountryCode != "" && (len(n.CountryCode) > 3 || !isDigits(n.CountryCode) || n.CountryCode[0] == '0'):
		return fmt.Errorf("ims.numbering.country_code %q must be 1 to 3 digits, not starting with 0", n.CountryCode)
	case (n.NationalPrefix != "" || n.InternationalPrefix != "") && n.CountryCode == "":
		return errors.New("ims.numbering prefixes need ims.numbering.country_code")
	case n.NationalPrefix != "" && (len(n.NationalPrefix) > 4 || !isDigits(n.NationalPrefix)):
		return fmt.Errorf("ims.numbering.national_prefix %q must be 1 to 4 digits", n.NationalPrefix)
	case n.InternationalPrefix != "" && (len(n.InternationalPrefix) > 4 || !isDigits(n.InternationalPrefix)):
		return fmt.Errorf("ims.numbering.international_prefix %q must be 1 to 4 digits", n.InternationalPrefix)
	case n.InternationalPrefix != "" && n.InternationalPrefix == n.NationalPrefix:
		return errors.New("ims.numbering.international_prefix and national_prefix must differ")
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
	case d.Port < 1 || d.Port > 65535:
		return fmt.Errorf("diameter.port %d is out of range", d.Port)
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
