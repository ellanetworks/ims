package registrar

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/reginfo"
	"github.com/ellanetworks/ims/sip/ua"
)

// watch is the subscription of the server to the reg event package of an IMPU it has bindings of through the IMS
// (TS 24.229 §5.7.1.1; RFC 3680), toward the S-CSCF of its latest third-party REGISTER, as the AS of its
// Request-URI: from the first binding of the IMPU to its last. ik is the key of the IMPU, the To of the third-party
// REGISTERs, and impu the SIP URI subscribed to (subscribed). Its fields but those are under g.mu.
type watch struct {
	impu, scscf, as sip.URI
	ik              string
	// aliases are the keys of the public user identities the documents report registered, of a <registration>
	// active (RFC 3680 §5.1): those the user owns (TS 24.229 §5.4.2.1.2 step 3), its clients served at them
	// (Resolve).
	aliases map[string]bool

	sub *ua.Subscription
	// active: a NOTIFY of Subscription-State active came (RFC 6665 §4.1.3), whose documents tell the contacts that
	// go; until, a deregistration removes all the bindings of the IMPU.
	active bool
	// version is that of the last document, once one came (RFC 3680 §5.2).
	version   uint32
	versioned bool
	stopped   bool
	// restored: of bindings Restore put back, until the first full document, which removes those it does not report
	// registered (staleIn); a subscription of them that fails is made again, after retry, attempts the failures.
	restored bool
	attempts int
}

// watching is the subscription to make, and those to end, of a change: a subscription for an IMPU with bindings, of
// a SIP URI to subscribe to, through the IMS with an agent, if it has none or one toward another S-CSCF, which it
// replaces; the end of those of the IMPUs left without bindings, and of those replaced. Called with g.mu held.
func (g *Registrar) watching(ch *change) (start *watch, stop []*ua.Subscription) {
	if g.cfg.Mode != ThroughIMS {
		return nil, nil
	}

	end := func(w *watch) {
		// One still being sent is unsubscribed once sent.
		w.stopped = true
		if w.sub != nil {
			stop = append(stop, w.sub)
		}
	}

	for ik := range ch.impus {
		if w := g.watches[ik]; w != nil && !g.has(ik) {
			delete(g.watches, ik)
			end(w)
		}
	}

	if w := ch.watch; w != nil && g.cfg.Agent != nil && w.impu.IsSIP() && g.has(w.ik) {
		old := g.watches[w.ik]
		if old != nil && old.scscf.Equivalent(w.scscf) {
			return start, stop
		}

		if old != nil {
			// The bindings restored of the IMPU are reconciled by the subscription that replaces theirs.
			w.restored = old.restored
			end(old)
		}

		g.watches[w.ik] = w
		start = w
	}

	return start, stop
}

// has reports whether the IMPU of key ik has bindings. Called with g.mu held.
func (g *Registrar) has(ik string) bool { return len(g.impus[ik]) > 0 }

// subscribe sends the SUBSCRIBE of w on the strand of its IMPU (TS 24.229 §5.7.1.1 a to f), of the default duration
// (RFC 3680 §4.4), which the agent refreshes. A SUBSCRIBE that cannot be sent leaves the IMPU without subscription,
// until its next third-party REGISTER.
func (g *Registrar) subscribe(st *ua.Strand, w *watch) {
	domain := g.cfg.Source.Current().ServiceConfiguration().Domain

	req := sip.NewRequest("SUBSCRIBE", w.impu)
	req.Header.Set("From", sip.Address{URI: w.as}.String())
	req.Header.Set("To", sip.Address{URI: w.impu}.String())
	req.Header.Set("Event", "reg")
	req.Header.Set("Accept", reginfo.ContentType)
	req.Header.Set("P-Asserted-Identity", sip.Address{URI: w.as}.String())
	req.Header.Set("P-Charging-Vector", identity.ChargingVector(domain))

	route := w.scscf.Clone()
	route.Params.Set("lr", "")
	req.Header.Set("Route", sip.Address{URI: route}.String())

	sub, err := g.cfg.Agent.Subscribe(req, ua.SubscriptionHandler{
		Notify: func(s *ua.Subscription, r *ua.Request) { g.notify(st, w, s, r) },
		Ended:  func(_ *ua.Subscription, e ua.End) { g.ended(st, w, e) },
	}, ua.On(st))

	g.mu.Lock()

	if err != nil {
		var after time.Duration
		if w.restored {
			after = g.retry(st, w, nil)
		} else {
			g.drop(w)
		}

		g.mu.Unlock()
		g.cfg.Logger.Warn("reg event SUBSCRIBE not sent", slog.String("impu", w.impu.String()), slog.Any("error", err),
			slog.Duration("retry_after", after))

		return
	}

	w.sub = sub
	stopped := w.stopped

	g.mu.Unlock()

	if stopped {
		unsubscribe(sub)
	}
}

// unsubscribe ends a subscription (RFC 6665 §4.1.2.3); one ended already needs nothing.
func unsubscribe(s *ua.Subscription) {
	_ = s.Unsubscribe(ua.Change{})
}

// ended forgets the subscription, ended by its notifier, a refusal or an unsubscribe: the next third-party REGISTER
// of the IMPU subscribes again. Of bindings restored, not reconciled yet: a 480, of an IMPU the S-CSCF has no binding
// of (TS 24.229 §5.4.2.1.1 step 0), removes them; a 403, of a subscriber not authorised (step 1), leaves the
// registration state unknown, the bindings kept until they expire or a third-party REGISTER changes them, and is not
// sent again (RFC 3261 §21.4.4); another refusal, of an S-CSCF unavailable, subscribes again later. A failure of no
// response, of an S-CSCF unreachable, as while the core of the IMS restarts, or of an expiry not refreshed,
// subscribes again later, restored or not, as Resubscribe does once the core runs again.
func (g *Registrar) ended(st *ua.Strand, w *watch, e ua.End) {
	var (
		ch    change
		after time.Duration
		code  int
	)

	if e.Response != nil {
		code = e.Response.StatusCode
	}

	g.mu.Lock()

	switch {
	case g.watches[w.ik] != w || g.closed:
		g.drop(w)
	case !w.restored && e.Cause == ua.EndRejected:
		g.drop(w)
	case e.Cause == ua.EndRejected && code == 480:
		g.unrestored(w, &ch)
		g.drop(w)
	case e.Cause == ua.EndRejected && code == 403:
		g.drop(w)
	case e.Cause == ua.EndClosed, e.Cause == ua.EndUnsubscribed, e.Cause == ua.EndDropped, e.Cause == ua.EndTerminated:
		g.drop(w)
	default:
		after = g.retry(st, w, e.Response)
	}

	g.mu.Unlock()

	switch {
	case w.restored && code == 403:
		g.cfg.Logger.Error("reg event subscription refused: the server is not an AS of the user's initial filter "+
			"criteria, or not of the IMS's trust domain; its registrations restored stay until they expire",
			slog.String("impu", w.impu.String()))
	case after > 0:
		g.cfg.Logger.Warn("reg event subscription failed: subscribing again", slog.String("impu", w.impu.String()),
			slog.String("cause", e.Cause.String()), slog.Int("code", code), slog.Duration("retry_after", after))
	default:
		g.cfg.Logger.Debug("reg event subscription ended", slog.String("impu", w.impu.String()),
			slog.String("cause", e.Cause.String()))
	}

	g.apply(st, g.cfg.Clock.Now(), &ch)
}

// The bounds of the waits between the subscriptions of bindings restored that failed.
const (
	minRetry = 30 * time.Second
	maxRetry = 10 * time.Minute
)

// retry schedules the subscription of w again, of bindings restored: after the Retry-After of res, if any, else
// doubling from minRetry to maxRetry, on its strand. It returns the wait. Called with g.mu held.
func (g *Registrar) retry(st *ua.Strand, w *watch, res *sip.Response) time.Duration {
	after := min(minRetry<<min(w.attempts, 5), maxRetry)

	if res != nil {
		if v, err := strconv.ParseUint(strings.TrimSpace(strings.SplitN(res.Header.Get("Retry-After"), ";", 2)[0]), 10, 32); err == nil && v > 0 {
			after = time.Duration(v) * time.Second
		}
	}

	w.attempts++
	w.sub = nil

	st.AfterFunc(after, func() {
		g.mu.Lock()
		current := g.watches[w.ik] == w && !w.stopped && w.sub == nil && !g.closed && g.has(w.ik)
		g.mu.Unlock()

		if current {
			g.subscribe(st, w)
		}
	})

	return after
}

// unrestored removes the bindings of the IMPU of w restored, not reconciled since, of its S-CSCF: the registration
// of the IMPU is gone. Called with g.mu held.
func (g *Registrar) unrestored(w *watch, ch *change) {
	for _, b := range g.impus[w.ik] {
		if b.restored && b.SCSCF.Equivalent(w.scscf) {
			g.unbind(b, ch, causeDeregistered)
		}
	}
}

// drop removes w, unless replaced. Called with g.mu held.
func (g *Registrar) drop(w *watch) {
	if g.watches[w.ik] == w {
		delete(g.watches, w.ik)
	}
}

// notify handles a NOTIFY of w (TS 24.229 §5.7.1.1; RFC 3680 §5.2): of a document not older than the last, the
// bindings of the contacts of the IMPU it reports terminated go, their clients logged off, and all of them with the
// registration of the IMPU itself terminated, or back to init (§4.7.1: no contact is registered). Only the contacts
// a document reports go: one a full document leaves out stays until it expires, should the S-CSCF tell it apart
// from the binding otherwise than the server does. A NOTIFY from outside the trust domain gets a 403; a document
// that does not decode, a 400, which leaves the bindings. The 200 has the P-Charging-Vector of §5.7.1.1.
func (g *Registrar) notify(st *ua.Strand, w *watch, s *ua.Subscription, r *ua.Request) {
	req := r.Message()
	snap := g.cfg.Source.Current()
	now := g.cfg.Clock.Now()

	var header sip.Header
	if v := chargingVector(req, snap); v != "" {
		header = append(header, sip.Field{Name: "P-Charging-Vector", Value: v})
	}

	if g.cfg.Trusted == nil || !g.cfg.Trusted(req.Flow.Remote.Addr()) {
		g.cfg.Logger.Debug("reg event NOTIFY from outside the trust domain", slog.String("source", req.Flow.Remote.String()))

		_ = r.Respond(ua.Reply{Code: 403, Header: header})

		return
	}

	// A document the server cannot read is answered 400, which keeps the subscription (RFC 6665 §4.2.2): its
	// Subscription-State applies (§4.1.3), and a refresh asks for the full state again (RFC 3680 §5.2).
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
			g.cfg.Logger.Warn("unreadable reg event document: refreshing", slog.String("impu", w.impu.String()),
				slog.Any("error", err))

			unreadable = true
		} else {
			doc = &d
		}
	}

	var ch change

	g.mu.Lock()

	stopped := w.stopped

	switch s.State() {
	case ua.SubscriptionActive:
		w.active = true
	case ua.SubscriptionTerminated:
		// RFC 6665 §4.1.3, §4.4.1: the subscription ends with this NOTIFY, and nothing is left to unsubscribe
		// (TS 24.229 §5.7.1.1 NOTE 5); the next third-party REGISTER of the IMPU subscribes again. Of bindings
		// restored, not reconciled by a document: "noresource" and "rejected", of a registration gone, remove them
		// (§3.2.4); another reason subscribes again. Of a document unreadable, but "rejected", a new subscription.
		w.stopped, w.sub = true, nil
		g.drop(w)

		switch {
		case w.restored && (doc == nil || doc.State != reginfo.Full):
			switch reason(r.Message()) {
			case "noresource", "rejected":
				g.unrestored(w, &ch)
			default:
				again := &watch{ik: w.ik, impu: w.impu, scscf: w.scscf, as: w.as, restored: true, attempts: w.attempts}
				g.watches[w.ik] = again
				g.retry(st, again, nil)
			}
		case unreadable && reason(r.Message()) != "rejected" && g.has(w.ik):
			// What the document told is unknown: a new subscription tells it again.
			again := &watch{ik: w.ik, impu: w.impu, scscf: w.scscf, as: w.as}
			g.watches[w.ik] = again

			st.AfterFunc(0, func() {
				g.mu.Lock()
				current := g.watches[w.ik] == again && again.sub == nil && !again.stopped && !g.closed
				g.mu.Unlock()

				if current {
					g.subscribe(st, again)
				}
			})
		}
	}

	// A subscription replaced, or ended, tells nothing more.
	if doc != nil && !stopped && (!w.versioned || doc.Version > w.version) {
		w.version, w.versioned = doc.Version, true
		w.registered(doc)
		g.deregistered(w.ik, doc, &ch)

		if w.restored && doc.State == reginfo.Full {
			g.staleIn(w, doc, now, &ch)
			w.restored = false
		}
	}

	g.mu.Unlock()

	if unreadable {
		_ = r.Respond(ua.Reply{Code: 400, Header: header})

		if s.State() != ua.SubscriptionTerminated && !stopped {
			_ = s.Refresh(ua.Change{}, nil)
		}
	} else {
		_ = r.Respond(ua.Reply{Code: 200, Header: header})
	}

	g.apply(st, now, &ch)
}

// registered updates the aliases of w of doc: those of a full document replace them (RFC 3680 §5.2).
func (w *watch) registered(doc *reginfo.Reginfo) {
	if w.aliases == nil || doc.State == reginfo.Full {
		w.aliases = map[string]bool{}
	}

	for _, reg := range doc.Registrations {
		aor, err := sip.ParseURI(reg.AOR)
		if err != nil {
			continue
		}

		if k := settings.Key(aor); reg.State == reginfo.Active {
			w.aliases[k] = true
		} else {
			delete(w.aliases, k)
		}
	}
}

// deregistered removes the bindings of the IMPU of key ik that doc reports deregistered. Called with g.mu held.
func (g *Registrar) deregistered(ik string, doc *reginfo.Reginfo, ch *change) {
	for _, reg := range doc.Registrations {
		if aor, err := sip.ParseURI(reg.AOR); err != nil || settings.Key(aor) != ik {
			continue
		}

		if reg.State != reginfo.Active {
			g.unbindIMPU(ik, ueReg{}, ch)
			continue
		}

		for _, c := range reg.Contacts {
			if c.State != reginfo.Terminated {
				continue
			}

			for _, b := range g.impus[ik] {
				if b.terminatedBy(c) {
					g.unbind(b, ch, causeDeregistered)
				}
			}
		}
	}
}

// terminatedBy reports whether a contact reported terminated is of the registration of b: of the "id" its
// registration token (TS 24.229 §5.4.1.7 NOTE 4), which an S-CSCF may make it, else of its instance ID and reg-id
// (§5.4.2.1.2: unknown-params of the contact), else of its URI. A contact last updated by a REGISTER of b's Call-ID of a lower CSeq than b's (RFC 3680 §5.1) is of an earlier
// registration of the UE, whose NOTIFY came late.
func (b *binding) terminatedBy(c reginfo.Contact) bool {
	if b.callID != "" && c.CallID == b.callID && c.CSeq != nil && *c.CSeq < uint64(b.cseq) {
		return false
	}

	return b.matchedBy(c)
}

// reason is the reason of the Subscription-State of a NOTIFY, "" if none.
func reason(n *sip.Request) string {
	ss, err := sip.ParseSubscriptionState(n.Header.Get("Subscription-State"))
	if err != nil {
		return ""
	}

	return ss.Reason
}

// matchedBy reports whether a contact of a reg event document is of the registration of b: of the "id" its
// registration token, else of its instance ID and reg-id, else of its URI (terminatedBy).
func (b *binding) matchedBy(c reginfo.Contact) bool {
	if b.reg.token != "" && c.ID == b.reg.token {
		return true
	}

	var o ueReg

	if v, ok := c.Param("+sip.instance"); ok {
		o.instance = instanceID(v)
	}

	o.regID, _ = c.Param("reg-id")

	if u, err := sip.ParseURI(c.URI); err == nil {
		o.contact = u
	}

	return b.reg.withoutToken().same(o)
}
