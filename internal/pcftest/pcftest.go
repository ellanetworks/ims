// Package pcftest is a fake PCF for tests: the NF service producer side of Npcf_PolicyAuthorization (TS 29.514)
// over cleartext HTTP/2, or over mutually authenticated TLS. It follows TS 29.514 where Open5GS deviates, and it checks every request strictly: one
// that breaks TS 29.514 or TS 29.500 fails the test, unless Config.AllowViolations is set.
package pcftest

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/internal/n5"
)

// TS 29.500 Table 5.2.7.2-1
const (
	CauseInvalidMsgFormat     = "INVALID_MSG_FORMAT"
	CauseMandatoryIEIncorrect = "MANDATORY_IE_INCORRECT"
	CauseMandatoryIEMissing   = "MANDATORY_IE_MISSING"
)

const (
	maxBody                  = 1 << 20
	notificationTimeout      = 10 * time.Second
	eventsSubscriptionSuffix = "/" + n5.EventsSubscriptionSegment
	deleteSuffix             = "/" + n5.DeleteSegment
)

type Config struct {
	Address netip.Addr
	// Host is the host of the API root, a domain name that resolves to Address. It defaults to Address.
	Host string

	// TLS, if set, makes the PCF serve https and send notifications over TLS: it presents the certificate of
	// TLS, checks the consumer's against the CAs of TLS, and requires one from a client.
	TLS *tls.Config

	// UEs are the addresses with a PDU session. An IPv6 address binds to the /64 of one of them (TS 29.513
	// §6.2). Empty, every UE has one.
	UEs []netip.Addr

	// Features are the features the PCF supports; it negotiates their intersection with the consumer's
	// (TS 29.514 §5.8). They default to IMS_SBI and PatchCorrection.
	Features n5.SupportedFeatures

	// AllowViolations stops a request that breaks TS 29.514 or TS 29.500 from failing the test.
	AllowViolations bool

	Logger *slog.Logger
}

// Context is an Individual Application Session Context as the PCF holds it: its service information with every
// patch merged in.
type Context struct {
	URI string
	n5.AppSessionContextReqData
	// SipForkInd is only in the AppSessionContextUpdateData of a patch, and so in the merged context.
	SipForkInd n5.SipForkingIndication `json:"sipForkInd,omitempty"`
}

// Request is a create, modify or delete that reached the PCF.
type Request struct {
	Op n5.Op
	// URI is the context the request targets. For a create, it is the URI the context gets if it is created.
	URI    string
	Create *n5.AppSessionContext
	Patch  json.RawMessage
	Delete *n5.EventsSubscReqData
	// Context is the context a create or modify leads to, or the one a delete ends.
	Context Context
	// Problem is set when the PCF refused the request itself, rather than through RefuseWhen.
	Problem *n5.ProblemDetails
}

func (r Request) String() string {
	switch r.Op {
	case n5.OpCreate:
		b, _ := json.Marshal(r.Create)
		return fmt.Sprintf("create %s %s", r.URI, b)
	case n5.OpModify:
		return fmt.Sprintf("modify %s %s", r.URI, r.Patch)
	case n5.OpDelete:
		return fmt.Sprintf("delete %s", r.URI)
	}

	return "empty request"
}

// Failure is an error the PCF answers instead of carrying out a request.
type Failure struct {
	Status     int
	Cause      string
	Detail     string
	RetryAfter time.Duration
	// Drop resets the stream without an answer.
	Drop bool
}

// Response is what the consumer answered to a notification.
type Response struct {
	Status  int
	Header  http.Header
	Problem *n5.ProblemDetails
}

type appSession struct {
	id  string
	doc json.RawMessage
}

type PCF struct {
	cfg    Config
	addr   netip.AddrPort
	root   string
	server *http.Server
	client *http.Client

	requests chan Request

	mu         sync.Mutex
	dropped    int
	violations []string
	nextID     int
	sessions   map[string]*appSession
	moved      map[string]string
	refuse     func(Request) *Failure
	answer     func(Request) *n5.EventsNotification
	hold       chan struct{}
}

func New(t testing.TB, cfg Config) *PCF {
	t.Helper()

	if !cfg.Address.IsValid() {
		cfg.Address = netip.MustParseAddr("127.0.0.1")
	}

	if cfg.Features == "" {
		cfg.Features = n5.Features(n5.FeatureIMSSBI, n5.FeaturePatchCorrection)
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", netip.AddrPortFrom(cfg.Address, 0).String())
	if err != nil {
		t.Fatalf("pcftest: %v", err)
	}

	var protocols http.Protocols

	transport := &http.Transport{Protocols: &protocols}
	scheme := "http"

	if cfg.TLS == nil {
		protocols.SetUnencryptedHTTP2(true)
	} else {
		protocols.SetHTTP2(true)

		scheme = "https"
		transport.TLSClientConfig = cfg.TLS.Clone()
		transport.TLSClientConfig.NextProtos = []string{"h2"}
	}

	p := &PCF{
		cfg:      cfg,
		addr:     netip.MustParseAddrPort(ln.Addr().String()),
		client:   &http.Client{Transport: transport, Timeout: notificationTimeout},
		requests: make(chan Request, 1024),
		sessions: make(map[string]*appSession),
		moved:    make(map[string]string),
	}
	p.root = scheme + "://" + p.addr.String()
	if cfg.Host != "" {
		p.root = scheme + "://" + net.JoinHostPort(cfg.Host, strconv.Itoa(int(p.addr.Port())))
	}

	p.server = &http.Server{Handler: http.HandlerFunc(p.serve), Protocols: &protocols, ErrorLog: slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelWarn)}

	go func() {
		if cfg.TLS == nil {
			_ = p.server.Serve(ln)
			return
		}

		p.server.TLSConfig = cfg.TLS.Clone()
		_ = p.server.ServeTLS(ln, "", "")
	}()

	t.Cleanup(func() {
		_ = p.server.Close()

		transport.CloseIdleConnections()

		if n := p.Dropped(); n > 0 {
			t.Errorf("pcftest: %d N5 requests dropped from the full request channel", n)
		}

		if v := p.Violations(); len(v) > 0 && !cfg.AllowViolations {
			t.Errorf("pcftest: requests that break TS 29.514:\n%s", strings.Join(v, "\n"))
		}
	})

	return p
}

func (p *PCF) Addr() netip.AddrPort {
	return p.addr
}

// URL is the API root of the PCF.
func (p *PCF) URL() string {
	return p.root
}

func (p *PCF) Requests() <-chan Request {
	return p.requests
}

func (p *PCF) Dropped() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.dropped
}

// Violations lists the requests that broke TS 29.514 or TS 29.500, and why.
func (p *PCF) Violations() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.violations)
}

func (p *PCF) record(r Request) {
	select {
	case p.requests <- r:
	default:
		p.mu.Lock()
		p.dropped++
		p.mu.Unlock()
	}
}

func (p *PCF) Next(t testing.TB) Request {
	t.Helper()

	select {
	case r := <-p.requests:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("pcftest: timed out waiting for an N5 request")
	}

	return Request{}
}

// Context returns the context at uri.
func (p *PCF) Context(uri string) (Context, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.sessions[p.id(uri)]
	if !ok {
		return Context{}, false
	}

	c, err := p.decode(s)

	return c, err == nil
}

// Contexts returns every context the PCF holds.
func (p *PCF) Contexts() []Context {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]Context, 0, len(p.sessions))

	for _, s := range p.sessions {
		if c, err := p.decode(s); err == nil {
			out = append(out, c)
		}
	}

	slices.SortFunc(out, func(a, b Context) int { return strings.Compare(a.URI, b.URI) })

	return out
}

// RefuseWhen answers f's failure, if any, to a create, modify or delete that passes the PCF's own checks.
func (p *PCF) RefuseWhen(f func(Request) *Failure) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.refuse = f
}

// RefuseMedia refuses every create and modify but those of signalling contexts (TS 29.514 §4.2.6.7).
func (p *PCF) RefuseMedia(f Failure) {
	p.RefuseWhen(func(r Request) *Failure {
		if r.Op == n5.OpDelete || Signalling(r.Context) {
			return nil
		}

		return &f
	})
}

// AnswerWith sets the evsNotif of the answer to a create, modify or delete. An answer to a delete with events is a
// 200 rather than a 204 (TS 29.514 §4.2.4.2).
func (p *PCF) AnswerWith(f func(Request) *n5.EventsNotification) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.answer = f
}

// Hold delays the answer to every create and modify until release is called.
func (p *PCF) Hold() (release func()) {
	hold := make(chan struct{})

	p.mu.Lock()
	p.hold = hold
	p.mu.Unlock()

	var once sync.Once

	return func() {
		once.Do(func() {
			p.mu.Lock()
			if p.hold == hold {
				p.hold = nil
			}
			p.mu.Unlock()

			close(hold)
		})
	}
}

// Move gives the context at uri a new URI. Requests to the old one are permanently redirected
// (TS 29.500 §6.10.9).
func (p *PCF) Move(uri string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	old := p.id(uri)

	s, ok := p.sessions[old]
	if !ok {
		return "", fmt.Errorf("pcftest: no context at %q", uri)
	}

	delete(p.sessions, old)

	s.id = p.newID()
	p.sessions[s.id] = s
	p.moved[old] = s.id

	for from, to := range p.moved {
		if to == old {
			p.moved[from] = s.id
		}
	}

	return p.uri(s.id), nil
}

// Forget drops the context at uri without a notification, as the Open5GS PCF does at PDU session release.
func (p *PCF) Forget(uri string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	id := p.id(uri)
	_, ok := p.sessions[id]
	delete(p.sessions, id)

	return ok
}

// Signalling reports whether c is the context of the AF signalling flows (TS 29.514 §4.2.6.7): media component 0,
// with only AF_SIGNALLING sub-components.
func Signalling(c Context) bool {
	if len(c.MedComponents) == 0 {
		return false
	}

	for _, m := range c.MedComponents {
		if m.MedCompN != 0 || len(m.MedSubComps) == 0 {
			return false
		}

		for _, s := range m.MedSubComps {
			if s.FlowUsage != n5.FlowUsageAFSignalling {
				return false
			}
		}
	}

	return true
}

// Terminate asks the consumer to delete the context at uri (TS 29.514 §4.2.5.3). The context stays until the
// consumer deletes it.
func (p *PCF) Terminate(ctx context.Context, uri string, cause n5.TerminationCause) (Response, error) {
	c, ok := p.Context(uri)
	if !ok {
		return Response{}, fmt.Errorf("pcftest: no context at %q", uri)
	}

	return p.notify(ctx, c.NotifURI+"/"+n5.TerminateSegment, n5.TerminationInfo{TermCause: cause, ResURI: c.URI})
}

// Notify reports events of the context at uri to its events subscription (TS 29.514 §4.2.5.2). Every event must
// be subscribed to. An empty evSubsUri is the context's Events Subscription sub-resource.
func (p *PCF) Notify(ctx context.Context, uri string, n n5.EventsNotification) (Response, error) {
	c, ok := p.Context(uri)
	if !ok {
		return Response{}, fmt.Errorf("pcftest: no context at %q", uri)
	}

	if c.EvSubsc == nil {
		return Response{}, fmt.Errorf("pcftest: %q has no events subscription", uri)
	}

	for _, e := range n.EvNotifs {
		if !slices.ContainsFunc(c.EvSubsc.Events, func(s n5.AfEventSubscription) bool { return s.Event == e.Event }) {
			return Response{}, fmt.Errorf("pcftest: %q is not subscribed to %s", uri, e.Event)
		}
	}

	if n.EvSubsURI == "" {
		n.EvSubsURI = c.URI + eventsSubscriptionSuffix
	}

	if n.EvNotifs == nil {
		n.EvNotifs = []n5.AfEventNotification{}
	}

	return p.notify(ctx, c.EvSubsc.NotifURI+"/"+n5.NotifySegment, n)
}

// notify sends a notification to the URI it is given, appending to the notifUri as the PCF does, so that a
// notifUri with a trailing "/" shows.
func (p *PCF) notify(ctx context.Context, target string, body any) (Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return Response{}, err
	}

	req.Header.Set("Content-Type", n5.ContentJSON)
	req.Header.Set("Accept", n5.ContentProblem)
	req.Header.Set("User-Agent", "PCF")

	resp, err := p.client.Do(req)
	if err != nil {
		return Response{}, err
	}

	defer func() { _ = resp.Body.Close() }()

	rb, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Response{}, err
	}

	out := Response{Status: resp.StatusCode, Header: resp.Header}

	if len(rb) > 0 && mediaType(resp.Header) == n5.ContentProblem {
		var pd n5.ProblemDetails
		if err := json.Unmarshal(rb, &pd); err == nil {
			out.Problem = &pd
		}
	}

	return out, nil
}

func (p *PCF) newID() string {
	p.nextID++
	return "asc-" + strconv.Itoa(p.nextID)
}

func (p *PCF) uri(id string) string {
	return p.root + n5.AppSessionsPath + "/" + id
}

// id returns the ID of a context URI, or "".
func (p *PCF) id(uri string) string {
	id, ok := strings.CutPrefix(uri, p.root+n5.AppSessionsPath+"/")
	if !ok || id == "" || strings.Contains(id, "/") {
		return ""
	}

	return id
}

func (p *PCF) decode(s *appSession) (Context, error) {
	c, err := parseContext(s.doc)
	c.URI = p.uri(s.id)

	return c, err
}

func parseContext(doc json.RawMessage) (Context, error) {
	var c Context

	err := json.Unmarshal(doc, &c)

	return c, err
}

// TS 29.514 §5.3: the resources and methods of the API that a P-CSCF uses.
func (p *PCF) serve(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, n5.AppSessionsPath)

	switch {
	case !ok:
		p.violation(w, r, n5.ProblemDetails{Status: http.StatusNotFound, Detail: "no such resource"})
	case rest == "" || rest == "/":
		if allow(w, r, http.MethodPost) {
			p.create(w, r)
		}
	default:
		id, op, _ := strings.Cut(strings.TrimPrefix(rest, "/"), "/")

		switch op {
		case "":
			if allow(w, r, http.MethodGet, http.MethodPatch) {
				p.session(w, r, id, "", p.individual)
			}
		case n5.DeleteSegment:
			if allow(w, r, http.MethodPost) {
				p.session(w, r, id, deleteSuffix, p.delete)
			}
		default:
			p.violation(w, r, n5.ProblemDetails{Status: http.StatusNotFound, Detail: "no such resource"})
		}
	}
}

// TS 29.500 §5.2.7.2: 405 with the methods of the resource.
func allow(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	if slices.Contains(methods, r.Method) {
		return true
	}

	w.Header().Set("Allow", strings.Join(methods, ", "))
	n5.WriteProblem(w, n5.ProblemDetails{Status: http.StatusMethodNotAllowed})

	return false
}

// session routes a request to an Individual Application Session Context: a moved one is redirected, an unknown
// one is APPLICATION_SESSION_CONTEXT_NOT_FOUND (TS 29.514 §5.7.3).
func (p *PCF) session(w http.ResponseWriter, r *http.Request, id, suffix string, h func(http.ResponseWriter, *http.Request, string)) {
	p.mu.Lock()
	to, moved := p.moved[id]
	_, known := p.sessions[id]
	p.mu.Unlock()

	switch {
	case moved:
		w.Header().Set("Location", p.uri(to)+suffix)
		w.WriteHeader(http.StatusPermanentRedirect)
	case !known:
		n5.WriteProblem(w, n5.ProblemDetails{Status: http.StatusNotFound, Cause: n5.CauseAppSessionContextNotFound})
	default:
		h(w, r, id)
	}
}

func (p *PCF) individual(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodPatch {
		p.modify(w, r, id)
		return
	}

	p.mu.Lock()
	s, ok := p.sessions[id]
	p.mu.Unlock()

	if !ok {
		n5.WriteProblem(w, n5.ProblemDetails{Status: http.StatusNotFound, Cause: n5.CauseAppSessionContextNotFound})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ascReqData": s.doc})
}

// TS 29.514 §4.2.2.2
func (p *PCF) create(w http.ResponseWriter, r *http.Request) {
	body, ok := p.body(w, r, n5.ContentJSON, true)
	if !ok {
		return
	}

	var raw struct {
		AscReqData json.RawMessage `json:"ascReqData"`
	}

	var asc n5.AppSessionContext

	if err := json.Unmarshal(body, &raw); err != nil {
		p.violation(w, r, invalid(CauseInvalidMsgFormat, "", err.Error()))
		return
	}

	if err := json.Unmarshal(body, &asc); err != nil {
		p.violation(w, r, invalid(CauseInvalidMsgFormat, "", err.Error()))
		return
	}

	p.mu.Lock()
	id := p.newID()
	p.mu.Unlock()

	req := Request{Op: n5.OpCreate, URI: p.uri(id), Create: &asc}

	if len(raw.AscReqData) == 0 || string(raw.AscReqData) == "null" {
		req.Problem = ptr(invalid(CauseMandatoryIEMissing, "/ascReqData", "no ascReqData"))
		p.record(req)
		p.violation(w, r, *req.Problem)

		return
	}

	c, err := parseContext(raw.AscReqData)
	if err != nil {
		req.Problem = ptr(invalid(CauseInvalidMsgFormat, "/ascReqData", err.Error()))
		p.record(req)
		p.violation(w, r, *req.Problem)

		return
	}

	c.URI = req.URI
	req.Context = c

	if pd := p.checkCreate(c.AppSessionContextReqData); pd != nil {
		req.Problem = pd
		p.record(req)
		p.violation(w, r, *pd)

		return
	}

	if !p.bound(c.AppSessionContextReqData) {
		req.Problem = &n5.ProblemDetails{Status: http.StatusInternalServerError, Cause: n5.CausePDUSessionNotAvailable}
		p.record(req)
		n5.WriteProblem(w, *req.Problem)

		return
	}

	if err := checkFlows(c.AppSessionContextReqData); err != nil {
		req.Problem = &n5.ProblemDetails{Status: http.StatusBadRequest, Cause: n5.CauseFilterRestrictions, Detail: err.Error()}
		p.cfg.Logger.Warn("pcftest: refused flow description", slog.String("uri", req.URI), slog.Any("error", err))
		p.record(req)
		n5.WriteProblem(w, *req.Problem)

		return
	}

	p.record(req)

	if !p.wait(r) {
		return
	}

	evs, ok := p.refused(w, req)
	if !ok {
		return
	}

	features := c.SuppFeat.Intersect(p.cfg.Features)

	p.mu.Lock()
	p.sessions[id] = &appSession{id: id, doc: raw.AscReqData}
	p.mu.Unlock()

	w.Header().Set("Location", req.URI)
	writeJSON(w, http.StatusCreated, map[string]any{
		"ascReqData":  raw.AscReqData,
		"ascRespData": n5.AppSessionContextRespData{SuppFeat: features},
		"evsNotif":    evs,
	})
}

// TS 29.514 §4.2.3.2
func (p *PCF) modify(w http.ResponseWriter, r *http.Request, id string) {
	patch, ok := p.body(w, r, n5.ContentMergePatch, true)
	if !ok {
		return
	}

	req := Request{Op: n5.OpModify, URI: p.uri(id), Patch: patch}

	if err := n5.CheckPatch(patch); err != nil {
		req.Problem = ptr(invalid(CauseInvalidMsgFormat, "", err.Error()))
		p.record(req)
		p.violation(w, r, *req.Problem)

		return
	}

	c, pd := p.merge(id, patch)
	if pd != nil {
		req.Problem = pd
		p.record(req)

		if pd.Status == http.StatusNotFound {
			n5.WriteProblem(w, *pd)
		} else {
			p.violation(w, r, *pd)
		}

		return
	}

	req.Context = c

	if err := checkFlows(c.AppSessionContextReqData); err != nil {
		req.Problem = &n5.ProblemDetails{Status: http.StatusBadRequest, Cause: n5.CauseFilterRestrictions, Detail: err.Error()}
		p.cfg.Logger.Warn("pcftest: refused flow description", slog.String("uri", req.URI), slog.Any("error", err))
		p.record(req)
		n5.WriteProblem(w, *req.Problem)

		return
	}

	p.record(req)

	if !p.wait(r) {
		return
	}

	evs, ok := p.refused(w, req)
	if !ok {
		return
	}

	// Merged again, onto the patches that landed while this one was held.
	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.sessions[id]
	if !ok {
		n5.WriteProblem(w, n5.ProblemDetails{Status: http.StatusNotFound, Cause: n5.CauseAppSessionContextNotFound})
		return
	}

	doc, _, pd := p.mergeDoc(s.doc, patch)
	if pd != nil {
		n5.WriteProblem(w, *pd)
		return
	}

	s.doc = doc

	writeJSON(w, http.StatusOK, map[string]any{"ascReqData": doc, "evsNotif": evs})
}

// merge returns the context the patch leads to, without storing it.
func (p *PCF) merge(id string, patch []byte) (Context, *n5.ProblemDetails) {
	p.mu.Lock()
	s, ok := p.sessions[id]

	var doc json.RawMessage
	if ok {
		doc = s.doc
	}
	p.mu.Unlock()

	if !ok {
		return Context{}, &n5.ProblemDetails{Status: http.StatusNotFound, Cause: n5.CauseAppSessionContextNotFound}
	}

	_, c, pd := p.mergeDoc(doc, patch)
	c.URI = p.uri(id)

	return c, pd
}

// mergeDoc applies a patch to the ascReqData of a context, and checks the result.
func (p *PCF) mergeDoc(doc json.RawMessage, patch []byte) (json.RawMessage, Context, *n5.ProblemDetails) {
	merged, err := n5.ApplyPatch(doc, patchedReqData(patch))
	if err != nil {
		return nil, Context{}, ptr(invalid(CauseInvalidMsgFormat, "", err.Error()))
	}

	c, err := parseContext(merged)
	if err != nil {
		return nil, Context{}, ptr(invalid(CauseInvalidMsgFormat, "/ascReqData", err.Error()))
	}

	if pd := p.checkContext(c.AppSessionContextReqData); pd != nil {
		return nil, Context{}, pd
	}

	return merged, c, nil
}

// patchedReqData returns the ascReqData of an AppSessionContextUpdateDataPatch, as a merge patch of its own.
func patchedReqData(patch []byte) []byte {
	var p struct {
		AscReqData json.RawMessage `json:"ascReqData"`
	}

	if json.Unmarshal(patch, &p) != nil || len(p.AscReqData) == 0 {
		return []byte("{}")
	}

	return p.AscReqData
}

// TS 29.514 §4.2.4.2
func (p *PCF) delete(w http.ResponseWriter, r *http.Request, id string) {
	body, ok := p.body(w, r, n5.ContentJSON, false)
	if !ok {
		return
	}

	req := Request{Op: n5.OpDelete, URI: p.uri(id)}

	if c, ok := p.Context(req.URI); ok {
		req.Context = c
	}

	if len(body) > 0 {
		var ev n5.EventsSubscReqData

		if err := json.Unmarshal(body, &ev); err != nil {
			req.Problem = ptr(invalid(CauseInvalidMsgFormat, "", err.Error()))
		} else if req.Delete = &ev; len(ev.Events) == 0 {
			req.Problem = ptr(invalid(CauseMandatoryIEMissing, "/events", "no events"))
		}

		if req.Problem != nil {
			p.record(req)
			p.violation(w, r, *req.Problem)

			return
		}
	}

	p.record(req)

	evs, ok := p.refused(w, req)
	if !ok {
		return
	}

	p.mu.Lock()
	_, known := p.sessions[id]
	delete(p.sessions, id)
	p.mu.Unlock()

	switch {
	case !known:
		n5.WriteProblem(w, n5.ProblemDetails{Status: http.StatusNotFound, Cause: n5.CauseAppSessionContextNotFound})
	case evs != nil:
		writeJSON(w, http.StatusOK, map[string]any{"evsNotif": evs})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// body reads a request body of the given content type (TS 29.514 §5.2.2.2, TS 29.500 §5.2.7.2).
func (p *PCF) body(w http.ResponseWriter, r *http.Request, contentType string, required bool) ([]byte, bool) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))

	switch {
	case err != nil:
		p.cfg.Logger.Warn("pcftest: cannot read the request", slog.Any("error", err))
		return nil, false
	case len(b) > maxBody:
		p.violation(w, r, n5.ProblemDetails{Status: http.StatusRequestEntityTooLarge})
		return nil, false
	case len(b) == 0 && !required:
		return nil, true
	case len(b) == 0:
		p.violation(w, r, invalid(CauseMandatoryIEMissing, "", "no body"))
		return nil, false
	}

	if t, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || t != contentType {
		if r.Method == http.MethodPatch {
			w.Header().Set("Accept-Patch", n5.ContentMergePatch)
		}

		p.violation(w, r, n5.ProblemDetails{
			Status: http.StatusUnsupportedMediaType,
			Detail: fmt.Sprintf("content type %q, want %s", r.Header.Get("Content-Type"), contentType),
		})

		return nil, false
	}

	return b, true
}

// wait holds an answer until Hold is released or the request is cancelled.
func (p *PCF) wait(r *http.Request) bool {
	p.mu.Lock()
	hold := p.hold
	p.mu.Unlock()

	if hold == nil {
		return true
	}

	select {
	case <-hold:
		return true
	case <-r.Context().Done():
		return false
	}
}

// refused answers an injected failure, if any. Otherwise it returns the evsNotif of the answer.
func (p *PCF) refused(w http.ResponseWriter, req Request) (*n5.EventsNotification, bool) {
	p.mu.Lock()
	refuse, answer := p.refuse, p.answer
	p.mu.Unlock()

	if refuse != nil {
		if f := refuse(req); f != nil {
			if f.Drop {
				panic(http.ErrAbortHandler)
			}

			if f.RetryAfter > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(int((f.RetryAfter+time.Second-1)/time.Second)))
			}

			n5.WriteProblem(w, n5.ProblemDetails{Status: f.Status, Cause: f.Cause, Detail: f.Detail})

			return nil, false
		}
	}

	if answer != nil {
		return answer(req), true
	}

	return nil, true
}

// violation answers a request that breaks TS 29.514 or TS 29.500, and keeps it for the test to fail on.
func (p *PCF) violation(w http.ResponseWriter, r *http.Request, pd n5.ProblemDetails) {
	msg := fmt.Sprintf("%s %s: %d %s %s", r.Method, r.URL.Path, pd.Status, pd.Cause, pd.Detail)

	for _, ip := range pd.InvalidParams {
		msg += fmt.Sprintf(" [%s: %s]", ip.Param, ip.Reason)
	}

	p.mu.Lock()
	p.violations = append(p.violations, msg)
	p.mu.Unlock()

	p.cfg.Logger.Warn("pcftest: request breaks TS 29.514", slog.String("violation", msg))
	n5.WriteProblem(w, pd)
}

func invalid(cause, param, reason string) n5.ProblemDetails {
	pd := n5.ProblemDetails{Status: http.StatusBadRequest, Cause: cause, Detail: reason}
	if param != "" {
		pd.InvalidParams = []n5.InvalidParam{{Param: param, Reason: reason}}
	}

	return pd
}

// checkCreate checks the AppSessionContextReqData of a create (TS 29.514 §4.2.2.2, §5.6.2.3).
func (p *PCF) checkCreate(r n5.AppSessionContextReqData) *n5.ProblemDetails {
	switch {
	case r.SuppFeat == "":
		return ptr(invalid(CauseMandatoryIEMissing, "/ascReqData/suppFeat", "no suppFeat"))
	case !r.SuppFeat.Valid():
		return ptr(invalid(CauseMandatoryIEIncorrect, "/ascReqData/suppFeat", "invalid suppFeat"))
	case !r.UEIPv4.IsValid() && !r.UEIPv6.IsValid():
		return ptr(invalid(CauseMandatoryIEMissing, "/ascReqData/ueIpv4", "no UE address"))
	case r.UEIPv4.IsValid() && r.UEIPv6.IsValid():
		return ptr(invalid(CauseMandatoryIEIncorrect, "/ascReqData/ueIpv6", "both ueIpv4 and ueIpv6"))
	case r.UEIPv4.IsValid() && !r.UEIPv4.Is4():
		return ptr(invalid(CauseMandatoryIEIncorrect, "/ascReqData/ueIpv4", "not an IPv4 address"))
	case r.UEIPv6.IsValid() && (!r.UEIPv6.Is6() || r.UEIPv6.Is4In6() || r.UEIPv6.Zone() != ""):
		return ptr(invalid(CauseMandatoryIEIncorrect, "/ascReqData/ueIpv6", "not an IPv6 address"))
	}

	return p.checkContext(r)
}

// checkContext checks what a create and a patch can both break: the notification URIs (TS 29.514 §4.2.2.2,
// §4.2.3.2), which have the scheme of the PCF, and the map keys (§5.6.2.3, §5.6.2.7).
func (p *PCF) checkContext(r n5.AppSessionContextReqData) *n5.ProblemDetails {
	if r.NotifURI == "" {
		return ptr(invalid(CauseMandatoryIEMissing, "/ascReqData/notifUri", "no notifUri"))
	}

	if !p.notifURI(r.NotifURI) {
		return ptr(invalid(CauseMandatoryIEIncorrect, "/ascReqData/notifUri", "not an absolute URI of the PCF's scheme"))
	}

	if e := r.EvSubsc; e != nil {
		switch {
		case len(e.Events) == 0:
			return ptr(invalid(CauseMandatoryIEMissing, "/ascReqData/evSubsc/events", "no events"))
		case e.NotifURI == "":
			return ptr(invalid(CauseMandatoryIEMissing, "/ascReqData/evSubsc/notifUri", "no notifUri"))
		case !p.notifURI(e.NotifURI):
			return ptr(invalid(CauseMandatoryIEIncorrect, "/ascReqData/evSubsc/notifUri", "not an absolute URI of the PCF's scheme"))
		}
	}

	for k, m := range r.MedComponents {
		if k != strconv.FormatUint(uint64(m.MedCompN), 10) {
			return ptr(invalid(CauseInvalidMsgFormat, "/ascReqData/medComponents/"+k, "key is not medCompN"))
		}

		for f, s := range m.MedSubComps {
			if f != strconv.FormatUint(uint64(s.FNum), 10) {
				return ptr(invalid(CauseInvalidMsgFormat, "/ascReqData/medComponents/"+k+"/medSubComps/"+f, "key is not fNum"))
			}
		}
	}

	return nil
}

func (p *PCF) notifURI(s string) bool {
	u, err := url.Parse(s)
	return err == nil && strings.HasPrefix(p.root, u.Scheme+"://") && u.Host != "" && u.RawQuery == "" && u.Fragment == ""
}

// bound applies session binding by UE address (TS 29.513 §6.2): an IPv6 address binds to the prefix of the PDU
// session, here a /64.
func (p *PCF) bound(r n5.AppSessionContextReqData) bool {
	if len(p.cfg.UEs) == 0 {
		return true
	}

	ue := r.UEIPv4
	if !ue.IsValid() {
		ue = r.UEIPv6
	}

	for _, a := range p.cfg.UEs {
		if a == ue || ue.Is6() && a.Is6() && samePrefix64(a, ue) {
			return true
		}
	}

	return false
}

func samePrefix64(a, b netip.Addr) bool {
	pa, _ := a.Prefix(64)
	pb, _ := b.Prefix(64)

	return pa == pb
}

// The flow description checks of the Open5GS PCF and SMF (lib/proto/types.c, lib/ipfw/ogs-ipfw.c), as in
// pcrftest: TS 29.514 §4.2.2.2 refuses them with FILTER_RESTRICTIONS.
func checkFlows(r n5.AppSessionContextReqData) error {
	ue := r.UEIPv4
	if !ue.IsValid() {
		ue = r.UEIPv6
	}

	for _, c := range r.MedComponents {
		for _, s := range c.MedSubComps {
			if len(s.FDescs) > 2 {
				return fmt.Errorf("media %d flow %d: %d flow descriptions", c.MedCompN, s.FNum, len(s.FDescs))
			}

			for _, d := range s.FDescs {
				f, err := rx.ParseFlowDescription(d)
				if err != nil {
					return err
				}

				side := f.Destination
				if f.Direction == rx.FlowDirectionIn {
					side = f.Source
				}

				switch {
				case f.DestinationPort == 0 && f.Destination.IsValid():
					return fmt.Errorf("%q: no destination port", d)
				case !side.IsValid() || !side.Contains(ue):
					return fmt.Errorf("%q: the UE %s is not on its side of the flow", d, ue)
				}
			}
		}
	}

	return nil
}

func writeJSON(w http.ResponseWriter, status int, v map[string]any) {
	for k, x := range v {
		if isNil(x) {
			delete(v, k)
		}
	}

	b, err := json.Marshal(v)
	if err != nil {
		n5.WriteProblem(w, n5.ProblemDetails{Detail: err.Error()})
		return
	}

	w.Header().Set("Content-Type", n5.ContentJSON)
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func isNil(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case *n5.EventsNotification:
		return x == nil
	case json.RawMessage:
		return x == nil
	}

	return false
}

func mediaType(h http.Header) string {
	t, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
	return t
}

func ptr[T any](v T) *T { return &v }
