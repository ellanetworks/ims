package scscf

import (
	"strings"

	"github.com/ellanetworks/ims/internal/bgcf"
	"github.com/ellanetworks/ims/sip"
)

// TS 24.229 clause 5.3.2.1
// TS 24.229 clause 5.4.3.2
func breakout(out *sip.Request, onNet bool) bool {
	number, global := globalNumber(out.URI)

	return global && bgcf.E164(number[1:]) && !onNet && out.Method == "INVITE"
}

// TS 24.229 clause 5.4.3.2
func withoutAccessNetworkChargingInfo(h *sip.Header) {
	ps, err := sip.ParseParams(h.Get("P-Charging-Vector"))
	if err != nil || !ps.Has("access-network-charging-info") {
		return
	}

	ps.Del("access-network-charging-info")
	h.Set("P-Charging-Vector", strings.TrimPrefix(ps.String(), ";"))
}
