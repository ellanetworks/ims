package rxpolicy

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/policy"
)

const (
	pcrfRealm = "epc.example.org"
	pcrf1     = "pcrf1." + pcrfRealm
	pcrf2     = "pcrf2." + pcrfRealm
)

var ue = netip.MustParseAddr("10.45.0.2")

// pcrfCluster is several PCRFs of one realm: a request to a Destination-Host goes to that PCRF, one routed by realm
// to the first PCRF up. Each AA-Answer carries the cluster's Session-Binding and Session-Server-Failover.
type pcrfCluster struct {
	mu    sync.Mutex
	nodes []string
	down  map[string]bool
	busy  map[string]bool
	hung  map[string]bool
	gone  map[string]bool
	// behind are the PCRFs reached through an agent, in their own realm: a request to one of them needs a route,
	// which only the configured realm has.
	behind map[string]string
	// class is the Class each PCRF puts in its AA-Answers, if any.
	class    map[string]string
	binding  diameter.SessionBinding
	failover diameter.SessionServerFailover
	sent     []rxSent
}

type rxSent struct {
	command uint32
	to      string
	by      string
}

func newPCRFCluster(binding diameter.SessionBinding, failover diameter.SessionServerFailover) (*pcrfCluster, *Backend) {
	c := &pcrfCluster{
		nodes: []string{pcrf1, pcrf2}, down: map[string]bool{}, busy: map[string]bool{}, hung: map[string]bool{},
		gone: map[string]bool{}, behind: map[string]string{}, class: map[string]string{}, binding: binding, failover: failover,
	}

	return c, New(Config{Diameter: c, Realm: func() string { return pcrfRealm }})
}

func (c *pcrfCluster) Identity() diameter.Identity {
	return diameter.Identity{OriginHost: "ims.ims.example.org", OriginRealm: "ims.example.org"}
}

func (c *pcrfCluster) NewSessionID() string { return "ims.ims.example.org;1;1" }

func (c *pcrfCluster) Send(ctx context.Context, req *diameter.Message, _ ...diameter.RequestOption) (*diameter.Message, error) {
	env := tgpp.ParseEnvelope(req)
	to := env.DestinationHost

	c.mu.Lock()

	if realm, ok := c.behind[to]; ok && env.DestinationRealm == realm {
		c.sent = append(c.sent, rxSent{command: req.CommandCode, to: to})
		c.mu.Unlock()

		return nil, diameter.ErrUnableToDeliver
	}

	by := to
	if to == "" {
		for _, n := range c.nodes {
			if !c.down[n] && !c.gone[n] && !c.hung[n] {
				by = n
				break
			}
		}
	}

	down, busy, hung, gone := by == "" || c.down[by], c.busy[by], c.hung[by], c.gone[by]
	binding, failover, class := c.binding, c.failover, c.class[by]
	realm := pcrfRealm

	if r, ok := c.behind[by]; ok {
		realm = r
	}

	c.sent = append(c.sent, rxSent{command: req.CommandCode, to: to, by: by})
	c.mu.Unlock()

	id := diameter.Identity{OriginHost: by, OriginRealm: realm}

	switch {
	case gone:
		return diameter.NewAnswer(req, diameter.Identity{OriginHost: "dra." + pcrfRealm, OriginRealm: pcrfRealm},
			diameter.ResultUnableToDeliver), nil
	case down:
		return nil, diameter.ErrNotConnected
	case busy:
		return diameter.NewAnswer(req, id, diameter.ResultTooBusy), nil
	case hung:
		<-ctx.Done()
		return nil, ctx.Err()
	}

	var (
		ans *diameter.Message
		err error
	)

	switch req.CommandCode {
	case rx.CommandAA:
		a := rx.AAAnswer{Result: tgpp.Result{Code: diameter.ResultSuccess}, SessionBinding: binding, SessionServerFailover: failover}
		if class != "" {
			a.Class = [][]byte{[]byte(class)}
		}

		ans, err = rx.NewAAAnswer(req, id, a)
	case rx.CommandSessionTermination:
		ans, err = rx.NewSessionTerminationAnswer(req, id, rx.SessionTerminationAnswer{})
	default:
		return nil, errors.New("unexpected command")
	}

	if err != nil {
		return nil, err
	}

	b, err := ans.Marshal()
	if err != nil {
		return nil, err
	}

	return diameter.Unmarshal(b)
}

func (c *pcrfCluster) set(f func(c *pcrfCluster)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	f(c)
}

func (c *pcrfCluster) take() []rxSent {
	c.mu.Lock()
	defer c.mu.Unlock()

	sent := c.sent
	c.sent = nil

	return sent
}

func wantRx(t *testing.T, got []rxSent, want ...rxSent) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Fatalf("Rx requests = %+v, want %+v", got, want)
	}
}

func audio() []policy.MediaComponent {
	return []policy.MediaComponent{{Number: 1, Type: policy.MediaAudio, Status: policy.FlowEnabled}}
}

// openCall opens a call's session, which pcrf1 takes.
func openCall(t *testing.T, b *Backend) string {
	t.Helper()

	g, err := b.Authorize(t.Context(), "call", "", policy.Request{UE: ue, Initial: true, Components: audio()})
	if err != nil {
		t.Fatalf("initial Authorize: %v", err)
	}

	return g.Ref
}

func update(t *testing.T, b *Backend, ref string) (policy.Grant, error) {
	t.Helper()

	return b.Authorize(t.Context(), "call", ref, policy.Request{UE: ue, Components: audio()})
}

// RFC 6733 §8.17: by default, the requests of a session after its first go to the PCRF that answered it.
func TestSessionStaysOnItsPCRF(t *testing.T) {
	c, b := newPCRFCluster(0, diameter.RefuseService)

	ref := openCall(t, b)

	c.set(func(c *pcrfCluster) { c.nodes = []string{pcrf2, pcrf1} })

	g, err := update(t, b, ref)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if err := b.Terminate(t.Context(), "call", g.Ref, policy.TerminationLogout, false); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	wantRx(t, c.take(),
		rxSent{rx.CommandAA, "", pcrf1},
		rxSent{rx.CommandAA, pcrf1, pcrf1},
		rxSent{rx.CommandSessionTermination, pcrf1, pcrf1},
	)
}

// RFC 6733 §8.17: the RE_AUTH and STR bits send those requests by realm.
func TestSessionBindingBits(t *testing.T) {
	c, b := newPCRFCluster(diameter.SessionBindingReAuth|diameter.SessionBindingSTR, diameter.RefuseService)

	ref := openCall(t, b)

	g, err := update(t, b, ref)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if err := b.Terminate(t.Context(), "call", g.Ref, policy.TerminationLogout, false); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	wantRx(t, c.take(),
		rxSent{rx.CommandAA, "", pcrf1},
		rxSent{rx.CommandAA, "", pcrf1},
		rxSent{rx.CommandSessionTermination, "", pcrf1},
	)
}

// RFC 6733 §8.18 REFUSE_SERVICE, the default: a session that no path reaches its PCRF with is lost, without
// another attempt.
func TestUnreachablePCRFRefusesService(t *testing.T) {
	c, b := newPCRFCluster(0, diameter.RefuseService)

	ref := openCall(t, b)

	c.take()
	c.set(func(c *pcrfCluster) { c.gone[pcrf1] = true })

	if _, err := update(t, b, ref); !errors.Is(err, policy.ErrSessionLost) || policy.Transient(err) {
		t.Fatalf("update = %v, want the session lost", err)
	}

	err := b.Terminate(t.Context(), "call", ref, policy.TerminationLogout, false)
	if !errors.Is(err, policy.ErrSessionLost) || policy.Transient(err) {
		t.Fatalf("Terminate = %v, want the session lost", err)
	}

	wantRx(t, c.take(),
		rxSent{rx.CommandAA, pcrf1, pcrf1},
		rxSent{rx.CommandSessionTermination, pcrf1, pcrf1},
	)
}

// RFC 6733 §5.5.4, §7.1.3: a PCRF whose connection is down, or that is too busy, has not lost the session: the
// requests stay pending, to be sent again.
func TestPendingPCRFKeepsTheSession(t *testing.T) {
	for name, lose := range map[string]func(c *pcrfCluster){
		"down":     func(c *pcrfCluster) { c.down[pcrf1] = true },
		"too busy": func(c *pcrfCluster) { c.busy[pcrf1] = true },
	} {
		t.Run(name, func(t *testing.T) {
			c, b := newPCRFCluster(0, diameter.RefuseService)

			ref := openCall(t, b)

			c.take()
			c.set(lose)

			if _, err := update(t, b, ref); errors.Is(err, policy.ErrSessionLost) || !policy.Transient(err) {
				t.Fatalf("update = %v, want it pending", err)
			}

			err := b.Terminate(t.Context(), "call", ref, policy.TerminationLogout, false)
			if errors.Is(err, policy.ErrSessionLost) || !policy.Transient(err) {
				t.Fatalf("Terminate = %v, want it pending, to be sent again", err)
			}

			c.set(func(c *pcrfCluster) { c.down, c.busy = map[string]bool{}, map[string]bool{} })

			if err := b.Terminate(t.Context(), "call", ref, policy.TerminationLogout, false); err != nil {
				t.Fatalf("Terminate once the PCRF is back: %v", err)
			}

			wantRx(t, c.take(),
				rxSent{rx.CommandAA, pcrf1, pcrf1},
				rxSent{rx.CommandSessionTermination, pcrf1, pcrf1},
				rxSent{rx.CommandSessionTermination, pcrf1, pcrf1},
			)
		})
	}
}

// RFC 6733 §8.18 TRY_AGAIN: once more by realm, and the session goes on with the PCRF that answers.
func TestLostPCRFTriesAgain(t *testing.T) {
	c, b := newPCRFCluster(0, diameter.TryAgain)

	ref := openCall(t, b)

	c.take()
	c.set(func(c *pcrfCluster) { c.down[pcrf1] = true })

	g, err := update(t, b, ref)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if err := b.Terminate(t.Context(), "call", g.Ref, policy.TerminationLogout, false); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	wantRx(t, c.take(),
		rxSent{rx.CommandAA, pcrf1, pcrf1},
		rxSent{rx.CommandAA, "", pcrf2},
		rxSent{rx.CommandSessionTermination, pcrf2, pcrf2},
	)

	c.set(func(c *pcrfCluster) { c.down[pcrf2] = true })

	if _, err := update(t, b, g.Ref); errors.Is(err, policy.ErrSessionLost) || !policy.Transient(err) {
		t.Fatalf("update with every PCRF down = %v, want it pending", err)
	}

	c.set(func(c *pcrfCluster) { c.gone[pcrf2] = true })

	if _, err := update(t, b, g.Ref); !errors.Is(err, policy.ErrSessionLost) {
		t.Fatalf("update with no path to any PCRF = %v, want the session lost", err)
	}
}

// RFC 6733 §8.18 TRY_AGAIN: a PCRF that stops answering has half the time, and the realm the rest.
func TestHungPCRFTriesAgainInTime(t *testing.T) {
	c, b := newPCRFCluster(0, diameter.TryAgain)

	ref := openCall(t, b)

	c.take()
	c.set(func(c *pcrfCluster) { c.hung[pcrf1] = true })

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	if err := b.Terminate(ctx, "call", ref, policy.TerminationLogout, true); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	wantRx(t, c.take(),
		rxSent{rx.CommandSessionTermination, pcrf1, pcrf1},
		rxSent{rx.CommandSessionTermination, "", pcrf2},
	)
}

// RFC 6733 §8.18 ALLOW_SERVICE: the session goes on, unbound: the next request goes by realm.
func TestLostPCRFAllowsService(t *testing.T) {
	c, b := newPCRFCluster(0, diameter.AllowService)

	ref := openCall(t, b)

	c.take()
	c.set(func(c *pcrfCluster) { c.gone[pcrf1] = true })

	g, err := update(t, b, ref)
	if err != nil || g.Ref == "" {
		t.Fatalf("update = %+v, %v; want it to succeed, unbound", g, err)
	}

	if _, err := update(t, b, g.Ref); err != nil {
		t.Fatalf("next update: %v", err)
	}

	wantRx(t, c.take(),
		rxSent{rx.CommandAA, pcrf1, pcrf1},
		rxSent{rx.CommandAA, "", pcrf2},
	)

	c.set(func(c *pcrfCluster) { c.gone[pcrf1] = false })

	ref = openCall(t, b)

	c.set(func(c *pcrfCluster) { c.gone[pcrf1] = true })

	if err := b.Terminate(t.Context(), "call", ref, policy.TerminationLogout, false); err != nil {
		t.Fatalf("Terminate = %v, want it to succeed", err)
	}
}

// RFC 6733 §8.20: a PCRF that takes a session over does not get the Class of the PCRF it took it from.
func TestRebindingDropsTheClass(t *testing.T) {
	c, b := newPCRFCluster(0, diameter.TryAgain)
	c.set(func(c *pcrfCluster) { c.class[pcrf1] = "pcrf1-state" })

	ref := openCall(t, b)

	if s, err := decodeRef(ref); err != nil || len(s.Class) != 1 {
		t.Fatalf("session = %+v, %v; want the Class of %s", s, err, pcrf1)
	}

	g, err := update(t, b, ref)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if s, err := decodeRef(g.Ref); err != nil || len(s.Class) != 1 {
		t.Fatalf("session = %+v, %v; want the Class kept by the same PCRF", s, err)
	}

	c.set(func(c *pcrfCluster) { c.gone[pcrf1] = true })

	if g, err = update(t, b, g.Ref); err != nil {
		t.Fatalf("update: %v", err)
	}

	if s, err := decodeRef(g.Ref); err != nil || s.PCRF != pcrf2 || len(s.Class) != 0 {
		t.Fatalf("session = %+v, %v; want it on %s without the Class of %s", s, err, pcrf2, pcrf1)
	}
}

// TS 29.213 §7.3.5: a PCRF behind an agent, in a realm the P-CSCF has no route for, is still reached through the
// configured realm.
func TestPCRFBehindAnAgentInAnotherRealm(t *testing.T) {
	const realm = "pcrf.example.net"

	c, b := newPCRFCluster(0, diameter.RefuseService)
	c.set(func(c *pcrfCluster) { c.behind[pcrf1] = realm })

	ref := openCall(t, b)

	if err := b.Terminate(t.Context(), "call", ref, policy.TerminationLogout, false); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	wantRx(t, c.take(),
		rxSent{rx.CommandAA, "", pcrf1},
		rxSent{rx.CommandSessionTermination, pcrf1, ""},
		rxSent{rx.CommandSessionTermination, pcrf1, pcrf1},
	)
}

func TestSignallingSessionStaysOnItsPCRF(t *testing.T) {
	c, b := newPCRFCluster(0, diameter.RefuseService)

	ref, err := b.OpenSignalling(t.Context(), "signalling", policy.Signalling{UE: ue}, false)
	if err != nil {
		t.Fatalf("OpenSignalling: %v", err)
	}

	c.set(func(c *pcrfCluster) { c.nodes = []string{pcrf2, pcrf1} })

	if err := b.Terminate(t.Context(), "signalling", ref, policy.TerminationLogout, false); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	wantRx(t, c.take(),
		rxSent{rx.CommandAA, "", pcrf1},
		rxSent{rx.CommandSessionTermination, pcrf1, pcrf1},
	)
}
