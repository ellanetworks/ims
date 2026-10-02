package scscf

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
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
}

type contactRequest struct {
	addr    sip.Address
	expires time.Duration
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
		return res, nil
	}

	res = r.handleRegister(ctx, rr)

	if res.StatusCode == 200 {
		r.scheduleSweep(ctx)
	}

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

	locked, err := r.lockForRegister(ctx, rr.impi)
	if err != nil {
		return retryLater(req)
	}

	if !locked {
		r.log.Debug("REGISTER while another one is being handled", slog.String("impi", rr.impi))
		return retryLater(req)
	}

	defer r.unlock(rr.impi)

	if ch := r.pendingChallenge(rr.impi); rr.creds.answers(ch) {
		return r.answer(ctx, rr, ch)
	}

	if !rr.protected {
		return r.challenge(ctx, rr, nil, 0)
	}

	return r.refresh(ctx, rr)
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
		req:     req,
		impu:    receivedIdentity(to.URI),
		impuKey: identityKey(to.URI),
		callID:  req.Header.CallID(),
		cseq:    cseq.Seq,
		path:    strings.Join(req.Header.Values("Path"), ", "),
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
		for _, c := range selectContacts(contacts) {
			granted := expires

			if v, ok := c.Params.Get("expires"); ok {
				if n, err := strconv.ParseUint(v, 10, 32); err == nil {
					granted = uint32(n)
				}
			}

			rr.contacts = append(rr.contacts, contactRequest{
				addr:    c,
				expires: min(time.Duration(granted)*time.Second, r.cfg.MaxExpires),
			})
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
		if q(c) > q(unique[best]) {
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

func q(c sip.Address) float64 {
	v, ok := c.Params.Get("q")
	if !ok {
		return 1
	}

	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}

	return f
}

func (r *Registrar) challenge(ctx context.Context, rr *registerRequest, resync *cx.Resync, resyncs int) *sip.Response {
	v, err := r.multimediaAuth(ctx, rr.impi, rr.impu, resync)
	if err != nil {
		return r.cxFailure(rr, err)
	}

	nonce := akaNonce(v)

	r.putChallenge(rr.impi, &challenge{
		callID:  rr.callID,
		impu:    rr.impu,
		impuKey: rr.impuKey,
		nonce:   nonce,
		vector:  v,
		resyncs: resyncs,
	})

	r.log.Debug("challenged REGISTER", slog.String("impi", rr.impi), slog.String("impu", rr.impu),
		slog.Bool("resync", resync != nil))

	res := sip.NewResponse(rr.req, 401, "")
	res.Header.Add("WWW-Authenticate", wwwAuthenticate(r.cfg.HomeDomain, nonce, v))

	return res
}

func (r *Registrar) answer(ctx context.Context, rr *registerRequest, ch *challenge) *sip.Response {
	c := rr.creds

	switch {
	case c.auts == "" && c.response != "" && !rr.protected:
		return r.challenge(ctx, rr, nil, 0)
	case rr.callID != ch.callID || rr.impuKey != ch.impuKey || !strings.EqualFold(c.algorithm, algorithmAKAv1):
		return r.authFailed(ctx, rr, ch)
	case c.auts != "":
		auts, err := base64.StdEncoding.DecodeString(c.auts)
		if err != nil || len(auts) != autsLen || ch.resyncs >= maxResyncs {
			return r.authFailed(ctx, rr, ch)
		}

		r.dropChallenge(rr.impi, ch)

		return r.challenge(ctx, rr, &cx.Resync{RAND: ch.vector.rand, AUTS: auts}, ch.resyncs+1)
	case c.response == "":
		return r.authFailed(ctx, rr, ch)
	case !verify(c, rr.req.Method, ch.nonce, ch.vector.xres):
		return r.authFailed(ctx, rr, ch)
	}

	r.dropChallenge(rr.impi, ch)

	return r.authenticated(ctx, rr)
}

func (r *Registrar) authFailed(ctx context.Context, rr *registerRequest, ch *challenge) *sip.Response {
	r.dropChallenge(rr.impi, ch)

	r.log.Info("REGISTER failed authentication", slog.String("impi", rr.impi), slog.String("impu", rr.impu))

	if _, err := r.serverAssignment(ctx, rr.impi, []string{ch.impu}, assignAuthenticationFailure, false); err != nil {
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

		return r.unbind(ctx, rr, set)
	}

	return r.assign(ctx, rr, st, set, true)
}

func (r *Registrar) refresh(ctx context.Context, rr *registerRequest) *sip.Response {
	st, err := r.load(ctx, rr.impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req)
	}

	if !st.any() {
		r.log.Info("protected REGISTER from an unregistered user", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
		return sip.NewResponse(rr.req, 500, "")
	}

	set := st.set(rr.impuKey)

	if rr.deregister() {
		if !st.registered(set) {
			return noBinding(rr)
		}

		return r.unbind(ctx, rr, set)
	}

	for _, c := range rr.contacts {
		if _, ok := st.contact(c.addr.URI); c.expires != 0 && !ok {
			r.log.Info("protected REGISTER from an unregistered contact", slog.String("impi", rr.impi),
				slog.String("contact", c.addr.URI.String()))

			return sip.NewResponse(rr.req, 403, "")
		}
	}

	if !st.registered(set) {
		return r.assign(ctx, rr, st, set, false)
	}

	res := r.bind(ctx, rr, st, *set, false)
	if res.StatusCode == 200 {
		rr.out = r.notifyChange(ctx, rr.impi, change{})
	}

	return res
}

func (r *Registrar) assign(ctx context.Context, rr *registerRequest, st *state, set *db.Registration, replace bool) *sip.Response {
	registered := st.registered(set)

	t := assignRegistration
	if registered {
		t = assignReRegistration
	}

	saa, err := r.serverAssignment(ctx, rr.impi, []string{rr.impu}, t, registered && len(set.UserData) > 0)
	if err != nil {
		return r.cxFailure(rr, err)
	}

	reg := db.Registration{IMPI: rr.impi}
	if set != nil {
		reg = *set
	}

	if !registered {
		reg.Bindings = nil
	}

	switch {
	case len(saa.UserData) > 0:
		sub, err := cx.ParseUserData(saa.UserData)
		if err != nil {
			r.log.Warn("invalid User-Data from the HSS", slog.String("impi", rr.impi), slog.Any("error", err))
			return sip.NewResponse(rr.req, 500, "")
		}

		reg.Identities = implicitSet(sub)
		reg.UserData = saa.UserData
	case !registered:
		r.log.Warn("SAA without User-Data", slog.String("impi", rr.impi))
		return sip.NewResponse(rr.req, 500, "")
	}

	if !holds(reg.Identities, rr.impuKey) || !hasUnbarred(reg.Identities) {
		r.log.Info("REGISTER of an IMPU outside its set, or of a set that is all barred",
			slog.String("impi", rr.impi), slog.String("impu", rr.impu))

		return sip.NewResponse(rr.req, 403, "")
	}

	for _, old := range st.overlapping(reg) {
		if err := r.cfg.DB.DeleteRegistration(ctx, old.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
			r.log.Warn("failed to delete an expired registration", slog.String("impi", rr.impi), slog.Any("error", err))
			return retryLater(rr.req)
		}
	}

	var ch change

	if replace {
		kept := only(reg.Bindings, rr.contacts)

		if dropped := without(st.live(reg.Bindings), kept); len(dropped) > 0 {
			ch.removed = append(ch.removed, removal{reg: reg, bindings: dropped, event: regevent.Unregistered})
		}

		reg.Bindings = kept
	}

	res := r.bind(ctx, rr, st, reg, !registered)
	if res.StatusCode != 200 {
		return res
	}

	if replace {
		ch.removed = append(ch.removed, r.replaceContacts(ctx, rr, st, reg.ID)...)
	}

	rr.out = r.notifyChange(ctx, rr.impi, ch)

	return res
}

func (r *Registrar) bind(ctx context.Context, rr *registerRequest, st *state, reg db.Registration, initial bool) *sip.Response {
	now := r.clock.Now()
	reg.IMPU = rr.impu
	bindings := st.live(reg.Bindings)

	for _, c := range rr.contacts {
		i := bindingIndex(bindings, c.addr.URI)

		if i >= 0 && bindings[i].CallID == rr.callID && bindings[i].CSeq >= int64(rr.cseq) {
			r.log.Info("out of order REGISTER", slog.String("impi", rr.impi), slog.String("call-id", rr.callID))
			return sip.NewResponse(rr.req, 500, "Out Of Order")
		}

		event := db.BindingRegistered

		if i >= 0 {
			event = db.BindingRefreshed

			bindings = append(bindings[:i], bindings[i+1:]...)
		}

		if c.expires == 0 {
			continue
		}

		contact, ok := st.contact(c.addr.URI)
		if !ok {
			contact = db.Contact{URI: c.addr.URI.String()}
		}

		params := c.addr.Params.Clone()
		params.Del("expires")

		contact.Params = params.String()
		contact.Path = rr.path

		bindings = append(bindings, db.Binding{
			Contact:   contact,
			CallID:    rr.callID,
			CSeq:      int64(rr.cseq),
			ExpiresAt: now.Add(c.expires),
			Event:     event,
		})
	}

	reg.Bindings = bindings

	saved, err := r.cfg.DB.SaveRegistration(ctx, reg)
	if err != nil {
		r.log.Warn("failed to store the registration", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req)
	}

	if initial {
		r.log.Info("registered", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
	} else {
		r.log.Debug("registration refreshed", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
	}

	return r.ok(ctx, rr, saved, nil)
}

func (r *Registrar) replaceContacts(ctx context.Context, rr *registerRequest, st *state, keep int64) []removal {
	var removed []removal

	for _, reg := range st.regs {
		if reg.ID == keep || !st.registered(&reg) {
			continue
		}

		live := st.live(reg.Bindings)

		reg.Bindings = only(live, rr.contacts)
		if dropped := without(live, reg.Bindings); len(dropped) > 0 {
			removed = append(removed, removal{reg: reg, bindings: dropped, event: regevent.Unregistered})
		}

		if len(reg.Bindings) > 0 {
			if _, err := r.cfg.DB.SaveRegistration(ctx, reg); err != nil {
				r.log.Warn("failed to store the registration", slog.String("impi", rr.impi), slog.Any("error", err))
			}

			continue
		}

		if _, err := r.serverAssignment(ctx, rr.impi, []string{reg.IMPU}, assignAdministrative, false); err != nil {
			r.log.Warn("failed to tell the HSS of a replaced registration", slog.String("impi", rr.impi), slog.Any("error", err))
		}

		if err := r.cfg.DB.DeleteRegistration(ctx, reg.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
			r.log.Warn("failed to delete the registration", slog.String("impi", rr.impi), slog.Any("error", err))
		}

		r.log.Info("registration replaced by a new contact", slog.String("impi", rr.impi), slog.String("impu", reg.IMPU))
	}

	return removed
}

func (r *Registrar) unbind(ctx context.Context, rr *registerRequest, set *db.Registration) *sip.Response {
	reg := *set
	bindings := liveAt(reg.Bindings, r.clock.Now())

	var removed []db.Binding

	if rr.star {
		removed, bindings = bindings, nil
	} else {
		for _, c := range rr.contacts {
			i := bindingIndex(bindings, c.addr.URI)
			if i < 0 {
				return noBinding(rr)
			}

			removed = append(removed, bindings[i])
			bindings = append(bindings[:i], bindings[i+1:]...)
		}
	}

	ch := change{removed: []removal{{reg: *set, bindings: removed, event: regevent.Unregistered, byUE: true}}}
	reg.Bindings = bindings

	if len(bindings) > 0 {
		if _, err := r.cfg.DB.SaveRegistration(ctx, reg); err != nil {
			r.log.Warn("failed to store the registration", slog.String("impi", rr.impi), slog.Any("error", err))
			return retryLater(rr.req)
		}

		r.log.Info("contacts deregistered", slog.String("impi", rr.impi), slog.String("impu", rr.impu),
			slog.Int("contacts", len(removed)))

		rr.out = r.notifyChange(ctx, rr.impi, ch)

		return r.ok(ctx, rr, reg, removed)
	}

	if _, err := r.serverAssignment(ctx, rr.impi, []string{rr.impu}, assignUserDeregistration, false); err != nil {
		r.log.Warn("failed to tell the HSS of a deregistration", slog.String("impi", rr.impi), slog.Any("error", err))
	}

	if err := r.cfg.DB.DeleteRegistration(ctx, reg.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
		r.log.Warn("failed to delete the registration", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req)
	}

	r.log.Info("deregistered", slog.String("impi", rr.impi), slog.String("impu", rr.impu))

	rr.out = r.notifyChange(ctx, rr.impi, ch)

	return r.ok(ctx, rr, reg, removed)
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
			if i := bindingIndex(reg.Bindings, c.addr.URI); i >= 0 {
				res.Header.Add("Service-Route", serviceRoute(r.cfg.Name, reg.Bindings[i].Contact.ID))
				break
			}
		}

		if uris := associatedURIs(reg.Identities); uris != "" {
			res.Header.Add("P-Associated-URI", uris)
		}
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
