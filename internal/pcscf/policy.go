package pcscf

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/policy"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/lifetime"
	"github.com/ellanetworks/ims/sip/reginfo"
)

const (
	DefaultPolicyTimeout = 10 * time.Second

	restoreWait = time.Minute

	DefaultTerminationRetry = 5 * time.Second
	maxTerminationRetry     = 5 * time.Minute
)

type Policy struct {
	Backend     policy.Backend
	Timeout     time.Duration
	CallTimeout time.Duration

	MediaLossTimeout time.Duration
	TerminationRetry time.Duration
}

var _ policy.Sink = (*PCSCF)(nil)

type policySession struct {
	id      string
	key     regKey
	backend policy.Backend

	call    *callPolicy
	pending atomic.Bool

	mu     sync.Mutex
	ended  bool
	opened bool
	ref    string
}

type policyClient struct {
	cfg Policy
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	now    func() time.Time

	mu       sync.Mutex
	closed   bool
	sessions map[string]*policySession
	retry    map[netip.Addr]retryHold
	stop     chan struct{}
}

func newPolicyClient(cfg Policy, logger *slog.Logger, now func() time.Time) *policyClient {
	if cfg.Backend == nil {
		return nil
	}

	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultPolicyTimeout
	}

	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultPolicyCallTimeout
	}

	if cfg.MediaLossTimeout <= 0 {
		cfg.MediaLossTimeout = DefaultMediaLossTimeout
	}

	if cfg.TerminationRetry <= 0 {
		cfg.TerminationRetry = DefaultTerminationRetry
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &policyClient{
		cfg: cfg, log: logger, ctx: ctx, cancel: cancel, now: now,
		sessions: make(map[string]*policySession), retry: make(map[netip.Addr]retryHold), stop: make(chan struct{}),
	}
}

// TS 29.214 clause 4.4.4
// TS 29.514 clause 4.2.4.2
func (c *policyClient) close(ctx context.Context) {
	c.mu.Lock()

	var calls []*policySession

	for _, s := range c.sessions {
		if s.call != nil {
			calls = append(calls, s)
		}
	}

	c.mu.Unlock()

	for _, s := range calls {
		s.call.end()
		c.end(ctx, s, policy.TerminationAdministrative, 0)
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
	case <-ctx.Done():
	}

	c.cancel()
	<-done
}

func (c *policyClient) closing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closed
}

func (c *policyClient) spawn(f func()) bool {
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

func (c *policyClient) begin(k regKey) *policySession {
	b := c.resolve(k.ue)
	s := &policySession{id: b.NewSessionID(), key: k, backend: b}
	s.mu.Lock()

	c.mu.Lock()
	c.sessions[s.id] = s
	c.mu.Unlock()

	return s
}

func (c *policyClient) track(id string, k regKey, ref string) *policySession {
	c.mu.Lock()
	defer c.mu.Unlock()

	if s, ok := c.sessions[id]; ok {
		return s
	}

	s := &policySession{id: id, key: k, ref: ref, backend: c.cfg.Backend}
	c.sessions[id] = s

	return s
}

func (c *policyClient) lookup(id string) (*policySession, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	s, ok := c.sessions[id]

	return s, ok
}

func (c *policyClient) forget(s *policySession) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sessions[s.id] == s {
		delete(c.sessions, s.id)
	}
}

func (c *policyClient) resolve(netip.Addr) policy.Backend {
	return c.cfg.Backend
}

func (c *policyClient) serves(p db.PolicySession) bool {
	return strings.EqualFold(p.Endpoint, c.cfg.Backend.Endpoint())
}

func (c *policyClient) deadline(cause context.Context, wait time.Duration) (context.Context, context.CancelFunc) {
	if wait <= 0 {
		wait = c.cfg.Timeout
	}

	return context.WithTimeout(lifetime.With(c.ctx, cause), wait)
}

func (c *policyClient) openSignalling(ctx context.Context, s *policySession, wait time.Duration) (string, error) {
	ctx, cancel := c.deadline(ctx, wait)
	defer cancel()

	return s.backend.OpenSignalling(ctx, s.id, policy.Signalling{UE: s.key.ue}, wait > 0)
}

// TS 29.214 clause 4.4.4
// TS 29.514 clause 4.2.4.2
// RFC 6733 section 8.4
// RFC 6733 section 8.18
func (c *policyClient) endLocked(ctx context.Context, s *policySession, cause policy.Termination, wait time.Duration) error {
	if s.ended {
		return nil
	}

	s.ended = true

	if s.call != nil && !s.opened {
		return nil
	}

	attrs := []any{
		slog.String("impi", s.key.impi), slog.String("ue", s.key.ue.String()), slog.String("session", s.id),
		slog.String("cause", cause.String()),
	}

	for delay := c.cfg.TerminationRetry; ; delay = min(2*delay, maxTerminationRetry) {
		start := time.Now()

		rctx, cancel := c.deadline(ctx, wait)
		err := s.backend.Terminate(rctx, s.id, s.ref, cause, wait > 0)

		cancel()

		switch {
		case err == nil:
			c.log.DebugContext(ctx, "policy session terminated", attrs...)
			return nil
		case errors.Is(err, policy.ErrUnknownSession):
			c.log.DebugContext(ctx, "policy session already ended at the policy function", attrs...)
			return nil
		case errors.Is(err, policy.ErrSessionLost):
			c.log.InfoContext(ctx, "policy session lost with its policy function", append(attrs, slog.Any("error", err))...)
			return nil
		case !policy.Transient(err) || c.closing():
			c.log.WarnContext(ctx, "policy session termination failed", append(attrs, slog.Any("error", err))...)
			return err
		}

		delay = max(delay, policy.Backoff(err))

		c.log.WarnContext(ctx, "policy session termination failed, will retry", append(attrs, slog.Any("error", err),
			slog.Duration("retry_in", delay))...)

		if errors.Is(err, policy.ErrUnreachable) {
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

func (c *policyClient) end(ctx context.Context, s *policySession, cause policy.Termination, wait time.Duration) {
	c.spawn(func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		_ = c.endLocked(ctx, s, cause, wait)
		c.forget(s)
	})
}

// TS 29.213 clause B.1
// TS 29.514 clause 4.2.6.7
func (p *PCSCF) openSignalling(ctx context.Context, k regKey, wait time.Duration) {
	s := p.policy.begin(k)

	attached := p.regs.edit(ctx, k, func(r *db.PCSCFRegistration) bool {
		if r.Policy.ID != "" {
			return false
		}

		r.Policy = db.PolicySession{Endpoint: s.backend.Endpoint(), ID: s.id}

		return true
	})

	if attached && p.policy.spawn(func() { p.initialOpen(ctx, s, wait) }) {
		return
	}

	if attached {
		p.detachPolicy(ctx, s)
	}

	s.ended = true
	p.policy.forget(s)
	s.mu.Unlock()
}

func (p *PCSCF) initialOpen(ctx context.Context, s *policySession, wait time.Duration) {
	defer s.mu.Unlock()

	attrs := []any{slog.String("impi", s.key.impi), slog.String("ue", s.key.ue.String()), slog.String("session", s.id)}

	ref, err := p.policy.openSignalling(ctx, s, wait)
	if err == nil {
		s.ref = ref

		if ref != "" {
			p.regs.edit(ctx, s.key, func(r *db.PCSCFRegistration) bool {
				if r.Policy.ID != s.id {
					return false
				}

				r.Policy.Ref = ref

				return true
			})
		}

		p.log.DebugContext(ctx, "policy session for IMS signalling opened", attrs...)

		return
	}

	attrs = append(attrs, slog.Any("error", err))
	if result, ok := policy.ResultOf(err); ok {
		attrs = append(attrs, slog.String("result", result))
	}

	if errors.Is(err, policy.ErrRefused) || errors.Is(err, policy.ErrUnknownSession) {
		p.log.WarnContext(ctx, "policy session for IMS signalling refused", attrs...)
		p.detachPolicy(ctx, s)

		s.ended = true
		p.policy.forget(s)

		return
	}

	if errors.Is(err, context.Canceled) || p.policy.closing() {
		return
	}

	p.log.WarnContext(ctx, "policy session for IMS signalling failed", attrs...)
	p.detachPolicy(ctx, s)

	cause, wait := policy.TerminationAdministrative, time.Duration(0)

	switch {
	case errors.Is(err, policy.ErrMalformed):
		cause = policy.TerminationBadAnswer
	case errors.Is(err, policy.ErrUnreachable):
		wait = p.policy.cfg.Timeout
	}

	_ = p.policy.endLocked(ctx, s, cause, wait)
	p.policy.forget(s)
}

func (p *PCSCF) detachPolicy(ctx context.Context, s *policySession) {
	p.regs.edit(ctx, s.key, func(r *db.PCSCFRegistration) bool {
		if r.Policy.ID != s.id {
			return false
		}

		r.Policy = db.PolicySession{}

		return true
	})
}

// RFC 3680 section 5.2
func terminationCause(e reginfo.Event) policy.Termination {
	switch e {
	case reginfo.Unregistered:
		return policy.TerminationLogout
	case reginfo.Expired:
		return policy.TerminationExpired
	default:
		return policy.TerminationAdministrative
	}
}

func graver(a, b reginfo.Event) reginfo.Event {
	rank := func(e reginfo.Event) int {
		switch {
		case e == "":
			return 0
		case terminationCause(e) == policy.TerminationExpired:
			return 1
		case terminationCause(e) == policy.TerminationLogout:
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

func (p *PCSCF) endPolicy(ctx context.Context, r db.PCSCFRegistration, cause policy.Termination, wait time.Duration) {
	if p.policy == nil || r.Policy.ID == "" || !p.policy.serves(r.Policy) {
		return
	}

	s := p.policy.track(r.Policy.ID, regKeyOf(&r), r.Policy.Ref)
	p.policy.end(ctx, s, cause, wait)
}

func (p *PCSCF) restorePolicy(ctx context.Context, expired, lost []db.PCSCFRegistration) {
	if p.policy == nil {
		return
	}

	for _, r := range expired {
		p.endPolicy(ctx, r, policy.TerminationExpired, restoreWait)
	}

	for _, r := range lost {
		p.endPolicy(ctx, r, policy.TerminationAdministrative, restoreWait)
	}

	for _, r := range p.regs.all() {
		if r.Policy.ID == "" {
			continue
		}

		s := p.policy.track(r.Policy.ID, regKeyOf(&r), r.Policy.Ref)
		foreign := !p.policy.serves(r.Policy)

		p.policy.spawn(func() {
			s.mu.Lock()

			var err error

			if foreign {
				s.ended = true

				p.log.InfoContext(ctx, "dropping a policy session of another policy function", slog.String("impi", s.key.impi),
					slog.String("ue", s.key.ue.String()), slog.String("session", s.id), slog.String("endpoint", r.Policy.Endpoint))
			} else {
				err = p.policy.endLocked(ctx, s, policy.TerminationAdministrative, restoreWait)
			}

			if err != nil && (errors.Is(err, context.Canceled) || p.policy.closing()) {
				p.policy.forget(s)
				s.mu.Unlock()

				return
			}

			p.detachPolicy(ctx, s)
			p.policy.forget(s)
			s.mu.Unlock()

			p.openSignalling(ctx, s.key, restoreWait)
		})
	}
}

// TS 24.229 clause 5.2.6.4.3
func (p *PCSCF) signallingRestored(ctx context.Context, req *sip.Request) {
	if p.regs.lost.Load() == 0 {
		return
	}

	k := regKey{privateIdentity(req), req.Flow.Remote.Addr().Unmap(), registrationFlow(req)}

	if req.Method != "REGISTER" {
		var ok bool
		if k, ok = p.ueKey(req); !ok {
			return
		}
	}

	if p.regs.restoreSignalling(ctx, k) {
		p.log.InfoContext(ctx, "IMS signalling path restored", slog.String("impi", k.impi), slog.String("ue", k.ue.String()),
			slog.String("method", req.Method))
	}
}

// TS 29.214 clause 4.4.6.2
// TS 29.214 clause 4.4.6.3
// TS 29.214 clause 4.4.6.5
// TS 29.514 clause 4.2.5.2
// TS 29.514 clause 4.2.5.8
// TS 29.514 clause 4.2.5.10
func (p *PCSCF) Notify(ctx context.Context, sessionID string, e policy.Event) bool {
	if p.policy == nil {
		return false
	}

	s, ok := p.policy.lookup(sessionID)
	if !ok {
		return false
	}

	if s.call != nil {
		p.log.DebugContext(ctx, "policy event for a call", slog.String("impi", s.key.impi), slog.String("ue", s.key.ue.String()),
			slog.String("session", s.id), slog.Any("events", e.Kinds))

		p.callNotify(ctx, s, e)

		return true
	}

	if e.Has(policy.EventBearerLost, policy.EventBearerReleased, policy.EventResourcesFailed) && p.regs.edit(ctx, s.key, func(reg *db.PCSCFRegistration) bool {
		if reg.Policy.ID != s.id {
			return false
		}

		reg.SignallingLost = true

		return true
	}) {
		p.log.InfoContext(ctx, "IMS signalling path lost", slog.String("impi", s.key.impi), slog.String("ue", s.key.ue.String()),
			slog.Any("events", e.Kinds))
	}

	return true
}

// TS 29.214 clause 4.4.6.1
// TS 29.514 clause 4.2.5.3
func (p *PCSCF) Abort(ctx context.Context, sessionID string, a policy.Abort) (terminate func(), known bool) {
	if p.policy == nil {
		return nil, false
	}

	s, ok := p.policy.lookup(sessionID)
	if !ok {
		return nil, false
	}

	if s.call != nil {
		p.log.InfoContext(ctx, "call media aborted by the policy function", slog.String("impi", s.key.impi), slog.String("ue", s.key.ue.String()),
			slog.String("session", s.id), slog.String("cause", a.Cause))

		cp := s.call

		cp.mu.Lock()
		if cp.session == s {
			cp.session = nil
		}
		cp.mu.Unlock()

		cp.end()

		if cp.dialog != nil {
			_ = p.releaseCall(ctx, cp.call, cp.dialog, !a.InsufficientResources)
		}

		return func() { p.policy.end(ctx, s, policy.TerminationAdministrative, 0) }, true
	}

	p.regs.edit(ctx, s.key, func(reg *db.PCSCFRegistration) bool {
		if reg.Policy.ID != s.id {
			return false
		}

		reg.Policy = db.PolicySession{}
		reg.SignallingLost = true

		return true
	})

	p.log.InfoContext(ctx, "IMS signalling path aborted by the policy function", slog.String("impi", s.key.impi),
		slog.String("ue", s.key.ue.String()), slog.String("cause", a.Cause))

	return func() { p.policy.end(ctx, s, policy.TerminationAdministrative, 0) }, true
}
