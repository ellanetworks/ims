package bgcf

import (
	"errors"
	"strings"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
)

const OnNetParam = "onnet"

var (
	ErrNoRoute       = errors.New("no SIP route")
	ErrNoUsableTrunk = errors.New("no usable SIP trunk on the SIP route")
)

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

const (
	minE164Digits = 7
	maxE164Digits = 15
)

// RFC 3966 section 5.1.4
// ITU-T E.164 section 6.1
// ITU-T E.164 section A.8.2
func E164(digits string) bool {
	return len(digits) >= minE164Digits && len(digits) <= maxE164Digits && digits[0] != '0' && strings.Trim(digits, "0123456789") == ""
}

func global(number string) bool {
	digits, ok := strings.CutPrefix(number, "+")

	return ok && E164(digits)
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
		return nil, ErrNoUsableTrunk
	}

	return trunks, nil
}

func (b *BGCF) Route(u sip.URI) error {
	number, ok := Number(u)
	if !ok {
		return ErrNoRoute
	}

	_, err := b.Trunks(number)

	return err
}
