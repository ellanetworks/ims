package icscf

import (
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

func (i *ICSCF) register(tx *transaction.ServerTransaction, req *sip.Request) {
	if !i.trusted(req) {
		i.log.Info("REGISTER from outside the trust domain", slog.String("source", req.Flow.Remote.String()))
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

	i.spawn(tx, func(ctx context.Context) { i.queryRegistration(ctx, reg) })
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

	if a, ok := credentials(req.Header, i.cfg.HomeDomain); ok {
		if username := authParam(a, "username"); username != "" {
			reg.uar.PrivateIdentity = username
		}
	}

	if reg.uar.PrivateIdentity == "" {
		return nil, sip.NewResponse(req, 400, "No Private Identity")
	}

	if reg.uar.VisitedNetwork == "" {
		reg.uar.VisitedNetwork = i.cfg.HomeDomain
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
	uaa, err := i.userAuthorization(ctx, reg.uar)
	if err != nil {
		i.log.Info("user registration status query failed", slog.String("impu", reg.uar.PublicIdentity),
			slog.String("impi", reg.uar.PrivateIdentity), slog.Any("error", err))
		i.answer(reg.tx, registrationFailure(err))

		return
	}

	name := i.cfg.SCSCF.Name

	switch {
	case uaa.ServerName != "":
		name = i.assigned(uaa.ServerName, reg.uar.PublicIdentity)
	case !i.capable(uaa.Capabilities):
		i.answer(reg.tx, 600)
		return
	}

	out := reg.req.Clone()
	out.URI = name.Clone()

	to, ok := i.target(reg.req.Flow, name)
	if !ok {
		i.log.Warn("no listener for the S-CSCF", slog.String("scscf", name.String()))
		i.answer(reg.tx, 480)

		return
	}

	i.forward(reg.tx, out, to, proxy.Options{
		Timeout: i.branchTimeout(reg.deadline),
		OnReply: func(r proxy.Reply) proxy.Verdict { return i.registerReply(reg, r) },
	})
}

func (i *ICSCF) registerReply(reg *registration, r proxy.Reply) proxy.Verdict {
	if unresponsive(r, false) {
		i.answer(reg.tx, 504)
		return proxy.Hold
	}

	if r.Response != nil {
		i.outgoing(reg.req, r.Response)
	}

	return proxy.Relay
}
