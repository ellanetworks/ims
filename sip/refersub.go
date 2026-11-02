package sip

import (
	"fmt"
	"strconv"
	"strings"
)

// RFC 4488 section 4
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

func (fs Header) ReferSub() (ReferSub, bool, error) {
	v, ok, err := fs.single("Refer-Sub")
	if !ok || err != nil {
		return ReferSub{}, ok, err
	}

	r, err := ParseReferSub(v)

	return r, true, err
}

// RFC 5318 section 5
func (fs Header) RefusedURIs() ([]Address, error) {
	return fs.Addresses("P-Refused-URI-List")
}
