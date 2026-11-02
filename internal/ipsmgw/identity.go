package ipsmgw

import (
	"regexp"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/imsxml"
)

// TS 23.003 clause 13.4
func key(u sip.URI) string {
	if !u.IsSIP() && !u.IsTel() {
		return ""
	}

	return u.Identity().String()
}

// TS 24.341 clause 5.3.3.2
// TS 29.338 clause 6.2.1.1
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

// TS 24.229 clause 5.4.1.7
// TS 24.229 clause 7.6
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

// ITU-T E.164 section 6
func e164(n string) bool {
	return n != "" && len(n) <= 15 && strings.Trim(n, "0123456789") == ""
}

// TS 24.229 clause 5.4.1.7
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

// TS 23.003 clause 13.3
// TS 23.003 clause 13.4B
var derivedRealm = regexp.MustCompile(`^ims\.(?:nid[0-9a-f]{11}\.)?mnc([0-9]{3})\.mcc([0-9]{3})\.3gppnetwork\.org$`)

// TS 23.003 clause 13.3
// TS 23.003 clause 13.4B
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

	if !strings.HasPrefix(user, mcc+mnc) && (mnc[0] != '0' || !strings.HasPrefix(user, mcc+mnc[1:])) {
		return ""
	}

	return user
}

// TS 24.229 clause 5.4.1.7A
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

// TS 24.229 clause 5.7.1.1
// TS 23.003 clause 13.4
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
