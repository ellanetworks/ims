package registrar

import (
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

// registrationToken is the Contact parameter of TS 24.229 §7.9.7, as a feature tag.
const registrationToken = "g.3gpp.registration-token"

// thirdParty answers a third-party REGISTER of the S-CSCF (TS 24.229 §5.7.1.1): To the IMPU, Contact the S-CSCF with
// the registration token, the expiration the S-CSCF granted, and a message/sip body of the client's REGISTER
// (§5.4.1.7, §5.4.1.7A). Its 200 has the same expiration (§5.7.1.1) and, of a service authorisation, the mcptt-info
// of §7.3.2 step 6; a failed authorisation gets a 403, which the S-CSCF's default handling does not act on
// (§5.4.1.7), with the warning text of TS 24.379 §7.3.3 step 6, the one of a failed service authorisation. Every
// response has the P-Charging-Vector of §5.7.1.1.
//
// The client is served at the implicit registration set of the IMPU (Resolve): the P-Associated-URI of the 200 to
// the client's REGISTER (§5.4.1.2.2F b), of its message/sip body (§5.4.1.7A); else the P-Associated-URI of the
// third-party REGISTER itself, as an S-CSCF that sends no 200 adds it; the To always.
//
// The registration of the UE among those of the IMPU is that of its registration token, which the S-CSCF may give
// (§5.4.1.7 l), else of the instance ID or Contact URI of the client's REGISTER (§5.7.1.3B NOTE; RFC 5626 §4.1): a
// REGISTER of the client without mcptt-info, as a reregistration may be (TS 33.180 §5.1.3.2.1), refreshes the
// bindings of its registration, if they are still authorised; without the MCPTT feature tags, it logs the client of
// its registration off (TS 24.379 §7.2.1 NOTE 1), not those of the other UEs of the IMPU. A REGISTER that tells no
// registration, of neither token nor body, refreshes all the bindings of the IMPU. A token is the same until the UE is
// deregistered (§5.4.1.7 l): a REGISTER of the instance ID or Contact of a binding and of another token is a new
// registration of its UE, whose deregistration the server missed, or which another S-CSCF serves; the binding takes
// its token and S-CSCF.
//
// The third-party REGISTERs of the REGISTERs of a UE may come out of order: the S-CSCF sends each once its REGISTER is
// answered, and TS 24.229 orders them no further. The REGISTERs of the UE are, of one Call-ID and a CSeq incremented
// (RFC 3261 §10.2.4): a third-party REGISTER of a client's REGISTER of the Call-ID of the last applied at the IMPU, and
// a CSeq not higher, was sent before it, and is ignored (§10.3 step 7), with a 200 as the S-CSCF needs no other: a
// refusal of a 5xx may end the registration (TS 24.229 §5.4.1.7). The last is kept until its registration expires, whether it
// bound, refreshed or logged off: an earlier REGISTER with mcptt-info does not log the client of a later log-off on
// again. The third-party REGISTERs themselves tell no order: an S-CSCF may make each of a new Call-ID.
//
// A deregistration, of an expiration of zero, carries neither body nor token (§5.4.1.7 g, l), and contacts of the
// IMPU may stay registered after it (NOTE 2): the server subscribes to the reg event package of each IMPU it has
// bindings of (§5.7.1.1), and the NOTIFYs tell which contacts went. Without that subscription, not yet active,
// refused or failed, a deregistration removes all the bindings of the IMPU.
func (g *Registrar) thirdParty(r *ua.Request, snap *settings.Snapshot, now time.Time) {
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
		g.cfg.Logger.Debug("REGISTER out of order ignored", slog.String("impu", impu.String()),
			slog.Uint64("cseq", uint64(seq.cseq)))

		_ = r.Respond(ua.Reply{Code: 200, Header: ok})

		return
	}

	var ch change

	if secs == 0 {
		g.mu.Lock()

		// A deregistration of a token or body, not of §5.4.1.7, tells its registration; another is told by the
		// NOTIFYs of an active subscription.
		if w := g.watches[ik]; ue.known() || w == nil || !w.active {
			g.unbindIMPU(ik, ue, &ch)
		}

		g.mu.Unlock()

		_ = r.Respond(ua.Reply{Code: 200, Header: ok})
		g.apply(r.Strand(), now, &ch)

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

			// The bindings no longer authorised went: their clients logged off, and their rows.
			g.apply(r.Strand(), now, &ch)

			return
		}

		refreshed = true
	}

	if ordered {
		g.sequenced(seq, expires, &ch)
	}

	ch.watch = &watch{ik: ik, impu: subscribed(impus), scscf: scscf.URI, as: req.URI}

	g.mu.Unlock()

	_ = r.Respond(ua.Reply{Code: 200, Header: ok, Bodies: bodies})

	switch {
	case a != nil:
		g.authorised(a, impu)
	case refreshed:
		g.cfg.Logger.Debug("registration refreshed", slog.String("impu", impu.String()))
	}

	g.apply(r.Strand(), now, &ch)
}

// ueReg is the registration of a UE through the IMS, among those of its IMPU (TS 24.229 §5.7.1.3B): its
// registration token (§5.4.1.7 l), the instance ID (RFC 5626 §4.1) and reg-id (§4.2) and Contact URI of its REGISTER,
// each "" or zero if unknown.
type ueReg struct {
	token, instance, regID string
	contact                sip.URI
}

// ueOf is the registration of a third-party REGISTER, of token and the client's REGISTER reg, nil if none: of the
// Contact of reg of the MCPTT feature tag, else its first.
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

// same reports whether u and o are one registration: of the same token, else of the same instance ID and reg-id,
// else of the same Contact URI (RFC 3261 §19.1.4); not if they have nothing to compare. Of different tokens, the same
// instance ID and reg-id, or Contact, is a new registration of the UE (§5.4.1.7 l: the token is the same until the UE
// is deregistered). An instance's flows of different reg-ids are registrations of their own, the S-CSCF's (TS 24.229
// §5.4.1.2.2 step 5 d; RFC 5626 §4.2): the MCPTT client's and VoLTE's of one device, of its IMEI (§5.1.1.2.1).
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

// adopt takes the registration ue of a REGISTER of the UE of b, client its REGISTER if the third-party REGISTER has
// it: a new token (§5.4.1.7 l), and what it tells of the UE.
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

// registered takes the Call-ID and CSeq of client, the REGISTER of the UE of b, if any.
func (b *binding) registered(client *sip.Request) {
	if client == nil {
		return
	}

	b.callID = client.Header.CallID()
	if cseq, err := client.Header.CSeq(); err == nil {
		b.cseq = cseq.Seq
	}
}

// seqKey is the dialog of REGISTERs of a UE at an IMPU, by IMPU key and Call-ID (RFC 3261 §10.2.4).
type seqKey struct{ impu, callID string }

// sequence is the last REGISTER of a UE applied at an IMPU: its CSeq, of the Call-ID of its key, until the
// registration it made expires.
type sequence struct {
	key     seqKey
	cseq    uint32
	expires time.Time
	timer   *ua.Timer
}

// sequenceOf is the sequence of client, the REGISTER of a UE at the IMPU of key ik; false if client is nil or of no
// Call-ID or CSeq.
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

// outOfOrder reports whether s is of a REGISTER of the UE not later than the last applied of its Call-ID (RFC 3261
// §10.3 step 7).
func (g *Registrar) outOfOrder(s *sequence) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	last := g.sequences[s.key]

	return last != nil && s.cseq <= last.cseq
}

// sequenced makes s, of a registration expiring at expires, the last REGISTER applied of its Call-ID, its timer to
// arm. Called with g.mu held.
func (g *Registrar) sequenced(s *sequence, expires time.Time, ch *change) {
	if last := g.sequences[s.key]; last != nil && last.timer != nil {
		last.timer.Stop()
	}

	s.expires = expires
	g.sequences[s.key] = s
	ch.sequences = append(ch.sequences, s)
}

// forget removes s, its registration expired, unless a later REGISTER replaced it.
func (g *Registrar) forget(s *sequence) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.sequences[s.key] == s {
		delete(g.sequences, s.key)
	}
}

// instanceID is the instance ID of a "+sip.instance" value: the URN of the quoted string, without its angle brackets
// (RFC 5626 §4.1).
func instanceID(v string) string {
	return strings.TrimSuffix(strings.TrimPrefix(sip.Unquote(v), "<"), ">")
}

// sameInstance compares instance IDs as URNs (RFC 5626 §4.1): the "urn" and NID case-insensitively (RFC 8141 §3),
// the NSS exactly but of a UUID, of hexadecimal digits of either case (RFC 4122 §3).
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

// implicitSet is the implicit registration set of impu, the To of a third-party REGISTER, impu first: of the
// P-Associated-URI of the 200 to the client's REGISTER in a message/sip body (§5.4.1.7A, §5.4.1.2.2F b), else of
// the P-Associated-URI of the third-party REGISTER; impu alone without either.
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

// subscribed is the IMPU of the reg event subscription of the implicit registration set impus, its first SIP URI:
// the To of the third-party REGISTER, or, of a tel URI, a SIP URI of its set (§5.7.1.1 a, c: "a SIP URI that contains
// the public user identity"); zero if none.
func subscribed(impus []sip.URI) sip.URI {
	if i := slices.IndexFunc(impus, sip.URI.IsSIP); i >= 0 {
		return impus[i]
	}

	return sip.URI{}
}

// registerResponse is the 200 to the client's REGISTER of the message/sip bodies of a third-party REGISTER
// (§5.4.1.7A), nil if none.
func registerResponse(req *sip.Request) *sip.Response {
	for _, m := range messages(req) {
		if res, ok := m.(*sip.Response); ok && res.StatusCode/100 == 2 {
			return res
		}
	}

	return nil
}

// messages are the messages of the message/sip bodies of a third-party REGISTER (§5.4.1.7A).
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

// withContact is u of the Contact c.
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

// unbindIMPU removes the bindings of the IMPU of key ik of the registration ue, all if ue is unknown, logging their
// clients off. Called with g.mu held.
func (g *Registrar) unbindIMPU(ik string, ue ueReg, ch *change) {
	for _, b := range g.impus[ik] {
		if !ue.known() || b.reg.same(ue) {
			g.unbind(b, ch, causeDeregistered)
		}
	}
}

// refresh refreshes by update the bindings of the IMPU of key ik of the registration ue, all if ue is unknown, whose
// users the configuration still authorises at it; the others go. It reports false if the IMPU had such bindings,
// and none stays. Called with g.mu held.
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

// clientRegister is the client's REGISTER of the message/sip bodies of a third-party REGISTER (TS 24.229 §5.4.1.7A),
// nil if none.
func clientRegister(req *sip.Request) *sip.Request {
	for _, m := range messages(req) {
		if reg, ok := m.(*sip.Request); ok && reg.Method == "REGISTER" {
			return reg
		}
	}

	return nil
}

// mcptt reports whether a REGISTER of a client has a Contact of the MCPTT feature tag (TS 24.379 §7.2.1).
func mcptt(reg *sip.Request) bool {
	contacts, err := reg.Header.Contacts()
	return err == nil && slices.ContainsFunc(contacts, isMCPTT)
}

func isMCPTT(c sip.Address) bool {
	fs, _ := sip.ParseFeatureSet(c.Params)
	return fs.Has(strings.TrimPrefix(identity.FeatureTag, "+"))
}

// charging are the charging values of a third-party REGISTER to store (TS 24.229 §5.7.1.1).
func charging(req *sip.Request) Charging {
	c := identity.ChargingOf(req.Header)

	return Charging{ICID: c.ICID, OrigIOI: c.OrigIOI, FunctionAddresses: req.Header.Get("P-Charging-Function-Addresses")}
}

// chargingVector is the P-Charging-Vector of the response to a third-party REGISTER (TS 24.229 §5.7.1.1): its
// icid-value and orig-ioi, and a type 3 term-ioi of the organisation's domain; "" without an icid-value.
func chargingVector(req *sip.Request, snap *settings.Snapshot) string {
	c := identity.ChargingOf(req.Header)
	if c.ICID == "" {
		return ""
	}

	return c.Response(snap.ServiceConfiguration().Domain)
}
