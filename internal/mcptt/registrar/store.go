package registrar

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/lifetime"
	"github.com/ellanetworks/ims/sip/reginfo"
	"go.opentelemetry.io/otel/trace"
)

type Store interface {
	SaveRegistrations(ctx context.Context, records []Record, deleted []string) error
}

type Record struct {
	Binding

	AS              sip.URI
	Instance, RegID string
	Contact         sip.URI
	CallID          string
	CSeq            uint32
}

func (b *binding) record() Record {
	r := Record{
		Binding: b.public(), AS: b.as, Instance: b.reg.instance, RegID: b.reg.regID, Contact: b.reg.contact,
		CallID: b.callID, CSeq: b.cseq,
	}
	r.Contacts = nil

	return r
}

func (g *Registrar) persist(ctx context.Context) {
	g.mu.Lock()
	clean := len(g.dirty) == 0
	g.mu.Unlock()

	if clean {
		return
	}

	g.saving.Lock()
	defer g.saving.Unlock()

	g.mu.Lock()

	var (
		saved   []Record
		deleted []string
	)

	if g.cfg.Store != nil {
		for client := range g.dirty {
			if b := g.clients[client]; b != nil && !b.direct {
				saved = append(saved, b.record())
			} else {
				deleted = append(deleted, client)
			}
		}
	}

	clear(g.dirty)
	g.mu.Unlock()

	if len(saved)+len(deleted) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(lifetime.With(g.life, ctx), saveTimeout)
	defer cancel()

	if err := g.cfg.Store.SaveRegistrations(ctx, saved, deleted); err != nil {
		g.cfg.Logger.WarnContext(ctx, "registrations not saved", slog.Int("clients", len(saved)+len(deleted)), slog.Any("error", err))

		g.mu.Lock()

		for _, r := range saved {
			g.dirty[r.ClientID] = true
		}

		for _, c := range deleted {
			g.dirty[c] = true
		}

		g.mu.Unlock()
	}
}

const saveTimeout = 5 * time.Second

func (g *Registrar) Restore(ctx context.Context, records []Record) []Binding {
	snap := g.cfg.Source.Current()
	now := g.cfg.Clock.Now()

	var out []Binding

	latest := map[string]time.Time{}
	cause := trace.SpanContextFromContext(ctx)

	g.mu.Lock()

	for _, r := range records {
		ik := settings.Key(r.IMPU)

		u := snap.User(r.MCPTTID)
		if g.cfg.Mode != ThroughIMS || ik == "" || g.clients[r.ClientID] != nil || !now.Before(r.Expires) || u == nil ||
			!slices.ContainsFunc(r.IMPUs, u.HasIMPU) {
			g.dirty[r.ClientID] = true
			continue
		}

		b := &binding{
			Binding: r.Binding, key: bindingKey{ik, r.ClientID}, user: settings.Key(r.MCPTTID),
			reg:    ueReg{token: r.Token, instance: r.Instance, regID: r.RegID, contact: r.Contact},
			callID: r.CallID, cseq: r.CSeq, as: r.AS, restored: true,
		}

		g.put(b)

		st := g.cfg.StrandOf(ik)
		b.timer = g.detach(cause, st, b.Expires.Sub(now), expiryName, func(ctx context.Context) error {
			g.expire(ctx, st, b)
			return nil
		})

		if r.CallID != "" {
			s := &sequence{key: seqKey{ik, r.CallID}, cseq: r.CSeq, expires: r.Expires}
			if last := g.sequences[s.key]; last == nil || last.cseq < s.cseq {
				if last != nil {
					last.timer.Stop()
				}

				s.timer = st.AfterFunc(s.expires.Sub(now), func() { g.forget(s) })
				g.sequences[s.key] = s
			}
		}

		impu := subscribed(r.IMPU, r.IMPUs, g.cfg.HomeDomain)
		if w := g.watches[ik]; r.AS.Scheme != "" && impu.IsSIP() && (w == nil || r.Expires.After(latest[ik])) {
			g.watches[ik] = &watch{ik: ik, impu: impu, scscf: r.SCSCF, as: r.AS, restored: true}
			latest[ik] = r.Expires
		}

		out = append(out, b.public())
	}

	for _, b := range out {
		delete(g.dirty, b.ClientID)
	}

	g.mu.Unlock()

	g.persist(ctx)

	return out
}

const resubscribePace = 20 * time.Millisecond

// TS 24.229 clause 5.7.1.1
func (g *Registrar) Resubscribe(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.cfg.Agent == nil {
		return
	}

	ws := make([]*watch, 0, len(g.watches))
	for _, w := range g.watches {
		if w.sub == nil && !w.stopped {
			ws = append(ws, w)
		}
	}

	slices.SortFunc(ws, func(a, b *watch) int { return strings.Compare(a.ik, b.ik) })

	cause := trace.SpanContextFromContext(ctx)

	for i, w := range ws {
		st := g.cfg.StrandOf(w.ik)
		g.detach(cause, st, time.Duration(i)*resubscribePace, resubscribeName, func(ctx context.Context) error {
			g.mu.Lock()
			current := g.watches[w.ik] == w && !w.stopped && w.sub == nil && !g.closed
			g.mu.Unlock()

			if !current {
				return nil
			}

			return g.subscribe(ctx, st, w)
		})
	}
}

// RFC 3680 section 5.1
func (g *Registrar) staleIn(w *watch, doc *reginfo.Reginfo, now time.Time, ch *change) {
	var active []reginfo.Contact

	for _, reg := range doc.Registrations {
		if !g.reports(w, reg.AOR) || reg.State != reginfo.Active {
			continue
		}

		for _, c := range reg.Contacts {
			if c.State == reginfo.Active {
				active = append(active, c)
			}
		}
	}

	for _, b := range g.impus[w.ik] {
		if !b.restored {
			continue
		}

		i := slices.IndexFunc(active, b.matchedLoosely)
		if i < 0 {
			g.unbind(b, ch, causeDeregistered)
			continue
		}

		b.restored = false

		if e := active[i].Expires; e != nil && *e > 0 {
			b.Expires = now.Add(time.Duration(*e) * time.Second)
			g.dirty[b.key.client] = true
			ch.armed = append(ch.armed, b)
		}
	}
}

func (b *binding) matchedLoosely(c reginfo.Contact) bool {
	if b.matchedBy(c) {
		return true
	}

	if v, ok := c.Param("+sip.instance"); ok && b.reg.instance != "" && sameInstance(instanceID(v), b.reg.instance) {
		regID, _ := c.Param("reg-id")
		if regID == "" || b.reg.regID == "" {
			return true
		}
	}

	u, err := sip.ParseURI(c.URI)

	return err == nil && b.reg.contact.Scheme != "" && b.reg.contact.Equivalent(u)
}
