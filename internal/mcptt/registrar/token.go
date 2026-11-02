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

// TS 33.180 clause 5.1.3
type TokenCheck interface {
	Authorise(cfg *settings.Snapshot, token string, impus []sip.URI) (*settings.User, error)
}

var (
	ErrToken   = errors.New("registrar: not an access token")
	ErrUnknown = errors.New("registrar: not a configured user")
	ErrIMPU    = errors.New("registrar: not an IMPU of the user")
)

// TS 33.180 clause B.2.2.1
// RFC 7519 section 3
type Unvalidated struct{}

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
