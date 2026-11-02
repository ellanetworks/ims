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

// RFC 5031 section 3
// RFC 5031 section 4.2
func emergencyURN(u sip.URI) bool {
	if !strings.EqualFold(u.Scheme, "urn") {
		return false
	}

	nss := strings.ToLower(u.Opaque)

	return nss == "service:sos" || strings.HasPrefix(nss, "service:sos.")
}

// TS 24.229 clause 5.2.10.5
// TS 24.229 clause 5.1.3.1
// TS 24.229 clause 5.1.6.8.4
func (p *PCSCF) alternativeService(req *sip.Request, reg db.PCSCFRegistration) *sip.Response {
	res := sip.NewResponse(req, 380, "")
	res.Header.Set("P-Asserted-Identity", "<"+pathURI(reg.FlowToken, reg.PCSCFAddress, p.cfg.Port).String()+">")
	res.SetBody(contentTypeIMS, []byte(`<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
		`<ims-3gpp version="1"><alternative-service><type>emergency</type><reason>`+emergencyReason+
		"</reason></alternative-service></ims-3gpp>\n"))

	return res
}
