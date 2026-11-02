package pcscf

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/internal/policy"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel/trace"
)

const (
	DefaultPolicyCallTimeout = 3 * time.Second

	DefaultMediaLossTimeout = 5 * time.Second
)

var (
	errCallEnded = errors.New("the call has ended")

	errRetryInterval = errors.New("the same service information was refused and its retry interval has not elapsed")

	errAARPending = errors.New("the previous media authorization is not answered")

	errAARTimeout = errors.New("no media authorization answer in time")
)

type callPolicy struct {
	key        regKey
	identities []string
	service    string

	call   *call
	dialog *proxy.Dialog

	mu         sync.Mutex
	session    *policySession
	busy       bool
	queue      []func()
	done       map[int]bool
	early      map[string]bool
	forked     bool
	flows      map[int]flowNumbers
	active     map[uint32]policy.MediaType
	components []policy.MediaComponent
	priority   *policy.MCPTTPriority
	granted    *policy.MCPTTPriority
	charging   string
	access     policy.Access
	loss       transaction.Timer
	lost       []uint32
	ended      bool
	orphaned   atomic.Bool
}

func (p *PCSCF) newCallPolicy(k regKey, identities []string, service string) *callPolicy {
	if p.policy == nil {
		return nil
	}

	return &callPolicy{
		key: k, identities: identities, service: service,
		done: make(map[int]bool), early: make(map[string]bool), flows: make(map[int]flowNumbers), active: make(map[uint32]policy.MediaType),
	}
}

func (cr *callPolicy) attach(c *call, d *proxy.Dialog) {
	if cr != nil {
		cr.call, cr.dialog = c, d
	}
}

func (cr *callPolicy) end() *policySession {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	cr.ended = true

	if cr.loss != nil {
		cr.loss.Stop()
		cr.loss = nil
	}

	return cr.session
}

type answerJob struct {
	x       *sdpExchange
	tag     string
	early   bool
	initial bool
	final   bool
}

// TS 29.214 clause A.1
// TS 29.214 clause A.3
// TS 29.214 clause A.13
// TS 29.213 clause B.2
// TS 29.513 clause 7.2.3
func (p *PCSCF) mediaReply(ctx context.Context, tx *transaction.ServerTransaction, d *proxy.Dialog, rep proxy.Reply, initial, priority bool) proxy.Verdict {
	c := callOf(d)
	if c == nil || c.policy == nil || rep.Response == nil {
		return proxy.Relay
	}

	res := rep.Response

	var job *answerJob
	if rep.Err == nil {
		job = p.responseAnswer(ctx, c, d, res, initial)
	}

	run := func() {
		if job != nil && !p.authorize(ctx, c, d, *job, res) {
			return
		}

		if initial && res.IsSuccess() && d.Released() {
			res = sip.NewResponse(tx.Request(), 500, "")
		}

		p.chargeUEResponse(c, res)

		if err := p.cfg.Proxy.Relay(ctx, tx, res); err != nil {
			p.log.DebugContext(ctx, "relaying a held response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}

		if job == nil && priority {
			p.updatePriority(ctx, c, d)
		}

		p.releaseOrphaned(ctx, c, d)
	}

	if p.enqueue(c.policy, job != nil || priority, run) {
		return proxy.Hold
	}

	p.chargeUEResponse(c, res)

	return proxy.Relay
}

// TS 24.229 clause 5.2.7.3
func (p *PCSCF) chargeUEResponse(c *call, res *sip.Response) {
	if c.ue == proxy.Callee && reliable(res) {
		chargeResponse(c, res)
	}
}

// RFC 3262 section 5
// RFC 3264
// TS 29.214 clause A.1
// TS 29.214 clause A.13
func (p *PCSCF) mediaRequest(ctx context.Context, d *proxy.Dialog, out *sip.Request, priority bool, forward, reject func()) bool {
	c := callOf(d)
	if c == nil || c.policy == nil {
		return false
	}

	_, answers := d.PendingOffer(out)

	return p.enqueue(c.policy, answers || priority, func() {
		offer, ok := d.PendingOffer(out)
		if !ok {
			forward()

			if priority {
				p.updatePriority(ctx, c, d)
				p.releaseOrphaned(ctx, c, d)
			}

			return
		}

		answer, _, _ := out.Content().FindFunc(sip.BodyPart.IsSession)

		x := p.parseExchange(ctx, c, d, proxy.Exchange{Offer: offer, Answer: proxy.Body{From: other(offer.From), Data: answer.Body}})
		if x == nil {
			forward()

			if priority {
				p.updatePriority(ctx, c, d)
				p.releaseOrphaned(ctx, c, d)
			}

			return
		}

		state := d.State()
		job := answerJob{
			x: x, tag: calleeTag(d, out), early: state == proxy.Early,
			initial: state == proxy.Early || out.Method == "ACK" && state == proxy.Answered,
		}

		switch {
		case out.Method == "ACK":
			forward()
			p.authorize(ctx, c, d, job, nil)
		case p.authorize(ctx, c, d, job, nil):
			forward()
		case reject != nil:
			reject()
		}

		p.releaseOrphaned(ctx, c, d)

		if ex, ok := d.Exchange(out); ok {
			c.policy.mu.Lock()
			c.policy.done[ex.Seq] = true
			c.policy.mu.Unlock()
		}
	})
}

func other(s proxy.Side) proxy.Side {
	if s == proxy.Caller {
		return proxy.Callee
	}

	return proxy.Caller
}

func calleeTag(d *proxy.Dialog, m sip.Message) string {
	from, _ := m.Env().Header.From()
	to, _ := m.Env().Header.To()

	if from.Tag() == d.CallerTag() {
		return to.Tag()
	}

	return from.Tag()
}

func (p *PCSCF) enqueue(cr *callPolicy, needed bool, run func()) bool {
	cr.mu.Lock()

	if cr.busy {
		cr.queue = append(cr.queue, run)
		cr.mu.Unlock()

		return true
	}

	if !needed {
		cr.mu.Unlock()
		return false
	}

	cr.busy = true
	cr.mu.Unlock()

	if !p.policy.spawn(func() { p.drain(cr, run) }) {
		cr.mu.Lock()
		cr.busy = false
		cr.mu.Unlock()

		return false
	}

	return true
}

func (p *PCSCF) drain(cr *callPolicy, run func()) {
	for {
		run()

		cr.mu.Lock()

		if len(cr.queue) == 0 {
			cr.busy = false
			cr.mu.Unlock()

			return
		}

		run = cr.queue[0]
		cr.queue = cr.queue[1:]

		cr.mu.Unlock()
	}
}

// TS 29.214 clause A.1
// TS 29.214 clause A.3
func (p *PCSCF) responseAnswer(ctx context.Context, c *call, d *proxy.Dialog, res *sip.Response, initial bool) *answerJob {
	if res.StatusCode <= 100 || res.StatusCode >= 300 {
		return nil
	}

	ex, ok := d.Exchange(res)
	if !ok {
		return nil
	}

	final := initial && res.IsSuccess()
	early := !final && d.State() == proxy.Early

	_, answered, _ := res.Content().FindFunc(sip.BodyPart.IsSession)

	cr := c.policy

	cr.mu.Lock()

	fresh := answered && !cr.done[ex.Seq]
	needed := fresh || final && cr.forked

	if needed {
		cr.done[ex.Seq] = true
	}

	cr.mu.Unlock()

	if !needed {
		return nil
	}

	x := p.parseExchange(ctx, c, d, ex)
	if x == nil {
		return nil
	}

	return &answerJob{x: x, tag: calleeTag(d, res), early: early, initial: initial || early, final: final}
}

func (p *PCSCF) parseExchange(ctx context.Context, c *call, d *proxy.Dialog, ex proxy.Exchange) *sdpExchange {
	o, err := sdp.Parse(ex.Offer.Data)
	if err != nil {
		p.log.WarnContext(ctx, "unusable SDP offer", slog.String("dialog", d.ID()), slog.Any("error", err))
		return nil
	}

	a, err := sdp.Parse(ex.Answer.Data)
	if err != nil {
		p.log.WarnContext(ctx, "unusable SDP answer", slog.String("dialog", d.ID()), slog.Any("error", err))
		return nil
	}

	return &sdpExchange{offer: o, answer: a, offerFromUE: ex.Offer.From == c.ue}
}

// TS 24.229 clause 5.2.7.2
// TS 29.214 clause 4.4.1
// TS 29.514 clause 4.2.2.2
func (p *PCSCF) authorize(ctx context.Context, c *call, d *proxy.Dialog, job answerJob, res *sip.Response) bool {
	err := p.callAAR(ctx, c, d, &job)
	if err == nil {
		if res != nil {
			p.chargeUEResponse(c, res)
		}

		return true
	}

	if errors.Is(err, errCallEnded) {
		return true
	}

	attrs := []any{
		slog.String("dialog", d.ID()), slog.String("impi", c.policy.key.impi), slog.String("ue", c.policy.key.ue.String()),
		slog.Any("error", err),
	}

	if result, ok := policy.ResultOf(err); ok {
		attrs = append(attrs, slog.String("result", result))
	}

	if removed := job.x.removed(); len(removed) > 0 {
		attrs = append(attrs, slog.Any("removed_media", removed))
	}

	if errors.Is(err, policy.ErrSessionLost) {
		p.log.WarnContext(ctx, "policy session of the call lost with its policy function: releasing the call", attrs...)
		c.policy.orphaned.Store(true)

		if job.initial {
			p.releaseOrphaned(ctx, c, d)
			return false
		}

		return true
	}

	if !job.initial {
		p.log.WarnContext(ctx, "media authorization refused for a session modification", attrs...)
		return true
	}

	p.log.WarnContext(ctx, "media authorization refused: releasing the call", attrs...)

	if err := d.Release(ctx, proxy.Release{Toward: proxy.Both, Code: 500}); err != nil {
		p.log.DebugContext(ctx, "releasing the call failed", slog.String("dialog", d.ID()), slog.Any("error", err))
	}

	return false
}

func (p *PCSCF) releaseOrphaned(ctx context.Context, c *call, d *proxy.Dialog) {
	if !c.policy.orphaned.Load() {
		return
	}

	cause, _ := sip.NewReason("SIP", 503, "")

	if err := d.Release(ctx, proxy.Release{Toward: proxy.Both, Code: 500, Reason: []sip.Reason{cause}}); err != nil &&
		!errors.Is(err, proxy.ErrDialogEnded) {
		p.log.DebugContext(ctx, "releasing the call failed", slog.String("dialog", d.ID()), slog.Any("error", err))
	}
}

// RFC 3262 section 3
func reliable(res *sip.Response) bool {
	return res.IsSuccess() || res.Header.Has("RSeq")
}

type grantResult struct {
	grant policy.Grant
	err   error
}

type handoff struct {
	mu        sync.Mutex
	c         chan grantResult
	abandoned bool
}

func (h *handoff) give(r grantResult) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.abandoned {
		return false
	}

	h.c <- r

	return true
}

func (h *handoff) abandon() (grantResult, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	select {
	case r := <-h.c:
		return r, true
	default:
		h.abandoned = true
		return grantResult{}, false
	}
}

// TS 29.214 clause 4.4.1
// TS 29.214 clause 4.4.2
// TS 29.214 clause 4.4.4
// TS 29.214 clause A.3
// TS 29.514 clause 4.2.2.2
// TS 29.514 clause 4.2.3.2
func (p *PCSCF) callAAR(ctx context.Context, c *call, d *proxy.Dialog, job *answerJob) error {
	return p.callAARBy(ctx, c, d, job, time.Now().Add(p.policy.cfg.CallTimeout), false)
}

// TS 24.229 clause 5.2.9.1
// TS 24.229 clause 5.2.9.2
// TS 29.214 clause A.13.1
// TS 29.514 clause B.10.1
func (p *PCSCF) updatePriority(ctx context.Context, c *call, d *proxy.Dialog) {
	err := p.callAAR(ctx, c, d, nil)
	if err == nil || errors.Is(err, errCallEnded) {
		return
	}

	attrs := []any{
		slog.String("dialog", d.ID()), slog.String("impi", c.policy.key.impi), slog.String("ue", c.policy.key.ue.String()),
		slog.Any("error", err),
	}

	if result, ok := policy.ResultOf(err); ok {
		attrs = append(attrs, slog.String("result", result))
	}

	if errors.Is(err, policy.ErrSessionLost) {
		p.log.WarnContext(ctx, "policy session of the call lost with its policy function: releasing the call", attrs...)
		c.policy.orphaned.Store(true)

		return
	}

	p.log.WarnContext(ctx, "MCPTT priority update failed", attrs...)
}

func (p *PCSCF) callAARBy(ctx context.Context, c *call, d *proxy.Dialog, job *answerJob, deadline time.Time, recovery bool) error {
	cr := c.policy

	cr.mu.Lock()

	if cr.ended || cr.orphaned.Load() {
		cr.mu.Unlock()
		return errCallEnded
	}

	if job == nil && (len(cr.components) == 0 || !recovery && (cr.session == nil || samePriority(cr.granted, cr.priority))) {
		cr.mu.Unlock()
		return nil
	}

	flows := make(map[int]flowNumbers, len(cr.flows))
	for k, v := range cr.flows {
		flows[k] = v
	}

	s := cr.session
	if s == nil {
		s = p.policy.callSession(cr)
		cr.session = s
	}

	fork := policy.ForkingSingleDialogue

	switch {
	case job == nil:
		if cr.forked {
			fork = policy.ForkingSeveralDialogues
		}
	case job.early && job.tag != "" && (cr.forked || len(cr.early) > 0 && !cr.early[job.tag]):
		fork = policy.ForkingSeveralDialogues
	}

	service, priority, components := cr.service, cr.priority, slices.Clone(cr.components)

	cr.mu.Unlock()

	if job != nil {
		var err error

		if components, err = mediaComponents(*job.x, flows); err != nil {
			return err
		}
	}

	if recovery {
		components = slices.DeleteFunc(components, func(m policy.MediaComponent) bool { return m.Status == policy.FlowRemoved })
	}

	if !s.pending.CompareAndSwap(false, true) {
		return errAARPending
	}

	s.mu.Lock()

	if s.ended {
		s.mu.Unlock()
		s.pending.Store(false)

		return errCallEnded
	}

	initial := !s.opened

	if job == nil && initial && !recovery {
		s.mu.Unlock()
		s.pending.Store(false)

		return nil
	}

	r := policy.Request{
		UE:          cr.key.ue,
		Initial:     initial,
		Service:     service,
		Components:  components,
		Subscribers: subscribers(cr.identities),
		Forking:     fork,
		MCPTT:       priority,
	}

	result := &handoff{c: make(chan grantResult, 1)}

	if !p.policy.spawn(func() {
		g, err := p.policy.authorize(ctx, s, r)

		if g.Ref != "" {
			s.ref = g.Ref
		}

		switch {
		case err == nil:
			s.opened = true
		case initial, errors.Is(err, policy.ErrUnknownSession), errors.Is(err, policy.ErrSessionLost):
			s.ended = true
			p.policy.forget(s)

			cr.mu.Lock()
			if cr.session == s {
				cr.session = nil
			}
			cr.mu.Unlock()
		}

		s.mu.Unlock()
		s.pending.Store(false)

		if !result.give(grantResult{g, err}) && errors.Is(err, policy.ErrSessionLost) {
			cr.orphaned.Store(true)
			p.releaseOrphaned(ctx, c, d)
		}
	}) {
		s.mu.Unlock()
		s.pending.Store(false)

		return errCallEnded
	}

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	var res grantResult

	select {
	case res = <-result.c:
	case <-timer.C:
		var ok bool
		if res, ok = result.abandon(); !ok {
			return errAARTimeout
		}
	}

	if res.err != nil {
		if errors.Is(res.err, policy.ErrUnknownSession) && !initial {
			p.log.WarnContext(ctx, "policy session of a call unknown to the policy function: opening a new one",
				slog.String("dialog", d.ID()), slog.String("impi", cr.key.impi), slog.String("session", s.id))

			return p.callAARBy(ctx, c, d, job, deadline, true)
		}

		return res.err
	}

	cr.mu.Lock()

	if job != nil && job.early && job.tag != "" {
		cr.early[job.tag] = true
		cr.forked = cr.forked || len(cr.early) > 1
	}

	if job != nil && job.final {
		cr.forked = false
	}

	cr.flows = flows
	cr.active = activeComponents(components)
	cr.components = slices.DeleteFunc(slices.Clone(components), func(m policy.MediaComponent) bool { return m.Status == policy.FlowRemoved })
	cr.granted = priority
	cr.chargingLocked(res.grant.Charging)

	cr.mu.Unlock()

	request := "update"

	switch {
	case initial:
		request = "initial"
	case job == nil:
		request = "priority"
	}

	attrs := []any{
		slog.String("dialog", d.ID()), slog.String("impi", cr.key.impi), slog.String("ue", cr.key.ue.String()),
		slog.String("session", s.id), slog.String("request", request), slog.String("forking", fork.String()),
	}

	if priority != nil {
		attrs = append(attrs, slog.String("mcptt_priority", priority.Namespace+"."+strconv.Itoa(int(priority.Level))))
	}

	p.log.DebugContext(ctx, "media authorized", attrs...)

	return nil
}

func samePriority(a, b *policy.MCPTTPriority) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (p *PCSCF) fromNetwork(d *proxy.Dialog, m sip.Message) bool {
	c := callOf(d)
	if c == nil || c.policy == nil {
		return false
	}

	if res, ok := m.(*sip.Response); ok {
		p.icsiFromNetwork(c, res)
	}

	return c.policy.receivedPriority(m)
}

// TS 24.229 clause 3.1
// TS 23.379 clause 10.6.2.6.1.1
// TS 24.229 clause 5.2.9.1
// TS 24.229 clause 5.2.9.2
func (cr *callPolicy) receivedPriority(m sip.Message) bool {
	if cr == nil {
		return false
	}

	h := m.Env().Header

	switch m := m.(type) {
	case *sip.Request:
		if m.Method == "BYE" || m.Method == "CANCEL" {
			return false
		}
	case *sip.Response:
		cseq, err := h.CSeq()
		if err != nil || m.StatusCode <= 100 || m.StatusCode >= 300 || cseq.Method == "BYE" || cseq.Method == "CANCEL" {
			return false
		}
	}

	p := mcpttPriority(h)
	if p == nil {
		return false
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	if samePriority(cr.priority, p) {
		return false
	}

	cr.priority = p

	return true
}

// TS 29.214 clause A.13.2
// RFC 8101 section 3.2
// RFC 4412 section 3.1
func mcpttPriority(h sip.Header) *policy.MCPTTPriority {
	rs, err := h.ResourcePriorities()
	if err != nil {
		return nil
	}

	for _, r := range rs {
		ns := strings.ToLower(r.Namespace)
		if ns != sip.ResourcePriorityMCPTTP && ns != sip.ResourcePriorityMCPTTQ {
			continue
		}

		level, err := strconv.ParseUint(r.Priority, 10, 8)
		if err != nil || level > 15 {
			continue
		}

		return &policy.MCPTTPriority{Namespace: ns, Level: uint8(level)}
	}

	return nil
}

// TS 29.214 clause A.1
func (p *PCSCF) icsiFromNetwork(c *call, res *sip.Response) {
	if c.ue != proxy.Caller || res.StatusCode/10 != 18 && !res.IsSuccess() {
		return
	}

	cr := c.policy

	cr.mu.Lock()
	defer cr.mu.Unlock()

	if cr.service == "" {
		cr.service = icsiRef(res.Header)
	}
}

func activeComponents(components []policy.MediaComponent) map[uint32]policy.MediaType {
	out := make(map[uint32]policy.MediaType, len(components))

	for _, c := range components {
		if c.Status != policy.FlowRemoved {
			out[c.Number] = c.Type
		}
	}

	return out
}

// TS 29.213 clause B.4.1
func (p *PCSCF) callEnded(ctx context.Context, c *call) {
	if c.policy == nil {
		return
	}

	s := c.policy.end()
	if s == nil {
		return
	}

	if !trace.SpanContextFromContext(ctx).IsValid() {
		var span trace.Span

		ctx, span = p.detached.Start(p.ctx, "call end", c.invite)
		defer span.End()
	}

	p.policy.end(ctx, s, policy.TerminationLogout, 0)
}

func (c *policyClient) callSession(cr *callPolicy) *policySession {
	b := c.resolve(cr.key.ue)
	s := &policySession{id: b.NewSessionID(), key: cr.key, call: cr, backend: b}

	c.mu.Lock()
	c.sessions[s.id] = s
	c.mu.Unlock()

	return s
}

type retryHold struct {
	until  time.Time
	digest string
}

// TS 29.214 clause 4.4.1
func (c *policyClient) authorize(ctx context.Context, s *policySession, r policy.Request) (policy.Grant, error) {
	b, err := json.Marshal(struct {
		Components []policy.MediaComponent
		MCPTT      *policy.MCPTTPriority
	}{r.Components, r.MCPTT})
	if err != nil {
		return policy.Grant{}, err
	}

	digest := string(b)
	now := c.now()

	c.mu.Lock()

	hold, held := c.retry[s.key.ue]
	if held && !now.Before(hold.until) {
		delete(c.retry, s.key.ue)

		held = false
	}

	c.mu.Unlock()

	if held && hold.digest == digest {
		return policy.Grant{}, errRetryInterval
	}

	ctx, cancel := c.deadline(ctx, 0)
	defer cancel()

	g, err := s.backend.Authorize(ctx, s.id, s.ref, r)

	if retry := policy.RetryAfter(err); retry > 0 {
		c.mu.Lock()
		c.retry[s.key.ue] = retryHold{until: now.Add(retry), digest: digest}
		c.mu.Unlock()
	}

	return g, err
}

// TS 24.229 clause 7.2A.5.2.1 table 7.2A.5
const maxItems = 9

// TS 24.229 clause 7.2A.5.2.7
// TS 24.229 clause 7.2A.5.2.10
// TS 29.214 clause 5.3.3
// TS 29.214 annex B
func chargingInfo(c policy.Charging, flows map[int]flowNumbers) string {
	if len(c.Identifiers) == 0 || !c.Address.IsValid() {
		return ""
	}

	var gateway, list, item, cid string

	switch c.Access {
	case policy.AccessUnknown, policy.AccessEPS:
		gateway, list, item, cid = "pdngw", "eps-info", "eps-item", ";eps-sig=no;ecid="
	case policy.Access5GS:
		gateway, list, item, cid = "smf", "5gs-info", "5gs-item", ";5gscid="
	default:
		return ""
	}

	ids := chargingIDs(c.Identifiers)

	switch {
	case len(ids) == 0:
		return ""
	case len(ids) > maxItems:
		ids = ids[:maxItems]
	}

	items := make([]string, 0, len(ids))

	for i, id := range ids {
		v := item + "=" + strconv.Itoa(i+1) + cid + strings.ToUpper(hex.EncodeToString(id.Value))

		if ids := flowIDs(id.Flows, flows); ids != "" {
			v += ";flow-id=" + ids
		}

		items = append(items, v)
	}

	return gateway + "=" + sip.FormatHost(c.Address.Unmap()) + ";" + list + `="` + strings.Join(items, ",") + `"`
}

// TS 24.229 clause 7.2A.5.2.7
// TS 24.229 clause 7.2A.5.2.10
// TS 29.514 clause 5.6.2.32
func chargingIDs(ids []policy.ChargingID) []policy.ChargingID {
	var out []policy.ChargingID

	index := make(map[string]int)

	for _, id := range ids {
		if len(id.Value) == 0 {
			continue
		}

		if i, ok := index[string(id.Value)]; ok {
			out[i].Flows = append(out[i].Flows, id.Flows...)
			continue
		}

		index[string(id.Value)] = len(out)
		out = append(out, policy.ChargingID{Value: id.Value, Flows: slices.Clone(id.Flows)})
	}

	return out
}

func flowIDs(fs []policy.Flows, numbers map[int]flowNumbers) string {
	var tuples []string

	seen := make(map[string]bool)

	for _, f := range fs {
		ns := f.FlowNumbers

		if len(ns) == 0 {
			n := numbers[int(f.Component)-1]

			for _, v := range []uint32{n.rtp, n.rtcp} {
				if v != 0 {
					ns = append(ns, v)
				}
			}

			slices.Sort(ns)
		}

		for _, n := range ns {
			t := "{" + strconv.FormatUint(uint64(f.Component), 10) + "," + strconv.FormatUint(uint64(n), 10) + "}"
			if !seen[t] {
				seen[t] = true
				tuples = append(tuples, t)
			}
		}
	}

	if len(tuples) == 0 {
		return ""
	}

	return "(" + strings.Join(tuples, ",") + ")"
}

// TS 29.214 clause 5.6.3
// TS 29.214 clause 4.4.2
func (cr *callPolicy) chargingLocked(c policy.Charging) {
	if c.Access == policy.AccessUnknown {
		c.Access = cr.access
	} else {
		cr.access = c.Access
	}

	if info := chargingInfo(c, cr.flows); info != "" {
		cr.charging = info
	}
}

// TS 24.229 clause 5.2.7.2
// TS 24.229 clause 5.2.7.3
func (cr *callPolicy) takeCharging() string {
	if cr == nil {
		return ""
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	info := cr.charging
	cr.charging = ""

	return info
}

func chargeResponse(c *call, res *sip.Response) {
	cv, ok := parseChargingVector(res.Header.Get("P-Charging-Vector"))
	if !ok {
		return
	}

	info := c.policy.takeCharging()
	if info == "" {
		return
	}

	cv.access = info
	cv.setResponse(res)
}

// TS 29.214 clause 4.4.6.2
// TS 29.214 clause 4.4.6.5
// TS 29.514 clause 4.2.5.2
// TS 24.229 clause 5.2.7.4
// TS 24.229 clause 5.2.8.1
func (p *PCSCF) callNotify(ctx context.Context, s *policySession, e policy.Event) {
	cr := s.call

	if e.Charging != nil {
		cr.mu.Lock()
		cr.chargingLocked(*e.Charging)
		cr.mu.Unlock()
	}

	if !e.Has(policy.EventBearerLost, policy.EventBearerReleased, policy.EventResourcesFailed) {
		return
	}

	components := slices.Clone(e.Components)

	cr.mu.Lock()
	defer cr.mu.Unlock()

	if cr.ended {
		return
	}

	if cr.loss != nil {
		if len(cr.lost) > 0 && len(components) > 0 {
			cr.lost = append(cr.lost, components...)
		} else {
			cr.lost = nil
		}

		return
	}

	cr.lost = components
	cause := trace.SpanContextFromContext(ctx)
	cr.loss = p.clock.AfterFunc(p.policy.cfg.MediaLossTimeout, func() {
		ctx, span := p.detached.Start(p.ctx, "media loss", cause)
		tracing.End(span, p.mediaLossExpired(ctx, cr))
	})
}

func (p *PCSCF) mediaLossExpired(ctx context.Context, cr *callPolicy) error {
	cr.mu.Lock()

	cr.loss = nil
	release, video := lossReleases(cr.active, cr.lost)
	live := !cr.ended && cr.dialog != nil

	cr.mu.Unlock()

	if !live {
		return nil
	}

	if video {
		p.log.InfoContext(ctx, "video bearer lost: the call goes on without video", slog.String("dialog", cr.dialog.ID()), slog.String("impi", cr.key.impi),
			slog.String("ue", cr.key.ue.String()))
	}

	if !release {
		return nil
	}

	p.log.InfoContext(ctx, "media bearer lost: releasing the call", slog.String("dialog", cr.dialog.ID()), slog.String("impi", cr.key.impi),
		slog.String("ue", cr.key.ue.String()))

	return p.releaseCall(ctx, cr.call, cr.dialog, false)
}

// GSMA IR.94 section 2.4.1
// GSMA NG.114 section 4.6.2
// TS 24.229 clause 5.2.8.1.2
func lossReleases(active map[uint32]policy.MediaType, lost []uint32) (release, video bool) {
	if len(lost) == 0 {
		return len(active) > 0, false
	}

	gone := make(map[uint32]bool, len(lost))

	for _, n := range lost {
		t, on := active[n]
		if !on {
			continue
		}

		if t != policy.MediaVideo {
			return true, false
		}

		gone[n] = true
	}

	if len(gone) == 0 {
		return false, false
	}

	for n, t := range active {
		if !gone[n] && t != policy.MediaVideo {
			return false, true
		}
	}

	return true, false
}

// TS 24.229 clause 5.2.8.1.1
// TS 24.229 clause 5.2.8.1.2
func (p *PCSCF) releaseCall(ctx context.Context, c *call, d *proxy.Dialog, signalling bool) error {
	cause, _ := sip.NewReason("SIP", 503, "")

	var r proxy.Release

	switch d.State() {
	case proxy.Early:
		switch {
		case !signalling:
			r = proxy.Release{Toward: proxy.Both, Code: 500, Reason: []sip.Reason{cause}}
		case c.ue == proxy.Caller:
			r = proxy.Release{Toward: proxy.Callee, Reason: []sip.Reason{cause}}
		default:
			r = proxy.Release{Toward: proxy.Caller, Code: 500}
		}
	case proxy.Answered, proxy.Confirmed:
		r = proxy.Release{Toward: other(c.ue), Reason: []sip.Reason{cause}}
	default:
		return nil
	}

	if err := d.Release(ctx, r); err != nil {
		p.log.DebugContext(ctx, "releasing the call failed", slog.String("dialog", d.ID()), slog.Any("error", err))
		return err
	}

	return nil
}

// TS 29.214 clause 5.4
// RFC 4006 section 8.46
func subscribers(identities []string) []policy.Subscriber {
	var out []policy.Subscriber

	for _, id := range identities {
		u, err := sip.ParseURI(id)
		if err != nil {
			continue
		}

		s := policy.Subscriber{Kind: policy.SubscriberSIPURI, ID: id}
		if t := telForm(u); t.IsTel() {
			s = policy.Subscriber{Kind: policy.SubscriberE164, ID: strings.TrimPrefix(t.User, "+")}
		}

		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}

	return out
}

// TS 29.214 clause A.1
// RFC 6809
func icsiRef(h sip.Header) string {
	caps, _ := h.FeatureCaps()

	for _, c := range caps {
		if icsis := c.Tokens("g.3gpp.icsi-ref"); len(icsis) > 0 {
			return icsis[0]
		}
	}

	return ""
}
