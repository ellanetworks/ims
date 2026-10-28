package server

import (
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/api/apitest"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/sip"
)

// apiDo sends a request of body, if any, to the API, and returns its status and its body.
func apiDo(t *testing.T, srv *Server, method, path, body string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, "http://"+srv.APIAddr().String()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	res, err := apitest.Client(srv.APIAddr().String()).Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	var b json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&b); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}

	return res.StatusCode, b
}

// udpInUse reports whether a UDP socket is bound to a.
func udpInUse(a netip.AddrPort) bool {
	c, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(a))
	if err != nil {
		return true
	}

	_ = c.Close()

	return false
}

// The MCPTT AS of the configuration runs with the IMS: in memory, of no socket, on its SIP port of each SIP address,
// which its name of the home domain resolves to; its settings over the API. A restart of the core leaves it running;
// one of a new home domain starts it again under the new name, its settings kept.
func TestMCPTT(t *testing.T) {
	cfg := testConfig(t)
	port := unusedPort(t)
	cfg.MCPTT = &config.MCPTT{
		Mode: config.MCPTTModeIMS, Port: port,
		Media: config.MCPTTMedia{Address: loopback, MinPort: 41000, MaxPort: 41099, ControlPort: unusedPort(t)},
	}

	srv := startIMS(t, cfg)
	as := srv.mcptt.Load()

	var status struct {
		Result api.MCPTTStatus `json:"result"`
	}

	code, b := apiDo(t, srv, http.MethodGet, "/api/v1/mcptt", "")
	if err := json.Unmarshal(b, &status); err != nil || code != http.StatusOK {
		t.Fatalf("status: %d %s", code, b)
	}

	want := []string{
		netip.AddrPortFrom(loopback, uint16(port)).String(), netip.AddrPortFrom(loopback6, uint16(port)).String(),
	}
	if m := status.Result; m.Mode != "ims" || strings.Join(m.SIP, " ") != strings.Join(want, " ") ||
		m.Name != "sip:mcptt.ims.mnc001.mcc001.3gppnetwork.org" ||
		m.ParticipatingPSI != "sip:mcptt-participating@mcptt.ims.mnc001.mcc001.3gppnetwork.org" {
		t.Fatalf("status %s, want MCPTT on %v", b, want)
	}

	if udpInUse(netip.AddrPortFrom(loopback, uint16(port))) {
		t.Fatal("MCPTT has a socket through the IMS")
	}

	resolves := func(host string) bool {
		servers, err := srv.ases.resolver.Resolve(t.Context(), sip.URI{Scheme: "sip", Host: host})
		return err == nil && len(servers) == 2 && servers[0].Addr == netip.AddrPortFrom(loopback, uint16(port))
	}

	if !resolves("mcptt.ims.mnc001.mcc001.3gppnetwork.org") {
		t.Fatal("the name of MCPTT does not resolve to it")
	}

	// The live state, through mcpttView: no client registered, so none affiliated and no call.
	for _, path := range []string{"/api/v1/mcptt/registrations", "/api/v1/mcptt/affiliations", "/api/v1/mcptt/calls"} {
		var list struct {
			Result struct {
				Items []json.RawMessage `json:"items"`
			} `json:"result"`
		}

		code, b := apiDo(t, srv, http.MethodGet, path, "")
		if err := json.Unmarshal(b, &list); err != nil || code != http.StatusOK || list.Result.Items == nil ||
			len(list.Result.Items) != 0 {
			t.Fatalf("GET %s: %d %s, want no items", path, code, b)
		}
	}

	if code, b := apiDo(t, srv, http.MethodGet, "/api/v1/mcptt/calls/x", ""); code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/mcptt/calls/x: %d %s", code, b)
	}

	if code, b := apiDo(t, srv, http.MethodPost, "/api/v1/mcptt/users",
		`{"mcptt_id": "sip:alice@mcptt.example.com", "impus": ["sip:alice@ims.example.com"]}`); code != http.StatusCreated {
		t.Fatalf("create a user: %d %s", code, b)
	}

	before := srv.core.Load()
	srv.restartCore(t.Context(), srv.settings.Get(), "for the test")

	if c := srv.core.Load(); c == nil || c == before || srv.mcptt.Load() != as {
		t.Fatal("the MCPTT AS did not outlive the core")
	}

	before = srv.core.Load()
	op := srv.settings.Get().Operator
	op.MNC = "02"

	if err := srv.settings.UpdateOperator(t.Context(), op); err != nil {
		t.Fatalf("UpdateOperator: %v", err)
	}

	eventually(t, "the core under the new identity", func() bool {
		c := srv.core.Load()
		return c != nil && c != before
	})

	if a := srv.mcptt.Load(); a == nil || a == as {
		t.Fatal("the MCPTT AS not started again for the new home domain")
	}

	if !resolves("mcptt.ims.mnc002.mcc001.3gppnetwork.org") || resolves("mcptt.ims.mnc001.mcc001.3gppnetwork.org") {
		t.Fatal("MCPTT not renamed for the new home domain")
	}

	if psi := srv.mcptt.Load().Settings().Current().ParticipatingPSI().String(); psi != "sip:mcptt-participating@mcptt.ims.mnc002.mcc001.3gppnetwork.org" {
		t.Fatalf("participating PSI %s after the new home domain", psi)
	}

	code, b = apiDo(t, srv, http.MethodGet, "/api/v1/mcptt/users", "")
	if code != http.StatusOK || !strings.Contains(string(b), "sip:alice@mcptt.example.com") {
		t.Fatalf("users after a restart of the core: %d %s", code, b)
	}
}

// Without its configuration, the IMS runs no MCPTT AS: its paths of the API are not found.
func TestMCPTTDisabled(t *testing.T) {
	srv := startIMS(t, testConfig(t))

	if srv.mcptt.Load() != nil {
		t.Fatal("an MCPTT AS runs")
	}

	if code, b := apiDo(t, srv, http.MethodGet, "/api/v1/mcptt/users", ""); code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/mcptt/users: %d %s", code, b)
	}

	if code, b := apiDo(t, srv, http.MethodGet, "/api/v1/status", ""); code != http.StatusOK || strings.Contains(string(b), "mcptt") {
		t.Fatalf("status: %d %s", code, b)
	}
}
