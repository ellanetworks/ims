package pcscf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
)

const (
	DefaultRxTimeout = 10 * time.Second

	restoreWait = time.Minute

	DefaultSTRRetry = 5 * time.Second
	maxSTRRetry     = 5 * time.Minute
	maxSTRAge       = 24 * time.Hour
)

var errUnsendable = errors.New("rx: STR cannot be built")

type RxTerminationStore interface {
	SaveRxTermination(context.Context, db.RxTermination) error
	GetRxTermination(context.Context, string) (db.RxTermination, error)
	DeleteRxTermination(context.Context, string) error
	ListRxTerminations(context.Context) ([]db.RxTermination, error)
}

type Diameter interface {
	Identity() diameter.Identity
	NewSessionID() string
	Do(ctx context.Context, peerID string, req *diameter.Message, opts ...diameter.DoOption) (*diameter.Message, error)
}

type PCRF struct {
	ID    string
	Host  string
	Realm string
}

type Rx struct {
	Diameter    Diameter
	PCRF        PCRF
	Timeout     time.Duration
	CallTimeout time.Duration

	MediaLossTimeout time.Duration
	STRRetry         time.Duration
}

type rxSession struct {
	id  string
	key regKey

	call    *callRx
	pending atomic.Bool

	mu     sync.Mutex
	ended  bool
	opened bool
	class  [][]byte
}

type rxClient struct {
	cfg   Rx
	store RxTerminationStore
	log   *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	now    func() time.Time

	mu       sync.Mutex
	closed   bool
	sessions map[string]*rxSession
	retry    map[netip.Addr]retryHold

	owed     chan struct{}
	up       chan struct{}
	stop     chan struct{}
	resender sync.Once
}

func newRxClient(cfg Rx, store RxTerminationStore, logger *slog.Logger, now func() time.Time) *rxClient {
	if cfg.Diameter == nil {
		return nil
	}

	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultRxTimeout
	}

	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultRxCallTimeout
	}

	if cfg.MediaLossTimeout <= 0 {
		cfg.MediaLossTimeout = DefaultMediaLossTimeout
	}

	if cfg.STRRetry <= 0 {
		cfg.STRRetry = DefaultSTRRetry
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &rxClient{
		cfg: cfg, store: store, log: logger, ctx: ctx, cancel: cancel, now: now,
		sessions: make(map[string]*rxSession), retry: make(map[netip.Addr]retryHold),
		owed: make(chan struct{}, 1), up: make(chan struct{}, 1), stop: make(chan struct{}),
	}
}

// TS 29.214 §4.4.4
func (c *rxClient) close() {
	c.mu.Lock()

	var calls []*rxSession

	for _, s := range c.sessions {
		if s.call != nil {
			calls = append(calls, s)
		}
	}

	c.mu.Unlock()

	for _, s := range calls {
		s.call.end()
		c.end(s, rx.TerminationAdministrative, 0)
	}

	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.stop)
	}
	c.mu.Unlock()

	done := make(chan struct{})

	go func() {
		c.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(c.cfg.Timeout)
	defer timer.Stop()

	select {
	case <-done:
	case <-timer.C:
	}

	c.cancel()
	<-done
}

func (c *rxClient) closing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closed
}

func (c *rxClient) spawn(f func()) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return false
	}

	c.wg.Add(1)

	go func() {
		defer c.wg.Done()

		f()
	}()

	return true
}

func (c *rxClient) begin(k regKey) *rxSession {
	s := &rxSession{id: c.cfg.Diameter.NewSessionID(), key: k}
	s.mu.Lock()

	c.mu.Lock()
	c.sessions[s.id] = s
	c.mu.Unlock()

	return s
}

func (c *rxClient) track(id string, k regKey, class [][]byte) *rxSession {
	c.mu.Lock()
	defer c.mu.Unlock()

	if s, ok := c.sessions[id]; ok {
		return s
	}

	s := &rxSession{id: id, key: k, class: class}
	c.sessions[id] = s

	return s
}

func (c *rxClient) lookup(id string) (*rxSession, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	s, ok := c.sessions[id]

	return s, ok
}

func (c *rxClient) forget(s *rxSession) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sessions[s.id] == s {
		delete(c.sessions, s.id)
	}
}

func (c *rxClient) envelope(id string) tgpp.Envelope {
	return tgpp.Envelope{
		SessionID:        id,
		Origin:           c.cfg.Diameter.Identity(),
		DestinationHost:  c.cfg.PCRF.Host,
		DestinationRealm: c.cfg.PCRF.Realm,
	}
}

func (c *rxClient) do(ctx context.Context, req *diameter.Message, wait time.Duration) (*diameter.Message, error) {
	opts := []diameter.DoOption{diameter.FailFast()}
	if wait > 0 {
		opts = nil
	} else {
		wait = c.cfg.Timeout
	}

	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	return c.cfg.Diameter.Do(ctx, c.cfg.PCRF.ID, req, opts...)
}

// TS 29.214 §4.4.5, §5.3.13
func (c *rxClient) aar(s *rxSession, wait time.Duration) ([][]byte, error) {
	initial := rx.RequestInitial
	control := rx.MediaControl
	signalling := rx.FlowUsageAFSignalling

	r := rx.AARequest{
		MediaComponents: []rx.MediaComponent{{
			Number:        0,
			Type:          &control,
			SubComponents: []rx.MediaSubComponent{{FlowNumber: 0, FlowUsage: &signalling}},
		}},
		SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer},
		RequestType:     &initial,
	}

	if s.key.ue.Is4() {
		r.FramedIPAddress = s.key.ue
	} else {
		r.FramedIPv6Address = s.key.ue
	}

	req, err := rx.NewAARequest(c.envelope(s.id), r)
	if err != nil {
		return nil, err
	}

	ans, err := c.do(c.ctx, req, wait)
	if err != nil {
		return nil, err
	}

	a, err := rx.ParseAAAnswer(ans)

	return a.Class, err
}

// TS 29.214 §4.4.4, RFC 6733 §8.4: the STR is stored before it is sent and
// deleted once answered, so it is retried until the PCRF answers it.
func (c *rxClient) endLocked(s *rxSession, cause rx.TerminationCause, wait time.Duration) error {
	if !c.oweLocked(s, cause) {
		return nil
	}

	t := db.RxTermination{SessionID: s.id, IMPI: s.key.impi, UEAddress: s.key.ue, Cause: uint32(cause), Class: s.class}

	err := c.str(c.ctx, t, wait)
	if unanswered(err) {
		c.log.Warn("Rx session termination failed, will retry", append(strAttrs(t), slog.Any("error", err))...)
		return err
	}

	c.settle(s.id)

	return err
}

func (c *rxClient) oweLocked(s *rxSession, cause rx.TerminationCause) bool {
	if s.ended {
		return false
	}

	s.ended = true

	if s.call != nil && !s.opened {
		return false
	}

	if c.store == nil {
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()

	t := db.RxTermination{
		SessionID: s.id, IMPI: s.key.impi, UEAddress: s.key.ue, Cause: uint32(cause), Class: s.class, CreatedAt: c.now(),
	}

	if err := c.store.SaveRxTermination(ctx, t); err != nil {
		c.log.Error("saving the pending Rx termination failed", slog.String("session", s.id), slog.Any("error", err))
	}

	return true
}

func (c *rxClient) str(ctx context.Context, t db.RxTermination, wait time.Duration) error {
	cause := rx.TerminationCause(t.Cause)
	if cause == 0 {
		cause = rx.TerminationAdministrative
	}

	req, err := rx.NewSessionTerminationRequest(c.envelope(t.SessionID), rx.SessionTerminationRequest{Cause: cause, Class: t.Class})
	if err != nil {
		c.log.Warn("building the Rx STR failed", slog.String("session", t.SessionID), slog.Any("error", err))
		return fmt.Errorf("%w: %w", errUnsendable, err)
	}

	ans, err := c.do(ctx, req, wait)
	if err == nil {
		_, err = rx.ParseSessionTerminationAnswer(ans)
	}

	attrs := append(strAttrs(t), slog.String("cause", cause.String()))

	switch {
	case err == nil:
		c.log.Info("Rx session terminated", attrs...)
	case !unanswered(err):
		c.log.Info("Rx session termination answered with an error", append(attrs, slog.Any("error", err))...)
	}

	return err
}

func strAttrs(t db.RxTermination) []any {
	return []any{slog.String("impi", t.IMPI), slog.String("ue", t.UEAddress.String()), slog.String("session", t.SessionID)}
}

func (c *rxClient) settle(id string) {
	if c.store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()

	if err := c.store.DeleteRxTermination(ctx, id); err != nil {
		c.log.Error("deleting the pending Rx termination failed", slog.String("session", id), slog.Any("error", err))
	}
}

func (c *rxClient) end(s *rxSession, cause rx.TerminationCause, wait time.Duration) {
	if c.spawn(func() {
		s.mu.Lock()
		err := c.endLocked(s, cause, wait)
		s.mu.Unlock()

		c.forget(s)

		if unanswered(err) {
			signal(c.owed)
		}
	}) {
		return
	}

	s.mu.Lock()
	c.oweLocked(s, cause)
	s.mu.Unlock()

	c.forget(s)
}

// RFC 6733 §7.1.3, §7.1.4, §8.4.2: any STA ends the session at the PCRF,
// except one that asks for a retry.
func unanswered(err error) bool {
	if err == nil || errors.Is(err, rx.ErrMalformedAnswer) || errors.Is(err, errUnsendable) {
		return false
	}

	r, ok := tgpp.ResultOf(err)
	if !ok {
		return true
	}

	return r.Transient() || !r.Experimental && (r.Code == diameter.ResultUnableToDeliver || r.Code == diameter.ResultTooBusy)
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (c *rxClient) resendOwed() {
	if c.store == nil {
		return
	}

	c.resender.Do(func() { c.spawn(c.resendLoop) })
}

// RFC 6733 §8.5: an ASR for a session still owed an STR is answered, then the STR follows.
func (c *rxClient) abortOwed(id string) (terminate func(), known bool) {
	if c.store == nil {
		return nil, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()

	if _, err := c.store.GetRxTermination(ctx, id); err != nil {
		return nil, false
	}

	return func() { signal(c.up) }, true
}

// RFC 6733 §8.4
func (c *rxClient) resendLoop() {
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()

	go func() {
		select {
		case <-c.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	delay := c.cfg.STRRetry

	for {
		if !c.resendPass(ctx) {
			delay = c.cfg.STRRetry

			select {
			case <-ctx.Done():
				return
			case <-c.owed:
			case <-c.up:
			}

			continue
		}

		timer := time.NewTimer(delay)

		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-c.up:
			timer.Stop()
		case <-timer.C:
			delay = min(2*delay, maxSTRRetry)
		}
	}
}

func (c *rxClient) resendPass(ctx context.Context) (left bool) {
	owed, err := c.store.ListRxTerminations(ctx)
	if err != nil {
		c.log.Warn("listing the pending Rx terminations failed", slog.Any("error", err))
		return ctx.Err() == nil
	}

	for _, t := range owed {
		if _, live := c.lookup(t.SessionID); live {
			continue
		}

		if c.now().Sub(t.CreatedAt) > maxSTRAge {
			c.log.Warn("Rx session termination abandoned", append(strAttrs(t), slog.Duration("age", maxSTRAge))...)
			c.settle(t.SessionID)

			continue
		}

		if err := c.str(ctx, t, c.cfg.Timeout); unanswered(err) {
			c.log.Debug("Rx session termination still pending", append(strAttrs(t), slog.Any("error", err))...)
			return true
		}

		c.settle(t.SessionID)
	}

	return false
}

// TS 29.213 Annex B.1
func (p *PCSCF) openRx(k regKey, wait time.Duration) {
	s := p.rx.begin(k)

	attached := p.regs.edit(k, func(r *db.PCSCFRegistration) bool {
		if r.RxSessionID != "" {
			return false
		}

		r.RxSessionID = s.id

		return true
	})

	if attached && p.rx.spawn(func() { p.initialAAR(s, wait) }) {
		return
	}

	if attached {
		p.detachRx(s)
		p.rx.settle(s.id)
	}

	s.ended = true
	p.rx.forget(s)
	s.mu.Unlock()
}

func (p *PCSCF) initialAAR(s *rxSession, wait time.Duration) {
	defer s.mu.Unlock()

	attrs := []any{slog.String("impi", s.key.impi), slog.String("ue", s.key.ue.String()), slog.String("session", s.id)}

	class, err := p.rx.aar(s, wait)
	if err == nil {
		s.class = class

		if len(class) > 0 {
			p.regs.edit(s.key, func(r *db.PCSCFRegistration) bool {
				if r.RxSessionID != s.id {
					return false
				}

				r.RxClass = class

				return true
			})
		}

		p.log.Info("Rx session for IMS signalling opened", attrs...)

		return
	}

	attrs = append(attrs, slog.Any("error", err))
	if result, ok := tgpp.ResultOf(err); ok {
		attrs = append(attrs, slog.String("result", result.String()))
	}

	var refused *rx.ResultError

	if errors.As(err, &refused) || errors.Is(err, diameter.ErrUnknownPeer) ||
		errors.Is(err, diameter.ErrApplicationUnsupported) {
		p.log.Warn("Rx session for IMS signalling refused", attrs...)
		p.detachRx(s)
		p.rx.settle(s.id)

		s.ended = true
		p.rx.forget(s)

		return
	}

	if errors.Is(err, context.Canceled) || p.rx.closing() {
		return
	}

	p.log.Warn("Rx session for IMS signalling failed", attrs...)
	p.detachRx(s)

	cause, wait := rx.TerminationAdministrative, time.Duration(0)

	switch {
	case errors.Is(err, rx.ErrMalformedAnswer):
		cause = rx.TerminationBadAnswer
	case errors.Is(err, diameter.ErrNotConnected):
		wait = p.rx.cfg.Timeout
	}

	err = p.rx.endLocked(s, cause, wait)
	p.rx.forget(s)

	if unanswered(err) {
		signal(p.rx.owed)
	}
}

func (p *PCSCF) detachRx(s *rxSession) {
	p.regs.edit(s.key, func(r *db.PCSCFRegistration) bool {
		if r.RxSessionID != s.id {
			return false
		}

		r.RxSessionID, r.RxClass = "", nil

		return true
	})
}

// RFC 3680 §5.2, RFC 3588 §8.15
func terminationCause(e regevent.Event) rx.TerminationCause {
	switch e {
	case regevent.Unregistered:
		return rx.TerminationLogout
	case regevent.Expired:
		return rx.TerminationAuthExpired
	default:
		return rx.TerminationAdministrative
	}
}

func graver(a, b regevent.Event) regevent.Event {
	rank := func(e regevent.Event) int {
		switch {
		case e == "":
			return 0
		case terminationCause(e) == rx.TerminationAuthExpired:
			return 1
		case terminationCause(e) == rx.TerminationLogout:
			return 2
		default:
			return 3
		}
	}

	if rank(b) > rank(a) {
		return b
	}

	return a
}

func (p *PCSCF) endRx(r db.PCSCFRegistration, cause rx.TerminationCause, wait time.Duration) {
	if p.rx == nil || r.RxSessionID == "" {
		return
	}

	s := p.rx.track(r.RxSessionID, regKey{r.IMPI, r.UEAddress.Addr()}, r.RxClass)
	p.rx.end(s, cause, wait)
}

// RFC 6733 §8.16
func (p *PCSCF) restoreRx(expired []db.PCSCFRegistration) {
	if p.rx == nil {
		return
	}

	p.rx.resendOwed()

	for _, r := range expired {
		p.endRx(r, rx.TerminationAuthExpired, restoreWait)
	}

	for _, r := range p.regs.all() {
		if r.RxSessionID == "" {
			continue
		}

		s := p.rx.track(r.RxSessionID, regKey{r.IMPI, r.UEAddress.Addr()}, r.RxClass)

		p.rx.spawn(func() {
			s.mu.Lock()

			err := p.rx.endLocked(s, rx.TerminationAdministrative, restoreWait)

			// The record keeps the session, so the next restore ends it.
			if err != nil && (errors.Is(err, context.Canceled) || p.rx.closing()) {
				p.rx.settle(s.id)
				p.rx.forget(s)
				s.mu.Unlock()

				return
			}

			p.detachRx(s)

			if !unanswered(err) {
				p.rx.settle(s.id)
			}

			p.rx.forget(s)
			s.mu.Unlock()

			if unanswered(err) {
				signal(p.rx.owed)
			}

			p.openRx(s.key, restoreWait)
		})
	}
}

// TS 24.229 §5.2.6.4.3
func (p *PCSCF) signallingRestored(req *sip.Request) {
	if p.regs.lost.Load() == 0 {
		return
	}

	k := regKey{privateIdentity(req), req.Flow.Remote.Addr().Unmap()}

	if req.Method != "REGISTER" {
		var ok bool
		if k, ok = p.ueKey(req); !ok {
			return
		}
	}

	if p.regs.restoreSignalling(k) {
		p.log.Info("IMS signalling path restored", slog.String("impi", k.impi), slog.String("ue", k.ue.String()),
			slog.String("method", req.Method))
	}
}

// RFC 6733 §8.4
func (p *PCSCF) PCRFOpen(peerID string) {
	if p.rx != nil && peerID == p.rx.cfg.PCRF.ID {
		signal(p.rx.up)
	}
}

// TS 29.214 §4.4.6.3
func (p *PCSCF) ReAuth(sessionID string, r rx.ReAuthRequest) bool {
	if p.rx == nil {
		return false
	}

	s, ok := p.rx.lookup(sessionID)
	if !ok {
		return false
	}

	if s.call != nil {
		p.log.Info("Rx re-authorization of a call", slog.String("impi", s.key.impi), slog.String("ue", s.key.ue.String()),
			slog.String("session", s.id), slog.Any("actions", r.SpecificActions))

		p.callReAuth(s, r)

		return true
	}

	lost := slices.ContainsFunc(r.SpecificActions, func(a rx.SpecificAction) bool {
		return a == rx.ActionIndicationOfLossOfBearer || a == rx.ActionIndicationOfReleaseOfBearer
	})

	if lost && p.regs.edit(s.key, func(reg *db.PCSCFRegistration) bool {
		if reg.RxSessionID != s.id {
			return false
		}

		reg.SignallingLost = true

		return true
	}) {
		p.log.Info("IMS signalling path lost", slog.String("impi", s.key.impi), slog.String("ue", s.key.ue.String()),
			slog.Any("actions", r.SpecificActions))
	}

	return true
}

// TS 29.214 §4.4.6.1
func (p *PCSCF) AbortSession(sessionID string, r rx.AbortSessionRequest) (terminate func(), known bool) {
	if p.rx == nil {
		return nil, false
	}

	s, ok := p.rx.lookup(sessionID)
	if !ok {
		return p.rx.abortOwed(sessionID)
	}

	if s.call != nil {
		p.log.Info("call media aborted by the PCRF", slog.String("impi", s.key.impi), slog.String("ue", s.key.ue.String()),
			slog.String("session", s.id), slog.String("cause", r.Cause.String()))

		cr := s.call

		cr.mu.Lock()
		if cr.session == s {
			cr.session = nil
		}
		cr.mu.Unlock()

		cr.end()

		if cr.dialog != nil {
			p.releaseCall(cr.call, cr.dialog, r.Cause != rx.AbortInsufficientBearerResources)
		}

		return func() { p.rx.end(s, rx.TerminationAdministrative, 0) }, true
	}

	p.regs.edit(s.key, func(reg *db.PCSCFRegistration) bool {
		if reg.RxSessionID != s.id {
			return false
		}

		reg.RxSessionID, reg.RxClass = "", nil
		reg.SignallingLost = true

		return true
	})

	p.log.Info("IMS signalling path aborted by the PCRF", slog.String("impi", s.key.impi),
		slog.String("ue", s.key.ue.String()), slog.String("cause", r.Cause.String()))

	return func() { p.rx.end(s, rx.TerminationAdministrative, 0) }, true
}
