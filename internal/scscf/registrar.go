package scscf

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regmetrics"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/reginfo"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel/trace"
)

const (
	regAwaitAuth = 4 * time.Minute

	sweepInterval = time.Second

	DefaultCxTimeout = 10 * time.Second

	retryAfter = 30

	maxResyncs = 2

	DefaultReauthExpires = 60 * time.Second

	DefaultMinExpires = 60 * time.Second
	DefaultMaxExpires = 3600 * time.Second

	backgroundTimeout = time.Minute

	tracerScope = "github.com/ellanetworks/ims/internal/scscf"
	tracerRole  = "scscf"
)

type Diameter interface {
	Identity() diameter.Identity
	NewSessionID() string
	Send(ctx context.Context, req *diameter.Message, opts ...diameter.RequestOption) (*diameter.Message, error)
}

type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) transaction.Timer
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) transaction.Timer {
	return time.AfterFunc(d, f)
}

// RFC 3263 section 4
type Resolver interface {
	Resolve(ctx context.Context, u sip.URI) ([]sip.Server, error)
}

type Config struct {
	HomeDomain string
	Name       sip.URI
	MinExpires time.Duration
	MaxExpires time.Duration

	ReauthInterval time.Duration
	ReauthExpires  time.Duration

	HSSRealm  func() string
	CxTimeout time.Duration
	Diameter  Diameter
	DB        *db.DB

	ASTimeout time.Duration

	Trust    *trust.Domain
	Resolver Resolver

	Records              *callrecords.Recorder
	RegistrationAttempts *regmetrics.Registrations

	Layer     *transaction.Layer
	Listeners []netip.AddrPort

	Clock  Clock
	Logger *slog.Logger
}

type Registrar struct {
	cfg        Config
	log        *slog.Logger
	clock      Clock
	detached   tracing.Detached
	serverName string
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	mu         sync.Mutex
	closed     bool
	busy       map[string]chan struct{}
	challenges map[authKey]*challenge
	authAt     map[authKey]authenticated

	reauth map[string]uint64
	sweep  transaction.Timer

	peers    map[string]map[string]*asPeer
	fetching map[string]*fetch

	profiles chan struct{}

	calls *calls

	causes map[string]map[int64]trace.SpanContext
}

type challenge struct {
	callID  string
	impu    string
	impuKey string
	nonce   string
	vector  authVector
	resyncs int
	timer   transaction.Timer
	hss     db.HSS
	initial bool

	cause trace.SpanContext
}

func New(cfg Config) *Registrar {
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	if cfg.ReauthExpires <= 0 {
		cfg.ReauthExpires = DefaultReauthExpires
	}

	if cfg.CxTimeout <= 0 {
		cfg.CxTimeout = DefaultCxTimeout
	}

	if cfg.ASTimeout <= 0 {
		cfg.ASTimeout = DefaultASTimeout
	}

	if cfg.Resolver == nil {
		cfg.Resolver = &sip.Resolver{}
	}

	if cfg.MinExpires <= 0 {
		cfg.MinExpires = DefaultMinExpires
	}

	if cfg.MaxExpires <= 0 {
		cfg.MaxExpires = max(DefaultMaxExpires, cfg.MinExpires)
	}

	ctx, cancel := context.WithCancel(context.Background())

	var tp trace.TracerProvider
	if cfg.Layer != nil {
		tp = cfg.Layer.TracerProvider()
	}

	r := &Registrar{
		cfg:        cfg,
		log:        cfg.Logger,
		clock:      cfg.Clock,
		detached:   tracing.NewDetached(tp, tracerScope, tracerRole),
		serverName: cfg.Name.String(),
		ctx:        ctx,
		cancel:     cancel,
		busy:       make(map[string]chan struct{}),
		challenges: make(map[authKey]*challenge),
		authAt:     make(map[authKey]authenticated),
		reauth:     make(map[string]uint64),
		calls:      newCalls(),
		causes:     make(map[string]map[int64]trace.SpanContext),

		peers:    make(map[string]map[string]*asPeer),
		fetching: make(map[string]*fetch),
		profiles: make(chan struct{}, 1),
	}

	return r
}

func (r *Registrar) Start() {
	r.armSweep()
}

func (r *Registrar) Register(ctx context.Context, req *sip.Request, respond func(*sip.Response)) {
	ctx, done, ok := r.begin(ctx)
	if !ok {
		res := retryLater(req)
		r.cfg.RegistrationAttempts.Answered(req, res, false)
		respond(res)

		return
	}

	defer done()

	responded := make(chan struct{})
	res, out := r.register(ctx, req, responded)

	respond(res)
	close(responded)
	r.send(ctx, out)
}

func (r *Registrar) Close() {
	r.mu.Lock()

	r.closed = true

	if r.sweep != nil {
		r.sweep.Stop()
	}

	for k, ch := range r.challenges {
		ch.timer.Stop()
		delete(r.challenges, k)
	}

	r.mu.Unlock()

	r.cancel()
	r.wg.Wait()
}

func (r *Registrar) begin(ctx context.Context) (context.Context, func(), bool) {
	if !r.start() {
		return nil, nil, false
	}

	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.ctx, cancel)

	return ctx, func() {
		stop()
		cancel()
		r.wg.Done()
	}, true
}

func (r *Registrar) start() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return false
	}

	r.wg.Add(1)

	return true
}

func (r *Registrar) tryLock(impi string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, busy := r.busy[impi]; busy {
		return false
	}

	r.busy[impi] = make(chan struct{})

	return true
}

func (r *Registrar) lock(ctx context.Context, impi string) error {
	for {
		r.mu.Lock()

		released, busy := r.busy[impi]
		if !busy {
			r.busy[impi] = make(chan struct{})
			r.mu.Unlock()

			return nil
		}

		r.mu.Unlock()

		select {
		case <-released:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *Registrar) unlock(impi string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	close(r.busy[impi])
	delete(r.busy, impi)
}

func (r *Registrar) lockProfiles(ctx context.Context) error {
	select {
	case r.profiles <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Registrar) unlockProfiles() {
	<-r.profiles
}

// TS 33.203 clause 6.1
// TS 24.229 clause 5.4.1.2.2
type authKey struct {
	impi     string
	impu     string
	instance string
	regID    int64
}

type authenticated struct {
	at     time.Time
	reauth uint64
}

func (r *Registrar) putChallenge(k authKey, ch *challenge) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if old := r.challenges[k]; old != nil {
		old.timer.Stop()
	}

	ch.timer = r.clock.AfterFunc(regAwaitAuth, func() { r.challengeExpired(k, ch) })
	r.challenges[k] = ch
}

// TS 24.229 clause 5.4.1.2.2
func (r *Registrar) challengeExpired(k authKey, ch *challenge) {
	impi := k.impi

	if !r.start() {
		return
	}

	go func() {
		defer r.wg.Done()

		if err := r.lock(r.ctx, impi); err != nil {
			return
		}

		defer r.unlock(impi)

		r.mu.Lock()

		current := r.challenges[k] == ch
		if current {
			delete(r.challenges, k)
		}

		r.mu.Unlock()

		if !current {
			return
		}

		ctx, span := r.detached.Start(r.ctx, "challenge expiry", ch.cause)

		ctx, cancel := context.WithTimeout(ctx, backgroundTimeout)
		defer cancel()

		r.log.DebugContext(ctx, "reg-await-auth expired", slog.String("impi", impi), slog.String("impu", ch.impu))

		_, _, err := r.serverAssignment(ctx, ch.hss, impi, []string{ch.impu}, assignAuthenticationTimeout, false)
		if err != nil {
			r.log.WarnContext(ctx, "failed to tell the HSS of an authentication timeout", slog.String("impi", impi),
				slog.Any("error", err))
		}

		tracing.End(span, err)
	}()
}

func (r *Registrar) pendingChallenge(k authKey) *challenge {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.challenges[k]
}

func (r *Registrar) challengeOf(impi string, c *credentials) (authKey, *challenge) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for k, ch := range r.challenges {
		if k.impi == impi && c.answers(ch) {
			return k, ch
		}
	}

	return authKey{}, nil
}

func (r *Registrar) dropChallenge(k authKey, ch *challenge) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.challenges[k] == ch {
		ch.timer.Stop()
		delete(r.challenges, k)
	}
}

func (r *Registrar) armSweep() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.closed {
		r.sweep = r.clock.AfterFunc(sweepInterval, r.runSweep)
	}
}

func (r *Registrar) runSweep() {
	if !r.start() {
		return
	}

	defer r.wg.Done()

	r.sweepExpired()
	r.releaseUnregistered()

	r.armSweep()
}

func (r *Registrar) sweepExpired() {
	ctx, cancel := context.WithTimeout(r.ctx, backgroundTimeout)
	impis, err := r.cfg.DB.ListExpiredIMPIs(ctx, r.clock.Now())

	cancel()

	if err != nil {
		r.log.WarnContext(ctx, "failed to list expired registrations", slog.Any("error", err))
		return
	}

	for _, impi := range impis {
		if !r.tryLock(impi) {
			continue
		}

		r.expire(impi)
	}
}

func (r *Registrar) expire(impi string) {
	ctx, cancel := context.WithTimeout(r.ctx, backgroundTimeout)
	st, err := r.load(ctx, impi)

	cancel()

	if err != nil {
		r.log.WarnContext(ctx, "failed to read the registrations", slog.String("impi", impi), slog.Any("error", err))
		r.unlock(impi)

		return
	}

	ctx, span := r.detached.Start(r.ctx, "registration expiry", r.expiryCauses(st)...)
	ctx, cancel = context.WithTimeout(ctx, backgroundTimeout)

	out, expired, err := r.sweepIMPI(ctx, st, impi)

	r.send(ctx, out)

	if len(expired) == 0 || !r.start() {
		cancel()
		r.unlock(impi)
		tracing.End(span, err)

		return
	}

	go func() {
		defer r.wg.Done()
		defer r.unlock(impi)
		defer cancel()

		errs := []error{err}

		for _, reg := range expired {
			if _, _, err := r.serverAssignment(ctx, reg.HSS, impi, []string{reg.IMPU}, assignTimeoutDeregistration, false); err != nil {
				r.log.WarnContext(ctx, "failed to tell the HSS of a deregistration", slog.String("impi", impi),
					slog.String("impu", reg.IMPU), slog.String("type", assignTimeoutDeregistration.String()),
					slog.Any("error", err))

				errs = append(errs, err)
			}
		}

		tracing.End(span, errors.Join(errs...))
	}()
}

func (r *Registrar) sweepIMPI(ctx context.Context, st *state, impi string) ([]*outgoing, []db.Registration, error) {
	out := r.expireSubscriptions(ctx, st, impi)

	var (
		ch           change
		deregistered []db.Registration
		failed       error
	)

	for _, reg := range st.regs {
		expired := without(reg.Bindings, st.live(reg.Bindings))
		if len(expired) == 0 {
			continue
		}

		rm, deleted, err := r.removeBindings(ctx, st, reg, expired, reginfo.Expired)
		if err != nil {
			r.log.WarnContext(ctx, "failed to remove expired contacts", slog.String("impi", impi), slog.Any("error", err))
			failed = cmp.Or(failed, err)
		}

		ch.removed = append(ch.removed, rm)

		if deleted {
			r.log.InfoContext(ctx, "registration expired", slog.String("impi", impi), slog.String("impu", reg.IMPU))
			deregistered = append(deregistered, reg)
		}
	}

	if len(ch.removed) == 0 {
		return out, deregistered, failed
	}

	r.releaseCalls(ctx, st, ch.removed)

	for _, rm := range ch.removed {
		r.deregistered(ctx, rm)
	}

	return append(out, r.notifyChange(ctx, impi, ch)...), deregistered, failed
}

func (r *Registrar) expiryCauses(st *state) []trace.SpanContext {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []trace.SpanContext

	for _, reg := range st.regs {
		for _, b := range without(reg.Bindings, st.live(reg.Bindings)) {
			if sc, ok := r.causes[st.impi][b.ID]; ok && !slices.ContainsFunc(out, sc.Equal) {
				out = append(out, sc)
			}
		}
	}

	return out
}

func (r *Registrar) noteCauses(ctx context.Context, impi string, bindings []int64) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() || len(bindings) == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.causes[impi] == nil {
		r.causes[impi] = make(map[int64]trace.SpanContext)
	}

	for _, id := range bindings {
		r.causes[impi][id] = sc
	}
}

func (r *Registrar) pruneCauses(st *state) {
	r.mu.Lock()
	defer r.mu.Unlock()

	causes := r.causes[st.impi]

	for id := range causes {
		if !slices.ContainsFunc(st.regs, func(reg db.Registration) bool {
			return slices.ContainsFunc(reg.Bindings, func(b db.Binding) bool { return b.ID == id })
		}) {
			delete(causes, id)
		}
	}

	if len(causes) == 0 {
		delete(r.causes, st.impi)
	}
}

func (r *Registrar) removeBindings(ctx context.Context, st *state, reg db.Registration, removed []db.Binding,
	event reginfo.Event,
) (removal, bool, error) {
	rm := removal{reg: reg, bindings: removed, event: event}

	reg.Bindings = without(st.live(reg.Bindings), removed)
	if len(reg.Bindings) > 0 {
		_, err := r.cfg.DB.SaveRegistration(ctx, reg)
		return rm, false, err
	}

	if err := r.cfg.DB.DeleteRegistration(ctx, reg.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
		return rm, false, err
	}

	rm.ended = true

	return rm, true, nil
}

func (r *Registrar) deregisterAtHSS(ctx context.Context, to db.HSS, impi, impu string, t cx.AssignmentType) {
	if _, _, err := r.serverAssignment(ctx, to, impi, []string{impu}, t, false); err != nil {
		r.log.WarnContext(ctx, "failed to tell the HSS of a deregistration", slog.String("impi", impi), slog.String("impu", impu),
			slog.String("type", t.String()), slog.Any("error", err))
	}
}

func (r *Registrar) expireSubscriptions(ctx context.Context, st *state, impi string) []*outgoing {
	subs, err := r.cfg.DB.ListRegSubscriptions(ctx, impi)
	if err != nil {
		r.log.WarnContext(ctx, "failed to list the reg event subscriptions", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	var out []*outgoing

	for _, s := range subs {
		if s.ExpiresAt.After(st.now) {
			continue
		}

		r.log.DebugContext(ctx, "reg event subscription expired", slog.String("impi", impi), slog.String("impu", s.IMPU))

		if o := r.buildNotify(ctx, st, s, nil, "terminated;reason="+reasonTimeout); o != nil {
			out = append(out, o)
		}
	}

	return out
}

func without(all, kept []db.Binding) []db.Binding {
	var out []db.Binding

	for _, b := range all {
		if !slices.ContainsFunc(kept, func(k db.Binding) bool { return k.ID == b.ID }) {
			out = append(out, b)
		}
	}

	return out
}

// TS 24.229 clause 5.4.1.2.2F
func serviceRoute(name sip.URI, bindingID int64) string {
	u := name.Clone()
	u.User = "orig-" + strconv.FormatInt(bindingID, 10)
	u.Params.Set("lr", "")

	return "<" + u.String() + ">"
}
