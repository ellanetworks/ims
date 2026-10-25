package registrar

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/reginfo"
)

// Store keeps the bindings through the IMS across restarts. SaveRegistrations saves the bindings of records, one per
// client, replacing the client's, and removes those of the clients of deleted.
type Store interface {
	SaveRegistrations(ctx context.Context, records []Record, deleted []string) error
}

// Record is what is kept of a binding through the IMS, for Restore to put it back: the binding, the registration of
// the UE that the third-party REGISTERs and the reg event documents tell it by (TS 24.229 §5.7.1.3B), the Call-ID and
// CSeq of its last REGISTER, and the AS URI that subscribes to the reg event package of its IMPU. The access token is
// not kept: the MCPTT ID it gave is.
type Record struct {
	Binding

	// AS is the Request-URI of the third-party REGISTER (TS 24.229 §5.4.1.7).
	AS sip.URI
	// Instance and RegID are the "+sip.instance" and "reg-id" of the Contact of the UE's REGISTER (RFC 5626 §4.1,
	// §4.2), Contact its URI; empty if unknown.
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

// persist writes the bindings changed through the IMS to the store, outside the lock: of each client changed, its
// binding, or its removal. A write that fails is logged, and its clients written with the next change.
func (g *Registrar) persist() {
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

	ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
	defer cancel()

	if err := g.cfg.Store.SaveRegistrations(ctx, saved, deleted); err != nil {
		g.cfg.Logger.Warn("registrations not saved", slog.Int("clients", len(saved)+len(deleted)), slog.Any("error", err))

		// Saved with the next change, of whatever client.
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

// saveTimeout bounds a write to the store, of the strand of an IMPU.
const saveTimeout = 5 * time.Second

// Restore puts back the bindings of records through the IMS, before the agent serves: of those not expired, whose
// users the settings still authorise at their IMPUs, as a refresh would (refresh); the others are removed from the
// store. Their expiry timers, and those of the sequences of their REGISTERs, are armed on the strands of their IMPUs.
// It returns the bindings put back; Resubscribe subscribes to the reg event package of their IMPUs.
func (g *Registrar) Restore(records []Record) []Binding {
	snap := g.cfg.Source.Current()
	now := g.cfg.Clock.Now()

	var out []Binding

	latest := map[string]time.Time{}

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
		b.timer = st.AfterFunc(b.Expires.Sub(now), func() { g.expire(st, b) })

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

		// The subscription of the IMPU, toward the S-CSCF of its latest registration.
		if w := g.watches[ik]; r.AS.Scheme != "" && subscribed(r.IMPUs).IsSIP() && (w == nil || r.Expires.After(latest[ik])) {
			g.watches[ik] = &watch{ik: ik, impu: subscribed(r.IMPUs), scscf: r.SCSCF, as: r.AS, restored: true}
			latest[ik] = r.Expires
		}

		out = append(out, b.public())
	}

	// Restored, not changed: only those removed are written.
	for _, b := range out {
		delete(g.dirty, b.ClientID)
	}

	g.mu.Unlock()

	g.persist()

	return out
}

// resubscribePace spaces the SUBSCRIBEs of Resubscribe, of 1,000 IMPUs at most (settings.MaxUsers) over 20 s.
const resubscribePace = 20 * time.Millisecond

// Resubscribe subscribes to the reg event package of the IMPUs of no subscription, once their S-CSCF is reachable:
// those Restore put bindings of back, and those whose subscription failed for want of a response (ended), one each
// resubscribePace, on the strands of the IMPUs (TS 24.229 §5.7.1.1). The first full document of each removes the
// bindings of the UEs it does not report registered, which deregistered meanwhile (notify), and a refusal of the
// subscription of bindings restored all the bindings of its IMPU (ended).
func (g *Registrar) Resubscribe() {
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

	for i, w := range ws {
		st := g.cfg.StrandOf(w.ik)
		st.AfterFunc(time.Duration(i)*resubscribePace, func() {
			g.mu.Lock()
			current := g.watches[w.ik] == w && !w.stopped && w.sub == nil && !g.closed
			g.mu.Unlock()

			if current {
				g.subscribe(st, w)
			}
		})
	}
}

// staleIn reconciles the bindings of the IMPU of w restored, not refreshed since, with the full document doc, the
// first of its subscription: one matched by an active contact of the registration of the IMPU (matchedLoosely) takes
// the expiry of the contact, if any (RFC 3680 §5.1), of a UE that registered again while the server was down; one
// matched by none goes, of a UE that deregistered meanwhile, all of them if the registration is not active. Unlike a
// later document (notify), the first is the IMS's state of a registration the server was told of before it
// restarted. Called with g.mu held.
func (g *Registrar) staleIn(w *watch, doc *reginfo.Reginfo, now time.Time, ch *change) {
	var active []reginfo.Contact

	for _, reg := range doc.Registrations {
		if aor, err := sip.ParseURI(reg.AOR); err != nil || settings.Key(aor) != w.ik || reg.State != reginfo.Active {
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

// matchedLoosely reports whether an active contact may be of the registration of b, restored: matchedBy, else of
// its instance ID, a reg-id on one side only, else of its URI. A binding is removed when none matches, so the match
// errs on keeping it.
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
