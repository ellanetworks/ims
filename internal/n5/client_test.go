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

			if got.Features != "10" || got.BodyErr != nil {
				t.Fatalf("negotiated features %q, body error %v", got.Features, got.BodyErr)
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

	if got != (Created{Result: Result{URI: "http://pcf.example:7777/npcf-policyauthorization/v1/app-sessions/7"}, Existing: true}) {
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
		"/npcf-policyauthorization/v1/app-sessions/a%2Fb":                  "/npcf-policyauthorization/v1/app-sessions/a%2Fb",
		"/npcf-policyauthorization/v1/app-sessions/a1?x=1#f":               "/npcf-policyauthorization/v1/app-sessions/a1",
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

// TS 29.500 §5.2.7.3: the PCF created the context, so a body that cannot be read does not fail the create.
func TestCreateBody(t *testing.T) {
	loc := []string{"Location", "/npcf-policyauthorization/v1/app-sessions/1"}

	for name, tc := range map[string]struct {
		h       http.HandlerFunc
		bodyErr bool
	}{
		"no body":          {reply(http.StatusCreated, ContentJSON, "", loc...), true},
		"bad JSON":         {reply(http.StatusCreated, ContentJSON, "{", loc...), true},
		"bad evsNotif":     {reply(http.StatusCreated, ContentJSON, `{"evsNotif":{"evNotifs":7}}`, loc...), true},
		"bad features":     {reply(http.StatusCreated, ContentJSON, `{"ascRespData":{"suppFeat":16}}`, loc...), true},
		"not JSON":         {reply(http.StatusCreated, "text/plain", "{}", loc...), true},
		"too large":        {reply(http.StatusCreated, ContentJSON, `{"x":"`+strings.Repeat("a", maxBody)+`"}`, loc...), true},
		"undefined 2xx":    {reply(http.StatusOK, ContentJSON, "{}", loc...), false},
		"undefined, empty": {reply(http.StatusAccepted, "", "", loc...), false},
	} {
		t.Run(name, func(t *testing.T) {
			pcf := newPCF(t, tc.h)

			got, err := pcf.client.Create(context.Background(), callContext())
			if err != nil || got.URI == "" {
				t.Fatalf("Create = %+v, %v; want the context", got, err)
			}

			if tc.bodyErr != errors.Is(got.BodyErr, ErrMalformedResponse) {
				t.Fatalf("body error %v, want one: %t", got.BodyErr, tc.bodyErr)
			}
		})
	}
}

// TS 29.514 §5.8: the negotiated features are in ascRespData, and only there.
func TestCreateFeatures(t *testing.T) {
	loc := []string{"Location", "/npcf-policyauthorization/v1/app-sessions/1"}

	for body, want := range map[string]SupportedFeatures{
		`{"ascRespData":{"suppFeat":"10"},"ascReqData":{"suppFeat":"8000010"}}`: "10",
		`{"ascReqData":{"suppFeat":"12"}}`:                                      "",
		`{}`:                                                                    "",
	} {
		pcf := newPCF(t, reply(http.StatusCreated, ContentJSON, body, loc...))

		got, err := pcf.client.Create(context.Background(), callContext())
		if err != nil || got.Features != want || got.BodyErr != nil {
			t.Errorf("%s: features %q, %v, %v; want %q", body, got.Features, err, got.BodyErr, want)
		}
	}
}

func TestCreateWithoutLocation(t *testing.T) {
	pcf := newPCF(t, reply(http.StatusCreated, ContentJSON, "{}"))

	_, err := pcf.client.Create(context.Background(), callContext())

	var e *Error
	if !errors.As(err, &e) || !errors.Is(err, ErrMalformedResponse) || e.Op != OpCreate {
		t.Fatalf("error %v, want a malformed create", err)
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
		"no events":          func(r *AppSessionContextReqData) { r.EvSubsc.Events = nil },
		"no events notifUri": func(r *AppSessionContextReqData) { r.EvSubsc.NotifURI = "" },
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
		want *EventsNotification
	}{
		"204": {reply(http.StatusNoContent, "", ""), nil},
		"200": {
			reply(http.StatusOK, ContentJSON, `{"evsNotif":{"evSubsUri":"x","evNotifs":[{"event":"QOS_NOTIF"}]}}`),
			&EventsNotification{EvSubsURI: "x", EvNotifs: []AfEventNotification{{Event: EventQoSNotif}}},
		},
		"200 unreadable": {reply(http.StatusOK, ContentJSON, `{"evsNotif":1}`), nil},
	} {
		t.Run(name, func(t *testing.T) {
			pcf := newPCF(t, tc.h)

			patch, err := NewPatch(nil, &AppSessionContextUpdateData{SipForkInd: ForkingSeveralDialogues})
			if err != nil {
				t.Fatal(err)
			}

			uri := pcf.URL + "/npcf-policyauthorization/v1/app-sessions/42"

			got, err := pcf.client.Modify(context.Background(), uri, patch)
			if err != nil || !reflect.DeepEqual(got.Notification, tc.want) || got.URI != uri {
				t.Fatalf("Modify = %+v, %v; want %+v", got, err, tc.want)
			}

			if (got.BodyErr != nil) != (name == "200 unreadable") {
				t.Fatalf("body error %v", got.BodyErr)
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
		if err != nil || got != (Result{}) {
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
		if !reflect.DeepEqual(got.Notification, want) {
			t.Fatalf("evsNotif %+v, want %+v", got.Notification, want)
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
		"partly bad problem": {
			reply(http.StatusForbidden, ContentProblem, `{"status":"403","cause":"REQUESTED_SERVICE_NOT_AUTHORIZED"}`),
			403, CauseRequestedServiceNotAuthorized, 0,
		},
		"huge retry after": {
			reply(http.StatusServiceUnavailable, "", "", "Retry-After", "99999999999999999999999"), 503, "", maxRetryAfter * time.Second,
		},
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

// RFC 9110 §15.4.8, §15.4.9: 307 and 308 repeat the request at the new URI; only 308 moves the context
// (TS 29.500 §6.10.9).
func TestRedirect(t *testing.T) {
	var pcf *fakePCF

	pcf = newPCF(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/temporary/"):
			w.Header().Set("Location", pcf.URL+strings.TrimPrefix(r.URL.Path, "/temporary"))
			w.WriteHeader(http.StatusTemporaryRedirect)
		case strings.HasPrefix(r.URL.Path, "/permanent/"):
			w.Header().Set("Location", "/moved"+strings.TrimPrefix(r.URL.Path, "/permanent"))
			w.WriteHeader(http.StatusPermanentRedirect)
		case strings.HasPrefix(r.URL.Path, "/loop/"):
			w.Header().Set("Location", r.URL.Path)
			w.WriteHeader(http.StatusTemporaryRedirect)
		case strings.HasPrefix(r.URL.Path, "/tls/"):
			w.Header().Set("Location", "https://"+r.Host+r.URL.Path)
			w.WriteHeader(http.StatusPermanentRedirect)
		case r.URL.Path == "/elsewhere"+AppSessionsPath:
			w.Header().Set("Location", "app-sessions/9")
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	patch, _ := NewPatch(nil, &AppSessionContextUpdateData{AFAppID: "app"})
	ctx := context.Background()

	got, err := pcf.client.Modify(ctx, pcf.URL+"/temporary/npcf-policyauthorization/v1/app-sessions/1", patch)
	if err != nil || got.URI != pcf.URL+"/temporary/npcf-policyauthorization/v1/app-sessions/1" {
		t.Fatalf("Modify through 307 = %+v, %v; want the URI unchanged", got, err)
	}

	ex := pcf.last(t)
	if ex.method != http.MethodPatch || ex.path != "/npcf-policyauthorization/v1/app-sessions/1" ||
		ex.contentType != ContentMergePatch || string(ex.body) != `{"ascReqData":{"afAppId":"app"}}` {
		t.Fatalf("redirected request %+v %s", ex, ex.body)
	}

	got, err = pcf.client.Modify(ctx, pcf.URL+"/permanent/npcf-policyauthorization/v1/app-sessions/1", patch)
	if err != nil || got.URI != pcf.URL+"/moved/npcf-policyauthorization/v1/app-sessions/1" {
		t.Fatalf("Modify through 308 = %+v, %v; want the moved URI", got, err)
	}

	for _, path := range []string{"/loop/x", "/tls/x"} {
		_, err := pcf.client.Delete(ctx, pcf.URL+path, nil)

		var e *Error
		if !errors.As(err, &e) || e.Status < 300 || e.Status >= 400 {
			t.Fatalf("%s: %v, want the redirection as the answer", path, err)
		}
	}

	pcf.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == AppSessionsPath {
			w.Header().Set("Location", "/elsewhere"+AppSessionsPath)
			w.WriteHeader(http.StatusTemporaryRedirect)

			return
		}

		w.Header().Set("Location", "app-sessions/9")
		w.WriteHeader(http.StatusCreated)
	})

	created, err := pcf.client.Create(ctx, callContext())
	if err != nil || created.URI != pcf.URL+"/elsewhere/npcf-policyauthorization/v1/app-sessions/9" {
		t.Fatalf("Create through 307 = %+v, %v; want Location resolved against the redirected URI", created, err)
	}
}

func TestDeleteWithoutEvents(t *testing.T) {
	pcf := newPCF(t, reply(http.StatusNoContent, "", ""))

	if _, err := pcf.client.Delete(context.Background(), pcf.URL+AppSessionsPath+"/1", &EventsSubscReqData{}); err == nil {
		t.Fatal("an events subscription without events accepted")
	}
}

func TestWriteProblem(t *testing.T) {
	w := httptest.NewRecorder()
	WriteProblem(w, ProblemDetails{Status: http.StatusBadRequest, Cause: CauseResourceContextNotFound})

	if w.Code != http.StatusBadRequest || w.Header().Get("Content-Type") != ContentProblem ||
		w.Body.String() != `{"status":400,"cause":"RESOURCE_CONTEXT_NOT_FOUND"}` {
		t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
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

	if u, _ := c.resource("/npcf-policyauthorization/v1/app-sessions/1", DeleteSegment); u !=
		"http://pcf:7777/npcf-policyauthorization/v1/app-sessions/1/delete" {
		t.Fatalf("relative URI resolved to %q", u)
	}

	for _, uri := range []string{"https://pcf/x", "::", "npcf-policyauthorization/v1/app-sessions/1", "app-sessions/1"} {
		if _, err := c.resource(uri, ""); err == nil {
			t.Errorf("context URI %q accepted", uri)
		}
	}
}

// Bodies captured between the P-CSCF and the Open5GS PCF (testdata/README.md).
func TestOpen5GS(t *testing.T) {
	const location = "http://10.80.0.10:7777/npcf-policyauthorization/v1/app-sessions/3"

	var req AppSessionContext
	if err := json.Unmarshal(fixture(t, "open5gs/create_request.json"), &req); err != nil {
		t.Fatal(err)
	}

	r := req.AscReqData
	if err := validateCreate(&req); err != nil || r.UEIPv4 != netip.MustParseAddr("10.46.0.250") || r.SuppFeat != "8000010" ||
		len(r.EvSubsc.Events) != 1 || *r.MedComponents["1"].MarBwUl != 41000 ||
		r.MedComponents["1"].MedSubComps["2"].FlowUsage != FlowUsageRTCP {
		t.Fatalf("create request decoded as %+v, %v", r, err)
	}

	t.Run("create", func(t *testing.T) {
		pcf := newPCF(t, reply(http.StatusCreated, ContentJSON, string(fixture(t, "open5gs/create_response.json")),
			"Location", location))

		got, err := pcf.client.Create(context.Background(), &req)
		if err != nil {
			t.Fatal(err)
		}

		// No ascRespData, so no negotiated features (TS 29.514 §5.8): Open5GS narrows the echoed
		// ascReqData.suppFeat instead.
		if got.URI != location || got.Features != "" || got.BodyErr != nil {
			t.Fatalf("created %+v", got)
		}
	})

	t.Run("update", func(t *testing.T) {
		patch := fixture(t, "open5gs/update_request.json")
		if err := CheckPatch(patch); err != nil {
			t.Fatalf("captured patch: %v", err)
		}

		var p AppSessionContextUpdateDataPatch
		if err := json.Unmarshal(patch, &p); err != nil {
			t.Fatal(err)
		}

		// Open5GS rebuilds a patched component from the patch: it came whole, with its medType.
		if c := p.AscReqData.MedComponents["1"]; c.MedType != MediaAudio || len(c.MedSubComps) != 2 || len(c.Codecs) != 2 {
			t.Fatalf("captured patch component %+v", c)
		}

		pcf := newPCF(t, reply(http.StatusOK, ContentJSON, string(fixture(t, "open5gs/update_response.json"))))

		next := &AppSessionContextUpdateData{MedComponents: p.AscReqData.MedComponents}

		sent, err := NewPatch(nil, next)
		if err != nil {
			t.Fatal(err)
		}

		got, err := pcf.client.Modify(context.Background(), pcf.URL+"/npcf-policyauthorization/v1/app-sessions/3", sent)
		if err != nil || got.BodyErr != nil {
			t.Fatalf("Modify = %+v, %v", got, err)
		}

		// The same service information gives the same patch as the one Open5GS took.
		sameJSON(t, pcf.last(t).body, patch)
	})

	// TS 29.514 §4.2.6.7 leaves medType out of the signalling component; Open5GS requires it.
	t.Run("signalling refused", func(t *testing.T) {
		pcf := newPCF(t, reply(http.StatusBadRequest, ContentProblem, string(fixture(t, "open5gs/problem_signalling.json"))))

		_, err := pcf.client.Create(context.Background(), signallingContext())

		var e *Error
		if !errors.As(err, &e) || e.Status != http.StatusBadRequest || e.Cause() != "" || e.Problem == nil ||
			!strings.Contains(e.Problem.Title, "Media-Type is Required") {
			t.Fatalf("Create = %v, want Open5GS's 400", err)
		}
	})

	t.Run("terminate", func(t *testing.T) {
		var ti TerminationInfo
		if err := json.Unmarshal(fixture(t, "open5gs/terminate.json"), &ti); err != nil {
			t.Fatal(err)
		}

		if ti.TermCause != TerminationPDUSessionTermination || !strings.HasPrefix(ti.ResURI, "http://10.80.0.10:7777"+AppSessionsPath+"/") {
			t.Fatalf("terminate %+v", ti)
		}
	})
}
