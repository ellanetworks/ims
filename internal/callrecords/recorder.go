package callrecords

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
)

const (
	DefaultMaxUnsaved = 100_000

	maxBatch = 512

	minRetry = 50 * time.Millisecond
	maxRetry = 5 * time.Second

	writeTimeout = 10 * time.Second
)

type Store interface {
	SaveCallRecords(ctx context.Context, records []*db.CallRecord, deleted []int64) ([]error, error)
	CloseOpenCallRecords(ctx context.Context, nodeID string, live []string) (int64, error)
}

type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type Config struct {
	Store      Store
	NodeID     string
	MaxUnsaved int
	Clock      Clock
	Logger     *slog.Logger
}

type Attempt struct {
	ICID       string
	CallID     string
	RequestURI string
	IMPI       string
	Asserted   []string
	InTrunk    string
	InName     string
}

type TrunkSide int

const (
	Inbound TrunkSide = iota + 1
	Outbound
)

type Routing struct {
	Asserted   []string
	RequestURI string
}

type End struct {
	Code  int
	Tag   string
	By    proxy.Side
	Cause proxy.EndCause
}

type Recorder struct {
	store      Store
	nodeID     string
	clock      Clock
	log        *slog.Logger
	maxUnsaved int
	metrics    metrics

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	stop   chan struct{}
	done   chan struct{}

	mu       sync.Mutex
	calls    map[string]*call
	sessions map[string]*call
	changed  []*call
	unsaved  int
	dropped  int
	orphaned bool
	closed   bool
}

type rejection struct {
	code      int
	tag       string
	rewritten bool
}

func (j rejection) matches(code int, tag string) bool {
	return j.code == code && (j.tag == tag || j.rewritten)
}

type call struct {
	rec        db.CallRecord
	network    bool
	rejecting  []rejection
	ended      bool
	superseded bool
	discard    bool
	changed    bool
}

func New(cfg Config) *Recorder {
	ctx, cancel := context.WithCancel(context.Background())

	r := &Recorder{
		ctx: ctx, cancel: cancel,
		store: cfg.Store, nodeID: cfg.NodeID, clock: cfg.Clock, log: cfg.Logger, maxUnsaved: cfg.MaxUnsaved,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
		calls: map[string]*call{}, sessions: map[string]*call{}, metrics: newMetrics(),
	}

	if r.clock == nil {
		r.clock = systemClock{}
	}

	if r.log == nil {
		r.log = slog.Default()
	}

	if r.maxUnsaved <= 0 {
		r.maxUnsaved = DefaultMaxUnsaved
	}

	go r.write()

	return r
}

func (r *Recorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}

	r.mu.Lock()

	if !r.closed {
		r.closed = true

		for _, c := range r.calls {
			if c.rec.ID == 0 && !c.ended {
				r.changeLocked(c)
			}
		}

		close(r.stop)
	}

	r.mu.Unlock()

	stop := context.AfterFunc(ctx, r.cancel)

	<-r.done

	gaveUp := !stop()

	r.cancel()

	if gaveUp {
		return ctx.Err()
	}

	return nil
}

func (r *Recorder) Attempt(a Attempt) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return
	}

	if _, ok := r.calls[a.ICID]; ok {
		r.log.Warn("call attempt with the ICID of another", slog.String("icid", a.ICID))
		return
	}

	c := &call{rec: db.CallRecord{
		ICID: a.ICID, SessionID: a.CallID, CallingParty: slices.Clone(a.Asserted), CallerIMPI: a.IMPI,
		RequestedParty: a.RequestURI, RequestedAt: r.clock.Now(), InTrunk: a.InTrunk, InTrunkName: trunkName(a.InTrunk, a.InName),
		NodeID: r.nodeID,
	}}
	r.calls[a.ICID] = c
	r.metrics.active.Inc()

	key := sessionKey(a.IMPI, a.CallID)
	if prev, ok := r.sessions[key]; ok && prev.rec.SIPStatus/100 != 2 {
		prev.superseded = true

		if prev.ended {
			prev.discard = true
			r.changeLocked(prev)
		}
	}

	r.sessions[key] = c
}

func sessionKey(impi, callID string) string {
	return impi + "\x00" + callID
}

func (r *Recorder) Rejecting(icid string, code int, tag string) {
	r.reject(icid, rejection{code: code, tag: tag})
}

func (r *Recorder) reject(icid string, j rejection) {
	r.update(icid, func(c *call, _ time.Time) bool {
		if !slices.Contains(c.rejecting, j) {
			c.rejecting = append(c.rejecting, j)
		}

		return false
	})
}

func (r *Recorder) RejectingRequest(req *sip.Request, code int, tag string) {
	if r == nil || req.Method != "INVITE" || code < 300 {
		return
	}

	if to, err := req.Header.To(); err != nil || to.Tag() != "" {
		return
	}

	up := proxy.UpstreamStatus(code)
	r.reject(req.Header.ICID(), rejection{code: up, tag: tag, rewritten: up != code})
}

func (r *Recorder) Routed(icid string, rt Routing) {
	asserted := slices.Clone(rt.Asserted)

	r.update(icid, func(c *call, _ time.Time) bool {
		c.rec.CallingParty = asserted
		c.rec.CalledParty = rt.RequestURI

		return false
	})
}

func (r *Recorder) Trunk(icid string, side TrunkSide, id, name string) {
	if id == "" {
		return
	}

	name = trunkName(id, name)

	r.update(icid, func(c *call, _ time.Time) bool {
		switch side {
		case Inbound:
			c.rec.InTrunk, c.rec.InTrunkName = id, name
		case Outbound:
			c.rec.OutTrunk, c.rec.OutTrunkName = id, name
		}

		return false
	})
}

func trunkName(id, name string) string {
	if id == "" {
		return ""
	}

	return cmp.Or(name, id)
}

func (r *Recorder) Reached(icid string, impi string) {
	r.update(icid, func(c *call, _ time.Time) bool {
		c.rec.CalleeIMPI = impi
		return false
	})
}

func (r *Recorder) Alerted(icid string) {
	r.update(icid, func(c *call, _ time.Time) bool {
		c.rec.Alerted = true
		return false
	})
}

func (r *Recorder) Answered(icid string, code int) {
	r.update(icid, func(c *call, now time.Time) bool {
		if c.rec.SIPStatus != 0 {
			return false
		}

		c.rec.SIPStatus, c.rec.Outcome, c.rec.DeliveryStartAt = code, db.OutcomeAnswered, now

		return true
	})
}

func (r *Recorder) Media(icid string, types []string) {
	r.update(icid, func(c *call, _ time.Time) bool {
		for _, t := range types {
			if !slices.Contains(c.rec.Media, t) {
				c.rec.Media = append(c.rec.Media, t)
			}
		}

		return false
	})
}

func (r *Recorder) Released(icid string) {
	r.update(icid, func(c *call, _ time.Time) bool {
		c.network = true
		return false
	})
}

func (r *Recorder) Ended(icid string, e End) {
	r.update(icid, func(c *call, now time.Time) bool {
		by := EndedBy(e.Cause, e.By)
		if c.network || c.rec.SIPStatus == 0 && slices.ContainsFunc(c.rejecting, func(j rejection) bool {
			return j.matches(e.Code, e.Tag)
		}) {
			by = db.PartyNetwork
		}

		r.endLocked(c, e.Code, by, now)

		return true
	})
}

func (r *Recorder) CloseOpen() {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, c := range r.calls {
		if !c.ended {
			c.ended, c.rec.Incomplete = true, true
			r.unsaved++
			r.metrics.active.Dec()
			r.changeLocked(c)
		}
	}
}

func (r *Recorder) update(icid string, change func(c *call, now time.Time) bool) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.calls[icid]
	if r.closed || !ok || c.ended {
		return
	}

	if milestone := change(c, r.clock.Now()); milestone && r.calls[icid] == c {
		r.changeLocked(c)
	}
}

func (r *Recorder) endLocked(c *call, code int, by db.CallParty, now time.Time) {
	if r.unsaved >= r.maxUnsaved {
		r.dropLocked(c)
		r.dropped++
		r.orphanedLocked()

		return
	}

	c.ended = true
	r.unsaved++
	r.metrics.active.Dec()

	switch {
	case c.rec.SIPStatus != 0:
		c.rec.DeliveryEndAt = now
	case c.superseded:
		c.discard = true
		return
	case code < 200 || code > 699:
		c.rec.Incomplete = true
		return
	default:
		c.rec.SIPStatus, c.rec.DeliveryStartAt = code, now
		if code < 300 {
			c.rec.DeliveryEndAt = now
		}
	}

	c.rec.EndedBy = by
	c.rec.Outcome = Outcome(c.rec.SIPStatus, by, c.rec.Alerted)
	r.metrics.calls.WithLabelValues(string(c.rec.Outcome)).Inc()
}

func (r *Recorder) changeLocked(c *call) {
	if c.changed {
		return
	}

	c.changed = true
	r.changed = append(r.changed, c)
	r.wakeLocked()
}

func (r *Recorder) orphanedLocked() {
	r.orphaned = true
	r.wakeLocked()
}

func (r *Recorder) wakeLocked() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Recorder) dropLocked(c *call) {
	if c.ended {
		r.unsaved--
	} else {
		r.metrics.active.Dec()
	}

	c.changed = false
	delete(r.calls, c.rec.ICID)

	if key := sessionKey(c.rec.CallerIMPI, c.rec.SessionID); r.sessions[key] == c {
		delete(r.sessions, key)
	}
}

func (r *Recorder) write() {
	defer close(r.done)

	retry := minRetry

	for {
		select {
		case <-r.wake:
		case <-r.stop:
			r.flush()
			return
		}

		for {
			more, err := r.sync()
			if err == nil {
				retry = minRetry

				if !more {
					break
				}

				continue
			}

			r.log.Error("saving call records failed", slog.Any("error", err))

			select {
			case <-time.After(retry):
				retry = min(2*retry, maxRetry)
			case <-r.stop:
				r.flush()
				return
			}
		}
	}
}

func (r *Recorder) flush() {
	for {
		more, err := r.sync()
		if err != nil {
			r.log.Error("saving call records failed", slog.Any("error", err))
			return
		}

		if !more {
			return
		}
	}
}

func (r *Recorder) sync() (more bool, err error) {
	if more, err = r.save(); err != nil || more {
		return more, err
	}

	return false, r.closeOrphans()
}

func (r *Recorder) closeOrphans() error {
	r.mu.Lock()

	if !r.orphaned {
		r.mu.Unlock()
		return nil
	}

	r.orphaned = false
	live := slices.Collect(maps.Keys(r.calls))

	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(r.ctx, writeTimeout)
	n, err := r.store.CloseOpenCallRecords(ctx, r.nodeID, live)

	cancel()

	if err != nil {
		r.mu.Lock()
		r.orphaned = true
		r.mu.Unlock()

		return err
	}

	if n > 0 {
		r.log.Warn("closed the call records of dropped calls as incomplete", slog.Int64("records", n))
	}

	return nil
}

func (r *Recorder) save() (more bool, err error) {
	r.mu.Lock()

	if r.dropped > 0 {
		r.log.Warn("dropped call records, the database is not keeping up", slog.Int("records", r.dropped))
		r.dropped = 0
	}

	var (
		batch     []*call
		records   []*db.CallRecord
		discarded []*call
		deleted   []int64
	)

	for len(r.changed) > 0 && len(batch)+len(discarded) < maxBatch {
		c := r.changed[0]
		r.changed[0] = nil
		r.changed = r.changed[1:]

		if !c.changed {
			continue
		}

		c.changed = false

		if c.discard {
			if c.rec.ID == 0 {
				r.dropLocked(c)
			} else {
				discarded = append(discarded, c)
				deleted = append(deleted, c.rec.ID)
			}

			continue
		}

		rec := c.rec
		rec.CallingParty, rec.Media = slices.Clone(rec.CallingParty), slices.Clone(rec.Media)

		batch = append(batch, c)
		records = append(records, &rec)
	}

	r.mu.Unlock()

	if len(batch) == 0 && len(deleted) == 0 {
		return false, nil
	}

	ctx, cancel := context.WithTimeout(r.ctx, writeTimeout)
	errs, err := r.store.SaveCallRecords(ctx, records, deleted)

	cancel()

	r.mu.Lock()
	defer r.mu.Unlock()

	if err != nil {
		for _, c := range slices.Concat(batch, discarded) {
			if r.calls[c.rec.ICID] == c {
				r.changeLocked(c)
			}
		}

		return false, err
	}

	for _, c := range discarded {
		if r.calls[c.rec.ICID] == c {
			r.dropLocked(c)
		}
	}

	for i, c := range batch {
		if r.calls[c.rec.ICID] != c {
			continue
		}

		if errs != nil && errs[i] != nil {
			r.log.Error("dropped a call record that cannot be saved", slog.Any("error", errs[i]))
			r.dropLocked(c)

			if c.rec.ID != 0 {
				r.orphaned = true
			}

			continue
		}

		c.rec.ID = records[i].ID

		if c.ended && !c.changed {
			r.dropLocked(c)
		}
	}

	return len(r.changed) > 0, nil
}
