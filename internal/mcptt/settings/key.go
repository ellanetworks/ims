package settings

import "github.com/ellanetworks/ims/sip"

func Key(u sip.URI) string { return key(u) }

// TS 23.003 clause 13.4
func key(u sip.URI) string {
	if !u.IsSIP() && !u.IsTel() {
		return ""
	}

	return u.Identity().String()
}

func SameIdentity(a, b string) bool {
	k := keyOf(a)
	return k != "" && k == keyOf(b)
}
