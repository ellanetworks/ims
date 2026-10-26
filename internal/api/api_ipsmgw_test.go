package api

import (
	"net/http"
	"testing"
)

func TestIPSMGW(t *testing.T) {
	cfg := Config{Settings: newFakeSettings()}

	code, body := serve(t, cfg, http.MethodGet, "/api/v1/ip-sm-gw", "")
	if got := decodeResult[IPSMGW](t, body); code != http.StatusOK || got.Number != "" ||
		got.Name != "sip:ipsmgw."+cfg.Settings.Get().Operator.HomeDomain() {
		t.Fatalf("GET = %d %s, want no number", code, body)
	}

	code, body = serve(t, cfg, http.MethodPut, "/api/v1/ip-sm-gw", `{"number": "15550000100"}`)
	if got := decodeResult[IPSMGW](t, body); code != http.StatusOK || got.Number != "15550000100" ||
		cfg.Settings.Get().IPSMGW.Number != "15550000100" {
		t.Fatalf("PUT = %d %s, want the number", code, body)
	}

	if code, body = serve(t, cfg, http.MethodPut, "/api/v1/ip-sm-gw", `{"number": ""}`); code != http.StatusOK ||
		cfg.Settings.Get().IPSMGW.Number != "" {
		t.Fatalf("PUT empty = %d %s, want no number", code, body)
	}

	for body, want := range map[string]string{
		`{"number": "+15550000100"}`:     "ip_sm_gw.number must be an E.164 number of up to 15 digits",
		`{"number": "1234567890123456"}`: "ip_sm_gw.number must be an E.164 number of up to 15 digits",
		`{"number": "1", "name": "x"}`:   "Invalid request data",
	} {
		if code, resp := serve(t, cfg, http.MethodPut, "/api/v1/ip-sm-gw", body); code != http.StatusBadRequest ||
			decodeError(t, resp) != want {
			t.Errorf("PUT %s = %d %s, want 400 %q", body, code, resp, want)
		}
	}
}
