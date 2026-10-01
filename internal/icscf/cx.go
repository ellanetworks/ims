package icscf

import (
	"context"
	"errors"
	"fmt"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
)

func (i *ICSCF) envelope() tgpp.Envelope {
	return tgpp.Envelope{
		SessionID:        i.cfg.Diameter.NewSessionID(),
		Origin:           i.cfg.Diameter.Identity(),
		DestinationRealm: i.cfg.HSS.Realm,
	}
}

func (i *ICSCF) userAuthorization(ctx context.Context, r cx.UserAuthorizationRequest) (cx.UserAuthorization, error) {
	req, err := cx.NewUserAuthorizationRequest(i.envelope(), r)
	if err != nil {
		return cx.UserAuthorization{}, fmt.Errorf("UAR: %w", err)
	}

	ans, err := i.do(ctx, req)
	if err != nil {
		return cx.UserAuthorization{}, fmt.Errorf("UAR: %w", err)
	}

	uaa, err := cx.ParseUserAuthorizationAnswer(ans)
	if err != nil {
		return cx.UserAuthorization{}, fmt.Errorf("UAA: %w", err)
	}

	return uaa, nil
}

func (i *ICSCF) locationInfo(ctx context.Context, impu string, originating bool) (cx.LocationInfo, error) {
	req, err := cx.NewLocationInfoRequest(i.envelope(), cx.LocationInfoRequest{PublicIdentity: impu, Originating: originating})
	if err != nil {
		return cx.LocationInfo{}, fmt.Errorf("LIR: %w", err)
	}

	ans, err := i.do(ctx, req)
	if err != nil {
		return cx.LocationInfo{}, fmt.Errorf("LIR: %w", err)
	}

	lia, err := cx.ParseLocationInfoAnswer(ans)
	if err != nil {
		return cx.LocationInfo{}, fmt.Errorf("LIA: %w", err)
	}

	return lia, nil
}

func (i *ICSCF) do(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, i.cfg.CxTimeout)
	defer cancel()

	return i.cfg.Diameter.Do(ctx, i.cfg.HSS.ID, req, diameter.FailFast())
}

// negative reports whether the HSS refused the query. Any other failure means
// the query could not be completed: a timeout, an unreachable or failing HSS
// (DIAMETER_UNABLE_TO_COMPLY), or a malformed answer.
func negative(err error) bool {
	var re *cx.ResultError
	if !errors.As(err, &re) {
		return false
	}

	if re.Experimental {
		return re.Permanent()
	}

	return re.Code == diameter.ResultAuthorizationRejected
}

// registrationFailure is the response to a failed user registration status
// query (TS 24.229 §5.3.1.3).
func registrationFailure(err error) int {
	if negative(err) {
		return 403
	}

	return 480
}

// locationFailure is the response to a failed user location query (TS 24.229
// §5.3.2.1, §5.3.2.2). A terminating request to a known user who is not
// registered and has no unregistered services gets 480.
func locationFailure(err error, originating bool) int {
	var re *cx.ResultError

	switch {
	case !negative(err):
		return 480
	case !originating && errors.As(err, &re) && re.IsExperimental(tgpp.ResultErrorIdentityNotRegistered):
		return 480
	default:
		return 404
	}
}
