package trunkmon

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
)

const (
	defaultFailures      = 2
	defaultTimeout       = 32 * time.Second
	defaultMaxConcurrent = 16
	resolveTimeout       = 4 * time.Second
)

type Settings interface {
	Get() settings.Settings
	Changed() <-chan struct{}
}

type ResolveFunc func(ctx context.Context, u sip.URI, transports []sip.Transport) ([]sip.Server, error)

func systemResolve(ctx context.Context, u sip.URI, transports []sip.Transport) ([]sip.Server, error) {
	r := &sip.Resolver{Transports: transports}

	return r.Resolve(ctx, u)
}

type Sender interface {
	Send(ctx context.Context, req *sip.Request, to sip.Server) (*sip.Response, error)
	Transports(t settings.Trunk) []sip.Transport
}

type Timer interface {
	Stop() bool
}

type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

type Config struct {
	Settings Settings
	Resolve  ResolveFunc
	Sender   Sender
	Metrics  *Metrics
	Clock    Clock

	Failures      int
	Timeout       time.Duration
	MaxConcurrent int

	Logger *slog.Logger
}

type Monitor struct {
	cfg  Config
	log  *slog.Logger
	sem  chan struct{}
	life context.Context
	kill context.CancelFunc
	wg   sync.WaitGroup

	mu     sync.Mutex
	closed bool
	probes map[string]*probe
	states map[string]settings.TrunkStatus
}

type probe struct {
	trunk    settings.Trunk
	ctx      context.Context
	cancel   context.CancelFunc
	timer    Timer
	running  bool
	started  time.Time
	failures int
}

func Start(cfg Config) (*Monitor, error) {
	if cfg.Settings == nil || cfg.Sender == nil {
		return nil, errors.New("start the SIP trunk monitor: no settings or sender")
	}

	if cfg.Resolve == nil {
		cfg.Resolve = systemResolve
	}

	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}

	if cfg.Failures <= 0 {
		cfg.Failures = defaultFailures
	}

	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}

	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = defaultMaxConcurrent
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	life, kill := context.WithCancel(context.Background())
	m := &Monitor{
		cfg:    cfg,
		log:    logger.With(slog.String("role", "trunkmon")),
		sem:    make(chan struct{}, cfg.MaxConcurrent),
		life:   life,
		kill:   kill,
		probes: map[string]*probe{},
		states: map[string]settings.TrunkStatus{},
	}

	if cfg.Metrics != nil {
		cfg.Metrics.mon.Store(m)
	}

	m.wg.Add(1)

	go m.follow()

	return m, nil
}

func (m *Monitor) TrunkStatus(id string) settings.TrunkStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	if st, ok := m.states[id]; ok {
		return st
	}

	return settings.TrunkStatus{State: settings.TrunkUnknown}
}

func (m *Monitor) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true

	for _, p := range m.probes {
		p.stop()
	}

	m.mu.Unlock()
	m.kill()

	done := make(chan struct{})

	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Monitor) follow() {
	defer m.wg.Done()

	for {
		changed := m.cfg.Settings.Changed()

		m.apply(m.cfg.Settings.Get().Trunks)

		select {
		case <-m.life.Done():
			return
		case <-changed:
		}
	}
}

func monitored(t settings.Trunk) bool {
	return t.Enabled && t.KeepaliveInterval > 0
}

func sameTarget(a, b settings.Trunk) bool {
	return a.Domain == b.Domain && slices.Equal(a.Addresses, b.Addresses)
}

func (m *Monitor) apply(trunks []settings.Trunk) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return
	}

	seen := make(map[string]bool, len(trunks))

	for _, t := range trunks {
		if !monitored(t) {
			continue
		}

		seen[t.ID] = true
		p := m.probes[t.ID]

		switch {
		case p == nil:
			m.add(t)
		case !sameTarget(p.trunk, t):
			m.remove(t.ID)
			m.add(t)
		case p.trunk.KeepaliveInterval != t.KeepaliveInterval:
			p.trunk = t

			if !p.running {
				p.stop()
				m.schedule(p, p.started.Add(t.KeepaliveInterval).Sub(m.cfg.Clock.Now()))
			}
		default:
			p.trunk = t
		}
	}

	for id := range m.probes {
		if !seen[id] {
			m.remove(id)
		}
	}
}

func (m *Monitor) add(t settings.Trunk) {
	ctx, cancel := context.WithCancel(m.life)
	p := &probe{trunk: t, ctx: ctx, cancel: cancel}
	m.probes[t.ID] = p
	m.schedule(p, 0)
}

func (m *Monitor) remove(id string) {
	if p := m.probes[id]; p != nil {
		p.stop()
		p.cancel()
		delete(m.probes, id)
	}

	delete(m.states, id)
}

func (p *probe) stop() {
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
}

func (m *Monitor) schedule(p *probe, d time.Duration) {
	if d <= 0 {
		m.begin(p)
		return
	}

	p.timer = m.cfg.Clock.AfterFunc(d, func() { m.fire(p) })
}

func (m *Monitor) fire(p *probe) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed || m.probes[p.trunk.ID] != p || p.running {
		return
	}

	p.timer = nil
	m.begin(p)
}

func (m *Monitor) begin(p *probe) {
	p.running = true
	p.started = m.cfg.Clock.Now()
	t := p.trunk

	m.wg.Add(1)

	go m.round(p, t)
}

func (m *Monitor) round(p *probe, t settings.Trunk) {
	defer m.wg.Done()

	select {
	case m.sem <- struct{}{}:
	case <-p.ctx.Done():
		return
	}

	alive := m.probe(p.ctx, t)

	<-m.sem

	m.finish(p, alive)
}

func (m *Monitor) finish(p *probe, alive bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed || p.ctx.Err() != nil || m.probes[p.trunk.ID] != p {
		return
	}

	p.running = false
	m.record(p, alive)
	m.schedule(p, p.started.Add(p.trunk.KeepaliveInterval).Sub(m.cfg.Clock.Now()))
}

func (m *Monitor) record(p *probe, alive bool) {
	id := p.trunk.ID
	st := m.states[id].State

	if alive {
		p.failures = 0

		if st != settings.TrunkUp {
			m.transition(p.trunk, settings.TrunkUp)
		}

		return
	}

	p.failures++

	if p.failures >= m.cfg.Failures && st != settings.TrunkDown {
		m.transition(p.trunk, settings.TrunkDown)
	}
}

func (m *Monitor) transition(t settings.Trunk, to settings.TrunkState) {
	m.states[t.ID] = settings.TrunkStatus{State: to, Since: m.cfg.Clock.Now()}

	level := slog.LevelInfo
	if to == settings.TrunkDown {
		level = slog.LevelWarn
	}

	m.log.Log(m.life, level, "SIP trunk "+string(to), slog.String("trunk", t.Name), slog.String("id", t.ID))
}

// RFC 3263 section 4.3
// SIPconnect 2.0 section 17.4
func (m *Monitor) probe(ctx context.Context, t settings.Trunk) bool {
	home := m.cfg.Settings.Get().Operator.HomeDomain()

	allowed := m.cfg.Sender.Transports(t)

	for _, a := range t.Addresses {
		servers, err := m.resolve(ctx, a, allowed)
		if err != nil {
			m.log.Debug("SIP trunk address not resolved", slog.String("trunk", t.Name), slog.String("address", a.String()),
				slog.Any("error", err))

			continue
		}

		for _, srv := range servers {
			if ctx.Err() != nil {
				return false
			}

			if m.ping(ctx, t, home, srv) {
				return true
			}
		}
	}

	return false
}

// SIPconnect 2.0 section 8.1
// SIPconnect 2.0 section 17.1.1.2
func (m *Monitor) resolve(ctx context.Context, a settings.TrunkAddress, allowed []sip.Transport) ([]sip.Server, error) {
	if a.Transport != "" {
		tr := sip.Transport(strings.ToUpper(string(a.Transport)))
		if !slices.Contains(allowed, tr) {
			return nil, errNoServer
		}

		allowed = []sip.Transport{tr}
	}

	servers, err := m.lookup(ctx, addressURI(a), allowed)
	if err != nil {
		return nil, err
	}

	if servers = permitted(servers, allowed); len(servers) == 0 {
		return nil, errNoServer
	}

	return servers, nil
}

var errNoServer = errors.New("trunkmon: no server over a transport the trunk allows")

func (m *Monitor) lookup(ctx context.Context, u sip.URI, allowed []sip.Transport) ([]sip.Server, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	return m.cfg.Resolve(ctx, u, allowed)
}

func permitted(servers []sip.Server, allowed []sip.Transport) []sip.Server {
	return slices.DeleteFunc(servers, func(s sip.Server) bool { return !slices.Contains(allowed, s.Transport) })
}

// RFC 3261 section 11
// RFC 3263 section 4.3
func (m *Monitor) ping(ctx context.Context, t settings.Trunk, home string, srv sip.Server) bool {
	sctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()

	res, err := m.cfg.Sender.Send(sctx, newOptions(t, home), srv)

	switch {
	case err != nil:
		m.log.Debug("SIP trunk OPTIONS failed", slog.String("trunk", t.Name), slog.String("server", srv.Addr.String()),
			slog.Any("error", err))

		return false
	case res == nil || res.StatusCode < 200 || res.StatusCode == 503:
		return false
	}

	return true
}

// RFC 3261 section 8.1.1
// RFC 3261 section 8.1.1.3
// RFC 3261 section 8.1.1.4
// RFC 3261 section 8.1.1.5
// RFC 3261 section 8.1.1.6
// RFC 3261 section 11.1
func newOptions(t settings.Trunk, home string) *sip.Request {
	target := sip.URI{Scheme: "sip", Host: host(t.Domain)}
	req := sip.NewRequest("OPTIONS", target)

	from := sip.Address{URI: sip.URI{Scheme: "sip", Host: home}}
	from.Params.Set("tag", sip.NewTag())

	req.Header.Set("Max-Forwards", "70")
	req.Header.Set("To", sip.Address{URI: target}.String())
	req.Header.Set("From", from.String())
	req.Header.Set("Call-ID", sip.NewTag())
	req.Header.Set("CSeq", sip.CSeq{Seq: 1, Method: "OPTIONS"}.String())
	req.Header.Set("Accept", "application/sdp")

	return req
}

// RFC 3263 section 4.1
// RFC 3263 section 4.2
func addressURI(a settings.TrunkAddress) sip.URI {
	return sip.URI{Scheme: "sip", Host: host(a.Host), Port: uint16(a.Port)}
}

func host(h string) string {
	if a, err := netip.ParseAddr(h); err == nil {
		return sip.FormatHost(a)
	}

	return h
}
