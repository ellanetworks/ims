package scscf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/db"
)

const (
	assignRegistration          = cx.AssignmentRegistration
	assignReRegistration        = cx.AssignmentReRegistration
	assignTimeoutDeregistration = cx.AssignmentTimeoutDeregistration
	assignUserDeregistration    = cx.AssignmentUserDeregistration
	assignAuthenticationFailure = cx.AssignmentAuthenticationFailure
	assignAuthenticationTimeout = cx.AssignmentAuthenticationTimeout
	assignAdministrative        = cx.AssignmentAdministrativeDeregistration
	assignTooMuchData           = cx.AssignmentDeregistrationTooMuchData
)

var errNoAKAVector = errors.New("no Digest-AKAv1-MD5 vector in the MAA")

type authVector struct {
	rand, autn, xres, ck, ik []byte
}

func (r *Registrar) multimediaAuth(ctx context.Context, to db.HSS, impi, impu string, resync *cx.Resync) (authVector, db.HSS, error) {
	ans, from, err := r.cx(ctx, to, func(env tgpp.Envelope) (*diameter.Message, error) {
		return cx.NewMultimediaAuthRequest(env, cx.MultimediaAuthRequest{
			PrivateIdentity: impi,
			PublicIdentity:  impu,
			ServerName:      r.serverName,
			NumberOfItems:   1,
			Scheme:          cx.SchemeDigestAKAv1MD5,
			Resync:          resync,
		})
	})
	if err != nil {
		return authVector{}, db.HSS{}, fmt.Errorf("MAR: %w", err)
	}

	maa, err := cx.ParseMultimediaAuthAnswer(ans)
	if err != nil {
		return authVector{}, db.HSS{}, fmt.Errorf("MAA: %w", err)
	}

	for _, item := range maa.Items {
		if item.Scheme == cx.SchemeDigestAKAv1MD5 && item.AKA != nil {
			v := item.AKA
			return authVector{rand: v.RAND, autn: v.AUTN, xres: v.XRES, ck: v.CK, ik: v.IK}, from, nil
		}
	}

	return authVector{}, db.HSS{}, errNoAKAVector
}

func (r *Registrar) serverAssignment(ctx context.Context, to db.HSS, impi string, impus []string, t cx.AssignmentType,
	userDataAvailable bool,
) (cx.ServerAssignment, db.HSS, error) {
	ans, from, err := r.cx(ctx, to, func(env tgpp.Envelope) (*diameter.Message, error) {
		return cx.NewServerAssignmentRequest(env, cx.ServerAssignmentRequest{
			PrivateIdentity:          impi,
			PublicIdentities:         impus,
			ServerName:               r.serverName,
			Type:                     t,
			UserDataAlreadyAvailable: userDataAvailable,
		})
	})
	if err != nil {
		return cx.ServerAssignment{}, db.HSS{}, fmt.Errorf("SAR %s: %w", t, err)
	}

	saa, err := cx.ParseServerAssignmentAnswer(ans)
	if err != nil {
		return cx.ServerAssignment{}, db.HSS{}, fmt.Errorf("SAA %s: %w", t, err)
	}

	return saa, from, nil
}

// cx sends a Cx request to the HSS that serves the registration, or, unknown, to the realm of the HSS, and returns
// the HSS that answered (TS 29.229 §5.5). When the HSS of the registration cannot be reached, a new request goes to
// the realm: Cx keeps no session state, and an HSS the S-CSCF cannot reach is no longer one it knows.
func (r *Registrar) cx(ctx context.Context, to db.HSS, build func(tgpp.Envelope) (*diameter.Message, error),
) (*diameter.Message, db.HSS, error) {
	ctx, cancel := context.WithTimeout(ctx, cxTimeout)
	defer cancel()

	ans, err := r.cxTo(ctx, to, build)
	if to.Host != "" && undeliverable(ans, err) {
		r.log.Info("the HSS of the registration is unreachable, trying the realm of the HSS", slog.String("hss", to.Host),
			slog.Any("error", undeliverableReason(ans, err)))

		ans, err = r.cxTo(ctx, db.HSS{}, build)
	}

	if err != nil {
		return nil, db.HSS{}, err
	}

	origin := tgpp.ParseEnvelope(ans).Origin

	return ans, db.HSS{Host: origin.OriginHost, Realm: origin.OriginRealm}, nil
}

func (r *Registrar) cxTo(ctx context.Context, to db.HSS, build func(tgpp.Envelope) (*diameter.Message, error),
) (*diameter.Message, error) {
	env := tgpp.Envelope{
		SessionID:        r.cfg.Diameter.NewSessionID(),
		Origin:           r.cfg.Diameter.Identity(),
		DestinationHost:  to.Host,
		DestinationRealm: to.Realm,
	}

	if to.Host == "" {
		env.DestinationRealm = r.cfg.HSSRealm()
	}

	req, err := build(env)
	if err != nil {
		return nil, err
	}

	return r.cfg.Diameter.Send(ctx, req, diameter.FailFast())
}

// undeliverable reports whether a request did not reach its HSS: no connection to it, no route, or an agent or the
// HSS itself answering that it cannot serve it (RFC 6733 §7.1.3).
func undeliverable(ans *diameter.Message, err error) bool {
	if err != nil {
		return errors.Is(err, diameter.ErrNotConnected) || errors.Is(err, diameter.ErrUnableToDeliver)
	}

	code, ok := protocolError(ans)

	return ok && (code == diameter.ResultUnableToDeliver || code == diameter.ResultTooBusy)
}

func undeliverableReason(ans *diameter.Message, err error) any {
	if err != nil {
		return err
	}

	code, _ := protocolError(ans)

	return diameter.ResultName(code)
}

func protocolError(ans *diameter.Message) (uint32, bool) {
	if ans.Flags&diameter.FlagError == 0 {
		return 0, false
	}

	a, ok := ans.Find(diameter.AVPResultCode, 0)
	if !ok {
		return 0, false
	}

	code, err := a.Unsigned32()

	return code, err == nil
}

func refused(err error) bool {
	return resultIs(err, tgpp.ResultErrorUserUnknown, tgpp.ResultErrorIdentitiesDontMatch,
		tgpp.ResultErrorRoamingNotAllowed, tgpp.ResultErrorAuthSchemeNotSupported)
}

func permanent(err error) bool {
	var re *cx.ResultError
	return errors.As(err, &re) && re.Permanent()
}

func resultIs(err error, codes ...uint32) bool {
	var re *cx.ResultError
	if !errors.As(err, &re) {
		return false
	}

	for _, code := range codes {
		if re.IsExperimental(code) {
			return true
		}
	}

	return false
}
