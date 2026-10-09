// Package callrecords keeps a record of each call attempt, which the nodes on the call's path add to by its ICID.
package callrecords

import (
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
	// DefaultMaxUnsaved is how many records of ended calls wait to be saved before the recorder drops those of
	// the calls that end next.
	DefaultMaxUnsaved = 100_000

	// maxBatch is how many records the writer saves in one transaction.
	maxBatch = 512

	minRetry = 50 * time.Millisecond
	maxRetry = 5 * time.Second
)

type Store interface {
	SaveCallRecords(ctx context.Context, records []*db.CallRecord, deleted []int64) ([]error, error)
	CloseOpenCallRecords(ctx context.Context, live []string) (int64, error)
}

type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type Config struct {
	Store Store
	// MaxUnsaved is how many records of ended calls wait to be saved, DefaultMaxUnsaved if 0.
	MaxUnsaved int
	Clock      Clock
	Logger     *slog.Logger
}

// Attempt is an INVITE from a registered UE, as the originating P-CSCF received it.
type Attempt struct {
	ICID string
	// CallID is the Call-ID of the INVITE.
	CallID string
	// RequestURI is the Request-URI of the INVITE.
	RequestURI string
	// IMPI is the private identity of the caller's registration.
	IMPI string
	// Asserted are the identities the P-CSCF asserts for the caller.
	Asserted []string
}

// Routing is the INVITE as the originating S-CSCF sends it on.
type Routing struct {
	// Asserted are the P-Asserted-Identity values of the INVITE.
	Asserted []string
	// RequestURI is the Request-URI of the INVITE, normalised.
	RequestURI string
}

// End is how the originating P-CSCF's dialog of a call ended.
type End struct {
	// Code is the final status of the INVITE that the caller got. It is only read for a call that was not
	// answered, and a 0 there leaves the record incomplete.
	Code  int
	By    proxy.Side
	Cause proxy.EndCause
}

// Recorder keeps the records of calls. The nodes report what they see of a call by its ICID, from SIP handlers,
// which must not wait on the database: a report only changes the record in memory, and a writer saves the
// records that changed. When the database cannot keep up, the records of ended calls wait in memory, up to a
// limit past which the recorder drops them: the writer then closes them as incomplete once it catches up. A nil
// Recorder records nothing.
type Recorder struct {
	store      Store
	clock      Clock
	log        *slog.Logger
	maxUnsaved int

	// wake tells the writer that records changed.
	wake chan struct{}
	stop chan struct{}
	done chan struct{}

	mu sync.Mutex
	// calls are the records of the calls in progress, and of those that ended until they are saved.
	calls map[string]*call
	// changed are the records changed since the writer last took them, in the order they first changed.
	changed []*call
	// unsaved is how many records of ended calls are in calls.
	unsaved int
	// dropped is how many records of ended calls were dropped since the writer last logged it.
	dropped int
	// orphaned reports that records may have been left open in the database for calls the recorder no longer
	// keeps, which the writer closes once it has saved the others.
	orphaned bool
	closed   bool
}

type call struct {
	rec db.CallRecord
	// network reports that the IMS released the call.
	network bool
	// rejecting are the statuses the nodes of the IMS answered the INVITE with themselves, as the caller gets them:
	// the call ended on one of them ended by the network.
	rejecting []int
	ended     bool
	// discard reports an attempt that the caller makes again, whose record is deleted rather than saved.
	discard bool
	changed bool
}

// New starts a recorder, which Close stops.
func New(cfg Config) *Recorder {
	r := &Recorder{
		store: cfg.Store, clock: cfg.Clock, log: cfg.Logger, maxUnsaved: cfg.MaxUnsaved,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
		calls: map[string]*call{},
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

// Close saves the records that changed, including those of the calls in progress as they are, then stops the
// recorder. It ignores what is reported after.
func (r *Recorder) Close() {
	if r == nil {
		return
	}

	r.mu.Lock()

	if !r.closed {
		r.closed = true
		close(r.stop)
	}

	r.mu.Unlock()

	<-r.done
}

// Attempt opens the record of a call, at the originating P-CSCF, for every INVITE from a registered UE.
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
		RequestedParty: a.RequestURI, RequestedAt: r.clock.Now(),
	}}
	r.calls[a.ICID] = c
	r.changeLocked(c)
}

// Rejecting reports that a node of the IMS answers the INVITE itself with an error status, before it does. The
// call is then ended by the network if the caller gets that status: a CANCEL that came first makes it end on
// the 487 instead.
func (r *Recorder) Rejecting(icid string, code int) {
	r.update(icid, func(c *call, _ time.Time) {
		if !slices.Contains(c.rejecting, code) {
			c.rejecting = append(c.rejecting, code)
		}
	})
}

// RejectingRequest reports that a node of the IMS behind the originating P-CSCF answers a request itself with a
// status, before it does, when the request is the INVITE that starts a call and the status ends it.
func (r *Recorder) RejectingRequest(req *sip.Request, code int) {
	if r == nil || req.Method != "INVITE" || code < 300 {
		return
	}

	if to, err := req.Header.To(); err != nil || to.Tag() != "" {
		return
	}

	// The proxies on the way back to the caller change some statuses.
	r.Rejecting(req.Header.ICID(), proxy.UpstreamStatus(code))
}

// Routed reports the INVITE as the originating S-CSCF sends it on.
func (r *Recorder) Routed(icid string, rt Routing) {
	asserted := slices.Clone(rt.Asserted)

	r.update(icid, func(c *call, _ time.Time) {
		c.rec.CallingParty = asserted
		c.rec.CalledParty = rt.RequestURI
	})
}

// Reached reports the private identity of the registration that answered, at the terminating P-CSCF.
func (r *Recorder) Reached(icid string, impi string) {
	r.update(icid, func(c *call, _ time.Time) {
		c.rec.CalleeIMPI = impi
	})
}

// Alerted reports a 180 relayed to the caller.
func (r *Recorder) Alerted(icid string) {
	r.update(icid, func(c *call, _ time.Time) {
		c.rec.Alerted = true
	})
}

// Answered reports the 2xx relayed to the caller.
func (r *Recorder) Answered(icid string, code int) {
	r.update(icid, func(c *call, now time.Time) {
		if c.rec.SIPStatus == 0 {
			c.rec.SIPStatus, c.rec.Outcome, c.rec.DeliveryStartAt = code, db.OutcomeAnswered, now
		}
	})
}

// Media reports the media types of the m= lines that an SDP answer accepted, with a non-zero port.
func (r *Recorder) Media(icid string, types []string) {
	r.update(icid, func(c *call, _ time.Time) {
		for _, t := range types {
			if !slices.Contains(c.rec.Media, t) {
				c.rec.Media = append(c.rec.Media, t)
			}
		}
	})
}

// Released reports that a node of the IMS is releasing the call, before it does: the BYE or final response that
// the release makes reach the caller from downstream, as if from the callee.
func (r *Recorder) Released(icid string) {
	r.update(icid, func(c *call, _ time.Time) {
		c.network = true
	})
}

// Ended reports the end of the originating P-CSCF's dialog of the call, or the final response the P-CSCF sent
// the caller itself, without a dialog.
func (r *Recorder) Ended(icid string, e End) {
	r.update(icid, func(c *call, now time.Time) {
		by := EndedBy(e.Cause, e.By)
		if c.network || c.rec.SIPStatus == 0 && slices.Contains(c.rejecting, e.Code) {
			by = db.PartyNetwork
		}

		r.endLocked(c, e.Code, by, now)
	})
}

// CloseOpen closes the records of the calls in progress as incomplete, for when the IMS loses them.
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
			r.changeLocked(c)
		}
	}
}

// update changes the record of a call in progress.
func (r *Recorder) update(icid string, change func(c *call, now time.Time)) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.calls[icid]
	if r.closed || !ok || c.ended {
		return
	}

	change(c, r.clock.Now())

	// endLocked may have dropped it.
	if r.calls[icid] == c {
		r.changeLocked(c)
	}
}

// endLocked ends the record of a call with the final status of its INVITE, which only counts if it was not
// answered. A status the caller retries on discards the record. It drops the record instead when too many ended
// ones are not saved yet: the record is then left as last saved, open, until the writer closes it as incomplete.
func (r *Recorder) endLocked(c *call, code int, by db.CallParty, now time.Time) {
	if r.unsaved >= r.maxUnsaved {
		r.dropLocked(c)
		r.dropped++
		r.orphanedLocked()

		return
	}

	c.ended = true
	r.unsaved++

	switch {
	case c.rec.SIPStatus != 0:
		c.rec.DeliveryEndAt = now
	case Retried(code):
		c.discard = true
		return
	case code < 200 || code > 699:
		// The caller's final response is unknown.
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
}

func (r *Recorder) changeLocked(c *call) {
	if c.changed {
		return
	}

	c.changed = true
	r.changed = append(r.changed, c)
	r.wakeLocked()
}

// orphanedLocked notes that a record may be left open in the database without a call the recorder keeps.
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

// dropLocked forgets a record. It may stay in changed, which the writer skips once it is no longer changed.
func (r *Recorder) dropLocked(c *call) {
	if c.ended {
		r.unsaved--
	}

	c.changed = false
	delete(r.calls, c.rec.ICID)
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

// flush saves the records that changed, and closes those dropped calls left open, and gives up at the first
// failure, so that stopping does not wait on a failing database.
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

// sync saves a batch of the records that changed, or once all are saved closes those left open by calls the
// recorder dropped, and reports whether work is left.
func (r *Recorder) sync() (more bool, err error) {
	if more, err = r.save(); err != nil || more {
		return more, err
	}

	return false, r.closeOrphans()
}

// closeOrphans closes as incomplete the records left open in the database by calls the recorder dropped: those
// open but of the calls it keeps. The writer alone inserts records, so none of a call that starts meanwhile is
// in the database yet.
func (r *Recorder) closeOrphans() error {
	r.mu.Lock()

	if !r.orphaned {
		r.mu.Unlock()
		return nil
	}

	r.orphaned = false
	live := slices.Collect(maps.Keys(r.calls))

	r.mu.Unlock()

	n, err := r.store.CloseOpenCallRecords(context.Background(), live)
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

// save saves a batch of the records that changed, and deletes those discarded, and reports whether others are
// left. When the transaction fails, the records stay changed, to be saved again.
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
			// No batch is being saved, so one never saved is not in the database.
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

	errs, err := r.store.SaveCallRecords(context.Background(), records, deleted)

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
			// Saving it again would fail again.
			r.log.Error("dropped a call record that cannot be saved", slog.Any("error", errs[i]))
			r.dropLocked(c)

			// One saved before is left as it was then, maybe open.
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
