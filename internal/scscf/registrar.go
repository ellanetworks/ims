package scscf

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	regAwaitAuth = 4 * time.Minute

	sweepInterval = 30 * time.Second

	cxTimeout = 10 * time.Second

	retryAfter = 30

	maxResyncs = 2
)

type Request struct {
	SIP       *sip.Request
	Protected bool
	UEAddress netip.Addr
}

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
	Name       string
	Port       int
	MinExpires time.Duration
	MaxExpires time.Duration

	HSS      HSS
	Diameter Diameter
	DB       *db.DB

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
	sweep      transaction.Timer
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
		serverName: "sip:" + cfg.Name + ":" + strconv.Itoa(cfg.Port),
		ctx:        ctx,
		cancel:     cancel,
		busy:       make(map[string]*hold),
		challenges: make(map[string]*challenge),
	}

	r.mu.Lock()
	r.scheduleSweep()
	r.mu.Unlock()

	return r
}

func (r *Registrar) Register(ctx context.Context, req Request) *sip.Response {
	if !r.start() {
		return retryLater(req.SIP)
	}

	defer r.wg.Done()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()

	return r.register(ctx, req)
}

func (r *Registrar) Close() {
	r.mu.Lock()

	r.closed = true
	r.sweep.Stop()

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

func (r *Registrar) scheduleSweep() {
	r.sweep = r.clock.AfterFunc(sweepInterval, func() {
		if !r.start() {
			return
		}

		r.sweepExpired(r.ctx)
		r.wg.Done()

		r.mu.Lock()
		defer r.mu.Unlock()

		if !r.closed {
			r.scheduleSweep()
		}
	})
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

		r.sweepIMPI(ctx, impi)
		r.unlock(impi)
	}
}

func (r *Registrar) sweepIMPI(ctx context.Context, impi string) {
	st, err := r.load(ctx, impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", impi), slog.Any("error", err))
		return
	}

	for _, reg := range st.regs {
		live := st.live(reg.Bindings)
		if len(live) == len(reg.Bindings) {
			continue
		}

		if len(live) > 0 {
			reg.Bindings = live
			if _, err := r.cfg.DB.SaveRegistration(ctx, reg); err != nil {
				r.log.Warn("failed to remove expired contacts", slog.String("impi", impi), slog.Any("error", err))
			}

			continue
		}

		r.log.Info("registration expired", slog.String("impi", impi), slog.String("impu", reg.IMPU))

		if _, err := r.serverAssignment(ctx, impi, []string{reg.IMPU}, assignTimeoutDeregistration, false); err != nil {
			r.log.Warn("failed to tell the HSS of an expired registration", slog.String("impi", impi), slog.Any("error", err))
		}

		if err := r.cfg.DB.DeleteRegistration(ctx, reg.ID); err != nil {
			r.log.Warn("failed to delete an expired registration", slog.String("impi", impi), slog.Any("error", err))
		}
	}
}

func serviceRoute(name string, port int, contactID int64) string {
	return fmt.Sprintf("<sip:orig-%d@%s:%d;lr>", contactID, name, port)
}
