package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	validDB  = "db:\n  path: ims.db\n"
	validAPI = "api:\n  address: 127.0.0.1\n"

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
	cfg, err := Load(writeConfig(t, validDB+"call_history:\n  retention: 24h\napi:\n  address: 127.0.0.1\n  port: 8080\n"+validDiameter))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := Config{
		DB:          DB{Path: "ims.db"},
		CallHistory: CallHistory{Retention: 24 * time.Hour},
		API:         API{Address: netip.MustParseAddr("127.0.0.1"), Port: 8080},
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
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validDB+validAPI+validDiameter))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.API.Port != defaultAPIPort {
		t.Fatalf("api.port = %d, want %d", cfg.API.Port, defaultAPIPort)
	}

	if cfg.CallHistory.Retention != defaultCallRetention {
		t.Fatalf("call_history.retention = %v, want %v", cfg.CallHistory.Retention, defaultCallRetention)
	}

	hss := cfg.Diameter.Peers[0]
	if hss.Port != defaultDiameterPort || hss.Transport != TransportTCP {
		t.Fatalf("hss port and transport = %d %s, want %d %s", hss.Port, hss.Transport, defaultDiameterPort, TransportTCP)
	}
}

func TestLoadOnePeerServesCxAndRx(t *testing.T) {
	core := `    - id: core
      host: core.mnc001.mcc001.3gppnetwork.org
      realm: mnc001.mcc001.3gppnetwork.org
      address: 10.0.0.10
      applications: [cx, rx]
`

	cfg, err := Load(writeConfig(t, validDB+validAPI+diameterIdentity+"  peers:\n"+core))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if p := cfg.Diameter.Peers[0]; !p.Serves(ApplicationCx) || !p.Serves(ApplicationRx) {
		t.Fatalf("peer applications = %v, want cx and rx", p.Applications)
	}
}

func TestLoadInvalid(t *testing.T) {
	valid := validDB + validAPI

	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"missing db path", validAPI + validDiameter, "db.path is required"},
		{"negative retention", validDB + "call_history:\n  retention: -1h\n" + validAPI + validDiameter, "call_history.retention must be positive"},
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
		{"no rx peer", valid + diameterIdentity + "  peers:\n" + hssPeer, "at least one diameter peer must serve rx"},
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
