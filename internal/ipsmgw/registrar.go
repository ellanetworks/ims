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

// smsipTag is the feature tag of a UE able to receive short messages over IP (TS 24.341 §5.3.1.2, §7.3).
const smsipTag = "+g.3gpp.smsip"

// strands is the number of strands the registrations share: those of an IMPU go on one, in order.
const strands = 16

// defaultExpires is the expiration of a third-party REGISTER of neither an expires parameter nor an Expires header
// field (RFC 3261 §10.3 step 7): TS 24.229 §5.4.1.2.1 has the S-CSCF grant at most 600000 s; 3600 s is the default of
// RFC 3261 §10.2.1.1.
const defaultExpires = 3600

// The bounds of the waits between the attempts of a reg event subscription that failed.
const (
	minSubscribeRetry = 30 * time.Second
	maxSubscribeRetry = 10 * time.Minute
)

// registration is the IP-SM-GW's registration of an IMPU (TS 24.341 §5.3.3.2): of its third-party REGISTERs, its
// user's identities, and the reg event subscription that tells whether a contact of the IMPU can receive short
// messages over IP. Its fields are under the registrar's lock.
type registration struct {
	key          string
	impu         sip.URI
	set          []sip.URI
	msisdn, imsi string
	scscf, as    sip.URI
	expires      time.Time
	available    bool
	// unri is set when the UE did not answer a short message over IMS: not reachable for IP (TS 23.040 §3.2.8;
	// TS 23.204 §6.5a step 17), until it registers again or its reg event tells it reachable (§6.5b).
	unri   bool
	expiry *ua.Timer
	watch  *watch
	strand *ua.Strand
}

// user is the user of the registration, as the HSS knows it.
func (r *registration) user() User {
	return User{MSISDN: r.msisdn, IMSI: r.imsi, IMPU: r.impu}
}

// watch is a reg event subscription of a registration (RFC 3680; TS 24.229 §5.7.1.1).
type watch struct {
	sub    *ua.Subscription
	active bool
	// version is that of the last document, once one came (RFC 3680 §5.2).
	version   uint32
	versioned bool
	// contacts are the active contacts of the IMPUs of the implicit registration set, by the key of the aor of their
	// <registration> (RFC 3680 §5.2), then by their id, and whether each can receive short messages over IP.
	contacts map[string]map[string]bool
	stopped  bool
	attempts int
}

// registrar keeps the registrations of the IP-SM-GW, and the HSS in line with them.
type registrar struct {
	agent *ua.Agent
	store Store
	home  string
	log   *slog.Logger
	// life is the lifetime of the IP-SM-GW, which its timers and writes are bound to.
	life     context.Context
	detached tracing.Detached

	seed    maphash.Seed
	strands [strands]*ua.Strand

	// saving orders the writes of the registrations: each writes the state of regs at its start, after the previous
	// one.
	saving sync.Mutex

	mu   sync.Mutex
	regs map[string]*registration
	// dirty are the keys of the registrations the store has yet to be given: of regs, else deleted.
	dirty  map[string]bool
	hss    *hssSync
	closed bool
}

func (g *registrar) strandOf(k string) *ua.Strand {
	return g.strands[maphash.String(g.seed, k)%strands]
}

// handleRegister answers a third-party REGISTER of the S-CSCF (TS 24.341 §5.3.3.2; TS 24.229 §5.7.1.1): a 200 of
// the expiration the S-CSCF granted; the identities of the user stored; a subscription to the reg event package of
// the IMPU, which tells whether it can receive short messages over IP. A deregistration of an IMPU of an active
// subscription waits for its NOTIFY, as contacts of the IMPU may stay registered (TS 24.229 §5.4.1.7 NOTE 2);
// without one, it ends the registration.
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

	// The expires parameter of the Contact, else the Expires header field, else a default (RFC 3261 §10.3 step 7).
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

	// Once the lock is released: the handler's context ends with the transaction, the writes are bound to life.
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

	// A REGISTER without the implicit registration set keeps the one an earlier one told.
	if set := implicitSet(req, to.URI); len(set) > 1 || len(reg.set) == 0 {
		reg.set = set
	}

	// A REGISTER without them keeps those an earlier one told.
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

// tracerName is the instrumentation scope of the spans of the IP-SM-GW's own, role their ella.ims.role.
const (
	tracerName = "github.com/ellanetworks/ims/internal/ipsmgw"
	role       = "ipsmgw"
)

// saveTimeout bounds a write to the store.
const saveTimeout = 5 * time.Second

// expireAt has reg removed once it expires, unless a REGISTER refreshes it: in a trace of its own, linked to that of
// ctx, which armed the timer. Called with g.mu held.
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

// expire removes reg, if expired still.
func (g *registrar) expire(ctx context.Context, reg *registration) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.regs[reg.key] == reg && !g.closed && !reg.expires.After(g.agent.Layer().Clock().Now()) {
		g.log.DebugContext(ctx, "IP-SM-GW registration expired", slog.String("impu", reg.impu.String()))
		g.remove(ctx, reg)
	}
}

// remove ends reg: its subscription, its row (at the next flush), and its user's availability. Called with g.mu held.
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

// save has reg written at the next flush. Called with g.mu held.
func (g *registrar) save(reg *registration) { g.dirty[reg.key] = true }

// row is the row of reg.
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

// flush writes the registrations dirty, and the HSS registrations unsaved, outside g.mu, which no write holds: each
// write is bounded by saveTimeout within life, as it may run after the transaction of ctx ended. Called without g.mu
// held, after the changes it writes. The writes are in the order of flush calls, of g.saving, each of the state of
// regs at its start, taken under g.mu: of a registration changed twice, the store ends with the later state, whichever
// flush writes it. Those that fail are written at the next flush.
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

// resubscribe replaces the subscription of reg with a new one, toward its S-CSCF. Called with g.mu held.
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

// subscribeDetached subscribes w of reg in a trace of its own, linked to cause: work that starts later than its cause,
// as a subscription ended (D5). Runs on the strand of reg.
func (g *registrar) subscribeDetached(cause trace.SpanContext, reg *registration, w *watch) {
	ctx, span := g.detached.Start(g.life, "reg event resubscribe", cause)
	tracing.End(span, g.subscribe(ctx, reg, w))
}

var errNoSIPURI = errors.New("ipsmgw: no SIP URI to subscribe to the reg event package of")

// subscribe sends the SUBSCRIBE of w to the reg event package of the IMPU of reg (TS 24.229 §5.7.1.1 a to f), of
// the default duration (RFC 3680 §4.4), which the agent refreshes, and returns why it was not sent, if so. Runs on
// the strand of reg.
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

// retry subscribes again after a wait, doubling from minSubscribeRetry to maxSubscribeRetry, while reg lasts, and
// returns the wait. Called with g.mu held.
func (g *registrar) retry(ctx context.Context, reg *registration, w *watch) time.Duration {
	return g.retryAfter(ctx, reg, w, 0)
}

// retryAfter subscribes again after the wait after, if any, else as retry does, in a trace of its own linked to that
// of ctx, and returns the wait. Called with g.mu held.
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

// ended handles the end of the subscription of w. A 480 or 404, of an IMPU the S-CSCF has no registration of
// (TS 24.229 §5.4.2.1.1), ends the registration. Another end of a subscription not ended by the IP-SM-GW subscribes
// again later, the availability unchanged meanwhile.
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

// notify handles a NOTIFY of w (RFC 3680 §5.2; TS 24.341 §5.3.3.2): of a document newer than the last, the contacts
// of the IMPUs of the implicit registration set it reports, and whether one of them can receive short messages over
// IP; a change of that updates the HSS. A document that reports the registration of the IMPU itself terminated ends
// the registration. A subscription the S-CSCF ends is made again (RFC 6665 §4.1.3): at once of "deactivated", after
// the retry-after of "probation", after a backoff of no reason or another than "noresource" and "rejected", which end
// the registration. A document that does not decode gets a 400, which keeps the subscription, and a refresh asks for
// the full state again. Every response has the P-Charging-Vector of TS 24.229 §5.7.1.1.
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
			// At once, in a trace of its own linked to the NOTIFY's (D5).
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

// apply applies doc to the contacts of w, and the availability of reg; it reports whether the document tells that
// the registration of the IMPU of reg itself ended. The contacts of a registration the document reports not active
// go, those of the others stay (TS 24.229 §5.4.1.8: an IMPU of a set may be deregistered alone). Called with g.mu
// held.
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

// smsip reports whether a contact can receive short messages over IP: of the feature tag of TS 24.341 §5.3.1.2, of
// no value or of TRUE (RFC 3840 §9: "boolean"), not negated.
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

// reported is the key of aor, of a <registration> of a reg event document, if it is of the IMPU of reg: one of its
// implicit registration set, or the URI subscribed to, whose key is that of the IMPU.
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

// want is the HSS registration to have for the user of userKey: available while one of its IMPUs is, and its
// identity.
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

// restore puts back the registrations kept, but those expired, before the agent serves; Resubscribe subscribes to
// their reg event packages once the core runs.
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

// resubscribeAll subscribes to the reg event package of the registrations of no subscription, as restored, once the
// core runs.
func (g *registrar) resubscribeAll(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, reg := range g.regs {
		if reg.watch == nil {
			g.resubscribe(ctx, reg)
		}
	}
}

// lookup is a copy of the registration of one of ids, if any: the first of ids registered.
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

// byUser is a copy of a registration of the MSISDN msisdn or of the IMSI imsi, if any: the IP-SM-GW has a public
// user identity of the MSISDN (TS 24.341 §5.3.3.3), else of the IMSI it stored of a user of no MSISDN in the
// <service-info> (§5.3.3.2). One that can receive short messages over IP comes first.
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

// availableFor is a copy of a registration of the MSISDN msisdn or of the IMSI imsi that can receive short messages
// over IP, if any.
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

// unreachable sets the UNRI of the registrations of the MSISDN msisdn or of the IMSI imsi (TS 23.204 §6.5a step 17).
func (g *registrar) unreachable(msisdn, imsi string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, reg := range g.regs {
		if reg.of(msisdn, imsi) {
			reg.unri = true
		}
	}
}

// of reports whether reg is of the MSISDN msisdn or of the IMSI imsi.
func (reg *registration) of(msisdn, imsi string) bool {
	return (msisdn != "" && reg.msisdn == msisdn) || (imsi != "" && reg.imsi == imsi)
}

// reachable clears the UNRI of reg, if set, and has the HSS told again that the IP-SM-GW serves the user, for it to
// alert the service centres of the messages waiting (TS 23.204 §6.5b; TS 23.040 §3.2.6 2c). Called with g.mu held.
func (g *registrar) reachable(ctx context.Context, reg *registration) {
	if !reg.unri {
		return
	}

	reg.unri = false

	g.hss.force(trace.SpanContextFromContext(ctx), reg.user().userKey())
	g.log.DebugContext(ctx, "UE reachable for IP again", slog.String("impu", reg.impu.String()))
}

// userKeys are the keys of the users of the registrations.
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

// chargingVector is the P-Charging-Vector of a response to req (TS 24.229 §5.7.1.1): its icid-value and orig-ioi,
// and the type 3 term-ioi of the home domain; "" without an icid-value.
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

// newChargingVector is the P-Charging-Vector of a request of the IP-SM-GW (TS 24.229 §5.7.3): a new icid-value, and
// the type 3 orig-ioi of the home domain.
func newChargingVector(home string) string {
	return "icid-value=" + strings.ReplaceAll(sip.NewBranch(), "z9hG4bK", "") + ";orig-ioi=" + home
}
