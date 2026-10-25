// Package identity holds the identifiers of MCPTT (TS 24.379) its functions and clients share.
package identity

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/ellanetworks/ims/sip"
)

const (
	// ICSI is the IMS communication service identifier of MCPTT (TS 24.379 Annex E).
	ICSI = "urn:urn-7:3gpp-service.ims.icsi.mcptt"

	// FeatureTag is the media feature tag of MCPTT (TS 24.379 Annex D.2), as a Contact parameter.
	FeatureTag = "+g.3gpp.mcptt"

	// ICSITag is the media feature tag of the ICSI (TS 24.229 §7.9.2), as a Contact parameter.
	ICSITag = "+g.3gpp.icsi-ref"

	// FocusTag is the media feature tag of a conference focus (RFC 3840 §9), "isfocus" as a parameter, in the Contact of the controlling
	// function (TS 24.379 §6.3.3.1.2 step 1, §6.3.3.2.3.2 step 6c).
	FocusTag = "sip.isfocus"
)

// Host is the host name of the MCPTT AS built into Ella IMS, of the IMS's home domain: the name of the AS in the iFCs,
// and the host of its PSIs (TS 24.379 Annex H) and of the session identities of its calls (§4.5).
func Host(homeDomain string) string { return "mcptt." + homeDomain }

// ParticipatingPSI and ControllingPSI are the PSIs of the functions of the MCPTT AS built into Ella IMS: fixed, of its
// host.
func ParticipatingPSI(homeDomain string) string { return "sip:mcptt-participating@" + Host(homeDomain) }

func ControllingPSI(homeDomain string) string { return "sip:mcptt-controlling@" + Host(homeDomain) }

// Warning texts of TS 24.379 Table 4.4.2-2.
const (
	WarnServiceAuthorisationFailed = "101 service authorisation failed"
	WarnTooManyAffiliations        = "102 too many simultaneous affiliations"
	WarnTooManyCalls               = "103 maximum simultaneous MCPTT group calls reached"
	WarnFocusNotAssigned           = "104 isfocus not assigned"
	WarnUserDeclined               = "110 user declined the call invitation"
	WarnProceededWithoutRequired   = "111 group call proceeded without all required group members"
	WarnAbandonedRequired          = "112 group call abandoned due to required group members not part of the group session"
	WarnNotAuthorisedToInitiate    = "119 user is not authorised to initiate the group call"
	WarnNotAffiliated              = "120 user is not affiliated to this group"
	WarnNotAuthorisedToJoin        = "121 user is not authorised to join the group call"
	WarnTooManyParticipants        = "122 too many participants"
	WarnSessionExists              = "123 MCPTT session already exists"
	WarnIntegrityCheckFailed       = "139 integrity protection check failed"
	WarnUnableToDecrypt            = "140 unable to decrypt XML content"
	WarnUserUnknown                = "141 user unknown to the participating function"
	WarnNoControlling              = "142 unable to determine the controlling function"
)

// Features are the feature tags of the Contact of the MCPTT functions (TS 24.379 §6.3.2.1.5, §6.3.3.2.3): MCPTT and
// its ICSI, and, of the controlling function, isfocus.
func Features(focus bool) sip.FeatureSet {
	fs := sip.FeatureSet{sip.NewFeature(FeatureTag), sip.NewFeature(ICSITag, ICSI)}
	if focus {
		fs = append(fs, sip.NewFeature(FocusTag))
	}

	return fs
}

// ServiceCaps is the Feature-Caps value of the ICSI (TS 24.229 §7.9A.2), which an AS may insert in an 18x or 2xx
// response it sends on behalf of an originating user (§5.7.1.9): the originating P-CSCF finds there the IMS
// communication service of the dialog (§5.2.6.3.4 NOTE 2), the ICSI it gives the PCRF or the PCF (TS 29.214 Annex
// A.1 NOTE 3, TS 29.514 Annex B.1 NOTE 3), which identifies an MCPTT call by it (TS 29.514 Annex B.10.1 NOTE 3).
func ServiceCaps() string {
	// The term of NewFeature encodes.
	v, _ := sip.FeatureCapsValue(sip.FeatureSet{sip.NewFeature(ICSITag, ICSI)})

	return v
}

// WithFeatures is a with the feature tags fs it lacks, as Contact parameters.
func WithFeatures(a sip.Address, fs sip.FeatureSet) sip.Address {
	a = a.Clone()

	have, _ := a.Features()
	for _, f := range fs {
		if have.Has(f.Tag) {
			continue
		}

		// Terms of NewFeature encode.
		p, _ := f.Param()
		a.Params = append(a.Params, p)
	}

	return a
}

// Warning is a Warning header field of a warning text (TS 24.379 §4.4.1): code 399, of the host name of the server.
func Warning(host, text string) sip.Field {
	return sip.Field{Name: "Warning", Value: sip.Warning{Code: sip.WarnMiscellaneous, Agent: host, Text: text}.String()}
}

// ChargingVector is the P-Charging-Vector of an initial request or a standalone request the server originates as an
// application server (TS 24.229 §5.7.3): a new icid-value, globally unique (TS 32.260), and the type 3 orig-ioi ioi,
// the identifier of the service provider, without a term-ioi.
func ChargingVector(ioi string) string {
	var b [16]byte

	_, _ = rand.Read(b[:])

	return Charging{ICID: hex.EncodeToString(b[:])}.Request(ioi)
}

// Charging are the icid-value and orig-ioi of a P-Charging-Vector (TS 24.229 §7.2A.5), "" if absent.
type Charging struct{ ICID, OrigIOI string }

// ChargingOf is the Charging of the P-Charging-Vector of h.
func ChargingOf(h sip.Header) Charging {
	var c Charging

	if ps, err := sip.ParseParams(h.Get("P-Charging-Vector")); err == nil {
		c.ICID, _ = ps.Get("icid-value")
		c.OrigIOI, _ = ps.Get("orig-ioi")
	}

	return c
}

// Request is the P-Charging-Vector of a request of the server of icid-value c.ICID (TS 24.229 §5.7.2, §5.7.3: of a
// subsequent request, that of the initial request of the dialog): the type 3 orig-ioi ioi, without a term-ioi.
func (c Charging) Request(ioi string) string {
	return "icid-value=" + c.ICID + ";orig-ioi=" + ioi
}

// Response is the P-Charging-Vector of a response of the server to a request of c (TS 24.229 §5.7.2, §5.7.5.1): its
// icid-value, its orig-ioi if any, and the type 3 term-ioi ioi.
func (c Charging) Response(ioi string) string {
	v := "icid-value=" + c.ICID
	if c.OrigIOI != "" {
		v += ";orig-ioi=" + c.OrigIOI
	}

	return v + ";term-ioi=" + ioi
}
