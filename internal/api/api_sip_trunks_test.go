package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
)

type fakeSIPTrunks map[string]settings.TrunkStatus

func (f fakeSIPTrunks) TrunkStatus(id string) settings.TrunkStatus { return f[id] }

const telnyxTrunk = `{"name": "Telnyx", "addresses": [{"host": "sip.telnyx.com"}],
	"inbound_cidrs": ["192.76.120.10/32", "64.16.250.10"], "domain": "sip.telnyx.com", "trusted": true, "enabled": true}`

const sbcTrunk = `{"name": "SBC", "addresses": [{"host": "::ffff:10.0.0.5", "port": 5080, "transport": "udp"}],
	"inbound_cidrs": ["10.0.0.0/24"], "domain": "sbc.example.org", "keepalive_interval": 60, "enabled": false}`

func createTrunk(t *testing.T, cfg Config, body string) SIPTrunk {
	t.Helper()

	code, resp := serve(t, cfg, http.MethodPost, "/api/v1/sip-trunks", body)
	if code != http.StatusCreated {
		t.Fatalf("POST trunk = %d, want 201: %s", code, resp)
	}

	return decodeResult[SIPTrunk](t, resp)
}

func createSIPRoute(t *testing.T, cfg Config, body string) SIPRoute {
	t.Helper()

	code, resp := serve(t, cfg, http.MethodPost, "/api/v1/sip-routes", body)
	if code != http.StatusCreated {
		t.Fatalf("POST route = %d, want 201: %s", code, resp)
	}

	return decodeResult[SIPRoute](t, resp)
}

func TestSIPTrunks(t *testing.T) {
	cfg := Config{Settings: newFakeSettings()}

	telnyx := createTrunk(t, cfg, telnyxTrunk)

	want := SIPTrunk{
		ID: telnyx.ID,
		SIPTrunkParams: SIPTrunkParams{
			Name:              "Telnyx",
			Addresses:         []SIPTrunkAddress{{Host: "sip.telnyx.com"}},
			InboundCIDRs:      []string{"192.76.120.10/32", "64.16.250.10/32"},
			Domain:            "sip.telnyx.com",
			Trusted:           true,
			KeepaliveInterval: new(30),
			Enabled:           new(true),
		},
		TLS: SIPTrunkTLS{
			SIPTrunkTLSParams:    SIPTrunkTLSParams{Pins: []string{}, Identities: []string{}},
			CACertificateDetails: []CertificateSummary{},
		},
		Effective: SIPTrunkEffective{
			Domain: "sip.telnyx.com", InboundCIDRs: []string{"192.76.120.10/32", "64.16.250.10/32"}, TLSIdentities: []string{},
		},
		Status: SIPTrunkStatus{State: settings.TrunkUnknown},
	}
	if telnyx.ID == "" || !reflect.DeepEqual(telnyx, want) {
		t.Fatalf("created = %+v, want %+v with an ID", telnyx, want)
	}

	sbc := createTrunk(t, cfg, sbcTrunk)
	if sbc.Addresses[0].Host != "10.0.0.5" || *sbc.Enabled || sbc.Trusted || *sbc.KeepaliveInterval != 60 {
		t.Fatalf("created = %+v, want an unmapped address, disabled, untrusted, every minute", sbc)
	}

	code, body := serve(t, cfg, http.MethodGet, "/api/v1/sip-trunks", "")
	if code != http.StatusOK {
		t.Fatalf("GET trunks = %d", code)
	}

	if got := decodeResult[SIPTrunkList](t, body); len(got.Items) != 2 || got.Items[0].ID != telnyx.ID || got.Items[1].ID != sbc.ID {
		t.Fatalf("trunks = %+v, want Telnyx then the SBC", got)
	}

	code, body = serve(t, cfg, http.MethodGet, "/api/v1/sip-trunks/"+sbc.ID, "")
	if code != http.StatusOK || !reflect.DeepEqual(decodeResult[SIPTrunk](t, body), sbc) {
		t.Fatalf("GET trunk = %d %s, want %+v", code, body, sbc)
	}

	enabled := strings.Replace(sbcTrunk, `"enabled": false`, `"enabled": true`, 1)

	code, body = serve(t, cfg, http.MethodPut, "/api/v1/sip-trunks/"+sbc.ID, enabled)
	if got := decodeResult[SIPTrunk](t, body); code != http.StatusOK || got.ID != sbc.ID || !*got.Enabled {
		t.Fatalf("PUT trunk = %d %s, want it enabled", code, body)
	}

	code, body = serve(t, cfg, http.MethodDelete, "/api/v1/sip-trunks/"+sbc.ID, "")
	if code != http.StatusOK || decodeResult[Message](t, body).Message != "SIP trunk deleted" {
		t.Fatalf("DELETE trunk = %d %s", code, body)
	}

	if code, _ := serve(t, cfg, http.MethodGet, "/api/v1/sip-trunks/"+sbc.ID, ""); code != http.StatusNotFound {
		t.Fatalf("GET deleted trunk = %d, want 404", code)
	}
}

func TestSIPTrunkDefaults(t *testing.T) {
	cfg := Config{Settings: newFakeSettings()}

	body := `{"name": "Carrier", "addresses": [{"host": "sip.carrier.example", "transport": "tls"},
		{"host": "192.0.2.10", "transport": "tls"}, {"host": "2001:db8::10", "transport": "tls"}], "trusted": true,
		"tls": {"pins": ["` + strings.Repeat("ab", 32) + `"]}}`

	got := createTrunk(t, cfg, body)

	if !*got.Enabled || got.Domain != "" || len(got.InboundCIDRs) != 0 || len(got.TLS.Identities) != 0 ||
		!reflect.DeepEqual(got.TLS.Pins, []string{strings.TrimSuffix(strings.Repeat("AB:", 32), ":")}) {
		t.Fatalf("created = %+v, want it enabled with only what was set", got)
	}

	want := SIPTrunkEffective{
		Domain: "sip.carrier.example", InboundCIDRs: []string{"192.0.2.10/32", "2001:db8::10/128"}, TLSIdentities: []string{"sip.carrier.example"},
	}
	if !reflect.DeepEqual(got.Effective, want) {
		t.Fatalf("effective = %+v, want %+v", got.Effective, want)
	}

	stored, _ := cfg.Settings.Get().Trunk(got.ID)
	if stored.Domain != "" || len(stored.InboundCIDRs) != 0 || len(stored.TLS.Identities) != 0 || !stored.Enabled {
		t.Fatalf("stored = %+v, want no defaults stored", stored)
	}

	moved := strings.Replace(body, "192.0.2.10", "192.0.2.20", 1)

	code, resp := serve(t, cfg, http.MethodPut, "/api/v1/sip-trunks/"+got.ID, moved)
	if updated := decodeResult[SIPTrunk](t, resp); code != http.StatusOK ||
		!reflect.DeepEqual(updated.Effective.InboundCIDRs, []string{"192.0.2.20/32", "2001:db8::10/128"}) {
		t.Fatalf("PUT = %d %s, want the default inbound CIDRs to follow the addresses", code, resp)
	}
}

func TestSIPTrunkGuard(t *testing.T) {
	live := newFakeSettings()
	cfg := Config{Settings: live}

	telnyx := createTrunk(t, cfg, telnyxTrunk)

	live.SetTrunkGuard(func(t settings.Trunk) error {
		if t.TLS.Required {
			return settings.Invalidf("tls.required needs ibcf.external.tls in the configuration file")
		}

		if t.ID == telnyx.ID {
			return settings.Invalidf("inbound_cidrs overlap the trust domain")
		}

		return nil
	})

	_, body := serve(t, cfg, http.MethodGet, "/api/v1/sip-trunks/"+telnyx.ID, "")
	if got := decodeResult[SIPTrunk](t, body); got.Status.State != settings.TrunkUnusable {
		t.Fatalf("state = %s, want unusable", got.Status.State)
	}

	required := `{"name": "Carrier", "addresses": [{"host": "sip.carrier.example", "transport": "tls"}],
		"tls": {"required": true, "pins": ["` + strings.Repeat("ab", 32) + `"]}}`

	code, body := serve(t, cfg, http.MethodPost, "/api/v1/sip-trunks", required)
	if code != http.StatusBadRequest || decodeError(t, body) != "tls.required needs ibcf.external.tls in the configuration file" {
		t.Fatalf("POST = %d %s, want 400", code, body)
	}
}

func TestSIPTrunkRoundTrip(t *testing.T) {
	cfg := Config{Settings: newFakeSettings()}

	created := createTrunk(t, cfg, telnyxTrunk)

	params := created.SIPTrunkParams
	params.TLS = &created.TLS.SIPTrunkTLSParams

	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}

	code, resp := serve(t, cfg, http.MethodPut, "/api/v1/sip-trunks/"+created.ID, string(body))
	if updated := decodeResult[SIPTrunk](t, resp); code != http.StatusOK || !reflect.DeepEqual(updated, created) {
		t.Fatalf("PUT of the GET fields = %d %s, want %+v", code, resp, created)
	}
}

func TestSIPTrunkByAddressWithoutKeepalive(t *testing.T) {
	cfg := Config{Settings: newFakeSettings()}

	body := strings.NewReplacer(`"sbc.example.org"`, `"::ffff:10.0.0.5"`, `"keepalive_interval": 60`, `"keepalive_interval": 0`).Replace(sbcTrunk)

	got := createTrunk(t, cfg, body)
	if got.Domain != "10.0.0.5" || *got.KeepaliveInterval != 0 || got.Status.State != settings.TrunkUnknown {
		t.Fatalf("created = %+v, want domain 10.0.0.5, no keepalive, and unknown", got)
	}

	body = strings.Replace(body, `"domain": "::ffff:10.0.0.5"`, `"domain": "2001:db8::5"`, 1)

	code, resp := serve(t, cfg, http.MethodPut, "/api/v1/sip-trunks/"+got.ID, body)
	if updated := decodeResult[SIPTrunk](t, resp); code != http.StatusOK || updated.Domain != "2001:db8::5" {
		t.Fatalf("PUT = %d %s, want the bare IPv6 domain", code, resp)
	}
}

func TestSIPTrunkAddressesForDNS(t *testing.T) {
	cfg := Config{Settings: newFakeSettings()}

	body := strings.Replace(sbcTrunk, `[{"host": "::ffff:10.0.0.5", "port": 5080, "transport": "udp"}]`,
		`[{"host": "sbc.example.org"}, {"host": "sbc.example.org", "transport": "tcp"}, {"host": "sbc.example.org", "port": 5070}, {"host": "10.0.0.5"}]`, 1)

	got := createTrunk(t, cfg, body)

	want := []SIPTrunkAddress{
		{Host: "sbc.example.org"},
		{Host: "sbc.example.org", Transport: "tcp"},
		{Host: "sbc.example.org", Port: new(5070)},
		{Host: "10.0.0.5"},
	}
	if !reflect.DeepEqual(got.Addresses, want) {
		t.Fatalf("addresses = %+v, want %+v with no defaults filled in", got.Addresses, want)
	}

	_, resp := serve(t, cfg, http.MethodGet, "/api/v1/sip-trunks/"+got.ID, "")
	if !strings.Contains(string(resp), `"addresses":[{"host":"sbc.example.org"},{"host":"sbc.example.org","transport":"tcp"},{"host":"sbc.example.org","port":5070},{"host":"10.0.0.5"}]`) {
		t.Fatalf("GET = %s, want absent ports and transports left out", resp)
	}
}

func TestSIPTrunksEmpty(t *testing.T) {
	cfg := Config{Settings: newFakeSettings()}

	for _, path := range []string{"/api/v1/sip-trunks", "/api/v1/sip-routes"} {
		if _, body := serve(t, cfg, http.MethodGet, path, ""); string(body) != `{"result":{"items":[]}}` {
			t.Errorf("%s = %s, want an empty list", path, body)
		}
	}
}

func TestSIPTrunkStatus(t *testing.T) {
	s := newFakeSettings()
	telnyx := createTrunk(t, Config{Settings: s}, telnyxTrunk)
	sbc := createTrunk(t, Config{Settings: s}, sbcTrunk)

	since := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	cfg := Config{Settings: s, SIPTrunks: fakeSIPTrunks{
		telnyx.ID: {State: settings.TrunkUp, Since: since},
		sbc.ID:    {State: settings.TrunkDown},
	}}

	_, body := serve(t, cfg, http.MethodGet, "/api/v1/sip-trunks", "")
	got := decodeResult[SIPTrunkList](t, body)

	want := []SIPTrunkStatus{{State: settings.TrunkUp, Since: "2026-11-06T12:00:00.000Z"}, {State: settings.TrunkDown}}
	if len(got.Items) != 2 || got.Items[0].Status != want[0] || got.Items[1].Status != want[1] {
		t.Fatalf("trunks = %+v, want statuses %+v", got.Items, want)
	}

	cfg.SIPTrunks = fakeSIPTrunks{}

	_, body = serve(t, cfg, http.MethodGet, "/api/v1/sip-trunks/"+telnyx.ID, "")
	if got := decodeResult[SIPTrunk](t, body).Status; got != (SIPTrunkStatus{State: settings.TrunkUnknown}) {
		t.Fatalf("status = %+v, want unknown until probed", got)
	}
}

func TestSIPTrunksRejected(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		code                     int
		want                     string
	}{
		{"unknown field", http.MethodPost, "", strings.Replace(sbcTrunk, `"name"`, `"id": "x", "name"`, 1), 400, "Invalid request data"},
		{"status on input", http.MethodPost, "", strings.Replace(sbcTrunk, `"name"`, `"status": {}, "name"`, 1), 400, "Invalid request data"},
		{"no name", http.MethodPost, "", strings.Replace(sbcTrunk, `"SBC"`, `""`, 1), 400, "name must be 1 to 64 printable characters, without leading or trailing spaces"},
		{"same name", http.MethodPost, "", strings.Replace(sbcTrunk, `"SBC"`, `"telnyx"`, 1), 409, "A SIP trunk already has name telnyx"},
		{"no addresses", http.MethodPost, "", strings.Replace(sbcTrunk, `[{"host": "::ffff:10.0.0.5", "port": 5080, "transport": "udp"}]`, `[]`, 1), 400, "addresses must list at least one address"},
		{"bad host", http.MethodPost, "", strings.Replace(sbcTrunk, "::ffff:10.0.0.5", "sbc example", 1), 400, `addresses must have a domain name or a specific IPv4 or IPv6 address as host, not "sbc example"`},
		{"bad port", http.MethodPost, "", strings.Replace(sbcTrunk, "5080", "65536", 1), 400, "addresses must have a port between 1 and 65535, or none"},
		{"zero port", http.MethodPost, "", strings.Replace(sbcTrunk, "5080", "0", 1), 400, "addresses must have a port between 1 and 65535, or none"},
		{"sctp", http.MethodPost, "", strings.Replace(sbcTrunk, `"udp"`, `"sctp"`, 1), 400, `addresses must have udp, tcp or tls as transport, or none, not "sctp"`},
		{"tls without trust", http.MethodPost, "", strings.Replace(sbcTrunk, `"udp"`, `"tls"`, 1), 400, "addresses over tls need tls.ca_certificates or tls.pins"},
		{"same host twice for DNS", http.MethodPost, "", strings.Replace(sbcTrunk, `[{"host": "::ffff:10.0.0.5", "port": 5080, "transport": "udp"}]`, `[{"host": "sbc.example.org"}, {"host": "sbc.example.org"}]`, 1), 400, "addresses lists sbc.example.org twice"},
		{"same address twice", http.MethodPost, "", strings.Replace(sbcTrunk, `"transport": "udp"}`, `"transport": "udp"}, {"host": "10.0.0.5", "port": 5080, "transport": "udp"}`, 1), 400, "addresses lists 10.0.0.5:5080 over udp twice"},
		{"bad CIDR", http.MethodPost, "", strings.Replace(sbcTrunk, "10.0.0.0/24", "10.0.0.0/33", 1), 400, `inbound_cidrs must be IPv4 or IPv6 networks, not "10.0.0.0/33"`},
		{"host bits", http.MethodPost, "", strings.Replace(sbcTrunk, "10.0.0.0/24", "10.0.0.1/24", 1), 400, "inbound_cidrs must be network addresses: 10.0.0.0/24, not 10.0.0.1/24"},
		{"overlapping CIDR", http.MethodPost, "", strings.Replace(sbcTrunk, "10.0.0.0/24", "192.76.120.0/24", 1), 409, "Inbound CIDR 192.76.120.0/24 overlaps 192.76.120.10/32 of SIP trunk Telnyx"},
		{"same CIDR", http.MethodPost, "", strings.Replace(sbcTrunk, "10.0.0.0/24", "64.16.250.10/32", 1), 409, "Inbound CIDR 64.16.250.10/32 overlaps 64.16.250.10/32 of SIP trunk Telnyx"},
		{"address of another trunk", http.MethodPost, "", strings.Replace(sbcTrunk, `[{"host": "::ffff:10.0.0.5", "port": 5080, "transport": "udp"}]`, `[{"host": "sip.telnyx.com"}]`, 1), 409, "Address sip.telnyx.com is an address of SIP trunk Telnyx"},
		{"no domain", http.MethodPost, "", strings.Replace(sbcTrunk, `"sbc.example.org"`, `""`, 1), 400, "domain is required when no address has a domain name as host"},
		{"no domain field", http.MethodPost, "", strings.Replace(sbcTrunk, `, "domain": "sbc.example.org"`, "", 1), 400, "domain is required when no address has a domain name as host"},
		{"wrapping keepalive", http.MethodPost, "", strings.Replace(sbcTrunk, `"keepalive_interval": 60`, `"keepalive_interval": 36028797018963998`, 1), 400, "keepalive_interval must be 0, for no keepalive, or between 5 and 3600 seconds"},
		{"negative keepalive", http.MethodPost, "", strings.Replace(sbcTrunk, `"keepalive_interval": 60`, `"keepalive_interval": -30`, 1), 400, "keepalive_interval must be 0, for no keepalive, or between 5 and 3600 seconds"},
		{"numeric domain", http.MethodPost, "", strings.Replace(sbcTrunk, `"sbc.example.org"`, `"10.0.0.256"`, 1), 400, "domain must be a domain name or a specific IPv4 or IPv6 address"},
		{"numeric host", http.MethodPost, "", strings.Replace(sbcTrunk, "::ffff:10.0.0.5", "192.168.1.010", 1), 400, `addresses must have a domain name or a specific IPv4 or IPv6 address as host, not "192.168.1.010"`},
		{"short keepalive", http.MethodPost, "", strings.Replace(sbcTrunk, `"keepalive_interval": 60`, `"keepalive_interval": 4`, 1), 400, "keepalive_interval must be 0, for no keepalive, or between 5 and 3600 seconds"},
		{"domain URI", http.MethodPost, "", strings.Replace(sbcTrunk, `"sbc.example.org"`, `"sip:sbc.example.org"`, 1), 400, "domain must be a domain name or a specific IPv4 or IPv6 address"},
		{"update unknown", http.MethodPut, "/0192", sbcTrunk, 404, "SIP trunk not found"},
		{"delete unknown", http.MethodDelete, "/0192", "", 404, "SIP trunk not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Settings: newFakeSettings()}
			telnyx := createTrunk(t, cfg, telnyxTrunk)

			code, body := serve(t, cfg, tt.method, "/api/v1/sip-trunks"+strings.Replace(tt.path, "{telnyx}", telnyx.ID, 1), tt.body)
			if code != tt.code || decodeError(t, body) != tt.want {
				t.Fatalf("%s = %d %s, want %d %q", tt.method, code, body, tt.code, tt.want)
			}

			if got := cfg.Settings.Get().Trunks; len(got) != 1 || !got[0].Enabled {
				t.Fatalf("trunks = %+v, want Telnyx alone and enabled", got)
			}
		})
	}
}

func TestSIPRoutes(t *testing.T) {
	cfg := Config{Settings: newFakeSettings()}

	telnyx := createTrunk(t, cfg, telnyxTrunk)
	sbc := createTrunk(t, cfg, sbcTrunk)

	def := createSIPRoute(t, cfg, `{"prefix": "", "trunks": ["`+telnyx.ID+`", "`+sbc.ID+`"]}`)

	want := SIPRoute{ID: def.ID, SIPRouteParams: SIPRouteParams{Prefix: "", Trunks: []string{telnyx.ID, sbc.ID}}}
	if def.ID == "" || !reflect.DeepEqual(def, want) {
		t.Fatalf("created = %+v, want %+v with an ID", def, want)
	}

	us := createSIPRoute(t, cfg, `{"prefix": "+1", "trunks": ["`+sbc.ID+`"]}`)

	code, body := serve(t, cfg, http.MethodGet, "/api/v1/sip-routes", "")
	if got := decodeResult[SIPRouteList](t, body); code != http.StatusOK || !reflect.DeepEqual(got.Items, []SIPRoute{def, us}) {
		t.Fatalf("GET routes = %d %+v, want the default route then +1", code, got)
	}

	code, body = serve(t, cfg, http.MethodGet, "/api/v1/sip-routes/"+us.ID, "")
	if code != http.StatusOK || !reflect.DeepEqual(decodeResult[SIPRoute](t, body), us) {
		t.Fatalf("GET route = %d %s, want %+v", code, body, us)
	}

	code, body = serve(t, cfg, http.MethodPut, "/api/v1/sip-routes/"+us.ID, `{"prefix": "+1555", "trunks": ["`+sbc.ID+`", "`+telnyx.ID+`"]}`)
	if got := decodeResult[SIPRoute](t, body); code != http.StatusOK || got.Prefix != "+1555" ||
		!reflect.DeepEqual(got.Trunks, []string{sbc.ID, telnyx.ID}) {
		t.Fatalf("PUT route = %d %s", code, body)
	}

	code, body = serve(t, cfg, http.MethodDelete, "/api/v1/sip-trunks/"+sbc.ID, "")
	if code != http.StatusConflict || decodeError(t, body) != "SIP trunk SBC is used by the default SIP route" {
		t.Fatalf("DELETE trunk in use = %d %s, want a conflict", code, body)
	}

	code, body = serve(t, cfg, http.MethodDelete, "/api/v1/sip-routes/"+def.ID, "")
	if code != http.StatusOK || decodeResult[Message](t, body).Message != "SIP route deleted" {
		t.Fatalf("DELETE route = %d %s", code, body)
	}

	code, body = serve(t, cfg, http.MethodDelete, "/api/v1/sip-trunks/"+sbc.ID, "")
	if code != http.StatusConflict || decodeError(t, body) != "SIP trunk SBC is used by the SIP route of prefix +1555" {
		t.Fatalf("DELETE trunk in use = %d %s, want a conflict", code, body)
	}

	if code, _ := serve(t, cfg, http.MethodGet, "/api/v1/sip-routes/"+def.ID, ""); code != http.StatusNotFound {
		t.Fatalf("GET deleted route = %d, want 404", code)
	}

	for _, tt := range []struct {
		method, path, body string
		code               int
		want               string
	}{
		{http.MethodPost, "", `{"prefix": "+1555", "trunks": ["` + telnyx.ID + `"]}`, 409, "A SIP route already has prefix +1555"},
		{http.MethodPost, "", `{"prefix": "1555", "trunks": ["` + telnyx.ID + `"]}`, 400, "prefix must be + followed by 1 to 15 digits, not starting with 0, or empty for the default route"},
		{http.MethodPost, "", `{"prefix": "+044", "trunks": ["` + telnyx.ID + `"]}`, 400, "prefix must be + followed by 1 to 15 digits, not starting with 0, or empty for the default route"},
		{http.MethodPost, "", `{"prefix": "+44", "trunks": []}`, 400, "trunks must list at least one SIP trunk"},
		{http.MethodPost, "", `{"prefix": "+44"}`, 400, "trunks must list at least one SIP trunk"},
		{http.MethodPost, "", `{"prefix": "+44", "trunks": ["0192"]}`, 400, "trunks lists 0192, which is not a SIP trunk"},
		{http.MethodPost, "", `{"prefix": "+44", "trunks": ["` + telnyx.ID + `", "` + telnyx.ID + `"]}`, 400, "trunks lists " + telnyx.ID + " twice"},
		{http.MethodPost, "", `{"prefix": "+44", "trunks": ["` + telnyx.ID + `"], "id": "x"}`, 400, "Invalid request data"},
		{http.MethodPut, "/0192", `{"prefix": "+44", "trunks": ["` + telnyx.ID + `"]}`, 404, "SIP route not found"},
		{http.MethodDelete, "/0192", "", 404, "SIP route not found"},
	} {
		code, body := serve(t, cfg, tt.method, "/api/v1/sip-routes"+tt.path, tt.body)
		if code != tt.code || decodeError(t, body) != tt.want {
			t.Errorf("%s %s %s = %d %s, want %d %q", tt.method, tt.path, tt.body, code, body, tt.code, tt.want)
		}
	}

	if n := len(cfg.Settings.Get().SIPRoutes); n != 1 {
		t.Fatalf("%d routes, want +1555 alone", n)
	}
}
