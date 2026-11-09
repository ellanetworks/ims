package icscf

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

type registration struct {
	tx  *transaction.ServerTransaction
	req *sip.Request
	uar cx.UserAuthorizationRequest

	deadline time.Time
}

func (i *ICSCF) register(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	if !i.trusted(req) {
		i.log.WarnContext(ctx, "REGISTER from outside the trust domain", slog.String("source", req.Flow.Remote.String()))
		i.respond(tx, sip.NewResponse(req, 403, ""))

		return
	}

	out, _, err := i.cfg.Proxy.Preprocess(req)
	if err != nil {
		i.respond(tx, sip.NewResponse(req, 400, "Bad Route"))
		return
	}

	reg, res := i.parseRegister(tx, out)
	if res != nil {
		i.respond(tx, res)
		return
	}

	i.spawn(ctx, tx, func(ctx context.Context) { i.queryRegistration(ctx, reg) })
}

func (i *ICSCF) parseRegister(tx *transaction.ServerTransaction, req *sip.Request) (*registration, *sip.Response) {
	to, _ := req.Header.To()

	impu, err := publicIdentity(to.URI)
	if err != nil {
		return nil, sip.NewResponse(req, 400, "Bad To")
	}

	authType, bad := authorizationType(req.Header)
	if bad != "" {
		return nil, sip.NewResponse(req, 400, bad)
	}

	reg := &registration{tx: tx, req: req, deadline: i.deadline(), uar: cx.UserAuthorizationRequest{
		PublicIdentity:    impu,
		PrivateIdentity:   privateIdentity(to.URI),
		VisitedNetwork:    visitedNetwork(req.Header),
		AuthorizationType: authType,
	}}

	if a, ok := credentials(req.Header, i.cfg.Home); ok {
		if username := authParam(a, "username"); username != "" {
			reg.uar.PrivateIdentity = username
		}
	}

	if reg.uar.PrivateIdentity == "" {
		return nil, sip.NewResponse(req, 400, "No Private Identity")
	}

	if reg.uar.VisitedNetwork == "" {
		reg.uar.VisitedNetwork = cmp.Or(i.cfg.NetworkID, i.cfg.Home.Primary())
	}

	return reg, nil
}

func authorizationType(h sip.Header) (cx.AuthorizationType, string) {
	contacts, err := h.Contacts()

	switch {
	case errors.Is(err, sip.ErrMissingHeader) || err == nil && len(contacts) == 0:
		return cx.AuthorizationRegistration, ""
	case err != nil:
		return 0, "Bad Contact"
	}

	expires, hasExpires := uint32(0), h.Has("Expires")
	if hasExpires {
		if expires, err = h.Expires(); err != nil {
			return 0, "Bad Expires"
		}
	}

	if slices.ContainsFunc(contacts, func(c sip.Address) bool { return c.Star }) &&
		(len(contacts) > 1 || !hasExpires || expires != 0) {
		return 0, "Bad Contact"
	}

	for _, c := range contacts {
		if c.Star {
			continue
		}

		v, ok := c.Params.Get("expires")
		if !ok {
			if !hasExpires || expires != 0 {
				return cx.AuthorizationRegistration, ""
			}

			continue
		}

		if n, err := strconv.ParseUint(v, 10, 32); err != nil || n != 0 {
			return cx.AuthorizationRegistration, ""
		}
	}

	return cx.AuthorizationDeregistration, ""
}

func (i *ICSCF) queryRegistration(ctx context.Context, reg *registration) {
	reg.uar.Features = cx.FeatureIMSRestoration

	uaa, err := i.userAuthorization(ctx, reg.uar)
	if err != nil {
		i.log.InfoContext(ctx, "user registration status query failed", slog.String("impu", reg.uar.PublicIdentity),
			slog.String("impi", reg.uar.PrivateIdentity), slog.Any("error", err))
		i.answer(ctx, reg.tx, registrationFailure(err))

		return
	}

	if uaa.ServerName == "" {
		if !i.capable(uaa.Capabilities) {
			i.answer(ctx, reg.tx, 600)
			return
		}

		i.registerAt(ctx, reg, i.cfg.SCSCF.Name, false)

		return
	}

	restorable := uaa.Features&cx.FeatureIMSRestoration != 0 && !i.ours(uaa.ServerName)

	name, err := sip.ParseURI(uaa.ServerName)
	if err != nil {
		i.log.WarnContext(ctx, "unusable S-CSCF name from the HSS", slog.String("impu", reg.uar.PublicIdentity),
			slog.String("server-name", uaa.ServerName), slog.Any("error", err))

		if restorable {
			i.reassignRegistration(ctx, reg)
			return
		}

		i.answer(ctx, reg.tx, 480)

		return
	}

	i.registerAt(ctx, reg, name, restorable)
}

func (i *ICSCF) registerAt(ctx context.Context, reg *registration, name sip.URI, restorable bool) {
	to, err := i.scscfTarget(ctx, reg.req.Flow, name)
	if err != nil {
		i.log.WarnContext(ctx, "the S-CSCF cannot be reached", slog.String("impu", reg.uar.PublicIdentity),
			slog.String("scscf", name.String()), slog.Any("error", err))

		if restorable {
			i.reassignRegistration(ctx, reg)
			return
		}

		i.answer(ctx, reg.tx, 480)

		return
	}

	out := reg.req.Clone()
	out.URI = name.Clone()

	timeout := i.branchTimeout(reg.deadline)
	if restorable {
		timeout = max(timeout/2, i.t1)
	}

	i.forward(ctx, reg.tx, out, to, proxy.Options{
		Timeout: timeout,
		OnReply: func(ctx context.Context, r proxy.Reply) proxy.Verdict {
			return i.registerReply(ctx, reg, r, restorable)
		},
	})
}

// TS 23.380 clause 4.2.2
func (i *ICSCF) reassignRegistration(ctx context.Context, reg *registration) {
	uar := reg.uar
	uar.AuthorizationType = cx.AuthorizationRegistrationAndCapabilities

	uaa, err := i.userAuthorization(ctx, uar)
	if err != nil {
		i.log.InfoContext(ctx, "S-CSCF capabilities query failed", slog.String("impu", uar.PublicIdentity),
			slog.Any("error", err))
		i.answer(ctx, reg.tx, registrationFailure(err))

		return
	}

	if !i.capable(uaa.Capabilities) {
		i.answer(ctx, reg.tx, 600)
		return
	}

	i.log.InfoContext(ctx, "user reassigned to this S-CSCF", slog.String("impu", uar.PublicIdentity),
		slog.String("scscf", i.cfg.SCSCF.Name.String()))
	i.registerAt(ctx, reg, i.cfg.SCSCF.Name, false)
}

func (i *ICSCF) registerReply(ctx context.Context, reg *registration, r proxy.Reply, restorable bool) proxy.Verdict {
	if unresponsive(r, false) {
		if restorable {
			i.reassignRegistration(ctx, reg)
			return proxy.Hold
		}

		i.answer(ctx, reg.tx, 504)

		return proxy.Hold
	}

	if r.Response != nil {
		i.outgoing(reg.req, r.Response)
	}

	return proxy.Relay
}
