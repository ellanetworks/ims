package ue

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

type procedure struct {
	start       sip.Flow
	startVerify []string

	flow   sip.Flow
	verify []string
	auth   string
	client client
	origin *saSet
	dereg  bool
}

func (u *UE) Register(ctx context.Context) error {
	u.op.Lock()
	defer u.op.Unlock()

	// TS 24.229 §5.1.9: no initial registration through a P-CSCF marked unavailable.
	u.mu.Lock()
	pcscf, marked := u.pcscf, u.markedLocked(u.pcscf)
	u.mu.Unlock()

	if marked {
		return fmt.Errorf("%w: %s", ErrPCSCFUnavailable, pcscf)
	}

	if err := u.ready(); err != nil {
		return err
	}

	for restart := 0; ; restart++ {
		u.newDialog()

		p := &procedure{start: u.unprotectedFlow(), auth: emptyAuthorization(u.id.IMPI, u.id.HomeDomain)}

		err := u.run(ctx, p)
		if errors.Is(err, ErrNoSecurityServer) && restart == 0 {
			continue
		}

		if err == nil && !u.cfg.NoRegEvent {
			if err := u.subscribe(ctx); err != nil {
				u.event(Event{Err: err})
			}
		}

		return err
	}
}

func (u *UE) Reregister(ctx context.Context) error {
	u.op.Lock()
	err := u.refresh(ctx, false)
	u.op.Unlock()

	// TS 24.229 §5.1.1.4.1: a reregistration that timer F ended, or a fatal transport error (NOTE 13: by other means
	// than timer F), is followed by an initial registration: the owner registers again, on a new flow. Out of the
	// operation, which the handlers that Drop waits for may take.
	if errors.Is(err, transaction.ErrTimeout) || errors.Is(err, transaction.ErrTransport) {
		u.flowFailed(err)
	}

	return err
}

func (u *UE) Deregister(ctx context.Context) error {
	u.op.Lock()
	defer u.op.Unlock()

	return u.refresh(ctx, true)
}

func (u *UE) ready() error {
	u.mu.Lock()
	closed := u.closed
	u.mu.Unlock()

	if closed {
		return ErrClosed
	}

	if err := u.reopen(); err != nil {
		return err
	}

	if u.cfg.Plain || u.portS != 0 {
		return nil
	}

	bound, err := u.listen(0)
	if err != nil {
		return err
	}

	u.portS = bound.Port()

	return nil
}

func (u *UE) refresh(ctx context.Context, dereg bool) error {
	if err := u.ready(); err != nil {
		return err
	}

	u.mu.Lock()
	registered, auth := u.state.Registered, u.auth
	u.mu.Unlock()

	if !registered {
		return ErrNotRegistered
	}

	p := &procedure{start: u.unprotectedFlow(), auth: auth, dereg: dereg}

	if !u.cfg.Plain {
		est := u.established()
		if est == nil {
			return fmt.Errorf("%w: no established SAs", ErrNotRegistered)
		}

		p.start, p.startVerify, p.origin = protectedFlow(est, u.cfg.Transport), est.server, est
	}

	return u.run(ctx, p)
}

func (u *UE) newDialog() {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.callID = timeUUID() + "@" + sip.FormatHost(u.cfg.Local)
	u.fromTag = sip.NewTag()
	u.cseq = 0
}

func (u *UE) unprotectedFlow() sip.Flow {
	return sip.Flow{Transport: u.cfg.Transport, Local: u.unprotected, Remote: u.pcscfAddr()}
}

func (u *UE) port(protected bool) uint16 {
	if protected {
		return u.portS
	}

	return u.unprotected.Port()
}

// contact is the Contact the UE registers (TS 24.229 §5.1.1.2.1): its instance, the feature tags of its enabled
// services, its reg-id (RFC 5626 §4.2), the ContactParams.
func (u *UE) contact(port uint16) string {
	uri := sip.URI{Scheme: "sip", User: u.user, Host: sip.FormatHost(u.cfg.Local), Port: port}

	params := sip.Params{{Name: "+sip.instance", Value: sip.Quote("<" + u.cfg.InstanceID + ">")}}

	// New checked the feature tags encode.
	tags, _ := featureSet(u.enabled()).Params()
	params = append(params, tags...)

	if u.cfg.RegID != 0 {
		params = append(params, sip.Param{Name: "reg-id", Value: strconv.FormatInt(u.cfg.RegID, 10)})
	}

	return sip.Address{URI: uri, Params: append(params, u.cfg.ContactParams...)}.String()
}

// RegisterContact is the Contact of the UE's REGISTERs now (TS 24.229 §5.1.1.2.1), at its protected server port, or at
// its unprotected port before its first registration or with Plain.
func (u *UE) RegisterContact() (sip.Address, error) {
	return sip.ParseAddress(u.contact(u.port(!u.cfg.Plain && u.portS != 0)))
}

func (u *UE) newClientFor(p *procedure) error {
	if u.cfg.Plain {
		return nil
	}

	last := p.client.portC
	u.releaseClient(p)

	c, err := u.newClient(last)
	if err != nil {
		return err
	}

	p.client = c

	return nil
}

func (u *UE) releaseClient(p *procedure) {
	if p.client != (client{}) {
		u.cfg.IPsec.ReleaseSPI(p.client.spiC)
		u.cfg.IPsec.ReleaseSPI(p.client.spiS)
		p.client = client{}
	}
}

func (u *UE) newRegister(p *procedure, protected bool) *sip.Request {
	u.mu.Lock()
	u.cseq++
	cseq, callID, fromTag, expires := u.cseq, u.callID, u.fromTag, u.expires
	u.mu.Unlock()

	if p.dereg {
		expires = 0
	}

	req := sip.NewRequest("REGISTER", sip.URI{Scheme: "sip", Host: u.id.HomeDomain})
	req.Flow = p.flow

	port := u.port(protected)

	via := sip.NewVia(p.flow.Transport, netip.AddrPortFrom(u.cfg.Local, port))
	via.Params.Set("rport", "")

	req.Header.Add("Via", via.String())
	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("From", "<"+u.id.IMPU+">;tag="+fromTag)
	req.Header.Add("To", "<"+u.id.IMPU+">")
	req.Header.Add("Call-ID", callID)
	req.Header.Add("CSeq", strconv.FormatUint(uint64(cseq), 10)+" REGISTER")
	req.Header.Add("Contact", u.contact(port))
	req.Header.Add("Expires", strconv.FormatInt(int64(expires/time.Second), 10))
	req.Header.Add("Supported", "path")

	if u.cfg.RegID != 0 {
		req.Header.Add("Supported", "outbound")
	}

	req.Header.Add("Authorization", p.auth)

	// The bodies the enabled services carry in a REGISTER (TS 24.379 §7.2.1), none in one that deregisters.
	if !p.dereg {
		var bodies []sip.BodyPart

		for _, s := range u.enabled() {
			if s.RegisterBodies != nil {
				bodies = append(bodies, s.RegisterBodies()...)
			}
		}

		if len(bodies) > 0 {
			req.SetBodies(bodies...)
		}
	}

	if !u.cfg.Plain {
		for _, v := range u.securityClient(p.client) {
			req.Header.Add("Security-Client", v)
		}

		for _, v := range p.verify {
			req.Header.Add("Security-Verify", v)
		}

		req.Header.Add("Require", "sec-agree")
		req.Header.Add("Proxy-Require", "sec-agree")

		if protected {
			req.Header.Add("P-Access-Network-Info", u.accessNetworkInfo())
		}
	}

	return req
}

func (u *UE) run(ctx context.Context, p *procedure) error {
	p.flow, p.verify = p.start, p.startVerify

	if err := u.newClientFor(p); err != nil {
		return err
	}

	var (
		temp       *saSet
		invalid    int
		challenges int
		macFailure bool
	)

	fail := func(err error) error {
		u.drop(temp)
		u.releaseClient(p)

		return err
	}

	for {
		res, err := u.request(ctx, u.newRegister(p, temp != nil || p.origin != nil))
		if err != nil {
			return fail(err)
		}

		switch code := res.StatusCode; {
		case code == 423:
			if err := u.adoptMinExpires(res); err != nil {
				return fail(err)
			}
		case code == 401:
			if challenges++; challenges > maxChallenges {
				return fail(ErrTooManyChallenges)
			}

			if temp != nil {
				temp.keepFlows = true
				u.drop(temp)
				temp, p.flow, p.verify = nil, p.start, p.startVerify
			}

			ch, err := parseChallenge(res)
			if err != nil {
				return fail(err)
			}

			// The UICC checks the challenge (TS 33.203 §6.1.1); one with an invalid MAC or SQN is answered with new
			// spi_uc, spi_us and port_uc, without temporary SAs (TS 24.229 §5.1.1.5.3).
			r, err := u.cfg.AKA.Authenticate(ch.rand, ch.autn)

			switch {
			case errors.Is(err, ErrMACFailure):
				if invalid++; invalid > maxInvalidChallenges {
					return fail(ErrNetworkAuthentication)
				}

				macFailure = true
				p.auth = answer(u.id.IMPI, u.id.HomeDomain, ch, nil, nil, true)
			case err != nil:
				return fail(fmt.Errorf("ue: AKA: %w", err))
			case r.AUTS != nil:
				if invalid++; invalid > maxInvalidChallenges {
					return fail(ErrSynchronisation)
				}

				p.auth = answer(u.id.IMPI, u.id.HomeDomain, ch, nil, r.AUTS, false)
			}

			if err != nil || r.AUTS != nil {
				if err := u.newClientFor(p); err != nil {
					return fail(err)
				}

				continue
			}

			var (
				offer  ipsec.Offer
				server []string
			)

			if !u.cfg.Plain {
				if offer, server, err = u.selectServer(res); err != nil {
					return fail(err)
				}
			}

			invalid, macFailure = 0, false

			p.auth = answer(u.id.IMPI, u.id.HomeDomain, ch, r.RES, nil, false)

			if u.cfg.Plain {
				continue
			}

			if temp, err = u.install(p.client, offer, Keys{CK: r.CK, IK: r.IK}, server); err != nil {
				return fail(err)
			}

			p.flow, p.verify = protectedFlow(temp, u.cfg.Transport), server
		case code >= 200 && code < 300:
			u.succeeded(p, temp, res)
			return nil
		default:
			err := error(&ResponseError{Response: res})
			if macFailure {
				err = fmt.Errorf("%w: %w", ErrNetworkAuthentication, err)
			}

			return fail(err)
		}
	}
}

func (u *UE) adoptMinExpires(res *sip.Response) error {
	v, err := strconv.ParseUint(res.Header.Get("Min-Expires"), 10, 32)

	u.mu.Lock()
	defer u.mu.Unlock()

	if err != nil || time.Duration(v)*time.Second <= u.expires {
		return &ResponseError{Response: res}
	}

	u.expires = time.Duration(v) * time.Second

	return nil
}

func (u *UE) granted(res *sip.Response) time.Duration {
	contacts, _ := res.Header.Contacts()
	for _, c := range contacts {
		if c.URI.User != u.user || !strings.EqualFold(c.URI.Host, sip.FormatHost(u.cfg.Local)) || !u.ownFlow(c.Params) {
			continue
		}

		if v, ok := c.Params.Get("expires"); ok {
			if n, err := strconv.ParseUint(v, 10, 32); err == nil {
				return time.Duration(n) * time.Second
			}
		}
	}

	if n, err := res.Header.Expires(); err == nil {
		return time.Duration(n) * time.Second
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	return u.expires
}

func (u *UE) succeeded(p *procedure, temp *saSet, res *sip.Response) {
	granted := u.granted(res)

	u.mu.Lock()
	defer u.mu.Unlock()

	if u.closed {
		return
	}

	if p.dereg || granted == 0 {
		u.releaseClient(p)

		for _, s := range slices.Clone(u.sets) {
			u.dropLocked(s)
		}

		go u.forgetLocked()()

		return
	}

	now := u.now()
	life := now.Add(granted + saMargin)

	switch {
	case temp != nil:
		temp.state, temp.ownsSPIs = Established, true
		p.client = client{}
		u.retired = nil

		if p.origin != nil && p.origin.expires.After(life) {
			life = p.origin.expires
		}

		u.lifetimeLocked(temp, life)

		for _, s := range slices.Clone(u.sets) {
			switch s {
			case temp:
			case p.origin:
				s.state = Old
				u.newSAUsed = false
			default:
				u.dropLocked(s)
			}
		}
	case p.origin != nil:
		u.releaseClient(p)

		if life.After(p.origin.expires) {
			u.lifetimeLocked(p.origin, life)
		}
	}

	var associated []string

	if addrs, err := res.Header.Addresses("P-Associated-URI"); err == nil {
		for _, a := range addrs {
			associated = append(associated, a.URI.String())
		}
	}

	var impus []string
	if u.state.Registered {
		impus = u.state.IMPUs
	}

	// The P-CSCF the contact is bound through, which a registration resets the failures of (RFC 5626 §4.5).
	u.registeredVia = u.pcscf
	delete(u.marks, u.pcscf)

	u.auth = p.auth
	u.state = State{
		Registered:     true,
		Outbound:       slices.ContainsFunc(res.Header.Elements("Require"), func(e string) bool { return strings.EqualFold(strings.TrimSpace(e), "outbound") }),
		Expires:        now.Add(granted),
		AssociatedURIs: associated,
		ServiceRoute:   res.Header.Values("Service-Route"),
		Barred:         !slices.ContainsFunc(associated, func(a string) bool { return sameURI(a, u.id.IMPU) }),
		IMPUs:          impus,
	}

	if len(associated) > 0 {
		u.state.DefaultIMPU = associated[0]
	}

	u.scheduleLocked(granted)

	u.flowTimer = 0

	if u.state.Outbound {
		if n, err := strconv.ParseUint(res.Header.Get("Flow-Timer"), 10, 32); err == nil && n > 0 {
			u.flowTimer = time.Duration(n) * time.Second
		}
	}

	u.keepaliveLocked()
}

// pongTimeout is how long a keep-alive waits for its answer (RFC 5626 §4.4.1: 10 seconds); tests shorten it.
var pongTimeout = 10 * time.Second

// keepaliveLocked schedules the next keep-alive of the registration's flow, if its 200 had a Flow-Timer (RFC 5626
// §4.4.1): between 80% and 100% of it, at random. Without one, none: the UE is behind no NAT (TS 24.229 §5.1.1.2.1
// d, the P-CSCF sending a Flow-Timer only to a UE behind one).
func (u *UE) keepaliveLocked() {
	u.stopKeepaliveLocked()

	if u.flowTimer <= 0 {
		return
	}

	d := u.flowTimer*4/5 + time.Duration(rand.Int64N(int64(u.flowTimer/5)+1))
	u.keepTimer = u.layer.Clock().AfterFunc(d, u.keepalive)
}

func (u *UE) stopKeepaliveLocked() {
	if u.keepTimer != nil {
		u.keepTimer.Stop()
		u.keepTimer = nil
	}
}

// keepalive pings the flow of the registration; its failure ends the registration (RFC 5626 §4.4.1, §4.5).
func (u *UE) keepalive() {
	flow, _, err := u.requestFlow(u.cfg.Transport)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), pongTimeout)
		err = u.layer.Ping(ctx, flow)

		cancel()
	}

	if err != nil {
		u.flowFailed(err)
		return
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	if !u.closed && u.state.Registered {
		u.keepaliveLocked()
	}
}

// flowFailed ends the registration whose flow failed: its SAs go, the UE stops processing the dialogs and
// transactions of the flow, sending nothing on their behalf (TS 24.229 §5.1.1.4.1, NOTE 13), and the owner registers
// again (RFC 5626 §4.4, §4.5), Register forming a new flow. Not called from a handler, whose strand Drop waits for.
func (u *UE) flowFailed(err error) {
	// The dialogs of the flow are stopped even when another failure, of a request, unbound the contact first.
	bound := u.unbind()

	u.mu.Lock()
	closed := u.closed
	u.mu.Unlock()

	if closed {
		return
	}

	u.agent.Drop(func(sip.Flow) bool { return true })

	if bound {
		u.event(Event{Deregistered: DeregisteredFlowFailed, Err: err})
	}
}

// unbind ends the registration, its SAs gone, and reports whether there was one.
func (u *UE) unbind() bool {
	u.mu.Lock()

	if u.closed || !u.state.Registered {
		u.mu.Unlock()
		return false
	}

	after := u.forgetLocked()
	sets := slices.Clone(u.sets)

	for _, s := range sets {
		u.dropLocked(s)
	}

	u.mu.Unlock()

	after()

	return true
}

// requestFailed is a request of the UE through the P-CSCF failing in its transaction: other than a REGISTER, timer F
// or a fatal transport error in Trying of a non-INVITE, timer B or a fatal transport error in Calling of an INVITE,
// leave the contact bound to no public user identity and mark the P-CSCF unavailable (TS 24.229 §5.1.2A.1.6): the
// registration ends, its flow dead with the multiple registration mechanism, and the owner registers again, through
// another P-CSCF if it has one. The dialogs are left to the owner. It runs on a goroutine of its own: the transaction
// tells it on a strand, whose handlers ending the registration waits for.
func (u *UE) requestFailed(req *sip.Request, in transaction.State, err error) {
	if req.Method == "REGISTER" || !errors.Is(err, transaction.ErrTimeout) && !errors.Is(err, transaction.ErrTransport) {
		return
	}

	if req.Method == "INVITE" && in != transaction.Calling || req.Method != "INVITE" && in != transaction.Trying {
		return
	}

	go func() {
		// The P-CSCF the contact was registered through (§5.1.2A.1.6 B), which the request went to a port of.
		u.mu.Lock()

		pcscf := u.registeredVia
		bound := !u.closed && u.state.Registered && req.Flow.Remote.Addr() == pcscf.Addr()

		if bound {
			u.markLocked(pcscf)
		}

		u.mu.Unlock()

		if bound && u.unbind() {
			u.event(Event{Deregistered: DeregisteredFlowFailed, Err: err, UnavailablePCSCF: pcscf})
		}
	}()
}

// connectionLost is a TCP connection of the UE ending, of flow f: that of an outbound registration is its flow, which
// failed (RFC 5626 §3, §4.4). Without outbound, the registration outlives its connections, the P-CSCF opening one of
// its own to reach the UE.
func (u *UE) connectionLost(f sip.Flow, err error) {
	u.mu.Lock()
	outbound := !u.closed && u.state.Registered && u.state.Outbound
	u.mu.Unlock()

	if !outbound {
		return
	}

	if flow, _, ferr := u.requestFlow(u.cfg.Transport); ferr != nil || flow != f {
		return
	}

	u.flowFailed(err)
}

// listenerFailed is a listener of the UE failing for good, of its unprotected port or of a protected one, as when the
// platform destroyed its sockets with the network they were bound to: the flow of the registration failed, as on a
// keep-alive not answered (TS 24.229 §5.1.1.4.1 NOTE 13: by other means than timer F). The UE listens on the port
// again before its next registration.
func (u *UE) listenerFailed(local netip.AddrPort, err error) {
	u.mu.Lock()

	if u.closed {
		u.mu.Unlock()
		return
	}

	u.failed = append(u.failed, local.Port())
	u.mu.Unlock()

	u.flowFailed(fmt.Errorf("ue: listener %s failed: %w", local, err))
}

// reopen listens again on the ports whose listeners failed, before a registration: the unprotected port as it was,
// the protected server port and the client ports anew, as for a new UE. Called with u.op held.
func (u *UE) reopen() error {
	u.mu.Lock()
	failed := u.failed
	u.failed = nil
	u.clientPorts = slices.DeleteFunc(u.clientPorts, func(p uint16) bool { return slices.Contains(failed, p) })
	u.mu.Unlock()

	if slices.Contains(failed, u.portS) {
		u.portS = 0
	}

	if !slices.Contains(failed, u.unprotected.Port()) {
		return nil
	}

	if _, err := u.listen(u.unprotected.Port()); err != nil {
		u.mu.Lock()
		u.failed = append(u.failed, u.unprotected.Port())
		u.mu.Unlock()

		return err
	}

	return nil
}

// ownFlow reports whether a contact's parameters name the UE's flow, when it registers one.
func (u *UE) ownFlow(params sip.Params) bool {
	if u.cfg.RegID == 0 {
		return true
	}

	v, ok := params.Get("reg-id")

	return ok && v == strconv.FormatInt(u.cfg.RegID, 10)
}

func sameURI(a, b string) bool {
	ua, errA := sip.ParseURI(a)
	ub, errB := sip.ParseURI(b)

	return errA == nil && errB == nil && ua.Equivalent(ub)
}

func reregisterIn(remaining time.Duration) time.Duration {
	if remaining > 1200*time.Second {
		return remaining - 600*time.Second
	}

	return remaining / 2
}

func (u *UE) scheduleLocked(remaining time.Duration) {
	u.stopTimersLocked()

	expires := u.state.Expires

	u.expiryTimer = u.layer.Clock().AfterFunc(remaining, func() {
		u.mu.Lock()

		expired := u.state.Registered && !u.state.Expires.After(expires)
		if expired {
			u.state.Registered = false
		}

		u.mu.Unlock()

		if expired {
			u.event(Event{Deregistered: DeregisteredExpired})
		}
	})

	if !u.auto {
		return
	}

	u.reregTimer = u.layer.Clock().AfterFunc(reregisterIn(remaining), func() {
		err := u.Reregister(context.Background())
		if err != nil && !errors.Is(err, ErrNotRegistered) && !errors.Is(err, ErrClosed) && !errors.Is(err, transaction.ErrClosed) {
			u.event(Event{Err: err})
		}
	})
}
