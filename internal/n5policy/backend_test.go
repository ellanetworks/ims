package n5policy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/n5"
	"github.com/ellanetworks/ims/internal/pcftest"
	"github.com/ellanetworks/ims/internal/policy"
)

var ue4 = netip.MustParseAddr("10.45.0.2")

type abortCall struct {
	id string
	a  policy.Abort
}

type notifyCall struct {
	id string
	e  policy.Event
}

type fakeSink struct {
	mu         sync.Mutex
	known      map[string]bool
	aborts     chan abortCall
	notifies   chan notifyCall
	terminated chan string
}

func newSink() *fakeSink {
	return &fakeSink{
		known: map[string]bool{}, aborts: make(chan abortCall, 8), notifies: make(chan notifyCall, 8),
		terminated: make(chan string, 8),
	}
}

func (s *fakeSink) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.known[id] = true
}

func (s *fakeSink) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.known[id]
}

func (s *fakeSink) Notify(id string, e policy.Event) bool {
	if !s.has(id) {
		return false
	}

	s.notifies <- notifyCall{id, e}

	return true
}

func (s *fakeSink) Abort(id string, a policy.Abort) (func(), bool) {
	if !s.has(id) {
		return nil, false
	}

	s.aborts <- abortCall{id, a}

	return func() { s.terminated <- id }, true
}

type fixture struct {
	t      *testing.T
	pcf    *pcftest.PCF
	b      *Backend
	sink   *fakeSink
	notify string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	pcf := pcftest.New(t, pcftest.Config{})

	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	root := "http://" + ln.Addr().String()

	b, err := New(Config{PCF: pcf.URL(), Notify: root, Logger: slog.New(slog.NewTextHandler(t.Output(), nil))})
	if err != nil {
		t.Fatal(err)
	}

	sink := newSink()
	b.Bind(sink)

	srv := NewServer(b, slog.New(slog.DiscardHandler))

	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(func() {
		_ = srv.Close()
		b.Close()
	})

	return &fixture{t: t, pcf: pcf, b: b, sink: sink, notify: root}
}

func (f *fixture) ctx() context.Context {
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	f.t.Cleanup(cancel)

	return ctx
}

func (f *fixture) noRequest() {
	f.t.Helper()

	select {
	case r := <-f.pcf.Requests():
		f.t.Fatalf("unexpected %s", r)
	case <-time.After(50 * time.Millisecond):
	}
}

func audio() policy.MediaComponent {
	return policy.MediaComponent{
		Number: 1, Type: policy.MediaAudio, Status: policy.FlowEnabled,
		MaxRequestedUL: new(uint64(49000)), MaxRequestedDL: new(uint64(49000)), RR: new(uint32(1837)), RS: new(uint32(612)),
		Codecs: []policy.Codec{
			{Uplink: true, SDP: "m=audio 49000 RTP/AVP 116\r\na=rtpmap:116 AMR-WB/16000/1\r\n"},
			{Answer: true, SDP: "m=audio 50000 RTP/AVP 116\r\na=rtpmap:116 AMR-WB/16000/1\r\n"},
		},
		SubComponents: []policy.SubComponent{
			{FlowNumber: 1, Flows: []policy.Flow{
				{Protocol: policy.ProtocolUDP, Source: netip.MustParsePrefix("192.0.2.20/32"), Destination: netip.MustParsePrefix("10.45.0.2/32"), DestinationPort: 49000},
				{Uplink: true, Protocol: policy.ProtocolUDP, Source: netip.MustParsePrefix("10.45.0.2/32"), Destination: netip.MustParsePrefix("192.0.2.20/32"), DestinationPort: 50000},
			}},
			{FlowNumber: 2, Usage: policy.FlowUsageRTCP},
		},
	}
}

func video() policy.MediaComponent {
	return policy.MediaComponent{Number: 2, Type: policy.MediaVideo, Status: policy.FlowEnabled, MaxRequestedUL: new(uint64(1e6))}
}

func callRequest(initial bool, cs ...policy.MediaComponent) policy.Request {
	return policy.Request{
		UE: ue4, Initial: initial, Service: "urn:urn-7:3gpp-service.ims.icsi.mmtel", Components: cs,
		Subscribers: []policy.Subscriber{{Kind: policy.SubscriberSIPURI, ID: "sip:a@ims"}, {Kind: policy.SubscriberE164, ID: "15551234567"}},
	}
}

// TS 29.514 §4.2.6.7, §4.2.4.2
func TestSignalling(t *testing.T) {
	f := newFixture(t)
	id := f.b.NewSessionID()

	uri, err := f.b.OpenSignalling(f.ctx(), id, policy.Signalling{UE: netip.MustParseAddr("2001:db8::1")}, false)
	if err != nil {
		t.Fatalf("OpenSignalling: %v", err)
	}

	r := f.pcf.Next(t)
	if r.Op != n5.OpCreate || r.URI != uri || !pcftest.Signalling(r.Context) {
		t.Fatalf("got %s, want the signalling create at %s", r, uri)
	}

	c := r.Context
	notif := f.notify + SessionsPath + "/" + id

	switch {
	case c.NotifURI != notif || c.EvSubsc == nil || c.EvSubsc.NotifURI != notif:
		t.Errorf("notifUri %q, evSubsc %+v, want %s in both", c.NotifURI, c.EvSubsc, notif)
	case len(c.EvSubsc.Events) != 1 || c.EvSubsc.Events[0].Event != n5.EventFailedResourcesAllocation:
		t.Errorf("events %+v, want FAILED_RESOURCES_ALLOCATION", c.EvSubsc.Events)
	case c.MedComponents["0"].MedType != "" || c.MedComponents["0"].MedSubComps["0"].FDescs != nil:
		t.Errorf("signalling component %+v, want nothing but medCompN, fNum and flowUsage", c.MedComponents["0"])
	case c.UEIPv6 != netip.MustParseAddr("2001:db8::1") || c.UEIPv4.IsValid() || c.SuppFeat != "8000010":
		t.Errorf("context %+v", c)
	}

	if err := f.b.Terminate(f.ctx(), id, uri, policy.TerminationLogout, false); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	if r := f.pcf.Next(t); r.Op != n5.OpDelete || r.URI != uri {
		t.Fatalf("got %s, want the delete of %s", r, uri)
	}

	if st := f.b.Status(); !st.Reachable || st.Result != "204" || st.PCF != f.pcf.URL() {
		t.Errorf("status %+v, want the PCF reachable after a 204", st)
	}
}

func TestNewSessionIDIsURLSafe(t *testing.T) {
	f := newFixture(t)

	for range 100 {
		id := f.b.NewSessionID()
		if len(id) != 22 || strings.ContainsFunc(id, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_'
		}) {
			t.Fatalf("session ID %q is not 22 unreserved characters", id)
		}
	}
}

// TS 29.514 §4.2.2.2, §4.2.3.2, TS 29.513 §7.2.3
func TestCall(t *testing.T) {
	f := newFixture(t)
	id := f.b.NewSessionID()

	g, err := f.b.Authorize(f.ctx(), id, "", callRequest(true, audio()))
	if err != nil {
		t.Fatalf("Authorize initial: %v", err)
	}

	r := f.pcf.Next(t)
	if r.Op != n5.OpCreate || r.URI != g.Ref {
		t.Fatalf("got %s, want a create at %s", r, g.Ref)
	}

	c := r.Context
	notif := f.notify + SessionsPath + "/" + id
	m := c.MedComponents["1"]
	rtp, rtcp := m.MedSubComps["1"], m.MedSubComps["2"]

	switch {
	case c.AFAppID != "urn:urn-7:3gpp-service.ims.icsi.mmtel" || c.GPSI != "msisdn-15551234567" || c.UEIPv4 != ue4:
		t.Errorf("context %+v", c)
	case c.NotifURI != notif || c.EvSubsc == nil || c.EvSubsc.NotifURI != notif || len(c.EvSubsc.Events) != 1:
		t.Errorf("notifUri %q, evSubsc %+v", c.NotifURI, c.EvSubsc)
	case m.MedCompN != 1 || m.MedType != n5.MediaAudio || m.FStatus != n5.FlowEnabled || *m.MarBwUl != 49000 || *m.RRBw != 1837:
		t.Errorf("media component %+v", m)
	case len(m.Codecs) != 2 || !strings.HasPrefix(m.Codecs[0], "uplink\noffer\nm=audio") || !strings.HasPrefix(m.Codecs[1], "downlink\nanswer\n"):
		t.Errorf("codecs %q", m.Codecs)
	case rtp.FNum != 1 || len(rtp.FDescs) != 2 || rtp.FDescs[0] != "permit out 17 from 192.0.2.20 to 10.45.0.2 49000" || rtp.FlowUsage != "":
		t.Errorf("RTP %+v", rtp)
	case rtcp.FNum != 2 || rtcp.FlowUsage != n5.FlowUsageRTCP:
		t.Errorf("RTCP %+v", rtcp)
	}

	if b, _ := json.Marshal(r.Create); strings.Contains(string(b), "sipForkInd") {
		t.Errorf("create %s with sipForkInd", b)
	}

	// The same service information: no PATCH.
	if g2, err := f.b.Authorize(f.ctx(), id, g.Ref, callRequest(false, audio())); err != nil || g2.Ref != g.Ref {
		t.Fatalf("Authorize unchanged = %+v, %v", g2, err)
	}

	f.noRequest()

	// TS 29.514 Annex B.3.2: forking.
	fork := callRequest(false, audio(), video())
	fork.Forking = policy.ForkingSeveralDialogues

	if _, err := f.b.Authorize(f.ctx(), id, g.Ref, fork); err != nil {
		t.Fatalf("Authorize forked: %v", err)
	}

	if r := f.pcf.Next(t); r.Op != n5.OpModify || r.Context.SipForkInd != n5.ForkingSeveralDialogues || len(r.Context.MedComponents) != 2 {
		t.Fatalf("got %s, want SEVERAL_DIALOGUES with both components", r)
	}

	// The video is removed: it stays at the PCF, REMOVED; sipForkInd goes.
	if _, err := f.b.Authorize(f.ctx(), id, g.Ref, callRequest(false, audio(),
		policy.MediaComponent{Number: 2, Type: policy.MediaVideo, Status: policy.FlowRemoved})); err != nil {
		t.Fatalf("Authorize removed: %v", err)
	}

	r = f.pcf.Next(t)
	if r.Op != n5.OpModify {
		t.Fatalf("got %s, want a modify", r)
	}

	v, ok := r.Context.MedComponents["2"]
	if !ok || v.FStatus != n5.FlowRemoved || v.MedType != n5.MediaVideo || v.MarBwUl == nil || r.Context.SipForkInd != "" {
		t.Fatalf("context %+v, want the video kept REMOVED and no sipForkInd", r.Context)
	}

	if strings.Contains(string(r.Patch), `"2":null`) {
		t.Fatalf("patch %s removes the video", r.Patch)
	}

	if err := f.b.Terminate(f.ctx(), id, g.Ref, policy.TerminationLogout, false); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	if r := f.pcf.Next(t); r.Op != n5.OpDelete || r.URI != g.Ref {
		t.Fatalf("got %s, want the delete", r)
	}
}

// TS 29.500 §6.10.9: a context moved for good is patched and deleted at its new URI.
func TestMovedContext(t *testing.T) {
	f := newFixture(t)
	id := f.b.NewSessionID()

	g, err := f.b.Authorize(f.ctx(), id, "", callRequest(true, audio()))
	if err != nil {
		t.Fatal(err)
	}

	f.pcf.Next(t)

	moved, err := f.pcf.Move(g.Ref)
	if err != nil {
		t.Fatal(err)
	}

	g2, err := f.b.Authorize(f.ctx(), id, g.Ref, callRequest(false, audio(), video()))
	if err != nil || g2.Ref != moved {
		t.Fatalf("Authorize = %+v, %v, want the new URI %s", g2, err, moved)
	}

	f.pcf.Next(t)

	if err := f.b.Terminate(f.ctx(), id, g.Ref, policy.TerminationLogout, false); err != nil {
		t.Fatal(err)
	}

	if r := f.pcf.Next(t); r.Op != n5.OpDelete || r.URI != moved {
		t.Fatalf("got %s, want the delete at %s", r, moved)
	}
}

func TestTerminateWithoutContext(t *testing.T) {
	f := newFixture(t)

	if err := f.b.Terminate(f.ctx(), f.b.NewSessionID(), "", policy.TerminationLogout, false); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	f.noRequest()
}

func TestErrors(t *testing.T) {
	f := newFixture(t)

	f.pcf.RefuseMedia(pcftest.Failure{Status: http.StatusForbidden, Cause: n5.CauseRequestedServiceNotAuthorized})

	_, err := f.b.Authorize(f.ctx(), f.b.NewSessionID(), "", callRequest(true, audio()))
	if !errors.Is(err, policy.ErrRefused) {
		t.Fatalf("Authorize = %v, want refused", err)
	}

	if res, _ := policy.ResultOf(err); res != "403 REQUESTED_SERVICE_NOT_AUTHORIZED" {
		t.Errorf("result %q", res)
	}

	f.pcf.Next(t)
	f.pcf.RefuseWhen(nil)

	id := f.b.NewSessionID()

	g, err := f.b.Authorize(f.ctx(), id, "", callRequest(true, audio()))
	if err != nil {
		t.Fatal(err)
	}

	f.pcf.Next(t)
	f.pcf.Forget(g.Ref)

	if _, err := f.b.Authorize(f.ctx(), id, g.Ref, callRequest(false, audio(), video())); !errors.Is(err, policy.ErrUnknownSession) {
		t.Fatalf("Authorize = %v, want the session unknown", err)
	}

	if _, ok := f.b.state(id); ok {
		t.Error("the backend keeps a context the PCF lost")
	}

	if st := f.b.Status(); !st.Reachable || st.Result != "404 APPLICATION_SESSION_CONTEXT_NOT_FOUND" {
		t.Errorf("status %+v", st)
	}

	b, err := New(Config{PCF: "http://127.0.0.1:1", Notify: "http://127.0.0.1:2"})
	if err != nil {
		t.Fatal(err)
	}

	defer b.Close()

	if _, err := b.OpenSignalling(f.ctx(), "s", policy.Signalling{UE: ue4}, false); !errors.Is(err, policy.ErrUnreachable) {
		t.Fatalf("OpenSignalling = %v, want unreachable", err)
	}

	if st := b.Status(); st.Reachable || st.At.IsZero() {
		t.Errorf("status %+v, want unreachable", st)
	}
}

// TS 29.514 §4.2.5.3
func TestTerminateNotification(t *testing.T) {
	f := newFixture(t)
	id := f.b.NewSessionID()

	uri, err := f.b.OpenSignalling(f.ctx(), id, policy.Signalling{UE: ue4}, false)
	if err != nil {
		t.Fatal(err)
	}

	f.pcf.Next(t)
	f.sink.add(id)

	res, err := f.pcf.Terminate(f.ctx(), uri, n5.TerminationInsufficientQoSFlowResources)
	if err != nil || res.Status != http.StatusNoContent {
		t.Fatalf("terminate = %+v, %v, want 204", res, err)
	}

	a := <-f.sink.aborts
	if a.id != id || a.a.Cause != string(n5.TerminationInsufficientQoSFlowResources) || !a.a.InsufficientResources {
		t.Fatalf("abort %+v", a)
	}

	if got := <-f.sink.terminated; got != id {
		t.Fatalf("terminated %s, want %s", got, id)
	}
}

// A context the P-CSCF no longer has a session for is deleted when the PCF terminates it.
func TestTerminateOrphan(t *testing.T) {
	f := newFixture(t)
	id := f.b.NewSessionID()

	uri, err := f.b.OpenSignalling(f.ctx(), id, policy.Signalling{UE: ue4}, false)
	if err != nil {
		t.Fatal(err)
	}

	f.pcf.Next(t)

	res, err := f.pcf.Terminate(f.ctx(), uri, n5.TerminationPDUSessionTermination)
	if err != nil || res.Status != http.StatusNoContent {
		t.Fatalf("terminate = %+v, %v, want 204", res, err)
	}

	if r := f.pcf.Next(t); r.Op != n5.OpDelete || r.URI != uri {
		t.Fatalf("got %s, want the delete of %s", r, uri)
	}

	// Nothing kept: a context of the PCF is still deleted, one elsewhere is not.
	post := func(path, body string) int {
		req, _ := http.NewRequestWithContext(f.ctx(), http.MethodPost, f.notify+path, strings.NewReader(body))
		req.Header.Set("Content-Type", n5.ContentJSON)

		resp, err := h2c().Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()

		return resp.StatusCode
	}

	other := f.pcf.URL() + n5.AppSessionsPath + "/elsewhere"
	if s := post(SessionsPath+"/gone/terminate", `{"termCause":"PDU_SESSION_TERMINATION","resUri":"`+other+`"}`); s != http.StatusNoContent {
		t.Fatalf("terminate of an unknown context of the PCF = %d, want 204", s)
	}

	// pcftest answers 404 to a context it never had, before recording a request.
	for deadline := time.Now().Add(5 * time.Second); f.b.Status().Result != "404 APPLICATION_SESSION_CONTEXT_NOT_FOUND"; {
		if time.Now().After(deadline) {
			t.Fatalf("status %+v, want the delete of %s answered", f.b.Status(), other)
		}

		time.Sleep(10 * time.Millisecond)
	}

	if s := post(SessionsPath+"/gone/terminate", `{"termCause":"PDU_SESSION_TERMINATION","resUri":"http://192.0.2.1/npcf-policyauthorization/v1/app-sessions/x"}`); s != http.StatusBadRequest {
		t.Fatalf("terminate of a context elsewhere = %d, want 400", s)
	}

	f.noRequest()
}

func h2c() *http.Client {
	var p http.Protocols

	p.SetUnencryptedHTTP2(true)

	return &http.Client{Transport: &http.Transport{Protocols: &p}, Timeout: 5 * time.Second}
}

// TS 29.514 §4.2.5.2, §4.2.5.8, §4.2.5.10
func TestNotify(t *testing.T) {
	f := newFixture(t)
	id := f.b.NewSessionID()

	g, err := f.b.Authorize(f.ctx(), id, "", callRequest(true, audio(), video()))
	if err != nil {
		t.Fatal(err)
	}

	f.pcf.Next(t)
	f.sink.add(id)

	failed := []n5.AfEventNotification{{Event: n5.EventFailedResourcesAllocation}}

	for name, tc := range map[string]struct {
		n          n5.EventsNotification
		lost       bool
		components []uint32
	}{
		"inactive": {
			n5.EventsNotification{EvNotifs: failed, FailedResourcAllocReports: []n5.ResourcesAllocationInfo{
				{McResourcStatus: n5.ResourcesActive, Flows: []n5.Flows{{MedCompN: 1}}},
				{McResourcStatus: n5.ResourcesInactive, Flows: []n5.Flows{{MedCompN: 2, FNums: []uint32{1}}}},
			}},
			true,
			[]uint32{2},
		},
		"still active": {
			n5.EventsNotification{EvNotifs: failed, FailedResourcAllocReports: []n5.ResourcesAllocationInfo{
				{McResourcStatus: n5.ResourcesActive, Flows: []n5.Flows{{MedCompN: 1}}},
			}},
			false, nil,
		},
		"inactive without flows": {
			n5.EventsNotification{EvNotifs: failed, FailedResourcAllocReports: []n5.ResourcesAllocationInfo{
				{McResourcStatus: n5.ResourcesInactive},
			}},
			true, nil,
		},
		"flows without reports": {
			n5.EventsNotification{EvNotifs: []n5.AfEventNotification{{Event: n5.EventFailedResourcesAllocation, Flows: []n5.Flows{{MedCompN: 1}}}}},
			true,
			[]uint32{1},
		},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := f.pcf.Notify(f.ctx(), g.Ref, tc.n)
			if err != nil || res.Status != http.StatusNoContent {
				t.Fatalf("notify = %+v, %v, want 204", res, err)
			}

			n := <-f.sink.notifies
			if n.id != id || n.e.Has(policy.EventResourcesFailed) != tc.lost || !slices.Equal(n.e.Components, tc.components) {
				t.Fatalf("event %+v, want lost %v of %v", n.e, tc.lost, tc.components)
			}
		})
	}

	// Routed on evSubsUri when the local ID is unknown.
	body, _ := json.Marshal(n5.EventsNotification{EvSubsURI: g.Ref + "/events-subscription", EvNotifs: failed})
	req, _ := http.NewRequestWithContext(f.ctx(), http.MethodPost, f.notify+SessionsPath+"/other/notify", bytes.NewReader(body))
	req.Header.Set("Content-Type", n5.ContentJSON)

	resp, err := h2c().Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("notify on evSubsUri = %+v, %v, want 204", resp, err)
	}

	_ = resp.Body.Close()

	if n := <-f.sink.notifies; n.id != id {
		t.Fatalf("notified %s, want %s", n.id, id)
	}

	f.sink.mu.Lock()
	delete(f.sink.known, id)
	f.sink.mu.Unlock()

	res, err := f.pcf.Notify(f.ctx(), g.Ref, n5.EventsNotification{EvNotifs: failed})
	if err != nil || res.Status != http.StatusBadRequest || res.Problem == nil || res.Problem.Cause != n5.CauseResourceContextNotFound {
		t.Fatalf("notify for an unknown session = %+v, %v, want 400 RESOURCE_CONTEXT_NOT_FOUND", res, err)
	}
}

func TestNotificationRequests(t *testing.T) {
	f := newFixture(t)

	for name, tc := range map[string]struct {
		method, path, contentType, body string
		status                          int
	}{
		"other resource":    {http.MethodPost, "/n5/v1/other", n5.ContentJSON, "{}", http.StatusNotFound},
		"other operation":   {http.MethodPost, SessionsPath + "/s/update", n5.ContentJSON, "{}", http.StatusNotFound},
		"no session":        {http.MethodPost, SessionsPath + "//notify", n5.ContentJSON, "{}", http.StatusNotFound},
		"GET":               {http.MethodGet, SessionsPath + "/s/notify", "", "", http.StatusMethodNotAllowed},
		"not JSON":          {http.MethodPost, SessionsPath + "/s/notify", "text/plain", "{}", http.StatusUnsupportedMediaType},
		"bad JSON":          {http.MethodPost, SessionsPath + "/s/notify", n5.ContentJSON, "{", http.StatusBadRequest},
		"no evNotifs":       {http.MethodPost, SessionsPath + "/s/notify", n5.ContentJSON, `{"evSubsUri":"http://x/y"}`, http.StatusBadRequest},
		"no resUri":         {http.MethodPost, SessionsPath + "/s/terminate", n5.ContentJSON, `{"termCause":"PS_TO_CS_HO"}`, http.StatusBadRequest},
		"unknown terminate": {http.MethodPost, SessionsPath + "/s/terminate", n5.ContentJSON, `{"termCause":"PS_TO_CS_HO","resUri":"http://x/y"}`, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(f.ctx(), tc.method, f.notify+tc.path, strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}

			resp, err := h2c().Do(req)
			if err != nil {
				t.Fatal(err)
			}

			_ = resp.Body.Close()

			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}

			if tc.status == http.StatusMethodNotAllowed && resp.Header.Get("Allow") != http.MethodPost {
				t.Errorf("Allow %q", resp.Header.Get("Allow"))
			}
		})
	}
}
