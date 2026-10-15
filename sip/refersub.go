package sip

import (
	"fmt"
	"strings"
)

// ReferSub is the Refer-Sub of a REFER or of its 2xx (RFC 4488 §4): false asks for no implicit subscription.
func (fs Header) ReferSub() (sub bool, present bool, err error) {
	v, ok, err := fs.single("Refer-Sub")
	if !ok || err != nil {
		return false, ok, err
	}

	value, _, err := ParseTokenParams(v)
	if err != nil {
		return false, true, fmt.Errorf("Refer-Sub %w", err)
	}

	switch strings.ToLower(value) {
	case "true":
		return true, true, nil
	case "false":
		return false, true, nil
	}

	return false, true, fmt.Errorf("Refer-Sub %q: want true or false", v)
}

// RefusedURIs are the P-Refused-URI-List entries (RFC 5318 §5), each with its members cid parameter, if any.
func (fs Header) RefusedURIs() ([]Address, error) {
	return fs.Addresses("P-Refused-URI-List")
}
