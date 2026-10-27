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
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/reginfo"
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

	// hss is the HSS that authenticated the UE in this request, if it did, and initial tells that the challenge it
	// answered was to an unprotected REGISTER, the initial registration procedure (TS 24.229 §5.4.1.1 item 1).
	hss     db.HSS
	initial bool

	// event is the registration the REGISTER made, changed or ended, for the ASs (TS 24.229 §5.4.1.7).
	event *registration
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

// authKey is what the REGISTER authenticates: its flow of the public user identity, or the private identity's
// registration.
func (rr *registerRequest) authKey() authKey {
	for _, c := range rr.contacts {
		if c.regID != 0 {
			return authKey{impi: rr.impi, impu: rr.impuKey, instance: c.instance, regID: c.regID}
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

// register handles a REGISTER: the third-party REGISTERs of the registration event it makes, if any, are held until
// responded closes.
func (r *Registrar) register(ctx context.Context, req *sip.Request, responded <-chan struct{}) (*sip.Response, []*outgoing) {
	rr, res := r.parse(req)
	if res != nil {
		r.cfg.RegistrationAttempts.Answered(req, res, false)
		return res, nil
	}

	res = r.handleRegister(ctx, rr, responded)
	r.cfg.RegistrationAttempts.Answered(req, res, rr.authFailed)

	return res, rr.out
}

func (r *Registrar) handleRegister(ctx context.Context, rr *registerRequest, responded <-chan struct{}) *sip.Response {
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

	res := r.handleLocked(ctx, rr)

	// Queued with the private identity locked, the third-party REGISTERs follow the order of the registration events.
	if res.StatusCode == 200 && rr.event != nil {
		rr.event.res = res
		r.notifyASs(*rr.event, responded)
	}

	return res
}

func (r *Registrar) handleLocked(ctx context.Context, rr *registerRequest) *sip.Response {
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
		outbound: req.Header.HasOptionTag("Supported", sip.OptionTagOutbound),
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
		initial: !rr.protected,
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

	rr.hss, rr.initial = ch.hss, ch.initial

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

	// TS 23.228 §5.2.2.4 step 6: a re-registration notifies the HSS too, which may send a profile that changed since
	// (TS 29.228 §6.6).
	return r.assign(ctx, rr, st, set, false)
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

	switch {
	case err == nil:
	// A registered set keeps the profile it holds while its HSS cannot be reached.
	case registered && !refused(err) && !permanent(err):
		r.log.Warn("HSS unreachable on re-registration: the profile held is kept", slog.String("impi", rr.impi),
			slog.String("impu", rr.impu), slog.Any("error", err))

		saa, from = cx.ServerAssignment{}, set.HSS
	default:
		return r.cxFailure(rr, err)
	}

	reg := db.Registration{IMPI: rr.impi}
	if set != nil {
		reg = *set
	}

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
		ch.removed = append(ch.removed, removal{reg: reg, bindings: expired, event: reginfo.Expired})
	}

	reg.Bindings = st.live(reg.Bindings)

	var sub *cx.IMSSubscription

	switch {
	case len(saa.UserData) > 0:
		s, err := cx.ParseUserData(saa.UserData)
		if err != nil {
			r.log.Warn("invalid User-Data from the HSS", slog.String("impi", rr.impi), slog.Any("error", err))

			undo = assignTooMuchData

			return sip.NewResponse(rr.req, 480, "")
		}

		sub = &s
		reg.Identities = implicitSet(s)
	case !registered:
		r.log.Warn("SAA without User-Data", slog.String("impi", rr.impi))
		return sip.NewResponse(rr.req, 500, "")
	}

	if !holds(reg.Identities, rr.impuKey) || !hasUnbarred(reg.Identities) {
		r.log.Debug("REGISTER of an IMPU outside its set, or of a set that is all barred",
			slog.String("impi", rr.impi), slog.String("impu", rr.impu))

		return sip.NewResponse(rr.req, 403, "")
	}

	// The set is stored with its registration, holding the lock of the profiles from its reading on.
	if err := r.lockProfiles(ctx); err != nil {
		return retryLater(rr.req)
	}

	pc, err := r.assignedSet(ctx, reg, sub, saa.UserData, from)
	if err != nil {
		r.unlockProfiles()
		r.log.Warn("failed to read the implicit set", slog.String("impi", rr.impi), slog.Any("error", err))

		return retryLater(rr.req)
	}

	reg.Identities, reg.UserData, reg.HSS = pc.set.Identities, pc.set.UserData, pc.set.HSS
	stale := st.overlapping(reg)

	// The HSS's answer wins over a registered set that still holds identities of it (TS 29.228 §6.5.2.1).
	moves, err := r.moves(ctx, pc.set, stale)
	if err != nil {
		r.unlockProfiles()
		r.log.Warn("failed to read the implicit sets", slog.String("impi", rr.impi), slog.Any("error", err))

		return retryLater(rr.req)
	}

	losing := make([]db.ImplicitSet, 0, len(moves))
	for _, m := range moves {
		losing = append(losing, m.pc.set)
	}

	if replace {
		kept := only(reg.Bindings, rr.contacts)

		if dropped := without(reg.Bindings, kept); len(dropped) > 0 {
			ch.removed = append(ch.removed, removal{reg: reg, bindings: dropped, event: reginfo.Unregistered})
		}

		reg.Bindings = kept
	}

	res, removed := r.bind(ctx, rr, st, reg, !registered, &pc.set, stale, losing)

	r.unlockProfiles()

	if res.StatusCode != 200 {
		return res
	}

	ch.removed = append(ch.removed, removed...)

	// The other private identities that registered the set learn of the change now; this one with its registration.
	r.spreadProfile(r.ctx, pc, rr.impi, false)
	r.applyMoves(r.ctx, rr.impi, moves)

	// TS 24.229 §5.4.1.8: a profile the HSS changed with the re-registration changes the registration.
	if registered && pc.identitiesChanged() {
		r.releaseRemoved(st, pc, st.live(reg.Bindings))

		notice := r.profileNotice(ctx, rr.impi, pc)
		ch.rejected, ch.created, ch.ended = notice.rejected, notice.created, notice.ended
	}

	if replace {
		ch.removed = append(ch.removed, r.replaceContacts(ctx, rr, st, reg.ID)...)
	}

	r.releaseCalls(st, ch.removed)
	rr.out = r.notifyChange(ctx, rr.impi, ch)

	return res
}

// assignedSet is the set a server assignment answered for, to store with its registration: with the profile it
// downloaded, if any, from the HSS that answered, and taken over from any set kept unregistered (TS 29.228 §6.1.2.1);
// and how that changed it. A registration not yet of a set is of the set with the default identity of the profile,
// which the HSS does not change while the set is registered (TS 29.228 §6.5.2.1), or else of a set kept unregistered
// that holds an identity of it, or else of a new set. The caller holds the lock of the profiles.
func (r *Registrar) assignedSet(ctx context.Context, reg db.Registration, sub *cx.IMSSubscription, data []byte,
	from db.HSS,
) (profileChange, error) {
	var cur db.ImplicitSet

	switch {
	case reg.SetID != 0:
		s, err := r.cfg.DB.GetSet(ctx, reg.SetID)
		if err != nil {
			return profileChange{}, err
		}

		cur = s
	case sub != nil:
		sets, err := r.cfg.DB.FindSets(ctx, identityKeysOf(implicitSet(*sub)))
		if err != nil {
			return profileChange{}, err
		}

		def := defaultIdentity(implicitSet(*sub)).Key

		i := slices.IndexFunc(sets, func(s db.ImplicitSet) bool { return defaultIdentity(s.Identities).Key == def })
		if i < 0 {
			i = slices.IndexFunc(sets, func(s db.ImplicitSet) bool { return s.Unregistered != nil })
		}

		if i >= 0 {
			cur = sets[i]
		}
	}

	next := cur
	next.HSS, next.Unregistered = from, nil

	if sub == nil {
		return profileChange{set: next}, nil
	}

	next.Identities, next.UserData = implicitSet(*sub), data

	return diffProfile(cur.Identities, cur.UserData, next, *sub), nil
}

// bind stores the bindings of the REGISTER to the registration, and the registration with the set it registers, in
// place of the stale ones, expired, that held its identities, and after the sets losing identities to it
// (db.SaveAssignment): set becomes the set as stored.
func (r *Registrar) bind(ctx context.Context, rr *registerRequest, st *state, reg db.Registration,
	initial bool, set *db.ImplicitSet, stale []db.Registration, losing []db.ImplicitSet,
) (*sip.Response, []removal) {
	now := r.clock.Now()
	before := reg
	reg.IMPU = rr.impu
	bindings := st.live(reg.Bindings)

	var deregistered, replaced []db.Binding

	regType := cx.RegistrationTypeReRegistration

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

		// TS 24.229 §5.4.1.1 item 1, §3.1: a REGISTER that went through the initial registration procedure, as one
		// from a UE that restarted, or that adds a contact, is an initial registration for the ASs.
		if event == db.BindingRegistered || rr.initial {
			regType = cx.RegistrationTypeInitial
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

	ids := make([]int64, 0, len(stale))
	for _, old := range stale {
		ids = append(ids, old.ID)
	}

	stored, saved, err := r.cfg.DB.SaveAssignment(ctx, *set, reg, ids, losing)
	if err != nil {
		r.log.Warn("failed to store the registration", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req), nil
	}

	*set = stored

	rr.event = &registration{reg: saved, impu: rr.impu, regType: regType, req: rr.req}

	for _, c := range rr.contacts {
		if i := bindingIndex(saved.Bindings, c); i >= 0 && c.expires != 0 {
			rr.event.bindings = append(rr.event.bindings, saved.Bindings[i].ID)
		}
	}

	if initial {
		r.log.Info("registered", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
	} else {
		r.log.Debug("registration refreshed", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
	}

	var removed []removal

	if len(deregistered) > 0 {
		removed = append(removed, removal{reg: before, bindings: deregistered, event: reginfo.Unregistered})
	}

	if len(replaced) > 0 {
		r.log.Info("registration flow replaced", slog.String("impi", rr.impi), slog.String("impu", rr.impu))

		removed = append(removed, removal{reg: before, bindings: replaced, event: reginfo.Unregistered, calls: replacedCalls})
	}

	if expired := without(before.Bindings, st.live(before.Bindings)); len(expired) > 0 {
		removed = append(removed, removal{reg: before, bindings: expired, event: reginfo.Expired})
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

		rm, deleted, err := r.removeBindings(ctx, st, reg, dropped, reginfo.Unregistered)
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

	rm, deleted, err := r.removeBindings(ctx, st, *set, removed, reginfo.Unregistered)
	if err != nil {
		r.log.Warn("failed to store the registration", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req)
	}

	rm.byUE = true

	rr.event = &registration{reg: *set, impu: rr.impu, regType: cx.RegistrationTypeDeregistration, req: rr.req, ended: deleted}
	for _, b := range removed {
		rr.event.bindings = append(rr.event.bindings, b.ID)
	}

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
			expires := int(b.ExpiresAt.Sub(now) / time.Second)

			// TS 24.229 §5.4.1.7 e: the third-party REGISTERs carry the expiry of the 200.
			if ev := rr.event; ev != nil && len(ev.bindings) > 0 && b.ID == ev.bindings[0] {
				ev.expires = expires
			}

			res.Header.Add("Contact", contactValue(b.Contact, expires))
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
