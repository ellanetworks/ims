package registrar

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/reginfo"
	"github.com/ellanetworks/ims/sip/ua"
	"go.opentelemetry.io/otel/trace"
)

// TS 24.229 clause 5.7.1.1
// RFC 3680
type watch struct {
	impu, scscf, as sip.URI
	ik              string
	aliases         map[string]bool

	sub       *ua.Subscription
	active    bool
	version   uint32
	versioned bool
	stopped   bool
	restored  bool
	attempts  int
}

func (g *Registrar) watching(ch *change) (start *watch, stop []*ua.Subscription) {
	if g.cfg.Mode != ThroughIMS {
		return nil, nil
	}

	end := func(w *watch) {
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
			w.restored = old.restored
			end(old)
		}

		g.watches[w.ik] = w
		start = w
	}

	return start, stop
}

func (g *Registrar) has(ik string) bool { return len(g.impus[ik]) > 0 }

// TS 24.229 clause 5.7.1.1
// RFC 3680 section 4.4
func (g *Registrar) subscribe(ctx context.Context, st *ua.Strand, w *watch) error {
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

	sub, err := g.cfg.Agent.Subscribe(ctx, req, ua.SubscriptionHandler{
		Notify: func(ctx context.Context, s *ua.Subscription, r *ua.Request) { g.notify(ctx, st, w, s, r) },
		Ended:  func(ctx context.Context, _ *ua.Subscription, e ua.End) { g.ended(ctx, st, w, e) },
	}, ua.On(st))

	g.mu.Lock()

	if err != nil {
		var after time.Duration
		if w.restored {
			after = g.retry(ctx, st, w, nil)
		} else {
			g.drop(w)
		}

		g.mu.Unlock()
		g.cfg.Logger.WarnContext(ctx, "reg event SUBSCRIBE not sent", slog.String("impu", w.impu.String()), slog.Any("error", err),
			slog.Duration("retry_after", after))

		return err
	}

	w.sub = sub
	stopped := w.stopped

	g.mu.Unlock()

	if stopped {
		unsubscribe(ctx, sub)
	}

	return nil
}

// RFC 6665 section 4.1.2.3
func unsubscribe(ctx context.Context, s *ua.Subscription) {
	_ = s.Unsubscribe(ctx, ua.Change{})
}

// TS 24.229 clause 5.4.2.1.1
func (g *Registrar) ended(ctx context.Context, st *ua.Strand, w *watch, e ua.End) {
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
		after = g.retry(ctx, st, w, e.Response)
	}

	g.mu.Unlock()

	switch {
	case w.restored && code == 403:
		g.cfg.Logger.ErrorContext(ctx, "reg event subscription refused: the server is not an AS of the user's initial filter "+
			"criteria, or not of the IMS's trust domain; its registrations restored stay until they expire",
			slog.String("impu", w.impu.String()))
	case after > 0:
		g.cfg.Logger.WarnContext(ctx, "reg event subscription failed: subscribing again", slog.String("impu", w.impu.String()),
			slog.String("cause", e.Cause.String()), slog.Int("code", code), slog.Duration("retry_after", after))
	default:
		g.cfg.Logger.DebugContext(ctx, "reg event subscription ended", slog.String("impu", w.impu.String()),
			slog.String("cause", e.Cause.String()))
	}

	g.apply(ctx, st, g.cfg.Clock.Now(), &ch)
}

const (
	minRetry = 30 * time.Second
	maxRetry = 10 * time.Minute
)

func (g *Registrar) retry(ctx context.Context, st *ua.Strand, w *watch, res *sip.Response) time.Duration {
	after := min(minRetry<<min(w.attempts, 5), maxRetry)

	if res != nil {
		if v, err := strconv.ParseUint(strings.TrimSpace(strings.SplitN(res.Header.Get("Retry-After"), ";", 2)[0]), 10, 32); err == nil && v > 0 {
			after = time.Duration(v) * time.Second
		}
	}

	w.attempts++
	w.sub = nil

	g.detach(trace.SpanContextFromContext(ctx), st, after, resubscribeName, func(ctx context.Context) error {
		g.mu.Lock()
		current := g.watches[w.ik] == w && !w.stopped && w.sub == nil && !g.closed && g.has(w.ik)
		g.mu.Unlock()

		if !current {
			return nil
		}

		return g.subscribe(ctx, st, w)
	})

	return after
}

func (g *Registrar) unrestored(w *watch, ch *change) {
	for _, b := range g.impus[w.ik] {
		if b.restored && b.SCSCF.Equivalent(w.scscf) {
			g.unbind(b, ch, causeDeregistered)
		}
	}
}

func (g *Registrar) drop(w *watch) {
	if g.watches[w.ik] == w {
		delete(g.watches, w.ik)
	}
}

// TS 24.229 clause 5.7.1.1
// RFC 3680 section 5.2
func (g *Registrar) notify(ctx context.Context, st *ua.Strand, w *watch, s *ua.Subscription, r *ua.Request) {
	req := r.Message()
	snap := g.cfg.Source.Current()
	now := g.cfg.Clock.Now()

	var header sip.Header
	if v := chargingVector(req, snap); v != "" {
		header = append(header, sip.Field{Name: "P-Charging-Vector", Value: v})
	}

	if g.cfg.Trusted == nil || !g.cfg.Trusted(req.Flow.Remote.Addr()) {
		g.cfg.Logger.DebugContext(ctx, "reg event NOTIFY from outside the trust domain", slog.String("source", req.Flow.Remote.String()))

		_ = r.Respond(ua.Reply{Code: 403, Header: header})

		return
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
			g.cfg.Logger.WarnContext(ctx, "unreadable reg event document: refreshing", slog.String("impu", w.impu.String()),
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
				g.retry(ctx, st, again, nil)
			}
		case unreadable && reason(r.Message()) != "rejected" && g.has(w.ik):
			again := &watch{ik: w.ik, impu: w.impu, scscf: w.scscf, as: w.as}
			g.watches[w.ik] = again

			g.detach(trace.SpanContextFromContext(ctx), st, 0, resubscribeName, func(ctx context.Context) error {
				g.mu.Lock()
				current := g.watches[w.ik] == again && again.sub == nil && !again.stopped && !g.closed
				g.mu.Unlock()

				if !current {
					return nil
				}

				return g.subscribe(ctx, st, again)
			})
		}
	}

	if doc != nil && !stopped && (!w.versioned || doc.Version > w.version) {
		w.version, w.versioned = doc.Version, true
		w.registered(doc)
		g.deregistered(w, doc, &ch)

		if w.restored && doc.State == reginfo.Full {
			g.staleIn(w, doc, now, &ch)
			w.restored = false
		}
	}

	g.mu.Unlock()

	if unreadable {
		_ = r.Respond(ua.Reply{Code: 400, Header: header})

		if s.State() != ua.SubscriptionTerminated && !stopped {
			_ = s.Refresh(ctx, ua.Change{}, nil)
		}
	} else {
		_ = r.Respond(ua.Reply{Code: 200, Header: header})
	}

	g.apply(ctx, st, now, &ch)
}

// RFC 3680 section 5.2
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

// RFC 3680 section 5.1
// TS 23.228 clause 4.3.3.2
func (g *Registrar) reports(w *watch, aor string) bool {
	u, err := sip.ParseURI(aor)
	if err != nil {
		return false
	}

	k := settings.Key(u)
	if k == "" {
		return false
	}

	if k == w.ik || k == settings.Key(w.impu) {
		return true
	}

	for _, b := range g.impus[w.ik] {
		for _, u := range b.IMPUs {
			if settings.Key(u) == k {
				return true
			}
		}
	}

	return false
}

func (g *Registrar) deregistered(w *watch, doc *reginfo.Reginfo, ch *change) {
	ik := w.ik

	for _, reg := range doc.Registrations {
		if !g.reports(w, reg.AOR) {
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

// TS 24.229 clause 5.4.1.7
// RFC 3680 section 5.1
func (b *binding) terminatedBy(c reginfo.Contact) bool {
	if b.callID != "" && c.CallID == b.callID && c.CSeq != nil && *c.CSeq < uint64(b.cseq) {
		return false
	}

	return b.matchedBy(c)
}

func reason(n *sip.Request) string {
	ss, err := sip.ParseSubscriptionState(n.Header.Get("Subscription-State"))
	if err != nil {
		return ""
	}

	return ss.Reason
}

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
