package ua

import (
	"crypto/rand"
	"encoding/hex"
	"hash/maphash"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// Published is event state a publication established at a PublicationStore (RFC 3903 §6).
type Published struct {
	ETag    string
	Bodies  []sip.BodyPart
	Expires time.Time
}

// StoreConfig configures a PublicationStore.
type StoreConfig struct {
	// Events are the event packages the store keeps state for; a PUBLISH for another gets a 489 (RFC 3903 §6 step 2).
	Events []string

	// Resource reports whether the store keeps state for a Request-URI; a PUBLISH for another gets a 404 (step 1).
	// Without it, the store keeps state for all.
	Resource func(uri sip.URI) bool

	// Check accepts the state of a PUBLISH that carries one, or returns the code to refuse it with: 415 for a body
	// of a type the event package does not define (step 5), 403 for a publisher not authorised. Without it, every
	// state is accepted.
	Check func(r *Request) int

	// MinExpires refuses with a 423 a publication asking for less (step 4); DefaultExpires is the duration of one
	// asking for none, required; MaxExpires cuts longer ones, unless zero. Durations up to 2^32-1 s are kept.
	MinExpires     time.Duration
	DefaultExpires time.Duration
	MaxExpires     time.Duration

	// Changed reports the state of a resource and event package after it changed: a publication established,
	// modified, removed or expired, with the publications it has now, none once the last one went. It runs on the
	// strand of the resource, in the order of the changes, outside its lock: a compositor notifies the subscribers
	// of the resource from it.
	Changed func(uri sip.URI, event string, state []Published)
}

// PublicationStore keeps the event state that publications establish, as an event state compositor (RFC 3903 §6):
// their entity-tags, bodies and expiries, by Request-URI and event package. The agent processes the PUBLISHes of a
// Request-URI on one strand, in the order they come (§6), each atomically.
type PublicationStore struct {
	a   *Agent
	cfg StoreConfig

	mu        sync.Mutex
	resources map[storeKey]map[string]*stored
}

type storeKey struct {
	uri, event string
}

type stored struct {
	Published

	timer transaction.Timer
}

// NewPublicationStore makes a store of the agent; the application gives it the PUBLISHes of OnRequest.
func (a *Agent) NewPublicationStore(cfg StoreConfig) *PublicationStore {
	return &PublicationStore{a: a, cfg: cfg, resources: map[storeKey]map[string]*stored{}}
}

// resourceOf is the resource a Request-URI designates: its scheme, user, host and port, the host case-insensitive.
func resourceOf(u sip.URI) string {
	k := u.Scheme + ":" + u.User + "@" + strings.ToLower(u.Host)
	if u.Port != 0 {
		k += ":" + strconv.Itoa(int(u.Port))
	}

	return k
}

// State is the state of a resource for an event package: its publications in force.
func (ps *PublicationStore) State(uri sip.URI, event string) []Published {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	return ps.stateLocked(storeKey{uri: resourceOf(uri), event: event})
}

func (ps *PublicationStore) stateLocked(k storeKey) []Published {
	var out []Published

	for _, p := range ps.resources[k] {
		out = append(out, p.Published)
	}

	slices.SortFunc(out, func(a, b Published) int { return a.Expires.Compare(b.Expires) })

	return out
}

// Handle processes a PUBLISH (RFC 3903 §6) and responds to it, then reports the change, if any. The application
// calls it from OnRequest, on the strand of the Request-URI.
func (ps *PublicationStore) Handle(r *Request) {
	code, header, changed := ps.process(r)

	if err := r.respondStateless(Reply{Code: code, Header: header}); err != nil {
		r.logger().Debug("PUBLISH response failed", slog.Any("error", err))
	}

	if changed != nil {
		changed()
	}
}

// process applies a PUBLISH to the store, atomically, and returns the response, and the report of the change.
func (ps *PublicationStore) process(r *Request) (int, sip.Header, func()) {
	req := r.req
	cfg := ps.cfg

	// Step 1.
	if cfg.Resource != nil && !cfg.Resource(req.URI) {
		return 404, nil, nil
	}

	// Step 2.
	ev, ok, err := req.Header.Event()
	if !ok || err != nil || !slices.Contains(cfg.Events, ev.Type()) {
		return 489, sip.Header{{Name: "Allow-Events", Value: strings.Join(cfg.Events, ", ")}}, nil
	}

	// Step 4, before the state changes.
	expires := cfg.DefaultExpires

	if v, err := req.Header.Expires(); err == nil {
		expires = time.Duration(v) * time.Second

		if expires > 0 && expires < cfg.MinExpires {
			return 423, sip.Header{{Name: "Min-Expires", Value: strconvSeconds(cfg.MinExpires)}}, nil
		}
	}

	if cfg.MaxExpires > 0 && expires > cfg.MaxExpires {
		expires = cfg.MaxExpires
	}

	parts, err := req.Content().Parts()
	if err != nil {
		return 400, nil, nil
	}

	// Step 5: the state of a PUBLISH that carries one is accepted first.
	if len(parts) > 0 && cfg.Check != nil {
		if code := cfg.Check(r); code != 0 {
			return code, nil, nil
		}
	}

	k := storeKey{uri: resourceOf(req.URI), event: ev.Type()}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	pubs := ps.resources[k]

	// Step 3.
	etag, conditional, err := req.Header.SIPIfMatch()

	var p *stored

	switch {
	case err != nil:
		return 400, nil, nil
	case conditional:
		if p = pubs[etag]; p == nil {
			return 412, nil, nil
		}
	case len(parts) == 0:
		// An initial publication carries the state.
		return 400, nil, nil
	case expires == 0:
		// An initial publication that would remove itself at once stores nothing.
		return 200, sip.Header{{Name: "Expires", Value: "0"}, {Name: "SIP-ETag", Value: newETag()}}, nil
	}

	if pubs == nil {
		pubs = map[string]*stored{}
		ps.resources[k] = pubs
	}

	// Step 5: the state, its refresh or its removal; step 6: a new entity-tag for each.
	next := newETag()

	if p != nil {
		delete(pubs, p.ETag)
		p.timer.Stop()
	} else {
		p = &stored{}
	}

	modified := len(parts) > 0 || expires == 0

	if len(parts) > 0 {
		p.Bodies = parts
	}

	header := sip.Header{{Name: "Expires", Value: strconvSeconds(expires)}, {Name: "SIP-ETag", Value: next}}

	if expires == 0 {
		if len(pubs) == 0 {
			delete(ps.resources, k)
		}

		return 200, header, ps.report(req.URI, k)
	}

	p.ETag, p.Expires = next, ps.a.clock.Now().Add(expires)
	pubs[next] = p

	st := ps.a.keyedStrand(k.uri)
	p.timer = ps.a.clock.AfterFunc(expires, func() { st.Post(func() { ps.expire(req.URI, k, p) }) })

	if !modified {
		return 200, header, nil
	}

	return 200, header, ps.report(req.URI, k)
}

// report is the report of the state of a resource, as it is now.
func (ps *PublicationStore) report(uri sip.URI, k storeKey) func() {
	if ps.cfg.Changed == nil {
		return nil
	}

	state := ps.stateLocked(k)

	return func() { ps.cfg.Changed(uri, k.event, state) }
}

// expire removes a publication that was not refreshed in time (RFC 3903 §6).
func (ps *PublicationStore) expire(uri sip.URI, k storeKey, p *stored) {
	ps.mu.Lock()

	pubs := ps.resources[k]
	if pubs[p.ETag] != p {
		ps.mu.Unlock()
		return
	}

	delete(pubs, p.ETag)

	if len(pubs) == 0 {
		delete(ps.resources, k)
	}

	changed := ps.report(uri, k)
	ps.mu.Unlock()

	if changed != nil {
		changed()
	}
}

// newETag is an entity-tag unique among those of the store, and not used before (RFC 3903 §6 step 6).
func newETag() string {
	var b [12]byte

	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}

// keyedStrand is the strand of a key, as the Request-URI of PUBLISHes, whose requests must go in order (RFC 3903
// §6): one of a fixed set, by hash.
func (a *Agent) keyedStrand(key string) *Strand {
	i := maphash.String(seed, key) % uint64(len(a.keyed))

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.keyed[i] == nil {
		a.keyed[i] = a.NewStrand()
	}

	return a.keyed[i]
}

var seed = maphash.MakeSeed()

// respondStateless responds to a request outside a dialog that creates none, as a PUBLISH: without a Record-Route
// (RFC 3903 §6).
func (r *Request) respondStateless(rp Reply) error {
	return r.st.do(func() error {
		if r.final {
			return transaction.ErrFinalResponseSent
		}

		res, err := r.build(rp)
		if err != nil {
			return err
		}

		res.Header.Del("Record-Route")
		r.complete(res)
		r.final = true

		return r.tx.Respond(res)
	})
}
