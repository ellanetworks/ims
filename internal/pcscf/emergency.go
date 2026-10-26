package pcscf

import (
	"strings"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

const (
	contentTypeIMS = "application/3gpp-ims+xml"

	emergencyReason = "Emergency calls are not supported on this network"
)

// emergencyURN reports whether u is an emergency service URN: a service URN with a top-level service type of
// "sos", compared without regard to case (RFC 5031 §3, §4.2).
func emergencyURN(u sip.URI) bool {
	if !strings.EqualFold(u.Scheme, "urn") {
		return false
	}

	nss := strings.ToLower(u.Opaque)

	return nss == "service:sos" || strings.HasPrefix(nss, "service:sos.")
}

// alternativeService rejects an emergency request, as the IMS does not handle emergency sessions, so that the UE
// attempts the emergency call another way (TS 24.229 §5.2.10.5). The UE acts on it when its P-Asserted-Identity is
// the P-CSCF's URI in the Path of the registration flow it came on (§5.1.3.1, §5.1.6.8.4).
func (p *PCSCF) alternativeService(req *sip.Request, reg db.PCSCFRegistration) *sip.Response {
	res := sip.NewResponse(req, 380, "")
	res.Header.Set("P-Asserted-Identity", "<"+pathURI(reg.FlowToken, reg.PCSCFAddress, p.cfg.Port).String()+">")
	res.SetBody(contentTypeIMS, []byte(`<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
		`<ims-3gpp version="1"><alternative-service><type>emergency</type><reason>`+emergencyReason+
		"</reason></alternative-service></ims-3gpp>\n"))

	return res
}
