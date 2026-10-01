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
)

var (
	// errNoAKAVector is a MAA without a Digest-AKAv1-MD5 vector.
	errNoAKAVector = errors.New("no Digest-AKAv1-MD5 vector in the MAA")

	errHSSDown = errors.New("the HSS is not connected")
)

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

// multimediaAuth fetches one AKA vector. resync carries RAND‖AUTS when the
// UE asked for resynchronisation.
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

// serverAssignment sends a SAR and returns the User-Data of the answer, if any.
func (r *Registrar) serverAssignment(ctx context.Context, impi string, impus []string, t cx.AssignmentType, userDataAvailable bool) ([]byte, error) {
	req, err := cx.NewServerAssignmentRequest(r.envelope(), cx.ServerAssignmentRequest{
		PrivateIdentity:          impi,
		PublicIdentities:         impus,
		ServerName:               r.serverName,
		Type:                     t,
		UserDataAlreadyAvailable: userDataAvailable,
	})
	if err != nil {
		return nil, fmt.Errorf("SAR %s: %w", t, err)
	}

	ans, err := r.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("SAR %s: %w", t, err)
	}

	saa, err := cx.ParseServerAssignmentAnswer(ans)
	if err != nil {
		return nil, fmt.Errorf("SAA %s: %w", t, err)
	}

	return saa.UserData, nil
}

// do sends a request to the HSS. It fails at once when the HSS is down, rather
// than wait for it to reconnect while the UE waits.
func (r *Registrar) do(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
	if p, ok := r.cfg.Diameter.Peer(r.cfg.HSS.ID); !ok || p.State != diameter.PeerOpen {
		return nil, errHSSDown
	}

	ctx, cancel := context.WithTimeout(ctx, cxTimeout)
	defer cancel()

	return r.cfg.Diameter.Do(ctx, r.cfg.HSS.ID, req)
}

// refused reports whether the HSS refused the user, which the UE gets as a 403.
func refused(err error) bool {
	var re *cx.ResultError
	if !errors.As(err, &re) {
		return false
	}

	return re.IsExperimental(tgpp.ResultErrorUserUnknown) || re.IsExperimental(tgpp.ResultErrorIdentitiesDontMatch)
}
