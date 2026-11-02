package scscf

import (
	"strings"

	"github.com/ellanetworks/ims/sip"
)

const pChargingVector = "P-Charging-Vector"

// RFC 7315 section 5.6
// TS 24.229 clause 5.4.1.7
// TS 24.229 clause 5.4.2.1.2
func (r *Registrar) chargingVector(icid string) string {
	return "icid-value=" + icid + ";orig-ioi=" + r.cfg.HomeDomain
}

func icidOf(h sip.Header) string {
	ps, err := sip.ParseParams(h.Get(pChargingVector))
	if err != nil {
		return ""
	}

	icid, _ := ps.Get("icid-value")

	return icid
}

func termIOIOf(res *sip.Response) string {
	ps, err := sip.ParseParams(res.Header.Get(pChargingVector))
	if err != nil {
		return ""
	}

	ioi, _ := ps.Get("term-ioi")

	return ioi
}

// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.3
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
