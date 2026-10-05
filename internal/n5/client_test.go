package n5

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type exchange struct {
	method, path, contentType, userAgent string
	proto                                int
	body                                 []byte
}

type fakePCF struct {
	*httptest.Server
	client *Client

	mu        sync.Mutex
	exchanges []exchange
}

// newPCF serves handler over cleartext HTTP/2 only, so that the client must speak it with prior knowledge.
func newPCF(t *testing.T, handler http.HandlerFunc) *fakePCF {
	t.Helper()

	p := &fakePCF{}

	p.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		p.mu.Lock()
		p.exchanges = append(p.exchanges, exchange{
			method: r.Method, path: r.URL.Path, contentType: r.Header.Get("Content-Type"),
			userAgent: r.Header.Get("User-Agent"), proto: r.ProtoMajor, body: body,
		})
		p.mu.Unlock()

		r.Body = io.NopCloser(bytes.NewReader(body))
		handler(w, r)
	}))

	p.Config.Protocols = new(http.Protocols)
	p.Config.Protocols.SetUnencryptedHTTP2(true)
	p.Start()
	t.Cleanup(p.Close)

	c, err := New(Config{PCF: p.URL})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(c.Close)
	p.client = c

	return p
}

func (p *fakePCF) last(t *testing.T) exchange {
	t.Helper()

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.exchanges) == 0 {
		t.Fatal("no request reached the PCF")
	}

	return p.exchanges[len(p.exchanges)-1]
}

func reply(status int, contentType, body string, header ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}

		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}

		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func sameJSON(t *testing.T, got, want []byte) {
	t.Helper()

	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: %v", got, err)
	}

	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(g, w) {
		t.Fatalf("JSON\n%s\nwant\n%s", got, want)
	}
}

func ptr[T any](v T) *T { return &v }

// TS 29.514 §4.2.6.7
func signallingContext() *AppSessionContext {
	notif := "http://192.0.2.10:7778/n5/v1/sessions/s1"

	return &AppSessionContext{AscReqData: &AppSessionContextReqData{
		EvSubsc: &EventsSubscReqData{Events: []AfEventSubscription{{Event: EventFailedResourcesAllocation}}, NotifURI: notif},
		MedComponents: map[string]MediaComponent{"0": {
			MedCompN: 0, MedSubComps: map[string]MediaSubComponent{"0": {FNum: 0, FlowUsage: FlowUsageAFSignalling}},
		}},
		NotifURI: notif,
		SuppFeat: Features(FeatureIMSSBI, FeaturePatchCorrection),
		UEIPv6:   netip.MustParseAddr("2001:db8::1"),
	}}
}

func callContext() *AppSessionContext {
	notif := "http://192.0.2.10:7778/n5/v1/sessions/c1"

	return &AppSessionContext{AscReqData: &AppSessionContextReqData{
		AFAppID: "urn:urn-7:3gpp-service.ims.icsi.mmtel",
		EvSubsc: &EventsSubscReqData{
			Events:   []AfEventSubscription{{Event: EventChargingCorrelation}, {Event: EventFailedResourcesAllocation}},
			NotifURI: notif,
		},
		MedComponents: map[string]MediaComponent{"1": {
			Codecs:  []CodecData{"uplink\noffer\nm=audio 49000 RTP/AVP 116\r\na=rtpmap:116 AMR-WB/16000/1\r\n"},
			FStatus: FlowEnabled, MarBwDl: ptr(BitRate(49000)), MarBwUl: ptr(BitRate(49000)), MedCompN: 1,
			MedSubComps: map[string]MediaSubComponent{
				"1": {FNum: 1, FDescs: []FlowDescription{
					"permit out 17 from 192.0.2.20 50000 to 10.45.0.2 49000",
					"permit in 17 from 10.45.0.2 49000 to 192.0.2.20 50000",
				}},
				"2": {FNum: 2, FlowUsage: FlowUsageRTCP, FDescs: []FlowDescription{
					"permit out 17 from 192.0.2.20 50001 to 10.45.0.2 49001",
					"permit in 17 from 10.45.0.2 49001 to 192.0.2.20 50001",
				}},
			},
			MedType: MediaAudio, RRBw: ptr(BitRate(1837)), RSBw: ptr(BitRate(612)),
		}},
		NotifURI: notif,
		GPSI:     "msisdn-15551234567",
		SuppFeat: Features(FeatureIMSSBI, FeaturePatchCorrection),
		UEIPv4:   netip.MustParseAddr("10.45.0.2"),
	}}
}

// TS 29.514 §4.2.2.2, §5.8: the PCF answers 201 with the context, its URI in Location, and the negotiated features.
func TestCreate(t *testing.T) {
	for name, tc := range map[string]struct {
		asc     *AppSessionContext
		fixture string
	}{
		"signalling": {signallingContext(), "create_signalling.json"},
		"call":       {callContext(), "create_call.json"},
	} {
		t.Run(name, func(t *testing.T) {
			pcf := newPCF(t, reply(http.StatusCreated, "application/json; charset=utf-8",
				`{"ascRespData":{"suppFeat":"10"}}`, "Location", "/npcf-policyauthorization/v1/app-sessions/42"))

			got, err := pcf.client.Create(context.Background(), tc.asc)
			if err != nil {
				t.Fatal(err)
			}

			ex := pcf.last(t)
			if ex.proto != 2 || ex.method != http.MethodPost || ex.path != "/npcf-policyauthorization/v1/app-sessions" ||
				ex.contentType != ContentJSON || ex.userAgent != DefaultUserAgent {
				t.Fatalf("request %+v", ex)
			}

			sameJSON(t, ex.body, fixture(t, tc.fixture))

			if got.URI != pcf.URL+"/npcf-policyauthorization/v1/app-sessions/42" || got.Existing {
				t.Fatalf("created %+v", got)
			}

			if f := got.Context.AscRespData.SuppFeat; !f.Has(FeatureIMSSBI) || f.Has(FeaturePatchCorrection) {
				t.Fatalf("negotiated features %q", f)
			}
		})
	}
}

// TS 29.514 §5.3.2.3.1: 303 points at an existing context with the same content.
func TestCreateExisting(t *testing.T) {
	pcf := newPCF(t, reply(http.StatusSeeOther, "", "", "Location", "http://pcf.example:7777/npcf-policyauthorization/v1/app-sessions/7"))

	got, err := pcf.client.Create(context.Background(), callContext())
	if err != nil {
		t.Fatal(err)
	}

	if got != (Created{URI: "http://pcf.example:7777/npcf-policyauthorization/v1/app-sessions/7", Existing: true}) {
		t.Fatalf("created %+v", got)
	}

	if n := len(pcf.exchanges); n != 1 {
		t.Fatalf("%d requests, want the 303 not followed", n)
	}
}

func TestCreateLocation(t *testing.T) {
	for loc, want := range map[string]string{
		"/npcf-policyauthorization/v1/app-sessions/a1":                     "/npcf-policyauthorization/v1/app-sessions/a1",
		"/npcf-policyauthorization/v1/app-sessions/a1/events-subscription": "/npcf-policyauthorization/v1/app-sessions/a1",
		"/prefix/npcf-policyauthorization/v1/app-sessions/a1":              "/prefix/npcf-policyauthorization/v1/app-sessions/a1",
		"": "",
		"/npcf-policyauthorization/v1/app-sessions":               "",
		"/npcf-policyauthorization/v1/app-sessions/":              "",
		"/npcf-policyauthorization/v1/other/a1":                   "",
		"https://pcf/npcf-policyauthorization/v1/app-sessions/a1": "",
	} {
		pcf := newPCF(t, reply(http.StatusCreated, ContentJSON, `{}`, "Location", loc))

		got, err := pcf.client.Create(context.Background(), callContext())

		switch {
		case want == "" && !errors.Is(err, ErrMalformedResponse):
			t.Errorf("Location %q: %+v, %v; want a malformed response", loc, got, err)
		case want != "" && (err != nil || got.URI != pcf.URL+want):
			t.Errorf("Location %q: %+v, %v; want %s", loc, got, err, want)
		}
	}
}

func TestCreateMalformed(t *testing.T) {
	loc := []string{"Location", "/npcf-policyauthorization/v1/app-sessions/1"}

	for name, h := range map[string]http.HandlerFunc{
		"no body":        reply(http.StatusCreated, ContentJSON, "", loc...),
		"bad JSON":       reply(http.StatusCreated, ContentJSON, "{", loc...),
		"wrong type":     reply(http.StatusCreated, ContentJSON, `{"ascRespData":{"suppFeat":16}}`, loc...),
		"not JSON":       reply(http.StatusCreated, "text/plain", "{}", loc...),
		"undefined code": reply(http.StatusOK, ContentJSON, "{}", loc...),
	} {
		t.Run(name, func(t *testing.T) {
			pcf := newPCF(t, h)

			got, err := pcf.client.Create(context.Background(), callContext())

			var e *Error
			if !errors.As(err, &e) || !errors.Is(err, ErrMalformedResponse) || e.Op != OpCreate {
				t.Fatalf("error %v, want a malformed create", err)
			}

			// A context the PCF created can still be deleted.
			if name != "undefined code" && got.URI == "" {
				t.Fatalf("created %+v, want the URI", got)
			}
		})
	}
}

// TS 29.514 §5.6.2.3
func TestCreateValidation(t *testing.T) {
	pcf := newPCF(t, reply(http.StatusCreated, ContentJSON, `{}`))

	for name, edit := range map[string]func(*AppSessionContextReqData){
		"no notifUri":    func(r *AppSessionContextReqData) { r.NotifURI = "" },
		"no suppFeat":    func(r *AppSessionContextReqData) { r.SuppFeat = "" },
		"bad suppFeat":   func(r *AppSessionContextReqData) { r.SuppFeat = "1g" },
		"no UE address":  func(r *AppSessionContextReqData) { r.UEIPv4 = netip.Addr{} },
		"both addresses": func(r *AppSessionContextReqData) { r.UEIPv6 = netip.MustParseAddr("2001:db8::1") },
		"IPv6 as IPv4":   func(r *AppSessionContextReqData) { r.UEIPv4 = netip.MustParseAddr("2001:db8::1") },
		"mapped IPv6": func(r *AppSessionContextReqData) {
			r.UEIPv4, r.UEIPv6 = netip.Addr{}, netip.MustParseAddr("::ffff:10.45.0.2")
		},
		"no events": func(r *AppSessionContextReqData) { r.EvSubsc.Events = nil },
	} {
		asc := callContext()
		edit(asc.AscReqData)

		if _, err := pcf.client.Create(context.Background(), asc); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	if _, err := pcf.client.Create(context.Background(), &AppSessionContext{}); err == nil {
		t.Error("no ascReqData: accepted")
	}

	if len(pcf.exchanges) != 0 {
		t.Fatalf("%d invalid requests sent", len(pcf.exchanges))
	}
}

// TS 29.514 §4.2.3.2, §5.2.2.2
func TestModify(t *testing.T) {
	for name, tc := range map[string]struct {
		h    http.HandlerFunc
		want *AppSessionContext
	}{
		"204": {reply(http.StatusNoContent, "", ""), nil},
		"200": {
			reply(http.StatusOK, ContentJSON, `{"ascRespData":{"suppFeat":"10"}}`),
			&AppSessionContext{AscRespData: &AppSessionContextRespData{SuppFeat: "10"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			pcf := newPCF(t, tc.h)

			patch, err := NewPatch(nil, &AppSessionContextUpdateData{SipForkInd: ForkingSeveralDialogues})
			if err != nil {
				t.Fatal(err)
			}

			got, err := pcf.client.Modify(context.Background(), pcf.URL+"/npcf-policyauthorization/v1/app-sessions/42", patch)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Modify = %+v, %v; want %+v", got, err, tc.want)
			}

			ex := pcf.last(t)
			if ex.proto != 2 || ex.method != http.MethodPatch || ex.path != "/npcf-policyauthorization/v1/app-sessions/42" ||
				ex.contentType != ContentMergePatch || string(ex.body) != `{"ascReqData":{"sipForkInd":"SEVERAL_DIALOGUES"}}` {
				t.Fatalf("request %+v %s", ex, ex.body)
			}
		})
	}
}

// TS 29.514 §4.2.4.2, §5.3.3.4.2
func TestDelete(t *testing.T) {
	uri := "/npcf-policyauthorization/v1/app-sessions/42"

	t.Run("204", func(t *testing.T) {
		pcf := newPCF(t, reply(http.StatusNoContent, "", ""))

		got, err := pcf.client.Delete(context.Background(), pcf.URL+uri, nil)
		if err != nil || got != nil {
			t.Fatalf("Delete = %+v, %v", got, err)
		}

		ex := pcf.last(t)
		if ex.method != http.MethodPost || ex.path != uri+"/delete" || ex.contentType != "" || len(ex.body) != 0 {
			t.Fatalf("request %+v", ex)
		}
	})

	t.Run("200 with events", func(t *testing.T) {
		pcf := newPCF(t, reply(http.StatusOK, ContentJSON, `{"evsNotif":{"evSubsUri":"x","evNotifs":[{"event":"CHARGING_CORRELATION"}],`+
			`"anChargIds":[{"accNetChargIdString":"c1","flows":[{"medCompN":1,"fNums":[1,2]}]}],"anChargAddr":{"anChargIpv4Addr":"192.0.2.1"}}}`))

		got, err := pcf.client.Delete(context.Background(), pcf.URL+uri,
			&EventsSubscReqData{Events: []AfEventSubscription{{Event: EventChargingCorrelation}}})
		if err != nil {
			t.Fatal(err)
		}

		want := &EventsNotification{
			EvSubsURI: "x", EvNotifs: []AfEventNotification{{Event: EventChargingCorrelation}},
			AnChargIDs:  []AccessNetChargingIdentifier{{AccNetChargIDString: "c1", Flows: []Flows{{MedCompN: 1, FNums: []uint32{1, 2}}}}},
			AnChargAddr: &AccNetChargingAddress{AnChargIPv4Addr: netip.MustParseAddr("192.0.2.1")},
		}
		if !reflect.DeepEqual(got.EvsNotif, want) {
			t.Fatalf("evsNotif %+v, want %+v", got.EvsNotif, want)
		}

		ex := pcf.last(t)
		if ex.contentType != ContentJSON || string(ex.body) != `{"events":[{"event":"CHARGING_CORRELATION"}]}` {
			t.Fatalf("request %+v %s", ex, ex.body)
		}
	})
}

// TS 29.500 §5.2.7, TS 29.514 §5.7
func TestErrors(t *testing.T) {
	date := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)

	for name, tc := range map[string]struct {
		h          http.HandlerFunc
		status     int
		cause      string
		retryAfter time.Duration
	}{
		"temporarily not authorized": {
			reply(http.StatusForbidden, ContentProblem, `{"status":403,"cause":"REQUESTED_SERVICE_TEMPORARILY_NOT_AUTHORIZED",`+
				`"acceptableServInfo":{"marBwUl":"10 Kbps"}}`, "Retry-After", "30"),
			403, CauseRequestedServiceTemporarilyNotAuthorized, 30 * time.Second,
		},
		"retry after a date": {
			reply(http.StatusForbidden, ContentProblem, `{"cause":"REQUESTED_SERVICE_TEMPORARILY_NOT_AUTHORIZED"}`, "Retry-After", date),
			403, CauseRequestedServiceTemporarilyNotAuthorized, time.Hour,
		},
		"no binding": {
			reply(http.StatusInternalServerError, ContentProblem, `{"cause":"PDU_SESSION_NOT_AVAILABLE"}`),
			500, CausePDUSessionNotAvailable, 0,
		},
		"no body":       {reply(http.StatusServiceUnavailable, "", ""), 503, "", 0},
		"not a problem": {reply(http.StatusBadRequest, "text/html", `{"cause":"X"}`), 400, "", 0},
		"bad problem":   {reply(http.StatusBadRequest, ContentProblem, `{"cause":1}`), 400, "", 0},
		"open5gs not found": {
			func(w http.ResponseWriter, r *http.Request) {
				reply(http.StatusNotFound, ContentProblem, string(fixture(t, "open5gs/problem_not_found.json")))(w, r)
			},
			404, "", 0,
		},
		"redirect not followed": {
			reply(http.StatusMovedPermanently, "", "", "Location", "/elsewhere"), 301, "", 0,
		},
	} {
		t.Run(name, func(t *testing.T) {
			pcf := newPCF(t, tc.h)

			_, err := pcf.client.Create(context.Background(), callContext())

			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("error %v, want *Error", err)
			}

			if e.Op != OpCreate || e.Status != tc.status || e.Cause() != tc.cause || errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("error %+v (%v), want status %d cause %q", e, err, tc.status, tc.cause)
			}

			if d := e.RetryAfter - tc.retryAfter; d < -2*time.Second || d > 2*time.Second {
				t.Fatalf("RetryAfter %s, want %s", e.RetryAfter, tc.retryAfter)
			}
		})
	}
}

// RFC 9110 §15.4.8, §15.4.9: 307 and 308 repeat the request at the new URI.
func TestRedirect(t *testing.T) {
	var pcf *fakePCF

	pcf = newPCF(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/old/") {
			w.Header().Set("Location", pcf.URL+strings.TrimPrefix(r.URL.Path, "/old"))
			w.WriteHeader(http.StatusTemporaryRedirect)

			return
		}

		w.WriteHeader(http.StatusNoContent)
	})

	patch, _ := NewPatch(nil, &AppSessionContextUpdateData{AFAppID: "app"})

	if _, err := pcf.client.Modify(context.Background(), pcf.URL+"/old/npcf-policyauthorization/v1/app-sessions/1", patch); err != nil {
		t.Fatal(err)
	}

	ex := pcf.last(t)
	if ex.method != http.MethodPatch || ex.path != "/npcf-policyauthorization/v1/app-sessions/1" ||
		ex.contentType != ContentMergePatch || string(ex.body) != `{"ascReqData":{"afAppId":"app"}}` {
		t.Fatalf("redirected request %+v %s", ex, ex.body)
	}
}

func TestUnreachable(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := l.Addr().String()
	_ = l.Close()

	c, err := New(Config{PCF: "http://" + addr})
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.Create(context.Background(), callContext())

	var e *Error
	if !errors.As(err, &e) || e.Status != 0 || !errors.Is(err, ErrConnect) {
		t.Fatalf("error %v, want a connection failure", err)
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	pcf := newPCF(t, func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	})

	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := pcf.client.Delete(ctx, pcf.URL+"/npcf-policyauthorization/v1/app-sessions/1", nil)

	var e *Error
	if !errors.As(err, &e) || e.Status != 0 || errors.Is(err, ErrConnect) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v, want an unanswered request", err)
	}
}

func TestConfig(t *testing.T) {
	for _, uri := range []string{"", "pcf:7777", "https://pcf:7777", "http://", "http://u@pcf", "http://pcf?x=1"} {
		if _, err := New(Config{PCF: uri}); err == nil {
			t.Errorf("%q accepted", uri)
		}
	}

	c, err := New(Config{PCF: "http://pcf:7777/prefix/"})
	if err != nil {
		t.Fatal(err)
	}

	if u, _ := c.resource("/npcf-policyauthorization/v1/app-sessions/1", deleteOperation); u !=
		"http://pcf:7777/npcf-policyauthorization/v1/app-sessions/1/delete" {
		t.Fatalf("relative URI resolved to %q", u)
	}

	for _, uri := range []string{"https://pcf/x", "::"} {
		if _, err := c.resource(uri, ""); err == nil {
			t.Errorf("context URI %q accepted", uri)
		}
	}
}

// Bodies as the Open5GS PCF and its test AF build them (testdata/README.md).
func TestOpen5GS(t *testing.T) {
	var req AppSessionContext
	if err := json.Unmarshal(fixture(t, "open5gs/create_request.json"), &req); err != nil {
		t.Fatal(err)
	}

	r := req.AscReqData
	if r.UEIPv4 != netip.MustParseAddr("10.45.0.2") || r.SuppFeat != "12" || len(r.EvSubsc.Events) != 2 ||
		r.EvSubsc.Events[1].NotifMethod != NotifOneTime || *r.MedComponents["1"].MarBwUl != 96000 ||
		r.MedComponents["1"].MedSubComps["2"].FlowUsage != FlowUsageRTCP {
		t.Fatalf("create request decoded as %+v", r)
	}

	t.Run("create", func(t *testing.T) {
		pcf := newPCF(t, reply(http.StatusCreated, ContentJSON, string(fixture(t, "open5gs/create_response.json")),
			"Location", "http://127.0.0.13:7777/npcf-policyauthorization/v1/app-sessions/1"))

		got, err := pcf.client.Create(context.Background(), callContext())
		if err != nil {
			t.Fatal(err)
		}

		// No ascRespData: the features are not negotiated as TS 29.514 §5.8 defines.
		if got.URI != "http://127.0.0.13:7777/npcf-policyauthorization/v1/app-sessions/1" || got.Context.AscRespData != nil {
			t.Fatalf("created %+v", got)
		}
	})

	t.Run("update", func(t *testing.T) {
		pcf := newPCF(t, reply(http.StatusOK, ContentJSON, string(fixture(t, "open5gs/update_response.json"))))

		patch, _ := NewPatch(nil, &AppSessionContextUpdateData{AFAppID: "IMS Services"})

		got, err := pcf.client.Modify(context.Background(), pcf.URL+"/npcf-policyauthorization/v1/app-sessions/1", patch)
		if err != nil || got == nil {
			t.Fatalf("Modify = %+v, %v", got, err)
		}
	})
}
