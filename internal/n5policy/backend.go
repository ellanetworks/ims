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
	"github.com/ellanetworks/ims/internal/sbitls"
	"github.com/ellanetworks/ims/internal/tracing"
	"go.opentelemetry.io/otel/trace"
)

// TS 29.514 clause 5.5
const SessionsPath = "/n5/v1/sessions"

// TS 29.514 clause 5.8
var (
	features     = n5.Features(n5.FeatureIMSSBI, n5.FeaturePatchCorrection)
	callFeatures = n5.Features(n5.FeatureIMSSBI, n5.FeatureMCPTT, n5.FeaturePatchCorrection)
)

// TS 29.514 clause 4.2.5.10
// TS 29.514 clause 4.2.5.8
var events = []n5.AfEventSubscription{{Event: n5.EventFailedResourcesAllocation}}

const deleteTimeout = 10 * time.Second

type Config struct {
	PCF            string
	Notify         string
	TLS            *sbitls.Credentials
	Observe        func(status int, err error, elapsed time.Duration)
	TracerProvider trace.TracerProvider
	Logger         *slog.Logger
}

type Backend struct {
	cfg      Config
	client   *n5.Client
	root     *url.URL
	endpoint string
	log      *slog.Logger
	sink     atomic.Pointer[policy.Sink]

	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	detached tracing.Detached

	mu       sync.Mutex
	sessions map[string]*session
	lost     map[string]time.Time
	status   Status
}

type session struct {
	uri      string
	features n5.SupportedFeatures
	last     *n5.AppSessionContextUpdateData
	tried    []*n5.AppSessionContextUpdateData
}

const (
	tracerName = "github.com/ellanetworks/ims/internal/n5policy"
	role       = "pcscf"
)

const maxLost = 1024

type Status struct {
	PCF       string
	At        time.Time
	Reachable bool
	Result    string
}

var _ policy.Backend = (*Backend)(nil)

func New(cfg Config) (*Backend, error) {
	client, err := n5.New(n5.Config{PCF: cfg.PCF, TLS: cfg.TLS, Observe: cfg.Observe, TracerProvider: cfg.TracerProvider})
	if err != nil {
		return nil, err
	}

	root, err := url.Parse(cfg.PCF)
	if err != nil {
		return nil, err
	}

	notify, err := url.Parse(cfg.Notify)
	if err != nil || notify.Scheme != root.Scheme || notify.Host == "" || strings.Trim(notify.Path, "/") != "" ||
		notify.RawQuery != "" || notify.Fragment != "" || notify.User != nil {
		return nil, fmt.Errorf("notification root %q: want %s://host[:port]", cfg.Notify, root.Scheme)
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
		ctx: ctx, cancel: cancel, detached: tracing.NewDetached(cfg.TracerProvider, tracerName, role),
		sessions: make(map[string]*session), lost: make(map[string]time.Time),
		status: Status{PCF: root.String()},
	}, nil
}

func (b *Backend) Close() {
	b.cancel()
	b.wg.Wait()
	b.client.Close()
}

// RFC 3986 section 6.2.2.1
func (b *Backend) Endpoint() string {
	return b.endpoint
}

// RFC 3986 section 2.3
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

func (b *Backend) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.status
}

func (b *Backend) notifURI(id string) string {
	return b.cfg.Notify + SessionsPath + "/" + id
}

// TS 29.514 clause 4.2.6.7
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

// TS 29.514 clause 4.2.2.2
// TS 29.514 clause 4.2.3.2
// TS 29.514 annex B
func (b *Backend) Authorize(ctx context.Context, id, ref string, r policy.Request) (policy.Grant, error) {
	notif := b.notifURI(id)

	b.mu.Lock()

	var (
		prev       *n5.AppSessionContextUpdateData
		tried      []*n5.AppSessionContextUpdateData
		negotiated n5.SupportedFeatures
	)

	uri := ref
	if s, ok := b.sessions[id]; ok {
		uri, negotiated, prev, tried = s.uri, s.features, s.last, slices.Clone(s.tried)
	}

	b.mu.Unlock()

	next := &n5.AppSessionContextUpdateData{
		AFAppID:       r.Service,
		EvSubsc:       &n5.EventsSubscReqData{Events: events, NotifURI: notif},
		MedComponents: medComponents(r.Components, prev),
	}

	mcptt(next, r.MCPTT, r.Initial || negotiated.Has(n5.FeatureMCPTT))

	if r.Initial {
		req := n5.AppSessionContextReqData{
			AFAppID:       next.AFAppID,
			EvSubsc:       next.EvSubsc,
			MedComponents: next.MedComponents,
			NotifURI:      notif,
			GPSI:          gpsi(r.Subscribers),
			McpttID:       next.McpttID,
			ResPrio:       next.ResPrio,
			SuppFeat:      callFeatures,
		}

		ue(&req, r.UE)

		uri, err := b.create(ctx, id, &req, next)

		return policy.Grant{Ref: uri}, err
	}

	if uri == "" {
		return policy.Grant{}, errors.New("npcf-policyauthorization modify: no application session context")
	}

	if r.Forking == policy.ForkingSeveralDialogues {
		next.SipForkInd = n5.ForkingSeveralDialogues
	}

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
			b.tried(id, uri, negotiated, prev, next)
		}

		return policy.Grant{}, err
	}

	b.bodyErr(ctx, id, n5.OpModify, res)

	if res.URI != "" {
		uri = res.URI
	}

	b.keep(id, uri, negotiated, next)

	return policy.Grant{Ref: uri}, nil
}

// TS 29.514 clause B.10.2
// TS 29.514 clause 5.8
func mcptt(d *n5.AppSessionContextUpdateData, p *policy.MCPTTPriority, supported bool) {
	if p == nil || !supported {
		return
	}

	d.McpttID = p.Namespace
	d.ResPrio = n5.Prio(int(p.Level) + 1)
}

func (b *Backend) create(ctx context.Context, id string, req *n5.AppSessionContextReqData, next *n5.AppSessionContextUpdateData) (string, error) {
	res, err := b.client.Create(ctx, &n5.AppSessionContext{AscReqData: req})
	b.record(err, "201")

	if err != nil {
		err = classify(err)

		if policy.Transient(err) && !errors.Is(err, n5.ErrConnect) {
			b.lose(id)
		}

		return "", err
	}

	if res.Existing {
		b.log.DebugContext(ctx, "the PCF already holds an equal application session context", slog.String("session", id),
			slog.String("uri", res.URI))
	}

	b.bodyErr(ctx, id, n5.OpCreate, res.Result)
	b.keep(id, res.URI, res.Features, next)

	return res.URI, nil
}

// TS 29.514 clause 4.2.4.2
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

	b.bodyErr(ctx, id, n5.OpDelete, res)
	b.forget(id)

	return nil
}

func (b *Backend) keep(id, uri string, features n5.SupportedFeatures, last *n5.AppSessionContextUpdateData) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.sessions[id] = &session{uri: uri, features: features, last: last}
}

func (b *Backend) tried(id, uri string, features n5.SupportedFeatures, last, next *n5.AppSessionContextUpdateData) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, ok := b.sessions[id]
	if !ok {
		s = &session{uri: uri, features: features, last: last}
		b.sessions[id] = s
	}

	s.tried = append(s.tried, next)
}

func (b *Backend) forget(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.sessions, id)
}

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

func (b *Backend) lostOne(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	_, ok := b.lost[id]

	return ok
}

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

// TS 29.500 clause 5.2.7.3
func (b *Backend) bodyErr(ctx context.Context, id string, op n5.Op, r n5.Result) {
	if r.BodyErr != nil {
		b.log.WarnContext(ctx, "unreadable response from the PCF", slog.String("session", id), slog.String("operation", string(op)),
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

// TS 29.514 clause 5.6.2.3
// TS 29.571 clause 5.3.2
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
