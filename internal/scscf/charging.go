package scscf

import (
	"strings"

	"github.com/ellanetworks/ims/sip"
)

const pChargingVector = "P-Charging-Vector"

// chargingVector is a P-Charging-Vector with icid, and an "orig-ioi" that identifies the S-CSCF's network
// (RFC 7315 §5.6): the IOI of a request the S-CSCF sends, of type 3 towards an AS and of type 1 towards a UE
// (TS 24.229 §5.4.1.7 h, §5.4.2.1.2 step 5).
func (r *Registrar) chargingVector(icid string) string {
	return "icid-value=" + icid + ";orig-ioi=" + r.cfg.HomeDomain
}

// icidOf is the icid-value of the P-Charging-Vector of h, if any.
func icidOf(h sip.Header) string {
	ps, err := sip.ParseParams(h.Get(pChargingVector))
	if err != nil {
		return ""
	}

	icid, _ := ps.Get("icid-value")

	return icid
}

// termIOIOf is the "term-ioi" of the P-Charging-Vector of a response, if any.
func termIOIOf(res *sip.Response) string {
	ps, err := sip.ParseParams(res.Header.Get(pChargingVector))
	if err != nil {
		return ""
	}

	ioi, _ := ps.Get("term-ioi")

	return ioi
}

// toASCharging sets the P-Charging-Vector of a request the S-CSCF sends to an AS: the one received, with a type 3
// "orig-ioi" that identifies the S-CSCF's network in place of any received, and no "term-ioi" nor "transit-ioi"
// (TS 24.229 §5.4.3.2 step 5 d and e, §5.4.3.3 step 4 d and f). The AS is in the trust domain: the
// access-network-charging-info stays (§5.4.3.2 step 5 c). A request without an icid-value has no P-Charging-Vector.
func (r *Registrar) toASCharging(h *sip.Header) {
	ps, err := sip.ParseParams(h.Get(pChargingVector))
	h.Del(pChargingVector)

	if icid, _ := ps.Get("icid-value"); err != nil || icid == "" {
		return
	}

	ps.Del("term-ioi")
	ps.Del("transit-ioi")
	ps.Set("orig-ioi", r.cfg.HomeDomain)

	h.Add(pChargingVector, strings.TrimPrefix(ps.String(), ";"))
}
