// Package mobile is the MCPTT session of Ella MCPTT's Android client, for gomobile bind: an IMS UE (ims/ue)
// registered with the ISIM's AKA and the platform's IPsec, hosting the MCPTT client (internal/client), logged on in
// its registration (TS 24.379 §7.2.1), affiliated to the user's groups (§9.2.1.2), its calls and their speech.
//
// Only basic types cross to the app. The settings are JSON of schema version 1:
//
//	{"version": 1, "impi", "impu", "homeDomain": the ISIM's identities; "imei"; "pcscf": "addr:port"; "network": the
//	 platform's handle of the MCPTT network, "local": the device's address on it; "accessNetworkInfo" (TS 24.229 §7.2A.4); "mcpttId", "clientId",
//	 "participatingPsi", "groups": [...]: the user's MCPTT settings until the CMS, the PSI that of the MCPTT AS of
//	 Ella IMS of the home domain if empty (identity.ParticipatingPSI); "logLevel": debug, info, warn or error}
//
// The events are JSON, each with "version" and "type":
//
//	registration  registered with "impu", or "error"
//	deregistered  the network ended the registration: "reason" (ue.Deregistration), "retryAfter" in seconds
//	stopped       the session registers no more: "reason" stop or rejected
//	affiliation   the answer to the affiliation: "status", or "error"
//	call          of "call": "kind" incoming ("group", "caller", "manual"), established, or ended ("cause": local,
//	              remote, rejected, missed or error; "status", "error")
//	floor         a notice of the floor participant of "call" (TS 24.380 §6.2.4): "notice", "party", of a grant "duration"
//	transmit      the client starts ("on": true, "mode") or stops sending speech in "call"
//	mode          the server requests the encoder's "mode" in "call" (RFC 4867 §4.3.1)
//	heard         a talker heard for the first time in "call": "party"
//
// Calls are by an ID: the app's for a call it places (Call), the session's for an invitation. Speech crosses in
// AMR-WB frames of the storage format (RFC 4867 §5.3), a frame each 20 ms.
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

// schemaVersion is the version of the settings and events of the session.
const schemaVersion = 1

// mcpttService is the name of the MCPTT service in the UE.
const mcpttService = "mcptt"

// minBackoff is the least wait before registering again, which the tests shorten.
var minBackoff = 5 * time.Second

const (
	// registerTimeout bounds a registration; maxBackoff the longest wait between attempts, doubling from minBackoff.
	registerTimeout = 30 * time.Second
	maxBackoff      = 5 * time.Minute

	// minRetryAfter is the least wait of a Retry-After, one of 0 not a loop.
	minRetryAfter = time.Second

	// stopTimeout bounds the deregistration of Stop.
	stopTimeout = 5 * time.Second

	// The media ports of the calls, four per call (client.Config).
	minMediaPort = 30000
	maxMediaPort = 30399
)

// noData is the frame of NO_DATA in the storage format, its quality bit set (TS 26.201 Table 1c).
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

	// plain and noRegEvent are the tests', against a server in direct mode (mcptt plan, PM 14); t1, their T1 when not
	// zero, shortening Timer F.
	plain, noRegEvent bool
	t1                time.Duration
}

// callState is a call of the session, as State gives it.
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

	// Of a call the user placed: its group; retried, placed again once (retry); held, the PTT as the user
	// left it; left, the call left; for the call placed again.
	group               sip.URI
	retried, held, left bool
}

// Session is the MCPTT session of the device.
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
	// ended are the deregistrations of the UE not yet taken, ending signals one more.
	ended  []ue.Event
	ending chan struct{}

	registered bool
	calls      map[string]*callRef
	byCall     map[*client.Call]*callRef
	nextID     int
}

// NewSession makes the session of settings, in JSON, without registering it.
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

// build makes the UE and its client on the network, of the device's address local, through pcscf.
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

	// The client, made once the UE is: its service's callbacks wait for it.
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
		// A call is placed by the PTT, as a radio: its INVITE the floor request, the floor granted in the 200
		// (TS 24.380 §14.2.5, §14.2.4).
		ImplicitRequest: true,
		Granted:         true,
		OnEvent:         s.clientEvent,
		Logger:          s.log,
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

// service is the MCPTT service the UE hosts: the client's feature tags, Accept-Contact and mcptt-info (TS 24.379
// §7.2.1, §10.1.1.2.1.1), its INVITEs.
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

// Start registers the UE, logging the client on (TS 24.379 §7.2.1), and affiliates it to the groups (§9.2.1.2); it
// registers again when the network ends the registration, as the reason tells (TS 24.229 §5.1.1.7, RFC 3680 §5.1,
// RFC 5626 §4.5), unless rejected, and after failures, waiting longer each time.
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

// halt stops the registration loop, and waits for it.
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

		// TS 24.229 §5.1.2A.1.6 D, §5.1.9: its P-CSCF marked unavailable, the session discovers the P-CSCFs anew, the
		// platform's for the network, which clears the marks, and registers at once.
		if errors.Is(err, ue.ErrPCSCFUnavailable) {
			u.ClearPCSCFMarks()
			continue
		}

		if err != nil {
			s.events.emit(event{Type: "registration", Error: err.Error()})

			wait := retryDelay(failures)
			failures++

			// TS 24.229 §5.1.1.2.1: not again before a Retry-After, the count of failures then reset.
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

// retryDelay is the wait before registering again after failures consecutive failed attempts before the last (TS
// 24.229 §5.1.1.2.1 a, RFC 5626 §4.5): uniform between half and all of min(maxBackoff, minBackoff·2^failures), so that
// the clients of a network back do not register at once. The doubling compared before it is made: minBackoff·2^failures
// overflows past 30 failures.
func retryDelay(failures int) time.Duration {
	w := maxBackoff
	if minBackoff <= maxBackoff>>failures {
		w = minBackoff << failures
	}

	return w/2 + rand.N(w/2+1)
}

// retryAfter is the Retry-After of a REGISTER's final response err, in seconds (RFC 3261 §20.33), if any.
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

// registeredNow records and reports the registration, and affiliates the client.
func (s *Session) registeredNow(u *ue.UE, affiliate bool) {
	s.mu.Lock()
	s.registered = true
	s.mu.Unlock()

	s.events.emit(event{Type: "registration", Registered: true, IMPU: u.State().DefaultIMPU})

	if affiliate {
		s.affiliate()
	}
}

// registration waits for the registration to end, and returns how long to wait before registering again: after the
// network ended it, unregistered, expired, its flow failed, a retry-after of probation; the UE registered by itself
// after a deactivation. ok is false to register no more: stopped, or rejected.
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
				// TS 24.229 §5.1.1.7: the dialogs of the identities released.
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

// reregistered waits for the UE's own registration after a deactivation (TS 24.229 §5.1.1.7).
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

// affiliateThen affiliates the client to the user's groups, then calls then, if not nil, with the outcome: nil of a
// 2xx, or of no client or no groups to affiliate to.
func (s *Session) affiliateThen(then func(error)) {
	if then == nil {
		then = func(error) {}
	}

	c := s.currentClient()
	if c == nil || len(s.groups) == 0 {
		then(nil)
		return
	}

	err := c.Affiliate(s.groups, func(res *sip.Response, err error) {
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

// reaffiliateDelay is the wait before an affiliation of a re-registration refused is published again.
const reaffiliateDelay = 2 * time.Second

// reregistered2xx reports whether res is a 2xx to a re-registration of the UE, of a contact registered: once
// registered, a REGISTER of its own refreshing it (TS 24.229 §5.1.1.4).
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

// ueEvent takes the events of the UE: how the network ended the registration, queued in order.
func (s *Session) ueEvent(e ue.Event) {
	// A re-registration affiliates the client again: its affiliations refreshed at a server that kept them, published
	// anew at one that lost them, restarted (TS 24.379 §9.2.1.2), as their publication, of 2^32-1 seconds, is not.
	// The third-party REGISTER that binds the client again follows the 2xx (TS 24.229 §5.4.1.7): an affiliation that
	// came first, refused, is published again once, after reaffiliateDelay.
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

// drainEnded forgets the deregistrations of a registration before a new one.
func (s *Session) drainEnded() {
	s.takeEnded()

	select {
	case <-s.ending:
	default:
	}
}

// Stop leaves the calls, deregisters the UE, logging the client off, and closes the session, its last event
// "stopped". It may take seconds: the app calls it off its main thread, and may call it from Event.
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

// SetNetwork tells the session of the network now: the platform's handle of the MCPTT network, the device's address
// on it, the P-CSCF, the access network info (TS 24.229 §7.2A.4). Another cell changes the access network info of the
// next requests; another network, address or P-CSCF makes a new registration, the calls left: a network lost and
// back, of the same address, has sockets of its own.
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

	// The old network gone, nothing signalled on it.
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

// Woken is the alarm id of Platform.Wake gone off.
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

// Call places a prearranged group call to group (TS 24.379 §10.1.1.2.1.1), of the app's ID id, unique among the
// session's calls: the ID of its events.
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

	// The lock held, the call's events wait for its ID (the agent never waits for them).
	c, err := s.client.Call(g)
	if err != nil {
		return err
	}

	r := &callRef{id: id, call: c, state: callState{ID: id, State: "calling", Group: group}, group: g, held: true}
	s.calls[id], s.byCall[c] = r, r

	return nil
}

// retry places the call of r again, once, when its INVITE was refused as of a user the participating function does
// not know (TS 24.379 §10.1.1.3.1.1 step 2a, warning 141), after registering and affiliating again; or as of a user
// not affiliated to the group (§10.1.1.4.2, warning 120), after affiliating again: of a server that lost the
// registration's binding or the affiliations, or of a registration that expired as the user pressed. A
// re-registration's third-party REGISTER binds the user again (TS 24.229 §5.4.1.7). Its own failure is the call's.
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
			if c, err = s.client.Call(r.group); err == nil {
				r.call, s.byCall[c] = c, r
				// Released, or left, while registering: as the client takes a release before the call is up.
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

// warned reports whether res has the Warning of text (TS 24.379 §4.4).
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

// callOf is the call of the session of c, an invitation given an ID "in-n" the first time.
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

// leaveCalls leaves every call of the session.
func (s *Session) leaveCalls() {
	s.mu.Lock()
	calls := make([]*client.Call, 0, len(s.calls))

	for _, r := range s.calls {
		calls = append(calls, r.call)
	}
	s.mu.Unlock()

	for _, c := range calls {
		c.Leave()
	}
}

// PressPTT and ReleasePTT press and release the PTT of the call (TS 24.380 §6.2.4); Accept and Decline answer an
// invitation of manual commencement (TS 24.379 §6.2.3.2); Leave leaves the call (§10.1.1.2.3). Holding or muting a
// call is the app's: it refuses the PTT, and stops taking NextSpeech.
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
	return s.do(id, (*client.Call).Leave)
}

// mark records what the user did of the call id, for a call placed again (retry).
func (s *Session) mark(id string, f func(r *callRef)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r, ok := s.calls[id]; ok {
		f(r)
	}
}

// SendSpeech sends a frame of the encoder in the call while it transmits (client.Call.SendSpeech); NextSpeech is the
// frame to decode for the next 20 ms (client.Call.NextSpeech), NO_DATA of a call gone.
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

// State is the state of the session, in JSON: {"version", "registered", "calls": [{"id", "state" (calling,
// incoming, established), "group", "caller", "floor" (its last notice), "talker", "transmitting"}]}, for the app to
// rebuild its own after a restart.
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

// logHandler gives the platform each line of the log with its level, from a minimum.
type logHandler struct {
	p Platform
	// min is the handler's least level, shared with the handlers of its attributes, changed at run time.
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

// setLevel sets v to level, a level of log/slog in text ("debug", "info", "warn", "error"), info if it is none.
func setLevel(v *slog.LevelVar, level string) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil || level == "" {
		l = slog.LevelInfo
	}

	v.Set(l)
}

// SetLogLevel changes the level of the session's log at once: "debug", "info", "warn" or "error".
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
