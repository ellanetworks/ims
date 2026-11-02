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

// HSS registers the IP-SM-GW at the HSS for a user, by the Sh-Update of its SMSRegistrationInfo (TS 23.204 §6.1
// step 6, §6.2; TS 29.328 §6.1.2.1 step 4b): of the IP-SM-GW number, or of an empty one, which deregisters it.
type HSS interface {
	UpdateSMSRegistration(ctx context.Context, user User, number string) error
}

// User is a user as the HSS knows it: by its MSISDN, if known, else by an IMPU (TS 29.328 Table 7.6.1). Its IMSI
// tells the IMPUs of a user without an MSISDN apart from those of another.
type User struct {
	MSISDN string
	IMSI   string
	IMPU   sip.URI
}

// userKey is the key of a user, whose IMPUs the HSS registration is of: its MSISDN, else its IMSI, else its IMPU.
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

// ErrRejected is of an update the HSS refused for good (RFC 6733 §7.1.5: a permanent failure), such as of a user it
// does not know: it is not tried again until the user changes.
var ErrRejected = errors.New("ipsmgw: the HSS refused the update")

// The bounds of the waits between the attempts of an update the HSS failed, and the time each attempt has.
const (
	minHSSRetry   = 5 * time.Second
	maxHSSRetry   = 10 * time.Minute
	hssReqTimeout = 30 * time.Second
)

// hssSync keeps the HSS registrations of the users in line with their availability (TS 24.341 §5.3.3.2): a user is
// registered, of the IP-SM-GW number, while one of its IMPUs is available, and deregistered once none is. It updates
// one user at a time, on its own goroutine, and tries a failed update again until it succeeds or is no longer needed.
// An update starts later than its cause: it is a trace of its own, linked to the latest cause of the user (D5).
type hssSync struct {
	hss    HSS
	store  Store
	number func() string
	// want is the user's registration to have: available, and its identity; of the registrar, under its lock.
	want func(userKey string) (User, bool)
	// life is the lifetime of the IP-SM-GW, which the updates and the writes of told are bound to.
	life     context.Context
	detached tracing.Detached
	tracer   trace.Tracer
	log      *slog.Logger

	// saving orders the writes of told: each writes the state of told at its start, after the previous one.
	saving sync.Mutex

	mu sync.Mutex
	// told are the users the HSS has the IP-SM-GW registered for, of the number it was given.
	told     map[string]db.IPSMGWHSSRegistration
	dirty    map[string]bool
	attempts map[string]int
	// causes are the span contexts of the latest causes of the users dirty, which their updates link to.
	causes map[string]trace.SpanContext
	// unsaved are the users whose told the store has yet to be given.
	unsaved map[string]bool
	// forced are the users to register at the HSS again, though registered already: of a UE reachable again.
	forced map[string]bool
	wake   chan struct{}
	stop   context.CancelFunc
	done   chan struct{}
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

// close stops the updates, and waits for the one in progress.
func (h *hssSync) close() {
	if h.stop == nil {
		return
	}

	h.stop()
	<-h.done
}

// mark has the user of userKey checked, of the cause cause.
func (h *hssSync) mark(cause trace.SpanContext, userKey string) {
	h.mu.Lock()
	h.markLocked(cause, userKey)
	h.mu.Unlock()

	h.poke()
}

// markLocked marks the user of userKey dirty, of the cause cause if valid, which replaces an earlier one. Called with
// h.mu held.
func (h *hssSync) markLocked(cause trace.SpanContext, userKey string) {
	h.dirty[userKey] = true

	if cause.IsValid() {
		h.causes[userKey] = cause
	}
}

// force has the user of userKey registered at the HSS again, if it is to be registered.
func (h *hssSync) force(cause trace.SpanContext, userKey string) {
	h.mu.Lock()
	h.forced[userKey] = true
	h.markLocked(cause, userKey)
	h.mu.Unlock()

	h.poke()
}

// markAll has every user checked at once: those registered, and those of userKeys, as when the number changes, or
// once the core that sends the updates runs again.
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

// rekey has the HSS registration of the user of the key from, if any, be that of the key to, of the same user,
// which a later third-party REGISTER told more of: the HSS keeps one registration of a user (TS 29.328 §6.1.2.1 step
// 4b), which the user's next update, of its new identity, replaces, and which deregistering the user of its former
// identity would delete. The store gets it at the next persist.
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

// persist gives the store the registrations at the HSS of the users unsaved, as told has them now, each write bounded
// by saveTimeout within the lifetime of the IP-SM-GW. Writes are in the order of persist calls, each of the state at
// its start: the store ends with the latest. Those that fail are written at the next persist.
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

// next takes a user to check, if any, and the span context of its latest cause.
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

// sync updates the HSS registration of the user of k, if it is not the one to have: of the number while the user is
// available and the IP-SM-GW has a number, else none. The update is the root of a trace of its own, linked to cause,
// bound to life.
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

	// A user no longer available is deregistered by the identity it was registered with.
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

	// The user may have changed while the HSS answered: a change of its own cause, if so.
	h.mark(trace.SpanContext{}, k)
}

func (h *hssSync) settled(k string) {
	h.mu.Lock()
	delete(h.attempts, k)
	h.mu.Unlock()
}

// retry checks the user of k again after a wait, doubling from minHSSRetry to maxHSSRetry, of the same cause, and
// returns the wait.
func (h *hssSync) retry(cause trace.SpanContext, k string) time.Duration {
	h.mu.Lock()
	after := min(minHSSRetry<<min(h.attempts[k], 8), maxHSSRetry)
	h.attempts[k]++
	h.mu.Unlock()

	time.AfterFunc(after, func() { h.mark(cause, k) })

	return after
}

// shutdownParallelism bounds the deregistrations at the HSS in flight at a shutdown.
const shutdownParallelism = 16

// deregisterAll deregisters the IP-SM-GW at the HSS for every user it is registered for, until ctx is done, once the
// updates stopped: while the IP-SM-GW is down, the HSS routes the short messages to the users' other serving nodes
// (TS 23.204 §6.2.2: a trigger of the IP-SM-GW of a network initiated deregistration). Those left at the deadline
// stay registered. The registrations of the users are kept, so that the next start registers the IP-SM-GW again for
// those available. Each update is in the trace of ctx, linked to the pending cause of its user, if any.
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
