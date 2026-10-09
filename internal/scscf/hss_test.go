package scscf

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/diametertest"
)

const (
	hss1 = "hss1." + homeDomain
	hss2 = "hss2." + homeDomain
)

// hssCluster is several HSS nodes of one realm sharing the fake HSS's subscribers, as a replicated HSS: a request to
// a Destination-Host goes to that node, and one routed by realm to the first node up.
type hssCluster struct {
	loop *diametertest.Loop

	mu    sync.Mutex
	nodes []string
	down  map[string]bool
	busy  map[string]bool
	hung  map[string]bool
	noCx  map[string]bool
	// behind are the nodes reached through an agent, in their own realm: a request to one of them needs a route,
	// which only the configured realm has.
	behind map[string]string
	sent   []cxSent
}

// cxSent is a Cx request: where the S-CSCF addressed it, and the HSS that answered.
type cxSent struct {
	command uint32
	to      string
	by      string
}

func newHSSCluster(h *harness, nodes ...string) *hssCluster {
	c := &hssCluster{
		loop: h.loop, nodes: nodes, down: map[string]bool{}, busy: map[string]bool{}, hung: map[string]bool{},
		noCx: map[string]bool{}, behind: map[string]string{},
	}

	h.cfg.Diameter = c
	h.restart()

	return c
}

func (c *hssCluster) Identity() diameter.Identity { return c.loop.Identity() }

func (c *hssCluster) NewSessionID() string { return c.loop.NewSessionID() }

func (c *hssCluster) Send(ctx context.Context, req *diameter.Message, opts ...diameter.RequestOption) (*diameter.Message, error) {
	env := tgpp.ParseEnvelope(req)
	to := env.DestinationHost

	c.mu.Lock()

	if realm, ok := c.behind[to]; ok && env.DestinationRealm != homeDomain && env.DestinationRealm == realm {
		c.sent = append(c.sent, cxSent{command: req.CommandCode, to: to})
		c.mu.Unlock()

		return nil, diameter.ErrUnableToDeliver
	}

	by := to
	if to == "" {
		for _, n := range c.nodes {
			if !c.down[n] {
				by = n
				break
			}
		}
	}

	down, busy, hung, noCx := by == "" || c.down[by], c.busy[by], c.hung[by], c.noCx[by]
	originRealm := homeDomain

	if realm, ok := c.behind[by]; ok {
		originRealm = realm
	}

	c.sent = append(c.sent, cxSent{command: req.CommandCode, to: to, by: by})
	c.mu.Unlock()

	switch {
	case down:
		return nil, diameter.ErrNotConnected
	case noCx:
		return nil, diameter.ErrApplicationUnsupported
	case hung:
		<-ctx.Done()
		return nil, ctx.Err()
	case busy:
		return diameter.NewAnswer(req, diameter.Identity{OriginHost: by, OriginRealm: homeDomain}, diameter.ResultTooBusy), nil
	}

	ans, err := c.loop.Send(ctx, req, opts...)
	if err != nil {
		return nil, err
	}

	for i, a := range ans.AVPs {
		switch a.Code {
		case diameter.AVPOriginHost:
			ans.AVPs[i] = diameter.UTF8String(diameter.AVPOriginHost, diameter.AVPFlagMandatory, 0, by)
		case diameter.AVPOriginRealm:
			ans.AVPs[i] = diameter.UTF8String(diameter.AVPOriginRealm, diameter.AVPFlagMandatory, 0, originRealm)
		}
	}

	return ans, nil
}

func (c *hssCluster) set(f func(c *hssCluster)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	f(c)
}

// take returns the Cx requests sent since the last call.
func (c *hssCluster) take() []cxSent {
	c.mu.Lock()
	defer c.mu.Unlock()

	sent := c.sent
	c.sent = nil

	return sent
}

func (h *harness) registeredHSS() db.HSS {
	h.t.Helper()

	regs, err := h.db.ListRegistrationsByIMPI(h.t.Context(), testIMPI)
	if err != nil || len(regs) != 1 {
		h.t.Fatalf("registrations = %+v, %v; want one", regs, err)
	}

	return regs[0].HSS
}

func wantSent(t *testing.T, got []cxSent, want ...cxSent) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Fatalf("Cx requests = %+v, want %+v", got, want)
	}
}

// TS 29.229 §5.5: the first request goes to the realm, the next ones to the HSS that answered.
func TestRegistrationGoesToTheHSSThatAnswered(t *testing.T) {
	h := newHarness(t)
	c := newHSSCluster(h, hss1, hss2)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	wantSent(t, c.take(),
		cxSent{cx.CommandMultimediaAuth, "", hss1},
		cxSent{cx.CommandServerAssignment, hss1, hss1},
	)

	if got := h.registeredHSS(); got != (db.HSS{Host: hss1, Realm: homeDomain}) {
		t.Fatalf("HSS = %+v, want %s", got, hss1)
	}

	c.set(func(c *hssCluster) { c.nodes = []string{hss2, hss1} })

	u.register(registerOptions{})

	if sar := h.hss.nextSAR(t); !sar.UserDataAlreadyAvailable {
		t.Fatal("SAR to the HSS of the registration does not say the user data is already available")
	}

	wantSent(t, c.take(),
		cxSent{cx.CommandMultimediaAuth, hss1, hss1},
		cxSent{cx.CommandServerAssignment, hss1, hss1},
	)
}

// TS 29.229 §5.5: the HSS is stored per Public Identity: another one of the private identity goes to the realm, and
// each then goes to its own HSS.
func TestHSSPerPublicIdentity(t *testing.T) {
	h := newHarness(t)
	c := newHSSCluster(h, hss1, hss2)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)
	c.take()

	c.set(func(c *hssCluster) { c.nodes = []string{hss2, hss1} })

	u.impu = secondIMPU
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
	h.hss.nextSAR(t)

	wantSent(t, c.take(), cxSent{cx.CommandServerAssignment, "", hss2})

	if got := h.registration(secondIMPU).HSS.Host; got != hss2 {
		t.Fatalf("HSS of %s = %s, want %s", secondIMPU, got, hss2)
	}

	u.impu = testIMPU
	u.register(registerOptions{})
	h.hss.nextSAR(t)

	wantSent(t, c.take(),
		cxSent{cx.CommandMultimediaAuth, hss1, hss1},
		cxSent{cx.CommandServerAssignment, hss1, hss1},
	)
}

func TestLostHSSFallsBackToTheRealm(t *testing.T) {
	for name, lose := range map[string]func(c *hssCluster){
		"down":     func(c *hssCluster) { c.down[hss1] = true },
		"too busy": func(c *hssCluster) { c.busy[hss1] = true },
		"no Cx":    func(c *hssCluster) { c.noCx[hss1] = true },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			c := newHSSCluster(h, hss1, hss2)
			u := h.newUE()

			u.register(registerOptions{})
			h.hss.nextSAR(t)
			c.take()

			c.set(func(c *hssCluster) {
				lose(c)
				c.nodes = []string{hss2, hss1}
			})

			u.register(registerOptions{})

			if sar := h.hss.nextSAR(t); sar.UserDataAlreadyAvailable {
				t.Fatal("SAR to another HSS says the user data is already available")
			}

			wantSent(t, c.take(),
				cxSent{cx.CommandMultimediaAuth, hss1, hss1},
				cxSent{cx.CommandMultimediaAuth, "", hss2},
				cxSent{cx.CommandServerAssignment, hss2, hss2},
			)

			if got := h.registeredHSS(); got.Host != hss2 {
				t.Fatalf("HSS = %+v, want %s", got, hss2)
			}
		})
	}
}

func TestDeregistrationGoesToTheHSSOfTheRegistration(t *testing.T) {
	h := newHarness(t)
	c := newHSSCluster(h, hss1, hss2)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)
	c.take()

	c.set(func(c *hssCluster) { c.nodes = []string{hss2, hss1} })

	wantStatus(t, u.send(registerOptions{expires: "0", auth: u.protected(testNonce(), testVector.XRES)}), 200)
	h.hss.wantSAR(t, cx.AssignmentUserDeregistration)

	wantSent(t, c.take(), cxSent{cx.CommandServerAssignment, hss1, hss1})
}

func TestTimeoutDeregistrationGoesToTheHSSOfTheRegistration(t *testing.T) {
	h := newHarness(t)
	c := newHSSCluster(h, hss1, hss2)
	u := h.newUE()

	u.register(registerOptions{expires: "600"})
	h.hss.nextSAR(t)
	c.take()

	c.set(func(c *hssCluster) { c.nodes = []string{hss2, hss1} })

	h.clock.Advance(600 * time.Second)
	h.clock.Advance(sweepInterval)
	h.hss.wantSAR(t, cx.AssignmentTimeoutDeregistration)

	wantSent(t, c.take(), cxSent{cx.CommandServerAssignment, hss1, hss1})
}

func TestAuthenticationFailureGoesToTheChallengingHSS(t *testing.T) {
	h := newHarness(t)
	c := newHSSCluster(h, hss1, hss2)
	u := h.newUE()

	nonce := u.challenged(registerOptions{})

	c.take()

	c.set(func(c *hssCluster) { c.nodes = []string{hss2, hss1} })

	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, []byte("wrong-xres"))}), 403)
	h.hss.wantSAR(t, cx.AssignmentAuthenticationFailure)

	wantSent(t, c.take(), cxSent{cx.CommandServerAssignment, hss1, hss1})
}

func TestRegistrationWithoutAnyHSS(t *testing.T) {
	h := newHarness(t)
	c := newHSSCluster(h, hss1)
	u := h.newUE()

	c.set(func(c *hssCluster) { c.down[hss1] = true })

	res := u.send(registerOptions{auth: u.unprotected()})
	wantStatus(t, res, 500)

	if res.Header.Get("Retry-After") == "" {
		t.Fatal("no Retry-After on an unreachable HSS")
	}
}

// RFC 6733 §5.5.4: an HSS that keeps its connection but answers nothing has half the time; the realm has the rest.
func TestHungHSSFallsBackInTime(t *testing.T) {
	h := newHarness(t)
	h.cfg.CxTimeout = time.Second
	c := newHSSCluster(h, hss1, hss2)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)
	c.take()

	c.set(func(c *hssCluster) {
		c.hung[hss1] = true
		c.nodes = []string{hss2, hss1}
	})

	start := time.Now()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	if d := time.Since(start); d >= h.cfg.CxTimeout {
		t.Fatalf("re-registration took %s, want the fallback within the %s Cx timeout", d, h.cfg.CxTimeout)
	}

	if got := h.registeredHSS(); got.Host != hss2 {
		t.Fatalf("HSS = %+v, want %s", got, hss2)
	}
}

// TS 29.229 §5.5: an HSS behind an agent, in a realm the S-CSCF has no route for, is still reached through the
// configured realm, and stays the HSS of the registration.
func TestHSSBehindAnAgentInAnotherRealm(t *testing.T) {
	const realm = "hss.example.net"

	h := newHarness(t)
	c := newHSSCluster(h, hss1, hss2)
	u := h.newUE()

	c.set(func(c *hssCluster) { c.behind[hss1] = realm })

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	if got := h.registeredHSS(); got != (db.HSS{Host: hss1, Realm: realm}) {
		t.Fatalf("HSS = %+v, want %s in %s", got, hss1, realm)
	}

	c.take()
	c.set(func(c *hssCluster) { c.nodes = []string{hss2, hss1} })

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	wantSent(t, c.take(),
		cxSent{cx.CommandMultimediaAuth, hss1, ""},
		cxSent{cx.CommandMultimediaAuth, hss1, hss1},
		cxSent{cx.CommandServerAssignment, hss1, ""},
		cxSent{cx.CommandServerAssignment, hss1, hss1},
	)
}
