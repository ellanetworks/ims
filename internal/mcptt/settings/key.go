package settings

import "github.com/ellanetworks/ims/sip"

// Key is what an identity is found by, for the tables of the functions: key.
func Key(u sip.URI) string { return key(u) }

// key is what an identity is found by, an MCPTT ID, an MCPTT group ID, a PSI or an IMPU: its canonical form as a
// public identity (sip.URI.Identity; TS 23.003 §13.4), of the URIs equivalent to it; "" for a URI neither SIP nor
// tel.
func key(u sip.URI) string {
	if !u.IsSIP() && !u.IsTel() {
		return ""
	}

	return u.Identity().String()
}

// SameIdentity reports whether the URIs a and b are of the same identity, as Key has it.
func SameIdentity(a, b string) bool {
	k := keyOf(a)
	return k != "" && k == keyOf(b)
}
