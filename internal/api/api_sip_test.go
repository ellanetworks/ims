package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"testing"
)

type fakeSIP []SIPEndpoint

func (f fakeSIP) Listeners() []SIPEndpoint { return f }

func getSIPStatus(t *testing.T, cfg Config) SIPStatus {
	t.Helper()

	cfg.Logger = slog.New(slog.DiscardHandler)

	rec := httptest.NewRecorder()
	NewHandler(cfg).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sip", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		Result SIPStatus `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	return resp.Result
}

func TestGetSIPStatus(t *testing.T) {
	got := getSIPStatus(t, Config{
		SIP: fakeSIP{
			{Role: "pcscf", Address: netip.MustParseAddrPort("10.0.0.5:5060")},
			{Role: "icscf", Address: netip.MustParseAddrPort("[2001:db8::5]:5070")},
		},
		HomeDomain: "ims.mnc001.mcc001.3gppnetwork.org",
		SIPAliases: []string{"ims.mnc001.mcc001.3gppnetwork.org", "pcscf.ims.mnc001.mcc001.3gppnetwork.org"},
	})

	want := SIPStatus{
		HomeDomain: "ims.mnc001.mcc001.3gppnetwork.org",
		Aliases:    []string{"ims.mnc001.mcc001.3gppnetwork.org", "pcscf.ims.mnc001.mcc001.3gppnetwork.org"},
		Listeners: []SIPListener{
			{Role: "pcscf", Address: "10.0.0.5:5060", Transports: []string{"udp", "tcp"}},
			{Role: "icscf", Address: "[2001:db8::5]:5070", Transports: []string{"udp", "tcp"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
}

func TestGetSIPStatusNoListeners(t *testing.T) {
	got := getSIPStatus(t, Config{SIP: fakeSIP{}})

	if got.Listeners == nil || len(got.Listeners) != 0 || got.Aliases == nil {
		t.Fatalf("status = %#v, want empty lists", got)
	}
}
