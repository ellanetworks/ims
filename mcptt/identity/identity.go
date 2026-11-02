package identity

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/ellanetworks/ims/sip"
)

const (
	// TS 24.379 annex E
	ICSI = "urn:urn-7:3gpp-service.ims.icsi.mcptt"

	// TS 24.379 clause D.2
	FeatureTag = "+g.3gpp.mcptt"

	// TS 24.229 clause 7.9.2
	ICSITag = "+g.3gpp.icsi-ref"

	// RFC 3840 section 9
	FocusTag = "sip.isfocus"
)

func Host(homeDomain string) string { return "mcptt." + homeDomain }

func ParticipatingPSI(homeDomain string) string { return "sip:mcptt-participating@" + Host(homeDomain) }

func ControllingPSI(homeDomain string) string { return "sip:mcptt-controlling@" + Host(homeDomain) }

// TS 24.379 clause 4.4.2
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

// TS 24.379 clause 6.3.2.1.5
// TS 24.379 clause 6.3.3.2.3
func Features(focus bool) sip.FeatureSet {
	fs := sip.FeatureSet{sip.NewFeature(FeatureTag), sip.NewFeature(ICSITag, ICSI)}
	if focus {
		fs = append(fs, sip.NewFeature(FocusTag))
	}

	return fs
}

// TS 24.229 clause 7.9A.2
func ServiceCaps() string {
	v, _ := sip.FeatureCapsValue(sip.FeatureSet{sip.NewFeature(ICSITag, ICSI)})

	return v
}

func WithFeatures(a sip.Address, fs sip.FeatureSet) sip.Address {
	a = a.Clone()

	have, _ := a.Features()
	for _, f := range fs {
		if have.Has(f.Tag) {
			continue
		}

		p, _ := f.Param()
		a.Params = append(a.Params, p)
	}

	return a
}

// TS 24.379 clause 4.4.1
func Warning(host, text string) sip.Field {
	return sip.Field{Name: "Warning", Value: sip.Warning{Code: sip.WarnMiscellaneous, Agent: host, Text: text}.String()}
}

// TS 24.229 clause 5.7.3
func ChargingVector(ioi string) string {
	var b [16]byte

	_, _ = rand.Read(b[:])

	return Charging{ICID: hex.EncodeToString(b[:])}.Request(ioi)
}

// TS 24.229 clause 7.2A.5
type Charging struct{ ICID, OrigIOI string }

func ChargingOf(h sip.Header) Charging {
	var c Charging

	if ps, err := sip.ParseParams(h.Get("P-Charging-Vector")); err == nil {
		c.ICID, _ = ps.Get("icid-value")
		c.OrigIOI, _ = ps.Get("orig-ioi")
	}

	return c
}

// TS 24.229 clause 5.7.2
// TS 24.229 clause 5.7.3
func (c Charging) Request(ioi string) string {
	return "icid-value=" + c.ICID + ";orig-ioi=" + ioi
}

// TS 24.229 clause 5.7.2
// TS 24.229 clause 5.7.5.1
func (c Charging) Response(ioi string) string {
	v := "icid-value=" + c.ICID
	if c.OrigIOI != "" {
		v += ";orig-ioi=" + c.OrigIOI
	}

	return v + ";term-ioi=" + ioi
}
