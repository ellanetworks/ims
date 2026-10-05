package pcftest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/n5"
)

var ue = netip.MustParseAddr("10.45.0.2")

type notification struct {
	method, path, contentType string
	body                      []byte
}

type consumer struct {
	t      *testing.T
	pcf    *PCF
	client *n5.Client
	notifs chan notification
	af     *httptest.Server
	// status answers notifications; zero is 204.
	status int
}

func newConsumer(t *testing.T, cfg Config) *consumer {
	t.Helper()

	cfg.Logger = slog.New(slog.DiscardHandler)

	c := &consumer{t: t, pcf: New(t, cfg), notifs: make(chan notification, 8)}

	c.af = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.notifs <- notification{method: r.Method, path: r.URL.Path, contentType: r.Header.Get("Content-Type"), body: b}

		if c.status != 0 {
			n5.WriteProblem(w, n5.ProblemDetails{Status: c.status, Cause: n5.CauseResourceContextNotFound})
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	c.af.Config.Protocols = new(http.Protocols)
	c.af.Config.Protocols.SetUnencryptedHTTP2(true)
	c.af.Start()
	t.Cleanup(c.af.Close)

	client, err := n5.New(n5.Config{PCF: c.pcf.URL()})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(client.Close)
	c.client = client

	return c
}

func (c *consumer) notifURI(session string) string {
	return c.af.URL + "/n5/v1/sessions/" + session
}

func (c *consumer) signalling(session string) *n5.AppSessionContext {
	return &n5.AppSessionContext{AscReqData: &n5.AppSessionContextReqData{
		EvSubsc: &n5.EventsSubscReqData{
			Events:   []n5.AfEventSubscription{{Event: n5.EventFailedResourcesAllocation}},
			NotifURI: c.notifURI(session),
		},
		MedComponents: map[string]n5.MediaComponent{"0": {
			MedSubComps: map[string]n5.MediaSubComponent{"0": {FlowUsage: n5.FlowUsageAFSignalling}},
		}},
		NotifURI: c.notifURI(session),
		SuppFeat: n5.Features(n5.FeatureIMSSBI, n5.FeaturePatchCorrection),
		UEIPv4:   ue,
	}}
}

func audio() map[string]n5.MediaComponent {
	return map[string]n5.MediaComponent{"1": {
		MedCompN: 1,
		MedType:  n5.MediaAudio,
		FStatus:  n5.FlowEnabled,
		MedSubComps: map[string]n5.MediaSubComponent{"1": {
			FNum: 1,
			FDescs: []string{
				"permit out 17 from 192.0.2.20 50000 to 10.45.0.2 49000",
				"permit in 17 from 10.45.0.2 49000 to 192.0.2.20 50000",
			},
		}},
	}}
}

func (c *consumer) call(session string) *n5.AppSessionContext {
	asc := c.signalling(session)
	asc.AscReqData.MedComponents = audio()

	return asc
}

func (c *consumer) create(asc *n5.AppSessionContext) n5.Created {
	c.t.Helper()

	created, err := c.client.Create(context.Background(), asc)
	if err != nil {
		c.t.Fatal(err)
	}

	return created
}

func (c *consumer) nextNotification() notification {
	c.t.Helper()

	select {
	case n := <-c.notifs:
		return n
	case <-time.After(5 * time.Second):
		c.t.Fatal("no notification")
	}

	return notification{}
}

// raw sends a request that the n5 client would refuse to.
type answer struct {
	status  int
	header  http.Header
	problem n5.ProblemDetails
}

func raw(t *testing.T, method, url, contentType, body string) answer {
	t.Helper()

	var protocols http.Protocols

	protocols.SetUnencryptedHTTP2(true)

	client := &http.Client{Transport: &http.Transport{Protocols: &protocols}}

	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	a := answer{status: resp.StatusCode, header: resp.Header}

	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &a.problem)

	return a
}

func status(t *testing.T, err error) (int, string) {
	t.Helper()

	var e *n5.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v, want an n5.Error", err)
	}

	return e.Status, e.Cause()
}

func TestCreate(t *testing.T) {
	c := newConsumer(t, Config{Features: n5.Features(n5.FeatureIMSSBI)})

	created := c.create(c.call("c1"))

	if !strings.HasPrefix(created.URI, c.pcf.URL()+n5.AppSessionsPath+"/") {
		t.Fatalf("URI %q", created.URI)
	}

	// TS 29.514 §5.8: the intersection of what both support.
	if created.Features != n5.Features(n5.FeatureIMSSBI) || created.BodyErr != nil {
		t.Fatalf("features %q, body error %v", created.Features, created.BodyErr)
	}

	r := c.pcf.Next(t)
	if r.Op != n5.OpCreate || r.URI != created.URI || r.Problem != nil || Signalling(r.Context) {
		t.Fatalf("request %v", r)
	}

	ctx, ok := c.pcf.Context(created.URI)
	if !ok || ctx.UEIPv4 != ue || ctx.MedComponents["1"].MedType != n5.MediaAudio {
		t.Fatalf("context %+v", ctx)
	}
}

func TestCreateSignalling(t *testing.T) {
	c := newConsumer(t, Config{})

	c.create(c.signalling("s1"))

	if r := c.pcf.Next(t); !Signalling(r.Context) {
		t.Fatalf("%v is not signalling", r)
	}
}

// TS 29.514 §4.2.2.2: session binding fails with PDU_SESSION_NOT_AVAILABLE. An IPv6 address binds to the
// prefix of the PDU session.
func TestBinding(t *testing.T) {
	c := newConsumer(t, Config{UEs: []netip.Addr{netip.MustParseAddr("10.45.0.9"), netip.MustParseAddr("2001:db8:1:2::1")}})

	_, err := c.client.Create(context.Background(), c.signalling("s1"))
	if s, cause := status(t, err); s != http.StatusInternalServerError || cause != n5.CausePDUSessionNotAvailable {
		t.Fatalf("%d %s", s, cause)
	}

	if r := c.pcf.Next(t); r.Problem == nil {
		t.Fatalf("%v has no problem", r)
	}

	asc := c.signalling("s2")
	asc.AscReqData.UEIPv4 = netip.Addr{}
	asc.AscReqData.UEIPv6 = netip.MustParseAddr("2001:db8:1:2::abcd")
	c.create(asc)

	if v := c.pcf.Violations(); len(v) > 0 {
		t.Fatalf("violations %v", v)
	}
}

// TS 29.514 §4.2.2.2, §5.6.2.3, TS 29.500 §5.2.7.2.
func TestCreateValidation(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		cause                   string
	}{
		{"content type", "text/plain", `{}`, http.StatusUnsupportedMediaType, ""},
		{"not JSON", n5.ContentJSON, `{`, http.StatusBadRequest, CauseInvalidMsgFormat},
		{"no ascReqData", n5.ContentJSON, `{}`, http.StatusBadRequest, CauseMandatoryIEMissing},
		{"no body", n5.ContentJSON, ``, http.StatusBadRequest, CauseMandatoryIEMissing},
		{"no notifUri", n5.ContentJSON, `{"ascReqData":{"suppFeat":"10","ueIpv4":"10.45.0.2"}}`, http.StatusBadRequest, CauseMandatoryIEMissing},
		{"relative notifUri", n5.ContentJSON, `{"ascReqData":{"notifUri":"/n5","suppFeat":"10","ueIpv4":"10.45.0.2"}}`, http.StatusBadRequest, CauseMandatoryIEIncorrect},
		{"no suppFeat", n5.ContentJSON, `{"ascReqData":{"notifUri":"http://af/n5","ueIpv4":"10.45.0.2"}}`, http.StatusBadRequest, CauseMandatoryIEMissing},
		{"bad suppFeat", n5.ContentJSON, `{"ascReqData":{"notifUri":"http://af/n5","suppFeat":"x","ueIpv4":"10.45.0.2"}}`, http.StatusBadRequest, CauseMandatoryIEIncorrect},
		{"no UE", n5.ContentJSON, `{"ascReqData":{"notifUri":"http://af/n5","suppFeat":"10"}}`, http.StatusBadRequest, CauseMandatoryIEMissing},
		{"two UEs", n5.ContentJSON, `{"ascReqData":{"notifUri":"http://af/n5","suppFeat":"10","ueIpv4":"10.45.0.2","ueIpv6":"2001:db8::1"}}`, http.StatusBadRequest, CauseMandatoryIEIncorrect},
		{"IPv6 as ueIpv4", n5.ContentJSON, `{"ascReqData":{"notifUri":"http://af/n5","suppFeat":"10","ueIpv4":"2001:db8::1"}}`, http.StatusBadRequest, CauseMandatoryIEIncorrect},
		{"events without notifUri", n5.ContentJSON, `{"ascReqData":{"notifUri":"http://af/n5","suppFeat":"10","ueIpv4":"10.45.0.2","evSubsc":{"events":[{"event":"QOS_NOTIF"}]}}}`, http.StatusBadRequest, CauseMandatoryIEMissing},
		{"no events", n5.ContentJSON, `{"ascReqData":{"notifUri":"http://af/n5","suppFeat":"10","ueIpv4":"10.45.0.2","evSubsc":{"events":[],"notifUri":"http://af/n5"}}}`, http.StatusBadRequest, CauseMandatoryIEMissing},
		{"medCompN key", n5.ContentJSON, `{"ascReqData":{"notifUri":"http://af/n5","suppFeat":"10","ueIpv4":"10.45.0.2","medComponents":{"2":{"medCompN":1}}}}`, http.StatusBadRequest, CauseInvalidMsgFormat},
		{"fNum key", n5.ContentJSON, `{"ascReqData":{"notifUri":"http://af/n5","suppFeat":"10","ueIpv4":"10.45.0.2","medComponents":{"1":{"medCompN":1,"medSubComps":{"1":{"fNum":2}}}}}}`, http.StatusBadRequest, CauseInvalidMsgFormat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newConsumer(t, Config{AllowViolations: true})

			a := raw(t, http.MethodPost, c.pcf.URL()+n5.AppSessionsPath, tc.contentType, tc.body)
			if a.status != tc.status || a.problem.Cause != tc.cause {
				t.Fatalf("%d %s, want %d %s", a.status, a.problem.Cause, tc.status, tc.cause)
			}

			if len(c.pcf.Violations()) != 1 || len(c.pcf.Contexts()) != 0 {
				t.Fatalf("violations %v, contexts %v", c.pcf.Violations(), c.pcf.Contexts())
			}
		})
	}
}

// A violation fails the test that let it through.
func TestViolationFailsTest(t *testing.T) {
	ft := &fakeTB{TB: t}
	p := New(ft, Config{Logger: slog.New(slog.DiscardHandler)})

	raw(t, http.MethodPost, p.URL()+n5.AppSessionsPath, "text/plain", "{}")
	ft.cleanup()

	if !ft.failed {
		t.Fatal("a violation did not fail the test")
	}
}

type fakeTB struct {
	testing.TB
	cleanups []func()
	failed   bool
}

func (f *fakeTB) Cleanup(fn func())     { f.cleanups = append(f.cleanups, fn) }
func (f *fakeTB) Errorf(string, ...any) { f.failed = true }

func (f *fakeTB) cleanup() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
}

// TS 29.514 §4.2.3.2
func TestModify(t *testing.T) {
	c := newConsumer(t, Config{})

	asc := c.call("c1")
	created := c.create(asc)
	c.pcf.Next(t)

	prev := &n5.AppSessionContextUpdateData{MedComponents: asc.AscReqData.MedComponents, EvSubsc: asc.AscReqData.EvSubsc}
	next := &n5.AppSessionContextUpdateData{MedComponents: audio(), SipForkInd: n5.ForkingSeveralDialogues}

	m := next.MedComponents["1"]
	m.FStatus = n5.FlowRemoved
	next.MedComponents["1"] = m

	patch, err := n5.NewPatch(prev, next)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.client.Modify(context.Background(), created.URI, patch); err != nil {
		t.Fatal(err)
	}

	r := c.pcf.Next(t)
	if r.Op != n5.OpModify || r.URI != created.URI || r.Context.MedComponents["1"].FStatus != n5.FlowRemoved {
		t.Fatalf("request %v", r)
	}

	ctx, _ := c.pcf.Context(created.URI)
	if ctx.EvSubsc != nil || ctx.SipForkInd != n5.ForkingSeveralDialogues || ctx.MedComponents["1"].FStatus != n5.FlowRemoved {
		t.Fatalf("context %+v", ctx)
	}
}

func TestModifyValidation(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		cause                   string
	}{
		{"content type", n5.ContentJSON, `{}`, http.StatusUnsupportedMediaType, ""},
		{"not JSON", n5.ContentMergePatch, `{`, http.StatusBadRequest, CauseInvalidMsgFormat},
		{"null notifUri", n5.ContentMergePatch, `{"ascReqData":{"notifUri":null}}`, http.StatusBadRequest, CauseInvalidMsgFormat},
		{"component without medCompN", n5.ContentMergePatch, `{"ascReqData":{"medComponents":{"2":{"medType":"AUDIO"}}}}`, http.StatusBadRequest, CauseInvalidMsgFormat},
		{"medCompN key", n5.ContentMergePatch, `{"ascReqData":{"medComponents":{"2":{"medCompN":3}}}}`, http.StatusBadRequest, CauseInvalidMsgFormat},
		{"remove events", n5.ContentMergePatch, `{"ascReqData":{"evSubsc":null}}`, http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newConsumer(t, Config{AllowViolations: true})
			created := c.create(c.signalling("s1"))

			a := raw(t, http.MethodPatch, created.URI, tc.contentType, tc.body)
			if a.status != tc.status || a.problem.Cause != tc.cause {
				t.Fatalf("%d %s, want %d %s", a.status, a.problem.Cause, tc.status, tc.cause)
			}

			// TS 29.500 §5.2.7.2
			if tc.status == http.StatusUnsupportedMediaType && a.header.Get("Accept-Patch") != n5.ContentMergePatch {
				t.Fatalf("Accept-Patch %q", a.header.Get("Accept-Patch"))
			}

			if want := tc.status != http.StatusOK; (len(c.pcf.Violations()) == 1) != want {
				t.Fatalf("violations %v", c.pcf.Violations())
			}
		})
	}

	t.Run("new events without notifUri", func(t *testing.T) {
		c := newConsumer(t, Config{AllowViolations: true})

		asc := c.signalling("s1")
		asc.AscReqData.EvSubsc = nil
		created := c.create(asc)

		a := raw(t, http.MethodPatch, created.URI, n5.ContentMergePatch, `{"ascReqData":{"evSubsc":{"events":[{"event":"QOS_NOTIF"}]}}}`)
		if a.status != http.StatusBadRequest || a.problem.Cause != CauseMandatoryIEMissing {
			t.Fatalf("%d %s", a.status, a.problem.Cause)
		}
	})
}

// TS 29.514 §5.7.3, TS 29.500 §5.2.7.2.
func TestUnknownContext(t *testing.T) {
	c := newConsumer(t, Config{})

	_, err := c.client.Modify(context.Background(), c.pcf.URL()+n5.AppSessionsPath+"/nope", n5.Patch{})
	if s, cause := status(t, err); s != http.StatusNotFound || cause != n5.CauseAppSessionContextNotFound {
		t.Fatalf("modify: %d %s", s, cause)
	}

	_, err = c.client.Delete(context.Background(), c.pcf.URL()+n5.AppSessionsPath+"/nope", nil)
	if s, cause := status(t, err); s != http.StatusNotFound || cause != n5.CauseAppSessionContextNotFound {
		t.Fatalf("delete: %d %s", s, cause)
	}

	if v := c.pcf.Violations(); len(v) > 0 {
		t.Fatalf("violations %v", v)
	}
}

func TestRouting(t *testing.T) {
	c := newConsumer(t, Config{AllowViolations: true})
	created := c.create(c.signalling("s1"))

	for _, tc := range []struct {
		method, url string
		status      int
		allow       string
	}{
		{http.MethodGet, c.pcf.URL() + n5.AppSessionsPath, http.StatusMethodNotAllowed, "POST"},
		{http.MethodPut, created.URI, http.StatusMethodNotAllowed, "GET, PATCH"},
		{http.MethodGet, created.URI + "/delete", http.StatusMethodNotAllowed, "POST"},
		{http.MethodGet, created.URI, http.StatusOK, ""},
		{http.MethodPut, created.URI + "/events-subscription", http.StatusNotFound, ""},
		{http.MethodPost, c.pcf.URL() + "/npcf-policyauthorization/v2/app-sessions", http.StatusNotFound, ""},
	} {
		a := raw(t, tc.method, tc.url, "", "")
		if a.status != tc.status || a.header.Get("Allow") != tc.allow {
			t.Errorf("%s %s: %d, Allow %q", tc.method, tc.url, a.status, a.header.Get("Allow"))
		}
	}
}

// Decision 3: HTTP/2 with prior knowledge only.
func TestHTTP1(t *testing.T) {
	p := New(t, Config{Logger: slog.New(slog.DiscardHandler)})

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.URL()+n5.AppSessionsPath, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", n5.ContentJSON)

	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("HTTP/1.1 answered %d", resp.StatusCode)
	}
}

// TS 29.514 §4.2.4.2
func TestDelete(t *testing.T) {
	c := newConsumer(t, Config{})
	created := c.create(c.call("c1"))
	c.pcf.Next(t)

	if _, err := c.client.Delete(context.Background(), created.URI, nil); err != nil {
		t.Fatal(err)
	}

	r := c.pcf.Next(t)
	if r.Op != n5.OpDelete || r.URI != created.URI || len(r.Context.MedComponents) != 1 {
		t.Fatalf("request %v", r)
	}

	if len(c.pcf.Contexts()) != 0 {
		t.Fatalf("contexts %v", c.pcf.Contexts())
	}

	// With events, and evsNotif to report: 200.
	created = c.create(c.call("c2"))
	c.pcf.Next(t)
	c.pcf.AnswerWith(func(r Request) *n5.EventsNotification {
		if r.Op != n5.OpDelete {
			return nil
		}

		return &n5.EventsNotification{EvSubsURI: r.URI + "/events-subscription", EvNotifs: []n5.AfEventNotification{{Event: n5.EventChargingCorrelation}}}
	})

	res, err := c.client.Delete(context.Background(), created.URI, &n5.EventsSubscReqData{Events: []n5.AfEventSubscription{{Event: n5.EventChargingCorrelation}}})
	if err != nil || res.Notification == nil || res.Notification.EvNotifs[0].Event != n5.EventChargingCorrelation {
		t.Fatalf("result %+v, error %v", res, err)
	}

	if r := c.pcf.Next(t); r.Delete == nil || r.Delete.Events[0].Event != n5.EventChargingCorrelation {
		t.Fatalf("request %v", r)
	}
}

func TestFailures(t *testing.T) {
	c := newConsumer(t, Config{})

	c.pcf.RefuseMedia(Failure{Status: http.StatusForbidden, Cause: n5.CauseRequestedServiceTemporarilyNotAuthorized, RetryAfter: 1500 * time.Millisecond})

	c.create(c.signalling("s1"))

	_, err := c.client.Create(context.Background(), c.call("c1"))

	var e *n5.Error
	if !errors.As(err, &e) || e.Status != http.StatusForbidden || e.Cause() != n5.CauseRequestedServiceTemporarilyNotAuthorized || e.RetryAfter != 2*time.Second {
		t.Fatalf("error %v", err)
	}

	if len(c.pcf.Contexts()) != 1 {
		t.Fatalf("contexts %v", c.pcf.Contexts())
	}

	c.pcf.RefuseWhen(func(Request) *Failure { return &Failure{Status: http.StatusServiceUnavailable} })

	if _, err := c.client.Create(context.Background(), c.signalling("s2")); err == nil {
		t.Fatal("503 accepted")
	} else if s, _ := status(t, err); s != http.StatusServiceUnavailable {
		t.Fatalf("status %d", s)
	}

	c.pcf.RefuseWhen(func(Request) *Failure { return &Failure{Drop: true} })

	if _, err := c.client.Create(context.Background(), c.signalling("s3")); err == nil {
		t.Fatal("dropped request answered")
	} else if s, _ := status(t, err); s != 0 {
		t.Fatalf("status %d", s)
	}
}

func TestFilterRestrictions(t *testing.T) {
	c := newConsumer(t, Config{})

	asc := c.call("c1")
	m := asc.AscReqData.MedComponents["1"]
	m.MedSubComps["1"] = n5.MediaSubComponent{FNum: 1, FDescs: []string{"permit out 17 from 192.0.2.20 50000 to 10.45.0.99 49000"}}

	_, err := c.client.Create(context.Background(), asc)
	if s, cause := status(t, err); s != http.StatusBadRequest || cause != n5.CauseFilterRestrictions {
		t.Fatalf("%d %s", s, cause)
	}
}

func TestHold(t *testing.T) {
	c := newConsumer(t, Config{})
	release := c.pcf.Hold()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if _, err := c.client.Create(ctx, c.signalling("s1")); err == nil {
		t.Fatal("held create answered")
	}

	done := make(chan error, 1)

	go func() {
		_, err := c.client.Create(context.Background(), c.signalling("s2"))
		done <- err
	}()

	c.pcf.Next(t)
	c.pcf.Next(t)
	release()

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if len(c.pcf.Contexts()) != 1 {
		t.Fatalf("contexts %v", c.pcf.Contexts())
	}
}

// TS 29.500 §6.10.9: the client follows the 308 and learns the new URI.
func TestMove(t *testing.T) {
	c := newConsumer(t, Config{})
	created := c.create(c.signalling("s1"))

	moved, err := c.pcf.Move(created.URI)
	if err != nil {
		t.Fatal(err)
	}

	res, err := c.client.Modify(context.Background(), created.URI, n5.Patch{})
	if err != nil || res.URI != moved {
		t.Fatalf("result %+v, error %v, want %s", res, err, moved)
	}

	if _, err := c.client.Delete(context.Background(), created.URI, nil); err != nil {
		t.Fatal(err)
	}

	if len(c.pcf.Contexts()) != 0 {
		t.Fatalf("contexts %v", c.pcf.Contexts())
	}
}

// TS 29.514 §4.2.5.3
func TestTerminate(t *testing.T) {
	c := newConsumer(t, Config{})
	created := c.create(c.call("c1"))

	resp, err := c.pcf.Terminate(context.Background(), created.URI, n5.TerminationPDUSessionTermination)
	if err != nil || resp.Status != http.StatusNoContent {
		t.Fatalf("response %+v, error %v", resp, err)
	}

	n := c.nextNotification()
	if n.method != http.MethodPost || n.path != "/n5/v1/sessions/c1/terminate" || n.contentType != n5.ContentJSON {
		t.Fatalf("notification %+v", n)
	}

	var info n5.TerminationInfo
	if err := json.Unmarshal(n.body, &info); err != nil || info.ResURI != created.URI || info.TermCause != n5.TerminationPDUSessionTermination {
		t.Fatalf("%s: %v", n.body, err)
	}

	// The context stays until the consumer deletes it.
	if _, ok := c.pcf.Context(created.URI); !ok {
		t.Fatal("context gone")
	}

	if !c.pcf.Forget(created.URI) || len(c.pcf.Contexts()) != 0 {
		t.Fatal("not forgotten")
	}

	c.status = http.StatusBadRequest
	created = c.create(c.call("c2"))

	resp, err = c.pcf.Terminate(context.Background(), created.URI, n5.TerminationAllSDFDeactivation)
	if err != nil || resp.Status != http.StatusBadRequest || resp.Problem == nil || resp.Problem.Cause != n5.CauseResourceContextNotFound {
		t.Fatalf("response %+v, error %v", resp, err)
	}
}

// TS 29.514 §4.2.5.2
func TestNotify(t *testing.T) {
	c := newConsumer(t, Config{})
	created := c.create(c.call("c1"))

	ev := n5.EventsNotification{
		EvNotifs: []n5.AfEventNotification{{Event: n5.EventFailedResourcesAllocation}},
		FailedResourcAllocReports: []n5.ResourcesAllocationInfo{{
			McResourcStatus: n5.ResourcesInactive, Flows: []n5.Flows{{MedCompN: 1}},
		}},
	}

	resp, err := c.pcf.Notify(context.Background(), created.URI, ev)
	if err != nil || resp.Status != http.StatusNoContent {
		t.Fatalf("response %+v, error %v", resp, err)
	}

	n := c.nextNotification()
	if n.path != "/n5/v1/sessions/c1/notify" {
		t.Fatalf("notification %+v", n)
	}

	var got n5.EventsNotification
	if err := json.Unmarshal(n.body, &got); err != nil || got.EvSubsURI != created.URI+"/events-subscription" {
		t.Fatalf("%s: %v", n.body, err)
	}

	ev.EvNotifs[0].Event = n5.EventQoSNotif
	if _, err := c.pcf.Notify(context.Background(), created.URI, ev); err == nil {
		t.Fatal("notified an event without a subscription")
	}

	// The events subscription as patched.
	patch, err := n5.NewPatch(&n5.AppSessionContextUpdateData{EvSubsc: c.call("c1").AscReqData.EvSubsc}, &n5.AppSessionContextUpdateData{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.client.Modify(context.Background(), created.URI, patch); err != nil {
		t.Fatal(err)
	}

	ev.EvNotifs[0].Event = n5.EventFailedResourcesAllocation
	if _, err := c.pcf.Notify(context.Background(), created.URI, ev); err == nil {
		t.Fatal("notified a removed events subscription")
	}
}

// A notifUri with a trailing "/" is not hidden.
func TestNotifURISlash(t *testing.T) {
	c := newConsumer(t, Config{})

	asc := c.call("c1")
	asc.AscReqData.NotifURI += "/"
	created := c.create(asc)

	if _, err := c.pcf.Terminate(context.Background(), created.URI, n5.TerminationPDUSessionTermination); err != nil {
		t.Fatal(err)
	}

	if n := c.nextNotification(); n.path != "/n5/v1/sessions/c1//terminate" {
		t.Fatalf("path %q", n.path)
	}
}
