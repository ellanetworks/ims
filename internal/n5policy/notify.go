package n5policy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/n5"
	"github.com/ellanetworks/ims/internal/policy"
	"github.com/ellanetworks/ims/internal/sbitls"
)

const maxNotification = 1 << 20

// TS 29.500 Table 5.2.7.2-1
const (
	causeInvalidMsgFormat   = "INVALID_MSG_FORMAT"
	causeMandatoryIEMissing = "MANDATORY_IE_MISSING"
)

// ServeHTTP receives the PCF's notifications (TS 29.514 §4.2.5.2, §4.2.5.3): POST {notifUri}/notify and
// {notifUri}/terminate, where notifUri ends with the local ID of the session.
func (b *Backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, SessionsPath+"/")
	id, op, _ := strings.Cut(rest, "/")

	if !ok || id == "" || op != n5.NotifySegment && op != n5.TerminateSegment {
		n5.WriteProblem(w, n5.ProblemDetails{Status: http.StatusNotFound, Detail: "no such resource"})
		return
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		n5.WriteProblem(w, n5.ProblemDetails{Status: http.StatusMethodNotAllowed})

		return
	}

	body, ok := readJSON(w, r)
	if !ok {
		return
	}

	sink := b.sink.Load()
	if sink == nil {
		n5.WriteProblem(w, n5.ProblemDetails{Status: http.StatusServiceUnavailable, Detail: "the P-CSCF is not running"})
		return
	}

	if op == n5.TerminateSegment {
		b.terminated(w, *sink, id, body)
	} else {
		b.notified(w, *sink, id, body)
	}
}

// readJSON reads a JSON body (TS 29.500 §5.2.7.2).
func readJSON(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if t, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || t != n5.ContentJSON {
		n5.WriteProblem(w, n5.ProblemDetails{Status: http.StatusUnsupportedMediaType, Detail: "want " + n5.ContentJSON})
		return nil, false
	}

	b, err := io.ReadAll(io.LimitReader(r.Body, maxNotification+1))

	switch {
	case err != nil:
		n5.WriteProblem(w, invalid(causeInvalidMsgFormat, err.Error()))
		return nil, false
	case len(b) > maxNotification:
		n5.WriteProblem(w, n5.ProblemDetails{Status: http.StatusRequestEntityTooLarge})
		return nil, false
	}

	return b, true
}

func invalid(cause, detail string) n5.ProblemDetails {
	return n5.ProblemDetails{Status: http.StatusBadRequest, Cause: cause, Detail: detail}
}

// TS 29.500 Table 5.2.7.2-1, §6.2.3: the PCF ends a subscription the P-CSCF does not know.
func unknownContext(w http.ResponseWriter) {
	n5.WriteProblem(w, n5.ProblemDetails{Status: http.StatusBadRequest, Cause: n5.CauseResourceContextNotFound})
}

// TS 29.514 §4.2.5.3: answer 204, then delete the context. The session is routed on its local ID, which only the
// PCF that holds its notifUri knows. A context the P-CSCF lost track of is deleted all the same: one it still holds
// the URI of, or the one an unanswered create made, at the resUri of the configured PCF.
func (b *Backend) terminated(w http.ResponseWriter, sink policy.Sink, id string, body []byte) {
	var t n5.TerminationInfo

	if err := json.Unmarshal(body, &t); err != nil {
		n5.WriteProblem(w, invalid(causeInvalidMsgFormat, err.Error()))
		return
	}

	if t.TermCause == "" || t.ResURI == "" {
		n5.WriteProblem(w, invalid(causeMandatoryIEMissing, "termCause and resUri are required"))
		return
	}

	attrs := []any{slog.String("session", id), slog.String("uri", t.ResURI), slog.String("cause", string(t.TermCause))}
	abort := policy.Abort{Cause: string(t.TermCause), InsufficientResources: t.TermCause == n5.TerminationInsufficientQoSFlowResources}

	if terminate, known := sink.Abort(id, abort); known {
		b.log.Debug("PCF terminates an application session context", attrs...)
		noContent(w)

		if terminate != nil {
			terminate()
		}

		return
	}

	uri, ok := b.state(id)
	if !ok && b.ours(t.ResURI) && b.found(id) {
		uri, ok = t.ResURI, true
	}

	if !ok {
		b.log.Warn("PCF terminates an unknown application session context", attrs...)
		unknownContext(w)

		return
	}

	b.log.Info("PCF terminates an application session context the P-CSCF no longer has: deleting it", attrs...)
	noContent(w)
	b.deleteOrphan(id, uri)
}

func noContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// state returns the URI of a context the backend still holds for id.
func (b *Backend) state(id string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, ok := b.sessions[id]
	if !ok {
		return "", false
	}

	return s.uri, true
}

// ours reports whether uri is an Individual Application Session Context of the configured PCF, so that a
// notification cannot make the P-CSCF send requests elsewhere.
func (b *Backend) ours(uri string) bool {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != b.root.Scheme || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!sameHost(u, b.root) {
		return false
	}

	// The delete URI is built by joining paths, which resolves dot segments: the path must have none.
	if path.Clean(u.Path) != u.Path {
		return false
	}

	id, ok := strings.CutPrefix(u.Path, b.root.Path+n5.AppSessionsPath+"/")

	return ok && id != "" && !strings.Contains(id, "/")
}

func sameHost(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}

		if u.Scheme == "https" {
			return "443"
		}

		return "80"
	}

	return strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func (b *Backend) deleteOrphan(id, uri string) {
	b.wg.Add(1)

	go func() {
		defer b.wg.Done()

		ctx, cancel := context.WithTimeout(b.ctx, deleteTimeout)
		defer cancel()

		_, err := b.client.Delete(ctx, uri, nil)
		b.record(err, "204")
		b.forget(id)

		if err != nil && !errors.Is(classify(err), policy.ErrUnknownSession) && !errors.Is(err, context.Canceled) {
			b.log.Warn("deleting an application session context failed", slog.String("uri", uri), slog.Any("error", err))
		}
	}()
}

// TS 29.514 §4.2.5.2: the session is routed on its local ID.
func (b *Backend) notified(w http.ResponseWriter, sink policy.Sink, id string, body []byte) {
	var n struct {
		n5.EventsNotification
		EvNotifs *[]n5.AfEventNotification `json:"evNotifs"`
	}

	if err := json.Unmarshal(body, &n); err != nil {
		n5.WriteProblem(w, invalid(causeInvalidMsgFormat, err.Error()))
		return
	}

	if n.EvSubsURI == "" || n.EvNotifs == nil {
		n5.WriteProblem(w, invalid(causeMandatoryIEMissing, "evSubsUri and evNotifs are required"))
		return
	}

	n.EventsNotification.EvNotifs = *n.EvNotifs
	e := event(n.EventsNotification)

	if !sink.Notify(id, e) {
		b.log.Debug("PCF notification for an unknown session", slog.String("session", id), slog.String("uri", n.EvSubsURI))
		unknownContext(w)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// event returns what the notification reports. A FAILED_RESOURCES_ALLOCATION is about the media components whose
// PCC rules went INACTIVE (TS 29.514 §4.2.5.8); without failedResourcAllocReports, as for the signalling path
// (§4.2.5.10), it is about the flows it names. No flows means every media component.
func event(n n5.EventsNotification) policy.Event {
	var e policy.Event

	for _, ev := range n.EvNotifs {
		if ev.Event != n5.EventFailedResourcesAllocation {
			if !slices.Contains(e.Kinds, policy.EventOther) {
				e.Kinds = append(e.Kinds, policy.EventOther)
			}

			continue
		}

		if slices.Contains(e.Kinds, policy.EventResourcesFailed) {
			continue
		}

		components, failed := failedComponents(n.FailedResourcAllocReports, ev.Flows)
		if failed {
			e.Kinds = append(e.Kinds, policy.EventResourcesFailed)
			e.Components = components
		}
	}

	return e
}

// failedComponents returns the media components whose resources are lost, nil for all of them, and whether any is.
func failedComponents(reports []n5.ResourcesAllocationInfo, flows []n5.Flows) ([]uint32, bool) {
	if len(reports) == 0 {
		return components(flows), true
	}

	var out []uint32

	failed := false

	for _, r := range reports {
		if r.McResourcStatus != n5.ResourcesInactive {
			continue
		}

		if len(r.Flows) == 0 {
			return nil, true
		}

		failed = true

		out = append(out, components(r.Flows)...)
	}

	return out, failed
}

func components(flows []n5.Flows) []uint32 {
	var out []uint32

	for _, f := range flows {
		if !slices.Contains(out, f.MedCompN) {
			out = append(out, f.MedCompN)
		}
	}

	return out
}

// Server is the notification server.
type Server struct {
	*http.Server
}

// NewServer returns the notification server: HTTP/2 only, with prior knowledge without credentials (RFC 9113
// §3.3), and over TLS with them, where the PCF must present a certificate (TS 33.501 §13.1.0).
func NewServer(h http.Handler, creds *sbitls.Credentials, logger *slog.Logger) *Server {
	var protocols http.Protocols

	srv := &http.Server{
		Handler:           h,
		Protocols:         &protocols,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       5 * time.Minute,
	}

	if creds == nil {
		protocols.SetUnencryptedHTTP2(true)
	} else {
		protocols.SetHTTP2(true)

		srv.TLSConfig = creds.Server()
	}

	return &Server{Server: srv}
}

// Serve accepts connections on ln, and serves them over TLS if the server has credentials.
func (s *Server) Serve(ln net.Listener) error {
	if s.TLSConfig != nil {
		return s.ServeTLS(ln, "", "")
	}

	return s.Server.Serve(ln)
}
