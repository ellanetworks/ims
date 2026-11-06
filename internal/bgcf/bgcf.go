package bgcf

import (
	"errors"
	"strings"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
)

const OnNetParam = "onnet"

var ErrNoRoute = errors.New("no usable SIP route")

type BGCF struct {
	settings func() settings.Settings
	states   settings.TrunkStates
}

func New(settings func() settings.Settings, states settings.TrunkStates) *BGCF {
	return &BGCF{settings: settings, states: states}
}

// RFC 3966 section 3
func Number(u sip.URI) (string, bool) {
	if !u.IsTel() || !strings.HasPrefix(u.User, "+") {
		return "", false
	}

	number := strings.Map(func(r rune) rune {
		if strings.ContainsRune("-.()", r) {
			return -1
		}

		return r
	}, u.User)

	return number, global(number)
}

func global(number string) bool {
	digits, ok := strings.CutPrefix(number, "+")

	return ok && digits != "" && strings.Trim(digits, "0123456789") == ""
}

// TS 23.228 clause 4.6.4
// TS 24.229 clause 5.6.2
func (b *BGCF) Trunks(number string) ([]settings.Trunk, error) {
	if !global(number) {
		return nil, ErrNoRoute
	}

	s := b.settings()

	best := -1

	for i, r := range s.SIPRoutes {
		if strings.HasPrefix(number, r.Prefix) && (best < 0 || len(r.Prefix) > len(s.SIPRoutes[best].Prefix)) {
			best = i
		}
	}

	if best < 0 {
		return nil, ErrNoRoute
	}

	var trunks []settings.Trunk

	for _, id := range s.SIPRoutes[best].Trunks {
		t, ok := s.Trunk(id)
		if ok && t.Enabled && settings.StatusOf(b.states, id).Usable() {
			trunks = append(trunks, t)
		}
	}

	if len(trunks) == 0 {
		return nil, ErrNoRoute
	}

	return trunks, nil
}

func (b *BGCF) Routable(u sip.URI) bool {
	number, ok := Number(u)
	if !ok {
		return false
	}

	_, err := b.Trunks(number)

	return err == nil
}
