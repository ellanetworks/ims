package sip

import (
	"fmt"
	"strconv"
	"strings"
)

// ReferSub is a Refer-Sub value (RFC 4488 §4): Subscribe false asks for no implicit subscription.
type ReferSub struct {
	Subscribe bool
	Params    Params
}

func ParseReferSub(s string) (ReferSub, error) {
	value, ps, err := ParseTokenParams(s)
	if err != nil {
		return ReferSub{}, fmt.Errorf("Refer-Sub %w", err)
	}

	switch strings.ToLower(value) {
	case "true":
		return ReferSub{Subscribe: true, Params: ps}, nil
	case "false":
		return ReferSub{Params: ps}, nil
	}

	return ReferSub{}, fmt.Errorf("Refer-Sub %q: want true or false", s)
}

func (r ReferSub) String() string {
	return strconv.FormatBool(r.Subscribe) + r.Params.String()
}

// ReferSub is the Refer-Sub of a REFER or of its 2xx.
func (fs Header) ReferSub() (ReferSub, bool, error) {
	v, ok, err := fs.single("Refer-Sub")
	if !ok || err != nil {
		return ReferSub{}, ok, err
	}

	r, err := ParseReferSub(v)

	return r, true, err
}

// RefusedURIs are the P-Refused-URI-List entries (RFC 5318 §5), each with its members cid parameter, if any.
func (fs Header) RefusedURIs() ([]Address, error) {
	return fs.Addresses("P-Refused-URI-List")
}
