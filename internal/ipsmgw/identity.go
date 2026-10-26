package ipsmgw

import (
	"regexp"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/imsxml"
)

// key is what an identity is found by: its canonical form as a public identity (sip.URI.Identity; TS 23.003 §13.4),
// "" for a URI neither SIP nor tel.
func key(u sip.URI) string {
	if !u.IsSIP() && !u.IsTel() {
		return ""
	}

	return u.Identity().String()
}

// user are the identities of the user of a third-party REGISTER that the IP-SM-GW stores (TS 24.341 §5.3.3.2): the
// MSISDN of the <service-info> of its application/3gpp-ims+xml body, if any; and the IMSI of the private user
// identity of the Authorization of the REGISTER of the UE in its message/sip body, else of the To of that REGISTER,
// which TS 24.341 has of a user of no MSISDN, and TS 29.338 Table 6.2.1.1/1 in the MO Forward Short Message of any
// user, if available. Each is "" if unknown.
func user(req *sip.Request) (msisdn, imsi string) {
	msisdn = serviceInfoMSISDN(req)

	reg := ueRegister(req)
	if reg == nil {
		return msisdn, ""
	}

	if auth := reg.Header.Get("Authorization"); auth != "" {
		creds, err := sip.ParseAuth(auth)
		if err != nil {
			return msisdn, ""
		}

		username, _ := creds.Params.Get("username")

		return msisdn, imsiOf(strings.Trim(username, `"`))
	}

	to, err := reg.Header.To()
	if err != nil || !to.URI.IsSIP() {
		return msisdn, ""
	}

	return msisdn, imsiOf(to.URI.User + "@" + to.URI.Host)
}

// serviceInfoMSISDN is the MSISDN of the <service-info> of the third-party REGISTER, of the iFC of the IP-SM-GW
// (TS 24.229 §5.4.1.7, §7.6): an E.164 number, of an optional "+"; "" if none.
func serviceInfoMSISDN(req *sip.Request) string {
	part, ok, err := req.Content().Find(imsxml.ContentType)
	if err != nil || !ok {
		return ""
	}

	doc, err := imsxml.Decode(part.Body)
	if err != nil {
		return ""
	}

	n := strings.TrimPrefix(strings.TrimSpace(doc.ServiceInfo), "+")
	if !e164(n) {
		return ""
	}

	return n
}

// e164 reports whether n is the digits of an international number (ITU-T E.164 §6).
func e164(n string) bool {
	return n != "" && len(n) <= 15 && strings.Trim(n, "0123456789") == ""
}

// ueRegister is the REGISTER of the UE in the message/sip body of a third-party REGISTER (TS 24.229 §5.4.1.7), nil
// if none.
func ueRegister(req *sip.Request) *sip.Request {
	parts, err := req.Content().Parts()
	if err != nil {
		return nil
	}

	for _, p := range parts {
		if p.MediaType() != "message/sip" {
			continue
		}

		if m, err := sip.Parse(p.Body); err == nil {
			if reg, ok := m.(*sip.Request); ok && reg.Method == "REGISTER" {
				return reg
			}
		}
	}

	return nil
}

// derivedRealm is the realm of an identity derived from an IMSI (TS 23.003 §13.3, §13.4B), of an SNPN or not.
var derivedRealm = regexp.MustCompile(`^ims\.(?:nid[0-9a-f]{11}\.)?mnc([0-9]{3})\.mcc([0-9]{3})\.3gppnetwork\.org$`)

// imsiOf is the IMSI of a private user identity derived from it, "<IMSI>@ims.mnc<MNC>.mcc<MCC>.3gppnetwork.org"
// (TS 23.003 §13.3), or of the user and host of a temporary public user identity, which has the same form (§13.4B);
// "" for another identity.
func imsiOf(nai string) string {
	user, realm, ok := strings.Cut(nai, "@")
	if !ok || len(user) < 6 || len(user) > 15 || strings.Trim(user, "0123456789") != "" {
		return ""
	}

	m := derivedRealm.FindStringSubmatch(strings.ToLower(realm))
	if m == nil {
		return ""
	}

	mnc, mcc := m[1], m[2]

	// A 2-digit MNC has a leading 0 in the realm (TS 23.003 §13.2).
	if !strings.HasPrefix(user, mcc+mnc) && (mnc[0] != '0' || !strings.HasPrefix(user, mcc+mnc[1:])) {
		return ""
	}

	return user
}

// implicitSet is the implicit registration set of the IMPU of a third-party REGISTER, impu first: the
// P-Associated-URI of the 200 to the REGISTER of the UE in its message/sip body (TS 24.229 §5.4.1.7A), else of the
// third-party REGISTER.
func implicitSet(req *sip.Request, impu sip.URI) []sip.URI {
	pau := req.Header
	if res := ueResponse(req); res != nil && res.Header.Has("P-Associated-URI") {
		pau = res.Header
	}

	set := []sip.URI{impu}

	as, _ := pau.Addresses("P-Associated-URI")
	for _, a := range as {
		if !slices.ContainsFunc(set, func(u sip.URI) bool { return key(u) == key(a.URI) }) {
			set = append(set, a.URI)
		}
	}

	return set
}

// ueResponse is the 200 to the REGISTER of the UE in the message/sip body of a third-party REGISTER, nil if none.
func ueResponse(req *sip.Request) *sip.Response {
	parts, err := req.Content().Parts()
	if err != nil {
		return nil
	}

	for _, p := range parts {
		if p.MediaType() != "message/sip" {
			continue
		}

		if m, err := sip.Parse(p.Body); err == nil {
			if res, ok := m.(*sip.Response); ok {
				return res
			}
		}
	}

	return nil
}

// subscribed is the SIP URI the IP-SM-GW subscribes to the reg event package of impu with (TS 24.229 §5.7.1.1 a,
// c): impu itself, a SIP URI; of a global tel URI, the SIP URI of its number at home (TS 23.003 §13.4); else a SIP
// URI of its implicit registration set. Zero if none.
func subscribed(impu sip.URI, set []sip.URI, home string) sip.URI {
	if impu.IsSIP() {
		return impu
	}

	if n := impu.Identity().User; impu.IsTel() && strings.HasPrefix(n, "+") && home != "" {
		u := sip.URI{Scheme: "sip", User: n, Host: home}
		u.Params.Set("user", "phone")

		return u
	}

	if i := slices.IndexFunc(set, sip.URI.IsSIP); i >= 0 {
		return set[i]
	}

	return sip.URI{}
}
