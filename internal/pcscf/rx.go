package pcscf

import (
	"context"
	"errors"
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
)

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

	OriginStateID    uint32
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
	cfg Rx
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	now    func() time.Time

	mu       sync.Mutex
	closed   bool
	sessions map[string]*rxSession
	retry    map[netip.Addr]retryHold
	stop     chan struct{}
}

func newRxClient(cfg Rx, logger *slog.Logger, now func() time.Time) *rxClient {
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
		cfg: cfg, log: logger, ctx: ctx, cancel: cancel, now: now,
		sessions: make(map[string]*rxSession), retry: make(map[netip.Addr]retryHold), stop: make(chan struct{}),
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

// RFC 6733 §8.16; TS 29.214 §5.6.1, §5.6.4
func (c *rxClient) withOriginState(req *diameter.Message) {
	if c.cfg.OriginStateID != 0 {
		req.AVPs = append(req.AVPs, diameter.Unsigned32(diameter.AVPOriginStateID, diameter.AVPFlagMandatory, 0,
			c.cfg.OriginStateID))
	}
}

func (c *rxClient) do(req *diameter.Message, wait time.Duration) (*diameter.Message, error) {
	opts := []diameter.DoOption{diameter.FailFast()}
	if wait > 0 {
		opts = nil
	} else {
		wait = c.cfg.Timeout
	}

	ctx, cancel := context.WithTimeout(c.ctx, wait)
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

	c.withOriginState(req)

	ans, err := c.do(req, wait)
	if err != nil {
		return nil, err
	}

	a, err := rx.ParseAAAnswer(ans)

	return a.Class, err
}

// TS 29.214 §4.4.4, RFC 6733 §8.4: retried until the PCRF answers or the P-CSCF stops.
func (c *rxClient) endLocked(s *rxSession, cause rx.TerminationCause, wait time.Duration) error {
	if s.ended {
		return nil
	}

	s.ended = true

	if s.call != nil && !s.opened {
		return nil
	}

	req, err := rx.NewSessionTerminationRequest(c.envelope(s.id), rx.SessionTerminationRequest{Cause: cause, Class: s.class})
	if err != nil {
		c.log.Warn("building the Rx STR failed", slog.String("session", s.id), slog.Any("error", err))
		return err
	}

	c.withOriginState(req)

	attrs := []any{
		slog.String("impi", s.key.impi), slog.String("ue", s.key.ue.String()), slog.String("session", s.id),
		slog.String("cause", cause.String()),
	}

	for delay := c.cfg.STRRetry; ; delay = min(2*delay, maxSTRRetry) {
		start := time.Now()

		ans, err := c.do(req, wait)
		if err == nil {
			_, err = rx.ParseSessionTerminationAnswer(ans)
		}

		switch {
		case err == nil:
			c.log.Info("Rx session terminated", attrs...)
			return nil
		case !unanswered(err) || c.closing():
			c.log.Warn("Rx session termination failed", append(attrs, slog.Any("error", err))...)
			return err
		}

		c.log.Warn("Rx session termination failed, will retry", append(attrs, slog.Any("error", err),
			slog.Duration("retry_in", delay))...)

		// Without a link, the next attempt waits for it, so it goes out as soon as the PCRF is back.
		if errors.Is(err, diameter.ErrNotConnected) {
			wait = delay
		}

		timer := time.NewTimer(delay - time.Since(start))

		select {
		case <-c.stop:
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}

// RFC 6733 §7.1.3, §7.1.4, §8.4.2: any STA ends the session at the PCRF,
// except one that asks for a retry.
func unanswered(err error) bool {
	if err == nil || errors.Is(err, rx.ErrMalformedAnswer) || errors.Is(err, diameter.ErrUnknownPeer) ||
		errors.Is(err, diameter.ErrApplicationUnsupported) {
		return false
	}

	r, ok := tgpp.ResultOf(err)
	if !ok {
		return true
	}

	return r.Transient() || !r.Experimental && (r.Code == diameter.ResultUnableToDeliver || r.Code == diameter.ResultTooBusy)
}

func (c *rxClient) end(s *rxSession, cause rx.TerminationCause, wait time.Duration) {
	c.spawn(func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		_ = c.endLocked(s, cause, wait)
		c.forget(s)
	})
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

	_ = p.rx.endLocked(s, cause, wait)
	p.rx.forget(s)
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

			if err != nil && (errors.Is(err, context.Canceled) || p.rx.closing()) {
				p.rx.forget(s)
				s.mu.Unlock()

				return
			}

			p.detachRx(s)
			p.rx.forget(s)
			s.mu.Unlock()

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
		return nil, false
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
