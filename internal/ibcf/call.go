package ibcf

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/ua"
	"go.opentelemetry.io/otel/trace"
)

type call struct {
	b        *IBCF
	outbound bool
	initial  *sip.Request
	in       *ua.Request
	icid     string
	cause    trace.SpanContext
	route    *route
	exit     exit

	abandoned chan struct{}

	keys []key

	mu      sync.Mutex
	trunk   settings.Trunk
	signal  netip.Addr
	media   *media
	callID  string
	fromTag string
	ended   bool
	metered string
	outcome outcome
}

type outcome struct {
	alerted   bool
	settled   bool
	rejected  int
	tag       string
	cancelled bool
	bye       proxy.Side
	network   bool
}

type exit struct {
	number    sip.URI
	caller    sip.Address
	asserted  sip.Address
	forwarder *forwarder
	privacy   sip.Privacy
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
		Headers:  b2bua.Headers{Sticky: []string{}, Field: c.crosses, Part: crossing},
		TimerC:   timerC,
		Request:  c.request,
		Response: c.response,
		Final:    c.final,
	}

	if c.media != nil {
		cfg.Media = c.media
	}

	if c.outbound {
		cfg.FailoverBudget = failoverBudget
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
	c.abandon()
	done := c.ended
	c.ended = true
	metered := c.metered
	c.metered = ""
	o := c.outcome
	c.mu.Unlock()

	if done {
		return
	}

	if c.media != nil {
		c.media.close()
	}

	b.legs.remove(c)

	e := o.end(c.status())

	if metered != "" {
		var result db.CallOutcome
		if e.Code >= 200 {
			result = callrecords.Outcome(e.Code, callrecords.EndedBy(e.Cause, e.By), o.alerted)
		}

		b.cfg.Metrics.leave(metered, c.direction(), result)
	}

	if !c.outbound {
		b.cfg.Records.Ended(c.icid, e)
	}
}

func (c *call) status() int {
	if c.in == nil {
		return 0
	}

	return c.in.Status()
}

func (o outcome) end(code int) callrecords.End {
	e := callrecords.End{Code: code, Cause: proxy.EndReleased}

	switch {
	case code >= 200 && code < 300 && o.bye != 0 && !o.network:
		e.By, e.Cause = o.bye, proxy.EndBye
	case code < 300:
	case o.cancelled:
		e.By, e.Cause = proxy.Caller, proxy.EndFailed
	case o.rejected == code:
		e.By, e.Cause, e.Tag = proxy.Callee, proxy.EndFailed, o.tag
	}

	return e
}

func (c *call) direction() string {
	if c.outbound {
		return outbound
	}

	return inbound
}

func (c *call) own(t settings.Trunk) {
	c.mu.Lock()
	prev := c.metered
	ended := c.ended
	c.metered = t.ID
	c.mu.Unlock()

	if ended || prev == t.ID {
		return
	}

	if prev != "" {
		c.b.cfg.Metrics.leave(prev, c.direction(), "")
	}

	c.b.cfg.Metrics.enter(t.ID, c.direction())

	if c.outbound {
		c.b.cfg.Records.Trunk(c.icid, callrecords.Outbound, t.ID, t.Name)
	}
}

func (c *call) ending(fromTrunk bool, method string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	caller := fromTrunk != c.outbound

	switch {
	case method == "CANCEL":
		if caller && !c.outcome.cancelled {
			c.abandon()
		}

		c.outcome.cancelled = c.outcome.cancelled || caller
	case c.outcome.bye == 0 && !c.outcome.network:
		c.outcome.bye = proxy.Callee
		if caller {
			c.outcome.bye = proxy.Caller
		}
	}
}

func (c *call) cancelled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.outcome.cancelled
}

// RFC 3261 section 9.2
func (c *call) final(res *sip.Response, err error) ua.Reply {
	if c.cancelled() {
		return ua.Reply{Code: 487}
	}

	return final(res, err)
}

// RFC 3261 section 9.2
// RFC 3261 section 17.2.3
func (c *call) cancels(cancel *sip.Request) bool {
	via, err := cancel.Header.TopVia()
	if err != nil || via.Branch() == "" {
		return false
	}

	invite, err := c.initial.Header.TopVia()

	return err == nil && via.Branch() == invite.Branch() && strings.EqualFold(via.Host, invite.Host) && sentByPort(via) == sentByPort(invite)
}

func sentByPort(v sip.Via) uint16 {
	if v.Port == 0 {
		return sip.DefaultPortFor(v.Transport)
	}

	return v.Port
}

func (c *call) abandon() {
	if c.abandoned != nil && !c.ended && !c.outcome.cancelled {
		close(c.abandoned)
	}
}

func (c *call) released() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.outcome.bye == 0 {
		c.outcome.network = true
	}
}

func (c *call) progressed(res *sip.Response, code int) {
	c.mu.Lock()

	if c.outcome.settled {
		c.mu.Unlock()
		return
	}

	alerting := code == 180 && !c.outcome.alerted
	c.outcome.alerted = c.outcome.alerted || code == 180
	c.outcome.settled = code >= 200

	if code >= 300 {
		to, _ := res.Header.To()
		c.outcome.rejected, c.outcome.tag = code, to.Tag()
	}

	c.mu.Unlock()

	switch {
	case c.outbound:
	case alerting:
		c.b.cfg.Records.Alerted(c.icid)
	case code/100 == 2:
		c.b.cfg.Records.Answered(c.icid, code)

		if _, offered, _ := c.initial.Content().FindFunc(sip.BodyPart.IsSession); !offered {
			return
		}

		if part, ok, _ := res.Content().FindFunc(sip.BodyPart.IsSession); ok {
			c.b.cfg.Records.Media(c.icid, callrecords.AcceptedMedia(part.Body))
		}
	}
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

// RFC 3261 section 20
// RFC 3323 section 4.2
// RFC 6086 section 7
// SIPconnect 2.0 section 10.1.4
var fromTrunk = []string{
	"privacy", "accept", "accept-encoding", "accept-language", "reason", "session-id", "recv-info", "info-package",
	"retry-after", "subject", "s", "priority", "warning",
}

// TS 24.229 clause 4.4.1
// TS 24.229 clause 4.4.2
// TS 24.229 clause 4.4.11
// TS 24.229 clause 5.10.3.2
var fromTrustedTrunk = []string{"p-asserted-identity", "p-charging-vector", "p-early-media"}

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
	case name == "privacy" && c.outbound && m == sip.Message(c.initial):
		return false
	case !c.towardsTrunk(to):
		return c.fromTrunk(name, trusted)
	case !slices.Contains(towardsTrunk, name):
		return false
	}

	switch name {
	case "p-asserted-identity":
		return trusted || !c.withheld(m)
	case "p-early-media":
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

// TS 24.229 clause 5.10.6.3
var crossingBodies = []string{"application/dtmf-relay", "application/dtmf"}

const accepted = "application/sdp, application/dtmf-relay, application/dtmf"

var errBody = errors.New("ibcf: required message body refused at the border")

// TS 24.229 clause 5.10.6.3
// TS 29.162 clause 9.1.4
func crossing(_, _ *b2bua.Leg, _ sip.Message, p sip.BodyPart) bool {
	return slices.Contains(crossingBodies, p.MediaType())
}

// TS 24.229 clause 5.10.6.3
// RFC 3261 section 8.2.3
// RFC 3261 section 21.4.13
// RFC 5621 section 8.4
func screen(req *sip.Request) error {
	if req.Method == "ACK" || req.Method == "CANCEL" {
		return nil
	}

	parts, err := req.Content().Split()

	switch {
	case errors.Is(err, sip.ErrUnsupportedEncoding):
		return unsupportedEncoding(err)
	case err != nil:
		return &sip.StatusError{StatusCode: 400, Err: err}
	}

	for _, p := range parts {
		switch err := p.Encoding(); {
		case p.Optional():
		case errors.Is(err, sip.ErrUnsupportedEncoding):
			return unsupportedEncoding(err)
		case err != nil:
			return &sip.StatusError{StatusCode: 400, Err: err}
		case !p.IsSession() && !crossing(nil, nil, nil, p):
			return &sip.StatusError{StatusCode: 415, Header: sip.Header{{Name: "Accept", Value: accepted}}, Err: errBody}
		}
	}

	return nil
}

// RFC 3261 section 8.2.3
func unsupportedEncoding(err error) error {
	return &sip.StatusError{StatusCode: 415, Header: sip.Header{{Name: "Accept-Encoding", Value: "identity"}}, Err: err}
}

func (c *call) fromTrunk(name string, trusted bool) bool {
	return slices.Contains(fromTrunk, name) || trusted && slices.Contains(fromTrustedTrunk, name)
}

// RFC 3325 section 5
// RFC 3325 section 7
func (c *call) withheld(m sip.Message) bool {
	privacy, err := m.Env().Header.Privacy()

	switch {
	case err != nil:
		return true
	case len(privacy) > 0:
		return withholds(privacy)
	}

	return c.outbound && withholds(c.exit.privacy)
}

// RFC 3325 section 7
// TS 24.607 clause 4.5.2.1
// TS 24.607 clause 4.5.2.9
func withholds(p sip.Privacy) bool {
	return (p.Has(sip.PrivacyID) || p.Has(sip.PrivacyHeader)) && !p.Has(sip.PrivacyNone)
}

// TS 24.229 clause 4.5.2
func (c *call) request(_, to *b2bua.Leg, _ *sip.Request, ch *ua.Change) {
	if !c.towardsTrunk(to) {
		charge(&ch.Header, c.icid)
	}
}

// TS 24.229 clause 4.5.2
func charge(h *sip.Header, icid string) {
	if icid == "" {
		return
	}

	ps := sip.Params{{Name: "icid-value", Value: icid}}

	if received, err := sip.ParseParams(h.Get("P-Charging-Vector")); err == nil {
		for _, p := range received {
			if !strings.EqualFold(p.Name, "icid-value") {
				ps = append(ps, p)
			}
		}
	}

	h.Set("P-Charging-Vector", strings.TrimPrefix(ps.String(), ";"))
}

// TS 24.229 clause 4.5.2
// RFC 3261 section 12.1.1
func (c *call) response(_, to *b2bua.Leg, res *sip.Response, rp *ua.Reply) {
	if !c.towardsTrunk(to) {
		charge(&rp.Header, c.icid)
	}

	cseq, err := res.Header.CSeq()
	if err == nil && c.media != nil && cseq.Method == "INVITE" && rp.Code/100 == 2 {
		c.media.answer()
	}

	if err == nil && cseq.Method == "INVITE" && !to.Outgoing() && rp.Code > 100 {
		c.progressed(res, rp.Code)
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
