package registrar

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// TS 24.229 clause 7.9.7
const registrationToken = "g.3gpp.registration-token"

// TS 24.229 clause 5.7.1.1
// TS 24.229 clause 5.4.1.7
// TS 24.229 clause 5.4.1.7A
func (g *Registrar) thirdParty(ctx context.Context, r *ua.Request, snap *settings.Snapshot, now time.Time) {
	req := r.Message()

	var header sip.Header
	if v := chargingVector(req, snap); v != "" {
		header = append(header, sip.Field{Name: "P-Charging-Vector", Value: v})
	}

	to, err1 := req.Header.To()
	contacts, err2 := req.Header.Contacts()

	if err1 != nil || err2 != nil || len(contacts) != 1 || contacts[0].Star {
		g.reject(r, snap, &refusal{code: 400}, header...)
		return
	}

	impu, scscf := to.URI, contacts[0]
	ik := settings.Key(impu)

	secs, err := req.Header.Expires()
	if err != nil {
		v, ok := scscf.Params.Get("expires")
		if n, perr := strconv.ParseUint(v, 10, 32); !ok || perr != nil {
			g.reject(r, snap, &refusal{code: 400}, header...)
			return
		} else {
			secs = uint32(n)
		}
	}

	ok := append(slices.Clone(header), sip.Field{Name: "Expires", Value: strconv.FormatUint(uint64(secs), 10)})

	var token string
	if fs, _ := sip.ParseFeatureSet(scscf.Params); len(fs.Tokens(registrationToken)) > 0 {
		token = fs.Tokens(registrationToken)[0]
	}

	client := clientRegister(req)
	ue := ueOf(token, client)
	impus := implicitSet(req, impu)
	seq, ordered := sequenceOf(ik, client)

	if ordered && g.outOfOrder(seq) {
		g.cfg.Logger.DebugContext(ctx, "REGISTER out of order ignored", slog.String("impu", impu.String()),
			slog.Uint64("cseq", uint64(seq.cseq)))

		_ = r.Respond(ua.Reply{Code: 200, Header: ok})

		return
	}

	var ch change

	if secs == 0 {
		g.mu.Lock()

		if w := g.watches[ik]; ue.known() || w == nil || !w.active {
			g.unbindIMPU(ik, ue, &ch)
		}

		g.mu.Unlock()

		_ = r.Respond(ua.Reply{Code: 200, Header: ok})
		g.apply(ctx, r.Strand(), now, &ch)

		return
	}

	expires := now.Add(time.Duration(secs) * time.Second)

	var a *authorisation

	if client != nil {
		var f *refusal
		if a, f = g.authorise(snap, client, impus); f != nil {
			g.reject(r, snap, f, header...)
			return
		}
	}

	var (
		bodies    []sip.BodyPart
		refreshed bool
	)

	g.mu.Lock()

	switch {
	case a != nil:
		b := &binding{
			key: bindingKey{ik, a.client}, user: settings.Key(a.user.ID()), reg: ue, as: req.URI,
			Binding: Binding{
				MCPTTID: a.user.ID(), ClientID: a.client, IMPU: impu, IMPUs: impus, Expires: expires, Since: now,
				SCSCF: scscf.URI, Token: token, Charging: charging(req),
			},
		}
		b.registered(client)

		g.bind(b, &ch)
		bodies = g.devices(b.user)
		ch.implicit = a
	case client != nil && !mcptt(client):
		g.unbindIMPU(ik, ue, &ch)
	default:
		if !g.refresh(snap, ik, ue, &ch, func(b *binding) {
			b.Expires, b.SCSCF, b.Charging, b.as, b.restored = expires, scscf.URI, charging(req), req.URI, false
			b.adopt(ue, client)

			if len(impus) > 1 || len(b.IMPUs) == 0 {
				b.IMPUs = impus
			}
		}) {
			g.mu.Unlock()
			g.reject(r, snap, failed, header...)

			g.apply(ctx, r.Strand(), now, &ch)

			return
		}

		refreshed = true
	}

	if ordered {
		g.sequenced(seq, expires, &ch)
	}

	ch.watch = &watch{ik: ik, impu: subscribed(impu, impus, g.cfg.HomeDomain), scscf: scscf.URI, as: req.URI}

	g.mu.Unlock()

	_ = r.Respond(ua.Reply{Code: 200, Header: ok, Bodies: bodies})

	switch {
	case a != nil:
		g.authorised(ctx, a, impu)
	case refreshed:
		g.cfg.Logger.DebugContext(ctx, "registration refreshed", slog.String("impu", impu.String()))
	}

	g.apply(ctx, r.Strand(), now, &ch)
}

// TS 24.229 clause 5.7.1.3B
type ueReg struct {
	token, instance, regID string
	contact                sip.URI
}

func ueOf(token string, reg *sip.Request) ueReg {
	u := ueReg{token: token}
	if reg == nil {
		return u
	}

	contacts, err := reg.Header.Contacts()
	if err != nil || len(contacts) == 0 || contacts[0].Star {
		return u
	}

	c := contacts[0]
	if i := slices.IndexFunc(contacts, isMCPTT); i >= 0 {
		c = contacts[i]
	}

	return u.withContact(c)
}

func (u ueReg) known() bool { return u.token != "" || u.instance != "" || u.contact.Scheme != "" }

// RFC 3261 section 19.1.4
// TS 24.229 clause 5.4.1.7
func (u ueReg) same(o ueReg) bool {
	switch {
	case u.token != "" && u.token == o.token:
		return true
	case u.instance != "" && o.instance != "":
		return sameInstance(u.instance, o.instance) && u.regID == o.regID
	case u.contact.Scheme != "" && o.contact.Scheme != "":
		return u.contact.Equivalent(o.contact)
	}

	return false
}

// TS 24.229 clause 5.4.1.7
func (b *binding) adopt(ue ueReg, client *sip.Request) {
	if ue.token != "" {
		b.reg.token, b.Token = ue.token, ue.token
	}

	if ue.instance != "" {
		b.reg.instance, b.reg.regID = ue.instance, ue.regID
	}

	if ue.contact.Scheme != "" {
		b.reg.contact = ue.contact
	}

	b.registered(client)
}

func (b *binding) registered(client *sip.Request) {
	if client == nil {
		return
	}

	b.callID = client.Header.CallID()
	if cseq, err := client.Header.CSeq(); err == nil {
		b.cseq = cseq.Seq
	}
}

// RFC 3261 section 10.2.4
type seqKey struct{ impu, callID string }

type sequence struct {
	key     seqKey
	cseq    uint32
	expires time.Time
	timer   *ua.Timer
}

func sequenceOf(ik string, client *sip.Request) (*sequence, bool) {
	if client == nil {
		return nil, false
	}

	cseq, err := client.Header.CSeq()
	if id := client.Header.CallID(); err == nil && id != "" {
		return &sequence{key: seqKey{ik, id}, cseq: cseq.Seq}, true
	}

	return nil, false
}

// RFC 3261 section 10.3
func (g *Registrar) outOfOrder(s *sequence) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	last := g.sequences[s.key]

	return last != nil && s.cseq <= last.cseq
}

func (g *Registrar) sequenced(s *sequence, expires time.Time, ch *change) {
	if last := g.sequences[s.key]; last != nil && last.timer != nil {
		last.timer.Stop()
	}

	s.expires = expires
	g.sequences[s.key] = s
	ch.sequences = append(ch.sequences, s)
}

func (g *Registrar) forget(s *sequence) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.sequences[s.key] == s {
		delete(g.sequences, s.key)
	}
}

// RFC 5626 section 4.1
func instanceID(v string) string {
	return strings.TrimSuffix(strings.TrimPrefix(sip.Unquote(v), "<"), ">")
}

// RFC 5626 section 4.1
// RFC 8141 section 3
// RFC 4122 section 3
func sameInstance(a, b string) bool {
	pa, pb := strings.SplitN(a, ":", 3), strings.SplitN(b, ":", 3)
	if len(pa) != 3 || len(pb) != 3 || !strings.EqualFold(pa[0], pb[0]) || !strings.EqualFold(pa[1], pb[1]) {
		return a == b
	}

	if strings.EqualFold(pa[1], "uuid") {
		return strings.EqualFold(pa[2], pb[2])
	}

	return pa[2] == pb[2]
}

// TS 24.229 clause 5.4.1.7A
// TS 24.229 clause 5.4.1.2.2F
func implicitSet(req *sip.Request, impu sip.URI) []sip.URI {
	pau := req.Header
	if res := registerResponse(req); res != nil && res.Header.Has("P-Associated-URI") {
		pau = res.Header
	}

	set := []sip.URI{impu}

	as, _ := pau.Addresses("P-Associated-URI")
	for _, a := range as {
		if !slices.ContainsFunc(set, func(u sip.URI) bool { return settings.Key(u) == settings.Key(a.URI) }) {
			set = append(set, a.URI)
		}
	}

	return set
}

// TS 24.229 clause 5.7.1.1
// TS 23.003 clause 13.4
func subscribed(impu sip.URI, set []sip.URI, home string) sip.URI {
	if impu.IsSIP() {
		return impu
	}

	if u := numberURI(impu, home); u.IsSIP() {
		return u
	}

	if i := slices.IndexFunc(set, sip.URI.IsSIP); i >= 0 {
		return set[i]
	}

	return sip.URI{}
}

// TS 23.003 clause 13.4
// RFC 3261 section 19.1.6
// RFC 3966 section 5.1.1
// RFC 3966 section 5.1.5
func numberURI(u sip.URI, home string) sip.URI {
	if !u.IsTel() || home == "" {
		return sip.URI{}
	}

	id := u.Identity()
	n := id.User
	pc, local := id.Params.Get("phone-context")

	switch {
	case !local && len(n) >= 2 && n[0] == '+' && strings.Trim(n[1:], "0123456789") == "":
	case local && n != "" && pc != "" && strings.Trim(n, "0123456789abcdef*#") == "":
		n = strings.ReplaceAll(n, "#", "%23") + ";phone-context=" + pc
	default:
		return sip.URI{}
	}

	s := sip.URI{Scheme: "sip", User: n, Host: home}
	s.Params.Set("user", "phone")

	return s
}

// TS 24.229 clause 5.4.1.7A
func registerResponse(req *sip.Request) *sip.Response {
	for _, m := range messages(req) {
		if res, ok := m.(*sip.Response); ok && res.StatusCode/100 == 2 {
			return res
		}
	}

	return nil
}

// TS 24.229 clause 5.4.1.7A
func messages(req *sip.Request) []sip.Message {
	parts, err := req.Content().Parts()
	if err != nil {
		return nil
	}

	var out []sip.Message

	for _, p := range parts {
		if p.MediaType() != "message/sip" {
			continue
		}

		if m, err := sip.Parse(p.Body); err == nil {
			out = append(out, m)
		}
	}

	return out
}

func (u ueReg) withContact(c sip.Address) ueReg {
	u.contact = c.URI
	if v, ok := c.Params.Get("+sip.instance"); ok {
		u.instance = instanceID(v)
	}

	u.regID, _ = c.Params.Get("reg-id")

	return u
}

func (u ueReg) withoutToken() ueReg {
	u.token = ""
	return u
}

func (g *Registrar) unbindIMPU(ik string, ue ueReg, ch *change) {
	for _, b := range g.impus[ik] {
		if !ue.known() || b.reg.same(ue) {
			g.unbind(b, ch, causeDeregistered)
		}
	}
}

func (g *Registrar) refresh(snap *settings.Snapshot, ik string, ue ueReg, ch *change, update func(*binding)) bool {
	found, kept := false, false

	for _, b := range g.impus[ik] {
		if ue.known() && !b.reg.same(ue) {
			continue
		}

		found = true

		if u := snap.User(b.MCPTTID); u == nil || !slices.ContainsFunc(b.IMPUs, u.HasIMPU) {
			g.unbind(b, ch, causeUnauthorised)
			continue
		}

		kept = true

		update(b)
		g.dirty[b.key.client] = true
		ch.armed = append(ch.armed, b)
	}

	return kept || !found
}

// TS 24.229 clause 5.4.1.7A
func clientRegister(req *sip.Request) *sip.Request {
	for _, m := range messages(req) {
		if reg, ok := m.(*sip.Request); ok && reg.Method == "REGISTER" {
			return reg
		}
	}

	return nil
}

// TS 24.379 clause 7.2.1
func mcptt(reg *sip.Request) bool {
	contacts, err := reg.Header.Contacts()
	return err == nil && slices.ContainsFunc(contacts, isMCPTT)
}

func isMCPTT(c sip.Address) bool {
	fs, _ := sip.ParseFeatureSet(c.Params)
	return fs.Has(strings.TrimPrefix(identity.FeatureTag, "+"))
}

// TS 24.229 clause 5.7.1.1
func charging(req *sip.Request) Charging {
	c := identity.ChargingOf(req.Header)

	return Charging{ICID: c.ICID, OrigIOI: c.OrigIOI, FunctionAddresses: req.Header.Get("P-Charging-Function-Addresses")}
}

// TS 24.229 clause 5.7.1.1
func chargingVector(req *sip.Request, snap *settings.Snapshot) string {
	c := identity.ChargingOf(req.Header)
	if c.ICID == "" {
		return ""
	}

	return c.Response(snap.ServiceConfiguration().Domain)
}
