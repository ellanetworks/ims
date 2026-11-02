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

// RFC 3903 section 6
type Published struct {
	ETag    string
	Bodies  []sip.BodyPart
	Expires time.Time
}

type StoreConfig struct {
	Events []string

	Resource func(uri sip.URI) bool

	Check func(r *Request) int

	MinExpires     time.Duration
	DefaultExpires time.Duration
	MaxExpires     time.Duration

	Changed func(uri sip.URI, event string, state []Published)
}

// RFC 3903 section 6
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

func (a *Agent) NewPublicationStore(cfg StoreConfig) *PublicationStore {
	return &PublicationStore{a: a, cfg: cfg, resources: map[storeKey]map[string]*stored{}}
}

func resourceOf(u sip.URI) string {
	k := u.Scheme + ":" + u.User + "@" + strings.ToLower(u.Host)
	if u.Port != 0 {
		k += ":" + strconv.Itoa(int(u.Port))
	}

	return k
}

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

// RFC 3903 section 6
func (ps *PublicationStore) Handle(r *Request) {
	code, header, changed := ps.process(r)

	if err := r.respondStateless(Reply{Code: code, Header: header}); err != nil {
		r.logger().Debug("PUBLISH response failed", slog.Any("error", err))
	}

	if changed != nil {
		changed()
	}
}

func (ps *PublicationStore) process(r *Request) (int, sip.Header, func()) {
	req := r.req
	cfg := ps.cfg

	if cfg.Resource != nil && !cfg.Resource(req.URI) {
		return 404, nil, nil
	}

	ev, ok, err := req.Header.Event()
	if !ok || err != nil || !slices.Contains(cfg.Events, ev.Type()) {
		return 489, sip.Header{{Name: "Allow-Events", Value: strings.Join(cfg.Events, ", ")}}, nil
	}

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

	if len(parts) > 0 && cfg.Check != nil {
		if code := cfg.Check(r); code != 0 {
			return code, nil, nil
		}
	}

	k := storeKey{uri: resourceOf(req.URI), event: ev.Type()}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	pubs := ps.resources[k]

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
		return 400, nil, nil
	case expires == 0:
		return 200, sip.Header{{Name: "Expires", Value: "0"}, {Name: "SIP-ETag", Value: newETag()}}, nil
	}

	if pubs == nil {
		pubs = map[string]*stored{}
		ps.resources[k] = pubs
	}

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

func (ps *PublicationStore) report(uri sip.URI, k storeKey) func() {
	if ps.cfg.Changed == nil {
		return nil
	}

	state := ps.stateLocked(k)

	return func() { ps.cfg.Changed(uri, k.event, state) }
}

// RFC 3903 section 6
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

// RFC 3903 section 6
func newETag() string {
	var b [12]byte

	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}

// RFC 3903 section 6
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

// RFC 3903 section 6
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
