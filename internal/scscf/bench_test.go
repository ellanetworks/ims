package scscf

import (
	"log/slog"
	"testing"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/sip"
)

// benchSubscription is the subscription of an MCPTT user with MMTel and presence: its IMPU, MSISDN and tel URI in
// one service profile, and the iFCs of an MCPTT participating server, a TAS, a presence server and a third-party
// registration (TS 24.379 §4.2, TS 24.229 §5.4.3.2).
func benchSubscription() cx.IMSSubscription {
	originating := cx.SessionCaseOriginatingRegistered
	registered := cx.ProfilePartRegistered
	continued := cx.DefaultHandlingSessionContinued

	method := func(m string, groups ...int32) cx.ServicePointTrigger {
		return cx.ServicePointTrigger{Groups: groups, Method: &m}
	}
	header := func(h, content string, groups ...int32) cx.ServicePointTrigger {
		return cx.ServicePointTrigger{Groups: groups, SIPHeader: &cx.HeaderMatch{Header: h, Content: &content}}
	}
	sescase := func(c cx.SessionCase, groups ...int32) cx.ServicePointTrigger {
		return cx.ServicePointTrigger{Groups: groups, SessionCase: &c}
	}
	ifc := func(priority int32, as string, spts ...cx.ServicePointTrigger) cx.InitialFilterCriteria {
		return cx.InitialFilterCriteria{
			Priority:          priority,
			Trigger:           &cx.TriggerPoint{ConjunctiveNormalForm: true, ServicePointTriggers: spts},
			ApplicationServer: cx.ApplicationServer{ServerName: "sip:" + as + "." + homeDomain, DefaultHandling: &continued},
			ProfilePart:       &registered,
		}
	}

	register := ifc(0, "mcptt", method("REGISTER", 0))
	register.ApplicationServer.IncludeRegisterRequest = true

	return cx.IMSSubscription{
		PrivateIdentity: testIMPI,
		ServiceProfiles: []cx.ServiceProfile{{
			PublicIdentities: []cx.ProfileIdentity{
				{Identity: testIMPU, Barred: true},
				{Identity: testMSISDN, DisplayName: "Alice"},
				{Identity: testTel},
			},
			InitialFilterCriteria: []cx.InitialFilterCriteria{
				register,
				ifc(1, "presence", method("PUBLISH", 0), method("SUBSCRIBE", 0), header("Event", "^presence", 1)),
				ifc(2, "mcptt", method("SUBSCRIBE", 0), header("Event", "^conference", 1),
					header("Accept-Contact", `icsi\.mcptt`, 2)),
				ifc(3, "mcptt", method("MESSAGE", 0), header("P-Preferred-Service", `icsi\.mcptt`, 1)),
				ifc(4, "mcptt", method("INVITE", 0), sescase(originating, 1), header("Accept-Contact", `icsi\.mcptt`, 2)),
				ifc(5, "tas", method("INVITE", 0), header("P-Preferred-Service", `icsi\.mmtel`, 1),
					cx.ServicePointTrigger{Groups: []int32{2}, SessionDescription: &cx.SessionDescriptionMatch{Line: "^m$", Content: ptr("^audio")}}),
			},
		}},
	}
}

// benchInvite is an originating MCPTT INVITE as the S-CSCF gets it from the P-CSCF.
func benchInvite() *sip.Request {
	req := sip.NewRequest("INVITE", sip.URI{Scheme: "sip", User: "mcptt-orig-part-server", Host: "mcptt." + homeDomain})
	req.Header = nil

	for _, f := range [][2]string{
		{"Via", "SIP/2.0/UDP pcscf." + homeDomain + ":5060;branch=z9hG4bK-pcscf"},
		{"Via", "SIP/2.0/UDP 10.45.0.2:5060;rport=5060;received=10.45.0.2;branch=z9hG4bK-ue"},
		{"Max-Forwards", "69"},
		{"Record-Route", "<sip:pcscf." + homeDomain + ";lr>"},
		{"From", `"Alice" <` + testMSISDN + ">;tag=1"},
		{"To", "<sip:mcptt-orig-part-server@mcptt." + homeDomain + ">"},
		{"Call-ID", "bench@10.45.0.2"},
		{"CSeq", "1 INVITE"},
		{"Contact", `<sip:alice@10.45.0.2:5060>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt"`},
		{"Accept-Contact", `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt";require;explicit`},
		{"P-Asserted-Identity", "<" + testMSISDN + ">"},
		{"P-Preferred-Service", "urn:urn-7:3gpp-service.ims.icsi.mcptt"},
		{"P-Charging-Vector", "icid-value=1234bc9876e;orig-ioi=" + homeDomain},
		{"Supported", "timer, 100rel, gruu"},
		{"Allow", "INVITE, ACK, CANCEL, BYE, UPDATE, PRACK, MESSAGE, OPTIONS, INFO, NOTIFY, REFER"},
		{"User-Agent", "Bench UE 1.0"},
		{"Session-Expires", "1800"},
	} {
		req.Header.Add(f[0], f[1])
	}

	req.SetBody("application/sdp", []byte("v=0\r\no=- 1 1 IN IP4 10.45.0.2\r\ns=-\r\nc=IN IP4 10.45.0.2\r\nt=0 0\r\n"+
		"m=audio 4000 RTP/AVP 96\r\na=rtpmap:96 AMR-WB/16000\r\nm=application 4002 udp MCPTT\r\na=fmtp:MCPTT mc_queueing\r\n"))

	return req
}

// BenchmarkTriggerPoints: an INVITE checked against each iFC of an MCPTT user's profile, as the chain of the
// S-CSCF checks them over its returns from the ASs (TS 29.228 Annex B.2.3).
func BenchmarkTriggerPoints(b *testing.B) {
	p := benchSubscription().ServiceProfiles[0]
	ifcs := filterCriteria(p, true)
	t := trigger{req: benchInvite(), sescase: cx.SessionCaseOriginatingRegistered}

	b.ReportAllocs()

	for b.Loop() {
		var matched []int32

		for _, c := range ifcs {
			if t.matches(c.Trigger) {
				matched = append(matched, c.Priority)
			}
		}

		// The MCPTT participating server alone: the TAS is for MMTel.
		if len(matched) != 1 || matched[0] != 4 {
			b.Fatalf("iFCs %v matched, want 4", matched)
		}
	}

	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(ifcs)), "ns/ifc")
}

// BenchmarkOriginatingChain: what the S-CSCF does with the user data of a registration for each originating
// request: the alias of P-Asserted-Identity, the service profile of the served user, its iFCs, and the trigger
// points checked (TS 24.229 §5.4.3.2).
func BenchmarkOriginatingChain(b *testing.B) {
	userData, err := cx.MarshalUserData(benchSubscription())
	if err != nil {
		b.Fatal(err)
	}

	r := &Registrar{cfg: Config{HomeDomain: homeDomain}, log: slog.New(slog.DiscardHandler)}
	req := benchInvite()

	u, err := sip.ParseURI(testMSISDN)
	if err != nil {
		b.Fatal(err)
	}

	served := sip.Address{URI: u}

	b.Run("parse", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(userData)))

		for b.Loop() {
			if _, err := cx.ParseUserData(userData); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("chain", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			sub, err := cx.ParseUserData(userData)
			if err != nil {
				b.Fatal(err)
			}

			if _, ok := assertedAlias(served, sub, homeDomain); !ok {
				b.Fatal("no alias")
			}

			p, ok := serviceProfile(sub, served.URI, homeDomain)
			if !ok {
				b.Fatal("no service profile")
			}

			ifcs := r.criteria(p, true)
			t := trigger{req: req, sescase: cx.SessionCaseOriginatingRegistered}

			for _, c := range ifcs {
				t.matches(c.Trigger)
			}
		}
	})
}
