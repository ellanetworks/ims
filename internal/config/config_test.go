package config

import (
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	validDB       = "db:\n  path: ims.db\n"
	validAPI      = "api:\n  address: 127.0.0.1\n"
	validSIP      = "sip:\n  addresses: [10.0.0.5]\n"
	validDiameter = "diameter:\n  address: 10.0.0.5\n"

	n5 = `n5:
  notify:
    address: 10.0.0.5
    port: 7778
`

	n5TLS = n5 + `  tls:
    ca: /etc/ims/tls/ca.crt
    cert: /etc/ims/tls/ims.crt
    key: /etc/ims/tls/ims.key
`
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
	cfg, err := Load(writeConfig(t, "logging:\n  level: DEBUG\n"+
		validDB+"api:\n  address: 127.0.0.1\n  port: 8080\n"+
		"sip:\n  addresses: [10.0.0.5, \"2001:db8::5\"]\n"+
		"pcscf:\n  port: 5062\n  ipsec:\n    server_port: 5163\n    client_ports: [5164, 5165]\n"+
		"icscf:\n  port: 5072\n"+
		"scscf:\n  port: 5082\n"+
		"diameter:\n  address: 10.0.0.5\n  port: 3869\n"+n5TLS))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := Config{
		Logging:  Logging{Level: slog.LevelDebug},
		DB:       DB{Path: "ims.db"},
		API:      API{Address: netip.MustParseAddr("127.0.0.1"), Port: 8080},
		SIP:      SIP{Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("2001:db8::5")}},
		PCSCF:    PCSCF{Port: 5062, IPsec: IPsec{ServerPort: 5163, ClientPorts: []int{5164, 5165}}},
		ICSCF:    ICSCF{Port: 5072},
		SCSCF:    SCSCF{Port: 5082},
		Diameter: Diameter{Address: netip.MustParseAddr("10.0.0.5"), Port: 3869},
		N5: &N5{
			Notify: N5Notify{Address: netip.MustParseAddr("10.0.0.5"), Port: 7778},
			TLS:    &TLS{CA: "/etc/ims/tls/ca.crt", Cert: "/etc/ims/tls/ims.crt", Key: "/etc/ims/tls/ims.key"},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("Load = %+v, want %+v", cfg, want)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validDB+validAPI+validSIP+validDiameter))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.PCSCF.Port != 5060 || cfg.ICSCF.Port != 5070 || cfg.SCSCF.Port != 5080 {
		t.Fatalf("ports = %d %d %d, want 5060 5070 5080", cfg.PCSCF.Port, cfg.ICSCF.Port, cfg.SCSCF.Port)
	}

	wantIPsec := IPsec{ServerPort: 5063, ClientPorts: []int{5064, 5065}}
	if !reflect.DeepEqual(cfg.PCSCF.IPsec, wantIPsec) {
		t.Fatalf("pcscf.ipsec = %+v, want %+v", cfg.PCSCF.IPsec, wantIPsec)
	}

	if cfg.API.Port != defaultAPIPort {
		t.Fatalf("api.port = %d, want %d", cfg.API.Port, defaultAPIPort)
	}

	if cfg.Diameter.Port != defaultDiameterPort {
		t.Fatalf("diameter.port = %d, want %d", cfg.Diameter.Port, defaultDiameterPort)
	}

	if cfg.N5 != nil || cfg.N5NotifyURI() != "" {
		t.Fatalf("n5 = %+v, want none", cfg.N5)
	}
}

func TestN5NotifyURI(t *testing.T) {
	for _, tt := range []struct {
		name, content, want string
	}{
		{"http", n5, "http://10.0.0.5:7778"},
		{"https", n5TLS, "https://10.0.0.5:7778"},
		{"IPv6", strings.Replace(n5, "address: 10.0.0.5", `address: "2001:db8::5"`, 1), "http://[2001:db8::5]:7778"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, validDB+validAPI+validSIP+validDiameter+tt.content))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			if got := cfg.N5NotifyURI(); got != tt.want {
				t.Fatalf("notify URI = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadInvalid(t *testing.T) {
	valid := validDB + validAPI + validSIP

	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"missing db path", validAPI + validDiameter, "db.path is required"},
		{"unknown log level", "logging:\n  level: trace\n" + valid + validDiameter, `level string "trace": unknown name`},
		{"no sip addresses", validDB + validAPI + validDiameter, "sip.addresses needs at least one address"},
		{"unspecified sip address", validDB + validAPI + "sip:\n  addresses: [0.0.0.0]\n" + validDiameter, "sip.addresses: 0.0.0.0 must be a specific address"},
		{"unspecified sip ipv6 address", validDB + validAPI + "sip:\n  addresses: [\"::\"]\n" + validDiameter, "sip.addresses: :: must be a specific address"},
		{"sip address with zone", validDB + validAPI + "sip:\n  addresses: [\"fe80::1%eth0\"]\n" + validDiameter, "sip.addresses: fe80::1%eth0 must not have a zone"},
		{"duplicate sip address", validDB + validAPI + "sip:\n  addresses: [10.0.0.5, \"::ffff:10.0.0.5\"]\n" + validDiameter, "sip.addresses: 10.0.0.5 is listed twice"},
		{"pcscf port out of range", valid + "pcscf:\n  port: 70000\n" + validDiameter, "pcscf.port 70000 is out of range"},
		{"icscf port out of range", valid + "icscf:\n  port: -1\n" + validDiameter, "icscf.port -1 is out of range"},
		{"same ports", valid + "pcscf:\n  port: 5080\n" + validDiameter, "pcscf.port and scscf.port are both 5080"},
		{"IPsec server port on a role port", valid + "pcscf:\n  ipsec:\n    server_port: 5070\n" + validDiameter, "icscf.port and pcscf.ipsec.server_port are both 5070"},
		{"IPsec client ports equal", valid + "pcscf:\n  ipsec:\n    client_ports: [5064, 5064]\n" + validDiameter, "pcscf.ipsec.client_ports[0] and pcscf.ipsec.client_ports[1] are both 5064"},
		{"IPsec client port on the server port", valid + "pcscf:\n  ipsec:\n    client_ports: [5063, 5064]\n" + validDiameter, "pcscf.ipsec.server_port and pcscf.ipsec.client_ports[0] are both 5063"},
		{"one IPsec client port", valid + "pcscf:\n  ipsec:\n    client_ports: [5064]\n" + validDiameter, "pcscf.ipsec.client_ports must list 2 ports"},
		{"IPsec on 5061", valid + "pcscf:\n  ipsec:\n    server_port: 5061\n" + validDiameter, "pcscf.ipsec.server_port 5061 is a standard SIP port"},
		{"IPsec port out of range", valid + "pcscf:\n  ipsec:\n    client_ports: [5064, 70000]\n" + validDiameter, "pcscf.ipsec.client_ports[1] 70000 is out of range"},
		{
			"role port on the API's address",
			validDB + "api:\n  address: 10.0.0.5\n  port: 5060\n" + validSIP + validDiameter,
			"pcscf.port and api.port are both 5060 on 10.0.0.5",
		},
		{
			"role port on the API's wildcard address",
			validDB + "api:\n  address: 0.0.0.0\n  port: 5070\n" + validSIP + validDiameter,
			"icscf.port and api.port are both 5070 on 0.0.0.0",
		},
		{"missing api address", validDB + "api:\n  port: 8080\n" + validDiameter, "api.address is required"},
		{"port out of range", validDB + "api:\n  address: 127.0.0.1\n  port: 70000\n" + validDiameter, "api.port 70000 is out of range"},
		{"unknown field", valid + validDiameter + "foo: bar\n", "field foo not found"},
		{"a setting of the database", valid + "scscf:\n  name: sip:scscf.example.org:5080\n" + validDiameter, "field name not found"},
		{"the operator", valid + validDiameter + "ims:\n  mcc: \"001\"\n", "field ims not found"},
		{"the peers", valid + validDiameter + "  peers: []\n", "field peers not found"},
		{"the policy", valid + "pcscf:\n  policy:\n    rx: pcrf\n" + validDiameter, "field policy not found"},
		{"missing diameter", valid, "diameter.address is required"},
		{"unspecified diameter address", valid + "diameter:\n  address: 0.0.0.0\n", "diameter.address must be a specific address"},
		{"diameter port out of range", valid + "diameter:\n  address: 10.0.0.5\n  port: 70000\n", "diameter.port 70000 is out of range"},
		{"n5 tls without ca", valid + validDiameter + strings.Replace(n5TLS, "    ca: /etc/ims/tls/ca.crt\n", "", 1), "n5.tls.ca is required"},
		{"n5 tls without cert", valid + validDiameter + strings.Replace(n5TLS, "    cert: /etc/ims/tls/ims.crt\n", "", 1), "n5.tls.cert is required"},
		{"n5 tls without key", valid + validDiameter + strings.Replace(n5TLS, "    key: /etc/ims/tls/ims.key\n", "", 1), "n5.tls.key is required"},
		{"n5 without notify address", valid + validDiameter + strings.Replace(n5, "    address: 10.0.0.5\n", "", 1), "n5.notify.address is required"},
		{"n5 unspecified notify address", valid + validDiameter + strings.Replace(n5, "address: 10.0.0.5", "address: 0.0.0.0", 1), "must be a specific address"},
		{"n5 notify port out of range", valid + validDiameter + strings.Replace(n5, "port: 7778", "port: 0", 1), "n5.notify.port 0 is out of range"},
		{"n5 notify port on SIP", valid + validDiameter + strings.Replace(n5, "port: 7778", "port: 5060", 1), "n5.notify.port and pcscf.port are both 5060 on 10.0.0.5"},
		{"n5 notify port on Diameter", valid + validDiameter + strings.Replace(n5, "port: 7778", "port: 3868", 1), "n5.notify.port and diameter.port are both 3868"},
		{
			"n5 notify port on the API",
			validDB + "api:\n  address: 0.0.0.0\n  port: 7778\n" + validSIP + validDiameter + n5,
			"n5.notify.port and api.port are both 7778",
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
