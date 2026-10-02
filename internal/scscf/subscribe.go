package scscf

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
)

const (
	defaultSubscriptionExpires = 3761 * time.Second
	maxSubscriptionExpires     = 1000000 * time.Second
)

func IsRegEvent(req *sip.Request) bool {
	event, _, err := sip.ParseTokenParams(req.Header.Get("Event"))

	return err == nil && strings.EqualFold(event, "reg")
}

func (r *Registrar) Subscribe(ctx context.Context, req *sip.Request, routes []sip.URI, respond func(*sip.Response)) {
	if !r.start() {
		respond(retryLater(req))
		return
	}

	defer r.wg.Done()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()

	to, err := req.Header.To()
	if err != nil {
		respond(sip.NewResponse(req, 400, "Bad To"))
		return
	}

	var (
		res *sip.Response
		out []*outgoing
	)

	if to.Tag() != "" {
		res, out = r.resubscribe(ctx, req)
	} else {
		res, out = r.subscribe(ctx, req, routes)
	}

	respond(res)
	r.send(out)
}

type subscribeRequest struct {
	req        *sip.Request
	impi       string
	subscriber db.Subscriber
	expires    time.Duration
}

func (r *Registrar) subscribe(ctx context.Context, req *sip.Request, routes []sip.URI) (*sip.Response, []*outgoing) {
	if !req.URI.IsSIP() && !req.URI.IsTel() {
		return sip.NewResponse(req, 416, ""), nil
	}

	key := identityKey(req.URI)

	sr, res := r.authorize(ctx, req, key, routes)
	if res != nil {
		return res, nil
	}

	expires, res := subscriptionExpires(req)
	if res != nil {
		return res, nil
	}

	sr.expires = expires

	if err := r.lock(ctx, sr.impi); err != nil {
		return retryLater(req), nil
	}

	defer r.unlock(sr.impi)

	st, err := r.load(ctx, sr.impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", sr.impi), slog.Any("error", err))
		return retryLater(req), nil
	}

	if !st.registered(st.set(key)) {
		return sip.NewResponse(req, 480, ""), nil
	}

	res = sip.NewResponse(req, 200, "")
	_ = res.Header.SetToTag(sip.NewTag())
	res.Header.Add("Expires", strconv.FormatInt(int64(expires/time.Second), 10))
	res.Header.Add("Contact", contactURI(req.Flow.Local, req.Flow.Transport))

	d, err := dialog.NewUAS(req, res)
	if err != nil {
		return sip.NewResponse(req, 400, "No Dialog"), nil
	}

	snap, err := json.Marshal(d.Snapshot())
	if err != nil {
		return retryLater(req), nil
	}

	id := d.ID()
	s := db.RegSubscription{
		IMPI:         sr.impi,
		IMPU:         receivedIdentity(req.URI),
		Subscriber:   sr.subscriber,
		CallID:       id.CallID,
		LocalTag:     id.LocalTag,
		RemoteTag:    id.RemoteTag,
		RemoteTarget: d.RemoteTarget().String(),
		Dialog:       snap,
		Version:      -1,
		ExpiresAt:    st.now.Add(expires),
	}

	subState := activeState(s.ExpiresAt, st.now)
	if expires == 0 {
		subState = "terminated;reason=" + reasonTimeout
	}

	o, err := r.newNotify(ctx, st, &s, nil, subState)
	if err != nil {
		r.log.Warn("failed to build a NOTIFY", slog.String("impi", sr.impi), slog.Any("error", err))
		return sip.NewResponse(req, 500, ""), nil
	}

	if expires == 0 {
		o.final = true
		return res, []*outgoing{o}
	}

	r.replaceDuplicates(ctx, s)

	if s.ID, err = r.cfg.DB.PutRegSubscription(ctx, s); err != nil {
		r.log.Warn("failed to store a reg event subscription", slog.String("impi", sr.impi), slog.Any("error", err))
		return retryLater(req), nil
	}

	o.subID = s.ID

	r.log.Info("subscribed to reg event", slog.String("impi", sr.impi), slog.String("impu", s.IMPU),
		slog.String("subscriber", string(sr.subscriber)))

	r.armSweep(s.ExpiresAt)

	return res, []*outgoing{o}
}

func (r *Registrar) authorize(ctx context.Context, req *sip.Request, key string, routes []sip.URI,
) (*subscribeRequest, *sip.Response) {
	if accept := req.Header.Values("Accept"); len(accept) > 0 && !accepts(req.Header.Elements("Accept")) {
		res := sip.NewResponse(req, 406, "")
		res.Header.Add("Accept", regevent.ContentType)

		return nil, res
	}

	regs, err := r.cfg.DB.ListRegistrationsByIdentity(ctx, key)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impu", req.URI.String()), slog.Any("error", err))
		return nil, retryLater(req)
	}

	now := r.clock.Now()

	registered := false

	for _, reg := range regs {
		registered = registered || len(liveAt(reg.Bindings, now)) > 0
	}

	if !registered {
		return nil, sip.NewResponse(req, 480, "")
	}

	asserted, err := req.Header.Addresses("P-Asserted-Identity")
	if err != nil {
		return nil, sip.NewResponse(req, 400, "Bad P-Asserted-Identity")
	}

	for _, a := range asserted {
		for _, reg := range regs {
			for _, b := range liveAt(reg.Bindings, now) {
				if pathHolds(b.Contact.Path, a.URI) {
					return &subscribeRequest{req: req, impi: reg.IMPI, subscriber: db.SubscriberPCSCF}, nil
				}
			}
		}
	}

	if impi, ok := serviceRouteUser(regs, routes); ok {
		regs = slices.DeleteFunc(slices.Clone(regs), func(reg db.Registration) bool { return reg.IMPI != impi })
	}

	for _, a := range asserted {
		for _, reg := range regs {
			if r.ownedBy(ctx, reg.IMPI, identityKey(a.URI)) {
				return &subscribeRequest{req: req, impi: reg.IMPI, subscriber: db.SubscriberUE}, nil
			}
		}
	}

	r.log.Info("reg event SUBSCRIBE from an unauthorised identity", slog.String("impu", req.URI.String()),
		slog.Any("p-asserted-identity", req.Header.Values("P-Asserted-Identity")))

	return nil, sip.NewResponse(req, 403, "")
}

func serviceRouteUser(regs []db.Registration, routes []sip.URI) (string, bool) {
	for _, u := range routes {
		id, ok := strings.CutPrefix(u.User, "orig-")
		if !ok {
			continue
		}

		contactID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			continue
		}

		for _, reg := range regs {
			for _, b := range reg.Bindings {
				if b.Contact.ID == contactID {
					return reg.IMPI, true
				}
			}
		}
	}

	return "", false
}

func accepts(types []string) bool {
	for _, t := range types {
		mt, _, _ := strings.Cut(t, ";")

		switch strings.ToLower(strings.TrimSpace(mt)) {
		case regevent.ContentType, "application/*", "*/*":
			return true
		}
	}

	return false
}

func pathHolds(path string, u sip.URI) bool {
	if path == "" {
		return false
	}

	entries, err := sip.ParseAddressList(path)
	if err != nil {
		return false
	}

	for _, e := range entries {
		if e.URI.Equivalent(u) {
			return true
		}
	}

	return false
}

func (r *Registrar) ownedBy(ctx context.Context, impi, key string) bool {
	regs, err := r.cfg.DB.ListRegistrationsByIMPI(ctx, impi)
	if err != nil {
		return false
	}

	for _, reg := range regs {
		for _, id := range reg.Identities {
			if id.Key == key && !id.Barred {
				return true
			}
		}
	}

	return false
}

func subscriptionExpires(req *sip.Request) (time.Duration, *sip.Response) {
	if !req.Header.Has("Expires") {
		return defaultSubscriptionExpires, nil
	}

	v, err := req.Header.Expires()
	if err != nil {
		return 0, sip.NewResponse(req, 400, "Bad Expires")
	}

	return min(time.Duration(v)*time.Second, maxSubscriptionExpires), nil
}

func (r *Registrar) replaceDuplicates(ctx context.Context, s db.RegSubscription) {
	subs, err := r.cfg.DB.ListRegSubscriptions(ctx, s.IMPI)
	if err != nil {
		return
	}

	target, err := sip.ParseURI(s.RemoteTarget)
	if err != nil {
		return
	}

	for _, old := range subs {
		u, err := sip.ParseURI(old.RemoteTarget)
		if err != nil || old.Subscriber != s.Subscriber || identityKeyOf(old.IMPU) != identityKeyOf(s.IMPU) || !u.Equivalent(target) {
			continue
		}

		r.log.Info("reg event subscription replaced", slog.String("impi", s.IMPI), slog.String("impu", s.IMPU))
		r.dropSubscription(ctx, old)
	}
}

func (r *Registrar) resubscribe(ctx context.Context, req *sip.Request) (*sip.Response, []*outgoing) {
	id := dialog.RequestID(req)

	s, err := r.cfg.DB.GetRegSubscription(ctx, id.CallID, id.LocalTag, id.RemoteTag)

	switch {
	case errors.Is(err, db.ErrNotFound):
		return sip.NewResponse(req, 481, ""), nil
	case err != nil:
		return retryLater(req), nil
	}

	expires, res := subscriptionExpires(req)
	if res != nil {
		return res, nil
	}

	if err := r.lock(ctx, s.IMPI); err != nil {
		return retryLater(req), nil
	}

	defer r.unlock(s.IMPI)

	if s, err = r.cfg.DB.GetRegSubscriptionByID(ctx, s.ID); err != nil {
		return sip.NewResponse(req, 481, ""), nil
	}

	d, err := restoreDialog(s.Dialog)
	if err != nil {
		r.log.Warn("invalid stored dialog", slog.String("impi", s.IMPI), slog.Any("error", err))
		return sip.NewResponse(req, 481, ""), nil
	}

	if err := d.ReceiveRequest(req); err != nil {
		code := 400
		if serr, ok := errors.AsType[*sip.StatusError](err); ok {
			code = serr.StatusCode
		}

		return sip.NewResponse(req, code, ""), nil
	}

	st, err := r.load(ctx, s.IMPI)
	if err != nil {
		return retryLater(req), nil
	}

	res = sip.NewResponse(req, 200, "")
	res.Header.Add("Expires", strconv.FormatInt(int64(expires/time.Second), 10))
	res.Header.Add("Contact", contactURI(req.Flow.Local, req.Flow.Transport))

	if s.Dialog, err = json.Marshal(d.Snapshot()); err != nil {
		return retryLater(req), nil
	}

	s.RemoteTarget = d.RemoteTarget().String()
	s.ExpiresAt = st.now.Add(expires)

	subState := activeState(s.ExpiresAt, st.now)
	if expires == 0 {
		subState = "terminated;reason=" + reasonTimeout

		r.log.Info("unsubscribed from reg event", slog.String("impi", s.IMPI), slog.String("impu", s.IMPU))
	} else {
		r.armSweep(s.ExpiresAt)
	}

	var out []*outgoing
	if o := r.buildNotify(ctx, st, s, nil, subState); o != nil {
		out = append(out, o)
	}

	return res, out
}
