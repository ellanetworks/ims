package scscf

import (
	"testing"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/sip"
)

// The trigger point of TS 29.228 Annex C, in CNF and in DNF: Method = "INVITE" OR Method = "MESSAGE" OR
// (Method = "SUBSCRIBE" AND NOT Header = "from" Content = "joe").
const (
	annexCCNF = `<?xml version="1.0" encoding="UTF-8"?>
<IMSSubscription xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
xsi:noNamespaceSchemaLocation="CxDataType.xsd">
        <PrivateID>IMPI1@homedomain.com</PrivateID>
        <ServiceProfile>
         <PublicIdentity>
         <BarringIndication>1</BarringIndication>
         <Identity> sip:IMPU1@homedomain.com </Identity>
         </PublicIdentity>
         <PublicIdentity>
         <Identity> sip:IMPU2@homedomain.com </Identity>
         </PublicIdentity>
         <InitialFilterCriteria>
         <Priority>0</Priority>
         <TriggerPoint>
             <ConditionTypeCNF>1</ConditionTypeCNF>
             <SPT>
             <ConditionNegated>0</ConditionNegated>
             <Group>0</Group>
             <Method>INVITE</Method>
             </SPT>
              <SPT>
              <ConditionNegated>0</ConditionNegated>
              <Group>0</Group>
              <Method>MESSAGE</Method>
              </SPT>
              <SPT>
              <ConditionNegated>0</ConditionNegated>
              <Group>0</Group>
              <Method>SUBSCRIBE</Method>
              </SPT>
              <SPT>
              <ConditionNegated>0</ConditionNegated>
              <Group>1</Group>
              <Method>INVITE</Method>
              </SPT>
              <SPT>
              <ConditionNegated>0</ConditionNegated>
              <Group>1</Group>
              <Method>MESSAGE</Method>
              </SPT>
            <SPT>
            <ConditionNegated>1</ConditionNegated>
            <Group>1</Group>
            <SIPHeader>
                 <Header>From</Header>
                 <Content>"joe"</Content>
            </SIPHeader>
            </SPT>
         </TriggerPoint>
         <ApplicationServer>
            <ServerName>sip:AS1@homedomain.com</ServerName>
            <DefaultHandling>0</DefaultHandling>
         </ApplicationServer>
         </InitialFilterCriteria>
       </ServiceProfile>
</IMSSubscription>`

	annexCDNF = `<?xml version="1.0" encoding="UTF-8"?>
<IMSSubscription xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
xsi:noNamespaceSchemaLocation="CxDataType.xsd">
        <PrivateID>IMPI1@homedomain.com</PrivateID>
        <ServiceProfile>
         <PublicIdentity>
         <BarringIndication>1</BarringIndication>
         <Identity> sip:IMPU1@homedomain.com </Identity>
         </PublicIdentity>
         <PublicIdentity>
         <Identity> sip:IMPU2@homedomain.com </Identity>
         </PublicIdentity>
         <InitialFilterCriteria>
         <Priority>0</Priority>
         <TriggerPoint>
            <ConditionTypeCNF>0</ConditionTypeCNF>
            <SPT>
            <ConditionNegated>0</ConditionNegated>
            <Group>0</Group>
            <Method>INVITE</Method>
            </SPT>
            <SPT>
            <ConditionNegated>0</ConditionNegated>
            <Group>1</Group>
            <Method>MESSAGE</Method>
            </SPT>
            <SPT>
            <ConditionNegated>0</ConditionNegated>
            <Group>2</Group>
            <Method>SUBSCRIBE</Method>
            </SPT>
            <SPT>
            <ConditionNegated>1</ConditionNegated>
            <Group>2</Group>
            <SIPHeader>
                 <Header>From</Header>
                 <Content>"joe"</Content>
            </SIPHeader>
            </SPT>
         </TriggerPoint>
         <ApplicationServer>
            <ServerName>sip:AS1@homedomain.com</ServerName>
            <DefaultHandling index="0">0</DefaultHandling>
         </ApplicationServer>
         </InitialFilterCriteria>
       </ServiceProfile>
</IMSSubscription>`
)

func annexCRequest(method, from string) *sip.Request {
	req := sip.NewRequest(method, sip.URI{Scheme: "sip", User: "IMPU2", Host: "homedomain.com"})
	req.Header = nil
	req.Header.Add("From", from+";tag=1")
	req.Header.Add("To", "<sip:IMPU2@homedomain.com>")

	return req
}

// TS 29.228 Annex C
func TestAnnexCTriggerPoint(t *testing.T) {
	tests := []struct {
		method, from string
		want         bool
	}{
		{"INVITE", `"joe" <sip:joe@homedomain.com>`, true},
		{"MESSAGE", `"joe" <sip:joe@homedomain.com>`, true},
		{"SUBSCRIBE", `"ann" <sip:ann@homedomain.com>`, true},
		{"SUBSCRIBE", `"joe" <sip:joe@homedomain.com>`, false},
		{"OPTIONS", `"ann" <sip:ann@homedomain.com>`, false},
		{"invite", `"ann" <sip:ann@homedomain.com>`, false},
	}

	for _, form := range []struct{ name, xml string }{{"CNF", annexCCNF}, {"DNF", annexCDNF}} {
		sub, err := cx.ParseUserData([]byte(form.xml))
		if err != nil {
			t.Fatalf("%s: %v", form.name, err)
		}

		p, ok := serviceProfile(sub, sip.URI{Scheme: "sip", User: "IMPU2", Host: "homedomain.com"}, "homedomain.com")
		if !ok {
			t.Fatalf("%s: no service profile for IMPU2", form.name)
		}

		ifcs := filterCriteria(p, true)
		if len(ifcs) != 1 || ifcs[0].ApplicationServer.ServerName != "sip:AS1@homedomain.com" {
			t.Fatalf("%s: iFCs %+v, want the one of AS1", form.name, ifcs)
		}

		for _, tt := range tests {
			tr := trigger{req: annexCRequest(tt.method, tt.from), sescase: cx.SessionCaseOriginatingRegistered}

			if got := tr.matches(ifcs[0].Trigger); got != tt.want {
				t.Errorf("%s: %s from %s matches = %v, want %v", form.name, tt.method, tt.from, got, tt.want)
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

// TS 29.228 Annex B.2.3, Annex F
func TestServicePointTriggers(t *testing.T) {
	invite := annexCRequest("INVITE", "<sip:ann@homedomain.com>")
	invite.URI = sip.URI{Scheme: "sip", User: "conf", Host: "mcptt.homedomain.com", Port: 5062}
	invite.Header.Add("Accept-Contact", `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt";require`)
	invite.Header.Add("P-Asserted-Service", "urn:urn-7:3gpp-service.ims.icsi.mcptt")
	invite.Header.Add("Supported", "timer,  100rel")
	invite.SetBody("application/sdp", []byte("v=0\r\nm=audio 4000 RTP/AVP 0\r\nm=application 4002 udp MCPTT\r\n"))

	tel := annexCRequest("INVITE", "<sip:ann@homedomain.com>")
	tel.URI = sip.URI{Scheme: "tel", User: "+15550001"}

	register := annexCRequest("REGISTER", "<sip:ann@homedomain.com>")

	tests := []struct {
		name string
		tr   trigger
		spt  cx.ServicePointTrigger
		want bool
	}{
		{"hostport of a SIP URI", trigger{req: invite}, cx.ServicePointTrigger{RequestURI: ptr(`^mcptt\.homedomain\.com:5062$`)}, true},
		{"not the user part", trigger{req: invite}, cx.ServicePointTrigger{RequestURI: ptr("conf")}, false},
		{"telephone-subscriber", trigger{req: tel}, cx.ServicePointTrigger{RequestURI: ptr(`^\+1555`)}, true},
		{"header present", trigger{req: invite}, cx.ServicePointTrigger{SIPHeader: &cx.HeaderMatch{Header: "accept-contact"}}, true},
		{"header absent", trigger{req: invite}, cx.ServicePointTrigger{Negated: true, SIPHeader: &cx.HeaderMatch{Header: "^Privacy$"}}, true},
		{"header content", trigger{req: invite}, cx.ServicePointTrigger{
			SIPHeader: &cx.HeaderMatch{Header: "P-Asserted-Service", Content: ptr(`icsi\.mcptt$`)},
		}, true},
		{"each value of a list", trigger{req: invite}, cx.ServicePointTrigger{
			SIPHeader: &cx.HeaderMatch{Header: "Supported", Content: ptr(`^100rel$`)},
		}, true},
		{"session case", trigger{req: invite, sescase: cx.SessionCaseTerminatingUnregistered}, cx.ServicePointTrigger{
			SessionCase: ptr(cx.SessionCaseTerminatingUnregistered),
		}, true},
		{"other session case", trigger{req: invite, sescase: cx.SessionCaseOriginatingRegistered}, cx.ServicePointTrigger{
			SessionCase: ptr(cx.SessionCaseTerminatingRegistered),
		}, false},
		{"session description", trigger{req: invite}, cx.ServicePointTrigger{
			SessionDescription: &cx.SessionDescriptionMatch{Line: "^m$", Content: ptr("MCPTT")},
		}, true},
		{"no such media", trigger{req: invite}, cx.ServicePointTrigger{
			SessionDescription: &cx.SessionDescriptionMatch{Line: "^m$", Content: ptr("^video")},
		}, false},
		{"registration type", trigger{req: register, regType: cx.RegistrationTypeReRegistration}, cx.ServicePointTrigger{
			Method: ptr("REGISTER"), RegistrationTypes: []cx.RegistrationType{cx.RegistrationTypeInitial, cx.RegistrationTypeReRegistration},
		}, true},
		{"other registration type", trigger{req: register, regType: cx.RegistrationTypeDeregistration}, cx.ServicePointTrigger{
			Method: ptr("REGISTER"), RegistrationTypes: []cx.RegistrationType{cx.RegistrationTypeInitial},
		}, false},
		{
			"any registration type",
			trigger{req: register, regType: cx.RegistrationTypeDeregistration},
			cx.ServicePointTrigger{Method: ptr("REGISTER")},
			true,
		},
		{"invalid expression", trigger{req: invite}, cx.ServicePointTrigger{RequestURI: ptr("(")}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.spt.Groups = []int32{0}

			if got := tt.tr.matches(&cx.TriggerPoint{ServicePointTriggers: []cx.ServicePointTrigger{tt.spt}}); got != tt.want {
				t.Fatalf("matches = %v, want %v", got, tt.want)
			}
		})
	}
}

// TS 29.228 Annex B.2.2: an SPT in two groups, and the profile parts.
func TestFilterCriteria(t *testing.T) {
	invite := annexCRequest("INVITE", "<sip:ann@homedomain.com>")

	// (A + B).(A + C), A true: met in CNF; A.B + A.C, B and C false: not met in DNF.
	spts := []cx.ServicePointTrigger{
		{Groups: []int32{0, 1}, Method: ptr("INVITE")},
		{Groups: []int32{0}, Method: ptr("MESSAGE")},
		{Groups: []int32{1}, Method: ptr("SUBSCRIBE")},
	}

	tr := trigger{req: invite}

	if !tr.matches(&cx.TriggerPoint{ConjunctiveNormalForm: true, ServicePointTriggers: spts}) {
		t.Error("CNF not met")
	}

	if tr.matches(&cx.TriggerPoint{ServicePointTriggers: spts}) {
		t.Error("DNF met")
	}

	if !tr.matches(nil) {
		t.Error("no trigger point not met")
	}

	p := cx.ServiceProfile{InitialFilterCriteria: []cx.InitialFilterCriteria{
		{Priority: 30, ApplicationServer: cx.ApplicationServer{ServerName: "sip:common@as"}},
		{Priority: 10, ProfilePart: ptr(cx.ProfilePartUnregistered), ApplicationServer: cx.ApplicationServer{ServerName: "sip:unreg@as"}},
		{Priority: 20, ProfilePart: ptr(cx.ProfilePartRegistered), ApplicationServer: cx.ApplicationServer{ServerName: "sip:reg@as"}},
	}}

	names := func(cs []cx.InitialFilterCriteria) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.ApplicationServer.ServerName)
		}

		return out
	}

	if got := names(filterCriteria(p, true)); len(got) != 2 || got[0] != "sip:reg@as" || got[1] != "sip:common@as" {
		t.Errorf("registered iFCs %v, want reg then common", got)
	}

	if got := names(filterCriteria(p, false)); len(got) != 2 || got[0] != "sip:unreg@as" || got[1] != "sip:common@as" {
		t.Errorf("unregistered iFCs %v, want unreg then common", got)
	}
}

// TS 23.003 §13.5
func TestWildcardedPSI(t *testing.T) {
	sub := cx.IMSSubscription{PrivateIdentity: "psi@homedomain.com", ServiceProfiles: []cx.ServiceProfile{
		{PublicIdentities: []cx.ProfileIdentity{{Identity: "sip:chatlist!.*!@example.com", Type: cx.IdentityWildcardedPSI}}},
	}}

	for _, tt := range []struct {
		uri  string
		want bool
	}{
		{"sip:chatlist1@example.com", true},
		{"sip:chatlist!1@example.com", true},
		{"sip:chatlistAbC@example.com", true},
		{"sip:chat@example.com", false},
		{"sip:chatlist1@example.org", false},
	} {
		u, err := sip.ParseURI(tt.uri)
		if err != nil {
			t.Fatal(err)
		}

		if _, got := serviceProfile(sub, u, "homedomain.com"); got != tt.want {
			t.Errorf("%s matches = %v, want %v", tt.uri, got, tt.want)
		}
	}
}
