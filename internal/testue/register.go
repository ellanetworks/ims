package testue

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/milenage"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

const icsiMMTel = `"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`

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

	if err := u.ready(); err != nil {
		return err
	}

	for restart := 0; ; restart++ {
		u.newDialog()

		p := &procedure{start: u.unprotectedFlow(), auth: emptyAuthorization(u.id.impi, u.id.domain)}

		err := u.run(ctx, p)
		if errors.Is(err, ErrNoSecurityServer) && restart == 0 {
			continue
		}

		return err
	}
}

func (u *UE) Reregister(ctx context.Context) error {
	u.op.Lock()
	defer u.op.Unlock()

	return u.refresh(ctx, false)
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
	return sip.Flow{Transport: u.cfg.Transport, Local: u.unprotected, Remote: u.cfg.PCSCF}
}

func (u *UE) port(protected bool) uint16 {
	if protected {
		return u.portS
	}

	return u.unprotected.Port()
}

func (u *UE) contact(port uint16) string {
	uri := sip.URI{Scheme: "sip", User: u.user, Host: sip.FormatHost(u.cfg.Local), Port: port}

	return sip.Address{URI: uri, Params: sip.Params{
		{Name: "+sip.instance", Value: sip.Quote("<" + u.instance + ">")},
		{Name: "+g.3gpp.icsi-ref", Value: icsiMMTel},
		{Name: "+g.3gpp.smsip"},
		{Name: "audio"},
	}}.String()
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
		u.spis.Release(p.client.spiC, p.client.spiS)
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

	req := sip.NewRequest("REGISTER", sip.URI{Scheme: "sip", Host: u.id.domain})
	req.Flow = p.flow

	port := u.port(protected)

	via := sip.NewVia(p.flow.Transport, netip.AddrPortFrom(u.cfg.Local, port))
	via.Params.Set("rport", "")

	req.Header.Add("Via", via.String())
	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("From", "<"+u.id.impu+">;tag="+fromTag)
	req.Header.Add("To", "<"+u.id.impu+">")
	req.Header.Add("Call-ID", callID)
	req.Header.Add("CSeq", strconv.FormatUint(uint64(cseq), 10)+" REGISTER")
	req.Header.Add("Contact", u.contact(port))
	req.Header.Add("Expires", strconv.FormatInt(int64(expires/time.Second), 10))
	req.Header.Add("Supported", "path")
	req.Header.Add("Authorization", p.auth)

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
			req.Header.Add("P-Access-Network-Info", u.cfg.AccessNetworkInfo)
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
				u.drop(temp)
				temp, p.flow, p.verify = nil, p.start, p.startVerify
			}

			ch, err := parseChallenge(res)
			if err != nil {
				return fail(err)
			}

			r, err := milenage.Respond(u.cfg.K, u.cfg.OPc, ch.rand, ch.autn)

			u.mu.Lock()
			sqnMS := u.sqn
			u.mu.Unlock()

			switch {
			case errors.Is(err, milenage.ErrMACFailure):
				if invalid++; invalid > maxInvalidChallenges {
					return fail(ErrNetworkAuthentication)
				}

				macFailure = true
				p.auth = answer(u.id.impi, u.id.domain, ch, nil, nil, true)
			case err != nil:
				return fail(fmt.Errorf("testue: %w", err))
			case r.SQN <= sqnMS:
				if invalid++; invalid > maxInvalidChallenges {
					return fail(fmt.Errorf("testue: SQN %d not above %d", r.SQN, sqnMS))
				}

				auts, err := milenage.AUTS(u.cfg.K, u.cfg.OPc, ch.rand, sqnMS)
				if err != nil {
					return fail(fmt.Errorf("testue: %w", err))
				}

				p.auth = answer(u.id.impi, u.id.domain, ch, nil, auts, false)
			}

			if err != nil || r.SQN <= sqnMS {
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

			u.mu.Lock()
			u.sqn = r.SQN
			u.mu.Unlock()

			p.auth = answer(u.id.impi, u.id.domain, ch, r.RES, nil, false)

			if u.cfg.Plain {
				continue
			}

			if temp, err = u.install(p.client, offer, ipsec.Keys{CK: r.CK, IK: r.IK}, server); err != nil {
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
		if c.URI.User != u.user || !strings.EqualFold(c.URI.Host, sip.FormatHost(u.cfg.Local)) {
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

		u.stopTimersLocked()
		u.state = State{}
		u.auth = ""

		return
	}

	now := time.Now()
	life := now.Add(granted + saMargin)

	switch {
	case temp != nil:
		temp.state, temp.ownsSPIs = Established, true
		p.client = client{}

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

	u.auth = p.auth
	u.state = State{
		Registered:     true,
		Expires:        now.Add(granted),
		AssociatedURIs: associated,
		ServiceRoute:   res.Header.Values("Service-Route"),
		Barred:         !slices.ContainsFunc(associated, func(a string) bool { return sameURI(a, u.id.impu) }),
	}

	if len(associated) > 0 {
		u.state.DefaultIMPU = associated[0]
	}

	u.scheduleLocked(granted)
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

	u.expiryTimer = time.AfterFunc(remaining, func() {
		u.mu.Lock()
		defer u.mu.Unlock()

		if u.state.Registered && !u.state.Expires.After(expires) {
			u.state.Registered = false
		}
	})

	if !u.auto {
		return
	}

	u.reregTimer = time.AfterFunc(reregisterIn(remaining), func() {
		err := u.Reregister(context.Background())
		if err != nil && !errors.Is(err, ErrNotRegistered) && !errors.Is(err, ErrClosed) && !errors.Is(err, transaction.ErrClosed) {
			u.event(Event{Err: err})
		}
	})
}
