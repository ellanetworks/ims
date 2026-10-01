package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
)

type fakeDiameter struct {
	identity diameter.Identity
	peers    []diameter.PeerStatus
}

func (f fakeDiameter) Identity() diameter.Identity { return f.identity }

func (f fakeDiameter) Peers() []diameter.PeerStatus { return f.peers }

func getDiameterStatus(t *testing.T, d Diameter) DiameterStatus {
	t.Helper()

	h := NewHandler(Config{Version: "0.0.1", Diameter: d, Logger: slog.New(slog.DiscardHandler)})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/diameter", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		Result DiameterStatus `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	return resp.Result
}

func TestGetDiameterStatus(t *testing.T) {
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	d := fakeDiameter{
		identity: diameter.Identity{
			OriginHost:  "ims.ims.mnc001.mcc001.3gppnetwork.org",
			OriginRealm: "ims.mnc001.mcc001.3gppnetwork.org",
		},
		peers: []diameter.PeerStatus{
			{
				ID:         "core",
				Host:       "core.mnc001.mcc001.3gppnetwork.org",
				Realm:      "mnc001.mcc001.3gppnetwork.org",
				Transport:  diameter.TransportTCP,
				RemoteAddr: netip.MustParseAddr("::ffff:10.0.0.10"),
				State:      diameter.PeerOpen,
				Since:      since,
				Applications: []diameter.Application{
					{ID: cx.ApplicationID, VendorID: tgpp.VendorID},
					{ID: rx.ApplicationID, VendorID: tgpp.VendorID},
				},
			},
			{
				ID:        "pcrf",
				Host:      "pcrf.epc.mnc001.mcc001.3gppnetwork.org",
				Transport: diameter.TransportSCTP,
				State:     diameter.PeerDown,
				Since:     since,
				LastError: "connection refused",
			},
		},
	}

	got := getDiameterStatus(t, d)

	want := DiameterStatus{
		Host:  "ims.ims.mnc001.mcc001.3gppnetwork.org",
		Realm: "ims.mnc001.mcc001.3gppnetwork.org",
		Peers: []DiameterPeer{
			{
				ID:           "core",
				Host:         "core.mnc001.mcc001.3gppnetwork.org",
				Realm:        "mnc001.mcc001.3gppnetwork.org",
				Transport:    "tcp",
				Address:      "10.0.0.10",
				State:        "open",
				Since:        "2026-10-01T12:00:00.000Z",
				Applications: []string{"cx", "rx"},
			},
			{
				ID:           "pcrf",
				Host:         "pcrf.epc.mnc001.mcc001.3gppnetwork.org",
				Transport:    "sctp",
				State:        "down",
				Since:        "2026-10-01T12:00:00.000Z",
				LastError:    "connection refused",
				Applications: []string{},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
}

func TestGetDiameterStatusNoPeers(t *testing.T) {
	got := getDiameterStatus(t, fakeDiameter{})

	if got.Peers == nil || len(got.Peers) != 0 {
		t.Fatalf("peers = %#v, want an empty list", got.Peers)
	}
}
