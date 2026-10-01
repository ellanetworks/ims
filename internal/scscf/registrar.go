// Package scscf is the S-CSCF: it registers UEs (TS 24.229 §5.4.1).
package scscf

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	// regAwaitAuth is how long a challenge waits for its answer (TS 24.229
	// Table 7.7.1).
	regAwaitAuth = 4 * time.Minute

	sweepInterval = 30 * time.Second

	// cxTimeout bounds one MAR or SAR.
	cxTimeout = 10 * time.Second

	// retryAfter is the Retry-After of a 500, in seconds.
	retryAfter = 30
)

// Diameter is what the registrar needs from the Diameter node.
type Diameter interface {
	Identity() diameter.Identity
	NewSessionID() string
	Peer(id string) (diameter.PeerStatus, bool)
	Do(ctx context.Context, peerID string, req *diameter.Message) (*diameter.Message, error)
}

// HSS is the Cx peer.
type HSS struct {
	ID    string
	Host  string
	Realm string
}

// Clock tells the time and runs the reg-await-auth and sweep timers.
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
	// Port is the SIP port, in the S-CSCF's name and the Service-Route.
	Port       int
	MinExpires time.Duration
	MaxExpires time.Duration

	HSS      HSS
	Diameter Diameter
	DB       *db.DB

	// Clock defaults to the system clock.
	Clock  Clock
	Logger *slog.Logger
}

// Registrar answers REGISTER.
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
	busy       map[string]bool
	challenges map[string]*challenge
	sweep      transaction.Timer
}

// challenge is an AKA challenge waiting for the UE's answer, keyed by IMPI.
type challenge struct {
	callID string
	impu   string
	nonce  string
	vector authVector
	timer  transaction.Timer
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
		serverName: "sip:scscf." + cfg.HomeDomain + ":" + strconv.Itoa(cfg.Port),
		ctx:        ctx,
		cancel:     cancel,
		busy:       make(map[string]bool),
		challenges: make(map[string]*challenge),
	}

	r.mu.Lock()
	r.scheduleSweep()
	r.mu.Unlock()

	return r
}

// HandleRequest answers a REGISTER. The Cx exchanges run on their own
// goroutine, so it returns at once.
func (r *Registrar) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	if !r.start() {
		r.respond(tx, retryLater(req))
		return
	}

	go func() {
		defer r.wg.Done()

		r.respond(tx, r.register(r.ctx, req))
	}()
}

// Close stops the timers and waits for the REGISTERs being handled.
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

// start registers a goroutine with the wait group, unless the registrar is
// closed.
func (r *Registrar) start() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return false
	}

	r.wg.Add(1)

	return true
}

func (r *Registrar) respond(tx *transaction.ServerTransaction, res *sip.Response) {
	if err := tx.Respond(res); err != nil {
		r.log.Debug("SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
	}
}

// lock marks an IMPI busy for the time of one REGISTER. It fails when another
// REGISTER of the IMPI is being handled.
func (r *Registrar) lock(impi string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.busy[impi] {
		return false
	}

	r.busy[impi] = true

	return true
}

func (r *Registrar) unlock(impi string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.busy, impi)
}

// putChallenge stores a challenge, replacing the IMPI's previous one, and
// drops it when reg-await-auth expires.
func (r *Registrar) putChallenge(impi string, ch *challenge) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if old := r.challenges[impi]; old != nil {
		old.timer.Stop()
	}

	ch.timer = r.clock.AfterFunc(regAwaitAuth, func() {
		r.mu.Lock()
		defer r.mu.Unlock()

		if r.challenges[impi] == ch {
			delete(r.challenges, impi)
			r.log.Debug("reg-await-auth expired", slog.String("impi", impi))
		}
	})
	r.challenges[impi] = ch
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

// scheduleSweep arms the expiry sweep. The caller holds r.mu.
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

// sweepExpired deletes the expired registrations and tells the HSS.
func (r *Registrar) sweepExpired(ctx context.Context) {
	regs, err := r.cfg.DB.DeleteExpiredRegistrations(ctx, r.clock.Now())
	if err != nil {
		r.log.Warn("failed to delete expired registrations", slog.Any("error", err))
		return
	}

	for _, reg := range regs {
		var impus []string
		if len(reg.Identities) > 0 {
			impus = []string{reg.Identities[0].URI}
		}

		r.log.Info("registration expired", slog.String("impi", reg.IMPI))

		if _, err := r.serverAssignment(ctx, reg.IMPI, impus, assignTimeoutDeregistration, false); err != nil {
			r.log.Warn("failed to tell the HSS of an expired registration",
				slog.String("impi", reg.IMPI), slog.Any("error", err))
		}
	}
}

func serviceRoute(homeDomain string, port int, id int64) string {
	return fmt.Sprintf("<sip:orig-%d@scscf.%s:%d;lr>", id, homeDomain, port)
}
