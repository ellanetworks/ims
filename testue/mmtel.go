package testue

import (
	"slices"
	"strings"
	"sync"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/ue"
)

// ICSIMMTel is the ICSI of MMTel (TS 24.173 §5.1).
const ICSIMMTel = "urn:urn-7:3gpp-service.ims.icsi.mmtel"

// MMTelConfig configures the MMTel service of a UE (TS 24.173, IR.92, IR.94): voice and video calls.
type MMTelConfig struct {
	// Video makes the UE video capable (IR.94): it registers with the video feature tag, takes the video streams it
	// is offered, and can make video calls (CallOptions.Video) and add video to a call (Call.AddVideo). Without it,
	// the UE is voice only and declines video with port 0 (RFC 3264 §6).
	Video bool

	AcceptCalls bool

	// NoPreconditions is a UE with preconditions disabled (TS 24.229 §5.1.5A): an INVITE requiring them gets a 420,
	// and its calls run without them.
	NoPreconditions bool

	EarlyMedia string
}

// MMTel is the MMTel service of a UE: its calls.
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

// mmtelAllow are the methods of MMTel (TS 24.229 §5.1.3.1).
var mmtelAllow = []string{"INVITE", "UPDATE", "PRACK"}

// Names of the services the UE implements.
const (
	MMTelService = "mmtel"
	SMSService   = "sms"
)

// service is MMTel as a service of the UE.
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

	// TS 24.229 §5.1.3.1 b: precondition is advertised for the calls that run them.
	if !m.cfg.NoPreconditions {
		s.Understood = []string{sip.OptionTagPrecondition}
	}

	return s
}

// isCall reports whether an INVITE is a call by its contents (TS 24.229 §5.1.2A.2): without an offer, or one with
// audio or video.
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

// mediaTags are the media feature tags of MMTel, in the Contact the UE registers and in its dialogs (RFC 3840 §9,
// TS 24.229 §5.1.3.1): video when the UE takes video, whether or not the call has video (IR.94 §2.2.1, §2.2.2).
func (m *MMTel) mediaTags() sip.FeatureSet {
	if m.cfg.Video {
		return sip.FeatureSet{sip.NewFeature("audio"), sip.NewFeature("video")}
	}

	return sip.FeatureSet{sip.NewFeature("audio")}
}

// UE is the UE of the service.
func (m *MMTel) UE() *UE { return m.u }

// Calls are the incoming calls the UE accepts (AcceptCalls).
func (m *MMTel) Calls() <-chan *Call { return m.incoming }

// callContact is the Contact of the dialogs of MMTel (TS 24.229 §5.1.2A.1.1).
func (m *MMTel) callContact() string {
	c, _ := m.u.core().Contact(MMTelService)
	return c.String()
}

// IR.92 §2.2.4, IR.94 §2.2.2: a video call asks for a video capable callee, to guide forking (RFC 3841 §9.2).
func acceptContact(video bool) string {
	fs := sip.FeatureSet{sip.NewFeature("+g.3gpp.icsi-ref", ICSIMMTel)}
	if video {
		fs = append(fs, sip.NewFeature("video"))
	}

	v, _ := sip.CallerPreference{Features: fs}.Value()

	return v
}

// offerOf is the session description of a request, if any (RFC 5621).
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
