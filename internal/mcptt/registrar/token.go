package registrar

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
)

// TokenCheck is the service authorisation of TS 33.180 §5.1.3: the user an access token authorises at the IMS public
// user identities of a registration, the implicit registration set of its IMPU through the IMS (TS 24.229 §5.4.1.7 c),
// its address of record in direct mode.
type TokenCheck interface {
	Authorise(cfg *settings.Snapshot, token string, impus []sip.URI) (*settings.User, error)
}

// Errors of Unvalidated.
var (
	ErrToken   = errors.New("registrar: not an access token")
	ErrUnknown = errors.New("registrar: not a configured user")
	ErrIMPU    = errors.New("registrar: not an IMPU of the user")
)

// Unvalidated is the token check of S1, without an identity management server [PM 4]: the MCPTT ID of the mcptt_id
// claim (TS 33.180 Table B.2.2.3-1) of the access token, a JWS of compact serialization (§B.2.2.1; RFC 7519 §3),
// decoded, its signature not verified. It authorises a configured user at a registration of one of the IMPUs
// configured for it only, which the IMS authenticated, the third-party REGISTER coming from the trust domain (TS 24.229 §4.4.1): a token claims the
// MCPTT ID of no user but those of its IMPU. An IMPU the configuration gives several users (TS 23.280 §8.3.1) is
// then any of theirs, its UE claiming the MCPTT ID it likes of them. In direct mode [PM 14], where nothing
// authenticates the IMPU, it authorises whoever claims it: a lab's guarantee.
//
// M4 replaces it with the validation of the token according to RFC 7519 (TS 33.180 §B.11.2; RFC 7519 §7.2), of
// the required exp, scope and client_id claims (Table B.2.2.2-1), whose MCPTT ID the IMPU is bound to (§5.1.3.2.2
// steps 9 to 11), and the configured IMPUs go.
type Unvalidated struct{}

// Authorise authorises the user of the token at impus.
func (Unvalidated) Authorise(cfg *settings.Snapshot, token string, impus []sip.URI) (*settings.User, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrToken, err)
	}

	var claims struct {
		MCPTTID string `json:"mcptt_id"`
	}

	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrToken, err)
	}

	id, err := sip.ParseURI(claims.MCPTTID)
	if err != nil {
		return nil, fmt.Errorf("%w: mcptt_id: %w", ErrToken, err)
	}

	u := cfg.User(id)

	switch {
	case u == nil:
		return nil, fmt.Errorf("%w: %s", ErrUnknown, id)
	case !slices.ContainsFunc(impus, u.HasIMPU):
		return nil, fmt.Errorf("%w: %v of %s", ErrIMPU, impus, id)
	}

	return u, nil
}
