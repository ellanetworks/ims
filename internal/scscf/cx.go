package scscf

import (
	"context"
	"errors"
	"fmt"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
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

func (r *Registrar) envelope() tgpp.Envelope {
	return tgpp.Envelope{
		SessionID:        r.cfg.Diameter.NewSessionID(),
		Origin:           r.cfg.Diameter.Identity(),
		DestinationHost:  r.cfg.HSS.Host,
		DestinationRealm: r.cfg.HSS.Realm,
	}
}

func (r *Registrar) multimediaAuth(ctx context.Context, impi, impu string, resync *cx.Resync) (authVector, error) {
	req, err := cx.NewMultimediaAuthRequest(r.envelope(), cx.MultimediaAuthRequest{
		PrivateIdentity: impi,
		PublicIdentity:  impu,
		ServerName:      r.serverName,
		NumberOfItems:   1,
		Scheme:          cx.SchemeDigestAKAv1MD5,
		Resync:          resync,
	})
	if err != nil {
		return authVector{}, fmt.Errorf("MAR: %w", err)
	}

	ans, err := r.do(ctx, req)
	if err != nil {
		return authVector{}, fmt.Errorf("MAR: %w", err)
	}

	maa, err := cx.ParseMultimediaAuthAnswer(ans)
	if err != nil {
		return authVector{}, fmt.Errorf("MAA: %w", err)
	}

	for _, item := range maa.Items {
		if item.Scheme == cx.SchemeDigestAKAv1MD5 && item.AKA != nil {
			v := item.AKA
			return authVector{rand: v.RAND, autn: v.AUTN, xres: v.XRES, ck: v.CK, ik: v.IK}, nil
		}
	}

	return authVector{}, errNoAKAVector
}

func (r *Registrar) serverAssignment(ctx context.Context, impi string, impus []string, t cx.AssignmentType, userDataAvailable bool) (cx.ServerAssignment, error) {
	req, err := cx.NewServerAssignmentRequest(r.envelope(), cx.ServerAssignmentRequest{
		PrivateIdentity:          impi,
		PublicIdentities:         impus,
		ServerName:               r.serverName,
		Type:                     t,
		UserDataAlreadyAvailable: userDataAvailable,
	})
	if err != nil {
		return cx.ServerAssignment{}, fmt.Errorf("SAR %s: %w", t, err)
	}

	ans, err := r.do(ctx, req)
	if err != nil {
		return cx.ServerAssignment{}, fmt.Errorf("SAR %s: %w", t, err)
	}

	saa, err := cx.ParseServerAssignmentAnswer(ans)
	if err != nil {
		return cx.ServerAssignment{}, fmt.Errorf("SAA %s: %w", t, err)
	}

	return saa, nil
}

func (r *Registrar) do(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, cxTimeout)
	defer cancel()

	return r.cfg.Diameter.Do(ctx, r.cfg.HSS.ID, req, diameter.FailFast())
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
