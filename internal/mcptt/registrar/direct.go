package registrar

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// RFC 3261 section 10.3
// TS 24.379 clause 7.3.2
func (g *Registrar) direct(ctx context.Context, r *ua.Request, snap *settings.Snapshot, now time.Time) {
	req := r.Message()

	if req.URI.User != "" || !strings.EqualFold(req.URI.Host, snap.ServiceConfiguration().Domain) {
		g.reject(r, snap, &refusal{code: 404})
		return
	}

	to, err1 := req.Header.To()
	contacts, err2 := req.Header.Contacts()
	cseq, err3 := req.Header.CSeq()

	if err1 != nil || err2 != nil || err3 != nil {
		g.reject(r, snap, &refusal{code: 400})
		return
	}

	aor := to.URI
	ik := settings.Key(aor)

	secs, err := req.Header.Expires()
	hasExpires := err == nil

	if req.Header.Has("Expires") && !hasExpires {
		g.reject(r, snap, &refusal{code: 400})
		return
	}

	star := slices.ContainsFunc(contacts, func(c sip.Address) bool { return c.Star })
	if star && (len(contacts) != 1 || !hasExpires || secs != 0) {
		g.reject(r, snap, &refusal{code: 400})
		return
	}

	a, f := g.authorise(snap, req, []sip.URI{aor})
	if f != nil {
		g.reject(r, snap, f)
		return
	}

	var ch change

	g.mu.Lock()

	u := &update{g: g, snap: snap, now: now, ik: ik, callID: req.Header.CallID(), cseq: cseq.Seq}
	u.load()

	if a != nil {
		u.authorised(a, aor)
	}

	code := 0

	switch {
	case star:
		code = u.removeAll()
	default:
		for _, c := range contacts {
			exp := g.cfg.DefaultExpires
			if hasExpires {
				exp = time.Duration(secs) * time.Second
			}

			if v, ok := c.Params.Get("expires"); ok {
				if n, err := strconv.ParseUint(v, 10, 32); err == nil {
					exp = time.Duration(n) * time.Second
				}
			}

			if code = u.contact(c, exp); code != 0 {
				break
			}
		}
	}

	if code != 0 {
		g.mu.Unlock()
		g.reject(r, snap, &refusal{code: code})

		return
	}

	u.commit(&ch)

	header := g.contactsOf(ik, now)

	var bodies []sip.BodyPart

	if a != nil && u.target != nil && len(u.target.contacts) > 0 {
		bodies = g.devices(u.target.user)
		ch.implicit = a
	}

	g.mu.Unlock()

	_ = r.Respond(ua.Reply{Code: 200, Header: header, Bodies: bodies})

	if ch.implicit != nil {
		g.authorised(ctx, ch.implicit, aor)
	}

	g.apply(ctx, r.Strand(), now, &ch)
}

// RFC 3261 section 10.3
type update struct {
	g      *Registrar
	snap   *settings.Snapshot
	now    time.Time
	ik     string
	callID string
	cseq   uint32

	copies map[bindingKey]*binding
	target *binding
}

func (u *update) load() {
	u.copies = map[bindingKey]*binding{}

	for k, b := range u.g.impus[u.ik] {
		if cfg := u.snap.User(b.MCPTTID); cfg == nil || !cfg.HasIMPU(b.IMPU) {
			continue
		}

		c := *b
		c.contacts = slices.Clone(b.contacts)
		u.copies[k] = &c
	}
}

func (u *update) authorised(a *authorisation, aor sip.URI) {
	k := bindingKey{u.ik, a.client}
	user := settings.Key(a.user.ID())

	b := u.copies[k]
	if b == nil || b.user != user {
		b = &binding{
			key: k, user: user, direct: true,
			Binding: Binding{MCPTTID: a.user.ID(), ClientID: a.client, IMPU: aor, IMPUs: []sip.URI{aor}, Since: u.now},
		}
		u.copies[k] = b
	}

	u.target = b
}

func (u *update) find(uri sip.URI) (*binding, int) {
	for _, b := range u.copies {
		if i := slices.IndexFunc(b.contacts, func(c contact) bool { return c.addr.URI.Equivalent(uri) }); i >= 0 {
			return b, i
		}
	}

	return nil, -1
}

// RFC 3261 section 10.3
func (u *update) contact(c sip.Address, exp time.Duration) int {
	b, i := u.find(c.URI)

	if b != nil && b.contacts[i].callID == u.callID && u.cseq <= b.contacts[i].cseq {
		return 500
	}

	if b != nil {
		b.contacts = slices.Delete(b.contacts, i, i+1)
	}

	if exp == 0 {
		return 0
	}

	t := u.target
	if t == nil {
		t = b
	}

	if t == nil {
		return 403
	}

	addr := c.Clone()
	addr.Params.Del("expires")
	t.contacts = append(t.contacts, contact{addr: addr, expires: u.now.Add(exp), callID: u.callID, cseq: u.cseq})

	return 0
}

func (u *update) removeAll() int {
	for _, b := range u.copies {
		for _, c := range b.contacts {
			if c.callID == u.callID && u.cseq <= c.cseq {
				return 500
			}
		}
	}

	for _, b := range u.copies {
		b.contacts = b.contacts[:0]
	}

	return 0
}

func (u *update) commit(ch *change) {
	g := u.g

	for k, b := range g.impus[u.ik] {
		if c := u.copies[k]; c == nil || c.user != b.user {
			cause := causeUnauthorised
			if c != nil {
				cause = causeRebound
			}

			g.unbind(b, ch, cause)
		}
	}

	for k, c := range u.copies {
		c.prune(u.now)

		if len(c.contacts) == 0 {
			if b := g.bindings[k]; b != nil {
				g.unbind(b, ch, causeDeregistered)
			}

			continue
		}

		if b := g.bindings[k]; b != nil {
			c.timer = b.timer

			g.remove(b)
		}

		g.bind(c, ch)
	}
}

// RFC 3261 section 10.3
func (g *Registrar) contactsOf(ik string, now time.Time) sip.Header {
	var h sip.Header

	for _, b := range g.impus[ik] {
		for _, c := range b.contacts {
			a := c.addr.Clone()
			a.Params.Set("expires", strconv.FormatInt(int64(c.expires.Sub(now)/time.Second), 10))
			h = append(h, sip.Field{Name: "Contact", Value: a.String()})
		}
	}

	return h
}
