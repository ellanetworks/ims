package testue

import (
	"slices"
	"strings"
	"sync"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/ue"
)

// TS 24.173 clause 5.1
const ICSIMMTel = "urn:urn-7:3gpp-service.ims.icsi.mmtel"

type MMTelConfig struct {
	Video bool

	AcceptCalls bool

	NoPreconditions bool

	EarlyMedia string
}

type MMTel struct {
	u   *UE
	cfg MMTelConfig

	incoming chan *Call

	mu   sync.Mutex
	next int
}

func newMMTel(u *UE, cfg MMTelConfig) *MMTel {
	return &MMTel{u: u, cfg: cfg, incoming: make(chan *Call, 16)}
}

// TS 24.229 clause 5.1.3.1
var mmtelAllow = []string{"INVITE", "UPDATE", "PRACK"}

const (
	MMTelService = "mmtel"
	SMSService   = "sms"
)

func (m *MMTel) service() ue.Service {
	s := ue.Service{
		Name:          MMTelService,
		ICSI:          ICSIMMTel,
		Features:      m.mediaTags(),
		AcceptContact: []sip.CallerPreference{{Features: sip.FeatureSet{sip.NewFeature("+g.3gpp.icsi-ref", ICSIMMTel)}}},
		Allow:         mmtelAllow,
		Match:         isCall,
		OnInvite:      m.incomingCall,
	}

	if !m.cfg.NoPreconditions {
		s.Understood = []string{sip.OptionTagPrecondition}
	}

	return s
}

// TS 24.229 clause 5.1.2A.2
func isCall(req *sip.Request) bool {
	if req.Method != "INVITE" {
		return false
	}

	body, ok := offerOf(req)
	if !ok {
		return true
	}

	s, err := sdp.Parse(body)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(s.Media, func(m *sdp.Media) bool { return m.Type() == sdp.Audio || m.Type() == sdp.Video })
}

// RFC 3840 section 9
// TS 24.229 clause 5.1.3.1
// GSMA IR.94 section 2.2.1
// GSMA IR.94 section 2.2.2
func (m *MMTel) mediaTags() sip.FeatureSet {
	if m.cfg.Video {
		return sip.FeatureSet{sip.NewFeature("audio"), sip.NewFeature("video")}
	}

	return sip.FeatureSet{sip.NewFeature("audio")}
}

func (m *MMTel) UE() *UE { return m.u }

func (m *MMTel) Calls() <-chan *Call { return m.incoming }

// TS 24.229 clause 5.1.2A.1.1
func (m *MMTel) callContact() string {
	c, _ := m.u.core().Contact(MMTelService)
	return c.String()
}

// GSMA IR.92 section 2.2.4
// GSMA IR.94 section 2.2.2
// RFC 3841 section 9.2
func acceptContact(video bool) string {
	fs := sip.FeatureSet{sip.NewFeature("+g.3gpp.icsi-ref", ICSIMMTel)}
	if video {
		fs = append(fs, sip.NewFeature("video"))
	}

	v, _ := sip.CallerPreference{Features: fs}.Value()

	return v
}

// RFC 5621
func offerOf(req *sip.Request) ([]byte, bool) {
	p, ok, err := req.Envelope.Content().FindFunc(sip.BodyPart.IsSession)
	if err != nil || !ok || len(p.Body) == 0 {
		return nil, false
	}

	return p.Body, true
}

func mediaType(ct string) string {
	mt, _, _ := strings.Cut(ct, ";")

	return strings.ToLower(strings.TrimSpace(mt))
}
