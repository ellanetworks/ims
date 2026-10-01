package icscf

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

type registration struct {
	tx  *transaction.ServerTransaction
	req *sip.Request
	uar cx.UserAuthorizationRequest

	// reselect is set when a failed S-CSCF may be replaced (TS 24.229
	// §5.3.1.3): the REGISTER has an Authorization header field whose
	// "integrity-protected" is absent or "no".
	reselect bool

	assigned bool
	tried    []*SCSCF
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

	reg := &registration{tx: tx, req: req, uar: cx.UserAuthorizationRequest{
		PublicIdentity:    impu,
		PrivateIdentity:   privateIdentity(to.URI),
		VisitedNetwork:    visitedNetwork(req.Header),
		AuthorizationType: authType,
	}}

	if a, ok := credentials(req.Header, i.cfg.HomeDomain); ok {
		if username := authParam(a, "username"); username != "" {
			reg.uar.PrivateIdentity = username
		}

		protected := authParam(a, "integrity-protected")
		reg.reselect = protected == "" || strings.EqualFold(protected, "no")
	}

	if reg.uar.PrivateIdentity == "" {
		return nil, sip.NewResponse(req, 400, "No Private Identity")
	}

	if reg.uar.VisitedNetwork == "" {
		reg.uar.VisitedNetwork = i.cfg.HomeDomain
	}

	return reg, nil
}

// authorizationType is DE_REGISTRATION when every contact's expiry is zero
// (TS 29.228 Table 6.1.1.1). A malformed header field is named in the reason
// phrase of a 400.
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

	var s *SCSCF

	if uaa.ServerName != "" {
		reg.assigned = true

		if s = i.lookupName(uaa.ServerName); s == nil {
			i.log.Warn("the HSS assigned an unknown S-CSCF", slog.String("impu", reg.uar.PublicIdentity),
				slog.String("server-name", uaa.ServerName))
			i.answer(reg.tx, 480)

			return
		}
	} else if s = i.choose(uaa.Capabilities, nil); s == nil {
		i.answer(reg.tx, 600)
		return
	}

	i.forwardRegister(reg, s, false)
}

func (i *ICSCF) forwardRegister(reg *registration, s *SCSCF, reselected bool) {
	reg.tried = append(reg.tried, s)

	out := reg.req.Clone()
	out.URI = s.Name.Clone()

	if reselected {
		out.URI.Params.Set("scscf-reselection", "")
	}

	to, ok := i.target(s, reg.req.Flow, s.Name)
	if !ok {
		i.log.Warn("no listener for the S-CSCF", slog.String("scscf", s.Name.String()))
		i.answer(reg.tx, 480)

		return
	}

	i.forward(reg.tx, out, to, proxy.Options{
		Timeout: i.cfg.SCSCFTimeout,
		OnReply: func(r proxy.Reply) proxy.Verdict { return i.registerReply(reg, r) },
	})
}

func (i *ICSCF) registerReply(reg *registration, r proxy.Reply) proxy.Verdict {
	timedOut := r.Response == nil && errors.Is(r.Err, transaction.ErrTimeout)
	refused := r.Response != nil && (r.Response.StatusCode/100 == 3 || r.Response.StatusCode == 480)

	if (timedOut || refused) && reg.reselect && i.untried(reg.tried) {
		i.spawn(reg.tx, func(ctx context.Context) { i.reselectRegister(ctx, reg, r.Response) })
		return proxy.Hold
	}

	if timedOut {
		i.answer(reg.tx, 504)
		return proxy.Hold
	}

	if r.Response != nil {
		i.outgoing(reg.req, r.Response)
	}

	return proxy.Relay
}

// reselectRegister replaces an S-CSCF that did not respond, or answered 3xx
// or 480 (TS 24.229 §5.3.1.3). When the HSS had assigned it, the HSS is asked
// for the capabilities first.
func (i *ICSCF) reselectRegister(ctx context.Context, reg *registration, failed *sip.Response) {
	var caps *cx.ServerCapabilities

	if reg.assigned {
		uar := reg.uar
		uar.AuthorizationType = cx.AuthorizationRegistrationAndCapabilities

		uaa, err := i.userAuthorization(ctx, uar)
		if err != nil {
			i.log.Info("capabilities query failed", slog.String("impu", reg.uar.PublicIdentity), slog.Any("error", err))
			i.answer(reg.tx, registrationFailure(err))

			return
		}

		if uaa.ServerName != "" {
			i.endRegister(reg, failed)
			return
		}

		caps = uaa.Capabilities
	}

	s := i.choose(caps, reg.tried)
	if s == nil {
		i.endRegister(reg, failed)
		return
	}

	i.log.Info("reselecting the S-CSCF", slog.String("impu", reg.uar.PublicIdentity), slog.String("scscf", s.Name.String()))
	i.forwardRegister(reg, s, true)
}

// endRegister ends a REGISTER whose S-CSCF cannot be replaced: with the
// S-CSCF's response, or 504 when it did not respond.
func (i *ICSCF) endRegister(reg *registration, failed *sip.Response) {
	if failed == nil {
		i.answer(reg.tx, 504)
		return
	}

	i.outgoing(reg.req, failed)

	if err := i.cfg.Proxy.Relay(reg.tx, failed); err != nil {
		i.log.Debug("relaying a response failed", slog.Any("error", err))
	}
}
