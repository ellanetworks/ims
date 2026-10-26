package ipsmgw

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
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
type hssSync struct {
	hss    HSS
	store  Store
	number func() string
	// want is the user's registration to have: available, and its identity; of the registrar, under its lock.
	want func(userKey string) (User, bool)
	log  *slog.Logger

	mu sync.Mutex
	// told are the users the HSS has the IP-SM-GW registered for, of the number it was given.
	told     map[string]db.IPSMGWHSSRegistration
	dirty    map[string]bool
	attempts map[string]int
	wake     chan struct{}
	stop     context.CancelFunc
	done     chan struct{}
}

func newHSSSync(hss HSS, store Store, number func() string, want func(string) (User, bool), told []db.IPSMGWHSSRegistration,
	log *slog.Logger,
) *hssSync {
	h := &hssSync{
		hss: hss, store: store, number: number, want: want, log: log,
		told: map[string]db.IPSMGWHSSRegistration{}, dirty: map[string]bool{}, attempts: map[string]int{},
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}

	for _, r := range told {
		h.told[r.UserKey] = r
		h.dirty[r.UserKey] = true
	}

	return h
}

func (h *hssSync) start() {
	ctx, cancel := context.WithCancel(context.Background())
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

// mark has the user of userKey checked.
func (h *hssSync) mark(userKey string) {
	h.mu.Lock()
	h.dirty[userKey] = true
	h.mu.Unlock()

	h.poke()
}

// markAll has every user checked at once: those registered, and those of userKeys, as when the number changes, or
// once the core that sends the updates runs again.
func (h *hssSync) markAll(userKeys []string) {
	h.mu.Lock()

	for k := range h.told {
		h.dirty[k] = true
	}

	for _, k := range userKeys {
		h.dirty[k] = true
	}

	clear(h.attempts)

	h.mu.Unlock()

	h.poke()
}

// rekey has the HSS registration of the user of the key from, if any, be that of the key to, of the same user,
// which a later third-party REGISTER told more of: the HSS keeps one registration of a user (TS 29.328 §6.1.2.1 step
// 4b), which the user's next update, of its new identity, replaces, and which deregistering the user of its former
// identity would delete.
func (h *hssSync) rekey(from, to string) {
	h.mu.Lock()

	r, ok := h.told[from]
	_, taken := h.told[to]

	if ok && !taken {
		delete(h.told, from)

		r.UserKey = to
		h.told[to] = r
	}

	h.mu.Unlock()

	if ok && !taken {
		if err := h.store.DeleteIPSMGWHSSRegistration(context.Background(), from); err != nil {
			h.log.Error("failed to store an IP-SM-GW registration at the HSS", slog.String("user", from), slog.Any("error", err))
		}

		if err := h.store.PutIPSMGWHSSRegistration(context.Background(), r); err != nil {
			h.log.Error("failed to store an IP-SM-GW registration at the HSS", slog.String("user", to), slog.Any("error", err))
		}
	}

	h.mark(from)
	h.mark(to)
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
		k, ok := h.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-h.wake:
				continue
			}
		}

		h.sync(ctx, k)

		if ctx.Err() != nil {
			return
		}
	}
}

// next takes a user to check, if any.
func (h *hssSync) next() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for k := range h.dirty {
		delete(h.dirty, k)
		return k, true
	}

	return "", false
}

// sync updates the HSS registration of the user of k, if it is not the one to have: of the number while the user is
// available and the IP-SM-GW has a number, else none.
func (h *hssSync) sync(ctx context.Context, k string) {
	u, available := h.want(k)
	number := h.number()

	h.mu.Lock()
	told, registered := h.told[k]
	h.mu.Unlock()

	want := ""
	if available && number != "" {
		want = number
	}

	if want == told.Number {
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

	rctx, cancel := context.WithTimeout(ctx, hssReqTimeout)
	err := h.hss.UpdateSMSRegistration(rctx, u, want)

	cancel()

	if errors.Is(err, ErrRejected) {
		h.settled(k)
		h.log.Error("IP-SM-GW registration at the HSS refused", slog.String("user", k), slog.Bool("register", want != ""),
			slog.Any("error", err))

		return
	}

	if err != nil {
		if ctx.Err() != nil {
			return
		}

		after := h.retry(k)
		h.log.Warn("IP-SM-GW registration at the HSS failed", slog.String("user", k), slog.Bool("register", want != ""),
			slog.Any("error", err), slog.Duration("retry_after", after))

		return
	}

	h.settled(k)

	if want == "" {
		h.mu.Lock()
		delete(h.told, k)
		h.mu.Unlock()

		if err := h.store.DeleteIPSMGWHSSRegistration(context.WithoutCancel(ctx), k); err != nil {
			h.log.Error("failed to store an IP-SM-GW deregistration at the HSS", slog.String("user", k), slog.Any("error", err))
		}

		h.log.Debug("IP-SM-GW deregistered at the HSS", slog.String("user", k), slog.Bool("registered", registered))

		return
	}

	r := db.IPSMGWHSSRegistration{UserKey: k, MSISDN: u.MSISDN, Number: want}
	if u.MSISDN == "" {
		r.IMPU = u.IMPU.String()
	}

	h.mu.Lock()
	h.told[k] = r
	h.mu.Unlock()

	if err := h.store.PutIPSMGWHSSRegistration(context.WithoutCancel(ctx), r); err != nil {
		h.log.Error("failed to store an IP-SM-GW registration at the HSS", slog.String("user", k), slog.Any("error", err))
	}

	h.log.Debug("IP-SM-GW registered at the HSS", slog.String("user", k))

	// The user may have changed while the HSS answered.
	h.mark(k)
}

func (h *hssSync) settled(k string) {
	h.mu.Lock()
	delete(h.attempts, k)
	h.mu.Unlock()
}

// retry checks the user of k again after a wait, doubling from minHSSRetry to maxHSSRetry, and returns the wait.
func (h *hssSync) retry(k string) time.Duration {
	h.mu.Lock()
	after := min(minHSSRetry<<min(h.attempts[k], 8), maxHSSRetry)
	h.attempts[k]++
	h.mu.Unlock()

	time.AfterFunc(after, func() { h.mark(k) })

	return after
}

// shutdownParallelism bounds the deregistrations at the HSS in flight at a shutdown.
const shutdownParallelism = 16

// deregisterAll deregisters the IP-SM-GW at the HSS for every user it is registered for, until ctx is done, once the
// updates stopped: while the IP-SM-GW is down, the HSS routes the short messages to the users' other serving nodes
// (TS 23.204 §6.2.2: a trigger of the IP-SM-GW of a network initiated deregistration). Those left at the deadline
// stay registered. The registrations of the users are kept, so that the next start registers the IP-SM-GW again for
// those available.
func (h *hssSync) deregisterAll(ctx context.Context) {
	h.mu.Lock()
	told := make([]db.IPSMGWHSSRegistration, 0, len(h.told))

	for _, r := range h.told {
		told = append(told, r)
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

			u := User{MSISDN: r.MSISDN}
			if r.MSISDN == "" {
				u.IMPU, _ = sip.ParseURI(r.IMPU)
			}

			if err := h.hss.UpdateSMSRegistration(ctx, u, ""); err != nil {
				h.log.Warn("IP-SM-GW not deregistered at the HSS at shutdown", slog.String("user", r.UserKey),
					slog.Any("error", err))

				return
			}

			h.mu.Lock()
			delete(h.told, r.UserKey)
			h.mu.Unlock()

			if err := h.store.DeleteIPSMGWHSSRegistration(context.WithoutCancel(ctx), r.UserKey); err != nil {
				h.log.Error("failed to store an IP-SM-GW deregistration at the HSS", slog.String("user", r.UserKey),
					slog.Any("error", err))
			}
		})
	}

	wg.Wait()
}
