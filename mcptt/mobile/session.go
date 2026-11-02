package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/ue"
)

const schemaVersion = 1

const mcpttService = "mcptt"

var minBackoff = 5 * time.Second

const (
	registerTimeout = 30 * time.Second
	maxBackoff      = 5 * time.Minute

	minRetryAfter = time.Second

	stopTimeout = 5 * time.Second

	minMediaPort = 30000
	maxMediaPort = 30399
)

// TS 26.201 clause 4.1.3
var noData = []byte{0x7c}

type settings struct {
	Version           int      `json:"version"`
	IMPI              string   `json:"impi"`
	IMPU              string   `json:"impu"`
	HomeDomain        string   `json:"homeDomain"`
	IMEI              string   `json:"imei"`
	PCSCF             string   `json:"pcscf"`
	Network           string   `json:"network"`
	Local             string   `json:"local"`
	AccessNetworkInfo string   `json:"accessNetworkInfo"`
	MCPTTID           string   `json:"mcpttId"`
	ClientID          string   `json:"clientId"`
	ParticipatingPSI  string   `json:"participatingPsi"`
	Groups            []string `json:"groups"`
	LogLevel          string   `json:"logLevel"`

	plain, noRegEvent bool
	t1                time.Duration
}

type callState struct {
	ID           string `json:"id"`
	State        string `json:"state"`
	Group        string `json:"group,omitempty"`
	Caller       string `json:"caller,omitempty"`
	Floor        string `json:"floor,omitempty"`
	Talker       string `json:"talker,omitempty"`
	Transmitting bool   `json:"transmitting"`
}

type callRef struct {
	id    string
	call  *client.Call
	state callState

	group               sip.URI
	retried, held, left bool
}

type Session struct {
	p      Platform
	set    settings
	log    *slog.Logger
	level  *slog.LevelVar
	clock  *clock
	events *events

	impu, mcpttID, psi sip.URI
	groups             []sip.URI

	mu      sync.Mutex
	running bool
	stopped bool
	stop    chan struct{}
	done    chan struct{}
	ue      *ue.UE
	client  *client.Client
	ended   []ue.Event
	ending  chan struct{}

	registered bool
	calls      map[string]*callRef
	byCall     map[*client.Call]*callRef
	nextID     int
}

func NewSession(p Platform, settingsJSON string) (*Session, error) {
	var set settings
	if err := json.Unmarshal([]byte(settingsJSON), &set); err != nil {
		return nil, fmt.Errorf("mobile: settings: %w", err)
	}

	return newSession(p, set)
}

func newSession(p Platform, set settings) (*Session, error) {
	if set.Version != schemaVersion {
		return nil, fmt.Errorf("mobile: settings of version %d, want %d", set.Version, schemaVersion)
	}

	s := &Session{
		p: p, set: set, clock: newClock(p), events: newEvents(p), ending: make(chan struct{}, 1),
		calls: map[string]*callRef{}, byCall: map[*client.Call]*callRef{},
	}

	h := newLogHandler(p, set.LogLevel)
	s.log, s.level = slog.New(h), h.min

	var err error

	if s.impu, err = sip.ParseURI(set.IMPU); err != nil {
		return nil, fmt.Errorf("mobile: IMPU: %w", err)
	}

	if s.mcpttID, err = sip.ParseURI(set.MCPTTID); err != nil {
		return nil, fmt.Errorf("mobile: MCPTT ID: %w", err)
	}

	psi := set.ParticipatingPSI
	if psi == "" {
		psi = identity.ParticipatingPSI(set.HomeDomain)
	}

	if s.psi, err = sip.ParseURI(psi); err != nil {
		return nil, fmt.Errorf("mobile: participating PSI: %w", err)
	}

	for _, g := range set.Groups {
		u, err := sip.ParseURI(g)
		if err != nil {
			return nil, fmt.Errorf("mobile: group %s: %w", g, err)
		}

		s.groups = append(s.groups, u)
	}

	if err := s.build(set.Network, set.Local, set.PCSCF, set.AccessNetworkInfo); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Session) build(network, local, pcscf, ani string) error {
	l, err := netip.ParseAddr(local)
	if err != nil {
		return fmt.Errorf("mobile: local address: %w", err)
	}

	pc, err := netip.ParseAddrPort(pcscf)
	if err != nil {
		return fmt.Errorf("mobile: P-CSCF: %w", err)
	}

	instance, err := ue.IMEIInstanceID(s.set.IMEI)
	if err != nil {
		return err
	}

	var current atomic.Pointer[client.Client]

	u, err := ue.New(ue.Config{
		Identities:        ue.Identities{IMPI: s.set.IMPI, IMPU: s.set.IMPU, HomeDomain: s.set.HomeDomain},
		InstanceID:        instance,
		AKA:               uicc{s.p},
		PCSCF:             pc,
		Local:             l,
		RegID:             1,
		Plain:             s.set.plain,
		AccessNetworkInfo: ani,
		NoRegEvent:        s.set.noRegEvent,
		T1:                s.set.t1,
		Services:          []ue.Service{s.service(current.Load)},
		IPsec:             ipsec{s.p},
		Control:           bindControl(s.p),
		Clock:             s.clock,
		OnEvent:           s.ueEvent,
		Logger:            s.log,
	})
	if err != nil {
		return err
	}

	c, err := client.New(client.Config{
		Agent:     u.Agent(),
		Originate: func(req *sip.Request) error { return u.Originate(req, mcpttService) },
		IMPU:      s.impu,
		MCPTTID:   s.mcpttID,
		ClientID:  s.set.ClientID,
		Contact: func() sip.Address {
			a, _ := u.Contact(mcpttService)
			return a
		},
		ParticipatingPSI: s.psi,
		Media:            media.UDP{Control: bindControl(s.p)},
		Addr:             l,
		MinPort:          minMediaPort,
		MaxPort:          maxMediaPort,
		Floor:            floor.ParticipantTimers{T100: time.Second, T101: time.Second},
		ImplicitRequest:  true,
		Granted:          true,
		OnEvent:          s.clientEvent,
		Logger:           s.log,
	})
	if err != nil {
		_ = u.Close()
		return err
	}

	current.Store(c)

	s.mu.Lock()
	s.ue, s.client = u, c
	s.set.Network, s.set.Local, s.set.PCSCF, s.set.AccessNetworkInfo = network, local, pcscf, ani
	s.mu.Unlock()

	return nil
}

// TS 24.379 clause 7.2.1
// TS 24.379 clause 10.1.1.2.1.1
func (s *Session) service(c func() *client.Client) ue.Service {
	var features sip.FeatureSet

	icsiRef := sip.NewFeature(identity.ICSITag).Tag

	for _, f := range client.Features() {
		if f.Tag != icsiRef {
			features = append(features, f)
		}
	}

	var accept sip.Header
	for _, v := range client.AcceptContact() {
		accept.Add("Accept-Contact", v)
	}

	prefs, _ := accept.AcceptContact()

	return ue.Service{
		Name:          mcpttService,
		ICSI:          identity.ICSI,
		Features:      features,
		AcceptContact: prefs,
		Allow:         []string{"INVITE", "UPDATE", "PRACK"},
		RegisterBodies: func() []sip.BodyPart {
			if cl := c(); cl != nil {
				return cl.RegisterBodies()
			}

			return nil
		},
		OnInvite: func(r *ua.Request) ua.Handler {
			if cl := c(); cl != nil {
				return cl.OnInvite(r)
			}

			_ = r.Reject(480)

			return ua.Handler{}
		},
	}
}

var (
	errStarted = errors.New("mobile: session started already")
	errStopped = errors.New("mobile: session stopped")
)

// TS 24.379 clause 7.2.1
// TS 24.379 clause 9.2.1.2
// TS 24.229 clause 5.1.1.7
// RFC 3680 section 5.1
// RFC 5626 section 4.5
func (s *Session) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case s.stopped:
		return errStopped
	case s.running:
		return errStarted
	}

	s.startLocked()

	return nil
}

func (s *Session) startLocked() {
	s.running, s.stop, s.done = true, make(chan struct{}), make(chan struct{})

	go s.run(s.ue, s.stop, s.done)
}

func (s *Session) halt() {
	s.mu.Lock()

	if !s.running {
		s.mu.Unlock()
		return
	}

	s.running = false
	stop, done := s.stop, s.done
	s.mu.Unlock()

	close(stop)
	<-done
}

func (s *Session) run(u *ue.UE, stop, done chan struct{}) {
	defer close(done)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-stop
		cancel()
	}()

	failures := 0

	for {
		s.drainEnded()

		rctx, rcancel := context.WithTimeout(ctx, registerTimeout)
		err := u.Register(rctx)

		rcancel()

		if ctx.Err() != nil {
			return
		}

		if errors.Is(err, ue.ErrPCSCFUnavailable) {
			u.ClearPCSCFMarks()
			continue
		}

		if err != nil {
			s.events.emit(event{Type: "registration", Error: err.Error()})

			wait := retryDelay(failures)
			failures++

			if ra, ok := retryAfter(err); ok {
				wait, failures = max(ra, minRetryAfter), 0
			}

			if !s.clock.sleep(wait, stop) {
				return
			}

			continue
		}

		failures = 0

		s.registeredNow(u, true)

		wait, ok := s.registration(u, stop)
		if !ok || !s.clock.sleep(wait, stop) {
			return
		}
	}
}

// TS 24.229 clause 5.1.1.2.1
// RFC 5626 section 4.5
func retryDelay(failures int) time.Duration {
	w := maxBackoff
	if minBackoff <= maxBackoff>>failures {
		w = minBackoff << failures
	}

	return w/2 + rand.N(w/2+1)
}

// RFC 3261 section 20.33
func retryAfter(err error) (time.Duration, bool) {
	var re *ue.ResponseError
	if !errors.As(err, &re) {
		return 0, false
	}

	for _, v := range re.Response.Header.Values("Retry-After") {
		digits, _, _ := strings.Cut(strings.TrimSpace(v), " ")
		digits, _, _ = strings.Cut(digits, ";")
		digits, _, _ = strings.Cut(digits, "(")

		if n, err := strconv.ParseUint(digits, 10, 32); err == nil {
			return time.Duration(n) * time.Second, true
		}
	}

	return 0, false
}

func (s *Session) registeredNow(u *ue.UE, affiliate bool) {
	s.mu.Lock()
	s.registered = true
	s.mu.Unlock()

	s.events.emit(event{Type: "registration", Registered: true, IMPU: u.State().DefaultIMPU})

	if affiliate {
		s.affiliate()
	}
}

func (s *Session) registration(u *ue.UE, stop chan struct{}) (time.Duration, bool) {
	for {
		select {
		case <-stop:
			return 0, false
		case <-s.ending:
		}

		for _, e := range s.takeEnded() {
			s.mu.Lock()
			s.registered = false
			s.mu.Unlock()

			s.events.emit(event{Type: "deregistered", Reason: string(e.Deregistered), RetryAfter: int64(e.RetryAfter / time.Second)})

			switch e.Deregistered {
			case ue.DeregisteredRejected:
				s.leaveCalls()
				s.events.emit(event{Type: "stopped", Reason: "rejected"})

				return 0, false
			case ue.DeregisteredDeactivated:
				if s.reregistered(u, stop) {
					s.registeredNow(u, true)
					continue
				}

				return 0, true
			case ue.DeregisteredProbation:
				return max(e.RetryAfter, minBackoff), true
			default:
				return minBackoff, true
			}
		}
	}
}

// TS 24.229 clause 5.1.1.7
func (s *Session) reregistered(u *ue.UE, stop chan struct{}) bool {
	deadline := s.clock.Now().Add(registerTimeout)

	for s.clock.Now().Before(deadline) {
		if u.State().Registered {
			return true
		}

		if !s.clock.sleep(500*time.Millisecond, stop) {
			return false
		}
	}

	return false
}

func (s *Session) affiliate() { s.affiliateThen(nil) }

func (s *Session) affiliateThen(then func(error)) {
	if then == nil {
		then = func(error) {}
	}

	c := s.currentClient()
	if c == nil || len(s.groups) == 0 {
		then(nil)
		return
	}

	err := c.Affiliate(context.Background(), s.groups, func(res *sip.Response, err error) {
		e := event{Type: "affiliation"}
		if res != nil {
			e.Status = res.StatusCode
		}

		if err == nil && res != nil && res.StatusCode/100 != 2 {
			err = fmt.Errorf("affiliation: %d", res.StatusCode)
		}

		if err != nil {
			e.Error = err.Error()
		}

		s.events.emit(e)
		then(err)
	})
	if err != nil {
		s.events.emit(event{Type: "affiliation", Error: err.Error()})
		then(err)
	}
}

const reaffiliateDelay = 2 * time.Second

// TS 24.229 clause 5.1.1.4
func (s *Session) reregistered2xx(res *sip.Response) bool {
	if cseq, err := res.Header.CSeq(); err != nil || cseq.Method != "REGISTER" || res.StatusCode/100 != 2 {
		return false
	}

	s.mu.Lock()
	registered := s.registered && !s.stopped
	s.mu.Unlock()

	contacts, _ := res.Header.Contacts()

	return registered && slices.ContainsFunc(contacts, func(c sip.Address) bool {
		e, ok := c.Params.Get("expires")
		return !ok || e != "0"
	})
}

func (s *Session) ueEvent(e ue.Event) {
	if e.Response != nil && s.reregistered2xx(e.Response) {
		go s.affiliateThen(func(err error) {
			if err != nil {
				s.clock.AfterFunc(reaffiliateDelay, s.affiliate)
			}
		})
	}

	if e.Deregistered == "" {
		return
	}

	s.mu.Lock()
	s.ended = append(s.ended, e)
	s.mu.Unlock()

	select {
	case s.ending <- struct{}{}:
	default:
	}
}

func (s *Session) takeEnded() []ue.Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := s.ended
	s.ended = nil

	return out
}

func (s *Session) drainEnded() {
	s.takeEnded()

	select {
	case <-s.ending:
	default:
	}
}

func (s *Session) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}

	s.stopped = true
	s.mu.Unlock()

	s.halt()
	s.leaveCalls()

	u := s.currentUE()

	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()

	if u.State().Registered {
		_ = u.Deregister(ctx)
	}

	_ = u.Shutdown(ctx)

	s.clock.close()
	s.events.emit(event{Type: "stopped", Reason: "stop"})
	s.events.close()
}

func (s *Session) SetNetwork(network, local, pcscf, accessNetworkInfo string) error {
	s.mu.Lock()
	same := network == s.set.Network && local == s.set.Local && pcscf == s.set.PCSCF
	stopped, running := s.stopped, s.running
	u := s.ue
	s.mu.Unlock()

	switch {
	case stopped:
		return errStopped
	case same:
		u.SetAccessNetworkInfo(accessNetworkInfo)

		s.mu.Lock()
		s.set.AccessNetworkInfo = accessNetworkInfo
		s.mu.Unlock()

		return nil
	}

	s.halt()
	s.leaveCalls()

	_ = u.Close()

	if err := s.build(network, local, pcscf, accessNetworkInfo); err != nil {
		return err
	}

	if running {
		s.mu.Lock()
		s.startLocked()
		s.mu.Unlock()
	}

	return nil
}

func (s *Session) Woken(id int64) { s.clock.woken(id) }

func (s *Session) currentUE() *ue.UE {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.ue
}

func (s *Session) currentClient() *client.Client {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.client
}

var (
	errNoCall = errors.New("mobile: no such call")
	errCallID = errors.New("mobile: call ID empty or in use")
)

// TS 24.379 clause 10.1.1.2.1.1
func (s *Session) Call(id, group string) error {
	g, err := sip.ParseURI(group)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, used := s.calls[id]; id == "" || used || strings.HasPrefix(id, "in-") {
		return errCallID
	}

	c, err := s.client.Call(context.Background(), g)
	if err != nil {
		return err
	}

	r := &callRef{id: id, call: c, state: callState{ID: id, State: "calling", Group: group}, group: g, held: true}
	s.calls[id], s.byCall[c] = r, r

	return nil
}

// TS 24.379 clause 10.1.1.3.1.1
// TS 24.379 clause 10.1.1.4.2
func (s *Session) retry(r *callRef, res *sip.Response) bool {
	unknown := res != nil && res.StatusCode == 404 && warned(res, identity.WarnUserUnknown)
	if !unknown && (res == nil || res.StatusCode != 403 || !warned(res, identity.WarnNotAffiliated) ||
		!slices.ContainsFunc(s.groups, r.group.Equivalent)) {
		return false
	}

	s.mu.Lock()
	if r.group.String() == "" || r.retried || r.left || s.stopped {
		s.mu.Unlock()
		return false
	}

	r.retried = true
	delete(s.byCall, r.call)
	u := s.ue
	s.mu.Unlock()

	if unknown {
		s.log.Info("participating function does not know the user: registering again", slog.String("call", r.id))
	} else {
		s.log.Info("not affiliated to the group: affiliating again", slog.String("call", r.id))
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), registerTimeout)
		defer cancel()

		var err error
		if unknown {
			err = u.Reregister(ctx)
		}

		if err == nil {
			affiliated := make(chan error, 1)

			s.affiliateThen(func(err error) { affiliated <- err })

			select {
			case err = <-affiliated:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}

		s.mu.Lock()
		defer s.mu.Unlock()

		if err == nil && !r.left && !s.stopped {
			var c *client.Call
			if c, err = s.client.Call(context.Background(), r.group); err == nil {
				r.call, s.byCall[c] = c, r
				if !r.held {
					c.ReleasePTT()
				}

				return
			}
		}

		delete(s.calls, r.id)

		out := event{Type: "call", Kind: "ended", Call: r.id, Cause: "rejected", Status: res.StatusCode}
		if r.left {
			out.Cause, out.Status = "local", 0
		} else if err != nil {
			out.Error = err.Error()
		}

		s.events.emit(out)
	}()

	return true
}

// TS 24.379 clause 4.4
func warned(res *sip.Response, text string) bool {
	ws, err := res.Header.Warnings()
	if err != nil {
		return false
	}

	for _, w := range ws {
		if w.Code == 399 && w.Text == text {
			return true
		}
	}

	return false
}

func (s *Session) callOf(c *client.Call) *callRef {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r, ok := s.byCall[c]; ok {
		return r
	}

	s.nextID++
	id := "in-" + strconv.Itoa(s.nextID)
	r := &callRef{id: id, call: c, state: callState{ID: id}}
	s.calls[id], s.byCall[c] = r, r

	return r
}

func (s *Session) update(r *callRef, f func(st *callState)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f(&r.state)
}

func (s *Session) forget(c *client.Call) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r, ok := s.byCall[c]; ok {
		delete(s.calls, r.id)
		delete(s.byCall, c)
	}
}

func (s *Session) call(id string) (*client.Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.calls[id]
	if !ok {
		return nil, errNoCall
	}

	return r.call, nil
}

func (s *Session) do(id string, f func(c *client.Call)) error {
	c, err := s.call(id)
	if err != nil {
		return err
	}

	f(c)

	return nil
}

func (s *Session) leaveCalls() {
	s.mu.Lock()
	calls := make([]*client.Call, 0, len(s.calls))

	for _, r := range s.calls {
		calls = append(calls, r.call)
	}
	s.mu.Unlock()

	for _, c := range calls {
		c.Leave(context.Background())
	}
}

// TS 24.380 clause 6.2.4
func (s *Session) PressPTT(id string) error {
	s.mark(id, func(r *callRef) { r.held = true })
	return s.do(id, (*client.Call).PressPTT)
}

func (s *Session) ReleasePTT(id string) error {
	s.mark(id, func(r *callRef) { r.held = false })
	return s.do(id, (*client.Call).ReleasePTT)
}

func (s *Session) Accept(id string) error  { return s.do(id, (*client.Call).Accept) }
func (s *Session) Decline(id string) error { return s.do(id, (*client.Call).Decline) }

func (s *Session) Leave(id string) error {
	s.mark(id, func(r *callRef) { r.left = true })
	return s.do(id, func(c *client.Call) { c.Leave(context.Background()) })
}

func (s *Session) mark(id string, f func(r *callRef)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r, ok := s.calls[id]; ok {
		f(r)
	}
}

func (s *Session) SendSpeech(id string, frame []byte) error {
	c, err := s.call(id)
	if err != nil {
		return err
	}

	return c.SendSpeech(frame)
}

func (s *Session) NextSpeech(id string) []byte {
	c, err := s.call(id)
	if err != nil {
		return bytes.Clone(noData)
	}

	return c.NextSpeech()
}

func (s *Session) State() string {
	s.mu.Lock()

	out := struct {
		Version    int         `json:"version"`
		Registered bool        `json:"registered"`
		Calls      []callState `json:"calls"`
	}{Version: schemaVersion, Registered: s.registered, Calls: []callState{}}

	for _, r := range s.calls {
		out.Calls = append(out.Calls, r.state)
	}
	s.mu.Unlock()

	b, _ := json.Marshal(out)

	return string(b)
}

type logHandler struct {
	p     Platform
	min   *slog.LevelVar
	mu    *sync.Mutex
	buf   *bytes.Buffer
	inner slog.Handler
}

func newLogHandler(p Platform, level string) *logHandler {
	lowest := &slog.LevelVar{}
	setLevel(lowest, level)

	buf := &bytes.Buffer{}

	return &logHandler{p: p, min: lowest, mu: &sync.Mutex{}, buf: buf, inner: slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})}
}

func (h *logHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.min.Level() }

func setLevel(v *slog.LevelVar, level string) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil || level == "" {
		l = slog.LevelInfo
	}

	v.Set(l)
}

func (s *Session) SetLogLevel(level string) { setLevel(s.level, level) }

func (h *logHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.buf.Reset()

	if err := h.inner.Handle(ctx, r); err != nil {
		return err
	}

	h.p.Log(int(r.Level), strings.TrimSuffix(h.buf.String(), "\n"))

	return nil
}

func (h *logHandler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.inner = h.inner.WithAttrs(as)

	return &c
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	c := *h
	c.inner = h.inner.WithGroup(name)

	return &c
}
