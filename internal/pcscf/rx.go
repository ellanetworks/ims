package pcscf

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
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

	// restoreWait bounds the wait for the PCRF connection when the P-CSCF
	// re-establishes, after a restart, the sessions of its stored records.
	restoreWait = time.Minute
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

// Rx configures the Rx sessions for IMS signalling (TS 29.214 §4.4.5). A nil
// Diameter disables them.
type Rx struct {
	Diameter Diameter
	PCRF     PCRF
	Timeout  time.Duration
}

// rxSession is one Rx session for a registration record. Its mutex is held
// for each request, so that an AAR follows the previous AAA (§4.4.2) and an
// STR follows an outstanding AAA (§4.4.4).
type rxSession struct {
	id  string
	key regKey

	mu    sync.Mutex
	ended bool
	// class holds the AAA's Class values, echoed in the STR (RFC 6733 §8.20).
	class [][]byte
}

type rxClient struct {
	cfg Rx
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	closed   bool
	sessions map[string]*rxSession
}

func newRxClient(cfg Rx, logger *slog.Logger) *rxClient {
	if cfg.Diameter == nil {
		return nil
	}

	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultRxTimeout
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &rxClient{cfg: cfg, log: logger, ctx: ctx, cancel: cancel, sessions: make(map[string]*rxSession)}
}

// close lets the requests in flight finish, for at most the Rx timeout: an STR
// for a record already deleted is not retried after a restart. It then
// cancels the rest.
func (c *rxClient) close() {
	c.mu.Lock()
	c.closed = true
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

// begin creates a session with a new Session-Id, its mutex held.
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

// do sends req to the PCRF. With a zero wait, it fails at once when the PCRF
// is not connected and waits the configured timeout for the answer; with a
// positive wait, it waits that long for the connection and the answer.
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

// aar subscribes to the signalling path status of the UE (§4.4.5): the AF
// signalling component and the bearer loss and release actions, which are
// non-one-time and so only allowed in the initial AAR (§5.3.13). Media-Type
// CONTROL is added for PCRFs that select the QoS from it, such as open5gs.
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

	ans, err := c.do(req, wait)
	if err != nil {
		return nil, err
	}

	a, err := rx.ParseAAAnswer(ans)

	return a.Class, err
}

// endLocked closes s with an STR (§4.4.4), unless it has ended already. The
// caller holds s.mu.
func (c *rxClient) endLocked(s *rxSession, cause rx.TerminationCause, wait time.Duration) {
	if s.ended {
		return
	}

	s.ended = true
	defer c.forget(s)

	req, err := rx.NewSessionTerminationRequest(c.envelope(s.id), rx.SessionTerminationRequest{Cause: cause, Class: s.class})
	if err != nil {
		c.log.Warn("building the Rx STR failed", slog.String("session", s.id), slog.Any("error", err))
		return
	}

	attrs := []any{
		slog.String("impi", s.key.impi), slog.String("ue", s.key.ue.String()), slog.String("session", s.id),
		slog.String("cause", cause.String()),
	}

	ans, err := c.do(req, wait)
	if err == nil {
		_, err = rx.ParseSessionTerminationAnswer(ans)
	}

	if err != nil {
		c.log.Warn("Rx session termination failed", append(attrs, slog.Any("error", err))...)
		return
	}

	c.log.Info("Rx session terminated", attrs...)
}

// end closes s in the background, once any outstanding request is answered.
func (c *rxClient) end(s *rxSession, cause rx.TerminationCause, wait time.Duration) {
	c.spawn(func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		c.endLocked(s, cause, wait)
	})
}

// openRx opens the Rx session of the record k, if it has none. It runs once
// the 200 to the REGISTER has been relayed (TS 29.213 Annex B.1), or after a
// restart.
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

	// Shutting down: the record keeps the session, which the PCRF may hold,
	// and which is ended after the restart.
	if errors.Is(err, context.Canceled) || p.rx.closing() {
		return
	}

	attrs = append(attrs, slog.Any("error", err))
	if result, ok := tgpp.ResultOf(err); ok {
		attrs = append(attrs, slog.String("result", result.String()))
	}

	p.log.Warn("Rx session for IMS signalling refused", attrs...)
	p.detachRx(s)

	var refused *rx.ResultError

	switch {
	case errors.As(err, &refused), errors.Is(err, diameter.ErrUnknownPeer),
		errors.Is(err, diameter.ErrApplicationUnsupported):
		// The PCRF holds no session: it refused it, or the AAR was not sent.
		s.ended = true
		p.rx.forget(s)
	case errors.Is(err, rx.ErrMalformedAnswer):
		// RFC 6733 §7.2, §8.4; RFC 3588 §8.15.
		p.rx.endLocked(s, rx.TerminationBadAnswer, 0)
	default:
		// A timeout or a lost connection: the PCRF may have authorized the
		// session (RFC 6733 §8.4).
		p.rx.endLocked(s, rx.TerminationAdministrative, 0)
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

// terminationCause maps the event that ended a registration's contacts in a
// reg event NOTIFY (RFC 3680 §5.2) to the STR's Termination-Cause (RFC 3588
// §8.15).
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

// graver returns whichever of two contact events maps to the graver
// Termination-Cause: the network's own decisions, then the UE's
// deregistration, then expiry. The order of the contacts in the NOTIFY does
// not matter.
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

// endRx closes the Rx session of a removed record.
func (p *PCSCF) endRx(r db.PCSCFRegistration, cause rx.TerminationCause, wait time.Duration) {
	if p.rx == nil || r.RxSessionID == "" {
		return
	}

	s := p.rx.track(r.RxSessionID, regKey{r.IMPI, r.UEAddress.Addr()}, r.RxClass)
	p.rx.end(s, cause, wait)
}

// restoreRx ends the sessions of the records that expired while the P-CSCF
// was down, and re-establishes those of the others. The PCRF may have dropped
// them: the P-CSCF advertises a new Origin-State-Id at each start (RFC 6733
// §8.16), and a session can have been pending at shutdown. Each is closed
// with an STR and replaced by a new one, so that the loss and release of the
// signalling bearer stay reported.
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

		// Detached first, so that a removal of the record meanwhile does not
		// end the session again.
		p.detachRx(s)

		p.rx.spawn(func() {
			s.mu.Lock()
			p.rx.endLocked(s, rx.TerminationAdministrative, restoreWait)
			s.mu.Unlock()

			p.openRx(s.key, restoreWait)
		})
	}
}

// signallingLost reports whether req is an initial request or a standalone
// transaction towards a UE whose signalling path the PCRF reported lost: the
// P-CSCF rejects it with 500 (TS 24.229 §5.2.6.4.3, §5.2.6.4.7).
func (p *PCSCF) signallingLost(req *sip.Request) bool {
	routes, err := req.Header.Routes()
	if err != nil || len(routes) == 0 || !p.cfg.Proxy.IsLocal(routes[0].URI) {
		return false
	}

	top := routes[0].URI
	if top.User == "" || !p.towardUE([]sip.URI{top}) {
		return false
	}

	return p.regs.signallingLost(top.User)
}

// signallingRestored clears the signalling lost mark of the registration a
// request from the UE arrives on: the path to the UE is back (TS 24.229
// §5.2.6.4.3 NOTE 1).
func (p *PCSCF) signallingRestored(req *sip.Request) {
	if p.regs.lost.Load() == 0 {
		return
	}

	// A REGISTER clears it on arrival (NOTE 1), on whatever flow it comes.
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

// ReAuth handles an Rx RAR (TS 29.214 §4.4.6.3). It reports whether the
// session is known.
func (p *PCSCF) ReAuth(sessionID string, r rx.ReAuthRequest) bool {
	if p.rx == nil {
		return false
	}

	s, ok := p.rx.lookup(sessionID)
	if !ok {
		return false
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

// AbortSession handles an Rx ASR (TS 29.214 §4.4.6.1): the registration is
// marked as having lost its signalling path. It reports whether the session is
// known, and returns the session's termination, which the caller runs once
// the ASA is sent: "After that the AF shall initiate an AF session
// termination procedure".
func (p *PCSCF) AbortSession(sessionID string, r rx.AbortSessionRequest) (terminate func(), known bool) {
	if p.rx == nil {
		return nil, false
	}

	s, ok := p.rx.lookup(sessionID)
	if !ok {
		return nil, false
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
