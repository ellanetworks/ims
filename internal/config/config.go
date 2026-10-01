package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultAPIPort       = 5020
	defaultCallRetention = 90 * 24 * time.Hour
	defaultDiameterPort  = 3868
	defaultSIPPort       = 5060
)

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

	HomeDomain string `yaml:"home_domain"`
}

type SIP struct {
	Addresses      []netip.Addr `yaml:"addresses"`
	Port           int          `yaml:"port"`
	Aliases        []string     `yaml:"aliases"`
	MaxConnections int          `yaml:"max_connections"`
}

func HomeDomain(mcc, mnc string) string {
	if len(mnc) == 2 {
		mnc = "0" + mnc
	}

	return "ims.mnc" + mnc + ".mcc" + mcc + ".3gppnetwork.org"
}

func (c Config) SIPAliases() []string {
	return append([]string{c.IMS.HomeDomain}, c.SIP.Aliases...)
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

	if cfg.SIP.Port == 0 {
		cfg.SIP.Port = defaultSIPPort
	}

	for i, a := range cfg.SIP.Addresses {
		cfg.SIP.Addresses[i] = a.Unmap()
	}

	for i, a := range cfg.SIP.Aliases {
		cfg.SIP.Aliases[i] = strings.ToLower(a)
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

	return c.Diameter.validate()
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

	return nil
}

func (s SIP) validate(homeDomain string) error {
	switch {
	case len(s.Addresses) == 0:
		return errors.New("sip.addresses needs at least one address")
	case s.Port < 1 || s.Port > 65535:
		return fmt.Errorf("sip.port %d is out of range", s.Port)
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
	case rx == 0:
		return errors.New("at least one diameter peer must serve rx")
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
