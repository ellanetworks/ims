package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel/trace"
)

// RFC 3261 section 16
type responseContext struct {
	p      *Proxy
	tx     *transaction.ServerTransaction
	invite bool
	ctx    context.Context

	breadth int

	mu sync.Mutex

	branches []*branch

	waiting []*branch
	groups  [][]*branch

	final     bool
	success   bool
	cancelled bool
	reason    []sip.Field
	cause     trace.SpanContext
	answers   map[string]bool

	stopped bool

	held bool

	forks int

	best       *sip.Response
	challenges []*sip.Response

	generated map[*sip.Response]bool

	queue    []delivery
	draining bool
}

type delivery struct {
	b *branch
	r Reply
}

type branch struct {
	c           *responseContext
	onReply     func(ctx context.Context, r Reply) Verdict
	timeout     time.Duration
	anyResponse bool
	noAnswer    time.Duration

	out     *sip.Request
	to      Target
	rr      *RecordRoute
	breadth int

	client    *transaction.ClientTransaction
	done      bool
	responded bool

	settled bool

	cancelling []sip.Field
	cancelled  bool

	timerC, timer, noAnswerTimer transaction.Timer
	genC                         int

	dialog  *Dialog
	req     *sip.Request
	initial bool

	delivered bool

	taken bool

	early, ended []string

	retry []*branch

	expired bool

	reached bool

	withdrawn []sip.Field

	key any
}

// RFC 5626 section 7
func (b *branch) retries(res *sip.Response) bool {
	return res != nil && res.StatusCode == 430 && len(b.retry) > 0
}

// RFC 3261 section 16.8
var errRangOut = fmt.Errorf("%w: rang out", transaction.ErrTimeout)

var errWithdrawn = errors.New("sip/proxy: branch withdrawn")

func (c *responseContext) withdrawnResponse(b *branch) *sip.Response {
	res := c.generate(480)
	for _, f := range b.withdrawn {
		res.Header.Add(f.Name, f.Value)
	}

	return res
}

func newContext(ctx context.Context, p *Proxy, tx *transaction.ServerTransaction) *responseContext {
	return &responseContext{
		p: p, tx: tx, invite: tx.Request().Method == "INVITE", ctx: ctx,
		answers: make(map[string]bool), generated: make(map[*sip.Response]bool),
	}
}

func (c *responseContext) open(groups [][]*branch, breadth int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case c.final || c.success || c.cancelled || c.stopped:
		return ErrAnswered
	case c.liveLocked() || len(c.waiting) > 0 || len(c.groups) > 0:
		return ErrForwarded
	}

	c.held = false
	c.groups, c.breadth = groups, breadth
	c.forks++

	return nil
}

func (c *responseContext) liveLocked() bool {
	return slices.ContainsFunc(c.branches, func(b *branch) bool { return !b.done })
}

// RFC 5393 section 5.3.2
func (c *responseContext) outgoingLocked() int {
	n := 0

	for _, b := range c.branches {
		if !b.settled {
			n += b.breadth
		}
	}

	return n
}

func (c *responseContext) pickLocked() []*branch {
	if c.final || c.stopped || c.cancelled || c.held {
		return nil
	}

	if len(c.waiting) == 0 {
		if c.liveLocked() || len(c.groups) == 0 {
			return nil
		}

		c.waiting, c.groups = c.groups[0], c.groups[1:]
	}

	avail := c.breadth - c.outgoingLocked()
	if avail <= 0 {
		return nil
	}

	n := min(avail, len(c.waiting))
	picked := c.waiting[:n:n]
	c.waiting = c.waiting[n:]

	for i, b := range picked {
		b.breadth = 1

		if len(c.waiting) == 0 {
			b.breadth = avail / n

			if i < avail%n {
				b.breadth++
			}
		}

		b.out.Header.Set("Max-Breadth", strconv.Itoa(b.breadth))
	}

	c.branches = append(c.branches, picked...)

	return picked
}

type failure struct {
	b   *branch
	err error
}

func (c *responseContext) start(ctx context.Context, bs []*branch) (int, []failure) {
	started := 0

	var failed []failure

	for _, b := range bs {
		if err := c.send(ctx, b); err != nil {
			failed = append(failed, failure{b: b, err: err})
			continue
		}

		started++
	}

	return started, failed
}

func (c *responseContext) send(ctx context.Context, b *branch) error {
	d := b.dialog

	if d != nil && !b.initial {
		tracked, err := d.request(ctx, b.out)
		if err != nil {
			c.drop(b)
			return err
		}

		if !tracked {
			c.mu.Lock()
			b.dialog = nil
			c.mu.Unlock()
		}
	}

	c.arm(b)

	client, err := c.p.layer.Request(c.ctx, b.out, b)
	if err != nil {
		c.drop(b)

		if d := b.dialog; d != nil && !b.initial {
			d.response(ctx, b.out, Reply{Response: sip.NewResponse(b.out, 500, ""), Err: err})

			c.mu.Lock()
			b.dialog = nil
			c.mu.Unlock()
		}

		return internal(err)
	}

	c.started(b, client)

	return nil
}

func (c *responseContext) drop(b *branch) {
	c.mu.Lock()
	b.finish()
	b.settled = true
	c.mu.Unlock()
}

// RFC 3261 section 16.6
func (c *responseContext) arm(b *branch) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if b.done {
		return
	}

	if c.invite {
		b.startTimerC()

		if b.noAnswer > 0 {
			b.noAnswerTimer = c.p.clock.AfterFunc(b.noAnswer, func() { b.noAnswerFired(context.Background()) })
		}
	}

	if b.timeout > 0 && !b.stopsTimeout() {
		b.timer = c.p.clock.AfterFunc(b.timeout, func() { b.timeoutFired(context.Background()) })
	}
}

func (c *responseContext) advance(ctx context.Context) {
	for {
		c.mu.Lock()
		picked := c.pickLocked()
		c.mu.Unlock()

		if len(picked) == 0 {
			return
		}

		_, failed := c.start(ctx, picked)

		for _, f := range failed {
			c.dispatch(ctx, f.b, Reply{Response: c.generate(statusCode(f.err)), Err: f.err})
		}

		if len(failed) < len(picked) {
			return
		}
	}
}

func statusCode(err error) int {
	if serr, ok := errors.AsType[*sip.StatusError](err); ok {
		return serr.StatusCode
	}

	return 500
}

func (c *responseContext) started(b *branch, client *transaction.ClientTransaction) {
	c.mu.Lock()

	b.client = client

	var (
		cancel bool
		reason []sip.Field
	)

	cause := context.Background()

	switch {
	case b.cancelled:
		cancel, reason = true, b.cancelling
	case c.cancelled && !b.done:
		cancel, reason = true, c.reason
		cause = trace.ContextWithSpanContext(cause, c.cause)
	}

	c.mu.Unlock()

	if cancel {
		_ = client.Cancel(cause, reason...)
	}
}

func (b *branch) cancelLocked(reason []sip.Field) *transaction.ClientTransaction {
	if b.cancelled || b.settled {
		return nil
	}

	b.cancelled, b.cancelling = true, reason

	return b.client
}

type cancellation struct {
	client *transaction.ClientTransaction
	reason []sip.Field
}

func (x cancellation) send(cause context.Context) {
	if x.client != nil {
		_ = x.client.Cancel(cause, x.reason...)
	}
}

// RFC 3261 section 16.7
// RFC 3261 section 16.10
func (c *responseContext) stopLocked(reason []sip.Field) []cancellation {
	c.waiting, c.groups = nil, nil

	if !c.invite {
		return nil
	}

	var out []cancellation

	for _, b := range c.branches {
		if b.done {
			continue
		}

		if client := b.cancelLocked(reason); client != nil {
			out = append(out, cancellation{client: client, reason: reason})
		}
	}

	return out
}

func sendAll(cause context.Context, xs []cancellation) {
	for _, x := range xs {
		x.send(cause)
	}
}

func (b *branch) startTimerC() {
	if b.timerC != nil {
		b.timerC.Stop()
	}

	b.genC++
	gen := b.genC
	b.timerC = b.c.p.clock.AfterFunc(b.c.p.timerC, func() { b.timerCFired(context.Background(), gen) })
}

func (b *branch) finish() {
	b.done = true
	b.stopTimers()
}

func (b *branch) stopTimers() {
	for _, t := range []transaction.Timer{b.timerC, b.timer, b.noAnswerTimer} {
		if t != nil {
			t.Stop()
		}
	}
}

// RFC 3261 section 16.8
func (b *branch) timerCFired(ctx context.Context, gen int) {
	b.expire(ctx, func() bool { return gen == b.genC }, "Timer C")
}

func (b *branch) noAnswerFired(ctx context.Context) {
	b.expire(ctx, func() bool { return true }, "no answer")
}

// RFC 3261 section 16.8
func (b *branch) expire(ctx context.Context, current func() bool, cause string) {
	c := b.c

	c.mu.Lock()

	if b.done || b.expired || !current() {
		c.mu.Unlock()
		return
	}

	c.p.log.Debug("INVITE branch expired", slog.String("cause", cause), slog.String("request", c.tx.Request().StartLine()))

	if b.responded {
		b.expired = true
		b.stopTimers()
		client := b.cancelLocked(nil)

		c.mu.Unlock()

		if client != nil {
			_ = client.Cancel(context.Background())
		}

		return
	}

	b.finish()
	client := b.cancelLocked(nil)
	b.settled = true

	c.mu.Unlock()

	if client != nil {
		_ = client.Cancel(context.Background())
	}

	c.dispatch(ctx, b, Reply{Response: c.generate(408), Err: fmt.Errorf("%w: %s", transaction.ErrTimeout, cause)})
}

func (b *branch) timeoutFired(ctx context.Context) {
	c := b.c

	c.mu.Lock()

	if b.done || b.stopsTimeout() {
		c.mu.Unlock()
		return
	}

	b.finish()

	var client *transaction.ClientTransaction
	if c.invite {
		client = b.cancelLocked(nil)
	}

	responded := b.responded
	b.settled = true

	c.mu.Unlock()

	var res *sip.Response

	if c.invite {
		if client != nil {
			_ = client.Cancel(context.Background())
		}

		res = c.generate(408)
	}

	c.dispatch(ctx, b, Reply{
		Response: res, Err: fmt.Errorf("%w: no response within %s", transaction.ErrTimeout, b.timeout),
		Responded: responded, Provisional: responded,
	})
}

func (b *branch) stopsTimeout() bool {
	return b.reached || (b.c.invite || b.anyResponse) && b.responded
}

// RFC 5393 section 5.3.3.1
func (b *branch) settleLocked() bool {
	if b.settled {
		return false
	}

	b.settled = true

	return len(b.c.waiting) > 0
}

func (b *branch) HandleResponse(ctx context.Context, res *sip.Response) {
	c := b.c

	res = res.Clone()

	if _, ok := res.Header.PopFirst("Via"); !ok || !res.Header.Has("Via") {
		return
	}

	c.mu.Lock()

	provisional := b.responded
	b.responded = true

	if b.timer != nil && b.stopsTimeout() {
		b.timer.Stop()
	}

	freed := !res.IsProvisional() && b.settleLocked()

	switch {
	case res.IsProvisional():
		if res.StatusCode == 100 || !c.invite || b.done || c.final {
			c.mu.Unlock()
			return
		}

		if !b.expired {
			b.startTimerC()
		}

		if tag := toTag(res); tag != "" {
			switch {
			case res.StatusCode == 199:
				b.ended = append(b.ended, tag)
			case !slices.Contains(b.early, tag):
				b.early = append(b.early, tag)
			}
		}
	case res.IsSuccess():
		tag := toTag(res)

		if relayed, seen := c.answers[tag]; seen {
			c.mu.Unlock()

			if relayed && c.invite {
				if err := c.tx.Relay(res); err != nil {
					c.p.log.Debug("relaying a 2xx retransmission failed", slog.Any("error", err))
				}
			}

			if b.dialog != nil && b.initial {
				b.dialog.retransmitted(res)
			}

			return
		}

		if c.final && !c.invite {
			b.finish()
			c.mu.Unlock()

			return
		}

		c.answers[tag] = false
		c.success = true

		b.finish()
	default:
		if b.done || c.final {
			b.finish()
			c.mu.Unlock()

			if freed {
				c.advance(ctx)
			}

			return
		}

		b.finish()

		if b.withdrawn != nil {
			c.mu.Unlock()

			c.dispatch(ctx, b, Reply{Response: c.withdrawnResponse(b), Err: errWithdrawn, Responded: true, Provisional: provisional})

			if freed {
				c.advance(ctx)
			}

			return
		}

		if b.expired {
			c.mu.Unlock()

			c.dispatch(ctx, b, Reply{Response: c.generate(408), Err: errRangOut, Responded: true, Provisional: provisional})

			if freed {
				c.advance(ctx)
			}

			return
		}
	}

	c.mu.Unlock()

	c.dispatch(ctx, b, Reply{Response: res, Responded: true, Provisional: provisional && !res.IsProvisional()})

	if freed {
		c.advance(ctx)
	}
}

func (b *branch) HandleError(ctx context.Context, err error) {
	c := b.c

	c.mu.Lock()

	freed := b.settleLocked()

	if b.done || c.final {
		b.finish()
		c.mu.Unlock()

		if freed {
			c.advance(ctx)
		}

		return
	}

	b.finish()

	if errors.Is(err, transaction.ErrClosed) {
		c.mu.Unlock()
		c.p.forget(c)

		return
	}

	var res *sip.Response

	responded := b.responded

	switch {
	case !c.invite && errors.Is(err, transaction.ErrTimeout):
	case b.expired:
		res, err = c.generateLocked(408), fmt.Errorf("%w: %w", errRangOut, err)
	case b.withdrawn != nil:
		res, err = c.generateLocked(480), fmt.Errorf("%w: %w", errWithdrawn, err)

		for _, f := range b.withdrawn {
			res.Header.Add(f.Name, f.Value)
		}
	case b.cancelled || c.cancelled:
		res = c.generateLocked(487)
	case errors.Is(err, transaction.ErrTimeout):
		res = c.generateLocked(408)
	default:
		res = c.generateLocked(500)
	}

	c.mu.Unlock()

	c.p.log.Debug("proxied request failed", slog.String("request", c.tx.Request().StartLine()), slog.Any("error", err))
	c.dispatch(ctx, b, Reply{Response: res, Err: err, Responded: responded, Provisional: responded})

	if freed {
		c.advance(ctx)
	}
}

func (c *responseContext) generate(code int) *sip.Response {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.generateLocked(code)
}

func (c *responseContext) generateLocked(code int) *sip.Response {
	res := sip.NewResponse(c.tx.Request(), code, "")
	c.generated[res] = true

	return res
}

func (c *responseContext) dispatch(ctx context.Context, b *branch, r Reply) {
	c.mu.Lock()

	c.queue = append(c.queue, delivery{b: b, r: r})

	if c.draining {
		c.mu.Unlock()
		return
	}

	c.draining = true

	for len(c.queue) > 0 {
		d := c.queue[0]
		c.queue[0] = delivery{}
		c.queue = c.queue[1:]

		if d.r.Response == nil || !d.r.Response.IsProvisional() {
			d.b.taken = true
		}

		c.mu.Unlock()
		c.deliver(ctx, d.b, d.r)
		c.mu.Lock()
	}

	c.draining = false
	c.mu.Unlock()
}

func (c *responseContext) deliver(ctx context.Context, b *branch, r Reply) {
	if d := b.dialog; d != nil {
		c.toDialog(ctx, d, b, r)
	}

	if b.onReply == nil {
		c.relayReply(ctx, b, r)
		return
	}

	c.mu.Lock()
	forks := c.forks
	c.mu.Unlock()

	if b.onReply(c.ctx, r) == Relay {
		c.relayReply(ctx, b, r)
		return
	}

	if r.Response == nil || !r.Response.IsProvisional() {
		c.mu.Lock()
		c.held = !c.final && c.forks == forks
		c.mu.Unlock()
	}
}

func (c *responseContext) toDialog(ctx context.Context, d *Dialog, b *branch, r Reply) {
	res := r.Response

	switch {
	case !b.initial:
		d.response(ctx, b.req, r)
		return
	case res != nil && (res.IsProvisional() || res.IsSuccess()):
		d.inviteResponse(ctx, b, res)
		return
	}

	c.mu.Lock()

	b.delivered = true

	best := c.bestWithLocked(res)
	downstream := !c.generated[best]

	over := res.StatusCode >= 600 || c.stopped || c.cancelled || len(c.waiting) == 0 && len(c.groups) == 0 && !b.retries(res)
	over = over && !slices.ContainsFunc(c.branches, func(o *branch) bool { return !o.delivered })

	c.mu.Unlock()

	d.branchFailed(b)

	if over {
		d.failed(ctx, best, downstream)
	}
}

func (c *responseContext) relayReply(ctx context.Context, b *branch, r Reply) {
	if err := c.relayFrom(ctx, b, r.Response); err != nil && !errors.Is(err, ErrAnswered) {
		c.p.log.Debug("relaying a response failed", slog.Any("error", err))
	}
}

func (c *responseContext) relay(ctx context.Context, res *sip.Response) error {
	return c.relayFrom(ctx, nil, res)
}

// RFC 3261 section 16.7
func (c *responseContext) relayFrom(ctx context.Context, b *branch, res *sip.Response) error {
	c.mu.Lock()

	switch {
	case res != nil && res.IsProvisional():
		final := c.final
		c.mu.Unlock()

		if final {
			return ErrAnswered
		}

		return c.tx.Relay(res)
	case res != nil && res.IsSuccess() && c.invite:
		c.final, c.stopped, c.held = true, true, false
		c.answers[toTag(res)] = true
		cancels := c.stopLocked(completedElsewhere)
		c.mu.Unlock()

		err := c.tx.Relay(res)

		sendAll(context.Background(), cancels)
		c.p.forget(c)

		return err
	case c.final:
		c.mu.Unlock()
		return ErrAnswered
	case b == nil:
		c.final, c.stopped, c.held = true, true, false
		cancels := c.stopLocked(nil)
		c.mu.Unlock()

		sendAll(context.Background(), cancels)

		return c.finalize(res)
	case res != nil && res.IsSuccess():
		c.final, c.stopped = true, true
		cancels := c.stopLocked(nil)
		c.mu.Unlock()

		sendAll(context.Background(), cancels)

		return c.finalize(res)
	}

	if b.retries(res) && !c.stopped && !c.cancelled {
		c.considerLocked(res)

		next := b.retry[0]
		next.retry = b.retry[1:]
		c.waiting = append([]*branch{next}, c.waiting...)

		c.mu.Unlock()

		c.advance(ctx)

		return c.conclude()
	}

	var cancels []cancellation

	if res != nil {
		c.considerLocked(res)

		if res.StatusCode >= 600 && !c.stopped {
			c.stopped = true
			cancels = c.stopLocked(reasonFor(res.StatusCode))
		}
	}

	c.mu.Unlock()

	sendAll(context.Background(), cancels)
	c.advance(ctx)

	err := c.conclude()

	if res != nil {
		c.earlyTerminated(b, res)
	}

	return err
}

// RFC 6228 section 6
// TS 24.229 clause 5.4.3.3
func (c *responseContext) earlyTerminated(b *branch, res *sip.Response) {
	in := c.tx.Request()

	if !c.invite || !in.Header.HasOptionTag("Supported", sip.OptionTag199) || in.Header.HasOptionTag("Require", sip.OptionTag100rel) ||
		in.Header.HasOptionTag("Proxy-Require", sip.OptionTag100rel) {
		return
	}

	c.mu.Lock()

	if c.final {
		c.mu.Unlock()
		return
	}

	var tags []string

	for _, tag := range b.early {
		if !slices.Contains(b.ended, tag) {
			tags = append(tags, tag)
		}
	}

	b.ended = append(b.ended, tags...)

	c.mu.Unlock()

	for _, tag := range tags {
		r := sip.NewResponse(in, 199, "")
		_ = r.Header.SetToTag(tag)

		for _, f := range reasonFor(res.StatusCode) {
			r.Header.Add(f.Name, f.Value)
		}

		if err := c.tx.Relay(r); err != nil {
			c.p.log.Debug("sending a 199 failed", slog.Any("error", err))
		}
	}
}

// RFC 3261 section 16.7
func (c *responseContext) considerLocked(res *sip.Response) {
	if res.StatusCode == 401 || res.StatusCode == 407 {
		c.challenges = append(c.challenges, res)
	}

	c.best = c.bestWithLocked(res)
}

func (c *responseContext) bestWithLocked(res *sip.Response) *sip.Response {
	switch {
	case c.best == nil, better(res.StatusCode, c.best.StatusCode):
		return res
	case !better(c.best.StatusCode, res.StatusCode) && res.StatusCode/100 == c.best.StatusCode/100 &&
		c.generated[c.best] && !c.generated[res]:
		return res
	}

	return c.best
}

func better(code, than int) bool {
	class, thanClass := code/100, than/100

	switch {
	case class == 6 || thanClass == 6:
		return class == 6 && thanClass != 6
	case class != thanClass:
		return class < thanClass
	case class == 4:
		return resubmission(code) && !resubmission(than)
	}

	return false
}

func resubmission(code int) bool {
	switch code {
	case 401, 407, 415, 420, 484:
		return true
	}

	return false
}

// RFC 3261 section 16.7
func (c *responseContext) conclude() error {
	c.mu.Lock()

	if c.final || c.held || slices.ContainsFunc(c.branches, func(b *branch) bool { return !b.taken }) ||
		len(c.waiting) > 0 || len(c.groups) > 0 {
		c.mu.Unlock()
		return nil
	}

	c.final = true
	best := c.best
	challenges := c.challenges

	c.mu.Unlock()

	if best == nil {
		if !c.invite {
			c.p.forget(c)
			return nil
		}

		best = c.generate(408)
	}

	if best.StatusCode == 401 || best.StatusCode == 407 {
		best = withChallenges(best, challenges)
	}

	return c.finalize(best)
}

// RFC 3261 section 16.7
func withChallenges(best *sip.Response, all []*sip.Response) *sip.Response {
	best = best.Clone()

	for _, res := range all {
		for _, name := range []string{"WWW-Authenticate", "Proxy-Authenticate"} {
			for _, v := range res.Header.Values(name) {
				if !slices.Contains(best.Header.Values(name), v) {
					best.Header.Add(name, v)
				}
			}
		}
	}

	return best
}

// RFC 3261 section 16.7
// RFC 5626 section 11.5
func (c *responseContext) finalize(res *sip.Response) error {
	if code := UpstreamStatus(res.StatusCode); code != res.StatusCode {
		res = c.generate(code)
	}

	err := c.tx.Relay(res)
	c.p.forget(c)

	return err
}

// RFC 3261 section 16.7
// RFC 5626 section 11.5
func UpstreamStatus(code int) int {
	switch code {
	case 503:
		return 500
	case 430:
		return 480
	}

	return code
}

var completedElsewhere = reasonFor(200)

// TS 24.229 clause 5.4.3.3
// RFC 3326
func reasonFor(code int) []sip.Field {
	r, err := sip.NewReason(sip.ReasonSIP, code, sip.ReasonText(sip.ReasonSIP, code))
	if err != nil {
		return nil
	}

	return []sip.Field{{Name: "Reason", Value: r.String()}}
}

// RFC 3261 section 16.10
func (c *responseContext) cancel(ctx context.Context, reason []sip.Field) {
	c.mu.Lock()

	if c.final || c.success || c.cancelled {
		c.mu.Unlock()
		return
	}

	c.cancelled = true
	c.reason = reason
	c.cause = trace.SpanContextFromContext(ctx)

	if !c.liveLocked() {
		c.waiting, c.groups = nil, nil
		c.final = true
		c.mu.Unlock()

		c.p.answer(c.tx, 487)
		c.p.forget(c)

		return
	}

	cancels := c.stopLocked(reason)

	c.mu.Unlock()

	sendAll(ctx, cancels)
}

// RFC 3261 section 16.10
func (c *responseContext) withdraw(ctx context.Context, key any, reason []sip.Field) (found, others bool) {
	c.mu.Lock()

	if !c.invite || c.final || c.stopped || c.cancelled {
		c.mu.Unlock()
		return false, false
	}

	mine := func(b *branch) bool { return b.key == key }

	for _, b := range c.branches {
		if b.done || b.cancelled {
			continue
		}

		if mine(b) {
			found = true
		} else {
			others = true
		}

		found = found || slices.ContainsFunc(b.retry, mine)
	}

	for _, b := range slices.Concat(append([][]*branch{c.waiting}, c.groups...)...) {
		if mine(b) {
			found = true
		} else {
			others = true
		}

		found = found || slices.ContainsFunc(b.retry, mine)
	}

	if !found || !others {
		c.mu.Unlock()
		return found, others
	}

	c.waiting = slices.DeleteFunc(c.waiting, mine)

	groups := c.groups[:0]

	for _, g := range c.groups {
		if g = slices.DeleteFunc(g, mine); len(g) > 0 {
			groups = append(groups, g)
		}
	}

	c.groups = groups

	var cancels []cancellation

	for _, b := range slices.Concat(append([][]*branch{c.branches, c.waiting}, c.groups...)...) {
		b.retry = slices.DeleteFunc(slices.Clone(b.retry), mine)

		if b.done || !mine(b) {
			continue
		}

		if b.cancelled || b.settled {
			continue
		}

		b.withdrawn = append([]sip.Field{}, reason...)

		if client := b.cancelLocked(reason); client != nil {
			cancels = append(cancels, cancellation{client: client, reason: reason})
		}
	}

	c.mu.Unlock()

	sendAll(ctx, cancels)

	return true, true
}

func (c *responseContext) dialog() *Dialog {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, b := range slices.Backward(c.branches) {
		if b.dialog != nil {
			return b.dialog
		}
	}

	return nil
}

func toTag(res *sip.Response) string {
	to, _ := res.Header.To()
	return to.Tag()
}
