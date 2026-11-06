package ibcf

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/ua"
)

type call struct {
	b        *IBCF
	outbound bool
	initial  *sip.Request
	icid     string
	route    *route
	exit     exit

	keys []key

	mu      sync.Mutex
	trunk   settings.Trunk
	signal  netip.Addr
	media   *media
	callID  string
	fromTag string
	ended   bool
}

type exit struct {
	number  sip.URI
	caller  sip.Address
	privacy sip.Privacy
}

func (c *call) owner() (settings.Trunk, []netip.Addr) {
	c.mu.Lock()
	t := c.trunk
	c.mu.Unlock()

	if c.route == nil {
		return t, nil
	}

	return t, c.route.addresses(t.ID)
}

func (c *call) trunkFamily() netip.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.signal
}

func (c *call) trusted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.trunk.Trusted
}

func (c *call) isEnded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.ended
}

func (c *call) towardsTrunk(to *b2bua.Leg) bool {
	return to.Outgoing() == c.outbound
}

func (b *IBCF) relay(c *call) b2bua.RelayConfig {
	cfg := b2bua.RelayConfig{
		Headers:  b2bua.Headers{Sticky: []string{}, Field: c.crosses},
		TimerC:   timerC,
		Request:  c.request,
		Response: c.response,
		Final:    final,
	}

	if c.media != nil {
		cfg.Media = c.media
	}

	if c.outbound {
		cfg.Failover = func(ctx context.Context, _ *b2bua.Leg, f b2bua.Failure) *sip.Request { return b.failover(ctx, c, f) }
	}

	return cfg
}

func (b *IBCF) begin(r *ua.Request, c *call) *b2bua.Call {
	return b2bua.NewCall(b.agent, r.Strand(), b2bua.CallHandler{
		Ended: func(context.Context, *b2bua.Call) { b.release(c) },
		Error: func(_ *b2bua.Call, err error) { b.log.Debug("call leg error", slog.Any("error", err)) },
	})
}

func (b *IBCF) release(c *call) {
	c.mu.Lock()
	c.ended = true
	c.mu.Unlock()

	if c.media != nil {
		c.media.close()
	}

	b.legs.remove(c)
}

// RFC 3261 section 8.1.3.5
// RFC 3261 section 16.7
func final(res *sip.Response, err error) ua.Reply {
	if res != nil && (res.StatusCode == 420 || res.StatusCode == 421) {
		return ua.Reply{Code: 500}
	}

	return b2bua.FinalFor(res, err)
}

// SIPconnect 2.0 section 10.2
// SIPconnect 2.0 section 14.9
// RFC 3261 section 20
// RFC 3323 section 4.2
// RFC 6086 section 7
var towardsTrunk = []string{
	"privacy", "accept", "accept-encoding", "accept-language", "reason", "session-id", "p-early-media",
	"recv-info", "info-package", "retry-after", "subject", "s", "priority", "p-asserted-identity",
}

// TS 24.229 clause 4.4
// TS 24.229 clause 5.10.3.2
var fromTrunk = []string{
	"p-charging-function-addresses", "p-visited-network-id", "p-private-network-indication", "p-served-user",
	"p-profile-key", "p-called-party-id", "remote-party-id", "p-charge-info",
}

// TS 24.229 clause 4.4
// TS 24.229 clause 4.4.7
// TS 24.229 clause 4.4.14
// TS 24.229 clause 5.10.2.2
// TS 24.229 clause 5.10.3.2
// TS 24.229 clause 5.10.3.3
// TS 24.229 clause 5.10.8
// RFC 3323 section 5.3
// RFC 3325 section 7
func (c *call) crosses(_, to *b2bua.Leg, m sip.Message, f sip.Field) bool {
	name := strings.ToLower(f.Name)
	trusted := c.trusted()

	switch {
	case name == "priority" && strings.EqualFold(strings.TrimSpace(f.Value), "psap-callback"):
		return false
	case name == "p-asserted-identity" && m == sip.Message(c.initial):
		return false
	case !c.towardsTrunk(to):
		return c.fromTrunk(name, trusted)
	case !slices.Contains(towardsTrunk, name):
		return false
	}

	switch name {
	case "p-asserted-identity":
		return trusted
	case "reason":
		_, response := m.(*sip.Response)
		return trusted || !response
	case "subject", "s":
		privacy, err := m.Env().Header.Privacy()
		return err == nil && (privacy.Has(sip.PrivacyNone) || !privacy.Has(sip.PrivacyUser))
	}

	return true
}

func (c *call) fromTrunk(name string, trusted bool) bool {
	switch {
	case slices.Contains(fromTrunk, name):
		return false
	case name == "p-charging-vector", name == "p-asserted-identity":
		return trusted
	}

	return trusted || !trust.Boundary(name)
}

// TS 24.229 clause 4.5.2
func (c *call) request(_, to *b2bua.Leg, _ *sip.Request, ch *ua.Change) {
	if !c.towardsTrunk(to) && c.icid != "" && !ch.Header.Has("P-Charging-Vector") {
		ch.Header.Add("P-Charging-Vector", "icid-value="+c.icid)
	}
}

// TS 24.229 clause 4.5.2
// RFC 3261 section 12.1.1
func (c *call) response(_, to *b2bua.Leg, res *sip.Response, rp *ua.Reply) {
	if !c.towardsTrunk(to) && c.icid != "" && !rp.Header.Has("P-Charging-Vector") {
		rp.Header.Add("P-Charging-Vector", "icid-value="+c.icid)
	}

	cseq, err := res.Header.CSeq()
	if err == nil && c.media != nil && cseq.Method == "INVITE" && rp.Code/100 == 2 {
		c.media.answer()
	}

	if to.Outgoing() || err != nil || cseq.Method != "INVITE" && cseq.Method != "UPDATE" || rp.Code <= 100 || rp.Code >= 300 {
		return
	}

	in := to.Request().Message()
	rp.Header.Set("Contact", c.b.contact(in.Flow.Local, in.Flow.Transport).String())
}

func icidOf(h sip.Header) string {
	ps, err := sip.ParseParams(h.Get("P-Charging-Vector"))
	if err != nil {
		return ""
	}

	icid, _ := ps.Get("icid-value")

	return icid
}
