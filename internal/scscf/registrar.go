package scscf

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	regAwaitAuth = 4 * time.Minute

	sweepRetry = time.Second

	cxTimeout = 10 * time.Second

	retryAfter = 30

	maxResyncs = 2
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
	busy       map[string]*hold
	challenges map[string]*challenge
	authAt     map[string]time.Time
	sweep      transaction.Timer
	sweepAt    time.Time
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

type hold struct {
	released chan struct{}
	register bool
}

func New(cfg Config) *Registrar {
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	ctx, cancel := context.WithCancel(context.Background())

	r := &Registrar{
		cfg:        cfg,
		log:        cfg.Logger,
		clock:      cfg.Clock,
		serverName: cfg.Name.String(),
		ctx:        ctx,
		cancel:     cancel,
		busy:       make(map[string]*hold),
		challenges: make(map[string]*challenge),
		authAt:     make(map[string]time.Time),
	}

	return r
}

func (r *Registrar) Start(ctx context.Context) {
	r.scheduleSweep(ctx)
}

func (r *Registrar) Register(ctx context.Context, req *sip.Request, respond func(*sip.Response)) {
	if !r.start() {
		respond(retryLater(req))
		return
	}

	defer r.wg.Done()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()

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

	r.busy[impi] = &hold{released: make(chan struct{})}

	return true
}

func (r *Registrar) lock(ctx context.Context, impi string) error {
	_, err := r.acquire(ctx, impi, false)
	return err
}

func (r *Registrar) lockForRegister(ctx context.Context, impi string) (bool, error) {
	return r.acquire(ctx, impi, true)
}

func (r *Registrar) acquire(ctx context.Context, impi string, register bool) (bool, error) {
	for {
		r.mu.Lock()

		h, busy := r.busy[impi]
		if !busy {
			r.busy[impi] = &hold{released: make(chan struct{}), register: register}
			r.mu.Unlock()

			return true, nil
		}

		r.mu.Unlock()

		if register && h.register {
			return false, nil
		}

		select {
		case <-h.released:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

func (r *Registrar) unlock(impi string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	close(r.busy[impi].released)
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

		r.log.Info("reg-await-auth expired", slog.String("impi", impi), slog.String("impu", ch.impu))

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

func (r *Registrar) scheduleSweep(ctx context.Context) {
	next, ok, err := r.cfg.DB.NextExpiry(ctx)
	if err != nil {
		r.log.Warn("failed to read the next expiry", slog.Any("error", err))
		next, ok = r.clock.Now().Add(sweepRetry), true
	}

	if ok {
		r.armSweep(next)
	}
}

func (r *Registrar) armSweep(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed || r.sweep != nil && !r.sweepAt.After(at) {
		return
	}

	if r.sweep != nil {
		r.sweep.Stop()
	}

	r.sweepAt = at
	r.sweep = r.clock.AfterFunc(at.Sub(r.clock.Now()), r.runSweep)
}

func (r *Registrar) runSweep() {
	if !r.start() {
		return
	}

	defer r.wg.Done()

	r.mu.Lock()
	r.sweep = nil
	r.mu.Unlock()

	skipped := r.sweepExpired(r.ctx)

	now := r.clock.Now()

	next, ok, err := r.cfg.DB.NextExpiry(r.ctx)
	if err != nil {
		r.log.Warn("failed to read the next expiry", slog.Any("error", err))

		next, ok = now.Add(sweepRetry), true
	}

	if ok && !next.After(now) || skipped {
		next, ok = now.Add(sweepRetry), true
	}

	if ok {
		r.armSweep(next)
	}
}

func (r *Registrar) sweepExpired(ctx context.Context) bool {
	impis, err := r.cfg.DB.ListExpiredIMPIs(ctx, r.clock.Now())
	if err != nil {
		r.log.Warn("failed to list expired registrations", slog.Any("error", err))
		return true
	}

	skipped := false

	for _, impi := range impis {
		if !r.tryLock(impi) {
			skipped = true
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
				if _, err := r.serverAssignment(r.ctx, impi, []string{impu}, assignTimeoutDeregistration, false); err != nil {
					r.log.Warn("failed to tell the HSS of an expired registration", slog.String("impi", impi),
						slog.Any("error", err))
				}
			}
		}()
	}

	return skipped
}

func (r *Registrar) sweepIMPI(ctx context.Context, impi string) ([]*outgoing, []string) {
	st, err := r.load(ctx, impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", impi), slog.Any("error", err))
		return nil, nil
	}

	var expiredIMPUs []string

	out := r.expireSubscriptions(ctx, st, impi)

	var ch change

	for _, reg := range st.regs {
		live := st.live(reg.Bindings)
		if len(live) == len(reg.Bindings) {
			continue
		}

		if expired := without(reg.Bindings, live); len(expired) > 0 {
			ch.removed = append(ch.removed, removal{reg: reg, bindings: expired, event: regevent.Expired})
		}

		if len(live) > 0 {
			reg.Bindings = live
			if _, err := r.cfg.DB.SaveRegistration(ctx, reg); err != nil {
				r.log.Warn("failed to remove expired contacts", slog.String("impi", impi), slog.Any("error", err))
			}

			continue
		}

		r.log.Info("registration expired", slog.String("impi", impi), slog.String("impu", reg.IMPU))

		if err := r.cfg.DB.DeleteRegistration(ctx, reg.ID); err != nil {
			r.log.Warn("failed to delete an expired registration", slog.String("impi", impi), slog.Any("error", err))
		}

		expiredIMPUs = append(expiredIMPUs, reg.IMPU)
	}

	if len(ch.removed) == 0 {
		return out, expiredIMPUs
	}

	return append(out, r.notifyChange(ctx, impi, ch)...), expiredIMPUs
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

		r.log.Info("reg event subscription expired", slog.String("impi", impi), slog.String("impu", s.IMPU))

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
