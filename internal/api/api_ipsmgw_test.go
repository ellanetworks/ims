package api

import (
	"net/http"
	"reflect"
	"strings"
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

func createSMSRoute(t *testing.T, cfg Config, body string) SMSRoute {
	t.Helper()

	code, resp := serve(t, cfg, http.MethodPost, "/api/v1/ip-sm-gw/routes", body)
	if code != http.StatusCreated {
		t.Fatalf("POST route = %d, want 201: %s", code, resp)
	}

	return decodeResult[SMSRoute](t, resp)
}

func TestSMSRoutes(t *testing.T) {
	cfg := Config{Settings: newFakeSettings()}

	smsc := createPeer(t, cfg, smscPeer)
	backup := createPeer(t, cfg, strings.NewReplacer("smsc.epc", "backup.epc", "10.0.0.12", "10.0.0.13").Replace(smscPeer))
	hss := createPeer(t, cfg, hssPeer)

	def := createSMSRoute(t, cfg, `{"service_centre": "", "peers": ["`+smsc.ID+`", "`+backup.ID+`"]}`)

	want := SMSRoute{ID: def.ID, SMSRouteParams: SMSRouteParams{Peers: []string{smsc.ID, backup.ID}}}
	if def.ID == "" || !reflect.DeepEqual(def, want) {
		t.Fatalf("created = %+v, want %+v with an ID", def, want)
	}

	sc := createSMSRoute(t, cfg, `{"service_centre": "15550000000", "peers": ["`+backup.ID+`"]}`)

	code, body := serve(t, cfg, http.MethodGet, "/api/v1/ip-sm-gw/routes", "")
	if got := decodeResult[SMSRouteList](t, body); code != http.StatusOK || !reflect.DeepEqual(got.Items, []SMSRoute{def, sc}) {
		t.Fatalf("GET routes = %d %+v, want the default route then 15550000000", code, got)
	}

	code, body = serve(t, cfg, http.MethodGet, "/api/v1/ip-sm-gw/routes/"+sc.ID, "")
	if code != http.StatusOK || !reflect.DeepEqual(decodeResult[SMSRoute](t, body), sc) {
		t.Fatalf("GET route = %d %s, want %+v", code, body, sc)
	}

	code, body = serve(t, cfg, http.MethodPut, "/api/v1/ip-sm-gw/routes/"+sc.ID,
		`{"service_centre": "15550000001", "peers": ["`+backup.ID+`", "`+smsc.ID+`"]}`)
	if got := decodeResult[SMSRoute](t, body); code != http.StatusOK || got.ServiceCentre != "15550000001" ||
		!reflect.DeepEqual(got.Peers, []string{backup.ID, smsc.ID}) {
		t.Fatalf("PUT route = %d %s", code, body)
	}

	code, body = serve(t, cfg, http.MethodDelete, "/api/v1/diameter/peers/"+smsc.ID, "")
	if code != http.StatusConflict ||
		decodeError(t, body) != "Diameter peer smsc.epc.mnc001.mcc001.3gppnetwork.org is used by the default SMS route" {
		t.Fatalf("DELETE peer in use = %d %s, want a conflict", code, body)
	}

	code, body = serve(t, cfg, http.MethodPut, "/api/v1/diameter/peers/"+smsc.ID, strings.Replace(smscPeer, `["sgd"]`, `["cx"]`, 1))
	if code != http.StatusConflict || decodeError(t, body) !=
		"Diameter peer smsc.epc.mnc001.mcc001.3gppnetwork.org must serve sgd, as the default SMS route uses it" {
		t.Fatalf("PUT peer without sgd = %d %s, want a conflict", code, body)
	}

	code, body = serve(t, cfg, http.MethodDelete, "/api/v1/ip-sm-gw/routes/"+def.ID, "")
	if code != http.StatusOK || decodeResult[Message](t, body).Message != "SMS route deleted" {
		t.Fatalf("DELETE route = %d %s", code, body)
	}

	if code, _ := serve(t, cfg, http.MethodGet, "/api/v1/ip-sm-gw/routes/"+def.ID, ""); code != http.StatusNotFound {
		t.Fatalf("GET deleted route = %d, want 404", code)
	}

	for _, tt := range []struct {
		method, path, body string
		code               int
		want               string
	}{
		{http.MethodPost, "", `{"service_centre": "15550000001", "peers": ["` + smsc.ID + `"]}`, 409, "An SMS route already has service centre 15550000001"},
		{http.MethodPost, "", `{"service_centre": "+1555", "peers": ["` + smsc.ID + `"]}`, 400, "service_centre must be an E.164 number of up to 15 digits, or empty for the default route"},
		{http.MethodPost, "", `{"service_centre": "1555", "peers": []}`, 400, "peers must list at least one Diameter peer"},
		{http.MethodPost, "", `{"service_centre": "1555"}`, 400, "peers must list at least one Diameter peer"},
		{http.MethodPost, "", `{"service_centre": "1555", "peers": ["0192"]}`, 400, "peers lists 0192, which is not a Diameter peer"},
		{http.MethodPost, "", `{"service_centre": "1555", "peers": ["` + smsc.ID + `", "` + smsc.ID + `"]}`, 400, "peers lists " + smsc.ID + " twice"},
		{http.MethodPost, "", `{"service_centre": "1555", "peers": ["` + hss.ID + `"]}`, 409, "Diameter peer hss.ims.mnc001.mcc001.3gppnetwork.org must serve sgd, as the SMS route of service centre 1555 uses it"},
		{http.MethodPost, "", `{"service_centre": "1555", "peers": ["` + smsc.ID + `"], "id": "x"}`, 400, "Invalid request data"},
		{http.MethodPut, "/0192", `{"service_centre": "1555", "peers": ["` + smsc.ID + `"]}`, 404, "SMS route not found"},
		{http.MethodDelete, "/0192", "", 404, "SMS route not found"},
	} {
		code, body := serve(t, cfg, tt.method, "/api/v1/ip-sm-gw/routes"+tt.path, tt.body)
		if code != tt.code || decodeError(t, body) != tt.want {
			t.Errorf("%s %s %s = %d %s, want %d %q", tt.method, tt.path, tt.body, code, body, tt.code, tt.want)
		}
	}

	if n := len(cfg.Settings.Get().SMSRoutes); n != 1 {
		t.Fatalf("%d routes, want 15550000001 alone", n)
	}
}
