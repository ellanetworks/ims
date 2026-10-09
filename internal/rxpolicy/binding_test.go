package rxpolicy

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"testing"

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
	mu       sync.Mutex
	nodes    []string
	down     map[string]bool
	busy     map[string]bool
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
		nodes: []string{pcrf1, pcrf2}, down: map[string]bool{}, busy: map[string]bool{}, binding: binding, failover: failover,
	}

	return c, New(Config{Diameter: c, Realm: func() string { return pcrfRealm }})
}

func (c *pcrfCluster) Identity() diameter.Identity {
	return diameter.Identity{OriginHost: "ims.ims.example.org", OriginRealm: "ims.example.org"}
}

func (c *pcrfCluster) NewSessionID() string { return "ims.ims.example.org;1;1" }

func (c *pcrfCluster) Send(_ context.Context, req *diameter.Message, _ ...diameter.RequestOption) (*diameter.Message, error) {
	to := tgpp.ParseEnvelope(req).DestinationHost

	c.mu.Lock()

	by := to
	if to == "" {
		for _, n := range c.nodes {
			if !c.down[n] {
				by = n
				break
			}
		}
	}

	down, busy := by == "" || c.down[by], c.busy[by]
	binding, failover := c.binding, c.failover
	c.sent = append(c.sent, rxSent{command: req.CommandCode, to: to, by: by})
	c.mu.Unlock()

	id := diameter.Identity{OriginHost: by, OriginRealm: pcrfRealm}

	switch {
	case down:
		return nil, diameter.ErrNotConnected
	case busy:
		return diameter.NewAnswer(req, id, diameter.ResultTooBusy), nil
	}

	var (
		ans *diameter.Message
		err error
	)

	switch req.CommandCode {
	case rx.CommandAA:
		ans, err = rx.NewAAAnswer(req, id, rx.AAAnswer{
			Result: tgpp.Result{Code: diameter.ResultSuccess}, SessionBinding: binding, SessionServerFailover: failover,
		})
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

// RFC 6733 §8.18 REFUSE_SERVICE, the default: a session whose PCRF is lost is lost, without another attempt.
func TestLostPCRFRefusesService(t *testing.T) {
	for name, lose := range map[string]func(c *pcrfCluster){
		"down":     func(c *pcrfCluster) { c.down[pcrf1] = true },
		"too busy": func(c *pcrfCluster) { c.busy[pcrf1] = true },
	} {
		t.Run(name, func(t *testing.T) {
			c, b := newPCRFCluster(0, diameter.RefuseService)

			ref := openCall(t, b)

			c.take()
			c.set(lose)

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

	if _, err := update(t, b, g.Ref); !errors.Is(err, policy.ErrSessionLost) {
		t.Fatalf("update with every PCRF lost = %v, want the session lost", err)
	}
}

// RFC 6733 §8.18 ALLOW_SERVICE: the session goes on, unbound: the next request goes by realm.
func TestLostPCRFAllowsService(t *testing.T) {
	c, b := newPCRFCluster(0, diameter.AllowService)

	ref := openCall(t, b)

	c.take()
	c.set(func(c *pcrfCluster) { c.down[pcrf1] = true })

	g, err := update(t, b, ref)
	if !errors.Is(err, policy.ErrUnreachable) || errors.Is(err, policy.ErrSessionLost) || policy.Transient(err) || g.Ref == "" {
		t.Fatalf("update = %+v, %v; want the service kept, unbound", g, err)
	}

	if _, err := update(t, b, g.Ref); err != nil {
		t.Fatalf("next update: %v", err)
	}

	wantRx(t, c.take(),
		rxSent{rx.CommandAA, pcrf1, pcrf1},
		rxSent{rx.CommandAA, "", pcrf2},
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
