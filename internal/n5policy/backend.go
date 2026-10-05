package n5policy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/internal/n5"
	"github.com/ellanetworks/ims/internal/policy"
)

// SessionsPath is where the P-CSCF receives notifications: each session has the notifUri
// {root}/n5/v1/sessions/{id}, to which the PCF appends /notify or /terminate (TS 29.514 §5.5).
const SessionsPath = "/n5/v1/sessions"

// IMS_SBI and PatchCorrection (TS 29.514 §5.8): without PatchCorrection, a PCF only interoperates when no
// context is updated.
var features = n5.Features(n5.FeatureIMSSBI, n5.FeaturePatchCorrection)

// The events a session subscribes to. FAILED_RESOURCES_ALLOCATION reports the loss of the signalling path
// (TS 29.514 §4.2.5.10) and of call media (§4.2.5.8).
var events = []n5.AfEventSubscription{{Event: n5.EventFailedResourcesAllocation}}

// deleteTimeout bounds the delete of a context the P-CSCF does not know, which the PCF asked to terminate.
const deleteTimeout = 10 * time.Second

type Config struct {
	// PCF is the API root of the PCF, http://host[:port][/prefix].
	PCF string
	// Notify is the root of the notification URIs, http://host:port, at which Handler is served.
	Notify string
	Logger *slog.Logger
}

// Backend is the policy backend over N5. It is also the HTTP handler of the PCF's notifications.
type Backend struct {
	cfg      Config
	client   *n5.Client
	root     *url.URL
	endpoint string
	log      *slog.Logger
	sink     atomic.Pointer[policy.Sink]

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	sessions map[string]*session
	lost     map[string]time.Time
	status   Status
}

// session is what the backend keeps of a context it created: its URI, and the service information the PCF
// holds, which the next PATCH is computed from. tried is what unanswered PATCHes since then sent: the PCF may hold
// any of them instead.
type session struct {
	uri   string
	last  *n5.AppSessionContextUpdateData
	tried []*n5.AppSessionContextUpdateData
}

// maxLost bounds the creates the backend remembers as unanswered.
const maxLost = 1024

// Status is the outcome of the last request to the PCF.
type Status struct {
	PCF string
	// At is zero until a request is made.
	At time.Time
	// Reachable means the PCF, or an intermediary, answered.
	Reachable bool
	Result    string
}

var _ policy.Backend = (*Backend)(nil)

func New(cfg Config) (*Backend, error) {
	client, err := n5.New(n5.Config{PCF: cfg.PCF})
	if err != nil {
		return nil, err
	}

	root, err := url.Parse(cfg.PCF)
	if err != nil {
		return nil, err
	}

	notify, err := url.Parse(cfg.Notify)
	if err != nil || notify.Scheme != "http" || notify.Host == "" || strings.Trim(notify.Path, "/") != "" ||
		notify.RawQuery != "" || notify.Fragment != "" || notify.User != nil {
		return nil, fmt.Errorf("notification root %q: want http://host:port", cfg.Notify)
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	cfg.Notify = strings.TrimSuffix(cfg.Notify, "/")

	root.Host = strings.ToLower(root.Host)
	root.Path = strings.TrimSuffix(root.Path, "/")
	root.RawPath = ""

	ctx, cancel := context.WithCancel(context.Background())

	return &Backend{
		cfg: cfg, client: client, root: root, endpoint: "n5:" + root.String(), log: cfg.Logger,
		ctx: ctx, cancel: cancel,
		sessions: make(map[string]*session), lost: make(map[string]time.Time),
		status: Status{PCF: root.String()},
	}, nil
}

// Close stops the deletes in progress and drops idle connections.
func (b *Backend) Close() {
	b.cancel()
	b.wg.Wait()
	b.client.Close()
}

// Endpoint names the PCF by its API root, with the host in lower case (RFC 3986 §6.2.2.1).
func (b *Backend) Endpoint() string {
	return b.endpoint
}

// NewSessionID returns a random ID that a URI path segment carries as is (RFC 3986 §2.3).
func (b *Backend) NewSessionID() string {
	var id [16]byte

	_, _ = rand.Read(id[:])

	return base64.RawURLEncoding.EncodeToString(id[:])
}

func (b *Backend) Bind(s policy.Sink) {
	if s == nil {
		b.sink.Store(nil)
		return
	}

	b.sink.Store(&s)
}

// Status returns the outcome of the last request to the PCF.
func (b *Backend) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.status
}

func (b *Backend) notifURI(id string) string {
	return b.cfg.Notify + SessionsPath + "/" + id
}

// TS 29.514 §4.2.6.7
func (b *Backend) OpenSignalling(ctx context.Context, id string, s policy.Signalling, _ bool) (string, error) {
	notif := b.notifURI(id)
	req := n5.AppSessionContextReqData{
		EvSubsc: &n5.EventsSubscReqData{Events: events, NotifURI: notif},
		MedComponents: map[string]n5.MediaComponent{"0": {
			MedCompN:    0,
			MedSubComps: map[string]n5.MediaSubComponent{"0": {FNum: 0, FlowUsage: n5.FlowUsageAFSignalling}},
		}},
		NotifURI: notif,
		SuppFeat: features,
	}

	ue(&req, s.UE)

	return b.create(ctx, id, &req, nil)
}

// TS 29.514 §4.2.2.2, §4.2.3.2, Annex B
func (b *Backend) Authorize(ctx context.Context, id, ref string, r policy.Request) (policy.Grant, error) {
	notif := b.notifURI(id)

	b.mu.Lock()

	var (
		prev  *n5.AppSessionContextUpdateData
		tried []*n5.AppSessionContextUpdateData
	)

	uri := ref
	if s, ok := b.sessions[id]; ok {
		uri, prev, tried = s.uri, s.last, slices.Clone(s.tried)
	}

	b.mu.Unlock()

	next := &n5.AppSessionContextUpdateData{
		AFAppID:       r.Service,
		EvSubsc:       &n5.EventsSubscReqData{Events: events, NotifURI: notif},
		MedComponents: medComponents(r.Components, prev),
	}

	if r.Initial {
		req := n5.AppSessionContextReqData{
			AFAppID:       next.AFAppID,
			EvSubsc:       next.EvSubsc,
			MedComponents: next.MedComponents,
			NotifURI:      notif,
			GPSI:          gpsi(r.Subscribers),
			SuppFeat:      features,
		}

		ue(&req, r.UE)

		uri, err := b.create(ctx, id, &req, next)

		return policy.Grant{Ref: uri}, err
	}

	if uri == "" {
		return policy.Grant{}, errors.New("npcf-policyauthorization modify: no application session context")
	}

	// TS 29.514 Annex B.3.2: SEVERAL_DIALOGUES while the early dialogues fork, removed at the final answer.
	if r.Forking == policy.ForkingSeveralDialogues {
		next.SipForkInd = n5.ForkingSeveralDialogues
	}

	// Without the service information the PCF holds, or when it may hold what an unanswered PATCH sent, the
	// patch sets it whole.
	var (
		patch n5.Patch
		err   error
	)

	if prev == nil || len(tried) > 0 {
		patch, err = n5.NewResyncPatch(next, append(tried, prev)...)
	} else {
		patch, err = n5.NewPatch(prev, next)
	}

	if err != nil {
		return policy.Grant{}, err
	}

	if patch.Empty() {
		return policy.Grant{Ref: uri}, nil
	}

	res, err := b.client.Modify(ctx, uri, patch)
	b.record(err, "200")

	if err != nil {
		err = classify(err)

		switch {
		case errors.Is(err, policy.ErrUnknownSession):
			b.forget(id)
		case policy.Transient(err):
			b.tried(id, uri, prev, next)
		}

		return policy.Grant{}, err
	}

	b.bodyErr(id, n5.OpModify, res)

	if res.URI != "" {
		uri = res.URI
	}

	b.keep(id, uri, next)

	return policy.Grant{Ref: uri}, nil
}

func (b *Backend) create(ctx context.Context, id string, req *n5.AppSessionContextReqData, next *n5.AppSessionContextUpdateData) (string, error) {
	res, err := b.client.Create(ctx, &n5.AppSessionContext{AscReqData: req})
	b.record(err, "201")

	if err != nil {
		err = classify(err)

		// The PCF may have created a context it could not report; its terminate names it (TS 29.514 §4.2.5.3).
		if policy.Transient(err) && !errors.Is(err, n5.ErrConnect) {
			b.lose(id)
		}

		return "", err
	}

	if res.Existing {
		b.log.Debug("the PCF already holds an equal application session context", slog.String("session", id),
			slog.String("uri", res.URI))
	}

	b.bodyErr(id, n5.OpCreate, res.Result)
	b.keep(id, res.URI, next)

	return res.URI, nil
}

// TS 29.514 §4.2.4.2. Without a URI, the create was not answered and there is no context to delete.
func (b *Backend) Terminate(ctx context.Context, id, ref string, _ policy.Termination, _ bool) error {
	uri := ref

	b.mu.Lock()
	if s, ok := b.sessions[id]; ok {
		uri = s.uri
	}
	b.mu.Unlock()

	if uri == "" {
		b.forget(id)
		return nil
	}

	res, err := b.client.Delete(ctx, uri, nil)
	b.record(err, "204")

	if err != nil {
		err = classify(err)
		if !policy.Transient(err) {
			b.forget(id)
		}

		return err
	}

	b.bodyErr(id, n5.OpDelete, res)
	b.forget(id)

	return nil
}

func (b *Backend) keep(id, uri string, last *n5.AppSessionContextUpdateData) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.sessions[id] = &session{uri: uri, last: last}
}

// tried notes a PATCH the PCF may or may not have applied.
func (b *Backend) tried(id, uri string, last, next *n5.AppSessionContextUpdateData) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, ok := b.sessions[id]
	if !ok {
		s = &session{uri: uri, last: last}
		b.sessions[id] = s
	}

	s.tried = append(s.tried, next)
}

func (b *Backend) forget(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.sessions, id)
}

// lose remembers a create that got no answer, the oldest forgotten first.
func (b *Backend) lose(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.lost) >= maxLost {
		oldest := ""

		for k, at := range b.lost {
			if oldest == "" || at.Before(b.lost[oldest]) {
				oldest = k
			}
		}

		delete(b.lost, oldest)
	}

	b.lost[id] = time.Now()
}

// found reports, once, whether the create of id got no answer.
func (b *Backend) found(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	_, ok := b.lost[id]
	delete(b.lost, id)

	return ok
}

func (b *Backend) record(err error, success string) {
	at, reachable, result := time.Now(), true, success

	var n *n5.Error

	switch {
	case err == nil:
	case errors.As(err, &n) && n.Status != 0:
		result, _ = policy.ResultOf(classify(err))
	default:
		reachable, result = false, err.Error()
	}

	b.mu.Lock()
	b.status.At, b.status.Reachable, b.status.Result = at, reachable, result
	b.mu.Unlock()
}

// TS 29.500 §5.2.7.3: the PCF carried out the request; only what it said about it is lost.
func (b *Backend) bodyErr(id string, op n5.Op, r n5.Result) {
	if r.BodyErr != nil {
		b.log.Warn("unreadable response from the PCF", slog.String("session", id), slog.String("operation", string(op)),
			slog.Any("error", r.BodyErr))
	}
}

func ue(r *n5.AppSessionContextReqData, a netip.Addr) {
	a = a.Unmap()

	if a.Is4() {
		r.UEIPv4 = a
	} else {
		r.UEIPv6 = a
	}
}

// TS 29.514 §5.6.2.3, TS 29.571 §5.3.2 (Gpsi, ^msisdn-[0-9]{5,15}$): the first E.164 number of the user.
func gpsi(subs []policy.Subscriber) string {
	for _, s := range subs {
		if s.Kind == policy.SubscriberE164 && len(s.ID) >= 5 && len(s.ID) <= 15 && digits(s.ID) {
			return "msisdn-" + s.ID
		}
	}

	return ""
}

func digits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}

	return true
}
