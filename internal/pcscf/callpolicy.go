package pcscf

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/policy"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/transaction"
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

	mu       sync.Mutex
	session  *policySession
	busy     bool
	queue    []func()
	done     map[int]bool
	early    map[string]bool
	forked   bool
	flows    map[int]flowNumbers
	active   map[uint32]bool
	charging string
	loss     transaction.Timer
	lost     []uint32
	ended    bool
}

func (p *PCSCF) newCallPolicy(k regKey, identities []string, service string) *callPolicy {
	if p.policy == nil {
		return nil
	}

	return &callPolicy{
		key: k, identities: identities, service: service,
		done: make(map[int]bool), early: make(map[string]bool), flows: make(map[int]flowNumbers), active: make(map[uint32]bool),
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
	headers sip.Header
}

// TS 29.214 Annex A.1, A.3, TS 29.213 Annex B.2, TS 29.513 §7.2.3
func (p *PCSCF) mediaReply(tx *transaction.ServerTransaction, d *proxy.Dialog, rep proxy.Reply, initial bool) proxy.Verdict {
	c := callOf(d)
	if c == nil || c.policy == nil || rep.Response == nil {
		return proxy.Relay
	}

	res := rep.Response

	var job *answerJob
	if rep.Err == nil {
		job = p.responseAnswer(c, d, res, initial)
	}

	run := func() {
		if job != nil && !p.authorize(c, d, *job, res) {
			return
		}

		if initial && res.IsSuccess() && d.Released() {
			res = sip.NewResponse(tx.Request(), 500, "")
		}

		p.chargeUEResponse(c, res)

		if err := p.cfg.Proxy.Relay(tx, res); err != nil {
			p.log.Debug("relaying a held response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}
	}

	if p.enqueue(c.policy, job != nil, run) {
		return proxy.Hold
	}

	p.chargeUEResponse(c, res)

	return proxy.Relay
}

// TS 24.229 §5.2.7.3
func (p *PCSCF) chargeUEResponse(c *call, res *sip.Response) {
	if c.ue == proxy.Callee && reliable(res) {
		chargeResponse(c, res)
	}
}

// RFC 3262 §5, RFC 3264, TS 29.214 Annex A.1
func (p *PCSCF) mediaRequest(d *proxy.Dialog, out *sip.Request, forward, reject func()) bool {
	c := callOf(d)
	if c == nil || c.policy == nil {
		return false
	}

	_, answers := d.PendingOffer(out)

	return p.enqueue(c.policy, answers, func() {
		offer, ok := d.PendingOffer(out)
		if !ok {
			forward()
			return
		}

		x := p.parseExchange(c, d, proxy.Exchange{Offer: offer, Answer: proxy.Body{From: other(offer.From), Data: out.Body}})
		if x == nil {
			forward()
			return
		}

		state := d.State()
		job := answerJob{
			x: x, tag: calleeTag(d, out), early: state == proxy.Early, headers: out.Header,
			initial: state == proxy.Early || out.Method == "ACK" && state == proxy.Answered,
		}

		switch {
		case out.Method == "ACK":
			forward()
			p.authorize(c, d, job, nil)
		case p.authorize(c, d, job, nil):
			forward()
		case reject != nil:
			reject()
		}

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

// TS 29.214 Annex A.1, A.3
func (p *PCSCF) responseAnswer(c *call, d *proxy.Dialog, res *sip.Response, initial bool) *answerJob {
	if res.StatusCode <= 100 || res.StatusCode >= 300 {
		return nil
	}

	ex, ok := d.Exchange(res)
	if !ok {
		return nil
	}

	final := initial && res.IsSuccess()
	early := !final && d.State() == proxy.Early

	cr := c.policy

	cr.mu.Lock()

	fresh := len(res.Body) > 0 && !cr.done[ex.Seq]
	needed := fresh || final && cr.forked

	if needed {
		cr.done[ex.Seq] = true
	}

	cr.mu.Unlock()

	if !needed {
		return nil
	}

	x := p.parseExchange(c, d, ex)
	if x == nil {
		return nil
	}

	return &answerJob{x: x, tag: calleeTag(d, res), early: early, initial: initial || early, final: final, headers: res.Header}
}

func (p *PCSCF) parseExchange(c *call, d *proxy.Dialog, ex proxy.Exchange) *sdpExchange {
	o, err := sdp.Parse(ex.Offer.Data)
	if err != nil {
		p.log.Warn("unusable SDP offer", slog.String("dialog", d.ID()), slog.Any("error", err))
		return nil
	}

	a, err := sdp.Parse(ex.Answer.Data)
	if err != nil {
		p.log.Warn("unusable SDP answer", slog.String("dialog", d.ID()), slog.Any("error", err))
		return nil
	}

	return &sdpExchange{offer: o, answer: a, offerFromUE: ex.Offer.From == c.ue}
}

// TS 24.229 §5.2.7.2, TS 29.214 §4.4.1, TS 29.514 §4.2.2.2
func (p *PCSCF) authorize(c *call, d *proxy.Dialog, job answerJob, res *sip.Response) bool {
	err := p.callAAR(c, d, job)
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

	if !job.initial {
		p.log.Warn("media authorization refused for a session modification", attrs...)
		return true
	}

	p.log.Warn("media authorization refused: releasing the call", attrs...)

	if err := d.Release(proxy.Release{Toward: proxy.Both, Code: 500}); err != nil {
		p.log.Debug("releasing the call failed", slog.String("dialog", d.ID()), slog.Any("error", err))
	}

	return false
}

// RFC 3262 §3
func reliable(res *sip.Response) bool {
	return res.IsSuccess() || res.Header.Has("RSeq")
}

type grantResult struct {
	grant policy.Grant
	err   error
}

// TS 29.214 §4.4.1, §4.4.2, §4.4.4, Annex A.3, TS 29.514 §4.2.2.2, §4.2.3.2
func (p *PCSCF) callAAR(c *call, d *proxy.Dialog, job answerJob) error {
	cr := c.policy

	cr.mu.Lock()

	if cr.ended {
		cr.mu.Unlock()
		return errCallEnded
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

	if job.early && job.tag != "" && (cr.forked || len(cr.early) > 0 && !cr.early[job.tag]) {
		fork = policy.ForkingSeveralDialogues
	}

	if cr.service == "" && c.ue == proxy.Caller {
		cr.service = icsiRef(job.headers.Values("Feature-Caps"))
	}

	service := cr.service

	cr.mu.Unlock()

	components, err := mediaComponents(*job.x, flows)
	if err != nil {
		return err
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

	r := policy.Request{
		UE:          cr.key.ue,
		Initial:     initial,
		Service:     service,
		Components:  components,
		Subscribers: subscribers(cr.identities),
		Forking:     fork,
	}

	result := make(chan grantResult, 1)

	if !p.policy.spawn(func() {
		defer s.pending.Store(false)
		defer s.mu.Unlock()

		g, err := p.policy.authorize(s, r)

		switch {
		case err == nil:
			s.opened = true
			if g.Ref != "" {
				s.ref = g.Ref
			}
		case initial, errors.Is(err, policy.ErrUnknownSession):
			s.ended = true
			p.policy.forget(s)
		}

		result <- grantResult{g, err}
	}) {
		s.mu.Unlock()
		s.pending.Store(false)

		return errCallEnded
	}

	timer := time.NewTimer(p.policy.cfg.CallTimeout)
	defer timer.Stop()

	var res grantResult

	select {
	case res = <-result:
	case <-timer.C:
		return errAARTimeout
	}

	if res.err != nil {
		lost := errors.Is(res.err, policy.ErrUnknownSession)

		if initial || lost {
			cr.mu.Lock()
			if cr.session == s {
				cr.session = nil
			}
			cr.mu.Unlock()
		}

		if lost && !initial {
			p.log.Warn("policy session of a call unknown to the policy function: opening a new one",
				slog.String("dialog", d.ID()), slog.String("impi", cr.key.impi), slog.String("session", s.id))

			return p.callAAR(c, d, job)
		}

		return res.err
	}

	cr.mu.Lock()

	if job.early && job.tag != "" {
		cr.early[job.tag] = true
		cr.forked = cr.forked || len(cr.early) > 1
	}

	if job.final {
		cr.forked = false
	}

	cr.flows = flows
	cr.active = activeComponents(components)

	if info := chargingInfo(res.grant.Charging, flows); info != "" {
		cr.charging = info
	}

	cr.mu.Unlock()

	request := "update"
	if initial {
		request = "initial"
	}

	p.log.Info("media authorized", slog.String("dialog", d.ID()), slog.String("impi", cr.key.impi),
		slog.String("ue", cr.key.ue.String()), slog.String("session", s.id), slog.String("request", request),
		slog.String("forking", fork.String()))

	return nil
}

func activeComponents(components []policy.MediaComponent) map[uint32]bool {
	out := make(map[uint32]bool, len(components))

	for _, c := range components {
		out[c.Number] = c.Status != policy.FlowRemoved
	}

	return out
}

// TS 29.213 Annex B.4.1
func (p *PCSCF) callEnded(c *call) {
	if c.policy == nil {
		return
	}

	if s := c.policy.end(); s != nil {
		p.policy.end(s, policy.TerminationLogout, 0)
	}
}

func (c *policyClient) callSession(cr *callPolicy) *policySession {
	s := &policySession{id: c.cfg.Backend.NewSessionID(), key: cr.key, call: cr}

	c.mu.Lock()
	c.sessions[s.id] = s
	c.mu.Unlock()

	return s
}

type retryHold struct {
	until  time.Time
	digest string
}

// TS 29.214 §4.4.1
func (c *policyClient) authorize(s *policySession, r policy.Request) (policy.Grant, error) {
	b, err := json.Marshal(r.Components)
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

	ctx, cancel := c.deadline(0)
	defer cancel()

	g, err := c.cfg.Backend.Authorize(ctx, s.id, s.ref, r)

	if retry := policy.RetryAfter(err); retry > 0 {
		c.mu.Lock()
		c.retry[s.key.ue] = retryHold{until: now.Add(retry), digest: digest}
		c.mu.Unlock()
	}

	return g, err
}

// TS 24.229 Table 7.2A.5: eps-item and 5gs-item are a single DIGIT.
const maxItems = 9

// TS 24.229 §7.2A.5.2.7, §7.2A.5.2.10, TS 29.214 §5.3.3, Annex B
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

	ids := c.Identifiers
	if len(ids) > maxItems {
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

func flowIDs(fs []policy.Flows, numbers map[int]flowNumbers) string {
	var tuples []string

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
			tuples = append(tuples, "{"+strconv.FormatUint(uint64(f.Component), 10)+","+strconv.FormatUint(uint64(n), 10)+"}")
		}
	}

	if len(tuples) == 0 {
		return ""
	}

	return "(" + strings.Join(tuples, ",") + ")"
}

// TS 24.229 §5.2.7.2, §5.2.7.3
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

// TS 29.214 §4.4.6.2, §4.4.6.5, TS 29.514 §4.2.5.2, TS 24.229 §5.2.7.4, §5.2.8.1
func (p *PCSCF) callNotify(s *policySession, e policy.Event) {
	cr := s.call

	if e.Charging != nil {
		cr.mu.Lock()

		if info := chargingInfo(*e.Charging, cr.flows); info != "" {
			cr.charging = info
		}

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
	cr.loss = p.clock.AfterFunc(p.policy.cfg.MediaLossTimeout, func() { p.mediaLossExpired(cr) })
}

func (p *PCSCF) mediaLossExpired(cr *callPolicy) {
	cr.mu.Lock()

	cr.loss = nil
	still := false

	if len(cr.lost) == 0 {
		for _, on := range cr.active {
			still = still || on
		}
	}

	for _, n := range cr.lost {
		still = still || cr.active[n]
	}

	release := still && !cr.ended && cr.dialog != nil

	cr.mu.Unlock()

	if !release {
		return
	}

	p.log.Info("media bearer lost: releasing the call", slog.String("dialog", cr.dialog.ID()), slog.String("impi", cr.key.impi),
		slog.String("ue", cr.key.ue.String()))

	p.releaseCall(cr.call, cr.dialog, false)
}

// TS 24.229 §5.2.8.1.1, §5.2.8.1.2
func (p *PCSCF) releaseCall(c *call, d *proxy.Dialog, signalling bool) {
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
		return
	}

	if err := d.Release(r); err != nil {
		p.log.Debug("releasing the call failed", slog.String("dialog", d.ID()), slog.Any("error", err))
	}
}

// TS 29.214 §5.4, RFC 4006 §8.46
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

// TS 29.214 Annex A.1 NOTE 3, RFC 6809
func icsiRef(values []string) string {
	for _, v := range values {
		for _, e := range sip.SplitList(v) {
			for part := range strings.SplitSeq(e, ";") {
				name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(name), "+g.3gpp.icsi-ref") {
					continue
				}

				value = strings.Trim(strings.TrimSpace(value), `"`)
				if s, err := url.PathUnescape(value); err == nil {
					value = s
				}

				if first, _, _ := strings.Cut(value, ","); first != "" {
					return first
				}
			}
		}
	}

	return ""
}
