package scscf

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
)

const autsLen = 14

type registerRequest struct {
	req       *sip.Request
	protected bool
	impu      string
	impuKey   string
	impi      string
	creds     *credentials
	contacts  []contactRequest
	star      bool
	callID    string
	cseq      uint32
	path      string
	out       []*outgoing

	// regID is set when a Contact has a reg-id, used or not, and outbound when the UE supports
	// outbound (RFC 5626 §4.2.1).
	regID, outbound bool

	// authFailed reports that the UE failed IMS-AKA.
	authFailed bool

	// hss is the HSS that authenticated the UE in this request, if it did.
	hss db.HSS
}

type contactRequest struct {
	addr    sip.Address
	expires time.Duration

	// instance is the contact's instance ID (RFC 5626 §4.1), and regID its reg-id when the multiple
	// registration mechanism applies to it (RFC 5626 §6), or else 0.
	instance string
	regID    int64
}

// binds reports whether stored is the binding the contact names: the same flow, or the same contact
// address (RFC 5626 §6, RFC 3261 §10.3).
func (c contactRequest) binds(stored db.Contact) bool {
	if c.regID != 0 {
		return stored.RegID == c.regID && stored.Instance == c.instance
	}

	if stored.Flow() {
		return false
	}

	u, err := sip.ParseURI(stored.URI)

	return err == nil && u.Equivalent(c.addr.URI)
}

// authKey is what the REGISTER authenticates: its flow, or the private identity's registration.
func (rr *registerRequest) authKey() authKey {
	for _, c := range rr.contacts {
		if c.regID != 0 {
			return authKey{impi: rr.impi, instance: c.instance, regID: c.regID}
		}
	}

	return authKey{impi: rr.impi}
}

// flows reports whether the REGISTER registers or deregisters a flow: whether the multiple
// registration mechanism applies to it (RFC 5626 §6).
func (rr *registerRequest) flows() bool {
	return slices.ContainsFunc(rr.contacts, func(c contactRequest) bool { return c.regID != 0 })
}

// firstHopLacksOutbound reports whether the UE asks for outbound through a first hop that does not
// support it (RFC 5626 §6, TS 24.229 §5.4.1.2.2 steps 3 and 4B).
func (rr *registerRequest) firstHopLacksOutbound() bool {
	return rr.regID && rr.outbound && !outbound(rr.path)
}

func (rr *registerRequest) deregister() bool {
	if rr.star {
		return true
	}

	for _, c := range rr.contacts {
		if c.expires != 0 {
			return false
		}
	}

	return true
}

func (r *Registrar) register(ctx context.Context, req *sip.Request) (*sip.Response, []*outgoing) {
	rr, res := r.parse(req)
	if res != nil {
		r.cfg.RegistrationAttempts.Answered(req, res, false)
		return res, nil
	}

	res = r.handleRegister(ctx, rr)
	r.cfg.RegistrationAttempts.Answered(req, res, rr.authFailed)

	return res, rr.out
}

func (r *Registrar) handleRegister(ctx context.Context, rr *registerRequest) *sip.Response {
	req := rr.req

	for _, c := range rr.contacts {
		if c.expires != 0 && c.expires < r.cfg.MinExpires {
			res := sip.NewResponse(req, 423, "")
			res.Header.Add("Min-Expires", strconv.Itoa(int(r.cfg.MinExpires/time.Second)))

			return res
		}
	}

	if err := r.lock(ctx, rr.impi); err != nil {
		return retryLater(req)
	}

	defer r.unlock(rr.impi)

	if res := r.flowLimit(ctx, rr); res != nil {
		return res
	}

	if ch := r.pendingChallenge(rr.authKey()); rr.creds.answers(ch) {
		return r.answer(ctx, rr, ch)
	}

	if !rr.protected || r.reauthDue(rr) {
		return r.challenge(ctx, rr, nil, 0, r.knownHSS(ctx, rr))
	}

	return r.refresh(ctx, rr)
}

func (r *Registrar) reauthDue(rr *registerRequest) bool {
	if rr.deregister() {
		return false
	}

	r.mu.Lock()
	last, ok := r.authAt[rr.authKey()]
	requested := r.reauth[rr.impi]
	r.mu.Unlock()

	// A re-authentication the network asked for applies to each flow until it authenticates again.
	if last.reauth < requested {
		return true
	}

	return r.cfg.ReauthInterval > 0 && (!ok || r.clock.Now().Sub(last.at) >= r.cfg.ReauthInterval)
}

func (r *Registrar) parse(req *sip.Request) (*registerRequest, *sip.Response) {
	to, err := req.Header.To()
	if err != nil || !to.URI.IsSIP() {
		return nil, sip.NewResponse(req, 400, "Bad To")
	}

	cseq, err := req.Header.CSeq()
	if err != nil {
		return nil, sip.NewResponse(req, 400, "Bad CSeq")
	}

	contacts, err := req.Header.Contacts()
	if err != nil && !errors.Is(err, sip.ErrMissingHeader) {
		return nil, sip.NewResponse(req, 400, "Bad Contact")
	}

	expires := uint32(r.cfg.MaxExpires / time.Second)

	expiresHeader, hasExpires := uint32(0), req.Header.Has("Expires")
	if hasExpires {
		if expiresHeader, err = req.Header.Expires(); err != nil {
			return nil, sip.NewResponse(req, 400, "Bad Expires")
		}

		expires = expiresHeader
	}

	rr := &registerRequest{
		req:      req,
		impu:     receivedIdentity(to.URI),
		impuKey:  identityKey(to.URI),
		callID:   req.Header.CallID(),
		cseq:     cseq.Seq,
		path:     strings.Join(req.Header.Values("Path"), ", "),
		outbound: hasOptionTag(req.Header, "Supported", "outbound"),
	}

	star := false
	for _, c := range contacts {
		star = star || c.Star
	}

	switch {
	case len(contacts) == 0:
		return nil, sip.NewResponse(req, 400, "Missing Contact")
	case star && (len(contacts) > 1 || !hasExpires || expiresHeader != 0):
		return nil, sip.NewResponse(req, 400, "Invalid Contact *")
	case star:
		rr.star = true
	default:
		registering, flowing := 0, false

		for _, c := range contacts {
			if v, ok := c.Params.Get("q"); ok {
				if _, err := sip.ParseQValue(v); err != nil {
					return nil, sip.NewResponse(req, 400, "Bad Contact q-value")
				}
			}

			rr.regID = rr.regID || c.Params.Has("reg-id")

			if contactExpires(c, expires) != 0 {
				registering++
				flowing = flowing || c.Params.Has("reg-id")
			}
		}

		// RFC 5626 §6: a REGISTER with a reg-id registers a single flow, and may deregister Contacts with
		// a zero expiry besides.
		if flowing && registering > 1 {
			return nil, sip.NewResponse(req, 400, "Several Contacts With reg-id")
		}

		for _, c := range selectContacts(contacts) {
			granted := contactExpires(c, expires)

			cr := contactRequest{addr: c, expires: min(time.Duration(granted)*time.Second, r.cfg.MaxExpires)}
			if res := cr.flow(req, rr.path); res != nil {
				return nil, res
			}

			rr.contacts = append(rr.contacts, cr)
		}
	}

	if rr.creds, err = parseAuthorization(req.Header, r.cfg.HomeDomain); err != nil {
		return nil, sip.NewResponse(req, 400, "Bad Authorization")
	}

	if rr.creds != nil {
		rr.impi = rr.creds.username
		rr.protected = strings.EqualFold(rr.creds.integrityProtected, integrityProtectedYes)
	} else {
		rr.impi = privateIdentity(to.URI)
	}

	return rr, nil
}

// flow sets the contact's instance ID and, when the multiple registration mechanism applies, its
// reg-id: it does when the contact has both and the first hop added "ob" to its Path; a reg-id is
// ignored otherwise (RFC 5626 §6, TS 24.229 §5.4.1.2.2 step 6d).
func (c *contactRequest) flow(req *sip.Request, path string) *sip.Response {
	if v, ok := c.addr.Params.Get("+sip.instance"); ok {
		c.instance = strings.TrimSuffix(strings.TrimPrefix(sip.Unquote(v), "<"), ">")
	}

	v, ok := c.addr.Params.Get("reg-id")
	if !ok {
		return nil
	}

	// RFC 5626 §11.1: 1 to 2^31 - 1
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 1 {
		return sip.NewResponse(req, 400, "Bad reg-id")
	}

	if c.instance != "" && outbound(path) {
		c.regID = n
	}

	return nil
}

// outbound reports whether the first hop of a Path added "ob" to its URI (RFC 5626 §5.1).
func outbound(path string) bool {
	hops, err := sip.ParseAddressList(path)

	return err == nil && len(hops) > 0 && hops[0].URI.Params.Has("ob")
}

// contactExpires is the expiry a Contact asks for: its "expires" parameter, or else that of the
// request (RFC 3261 §10.2.1.1).
func contactExpires(c sip.Address, expires uint32) uint32 {
	if v, ok := c.Params.Get("expires"); ok {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			return uint32(n)
		}
	}

	return expires
}

func selectContacts(contacts []sip.Address) []sip.Address {
	var unique []sip.Address

	for _, c := range contacts {
		if !slicesContainsURI(unique, c.URI) {
			unique = append(unique, c)
		}
	}

	sameAddress := true

	for _, c := range unique[1:] {
		if !strings.EqualFold(c.URI.Host, unique[0].URI.Host) || c.URI.Port != unique[0].URI.Port {
			sameAddress = false
			break
		}
	}

	if sameAddress {
		return unique
	}

	best := 0

	for i, c := range unique {
		if QValue(c.Params) > QValue(unique[best].Params) {
			best = i
		}
	}

	return unique[best : best+1]
}

func slicesContainsURI(addrs []sip.Address, u sip.URI) bool {
	for _, a := range addrs {
		if a.URI.Equivalent(u) {
			return true
		}
	}

	return false
}

// knownHSS is the HSS stored for the Public Identity of a REGISTER, the last that answered for its registration set,
// if it has one (TS 29.229 §5.5).
func (r *Registrar) knownHSS(ctx context.Context, rr *registerRequest) db.HSS {
	st, err := r.load(ctx, rr.impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", rr.impi), slog.Any("error", err))
		return db.HSS{}
	}

	if set := st.set(rr.impuKey); set != nil {
		return set.HSS
	}

	return db.HSS{}
}

func (r *Registrar) challenge(ctx context.Context, rr *registerRequest, resync *cx.Resync, resyncs int, to db.HSS) *sip.Response {
	v, from, err := r.multimediaAuth(ctx, to, rr.impi, rr.impu, resync)
	if err != nil {
		return r.cxFailure(rr, err)
	}

	nonce := akaNonce(v)

	r.putChallenge(rr.authKey(), &challenge{
		callID:  rr.callID,
		impu:    rr.impu,
		impuKey: rr.impuKey,
		nonce:   nonce,
		vector:  v,
		resyncs: resyncs,
		hss:     from,
	})

	r.log.Debug("challenged REGISTER", slog.String("impi", rr.impi), slog.String("impu", rr.impu),
		slog.Bool("resync", resync != nil))

	res := sip.NewResponse(rr.req, 401, "")
	res.Header.Add("WWW-Authenticate", wwwAuthenticate(r.cfg.HomeDomain, nonce, v))

	// TS 24.229 §5.4.1.2.1 step 6, "as described in RFC 5626": only for a flow, of a UE that supports outbound
	// (RFC 5626 §6; RFC 3261 §8.2.4: no extension the request's Supported does not list).
	if rr.flows() && rr.outbound {
		res.Header.Add("Require", "outbound")
	}

	return res
}

func (r *Registrar) answer(ctx context.Context, rr *registerRequest, ch *challenge) *sip.Response {
	c := rr.creds

	switch {
	case c.auts == "" && c.response != "" && !rr.protected:
		return r.challenge(ctx, rr, nil, 0, ch.hss)
	case rr.callID != ch.callID || rr.impuKey != ch.impuKey || !strings.EqualFold(c.algorithm, algorithmAKAv1):
		return r.authFailed(ctx, rr, ch)
	case c.auts != "":
		auts, err := base64.StdEncoding.DecodeString(c.auts)
		if err != nil || len(auts) != autsLen || ch.resyncs >= maxResyncs {
			return r.authFailed(ctx, rr, ch)
		}

		r.dropChallenge(rr.authKey(), ch)

		return r.challenge(ctx, rr, &cx.Resync{RAND: ch.vector.rand, AUTS: auts}, ch.resyncs+1, ch.hss)
	case c.response == "":
		return r.authFailed(ctx, rr, ch)
	case !verify(c, rr.req.Method, ch.nonce, ch.vector.xres):
		return r.authFailed(ctx, rr, ch)
	}

	r.dropChallenge(rr.authKey(), ch)

	r.mu.Lock()
	r.authAt[rr.authKey()] = authenticated{at: r.clock.Now(), reauth: r.reauth[rr.impi]}
	r.mu.Unlock()

	rr.hss = ch.hss

	return r.authenticated(ctx, rr)
}

func (r *Registrar) authFailed(ctx context.Context, rr *registerRequest, ch *challenge) *sip.Response {
	r.dropChallenge(rr.authKey(), ch)

	rr.authFailed = true

	r.log.Info("REGISTER failed authentication", slog.String("impi", rr.impi), slog.String("impu", rr.impu))

	if _, _, err := r.serverAssignment(ctx, ch.hss, rr.impi, []string{ch.impu}, assignAuthenticationFailure, false); err != nil {
		r.log.Warn("failed to tell the HSS of an authentication failure", slog.String("impi", rr.impi), slog.Any("error", err))
	}

	return sip.NewResponse(rr.req, 403, "")
}

func (r *Registrar) authenticated(ctx context.Context, rr *registerRequest) *sip.Response {
	st, err := r.load(ctx, rr.impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req)
	}

	set := st.set(rr.impuKey)

	if rr.deregister() {
		if !st.registered(set) {
			return noBinding(rr)
		}

		return r.unbind(ctx, rr, st, set)
	}

	if rr.firstHopLacksOutbound() {
		return firstHopLacksOutbound(rr)
	}

	// TS 24.229 §5.4.1.2.2 step 4A: without the multiple registration mechanism, a new contact
	// replaces the others of the private identity.
	return r.assign(ctx, rr, st, set, !rr.flows())
}

func (r *Registrar) refresh(ctx context.Context, rr *registerRequest) *sip.Response {
	st, err := r.load(ctx, rr.impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req)
	}

	if !st.any() {
		r.log.Debug("protected REGISTER from an unregistered user", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
		return sip.NewResponse(rr.req, 500, "")
	}

	set := st.set(rr.impuKey)

	if rr.deregister() {
		if !st.registered(set) {
			return noBinding(rr)
		}

		return r.unbind(ctx, rr, st, set)
	}

	if rr.firstHopLacksOutbound() {
		return firstHopLacksOutbound(rr)
	}

	// TS 24.229 §5.4.1.2.2 step 2: a new contact address registers through an initial registration;
	// a new flow does not need one.
	for _, c := range rr.contacts {
		if _, ok := st.contact(c); c.expires != 0 && c.regID == 0 && !ok {
			r.log.Debug("protected REGISTER from an unregistered contact", slog.String("impi", rr.impi),
				slog.String("contact", c.addr.URI.String()))

			return sip.NewResponse(rr.req, 403, "")
		}
	}

	if !st.registered(set) {
		return r.assign(ctx, rr, st, set, false)
	}

	res, removed := r.bind(ctx, rr, st, *set, false)
	if res.StatusCode == 200 {
		r.releaseCalls(st, removed)
		rr.out = r.notifyChange(ctx, rr.impi, change{removed: removed})
	}

	return res
}

func (r *Registrar) assign(ctx context.Context, rr *registerRequest, st *state, set *db.Registration, replace bool) (res *sip.Response) {
	registered := st.registered(set)

	t := assignRegistration
	if registered {
		t = assignReRegistration
	}

	to := rr.hss
	if to.Host == "" && set != nil {
		to = set.HSS
	}

	// The user data the set holds came from its HSS: another HSS sends its own.
	available := registered && len(set.UserData) > 0 && to.Host != "" && strings.EqualFold(to.Host, set.HSS.Host)

	saa, from, err := r.serverAssignment(ctx, to, rr.impi, []string{rr.impu}, t, available)
	if err != nil {
		return r.cxFailure(rr, err)
	}

	reg := db.Registration{IMPI: rr.impi}
	if set != nil {
		reg = *set
	}

	reg.HSS = from

	undo := assignAdministrative

	if !registered {
		defer func() {
			if res.StatusCode != 200 && !st.registeredAny(reg.Identities) {
				r.deregisterAtHSS(ctx, from, rr.impi, rr.impu, undo)
			}
		}()
	}

	var ch change

	if expired := without(reg.Bindings, st.live(reg.Bindings)); len(expired) > 0 {
		ch.removed = append(ch.removed, removal{reg: reg, bindings: expired, event: regevent.Expired})
	}

	reg.Bindings = st.live(reg.Bindings)

	switch {
	case len(saa.UserData) > 0:
		sub, err := cx.ParseUserData(saa.UserData)
		if err != nil {
			r.log.Warn("invalid User-Data from the HSS", slog.String("impi", rr.impi), slog.Any("error", err))

			undo = assignTooMuchData

			return sip.NewResponse(rr.req, 480, "")
		}

		reg.Identities = implicitSet(sub)
		reg.UserData = saa.UserData
	case !registered:
		r.log.Warn("SAA without User-Data", slog.String("impi", rr.impi))
		return sip.NewResponse(rr.req, 500, "")
	}

	if !holds(reg.Identities, rr.impuKey) || !hasUnbarred(reg.Identities) {
		r.log.Debug("REGISTER of an IMPU outside its set, or of a set that is all barred",
			slog.String("impi", rr.impi), slog.String("impu", rr.impu))

		return sip.NewResponse(rr.req, 403, "")
	}

	for _, old := range st.overlapping(reg) {
		if err := r.cfg.DB.DeleteRegistration(ctx, old.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
			r.log.Warn("failed to delete an expired registration", slog.String("impi", rr.impi), slog.Any("error", err))
			return retryLater(rr.req)
		}
	}

	if replace {
		kept := only(reg.Bindings, rr.contacts)

		if dropped := without(reg.Bindings, kept); len(dropped) > 0 {
			ch.removed = append(ch.removed, removal{reg: reg, bindings: dropped, event: regevent.Unregistered})
		}

		reg.Bindings = kept
	}

	res, removed := r.bind(ctx, rr, st, reg, !registered)
	if res.StatusCode != 200 {
		return res
	}

	ch.removed = append(ch.removed, removed...)

	if replace {
		ch.removed = append(ch.removed, r.replaceContacts(ctx, rr, st, reg.ID)...)
	}

	r.releaseCalls(st, ch.removed)
	rr.out = r.notifyChange(ctx, rr.impi, ch)

	return res
}

func (r *Registrar) bind(ctx context.Context, rr *registerRequest, st *state, reg db.Registration,
	initial bool,
) (*sip.Response, []removal) {
	now := r.clock.Now()
	set := reg
	reg.IMPU = rr.impu
	bindings := st.live(reg.Bindings)

	var deregistered, replaced []db.Binding

	for _, c := range rr.contacts {
		i := bindingIndex(bindings, c)

		if i >= 0 && bindings[i].CallID == rr.callID && bindings[i].CSeq >= int64(rr.cseq) {
			r.log.Debug("out of order REGISTER", slog.String("impi", rr.impi), slog.String("call-id", rr.callID))
			return sip.NewResponse(rr.req, 500, "Out Of Order"), nil
		}

		event, registeredAt := db.BindingRegistered, now

		if i >= 0 {
			event, registeredAt = db.BindingRefreshed, bindings[i].RegisteredAt

			switch {
			case c.expires == 0:
				deregistered = append(deregistered, bindings[i])
			case c.regID != 0 && !sameFirstHop(bindings[i].Contact.Path, rr.path):
				// TS 24.229 §5.4.1.2.2 step 6d, NOTE 6: the flow registers over a new Path from the
				// P-CSCF, a new flow in place of the old one.
				replaced = append(replaced, bindings[i])
				event, registeredAt = db.BindingRegistered, now
			}

			bindings = append(bindings[:i], bindings[i+1:]...)
		}

		if c.expires == 0 {
			continue
		}

		params := c.addr.Params.Clone()
		params.Del("expires")

		contact := db.Contact{
			URI: c.addr.URI.String(), Instance: c.instance, RegID: c.regID, Params: params.String(), Path: rr.path,
		}

		bindings = append(bindings, db.Binding{
			Contact:      contact,
			CallID:       rr.callID,
			CSeq:         int64(rr.cseq),
			ExpiresAt:    now.Add(c.expires),
			Event:        event,
			IMPU:         rr.impuKey,
			RegisteredAt: registeredAt,
		})
	}

	reg.Bindings = bindings

	// The calls of the flows replaced are those set up before the new flows are stored.
	var replacedCalls map[bindingKey]map[*proxy.Dialog]proxy.Side

	if len(replaced) > 0 {
		replacedCalls = make(map[bindingKey]map[*proxy.Dialog]proxy.Side, len(replaced))

		for _, b := range replaced {
			k := bindingKey(b.ID)
			replacedCalls[k] = r.calls.of(k)
		}
	}

	saved, err := r.cfg.DB.SaveRegistration(ctx, reg)
	if err != nil {
		r.log.Warn("failed to store the registration", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req), nil
	}

	if initial {
		r.log.Info("registered", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
	} else {
		r.log.Debug("registration refreshed", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
	}

	var removed []removal

	if len(deregistered) > 0 {
		removed = append(removed, removal{reg: set, bindings: deregistered, event: regevent.Unregistered})
	}

	if len(replaced) > 0 {
		r.log.Info("registration flow replaced", slog.String("impi", rr.impi), slog.String("impu", rr.impu))

		removed = append(removed, removal{reg: set, bindings: replaced, event: regevent.Unregistered, calls: replacedCalls})
	}

	if expired := without(set.Bindings, st.live(set.Bindings)); len(expired) > 0 {
		removed = append(removed, removal{reg: set, bindings: expired, event: regevent.Expired})
	}

	return r.ok(ctx, rr, saved, deregistered), removed
}

func (r *Registrar) replaceContacts(ctx context.Context, rr *registerRequest, st *state, keep int64) []removal {
	var removed []removal

	for _, reg := range st.regs {
		if reg.ID == keep || !st.registered(&reg) {
			continue
		}

		live := st.live(reg.Bindings)

		dropped := without(live, only(live, rr.contacts))
		if len(dropped) == 0 {
			continue
		}

		rm, deleted, err := r.removeBindings(ctx, st, reg, dropped, regevent.Unregistered)
		if err != nil {
			r.log.Warn("failed to store the registration", slog.String("impi", rr.impi), slog.Any("error", err))
		}

		removed = append(removed, rm)

		if deleted {
			r.log.Info("registration replaced by a new contact", slog.String("impi", rr.impi), slog.String("impu", reg.IMPU))
			r.deregisterAtHSS(ctx, reg.HSS, rr.impi, reg.IMPU, assignAdministrative)
		}
	}

	return removed
}

func (r *Registrar) unbind(ctx context.Context, rr *registerRequest, st *state, set *db.Registration) *sip.Response {
	removed := st.live(set.Bindings)

	if !rr.star {
		live := removed
		removed = nil

		for _, c := range rr.contacts {
			i := bindingIndex(live, c)
			if i < 0 {
				return noBinding(rr)
			}

			removed = append(removed, live[i])
		}
	}

	// RFC 3261 §10.3 step 7: a binding is removed only by a REGISTER newer than the one that made it.
	for _, b := range removed {
		if b.CallID == rr.callID && b.CSeq >= int64(rr.cseq) {
			r.log.Debug("out of order REGISTER", slog.String("impi", rr.impi), slog.String("call-id", rr.callID))
			return sip.NewResponse(rr.req, 500, "Out Of Order")
		}
	}

	rm, deleted, err := r.removeBindings(ctx, st, *set, removed, regevent.Unregistered)
	if err != nil {
		r.log.Warn("failed to store the registration", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req)
	}

	rm.byUE = true

	if deleted {
		r.log.Info("deregistered", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
		r.deregisterAtHSS(ctx, set.HSS, rr.impi, rr.impu, assignUserDeregistration)
	} else {
		r.log.Info("contacts deregistered", slog.String("impi", rr.impi), slog.String("impu", rr.impu),
			slog.Int("contacts", len(removed)))
	}

	r.releaseCalls(st, []removal{rm})
	rr.out = r.notifyChange(ctx, rr.impi, change{removed: []removal{rm}})

	return r.ok(ctx, rr, *set, removed)
}

// maxFlowsPerInstance is how many registration flows a UE, a private identity and instance ID, may
// have for a public user identity (TS 24.229 §5.4.1.2.1, §5.4.1.2.2).
const maxFlowsPerInstance = 4

// flowLimit refuses a REGISTER that adds a flow to a UE that has the most it may have already
// (TS 24.229 §5.4.1.2.1, §5.4.1.2.2).
func (r *Registrar) flowLimit(ctx context.Context, rr *registerRequest) *sip.Response {
	if !rr.flows() || rr.deregister() {
		return nil
	}

	st, err := r.load(ctx, rr.impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req)
	}

	set := st.set(rr.impuKey)
	if set == nil {
		return nil
	}

	live := st.live(set.Bindings)

	for _, c := range rr.contacts {
		if c.regID == 0 || c.expires == 0 || bindingIndex(live, c) >= 0 {
			continue
		}

		n := 0

		for _, b := range live {
			if b.Contact.Flow() && b.Contact.Instance == c.instance {
				n++
			}
		}

		if n >= maxFlowsPerInstance {
			r.log.Info("REGISTER refused: too many registration flows", slog.String("impi", rr.impi),
				slog.String("instance", c.instance), slog.Int("flows", n))

			return sip.NewResponse(rr.req, 403, "Too Many Flows")
		}
	}

	return nil
}

func firstHopLacksOutbound(rr *registerRequest) *sip.Response {
	return sip.NewResponse(rr.req, 439, "")
}

// sameFirstHop reports whether two Paths start with the same URI: that of the P-CSCF, with its flow
// token (TS 24.229 §5.4.1.2.2 NOTE 6).
func sameFirstHop(a, b string) bool {
	x, err := sip.ParseAddressList(a)
	if err != nil || len(x) == 0 {
		return false
	}

	y, err := sip.ParseAddressList(b)

	return err == nil && len(y) > 0 && x[0].URI.Equivalent(y[0].URI)
}

func hasOptionTag(h sip.Header, name, tag string) bool {
	return slices.ContainsFunc(h.Elements(name), func(e string) bool { return strings.EqualFold(strings.TrimSpace(e), tag) })
}

func noBinding(rr *registerRequest) *sip.Response {
	return sip.NewResponse(rr.req, 481, "")
}

func (r *Registrar) ok(ctx context.Context, rr *registerRequest, reg db.Registration, removed []db.Binding) *sip.Response {
	res := sip.NewResponse(rr.req, 200, "")

	for _, v := range rr.req.Header.Values("Path") {
		res.Header.Add("Path", v)
	}

	if !rr.deregister() {
		for _, c := range rr.contacts {
			if i := bindingIndex(reg.Bindings, c); i >= 0 {
				res.Header.Add("Service-Route", serviceRoute(r.cfg.Name, reg.Bindings[i].ID))
				break
			}
		}

		if uris := associatedURIs(reg.Identities); uris != "" {
			res.Header.Add("P-Associated-URI", uris)
		}
	}

	// TS 24.229 §5.4.1.2.2F h; RFC 5626 §6, in the response to a deregistration too.
	if rr.flows() && rr.outbound {
		res.Header.Add("Require", "outbound")
	}

	now := r.clock.Now()

	bound, err := r.cfg.DB.ListRegistrationsByIdentity(ctx, rr.impuKey)
	if err != nil {
		r.log.Warn("failed to list the IMPU's contacts", slog.String("impu", rr.impu), slog.Any("error", err))
	}

	for _, other := range bound {
		for _, b := range liveAt(other.Bindings, now) {
			res.Header.Add("Contact", contactValue(b.Contact, int(b.ExpiresAt.Sub(now)/time.Second)))
		}
	}

	for _, b := range removed {
		res.Header.Add("Contact", contactValue(b.Contact, 0))
	}

	return res
}

func contactValue(c db.Contact, expires int) string {
	return "<" + c.URI + ">" + c.Params + ";expires=" + strconv.Itoa(expires)
}

func (r *Registrar) cxFailure(rr *registerRequest, err error) *sip.Response {
	switch {
	case refused(err):
		r.log.Info("HSS refused the user", slog.String("impi", rr.impi), slog.String("impu", rr.impu), slog.Any("error", err))
		return sip.NewResponse(rr.req, 403, "")
	case permanent(err), errors.Is(err, errNoAKAVector):
		r.log.Warn("Cx request failed", slog.String("impi", rr.impi), slog.String("impu", rr.impu), slog.Any("error", err))
		return sip.NewResponse(rr.req, 500, "")
	default:
		r.log.Warn("Cx request failed", slog.String("impi", rr.impi), slog.String("impu", rr.impu), slog.Any("error", err))
		return retryLater(rr.req)
	}
}

func retryLater(req *sip.Request) *sip.Response {
	res := sip.NewResponse(req, 500, "")
	res.Header.Add("Retry-After", strconv.Itoa(retryAfter))

	return res
}
