package proxy

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// responseContext is the RFC 3261 §16 response context of one server transaction: the branches it
// forwarded, in groups tried one after another, and the responses they returned.
type responseContext struct {
	p      *Proxy
	tx     *transaction.ServerTransaction
	invite bool

	// breadth is the Incoming Max-Breadth (RFC 5393 §5.3.2).
	breadth int

	mu sync.Mutex

	// branches holds every branch started, in order.
	branches []*branch

	// waiting holds the branches of the current group not started yet, and groups the groups after it.
	waiting []*branch
	groups  [][]*branch

	final     bool
	success   bool
	cancelled bool
	reason    []sip.Field
	answers   map[string]bool

	// stopped is set once a 2xx or 6xx arrived: no new branch may start (RFC 3261 §16.7 step 5).
	stopped bool

	// held is set while the owner of a branch holds a final response it will answer with itself.
	held bool

	// forks counts the forks opened, to tell whether the owner forwarded again on a held reply.
	forks int

	// best is the best final response so far (RFC 3261 §16.7 step 6), and challenges every 401
	// and 407 received (step 7).
	best       *sip.Response
	challenges []*sip.Response

	// generated holds the responses the proxy made up for branches that got none.
	generated map[*sip.Response]bool

	queue    []func()
	draining bool
}

type branch struct {
	c        *responseContext
	onReply  func(r Reply) Verdict
	timeout  time.Duration
	noAnswer time.Duration

	out     *sip.Request
	to      Target
	rr      *RecordRoute
	breadth int

	client    *transaction.ClientTransaction
	done      bool
	responded bool

	// settled is set once the branch has its final response: received, or taken as a 408 when
	// it expired (RFC 3261 §16.8), or failed. Until then it counts toward the Outgoing
	// Max-Breadth (RFC 5393 §5.3.2).
	settled bool

	// cancelling holds the Reason of a CANCEL due before the client transaction exists.
	cancelling []sip.Field
	cancelled  bool

	timerC, timer, noAnswerTimer transaction.Timer
	genC                         int

	dialog  *Dialog
	req     *sip.Request
	initial bool

	// delivered is set once the dialog learnt the branch failed.
	delivered bool

	// early holds the To tags of the early dialogs that came on the branch, and ended those a
	// 199 already ended (RFC 6228 §6).
	early, ended []string

	// retry holds the branches to try in turn in its place while it fails with 430.
	retry []*branch

	// expired is set once the branch rang past its Timer C or no-answer time: it was cancelled, and the final
	// response it ends with is delivered as a 408.
	expired bool

	key any
}

// retries reports whether res sends the request to b's next flow instead of ending b's search
// (RFC 5626 §7).
func (b *branch) retries(res *sip.Response) bool {
	return res != nil && res.StatusCode == 430 && len(b.retry) > 0
}

// errRangOut is the error of a branch that rang past its Timer C or no-answer time (RFC 3261 §16.8).
var errRangOut = fmt.Errorf("%w: rang out", transaction.ErrTimeout)

func newContext(p *Proxy, tx *transaction.ServerTransaction) *responseContext {
	return &responseContext{
		p: p, tx: tx, invite: tx.Request().Method == "INVITE",
		answers: make(map[string]bool), generated: make(map[*sip.Response]bool),
	}
}

// open installs the groups of a new fork; a context takes a new fork only once the earlier
// branches all ended without an answer the context relayed.
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

// RFC 5393 §5.3.2
func (c *responseContext) outgoingLocked() int {
	n := 0

	for _, b := range c.branches {
		if !b.settled {
			n += b.breadth
		}
	}

	return n
}

// pickLocked takes the branches that can start now: those of the current group, as far as the
// Max-Breadth left allows, or once every branch of it ended, those of the next group.
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

	// The branches started together share the Max-Breadth left; when it is too small for the
	// whole group, they get 1 each and the rest of the group follows as they end
	// (RFC 5393 §5.3.3, §5.5).
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

// start sends the branches picked; a branch that cannot be sent ends at once.
func (c *responseContext) start(bs []*branch) (int, []failure) {
	started := 0

	var failed []failure

	for _, b := range bs {
		if err := c.send(b); err != nil {
			failed = append(failed, failure{b: b, err: err})
			continue
		}

		started++
	}

	return started, failed
}

func (c *responseContext) send(b *branch) error {
	d := b.dialog

	if d != nil && !b.initial {
		tracked, err := d.request(b.out)
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

	client, err := c.p.layer.Request(b.out, b)
	if err != nil {
		c.drop(b)

		// The dialog learns of the failure here, whether or not the failure is dispatched later.
		if d := b.dialog; d != nil && !b.initial {
			d.response(b.out, Reply{Response: sip.NewResponse(b.out, 500, ""), Err: err})

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
	b.done, b.settled = true, true
	c.mu.Unlock()
}

// advance starts what can start; a branch that cannot be sent counts as answering with an error.
func (c *responseContext) advance() {
	for {
		c.mu.Lock()
		picked := c.pickLocked()
		c.mu.Unlock()

		if len(picked) == 0 {
			return
		}

		_, failed := c.start(picked)

		for _, f := range failed {
			c.dispatch(f.b, Reply{Response: c.generate(statusCode(f.err)), Err: f.err})
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

	if !b.done {
		if c.invite {
			b.startTimerC()

			if b.noAnswer > 0 {
				b.noAnswerTimer = c.p.clock.AfterFunc(b.noAnswer, b.noAnswerFired)
			}
		}

		if b.timeout > 0 && !b.stopsTimeout() {
			b.timer = c.p.clock.AfterFunc(b.timeout, b.timeoutFired)
		}
	}

	var (
		cancel bool
		reason []sip.Field
	)

	switch {
	case b.cancelled:
		cancel, reason = true, b.cancelling
	case c.cancelled && !b.done:
		cancel, reason = true, c.reason
	}

	c.mu.Unlock()

	if cancel {
		_ = client.Cancel(reason...)
	}
}

// cancelLocked marks b for a CANCEL and returns the client transaction to send it on, if it
// exists already.
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

func (x cancellation) send() {
	if x.client != nil {
		_ = x.client.Cancel(x.reason...)
	}
}

// stopLocked ends the search: pending groups never start, and every branch still live is
// cancelled (RFC 3261 §16.7 step 10, §16.10).
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

func sendAll(xs []cancellation) {
	for _, x := range xs {
		x.send()
	}
}

func (b *branch) startTimerC() {
	if b.timerC != nil {
		b.timerC.Stop()
	}

	b.genC++
	gen := b.genC
	b.timerC = b.c.p.clock.AfterFunc(b.c.p.timerC, func() { b.timerCFired(gen) })
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

// RFC 3261 §16.8
func (b *branch) timerCFired(gen int) {
	b.expire(func() bool { return gen == b.genC }, "Timer C")
}

func (b *branch) noAnswerFired() {
	b.expire(func() bool { return true }, "no answer")
}

func (b *branch) expire(current func() bool, cause string) {
	c := b.c

	c.mu.Lock()

	if b.done || b.expired || !current() {
		c.mu.Unlock()
		return
	}

	c.p.log.Debug("INVITE branch expired", slog.String("cause", cause), slog.String("request", c.tx.Request().StartLine()))

	// RFC 3261 §16.8: a branch with a provisional response is cancelled, and ends with the final response that
	// brings; until then it is live, and counts toward the Outgoing Max-Breadth (RFC 5393 §5.3.2). One without
	// is taken as answering with a 408.
	if b.responded {
		b.expired = true
		b.stopTimers()
		client := b.cancelLocked(nil)

		c.mu.Unlock()

		if client != nil {
			_ = client.Cancel()
		}

		return
	}

	b.finish()
	client := b.cancelLocked(nil)
	b.settled = true

	c.mu.Unlock()

	if client != nil {
		_ = client.Cancel()
	}

	c.dispatch(b, Reply{Response: c.generate(408), Err: fmt.Errorf("%w: %s", transaction.ErrTimeout, cause)})
}

func (b *branch) timeoutFired() {
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
			_ = client.Cancel()
		}

		res = c.generate(408)
	}

	c.dispatch(b, Reply{Response: res, Err: fmt.Errorf("%w: no response within %s", transaction.ErrTimeout, b.timeout), Responded: responded})
}

func (b *branch) stopsTimeout() bool {
	return b.c.invite && b.responded
}

// settleLocked records that the client transaction has its final outcome, and reports whether
// that frees Max-Breadth for branches still waiting (RFC 5393 §5.3.3.1).
func (b *branch) settleLocked() bool {
	if b.settled {
		return false
	}

	b.settled = true

	return len(b.c.waiting) > 0
}

func (b *branch) HandleResponse(res *sip.Response) {
	c := b.c

	res = res.Clone()

	if _, ok := res.Header.PopFirst("Via"); !ok || !res.Header.Has("Via") {
		return
	}

	c.mu.Lock()

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
		// A branch the context cancelled once it had its answer ends here, unseen: its timers stop, so it cannot
		// expire into a 408 later.
		if b.done || c.final {
			b.finish()
			c.mu.Unlock()

			if freed {
				c.advance()
			}

			return
		}

		b.finish()

		// The branch rang out and was cancelled: whatever final response it ends with stands for the timeout.
		if b.expired {
			c.mu.Unlock()

			c.dispatch(b, Reply{Response: c.generate(408), Err: errRangOut, Responded: true})

			if freed {
				c.advance()
			}

			return
		}
	}

	c.mu.Unlock()

	c.dispatch(b, Reply{Response: res, Responded: true})

	if freed {
		c.advance()
	}
}

func (b *branch) HandleError(err error) {
	c := b.c

	c.mu.Lock()

	freed := b.settleLocked()

	if b.done || c.final {
		b.finish()
		c.mu.Unlock()

		if freed {
			c.advance()
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
	case b.cancelled || c.cancelled:
		res = c.generateLocked(487)
	case errors.Is(err, transaction.ErrTimeout):
		res = c.generateLocked(408)
	default:
		res = c.generateLocked(500)
	}

	c.mu.Unlock()

	c.p.log.Debug("proxied request failed", slog.String("request", c.tx.Request().StartLine()), slog.Any("error", err))
	c.dispatch(b, Reply{Response: res, Err: err, Responded: responded})

	if freed {
		c.advance()
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

func (c *responseContext) dispatch(b *branch, r Reply) {
	c.mu.Lock()

	c.queue = append(c.queue, func() { c.deliver(b, r) })

	if c.draining {
		c.mu.Unlock()
		return
	}

	c.draining = true

	for len(c.queue) > 0 {
		f := c.queue[0]
		c.queue[0] = nil
		c.queue = c.queue[1:]

		c.mu.Unlock()
		f()
		c.mu.Lock()
	}

	c.draining = false
	c.mu.Unlock()
}

func (c *responseContext) deliver(b *branch, r Reply) {
	if d := b.dialog; d != nil {
		c.toDialog(d, b, r)
	}

	if b.onReply == nil {
		c.relayReply(b, r)
		return
	}

	c.mu.Lock()
	forks := c.forks
	c.mu.Unlock()

	if b.onReply(r) == Relay {
		c.relayReply(b, r)
		return
	}

	if r.Response == nil || !r.Response.IsProvisional() {
		c.mu.Lock()
		c.held = !c.final && c.forks == forks
		c.mu.Unlock()
	}
}

// toDialog passes a response to the dialog the branch carries. A failed branch of the initial
// INVITE ends only its early dialogs; the dialog fails with the INVITE, once no branch is left.
func (c *responseContext) toDialog(d *Dialog, b *branch, r Reply) {
	res := r.Response

	switch {
	case !b.initial:
		d.response(b.req, r)
		return
	case res != nil && (res.IsProvisional() || res.IsSuccess()):
		d.inviteResponse(b, res)
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
		d.failed(best, downstream)
	}
}

func (c *responseContext) relayReply(b *branch, r Reply) {
	if err := c.relayFrom(b, r.Response); err != nil && !errors.Is(err, ErrAnswered) {
		c.p.log.Debug("relaying a response failed", slog.Any("error", err))
	}
}

// relay answers with a response its owner chose, ending the search: branches still live are
// cancelled.
func (c *responseContext) relay(res *sip.Response) error {
	return c.relayFrom(nil, res)
}

// RFC 3261 §16.7
func (c *responseContext) relayFrom(b *branch, res *sip.Response) error {
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

		sendAll(cancels)
		c.p.forget(c)

		return err
	case c.final:
		c.mu.Unlock()
		return ErrAnswered
	case b == nil:
		c.final, c.stopped, c.held = true, true, false
		cancels := c.stopLocked(nil)
		c.mu.Unlock()

		sendAll(cancels)

		return c.finalize(res)
	case res != nil && res.IsSuccess():
		c.final, c.stopped = true, true
		cancels := c.stopLocked(nil)
		c.mu.Unlock()

		sendAll(cancels)

		return c.finalize(res)
	}

	if b.retries(res) && !c.stopped && !c.cancelled {
		// The 430 stays a candidate: should every flow fail, it is the answer (RFC 5626 §11.5).
		c.considerLocked(res)

		next := b.retry[0]
		next.retry = b.retry[1:]
		c.waiting = append([]*branch{next}, c.waiting...)

		c.mu.Unlock()

		c.advance()

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

	sendAll(cancels)
	c.advance()

	err := c.conclude()

	if res != nil {
		c.earlyTerminated(b, res)
	}

	return err
}

// earlyTerminated tells the caller, with a 199, of each early dialog of branch b that the final
// response res ended, when that response does not go upstream at once (RFC 6228 §6, TS 24.229
// §5.4.3.3).
func (c *responseContext) earlyTerminated(b *branch, res *sip.Response) {
	in := c.tx.Request()

	if !c.invite || !has(in.Header, "Supported", "199") || has(in.Header, "Require", "100rel") ||
		has(in.Header, "Proxy-Require", "100rel") {
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

func has(h sip.Header, name, tag string) bool {
	return slices.ContainsFunc(h.Elements(name), func(e string) bool { return strings.EqualFold(strings.TrimSpace(e), tag) })
}

// considerLocked weighs a final response against the best one so far (RFC 3261 §16.7 step 6).
func (c *responseContext) considerLocked(res *sip.Response) {
	if res.StatusCode == 401 || res.StatusCode == 407 {
		c.challenges = append(c.challenges, res)
	}

	c.best = c.bestWithLocked(res)
}

// bestWithLocked is the best final response once res is weighed too. Within what better leaves equal, it prefers
// a response a branch returned to one the context generated, such as the 408 of a branch that rang out.
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

// better reports whether a final response with code is better than one with than: 6xx first,
// then the lowest class, and in the 4xx class those that tell how to resubmit the request.
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

// resubmission reports a response that tells the client how to resubmit the request.
func resubmission(code int) bool {
	switch code {
	case 401, 407, 415, 420, 484:
		return true
	}

	return false
}

// conclude sends the best response once every branch ended and no group is left (RFC 3261 §16.7
// step 5).
func (c *responseContext) conclude() error {
	c.mu.Lock()

	if c.final || c.held || c.liveLocked() || len(c.waiting) > 0 || len(c.groups) > 0 {
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

// RFC 3261 §16.7 step 7
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

// finalize relays the final response. A 503 goes upstream as a 500 (RFC 3261 §16.7 step 6), and a
// 430, meant for the proxy holding the registration, as a 480 (RFC 5626 §11.5).
func (c *responseContext) finalize(res *sip.Response) error {
	switch res.StatusCode {
	case 503:
		res = c.generate(500)
	case 430:
		res = c.generate(480)
	}

	err := c.tx.Relay(res)
	c.p.forget(c)

	return err
}

var completedElsewhere = reasonFor(200)

// TS 24.229 §5.4.3.3, RFC 3326
func reasonFor(code int) []sip.Field {
	r, err := sip.NewReason(sip.ReasonSIP, code, sip.ReasonText(sip.ReasonSIP, code))
	if err != nil {
		return nil
	}

	return []sip.Field{{Name: "Reason", Value: r.String()}}
}

// RFC 3261 §16.10
func (c *responseContext) cancel(reason []sip.Field) {
	c.mu.Lock()

	if c.final || c.success || c.cancelled {
		c.mu.Unlock()
		return
	}

	c.cancelled = true
	c.reason = reason

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

	sendAll(cancels)
}

// withdraw ends the search toward the branches with key: those not yet sent never are, and those
// ringing are cancelled with reason (RFC 3261 §16.10). It changes nothing when no branch with key is
// pending or ringing (found is false), or when no other branch would be left to answer (others is
// false).
func (c *responseContext) withdraw(key any, reason []sip.Field) (found, others bool) {
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

		if client := b.cancelLocked(reason); client != nil {
			cancels = append(cancels, cancellation{client: client, reason: reason})
		}
	}

	c.mu.Unlock()

	sendAll(cancels)

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
