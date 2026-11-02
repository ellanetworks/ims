package ipsmgw

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/lifetime"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TS 23.204 clause 6.1
// TS 23.204 clause 6.2
// TS 29.328 clause 6.1.2.1
type HSS interface {
	UpdateSMSRegistration(ctx context.Context, user User, number string) error
}

// TS 29.328 clause 7.6 table 7.6.1
type User struct {
	MSISDN string
	IMSI   string
	IMPU   sip.URI
}

func (u User) userKey() string {
	switch {
	case u.MSISDN != "":
		return "msisdn:" + u.MSISDN
	case u.IMSI != "":
		return "imsi:" + u.IMSI
	default:
		return "impu:" + key(u.IMPU)
	}
}

// RFC 6733 section 7.1.5
var ErrRejected = errors.New("ipsmgw: the HSS refused the update")

const (
	minHSSRetry   = 5 * time.Second
	maxHSSRetry   = 10 * time.Minute
	hssReqTimeout = 30 * time.Second
)

// TS 24.341 clause 5.3.3.2
type hssSync struct {
	hss      HSS
	store    Store
	number   func() string
	want     func(userKey string) (User, bool)
	life     context.Context
	detached tracing.Detached
	tracer   trace.Tracer
	log      *slog.Logger

	saving sync.Mutex

	mu       sync.Mutex
	told     map[string]db.IPSMGWHSSRegistration
	dirty    map[string]bool
	attempts map[string]int
	causes   map[string]trace.SpanContext
	unsaved  map[string]bool
	forced   map[string]bool
	wake     chan struct{}
	stop     context.CancelFunc
	done     chan struct{}
}

func newHSSSync(life context.Context, tp trace.TracerProvider, hss HSS, store Store, number func() string,
	want func(string) (User, bool), told []db.IPSMGWHSSRegistration, log *slog.Logger,
) *hssSync {
	if tp == nil {
		tp = noop.NewTracerProvider()
	}

	h := &hssSync{
		hss: hss, store: store, number: number, want: want, life: life, log: log,
		detached: tracing.NewDetached(tp, tracerName, role), tracer: tp.Tracer(tracerName, trace.WithSchemaURL(semconv.SchemaURL)),
		told: map[string]db.IPSMGWHSSRegistration{}, dirty: map[string]bool{}, attempts: map[string]int{},
		causes: map[string]trace.SpanContext{}, unsaved: map[string]bool{}, forced: map[string]bool{},
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}

	for _, r := range told {
		h.told[r.UserKey] = r
		h.dirty[r.UserKey] = true
	}

	return h
}

func (h *hssSync) start() {
	ctx, cancel := context.WithCancel(h.life)
	h.stop = cancel

	go h.run(ctx)
}

func (h *hssSync) close() {
	if h.stop == nil {
		return
	}

	h.stop()
	<-h.done
}

func (h *hssSync) mark(cause trace.SpanContext, userKey string) {
	h.mu.Lock()
	h.markLocked(cause, userKey)
	h.mu.Unlock()

	h.poke()
}

func (h *hssSync) markLocked(cause trace.SpanContext, userKey string) {
	h.dirty[userKey] = true

	if cause.IsValid() {
		h.causes[userKey] = cause
	}
}

func (h *hssSync) force(cause trace.SpanContext, userKey string) {
	h.mu.Lock()
	h.forced[userKey] = true
	h.markLocked(cause, userKey)
	h.mu.Unlock()

	h.poke()
}

func (h *hssSync) markAll(cause trace.SpanContext, userKeys []string) {
	h.mu.Lock()

	for k := range h.told {
		h.markLocked(cause, k)
	}

	for _, k := range userKeys {
		h.markLocked(cause, k)
	}

	clear(h.attempts)

	h.mu.Unlock()

	h.poke()
}

// TS 29.328 clause 6.1.2.1
func (h *hssSync) rekey(cause trace.SpanContext, from, to string) {
	h.mu.Lock()

	r, ok := h.told[from]
	_, taken := h.told[to]

	if ok && !taken {
		delete(h.told, from)

		r.UserKey = to
		h.told[to] = r
		h.unsaved[from], h.unsaved[to] = true, true
	}

	h.markLocked(cause, from)
	h.markLocked(cause, to)
	h.mu.Unlock()

	h.poke()
}

func (h *hssSync) persist(ctx context.Context) error {
	h.saving.Lock()
	defer h.saving.Unlock()

	h.mu.Lock()

	rows := make(map[string]*db.IPSMGWHSSRegistration, len(h.unsaved))

	for k := range h.unsaved {
		if r, ok := h.told[k]; ok {
			rows[k] = &r
		} else {
			rows[k] = nil
		}
	}

	clear(h.unsaved)
	h.mu.Unlock()

	var errs []error

	for k, r := range rows {
		wctx, cancel := context.WithTimeout(lifetime.With(h.life, ctx), saveTimeout)

		var err error
		if r != nil {
			err = h.store.PutIPSMGWHSSRegistration(wctx, *r)
		} else {
			err = h.store.DeleteIPSMGWHSSRegistration(wctx, k)
		}

		cancel()

		if err != nil {
			h.log.ErrorContext(ctx, "failed to store an IP-SM-GW registration at the HSS", slog.String("user", k),
				slog.Bool("registered", r != nil), slog.Any("error", err))

			h.mu.Lock()
			h.unsaved[k] = true
			h.mu.Unlock()

			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func (h *hssSync) poke() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *hssSync) run(ctx context.Context) {
	defer close(h.done)

	for {
		k, cause, ok := h.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-h.wake:
				continue
			}
		}

		h.sync(ctx, k, cause)

		if ctx.Err() != nil {
			return
		}
	}
}

func (h *hssSync) next() (string, trace.SpanContext, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for k := range h.dirty {
		cause := h.causes[k]
		delete(h.dirty, k)
		delete(h.causes, k)

		return k, cause, true
	}

	return "", trace.SpanContext{}, false
}

func (h *hssSync) sync(life context.Context, k string, cause trace.SpanContext) {
	u, available := h.want(k)
	number := h.number()

	h.mu.Lock()
	told, registered := h.told[k]
	forced := h.forced[k]
	delete(h.forced, k)
	h.mu.Unlock()

	want := ""
	if available && number != "" {
		want = number
	}

	if want == told.Number && (!forced || want == "") {
		h.settled(k)
		return
	}

	if want == "" {
		u = User{MSISDN: told.MSISDN}
		if told.MSISDN == "" {
			u.IMPU, _ = sip.ParseURI(told.IMPU)
		}
	}

	ctx, span := h.detached.Start(life, "ip-sm-gw hss update", cause)

	var err error
	defer func() { tracing.End(span, err) }()

	rctx, cancel := context.WithTimeout(ctx, hssReqTimeout)
	err = h.hss.UpdateSMSRegistration(rctx, u, want)

	cancel()

	if errors.Is(err, ErrRejected) {
		h.settled(k)
		h.log.ErrorContext(ctx, "IP-SM-GW registration at the HSS refused", slog.String("user", k),
			slog.Bool("register", want != ""), slog.Any("error", err))

		return
	}

	if err != nil {
		if forced {
			h.mu.Lock()
			h.forced[k] = true
			h.mu.Unlock()
		}

		if life.Err() != nil {
			return
		}

		after := h.retry(cause, k)
		h.log.WarnContext(ctx, "IP-SM-GW registration at the HSS failed", slog.String("user", k),
			slog.Bool("register", want != ""), slog.Any("error", err), slog.Duration("retry_after", after))

		return
	}

	h.settled(k)

	if want == "" {
		h.mu.Lock()
		delete(h.told, k)
		h.unsaved[k] = true
		h.mu.Unlock()

		err = h.persist(ctx)

		h.log.DebugContext(ctx, "IP-SM-GW deregistered at the HSS", slog.String("user", k),
			slog.Bool("registered", registered))

		return
	}

	r := db.IPSMGWHSSRegistration{UserKey: k, MSISDN: u.MSISDN, Number: want}
	if u.MSISDN == "" {
		r.IMPU = u.IMPU.String()
	}

	h.mu.Lock()
	h.told[k] = r
	h.unsaved[k] = true
	h.mu.Unlock()

	err = h.persist(ctx)

	h.log.DebugContext(ctx, "IP-SM-GW registered at the HSS", slog.String("user", k))

	h.mark(trace.SpanContext{}, k)
}

func (h *hssSync) settled(k string) {
	h.mu.Lock()
	delete(h.attempts, k)
	h.mu.Unlock()
}

func (h *hssSync) retry(cause trace.SpanContext, k string) time.Duration {
	h.mu.Lock()
	after := min(minHSSRetry<<min(h.attempts[k], 8), maxHSSRetry)
	h.attempts[k]++
	h.mu.Unlock()

	time.AfterFunc(after, func() { h.mark(cause, k) })

	return after
}

const shutdownParallelism = 16

// TS 23.204 clause 6.2.2
func (h *hssSync) deregisterAll(ctx context.Context) {
	h.mu.Lock()
	told := make([]db.IPSMGWHSSRegistration, 0, len(h.told))
	causes := make(map[string]trace.SpanContext, len(h.causes))

	for _, r := range h.told {
		told = append(told, r)
	}

	for k, c := range h.causes {
		causes[k] = c
	}
	h.mu.Unlock()

	var wg sync.WaitGroup

	sem := make(chan struct{}, shutdownParallelism)

	for _, r := range told {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()

			return
		}

		wg.Go(func() {
			defer func() { <-sem }()

			opts := []trace.SpanStartOption{trace.WithAttributes(tracing.RoleKey.String(role))}
			if c := causes[r.UserKey]; c.IsValid() {
				opts = append(opts, trace.WithLinks(trace.Link{SpanContext: c}))
			}

			ctx, span := h.tracer.Start(ctx, "ip-sm-gw hss update", opts...)

			var err error
			defer func() { tracing.End(span, err) }()

			u := User{MSISDN: r.MSISDN}
			if r.MSISDN == "" {
				u.IMPU, _ = sip.ParseURI(r.IMPU)
			}

			if err = h.hss.UpdateSMSRegistration(ctx, u, ""); err != nil {
				h.log.WarnContext(ctx, "IP-SM-GW not deregistered at the HSS at shutdown", slog.String("user", r.UserKey),
					slog.Any("error", err))

				return
			}

			h.mu.Lock()
			delete(h.told, r.UserKey)
			h.unsaved[r.UserKey] = true
			h.mu.Unlock()

			err = h.persist(ctx)
		})
	}

	wg.Wait()
}
