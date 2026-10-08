package scscf

import (
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
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	regAwaitAuth = 4 * time.Minute

	sweepInterval = time.Second

	cxTimeout = 10 * time.Second

	retryAfter = 30

	maxResyncs = 2

	DefaultReauthExpires = 60 * time.Second

	DefaultMinExpires = 60 * time.Second
	DefaultMaxExpires = 3600 * time.Second
)

type Diameter interface {
	Identity() diameter.Identity
	NewSessionID() string
	Do(ctx context.Context, peerID string, req *diameter.Message, opts ...diameter.DoOption) (*diameter.Message, error)
}

type HSS struct {
	ID    string
	Host  string
	Realm string
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

type Config struct {
	HomeDomain string
	Name       sip.URI
	MinExpires time.Duration
	MaxExpires time.Duration

	ReauthInterval time.Duration
	ReauthExpires  time.Duration

	HSS      HSS
	Diameter Diameter
	DB       *db.DB

	Layer     *transaction.Layer
	Listeners []netip.AddrPort

	Clock  Clock
	Logger *slog.Logger
}

type Registrar struct {
	cfg        Config
	log        *slog.Logger
	clock      Clock
	serverName string
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	mu         sync.Mutex
	closed     bool
	busy       map[string]chan struct{}
	challenges map[string]*challenge
	authAt     map[string]time.Time
	reauth     map[string]bool
	sweep      transaction.Timer

	calls *calls
}

type challenge struct {
	callID  string
	impu    string
	impuKey string
	nonce   string
	vector  authVector
	resyncs int
	timer   transaction.Timer
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

	if cfg.MinExpires <= 0 {
		cfg.MinExpires = DefaultMinExpires
	}

	if cfg.MaxExpires <= 0 {
		cfg.MaxExpires = max(DefaultMaxExpires, cfg.MinExpires)
	}

	ctx, cancel := context.WithCancel(context.Background())

	r := &Registrar{
		cfg:        cfg,
		log:        cfg.Logger,
		clock:      cfg.Clock,
		serverName: cfg.Name.String(),
		ctx:        ctx,
		cancel:     cancel,
		busy:       make(map[string]chan struct{}),
		challenges: make(map[string]*challenge),
		authAt:     make(map[string]time.Time),
		reauth:     make(map[string]bool),
		calls:      newCalls(),
	}

	return r
}

func (r *Registrar) Start() {
	r.armSweep()
}

func (r *Registrar) Register(ctx context.Context, req *sip.Request, respond func(*sip.Response)) {
	ctx, done, ok := r.begin(ctx)
	if !ok {
		respond(retryLater(req))
		return
	}

	defer done()

	res, out := r.register(ctx, req)

	respond(res)
	r.send(out)
}

func (r *Registrar) Close() {
	r.mu.Lock()

	r.closed = true

	if r.sweep != nil {
		r.sweep.Stop()
	}

	for impi, ch := range r.challenges {
		ch.timer.Stop()
		delete(r.challenges, impi)
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

func (r *Registrar) putChallenge(impi string, ch *challenge) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if old := r.challenges[impi]; old != nil {
		old.timer.Stop()
	}

	ch.timer = r.clock.AfterFunc(regAwaitAuth, func() { r.challengeExpired(impi, ch) })
	r.challenges[impi] = ch
}

func (r *Registrar) challengeExpired(impi string, ch *challenge) {
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

		current := r.challenges[impi] == ch
		if current {
			delete(r.challenges, impi)
		}

		r.mu.Unlock()

		if !current {
			return
		}

		r.log.Debug("reg-await-auth expired", slog.String("impi", impi), slog.String("impu", ch.impu))

		if _, err := r.serverAssignment(r.ctx, impi, []string{ch.impu}, assignAuthenticationTimeout, false); err != nil {
			r.log.Warn("failed to tell the HSS of an authentication timeout", slog.String("impi", impi), slog.Any("error", err))
		}
	}()
}

func (r *Registrar) pendingChallenge(impi string) *challenge {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.challenges[impi]
}

func (r *Registrar) dropChallenge(impi string, ch *challenge) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.challenges[impi] == ch {
		ch.timer.Stop()
		delete(r.challenges, impi)
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

	r.sweepExpired(r.ctx)
	r.armSweep()
}

func (r *Registrar) sweepExpired(ctx context.Context) {
	impis, err := r.cfg.DB.ListExpiredIMPIs(ctx, r.clock.Now())
	if err != nil {
		r.log.Warn("failed to list expired registrations", slog.Any("error", err))
		return
	}

	for _, impi := range impis {
		if !r.tryLock(impi) {
			continue
		}

		out, expired := r.sweepIMPI(ctx, impi)

		r.send(out)

		if len(expired) == 0 || !r.start() {
			r.unlock(impi)
			continue
		}

		go func() {
			defer r.wg.Done()
			defer r.unlock(impi)

			for _, impu := range expired {
				r.deregisterAtHSS(r.ctx, impi, impu, assignTimeoutDeregistration)
			}
		}()
	}
}

func (r *Registrar) sweepIMPI(ctx context.Context, impi string) ([]*outgoing, []string) {
	st, err := r.load(ctx, impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", impi), slog.Any("error", err))
		return nil, nil
	}

	out := r.expireSubscriptions(ctx, st, impi)

	var (
		ch           change
		deregistered []string
	)

	for _, reg := range st.regs {
		expired := without(reg.Bindings, st.live(reg.Bindings))
		if len(expired) == 0 {
			continue
		}

		rm, deleted, err := r.removeBindings(ctx, st, reg, expired, regevent.Expired)
		if err != nil {
			r.log.Warn("failed to remove expired contacts", slog.String("impi", impi), slog.Any("error", err))
		}

		ch.removed = append(ch.removed, rm)

		if deleted {
			r.log.Info("registration expired", slog.String("impi", impi), slog.String("impu", reg.IMPU))
			deregistered = append(deregistered, reg.IMPU)
		}
	}

	if len(ch.removed) == 0 {
		return out, deregistered
	}

	r.releaseCalls(st, ch.removed)

	return append(out, r.notifyChange(ctx, impi, ch)...), deregistered
}

func (r *Registrar) removeBindings(ctx context.Context, st *state, reg db.Registration, removed []db.Binding,
	event regevent.Event,
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

	return rm, true, nil
}

func (r *Registrar) deregisterAtHSS(ctx context.Context, impi, impu string, t cx.AssignmentType) {
	if _, err := r.serverAssignment(ctx, impi, []string{impu}, t, false); err != nil {
		r.log.Warn("failed to tell the HSS of a deregistration", slog.String("impi", impi), slog.String("impu", impu),
			slog.String("type", t.String()), slog.Any("error", err))
	}
}

func (r *Registrar) expireSubscriptions(ctx context.Context, st *state, impi string) []*outgoing {
	subs, err := r.cfg.DB.ListRegSubscriptions(ctx, impi)
	if err != nil {
		r.log.Warn("failed to list the reg event subscriptions", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	var out []*outgoing

	for _, s := range subs {
		if s.ExpiresAt.After(st.now) {
			continue
		}

		r.log.Debug("reg event subscription expired", slog.String("impi", impi), slog.String("impu", s.IMPU))

		if o := r.buildNotify(ctx, st, s, nil, "terminated;reason="+reasonTimeout); o != nil {
			out = append(out, o)
		}
	}

	return out
}

func without(all, kept []db.Binding) []db.Binding {
	var out []db.Binding

	for _, b := range all {
		if !slices.ContainsFunc(kept, func(k db.Binding) bool { return k.Contact.ID == b.Contact.ID }) {
			out = append(out, b)
		}
	}

	return out
}

func serviceRoute(name sip.URI, contactID int64) string {
	u := name.Clone()
	u.User = "orig-" + strconv.FormatInt(contactID, 10)
	u.Params.Set("lr", "")

	return "<" + u.String() + ">"
}
