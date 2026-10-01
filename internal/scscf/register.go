package scscf

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

const autsLen = 14

// registerRequest is what the registrar reads from a REGISTER.
type registerRequest struct {
	req   *sip.Request
	impu  string
	impi  string
	creds *credentials
	// contact is the Contact address. It is the zero Address for a REGISTER
	// without Contact or with Contact: *.
	contact    sip.Address
	hasContact bool
	// expires is the granted expiry, 0 for a deregistration.
	expires   time.Duration
	callID    string
	cseq      uint32
	path      string
	ueAddress netip.Addr
}

func (rr *registerRequest) deregister() bool {
	return rr.expires == 0
}

func (r *Registrar) register(ctx context.Context, req *sip.Request) *sip.Response {
	rr, res := r.parse(req)
	if res != nil {
		return res
	}

	if !rr.deregister() && rr.expires < r.cfg.MinExpires {
		res := sip.NewResponse(req, 423, "")
		res.Header.Add("Min-Expires", strconv.Itoa(int(r.cfg.MinExpires/time.Second)))

		return res
	}

	if !r.lock(rr.impi) {
		r.log.Debug("REGISTER while another one is being handled", slog.String("impi", rr.impi))
		return retryLater(req)
	}

	defer r.unlock(rr.impi)

	if !rr.creds.protected() {
		return r.challenge(ctx, rr, nil)
	}

	if ch := r.pendingChallenge(rr.impi); ch != nil {
		return r.authenticate(ctx, rr, ch)
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
		req:    req,
		impu:   normalizeURI(to.URI),
		callID: req.Header.CallID(),
		cseq:   cseq.Seq,
		path:   strings.Join(req.Header.Values("Path"), ", "),
	}

	switch {
	case len(contacts) > 1:
		return nil, sip.NewResponse(req, 400, "One Contact Only")
	case len(contacts) == 1 && contacts[0].Star:
		// RFC 3261 §10.3 step 6.
		if !hasExpires || expiresHeader != 0 {
			return nil, sip.NewResponse(req, 400, "Contact * Without Expires 0")
		}
	case len(contacts) == 1:
		rr.contact, rr.hasContact = contacts[0], true

		if v, ok := rr.contact.Params.Get("expires"); ok {
			if n, err := strconv.ParseUint(v, 10, 32); err == nil {
				expires = uint32(n)
			}
		}
	case expires != 0:
		return nil, sip.NewResponse(req, 400, "Missing Contact")
	}

	rr.expires = min(time.Duration(expires)*time.Second, r.cfg.MaxExpires)

	if rr.creds, err = parseAuthorization(req.Header, r.cfg.HomeDomain); err != nil {
		return nil, sip.NewResponse(req, 400, "Bad Authorization")
	}

	if rr.creds != nil {
		rr.impi = rr.creds.username
	} else {
		rr.impi = privateIdentity(to.URI)
	}

	rr.ueAddress = ueAddress(req)

	return rr, nil
}

// ueAddress is the address the P-CSCF saw the UE at: the received parameter
// of the bottom Via, else its host, else the source of the request.
func ueAddress(req *sip.Request) netip.Addr {
	if vias, err := req.Header.Vias(); err == nil {
		bottom := vias[len(vias)-1]

		if a, err := netip.ParseAddr(strings.Trim(bottom.Received(), "[]")); err == nil {
			return a.Unmap()
		}

		if a, ok := bottom.Addr(); ok {
			return a.Unmap()
		}
	}

	return req.Flow.Remote.Addr().Unmap()
}

// challenge fetches a vector and sends the 401 (TS 24.229 §5.4.1.2.1).
func (r *Registrar) challenge(ctx context.Context, rr *registerRequest, resync *cx.Resync) *sip.Response {
	v, err := r.multimediaAuth(ctx, rr.impi, rr.impu, resync)
	if err != nil {
		return r.cxFailure(rr, err)
	}

	nonce := akaNonce(v)

	r.putChallenge(rr.impi, &challenge{callID: rr.callID, impu: rr.impu, nonce: nonce, vector: v})

	r.log.Debug("challenged REGISTER", slog.String("impi", rr.impi), slog.String("impu", rr.impu),
		slog.Bool("resync", resync != nil))

	res := sip.NewResponse(rr.req, 401, "")
	res.Header.Add("WWW-Authenticate", wwwAuthenticate(r.cfg.HomeDomain, nonce, v))

	return res
}

// authenticate checks the answer to a challenge (TS 24.229 §5.4.1.2.2). The
// challenge is keyed by IMPI, so the username is the challenged IMPI.
func (r *Registrar) authenticate(ctx context.Context, rr *registerRequest, ch *challenge) *sip.Response {
	c := rr.creds

	if rr.callID != ch.callID || rr.impu != ch.impu || !strings.EqualFold(c.algorithm, algorithmAKAv1) {
		return r.authFailed(ctx, rr, ch)
	}

	if c.auts != "" {
		auts, err := base64.StdEncoding.DecodeString(c.auts)
		if err != nil || len(auts) != autsLen {
			return r.authFailed(ctx, rr, ch)
		}

		// TS 24.229 §5.4.1.2.3: resynchronise with the RAND of the challenge.
		r.dropChallenge(rr.impi, ch)

		return r.challenge(ctx, rr, &cx.Resync{RAND: ch.vector.rand, AUTS: auts})
	}

	if !verify(c, rr.req.Method, ch.nonce, ch.vector.xres) {
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

// authenticated registers, re-registers or deregisters an authenticated UE.
func (r *Registrar) authenticated(ctx context.Context, rr *registerRequest) *sip.Response {
	existing, found, err := r.current(ctx, rr.impi)
	if err != nil {
		r.log.Warn("failed to read the registration", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req)
	}

	if rr.deregister() {
		return r.deregister(ctx, rr, found)
	}

	t := assignRegistration
	if found {
		t = assignReRegistration
	}

	data, err := r.serverAssignment(ctx, rr.impi, []string{rr.impu}, t, found)
	if err != nil {
		return r.cxFailure(rr, err)
	}

	var set []db.PublicIdentity

	switch {
	case len(data) > 0:
		sub, err := cx.ParseUserData(data)
		if err != nil {
			r.log.Warn("invalid User-Data from the HSS", slog.String("impi", rr.impi), slog.Any("error", err))
			return sip.NewResponse(rr.req, 500, "")
		}

		var ok bool
		if set, ok = implicitSet(sub, rr.impu); !ok {
			r.log.Info("IMPU not in its service profile", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
			return sip.NewResponse(rr.req, 403, "")
		}
	case found:
		set = existing.Identities
	default:
		r.log.Warn("SAA without User-Data", slog.String("impi", rr.impi))
		return sip.NewResponse(rr.req, 500, "")
	}

	if id, ok := lookupIdentity(set, rr.impu); !ok || id.Barred {
		r.log.Info("REGISTER of a barred or unknown IMPU", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
		return sip.NewResponse(rr.req, 403, "")
	}

	if found && sameContact(existing.Contact, rr.contact) && slices.Equal(existing.Identities, set) {
		if err := r.refreshRegistration(ctx, rr, existing); err != nil {
			r.log.Warn("failed to store the registration", slog.String("impi", rr.impi), slog.Any("error", err))
			return retryLater(rr.req)
		}

		return r.ok(rr, existing.ID, set)
	}

	// A new contact replaces the IMPI's registration (§5.4.1.2.2 step 4A).
	instance, _ := rr.contact.Params.Get("+sip.instance")
	now := r.clock.Now()

	id, err := r.cfg.DB.PutRegistration(ctx, db.Registration{
		IMPI:         rr.impi,
		Contact:      sip.Address{URI: rr.contact.URI}.String(),
		InstanceID:   sip.Unquote(instance),
		CallID:       rr.callID,
		CSeq:         int64(rr.cseq),
		UEAddress:    rr.ueAddress,
		Path:         rr.path,
		Identities:   set,
		RegisteredAt: now,
		ExpiresAt:    now.Add(rr.expires),
	})
	if err != nil {
		r.log.Warn("failed to store the registration", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req)
	}

	r.log.Info("registered", slog.String("impi", rr.impi), slog.String("impu", rr.impu),
		slog.String("contact", rr.contact.URI.String()), slog.Duration("expires", rr.expires))

	return r.ok(rr, id, set)
}

// refresh handles a protected REGISTER without a pending challenge: a
// registered contact refreshes or ends its registration (§5.4.1.2.2 step 2).
func (r *Registrar) refresh(ctx context.Context, rr *registerRequest) *sip.Response {
	existing, found, err := r.current(ctx, rr.impi)
	if err != nil {
		r.log.Warn("failed to read the registration", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req)
	}

	if _, ok := lookupIdentity(existing.Identities, rr.impu); !found || !ok ||
		rr.hasContact && !sameContact(existing.Contact, rr.contact) {
		r.log.Info("protected REGISTER from an unregistered contact", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
		return sip.NewResponse(rr.req, 403, "")
	}

	if rr.deregister() {
		return r.deregister(ctx, rr, true)
	}

	if err := r.refreshRegistration(ctx, rr, existing); err != nil {
		r.log.Warn("failed to store the registration", slog.String("impi", rr.impi), slog.Any("error", err))
		return retryLater(rr.req)
	}

	return r.ok(rr, existing.ID, existing.Identities)
}

func (r *Registrar) refreshRegistration(ctx context.Context, rr *registerRequest, existing db.Registration) error {
	err := r.cfg.DB.RefreshRegistration(ctx, rr.impi, db.RegistrationRefresh{
		CallID:    rr.callID,
		CSeq:      int64(rr.cseq),
		Path:      rr.path,
		IPsec:     existing.IPsec,
		ExpiresAt: r.clock.Now().Add(rr.expires),
	})
	if err != nil {
		return err
	}

	r.log.Debug("registration refreshed", slog.String("impi", rr.impi), slog.Duration("expires", rr.expires))

	return nil
}

// deregister ends a registration (§5.4.1.4). The HSS not answering doesn't
// keep the UE registered.
func (r *Registrar) deregister(ctx context.Context, rr *registerRequest, found bool) *sip.Response {
	if found {
		if _, err := r.serverAssignment(ctx, rr.impi, []string{rr.impu}, assignUserDeregistration, false); err != nil {
			r.log.Warn("failed to tell the HSS of a deregistration", slog.String("impi", rr.impi), slog.Any("error", err))
		}

		if err := r.cfg.DB.DeleteRegistration(ctx, rr.impi); err != nil && !errors.Is(err, db.ErrNotFound) {
			r.log.Warn("failed to delete the registration", slog.String("impi", rr.impi), slog.Any("error", err))
			return retryLater(rr.req)
		}

		r.log.Info("deregistered", slog.String("impi", rr.impi), slog.String("impu", rr.impu))
	}

	return sip.NewResponse(rr.req, 200, "")
}

// current returns the IMPI's registration. found is false when there is none
// or it has expired.
func (r *Registrar) current(ctx context.Context, impi string) (db.Registration, bool, error) {
	reg, err := r.cfg.DB.GetRegistration(ctx, impi)
	if errors.Is(err, db.ErrNotFound) {
		return db.Registration{}, false, nil
	}

	if err != nil {
		return db.Registration{}, false, err
	}

	return reg, reg.ExpiresAt.After(r.clock.Now()), nil
}

// ok is the 200 to a registration (§5.4.1.2.2F).
func (r *Registrar) ok(rr *registerRequest, id int64, set []db.PublicIdentity) *sip.Response {
	res := sip.NewResponse(rr.req, 200, "")

	for _, v := range rr.req.Header.Values("Path") {
		res.Header.Add("Path", v)
	}

	res.Header.Add("Service-Route", serviceRoute(r.cfg.HomeDomain, r.cfg.Port, id))

	if uris := associatedURIs(set); uris != "" {
		res.Header.Add("P-Associated-URI", uris)
	}

	contact := rr.contact.Clone()
	contact.Params.Set("expires", strconv.Itoa(int(rr.expires/time.Second)))
	res.Header.Add("Contact", contact.String())

	if icid := icidValue(rr.req.Header.Get("P-Charging-Vector")); icid != "" {
		res.Header.Add("P-Charging-Vector", "icid-value="+icid)
	}

	return res
}

// cxFailure maps a failed MAR or SAR to the response (TS 24.229 §5.4.1.2.1).
func (r *Registrar) cxFailure(rr *registerRequest, err error) *sip.Response {
	r.log.Warn("Cx request failed", slog.String("impi", rr.impi), slog.String("impu", rr.impu), slog.Any("error", err))

	switch {
	case refused(err):
		return sip.NewResponse(rr.req, 403, "")
	case errors.Is(err, errNoAKAVector):
		return sip.NewResponse(rr.req, 500, "")
	default:
		return retryLater(rr.req)
	}
}

func retryLater(req *sip.Request) *sip.Response {
	res := sip.NewResponse(req, 500, "")
	res.Header.Add("Retry-After", strconv.Itoa(retryAfter))

	return res
}

func sameContact(stored string, c sip.Address) bool {
	a, err := sip.ParseAddress(stored)
	return err == nil && a.URI.String() == c.URI.String()
}

// icidValue returns the icid-value parameter of a P-Charging-Vector, as
// received (RFC 7315 §4.6).
func icidValue(pcv string) string {
	for p := range strings.SplitSeq(pcv, ";") {
		name, value, ok := strings.Cut(p, "=")
		if ok && strings.EqualFold(strings.TrimSpace(name), "icid-value") {
			return strings.TrimSpace(value)
		}
	}

	return ""
}
