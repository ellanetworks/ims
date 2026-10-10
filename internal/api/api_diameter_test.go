package api

import (
	"net/http"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
)

type fakeDiameter struct {
	peers []diameter.PeerStatus
}

func (f fakeDiameter) Identity() diameter.Identity { return diameter.Identity{} }

func (f fakeDiameter) Peers() []diameter.PeerStatus { return f.peers }

const hssPeer = `{"host": "hss.ims.mnc001.mcc001.3gppnetwork.org", "address": "10.0.0.10", "applications": ["cx"]}`

const pcrfPeer = `{"host": "pcrf.epc.mnc001.mcc001.3gppnetwork.org", "address": "10.0.0.11", "port": 3869,
	"transport": "sctp", "applications": ["rx"], "priority": 0}`

func TestGetDiameterStatus(t *testing.T) {
	code, body := serve(t, Config{Settings: newFakeSettings()}, http.MethodGet, "/api/v1/diameter", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	want := DiameterStatus{Host: "ims.ims.mnc001.mcc001.3gppnetwork.org", Realm: "ims.mnc001.mcc001.3gppnetwork.org"}
	if got := decodeResult[DiameterStatus](t, body); got != want {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
}

func createPeer(t *testing.T, cfg Config, body string) DiameterPeer {
	t.Helper()

	code, resp := serve(t, cfg, http.MethodPost, "/api/v1/diameter/peers", body)
	if code != http.StatusCreated {
		t.Fatalf("POST peer = %d, want 201: %s", code, resp)
	}

	return decodeResult[DiameterPeer](t, resp)
}

func TestDiameterPeers(t *testing.T) {
	cfg := Config{Settings: newFakeSettings()}

	hss := createPeer(t, cfg, hssPeer)

	want := DiameterPeer{
		ID: hss.ID,
		DiameterPeerParams: DiameterPeerParams{
			Host: "hss.ims.mnc001.mcc001.3gppnetwork.org", Address: "10.0.0.10", Port: 3868, Transport: "tcp",
			Applications: []string{"cx"}, Priority: new(10),
		},
		Status: DiameterPeerStatus{State: "down"},
	}
	if hss.ID == "" || !reflect.DeepEqual(hss, want) {
		t.Fatalf("created = %+v, want %+v with an ID", hss, want)
	}

	pcrf := createPeer(t, cfg, pcrfPeer)

	code, body := serve(t, cfg, http.MethodGet, "/api/v1/diameter/peers", "")
	if code != http.StatusOK {
		t.Fatalf("GET peers = %d", code)
	}

	if got := decodeResult[DiameterPeers](t, body); len(got.Items) != 2 || got.Items[0].ID != hss.ID || got.Items[1].ID != pcrf.ID {
		t.Fatalf("peers = %+v, want the HSS then the PCRF", got)
	}

	code, body = serve(t, cfg, http.MethodGet, "/api/v1/diameter/peers/"+pcrf.ID, "")
	if code != http.StatusOK || !reflect.DeepEqual(decodeResult[DiameterPeer](t, body), pcrf) {
		t.Fatalf("GET peer = %d %s, want %+v", code, body, pcrf)
	}

	moved := strings.Replace(pcrfPeer, "10.0.0.11", "::ffff:10.0.0.12", 1)

	code, body = serve(t, cfg, http.MethodPut, "/api/v1/diameter/peers/"+pcrf.ID, moved)
	if code != http.StatusOK {
		t.Fatalf("PUT peer = %d: %s", code, body)
	}

	if got := decodeResult[DiameterPeer](t, body); got.ID != pcrf.ID || got.Address != "10.0.0.12" {
		t.Fatalf("updated = %+v, want the unmapped address", got)
	}

	code, body = serve(t, cfg, http.MethodDelete, "/api/v1/diameter/peers/"+pcrf.ID, "")
	if code != http.StatusOK || decodeResult[Message](t, body).Message != "Diameter peer deleted" {
		t.Fatalf("DELETE peer = %d %s", code, body)
	}

	if code, _ := serve(t, cfg, http.MethodGet, "/api/v1/diameter/peers/"+pcrf.ID, ""); code != http.StatusNotFound {
		t.Fatalf("GET deleted peer = %d, want 404", code)
	}
}

func TestDiameterPeerStatus(t *testing.T) {
	s := newFakeSettings()
	hss := createPeer(t, Config{Settings: s}, hssPeer)

	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cfg := Config{Settings: s, Diameter: fakeDiameter{peers: []diameter.PeerStatus{{
		ID: hss.ID, Host: "hss.ims.mnc001.mcc001.3gppnetwork.org", Realm: "ims.mnc001.mcc001.3gppnetwork.org",
		State: diameter.PeerOpen, Since: since, RemoteAddr: netip.MustParseAddr("::ffff:10.0.0.10"),
	}}}}

	_, body := serve(t, cfg, http.MethodGet, "/api/v1/diameter/peers/"+hss.ID, "")

	want := DiameterPeerStatus{
		State: "open", Since: "2026-10-01T12:00:00.000Z", RemoteAddress: "10.0.0.10",
		Host: "hss.ims.mnc001.mcc001.3gppnetwork.org", Realm: "ims.mnc001.mcc001.3gppnetwork.org",
	}
	if got := decodeResult[DiameterPeer](t, body).Status; got != want {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
}

func TestDiameterPeerWithoutHost(t *testing.T) {
	s := newFakeSettings()
	noHost := strings.Replace(hssPeer, `"host": "hss.ims.mnc001.mcc001.3gppnetwork.org", `, "", 1)

	hss := createPeer(t, Config{Settings: s}, noHost)
	if hss.Host != "" {
		t.Fatalf("host = %q, want none", hss.Host)
	}

	if code, body := serve(t, Config{Settings: s}, http.MethodPost, "/api/v1/diameter/peers",
		strings.Replace(noHost, `["cx"]`, `["rx"]`, 1)); code != http.StatusConflict ||
		decodeError(t, body) != "A Diameter peer without a host already has address 10.0.0.10 over tcp" {
		t.Fatalf("second peer without a host on the same address = %d %s, want a conflict", code, body)
	}

	learned := "mmec01.mmegi0001.mme.epc.mnc001.mcc001.3gppnetwork.org"
	cfg := Config{Settings: s, Diameter: fakeDiameter{peers: []diameter.PeerStatus{{
		ID: hss.ID, Host: learned, State: diameter.PeerOpen,
	}}}}

	_, body := serve(t, cfg, http.MethodGet, "/api/v1/diameter/peers/"+hss.ID, "")
	if got := decodeResult[DiameterPeer](t, body); got.Host != "" || got.Status.Host != learned {
		t.Fatalf("peer = %+v, want no host and %s learned", got, learned)
	}
}

func TestDiameterPeerError(t *testing.T) {
	s := newFakeSettings()
	hss := createPeer(t, Config{Settings: s}, hssPeer)

	reason := "peer answered as mmec01.mmegi0001.mme.epc.mnc001.mcc001.3gppnetwork.org, expected hss.ims.mnc001.mcc001.3gppnetwork.org"
	cfg := Config{Settings: s, Diameter: fakeDiameter{peers: []diameter.PeerStatus{{
		ID: hss.ID, Host: "hss.ims.mnc001.mcc001.3gppnetwork.org", State: diameter.PeerDown, LastError: reason,
	}}}}

	_, body := serve(t, cfg, http.MethodGet, "/api/v1/diameter/peers/"+hss.ID, "")
	if got := decodeResult[DiameterPeer](t, body).Status; got.State != "down" || got.Error != reason {
		t.Fatalf("status = %+v, want down with %q", got, reason)
	}
}

func TestDiameterPeersEmpty(t *testing.T) {
	_, body := serve(t, Config{Settings: newFakeSettings()}, http.MethodGet, "/api/v1/diameter/peers", "")

	if got := string(body); got != `{"result":{"items":[]}}` {
		t.Fatalf("peers = %s, want an empty list", got)
	}
}

func TestDiameterPeersRejected(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		code                     int
		want                     string
	}{
		{"unknown field", http.MethodPost, "", strings.Replace(hssPeer, `"host"`, `"id": "x", "host"`, 1), 400, "Invalid request data"},
		{"status on input", http.MethodPost, "", strings.Replace(hssPeer, `"host"`, `"status": {}, "host"`, 1), 400, "Invalid request data"},
		{"host not a domain name", http.MethodPost, "", strings.Replace(hssPeer, "hss.ims.", "hss ims.", 1), 400, "host must be a domain name"},
		{"realm", http.MethodPost, "", strings.Replace(hssPeer, `"host"`, `"realm": "example.org", "host"`, 1), 400, "Invalid request data"},
		{"bad priority", http.MethodPost, "", strings.Replace(pcrfPeer, `"priority": 0`, `"priority": 65536`, 1), 400, "priority must be between 0 and 65535"},
		{"same host in another case", http.MethodPost, "", strings.NewReplacer(`["cx"]`, `["rx"]`, "hss.ims", "HSS.ims").Replace(hssPeer), 409, "A Diameter peer already has host HSS.ims.mnc001.mcc001.3gppnetwork.org"},
		{"bad address", http.MethodPost, "", strings.Replace(hssPeer, "10.0.0.10", "hss.example.org", 1), 400, "address must be an IPv4 or IPv6 address"},
		{"no address", http.MethodPost, "", strings.Replace(hssPeer, `"10.0.0.10"`, `""`, 1), 400, "address is required"},
		{"bad transport", http.MethodPost, "", strings.Replace(pcrfPeer, `"sctp"`, `"udp"`, 1), 400, "transport must be tcp or sctp"},
		{"bad application", http.MethodPost, "", strings.Replace(hssPeer, `["cx"]`, `["gx"]`, 1), 400, `applications must list cx, rx or both, not "gx"`},
		{"same host", http.MethodPost, "", strings.Replace(hssPeer, `["cx"]`, `["rx"]`, 1), 409, "A Diameter peer already has host hss.ims.mnc001.mcc001.3gppnetwork.org"},
		{"update unknown", http.MethodPut, "/0192", pcrfPeer, 404, "Diameter peer not found"},
		{"delete unknown", http.MethodDelete, "/0192", "", 404, "Diameter peer not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Settings: newFakeSettings()}
			createPeer(t, cfg, hssPeer)

			code, body := serve(t, cfg, tt.method, "/api/v1/diameter/peers"+tt.path, tt.body)
			if code != tt.code || decodeError(t, body) != tt.want {
				t.Fatalf("%s = %d %s, want %d %q", tt.method, code, body, tt.code, tt.want)
			}

			if n := len(cfg.Settings.Get().Peers); n != 1 {
				t.Fatalf("%d peers, want the HSS alone", n)
			}
		})
	}
}

func TestDiameterRoutes(t *testing.T) {
	s := newFakeSettings()
	cfg := Config{Settings: s}

	secondary := createPeer(t, cfg, hssPeer)
	primary := createPeer(t, cfg, strings.NewReplacer("hss.ims", "hss2.ims", `"cx"]`, `"cx", "rx"], "priority": 1`).Replace(hssPeer))
	pcrf := createPeer(t, cfg, pcrfPeer)

	cfg.Diameter = fakeDiameter{peers: []diameter.PeerStatus{{ID: primary.ID, State: diameter.PeerOpen}}}

	code, body := serve(t, cfg, http.MethodGet, "/api/v1/diameter/routes", "")
	if code != http.StatusOK {
		t.Fatalf("GET routes = %d", code)
	}

	open := DiameterPeerStatus{State: "open"}
	down := DiameterPeerStatus{State: "down"}

	want := DiameterRoutes{Items: []DiameterRoute{
		{
			Application: "cx", DestinationRealm: "ims.mnc001.mcc001.3gppnetwork.org",
			DiameterRoutePeers: DiameterRoutePeers{Peers: []DiameterRoutePeer{
				{ID: primary.ID, Host: "hss2.ims.mnc001.mcc001.3gppnetwork.org", Priority: 1, Status: open},
				{ID: secondary.ID, Host: "hss.ims.mnc001.mcc001.3gppnetwork.org", Priority: 10, Status: down},
			}},
		},
		{
			Application: "rx", DestinationRealm: "ims.mnc001.mcc001.3gppnetwork.org",
			DiameterRoutePeers: DiameterRoutePeers{Peers: []DiameterRoutePeer{
				{ID: pcrf.ID, Host: "pcrf.epc.mnc001.mcc001.3gppnetwork.org", Priority: 0, Status: down},
				{ID: primary.ID, Host: "hss2.ims.mnc001.mcc001.3gppnetwork.org", Priority: 1, Status: open},
			}},
		},
	}}
	if got := decodeResult[DiameterRoutes](t, body); !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %+v, want %+v", got, want)
	}

	code, body = serve(t, cfg, http.MethodPut, "/api/v1/diameter/routes/rx", `{"realm": "epc.mnc001.mcc001.3gppnetwork.org"}`)
	if got := decodeResult[DiameterRoute](t, body); code != http.StatusOK || got.Realm != "epc.mnc001.mcc001.3gppnetwork.org" ||
		got.DestinationRealm != got.Realm || len(got.Peers) != 2 {
		t.Fatalf("PUT route = %d %+v", code, got)
	}

	code, body = serve(t, cfg, http.MethodGet, "/api/v1/diameter/routes/rx", "")
	if got := decodeResult[DiameterRoute](t, body); code != http.StatusOK || got.Realm != "epc.mnc001.mcc001.3gppnetwork.org" {
		t.Fatalf("GET route = %d %+v", code, got)
	}

	code, body = serve(t, cfg, http.MethodPut, "/api/v1/diameter/routes/rx", `{"realm": ""}`)
	if got := decodeResult[DiameterRoute](t, body); code != http.StatusOK || got.Realm != "" ||
		got.DestinationRealm != "ims.mnc001.mcc001.3gppnetwork.org" {
		t.Fatalf("PUT route back to the home domain = %d %+v", code, got)
	}

	for _, tt := range []struct {
		method, path, body string
		code               int
		want               string
	}{
		{http.MethodGet, "/gx", "", 404, "Diameter route not found"},
		{http.MethodPut, "/gx", `{"realm": "example.org"}`, 404, "Diameter route not found"},
		{http.MethodPut, "/cx", `{"realm": "bad realm"}`, 400, "realm must be a domain name"},
		{http.MethodPut, "/cx", `{"realm": "example.org", "peers": []}`, 400, "Invalid request data"},
	} {
		code, body := serve(t, cfg, tt.method, "/api/v1/diameter/routes"+tt.path, tt.body)
		if code != tt.code || decodeError(t, body) != tt.want {
			t.Errorf("%s %s = %d %s, want %d %q", tt.method, tt.path, code, body, tt.code, tt.want)
		}
	}
}
