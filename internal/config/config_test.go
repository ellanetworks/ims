package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
)

const (
	validDB  = "db:\n  path: ims.db\n"
	validAPI = "api:\n  address: 127.0.0.1\n"
	validIMS = "ims:\n  mcc: \"001\"\n  mnc: \"01\"\n"
	validSIP = "sip:\n  addresses: [10.0.0.5]\n"

	diameterIdentity = `diameter:
  origin_host: ims.ims.mnc001.mcc001.3gppnetwork.org
  origin_realm: ims.mnc001.mcc001.3gppnetwork.org
  address: 10.0.0.5
`

	hssPeer = `    - id: hss
      host: hss.ims.mnc001.mcc001.3gppnetwork.org
      realm: ims.mnc001.mcc001.3gppnetwork.org
      address: 10.0.0.10
      applications: [cx]
`

	pcrfPeer = `    - id: pcrf
      host: pcrf.epc.mnc001.mcc001.3gppnetwork.org
      realm: epc.mnc001.mcc001.3gppnetwork.org
      address: 10.0.0.11
      port: 3869
      transport: sctp
      applications: [rx]
`

	validDiameter = diameterIdentity + "  peers:\n" + hssPeer + pcrfPeer
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "ims.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoad(t *testing.T) {
	cfg, err := Load(writeConfig(t, validDB+"call_history:\n  retention: 24h\napi:\n  address: 127.0.0.1\n  port: 8080\n"+
		"ims:\n  mcc: \"310\"\n  mnc: \"410\"\n  trusted_networks: [192.0.2.0/24, \"::ffff:198.51.100.0/120\"]\n"+
		"sip:\n  addresses: [10.0.0.5, \"2001:db8::5\"]\n  aliases: [PCSCF.ims.mnc410.mcc310.3gppnetwork.org, scscf.example.org]\n  max_connections: 100\n"+
		"pcscf:\n  port: 5062\n  ipsec:\n    server_port: 5163\n    client_ports: [5164, 5165]\n    integrity: [hmac-md5-96]\n    encryption: preferred\n"+
		"icscf:\n  port: 5072\n"+
		"scscf:\n  port: 5082\n  name: sip:SCSCF.example.org:5082\n  capabilities: [1, 2]\n  min_expires: 120\n  max_expires: 7200\n"+
		validDiameter))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := Config{
		DB:          DB{Path: "ims.db"},
		CallHistory: CallHistory{Retention: 24 * time.Hour},
		API:         API{Address: netip.MustParseAddr("127.0.0.1"), Port: 8080},
		IMS: IMS{
			MCC:        "310",
			MNC:        "410",
			HomeDomain: "ims.mnc410.mcc310.3gppnetwork.org",
			TrustedNetworks: []netip.Prefix{
				netip.MustParsePrefix("192.0.2.0/24"),
				netip.MustParsePrefix("198.51.100.0/24"),
			},
		},
		SIP: SIP{
			Addresses:      []netip.Addr{netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("2001:db8::5")},
			Aliases:        []string{"pcscf.ims.mnc410.mcc310.3gppnetwork.org", "scscf.example.org"},
			MaxConnections: 100,
		},
		PCSCF: PCSCF{Port: 5062, IPsec: IPsec{
			ServerPort:  5163,
			ClientPorts: []int{5164, 5165},
			Integrity:   []ipsec.Integrity{ipsec.HMACMD596},
			Encryption:  ipsec.EncryptionPreferred,
		}},
		ICSCF: ICSCF{Port: 5072},
		SCSCF: SCSCF{
			Port:         5082,
			Name:         "sip:scscf.example.org:5082",
			Capabilities: []uint32{1, 2},
			MinExpires:   120,
			MaxExpires:   7200,
		},
		Diameter: Diameter{
			OriginHost:  "ims.ims.mnc001.mcc001.3gppnetwork.org",
			OriginRealm: "ims.mnc001.mcc001.3gppnetwork.org",
			Address:     netip.MustParseAddr("10.0.0.5"),
			Peers: []DiameterPeer{
				{
					ID:           "hss",
					Host:         "hss.ims.mnc001.mcc001.3gppnetwork.org",
					Realm:        "ims.mnc001.mcc001.3gppnetwork.org",
					Address:      netip.MustParseAddr("10.0.0.10"),
					Port:         3868,
					Transport:    TransportTCP,
					Applications: []Application{ApplicationCx},
				},
				{
					ID:           "pcrf",
					Host:         "pcrf.epc.mnc001.mcc001.3gppnetwork.org",
					Realm:        "epc.mnc001.mcc001.3gppnetwork.org",
					Address:      netip.MustParseAddr("10.0.0.11"),
					Port:         3869,
					Transport:    TransportSCTP,
					Applications: []Application{ApplicationRx},
				},
			},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("Load = %+v, want %+v", cfg, want)
	}

	wantAliases := []string{"ims.mnc410.mcc310.3gppnetwork.org", "pcscf.ims.mnc410.mcc310.3gppnetwork.org", "scscf.example.org"}
	if got := cfg.SIPAliases(); !reflect.DeepEqual(got, wantAliases) {
		t.Fatalf("SIPAliases = %v, want %v", got, wantAliases)
	}
}

func TestHomeDomain(t *testing.T) {
	tests := []struct {
		mcc, mnc, want string
	}{
		{"001", "01", "ims.mnc001.mcc001.3gppnetwork.org"},
		{"234", "15", "ims.mnc015.mcc234.3gppnetwork.org"},
		{"310", "410", "ims.mnc410.mcc310.3gppnetwork.org"},
	}

	for _, tt := range tests {
		cfg, err := Load(writeConfig(t, validDB+validAPI+"ims:\n  mcc: \""+tt.mcc+"\"\n  mnc: \""+tt.mnc+"\"\n"+validSIP+validDiameter))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		if cfg.IMS.HomeDomain != tt.want {
			t.Fatalf("home domain for %s/%s = %q, want %q", tt.mcc, tt.mnc, cfg.IMS.HomeDomain, tt.want)
		}
	}
}

func TestHomeDomainOverride(t *testing.T) {
	cfg, err := Load(writeConfig(t, validDB+validAPI+validIMS+"  home_domain: IMS.Example.org\n"+validSIP+validDiameter))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.IMS.HomeDomain != "ims.example.org" {
		t.Fatalf("home domain = %q, want ims.example.org", cfg.IMS.HomeDomain)
	}

	if got, want := cfg.SIPAliases(), []string{"ims.example.org", "scscf.ims.example.org"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SIPAliases = %v, want %v", got, want)
	}
}

func TestLoadIPAliases(t *testing.T) {
	cfg, err := Load(writeConfig(t, validDB+validAPI+validIMS+"sip:\n  addresses: [10.0.0.5]\n  aliases: [192.0.2.1, \"[2001:DB8::1]\", 2001:db8::2]\n"+validDiameter))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := []string{"192.0.2.1", "[2001:db8::1]", "2001:db8::2"}
	if !reflect.DeepEqual(cfg.SIP.Aliases, want) {
		t.Fatalf("aliases = %v, want %v", cfg.SIP.Aliases, want)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validDB+validAPI+validIMS+validSIP+validDiameter))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.PCSCF.Port != 5060 || cfg.ICSCF.Port != 5070 || cfg.SCSCF.Port != 5080 || cfg.SIP.MaxConnections != 0 {
		t.Fatalf("ports and max_connections = %d %d %d %d, want 5060 5070 5080 0",
			cfg.PCSCF.Port, cfg.ICSCF.Port, cfg.SCSCF.Port, cfg.SIP.MaxConnections)
	}

	wantIPsec := IPsec{
		ServerPort:  5063,
		ClientPorts: []int{5064, 5065},
		Integrity:   []ipsec.Integrity{ipsec.HMACSHA196, ipsec.HMACMD596},
		Encryption:  ipsec.EncryptionOff,
	}
	if !reflect.DeepEqual(cfg.PCSCF.IPsec, wantIPsec) {
		t.Fatalf("pcscf.ipsec = %+v, want %+v", cfg.PCSCF.IPsec, wantIPsec)
	}

	if cfg.API.Port != defaultAPIPort {
		t.Fatalf("api.port = %d, want %d", cfg.API.Port, defaultAPIPort)
	}

	if cfg.CallHistory.Retention != defaultCallRetention {
		t.Fatalf("call_history.retention = %v, want %v", cfg.CallHistory.Retention, defaultCallRetention)
	}

	if cfg.SCSCF.Name != "sip:scscf.ims.mnc001.mcc001.3gppnetwork.org:5080" {
		t.Fatalf("scscf.name = %q, want sip:scscf.<home domain>:<scscf port>", cfg.SCSCF.Name)
	}

	if cfg.SCSCF.MinExpires != defaultMinExpires || cfg.SCSCF.MaxExpires != defaultMaxExpires {
		t.Fatalf("scscf = %+v, want min %d and max %d", cfg.SCSCF, defaultMinExpires, defaultMaxExpires)
	}

	hss := cfg.Diameter.Peers[0]
	if cx := cfg.Diameter.CxPeer(); cx.ID != "hss" {
		t.Fatalf("CxPeer = %q, want hss", cx.ID)
	}

	if hss.Port != defaultDiameterPort || hss.Transport != TransportTCP {
		t.Fatalf("hss port and transport = %d %s, want %d %s", hss.Port, hss.Transport, defaultDiameterPort, TransportTCP)
	}
}

func TestLoadSCSCFNameOnTheHomeDomain(t *testing.T) {
	cfg, err := Load(writeConfig(t, validDB+validAPI+validIMS+validSIP+
		"scscf:\n  name: sip:IMS.mnc001.mcc001.3gppnetwork.org:5080\n"+validDiameter))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got, want := cfg.SIPAliases(), []string{"ims.mnc001.mcc001.3gppnetwork.org"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SIPAliases = %v, want %v", got, want)
	}
}

func TestLoadOnePeerServesCxAndRx(t *testing.T) {
	core := `    - id: core
      host: core.mnc001.mcc001.3gppnetwork.org
      realm: mnc001.mcc001.3gppnetwork.org
      address: 10.0.0.10
      applications: [cx, rx]
`

	cfg, err := Load(writeConfig(t, validDB+validAPI+validIMS+validSIP+diameterIdentity+"  peers:\n"+core))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if p := cfg.Diameter.Peers[0]; !p.Serves(ApplicationCx) || !p.Serves(ApplicationRx) {
		t.Fatalf("peer applications = %v, want cx and rx", p.Applications)
	}
}

func TestLoadRxPeer(t *testing.T) {
	cfg, err := Load(writeConfig(t, validDB+validAPI+validIMS+validSIP+validDiameter))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if p, ok := cfg.Diameter.RxPeer(); !ok || p.ID != "pcrf" {
		t.Fatalf("RxPeer = %+v, %v; want pcrf", p, ok)
	}

	cfg, err = Load(writeConfig(t, validDB+validAPI+validIMS+validSIP+diameterIdentity+"  peers:\n"+hssPeer))
	if err != nil {
		t.Fatalf("Load without an rx peer: %v", err)
	}

	if p, ok := cfg.Diameter.RxPeer(); ok {
		t.Fatalf("RxPeer = %+v, want none", p)
	}
}

func TestLoadInvalid(t *testing.T) {
	valid := validDB + validAPI + validIMS + validSIP

	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"missing db path", validAPI + validDiameter, "db.path is required"},
		{"negative retention", validDB + "call_history:\n  retention: -1h\n" + validAPI + validDiameter, "call_history.retention must be positive"},
		{"missing mcc", validDB + validAPI + "ims:\n  mnc: \"01\"\n" + validSIP + validDiameter, `ims.mcc "" must be 3 digits`},
		{"short mcc", validDB + validAPI + "ims:\n  mcc: \"01\"\n  mnc: \"01\"\n" + validSIP + validDiameter, `ims.mcc "01" must be 3 digits`},
		{"mcc not digits", validDB + validAPI + "ims:\n  mcc: \"0a1\"\n  mnc: \"01\"\n" + validSIP + validDiameter, `ims.mcc "0a1" must be 3 digits`},
		{"short mnc", validDB + validAPI + "ims:\n  mcc: \"001\"\n  mnc: \"1\"\n" + validSIP + validDiameter, `ims.mnc "1" must be 2 or 3 digits`},
		{"long mnc", validDB + validAPI + "ims:\n  mcc: \"001\"\n  mnc: \"0001\"\n" + validSIP + validDiameter, `ims.mnc "0001" must be 2 or 3 digits`},
		{"no sip addresses", validDB + validAPI + validIMS + validDiameter, "sip.addresses needs at least one address"},
		{"unspecified sip address", validDB + validAPI + validIMS + "sip:\n  addresses: [0.0.0.0]\n" + validDiameter, "sip.addresses: 0.0.0.0 must be a specific address"},
		{"unspecified sip ipv6 address", validDB + validAPI + validIMS + "sip:\n  addresses: [\"::\"]\n" + validDiameter, "sip.addresses: :: must be a specific address"},
		{"sip address with zone", validDB + validAPI + validIMS + "sip:\n  addresses: [\"fe80::1%eth0\"]\n" + validDiameter, "sip.addresses: fe80::1%eth0 must not have a zone"},
		{"duplicate sip address", validDB + validAPI + validIMS + "sip:\n  addresses: [10.0.0.5, \"::ffff:10.0.0.5\"]\n" + validDiameter, "sip.addresses: 10.0.0.5 is listed twice"},
		{"pcscf port out of range", valid + "pcscf:\n  port: 70000\n" + validDiameter, "pcscf.port 70000 is out of range"},
		{"icscf port out of range", valid + "icscf:\n  port: -1\n" + validDiameter, "icscf.port -1 is out of range"},
		{"same ports", valid + "pcscf:\n  port: 5080\n" + validDiameter, "pcscf.port and scscf.port are both 5080"},
		{"IPsec server port on a role port", valid + "pcscf:\n  ipsec:\n    server_port: 5070\n" + validDiameter, "icscf.port and pcscf.ipsec.server_port are both 5070"},
		{"IPsec client ports equal", valid + "pcscf:\n  ipsec:\n    client_ports: [5064, 5064]\n" + validDiameter, "pcscf.ipsec.client_ports[0] and pcscf.ipsec.client_ports[1] are both 5064"},
		{"IPsec client port on the server port", valid + "pcscf:\n  ipsec:\n    client_ports: [5063, 5064]\n" + validDiameter, "pcscf.ipsec.server_port and pcscf.ipsec.client_ports[0] are both 5063"},
		{"one IPsec client port", valid + "pcscf:\n  ipsec:\n    client_ports: [5064]\n" + validDiameter, "pcscf.ipsec.client_ports must list 2 ports"},
		{"IPsec on 5061", valid + "pcscf:\n  ipsec:\n    server_port: 5061\n" + validDiameter, "pcscf.ipsec.server_port 5061 is a standard SIP port"},
		{"IPsec port out of range", valid + "pcscf:\n  ipsec:\n    client_ports: [5064, 70000]\n" + validDiameter, "pcscf.ipsec.client_ports[1] 70000 is out of range"},
		{"unknown integrity", valid + "pcscf:\n  ipsec:\n    integrity: [hmac-sha2-256-128]\n" + validDiameter, `pcscf.ipsec: unsupported integrity algorithm "hmac-sha2-256-128"`},
		{"unknown encryption policy", valid + "pcscf:\n  ipsec:\n    encryption: always\n" + validDiameter, `pcscf.ipsec: unknown encryption policy "always"`},
		{"bad trusted network", validDB + validAPI + validIMS + "  trusted_networks: [10.0.0.0]\n" + validSIP + validDiameter, "no '/'"},
		{"negative max connections", validDB + validAPI + validIMS + "sip:\n  addresses: [10.0.0.5]\n  max_connections: -1\n" + validDiameter, "sip.max_connections -1 must not be negative"},
		{"negative min expires", valid + "scscf:\n  min_expires: -1\n" + validDiameter, "scscf.min_expires -1 must be positive"},
		{"max expires below min", valid + "scscf:\n  min_expires: 600\n  max_expires: 300\n" + validDiameter, "scscf.max_expires 300 is below scscf.min_expires 600"},
		{"negative reauth interval", valid + "scscf:\n  reauth_interval: -1m\n" + validDiameter, "scscf.reauth_interval -1m0s is negative"},
		{"negative reauth expires", valid + "scscf:\n  reauth_expires: -1m\n" + validDiameter, "scscf.reauth_expires -1m0s is negative"},
		{"S-CSCF name not a URI", valid + "scscf:\n  name: scscf.example.org\n" + validDiameter, `scscf.name "scscf.example.org" is not a SIP URI`},
		{"S-CSCF name with a user", valid + "scscf:\n  name: sip:s@scscf.example.org:5080\n" + validDiameter, "must have no user part"},
		{"S-CSCF name with parameters", valid + "scscf:\n  name: sip:scscf.example.org:5080;transport=tcp\n" + validDiameter, "must have no user part, parameters"},
		{"S-CSCF name on another port", valid + "scscf:\n  name: sip:scscf.example.org\n" + validDiameter, "must have the port of scscf.port 5080"},
		{"S-CSCF name not a domain name", valid + "scscf:\n  name: sip:scscf_1.example.org:5080\n" + validDiameter, `"scscf_1.example.org" is not a domain name`},
		{"S-CSCF name on another address", valid + "scscf:\n  name: sip:10.0.0.6:5080\n" + validDiameter, "10.0.0.6 is not one of sip.addresses"},
		{
			"S-CSCF name on another domain",
			valid + "scscf:\n  name: sip:scscf.example.org:5080\n" + validDiameter,
			"scscf.example.org must be the home domain, scscf.ims.mnc001.mcc001.3gppnetwork.org or one of sip.aliases",
		},
		{
			"role port on the API's address",
			validDB + "api:\n  address: 10.0.0.5\n  port: 5060\n" + validIMS + validSIP + validDiameter,
			"pcscf.port and api.port are both 5060 on 10.0.0.5",
		},
		{
			"role port on the API's wildcard address",
			validDB + "api:\n  address: 0.0.0.0\n  port: 5070\n" + validIMS + validSIP + validDiameter,
			"icscf.port and api.port are both 5070 on 0.0.0.0",
		},
		{"trusting every IPv4 address", validDB + validAPI + validIMS + "  trusted_networks: [0.0.0.0/0]\n" + validSIP + validDiameter, "0.0.0.0/0 would trust every address"},
		{"trusting every address", validDB + validAPI + validIMS + "  trusted_networks: [\"::/0\"]\n" + validSIP + validDiameter, "::/0 would trust every address"},
		{
			"short IPv4-mapped network",
			validDB + validAPI + validIMS + "  trusted_networks: [\"::ffff:10.0.0.0/64\"]\n" + validSIP + validDiameter,
			"::ffff:10.0.0.0/64 is IPv4-mapped and must be /96 or longer",
		},
		{"home domain with a space", validDB + validAPI + validIMS + "  home_domain: ims example.org\n" + validSIP + validDiameter, `ims.home_domain "ims example.org" is not a domain name`},
		{"home domain with an empty label", validDB + validAPI + validIMS + "  home_domain: ims..example.org\n" + validSIP + validDiameter, `ims.home_domain "ims..example.org" is not a domain name`},
		{"home domain label starts with a hyphen", validDB + validAPI + validIMS + "  home_domain: -ims.example.org\n" + validSIP + validDiameter, `ims.home_domain "-ims.example.org" is not a domain name`},
		{"home domain label too long", validDB + validAPI + validIMS + "  home_domain: " + strings.Repeat("a", 64) + ".org\n" + validSIP + validDiameter, "is not a domain name"},
		{"alias not a domain name", validDB + validAPI + validIMS + "sip:\n  addresses: [10.0.0.5]\n  aliases: [pcscf_1.example.org]\n" + validDiameter, `sip.aliases: "pcscf_1.example.org" is neither a domain name nor an IP address`},
		{"empty alias", validDB + validAPI + validIMS + "sip:\n  addresses: [10.0.0.5]\n  aliases: [\"\"]\n" + validDiameter, "sip.aliases: an alias is empty"},
		{"duplicate alias", validDB + validAPI + validIMS + "sip:\n  addresses: [10.0.0.5]\n  aliases: [pcscf.example.org, PCSCF.example.org]\n" + validDiameter, "sip.aliases: pcscf.example.org is listed twice"},
		{"alias is the home domain", validDB + validAPI + validIMS + "sip:\n  addresses: [10.0.0.5]\n  aliases: [ims.mnc001.mcc001.3gppnetwork.org]\n" + validDiameter, "is listed twice or is the home domain"},
		{"missing api address", validDB + "api:\n  port: 8080\n" + validDiameter, "api.address is required"},
		{"port out of range", validDB + "api:\n  address: 127.0.0.1\n  port: 70000\n" + validDiameter, "api.port 70000 is out of range"},
		{"unknown field", valid + validDiameter + "foo: bar\n", "field foo not found"},
		{"missing diameter", valid, "diameter.origin_host is required"},
		{
			"missing origin realm",
			valid + strings.Replace(validDiameter, "  origin_realm: ims.mnc001.mcc001.3gppnetwork.org\n", "", 1),
			"diameter.origin_realm is required",
		},
		{
			"missing diameter address",
			valid + strings.Replace(validDiameter, "  address: 10.0.0.5\n", "", 1),
			"diameter.address is required",
		},
		{
			"unspecified diameter address",
			valid + strings.Replace(validDiameter, "address: 10.0.0.5", "address: 0.0.0.0", 1),
			"diameter.address must be a specific address",
		},
		{"no peers", valid + diameterIdentity, "exactly one diameter peer must serve cx, found 0"},
		{"no cx peer", valid + diameterIdentity + "  peers:\n" + pcrfPeer, "exactly one diameter peer must serve cx, found 0"},
		{
			"two cx peers",
			valid + diameterIdentity + "  peers:\n" + hssPeer + pcrfPeer +
				strings.NewReplacer("id: hss", "id: hss2", "hss.ims", "hss2.ims").Replace(hssPeer),
			"exactly one diameter peer must serve cx, found 2",
		},
		{
			"two rx peers",
			valid + diameterIdentity + "  peers:\n" + hssPeer + pcrfPeer +
				strings.NewReplacer("id: pcrf", "id: pcrf2", "pcrf.epc", "pcrf2.epc").Replace(pcrfPeer),
			"at most one diameter peer may serve rx, found 2",
		},
		{
			"duplicate peer id",
			valid + diameterIdentity + "  peers:\n" + hssPeer + strings.Replace(pcrfPeer, "id: pcrf", "id: hss", 1),
			`diameter peer "hss" is defined twice`,
		},
		{
			"missing peer id",
			valid + diameterIdentity + "  peers:\n" + hssPeer + strings.Replace(pcrfPeer, "- id: pcrf\n      ", "- ", 1),
			"every diameter peer needs an id",
		},
		{
			"missing peer host",
			valid + diameterIdentity + "  peers:\n" + hssPeer + strings.Replace(pcrfPeer, "      host: pcrf.epc.mnc001.mcc001.3gppnetwork.org\n", "", 1),
			`diameter peer "pcrf": host is required`,
		},
		{
			"missing peer realm",
			valid + diameterIdentity + "  peers:\n" + hssPeer + strings.Replace(pcrfPeer, "      realm: epc.mnc001.mcc001.3gppnetwork.org\n", "", 1),
			`diameter peer "pcrf": realm is required`,
		},
		{
			"missing peer address",
			valid + diameterIdentity + "  peers:\n" + hssPeer + strings.Replace(pcrfPeer, "      address: 10.0.0.11\n", "", 1),
			`diameter peer "pcrf": address is required`,
		},
		{
			"unspecified peer address",
			valid + diameterIdentity + "  peers:\n" + hssPeer + strings.Replace(pcrfPeer, "address: 10.0.0.11", "address: '::'", 1),
			`diameter peer "pcrf": address must be a specific address`,
		},
		{
			"peer port out of range",
			valid + diameterIdentity + "  peers:\n" + hssPeer + strings.Replace(pcrfPeer, "port: 3869", "port: 70000", 1),
			`diameter peer "pcrf": port 70000 is out of range`,
		},
		{
			"unknown transport",
			valid + diameterIdentity + "  peers:\n" + hssPeer + strings.Replace(pcrfPeer, "transport: sctp", "transport: udp", 1),
			`diameter peer "pcrf": unknown transport "udp"`,
		},
		{
			"no applications",
			valid + diameterIdentity + "  peers:\n" + hssPeer + strings.Replace(pcrfPeer, "applications: [rx]", "applications: []", 1),
			`diameter peer "pcrf": at least one application is required`,
		},
		{
			"unknown application",
			valid + diameterIdentity + "  peers:\n" + hssPeer + strings.Replace(pcrfPeer, "applications: [rx]", "applications: [rx, gx]", 1),
			`diameter peer "pcrf": unknown application "gx"`,
		},
		{
			"duplicate application",
			valid + diameterIdentity + "  peers:\n" + hssPeer + strings.Replace(pcrfPeer, "applications: [rx]", "applications: [rx, rx]", 1),
			`diameter peer "pcrf": application rx is listed twice`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.content))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("Load succeeded for a missing file")
	}
}
