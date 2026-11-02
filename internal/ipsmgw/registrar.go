package ipsmgw

import (
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/lifetime"
	"github.com/ellanetworks/ims/sip/reginfo"
	"github.com/ellanetworks/ims/sip/ua"
	"go.opentelemetry.io/otel/trace"
)

// TS 24.341 clause 5.3.1.2
// TS 24.341 clause 7.3
const smsipTag = "+g.3gpp.smsip"

const strands = 16

// RFC 3261 section 10.3
// TS 24.229 clause 5.4.1.2.1
// RFC 3261 section 10.2.1.1
const defaultExpires = 3600

const (
	minSubscribeRetry = 30 * time.Second
	maxSubscribeRetry = 10 * time.Minute
)

// TS 24.341 clause 5.3.3.2
type registration struct {
	key          string
	impu         sip.URI
	set          []sip.URI
	msisdn, imsi string
	scscf, as    sip.URI
	expires      time.Time
	available    bool
	unri         bool
	expiry       *ua.Timer
	watch        *watch
	strand       *ua.Strand
}

func (r *registration) user() User {
	return User{MSISDN: r.msisdn, IMSI: r.imsi, IMPU: r.impu}
}

// RFC 3680
// TS 24.229 clause 5.7.1.1
type watch struct {
	sub       *ua.Subscription
	active    bool
	version   uint32
	versioned bool
	contacts  map[string]map[string]bool
	stopped   bool
	attempts  int
}

type registrar struct {
	agent    *ua.Agent
	store    Store
	home     string
	log      *slog.Logger
	life     context.Context
	detached tracing.Detached

	seed    maphash.Seed
	strands [strands]*ua.Strand

	saving sync.Mutex

	mu     sync.Mutex
	regs   map[string]*registration
	dirty  map[string]bool
	hss    *hssSync
	closed bool
}

func (g *registrar) strandOf(k string) *ua.Strand {
	return g.strands[maphash.String(g.seed, k)%strands]
}

// TS 24.341 clause 5.3.3.2
// TS 24.229 clause 5.7.1.1
// TS 24.229 clause 5.4.1.7
func (g *registrar) handleRegister(ctx context.Context, r *ua.Request) {
	req := r.Message()
	now := g.agent.Layer().Clock().Now()

	var header sip.Header
	if v := chargingVector(req, g.home); v != "" {
		header = append(header, sip.Field{Name: "P-Charging-Vector", Value: v})
	}

	to, err1 := req.Header.To()
	contacts, err2 := req.Header.Contacts()

	if err1 != nil || err2 != nil || len(contacts) != 1 || contacts[0].Star || key(to.URI) == "" {
		_ = r.Reject(400, header...)
		return
	}

	scscf := contacts[0]

	secs := uint32(defaultExpires)

	if v, ok := scscf.Params.Get("expires"); ok {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			_ = r.Reject(400, header...)
			return
		}

		secs = uint32(n)
	} else if e, err := req.Header.Expires(); err == nil {
		secs = e
	}

	_ = r.Respond(ua.Reply{Code: 200, Header: append(header, sip.Field{Name: "Expires", Value: strconv.FormatUint(uint64(secs), 10)})})

	k := key(to.URI)

	defer func() { _ = g.flush(ctx) }()

	g.mu.Lock()
	defer g.mu.Unlock()

	reg := g.regs[k]

	if secs == 0 {
		if reg != nil && (reg.watch == nil || !reg.watch.active) {
			g.remove(ctx, reg)
		}

		return
	}

	msisdn, imsi := user(req)

	if reg == nil {
		reg = &registration{key: k, strand: g.strandOf(k)}
		g.regs[k] = reg
	}

	before := reg.user().userKey()

	reg.impu, reg.as = to.URI, req.URI
	reg.expires = now.Add(time.Duration(secs) * time.Second)

	if set := implicitSet(req, to.URI); len(set) > 1 || len(reg.set) == 0 {
		reg.set = set
	}

	if msisdn != "" || imsi != "" {
		reg.msisdn, reg.imsi = msisdn, imsi
	}

	moved := !reg.scscf.Equivalent(scscf.URI)
	reg.scscf = scscf.URI

	g.reachable(ctx, reg)

	g.expireAt(ctx, reg)
	g.save(reg)

	if after := reg.user().userKey(); after != before {
		g.hss.rekey(trace.SpanContextFromContext(ctx), before, after)
	}

	if reg.watch == nil || moved {
		g.resubscribe(ctx, reg)
	}
}

const (
	tracerName = "github.com/ellanetworks/ims/internal/ipsmgw"
	role       = "ipsmgw"
)

const saveTimeout = 5 * time.Second

func (g *registrar) expireAt(ctx context.Context, reg *registration) {
	if reg.expiry != nil {
		reg.expiry.Stop()
	}

	d := max(reg.expires.Sub(g.agent.Layer().Clock().Now()), 0)
	cause := trace.SpanContextFromContext(ctx)

	reg.expiry = reg.strand.AfterFunc(d, func() {
		ctx, span := g.detached.Start(g.life, "registration expiry", cause)
		g.expire(ctx, reg)
		tracing.End(span, g.flush(ctx))
	})
}

func (g *registrar) expire(ctx context.Context, reg *registration) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.regs[reg.key] == reg && !g.closed && !reg.expires.After(g.agent.Layer().Clock().Now()) {
		g.log.DebugContext(ctx, "IP-SM-GW registration expired", slog.String("impu", reg.impu.String()))
		g.remove(ctx, reg)
	}
}

func (g *registrar) remove(ctx context.Context, reg *registration) {
	if g.regs[reg.key] != reg {
		return
	}

	delete(g.regs, reg.key)

	if reg.expiry != nil {
		reg.expiry.Stop()
	}

	if w := reg.watch; w != nil {
		w.stopped = true
		if w.sub != nil {
			sub := w.sub

			reg.strand.Post(func() { _ = sub.Unsubscribe(ctx, ua.Change{}) })
		}
	}

	g.dirty[reg.key] = true
	g.hss.mark(trace.SpanContextFromContext(ctx), reg.user().userKey())
}

func (g *registrar) save(reg *registration) { g.dirty[reg.key] = true }

func (reg *registration) row() db.IPSMGWRegistration {
	r := db.IPSMGWRegistration{
		IMPUKey: reg.key, IMPU: reg.impu.String(), MSISDN: reg.msisdn, IMSI: reg.imsi, SCSCF: reg.scscf.String(),
		AS: reg.as.String(), ExpiresAt: reg.expires, Available: reg.available,
	}

	for _, u := range reg.set {
		r.IMPUs = append(r.IMPUs, u.String())
	}

	return r
}

func (g *registrar) flush(ctx context.Context) error {
	return errors.Join(g.persist(ctx), g.hss.persist(ctx))
}

func (g *registrar) persist(ctx context.Context) error {
	g.saving.Lock()
	defer g.saving.Unlock()

	g.mu.Lock()

	rows := make(map[string]*db.IPSMGWRegistration, len(g.dirty))

	for k := range g.dirty {
		if reg := g.regs[k]; reg != nil {
			r := reg.row()
			rows[k] = &r
		} else {
			rows[k] = nil
		}
	}

	clear(g.dirty)
	g.mu.Unlock()

	var errs []error

	for k, r := range rows {
		wctx, cancel := context.WithTimeout(lifetime.With(g.life, ctx), saveTimeout)

		var err error
		if r != nil {
			err = g.store.PutIPSMGWRegistration(wctx, *r)
		} else {
			err = g.store.DeleteIPSMGWRegistration(wctx, k)
		}

		cancel()

		if err != nil {
			g.log.ErrorContext(ctx, "failed to store an IP-SM-GW registration", slog.String("impu", k),
				slog.Bool("deleted", r == nil), slog.Any("error", err))

			g.mu.Lock()
			g.dirty[k] = true
			g.mu.Unlock()

			errs = append(errs, fmt.Errorf("store the IP-SM-GW registration: %w", err))
		}
	}

	return errors.Join(errs...)
}

func (g *registrar) resubscribe(ctx context.Context, reg *registration) {
	if old := reg.watch; old != nil {
		old.stopped = true
		if old.sub != nil {
			sub := old.sub

			reg.strand.Post(func() { _ = sub.Unsubscribe(ctx, ua.Change{}) })
		}
	}

	w := &watch{}
	reg.watch = w

	reg.strand.Post(func() { _ = g.subscribe(ctx, reg, w) })
}

func (g *registrar) subscribeDetached(cause trace.SpanContext, reg *registration, w *watch) {
	ctx, span := g.detached.Start(g.life, "reg event resubscribe", cause)
	tracing.End(span, g.subscribe(ctx, reg, w))
}

var errNoSIPURI = errors.New("ipsmgw: no SIP URI to subscribe to the reg event package of")

// TS 24.229 clause 5.7.1.1
// RFC 3680 section 4.4
func (g *registrar) subscribe(ctx context.Context, reg *registration, w *watch) error {
	g.mu.Lock()

	if g.closed || g.regs[reg.key] != reg || reg.watch != w || w.stopped {
		g.mu.Unlock()
		return nil
	}

	target := subscribed(reg.impu, reg.set, g.home)
	from, scscf := reg.as, reg.scscf

	g.mu.Unlock()

	if !target.IsSIP() {
		g.log.WarnContext(ctx, "no SIP URI to subscribe to the reg event package of", slog.String("impu", reg.impu.String()))
		return errNoSIPURI
	}

	req := sip.NewRequest("SUBSCRIBE", target)
	req.Header.Set("From", sip.Address{URI: from}.String())
	req.Header.Set("To", sip.Address{URI: target}.String())
	req.Header.Set("Event", "reg")
	req.Header.Set("Accept", reginfo.ContentType)
	req.Header.Set("P-Asserted-Identity", sip.Address{URI: from}.String())
	req.Header.Set("P-Charging-Vector", newChargingVector(g.home))

	route := scscf.Clone()
	route.Params.Set("lr", "")
	req.Header.Set("Route", sip.Address{URI: route}.String())

	sub, err := g.agent.Subscribe(ctx, req, ua.SubscriptionHandler{
		Notify: func(ctx context.Context, s *ua.Subscription, n *ua.Request) { g.notify(ctx, reg, w, s, n) },
		Ended:  func(ctx context.Context, _ *ua.Subscription, e ua.End) { g.ended(ctx, reg, w, e) },
	}, ua.On(reg.strand))

	g.mu.Lock()
	defer g.mu.Unlock()

	if err != nil {
		if g.closed || g.regs[reg.key] != reg || reg.watch != w || w.stopped {
			return err
		}

		after := g.retry(ctx, reg, w)
		g.log.WarnContext(ctx, "reg event SUBSCRIBE not sent", slog.String("impu", reg.impu.String()),
			slog.Any("error", err), slog.Duration("retry_after", after))

		return err
	}

	w.sub = sub

	if w.stopped {
		reg.strand.Post(func() { _ = sub.Unsubscribe(ctx, ua.Change{}) })
	}

	return nil
}

func (g *registrar) retry(ctx context.Context, reg *registration, w *watch) time.Duration {
	return g.retryAfter(ctx, reg, w, 0)
}

func (g *registrar) retryAfter(ctx context.Context, reg *registration, w *watch, after time.Duration) time.Duration {
	if after <= 0 {
		after = min(minSubscribeRetry<<min(w.attempts, 5), maxSubscribeRetry)
	}

	next := &watch{attempts: w.attempts + 1}
	w.stopped = true
	reg.watch = next

	cause := trace.SpanContextFromContext(ctx)

	reg.strand.AfterFunc(after, func() { g.subscribeDetached(cause, reg, next) })

	return after
}

// TS 24.229 clause 5.4.2.1.1
func (g *registrar) ended(ctx context.Context, reg *registration, w *watch, e ua.End) {
	code := 0
	if e.Response != nil {
		code = e.Response.StatusCode
	}

	defer func() { _ = g.flush(ctx) }()

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.closed || g.regs[reg.key] != reg || reg.watch != w || w.stopped {
		return
	}

	switch {
	case e.Cause == ua.EndRejected && (code == 480 || code == 404):
		g.log.DebugContext(ctx, "reg event subscription refused: no registration", slog.String("impu", reg.impu.String()),
			slog.Int("code", code))
		g.remove(ctx, reg)
	case e.Cause == ua.EndUnsubscribed, e.Cause == ua.EndClosed:
	default:
		after := g.retry(ctx, reg, w)
		g.log.WarnContext(ctx, "reg event subscription ended: subscribing again", slog.String("impu", reg.impu.String()),
			slog.String("cause", e.Cause.String()), slog.Int("code", code), slog.Duration("retry_after", after))
	}
}

// RFC 3680 section 5.2
// TS 24.341 clause 5.3.3.2
// RFC 6665 section 4.1.3
// TS 24.229 clause 5.7.1.1
func (g *registrar) notify(ctx context.Context, reg *registration, w *watch, s *ua.Subscription, n *ua.Request) {
	req := n.Message()

	var header sip.Header
	if v := chargingVector(req, g.home); v != "" {
		header = append(header, sip.Field{Name: "P-Charging-Vector", Value: v})
	}

	var (
		doc        *reginfo.Reginfo
		unreadable bool
	)

	switch part, ok, err := req.Content().Find(reginfo.ContentType); {
	case err != nil:
		unreadable = true
	case ok:
		d, err := reginfo.Decode(part.Body)
		if err != nil {
			g.log.WarnContext(ctx, "unreadable reg event document", slog.String("impu", reg.impu.String()),
				slog.Any("error", err))

			unreadable = true
		} else {
			doc = &d
		}
	}

	g.mu.Lock()

	current := !g.closed && g.regs[reg.key] == reg && reg.watch == w && !w.stopped

	if current && s.State() == ua.SubscriptionActive {
		w.active = true
	}

	ended := false

	if current && doc != nil && (!w.versioned || doc.Version > w.version) {
		w.version, w.versioned = doc.Version, true
		ended = g.apply(ctx, reg, w, doc)
	}

	if current && !ended && s.State() == ua.SubscriptionTerminated {
		w.stopped, w.sub = true, nil
		ss, _ := sip.ParseSubscriptionState(req.Header.Get("Subscription-State"))

		switch ss.Reason {
		case "noresource", "rejected":
			ended = true
		case "deactivated":
			next := &watch{}
			reg.watch = next
			cause := trace.SpanContextFromContext(ctx)

			reg.strand.Post(func() { g.subscribeDetached(cause, reg, next) })
		default:
			var after time.Duration

			if ss.Reason == "probation" {
				if v, ok := ss.Params.Get("retry-after"); ok {
					if n, err := strconv.ParseUint(v, 10, 32); err == nil {
						after = time.Duration(n) * time.Second
					}
				}
			}

			g.retryAfter(ctx, reg, w, after)
		}
	}

	if current && ended {
		g.remove(ctx, reg)
	}

	g.mu.Unlock()

	_ = g.flush(ctx)

	if unreadable {
		_ = n.Respond(ua.Reply{Code: 400, Header: header})

		if current && s.State() != ua.SubscriptionTerminated {
			_ = s.Refresh(ctx, ua.Change{}, nil)
		}

		return
	}

	_ = n.Respond(ua.Reply{Code: 200, Header: header})
}

// TS 24.229 clause 5.4.1.8
func (g *registrar) apply(ctx context.Context, reg *registration, w *watch, doc *reginfo.Reginfo) bool {
	if w.contacts == nil || doc.State == reginfo.Full {
		w.contacts = map[string]map[string]bool{}
	}

	ended := false

	for _, r := range doc.Registrations {
		aor, ok := reg.reported(r.AOR)
		if !ok {
			continue
		}

		if r.State != reginfo.Active {
			delete(w.contacts, aor)

			ended = ended || r.State == reginfo.Terminated && aor == reg.key

			continue
		}

		cs := w.contacts[aor]
		if cs == nil {
			cs = map[string]bool{}
			w.contacts[aor] = cs
		}

		for _, c := range r.Contacts {
			if c.State == reginfo.Active {
				cs[c.ID] = smsip(c)
			} else {
				delete(cs, c.ID)
			}
		}
	}

	available := false

	for _, cs := range w.contacts {
		for _, able := range cs {
			available = available || able
		}
	}

	if available != reg.available {
		reg.available = available
		g.save(reg)
		g.hss.mark(trace.SpanContextFromContext(ctx), reg.user().userKey())

		g.log.DebugContext(ctx, "SMS over IP availability changed", slog.String("impu", reg.impu.String()),
			slog.Bool("available", available))
	} else if available {
		g.reachable(ctx, reg)
	}

	return ended
}

// TS 24.341 clause 5.3.1.2
// RFC 3840 section 9
func smsip(c reginfo.Contact) bool {
	v, ok := c.Param(smsipTag)
	if !ok {
		return false
	}

	switch strings.ToUpper(strings.Trim(v, `"`)) {
	case "", "TRUE":
		return true
	default:
		return false
	}
}

func (r *registration) reported(aor string) (string, bool) {
	u, err := sip.ParseURI(aor)
	if err != nil {
		return "", false
	}

	k := key(u)
	if k == "" {
		return "", false
	}

	for _, v := range r.set {
		if key(v) == k {
			return k, true
		}
	}

	if k == r.key || r.impu.IsTel() && u.IsSIP() && u.User == r.impu.Identity().User {
		return r.key, true
	}

	return "", false
}

func (g *registrar) want(userKey string) (User, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var (
		u     User
		found bool
	)

	for _, reg := range g.regs {
		if reg.user().userKey() != userKey {
			continue
		}

		if !found || reg.available {
			u, found = reg.user(), true
		}

		if reg.available {
			return u, true
		}
	}

	return u, false
}

func (g *registrar) restore(ctx context.Context, rows []db.IPSMGWRegistration) {
	now := g.agent.Layer().Clock().Now()
	cause := trace.SpanContextFromContext(ctx)

	defer func() { _ = g.flush(ctx) }()

	g.mu.Lock()
	defer g.mu.Unlock()

	for _, row := range rows {
		impu, err1 := sip.ParseURI(row.IMPU)
		scscf, err2 := sip.ParseURI(row.SCSCF)
		as, err3 := sip.ParseURI(row.AS)

		if err1 != nil || err2 != nil || err3 != nil || !row.ExpiresAt.After(now) {
			g.dirty[row.IMPUKey] = true

			u := User{MSISDN: row.MSISDN, IMSI: row.IMSI, IMPU: impu}
			g.hss.mark(cause, u.userKey())

			continue
		}

		reg := &registration{
			key: row.IMPUKey, impu: impu, set: []sip.URI{impu}, msisdn: row.MSISDN, imsi: row.IMSI, scscf: scscf, as: as,
			expires: row.ExpiresAt, available: row.Available, strand: g.strandOf(row.IMPUKey),
		}

		for _, v := range row.IMPUs {
			if u, err := sip.ParseURI(v); err == nil && key(u) != reg.key {
				reg.set = append(reg.set, u)
			}
		}

		g.regs[reg.key] = reg
		g.expireAt(ctx, reg)
		g.hss.mark(cause, reg.user().userKey())
	}
}

func (g *registrar) resubscribeAll(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, reg := range g.regs {
		if reg.watch == nil {
			g.resubscribe(ctx, reg)
		}
	}
}

func (g *registrar) lookup(ids []sip.URI) (registration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, u := range ids {
		if reg := g.regs[key(u)]; reg != nil {
			return *reg, true
		}
	}

	for _, reg := range g.regs {
		for _, u := range ids {
			if _, ok := reg.reported(u.String()); ok {
				return *reg, true
			}
		}
	}

	return registration{}, false
}

// TS 24.341 clause 5.3.3.3
// TS 24.341 clause 5.3.3.2
func (g *registrar) byUser(msisdn, imsi string) (registration, bool) {
	if reg, ok := g.availableFor(msisdn, imsi); ok {
		return reg, true
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	for _, reg := range g.regs {
		if reg.of(msisdn, imsi) {
			return *reg, true
		}
	}

	return registration{}, false
}

func (g *registrar) availableFor(msisdn, imsi string) (registration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, reg := range g.regs {
		if reg.of(msisdn, imsi) && reg.available {
			return *reg, true
		}
	}

	return registration{}, false
}

// TS 23.204 clause 6.5a
func (g *registrar) unreachable(msisdn, imsi string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, reg := range g.regs {
		if reg.of(msisdn, imsi) {
			reg.unri = true
		}
	}
}

func (reg *registration) of(msisdn, imsi string) bool {
	return (msisdn != "" && reg.msisdn == msisdn) || (imsi != "" && reg.imsi == imsi)
}

// TS 23.204 clause 6.5b
// TS 23.040 clause 3.2.6
func (g *registrar) reachable(ctx context.Context, reg *registration) {
	if !reg.unri {
		return
	}

	reg.unri = false

	g.hss.force(trace.SpanContextFromContext(ctx), reg.user().userKey())
	g.log.DebugContext(ctx, "UE reachable for IP again", slog.String("impu", reg.impu.String()))
}

func (g *registrar) userKeys() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	out := make([]string, 0, len(g.regs))
	for _, reg := range g.regs {
		out = append(out, reg.user().userKey())
	}

	return out
}

func (g *registrar) close() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.closed = true

	for _, reg := range g.regs {
		if reg.expiry != nil {
			reg.expiry.Stop()
		}
	}
}

// TS 24.229 clause 5.7.1.1
func chargingVector(req *sip.Request, home string) string {
	ps, err := sip.ParseParams(req.Header.Get("P-Charging-Vector"))
	if err != nil {
		return ""
	}

	icid, _ := ps.Get("icid-value")
	if icid == "" {
		return ""
	}

	v := "icid-value=" + icid
	if orig, _ := ps.Get("orig-ioi"); orig != "" {
		v += ";orig-ioi=" + orig
	}

	return v + ";term-ioi=" + home
}

// TS 24.229 clause 5.7.3
func newChargingVector(home string) string {
	return "icid-value=" + strings.ReplaceAll(sip.NewBranch(), "z9hG4bK", "") + ";orig-ioi=" + home
}
