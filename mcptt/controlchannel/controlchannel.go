// Package controlchannel is the MCPTT media plane control channel in SDP (TS 24.380 §4.3.3, §12.1.2, §14): its media
// description, its fmtp:MCPTT parameters, and the rules of their offer/answer exchange.
package controlchannel

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/sip/sdp"
)

// Format is the <fmt> of the control channel, and of its fmtp attribute.
const Format = "MCPTT"

// Params are the fmtp:MCPTT parameters (TS 24.380 §12.1.2).
type Params struct {
	// Queueing: mc_queueing, floor request queueing supported (§14.2.2).
	Queueing bool

	// Priority: mc_priority, the floor priority requested or granted, 1 to 255; 0 without it, the default priority
	// (§12.1.2.2 NOTE 1).
	Priority uint8

	// Granted: mc_granted, a granted indication acceptable in the 200 (offer) or given (answer) (§14.2.4, §14.3.4).
	Granted bool

	// ImplicitRequest: mc_implicit_request, the request is an implicit floor request (offer), accepted (answer).
	ImplicitRequest bool

	// SSRC: mc_ssrc, the RTP SSRC of the offerer's media, in an answer (§14.3.6); HasSSRC tells it is there.
	SSRC    uint32
	HasSSRC bool

	// NoFloorControl: mc_no_floor_ctrl, a pre-established session without floor control (§14.2.6, §14.3.7).
	NoFloorControl bool

	// FloorSSRC: mc_floor_ssrc, the SSRC the peer puts in the RTCP header of its floor control messages, for the
	// receiver to multiplex sessions on one port (§4.3.3.1, §14.2.7, §14.3.8); HasFloorSSRC tells it is there.
	FloorSSRC    uint32
	HasFloorSSRC bool

	// Other are the parameters the present document does not define, as received.
	Other sdp.FormatParams
}

// ParseParams parses the parameters of an fmtp:MCPTT attribute, after the format. The ABNF separates them with ":",
// its examples and deployed clients with ";" (§12.1.2.3, §4.3.3.1): parameters with a ";" are separated by it, and
// their values keep their ":"; others by a ":" before a parameter of the present document. mc_priority takes 1 to
// 255, as its semantics say, though the ABNF allows two digits; the SSRCs are 32-bit (RFC 3550 §5.1). A parameter
// given twice counts once, as first given. A malformed parameter, a value out of range or a flag with a value, is
// left out, its parameter absent (mc_priority then the default priority, §12.1.2.2 NOTE 1), and reported in the
// error, next to the parameters that parsed.
func ParseParams(s string) (Params, error) {
	var (
		p    Params
		seen []string
		errs []error
	)

	for _, fp := range sdp.ParseFormatParams(colonsToSemicolons(s)) {
		name := strings.ToLower(fp.Name)
		if slices.Contains(seen, name) {
			continue
		}

		seen = append(seen, name)

		var err error

		switch name {
		case "mc_queueing", "mc_granted", "mc_implicit_request", "mc_no_floor_ctrl":
			if fp.HasValue {
				err = errors.New("flag with a value")
				break
			}

			switch name {
			case "mc_queueing":
				p.Queueing = true
			case "mc_granted":
				p.Granted = true
			case "mc_implicit_request":
				p.ImplicitRequest = true
			default:
				p.NoFloorControl = true
			}
		case "mc_priority":
			var v uint64

			v, err = strconv.ParseUint(fp.Value, 10, 8)
			if err == nil && v == 0 {
				err = strconv.ErrRange
			}

			if err == nil {
				p.Priority = uint8(v)
			}
		case "mc_ssrc":
			p.SSRC, err = parseSSRC(fp.Value)
			p.HasSSRC = err == nil
		case "mc_floor_ssrc":
			p.FloorSSRC, err = parseSSRC(fp.Value)
			p.HasFloorSSRC = err == nil
		default:
			p.Other = append(p.Other, fp)
		}

		if err != nil {
			errs = append(errs, fmt.Errorf("controlchannel: fmtp:MCPTT %s=%q: %w", fp.Name, fp.Value, err))
		}
	}

	return p, errors.Join(errs...)
}

// colonsToSemicolons makes each ":" that separates parameters a ";", in parameters separated by ":": the ":" followed
// by an "mc_" name, so that the values of other parameters keep theirs. Parameters with a ";" are kept.
func colonsToSemicolons(s string) string {
	if strings.Contains(s, ";") {
		return s
	}

	var b strings.Builder

	for i := 0; i < len(s); i++ {
		if s[i] == ':' && len(s)-i > 3 && strings.EqualFold(s[i+1:i+4], "mc_") {
			b.WriteByte(';')
			continue
		}

		b.WriteByte(s[i])
	}

	return b.String()
}

func parseSSRC(v string) (uint32, error) {
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return 0, err
	}

	return uint32(n), nil
}

// MaxPriority is the highest mc_priority of the ABNF's 1*2(DIGIT) (§12.1.2.3), which a strict peer may hold to,
// below the 255 of the semantics (§12.1.2.2): the highest the server sends.
const MaxPriority = 99

// String is the parameters as sent: separated by ";", in the order of the ABNF, then the others. An mc_priority
// above MaxPriority has three digits, which a strict peer may refuse.
func (p Params) String() string {
	var ps sdp.FormatParams

	flag := func(on bool, name string) {
		if on {
			ps = append(ps, sdp.FormatParam{Name: name})
		}
	}

	value := func(on bool, name string, v uint64) {
		if on {
			ps = append(ps, sdp.FormatParam{Name: name, Value: strconv.FormatUint(v, 10), HasValue: true})
		}
	}

	flag(p.Queueing, "mc_queueing")
	value(p.Priority != 0, "mc_priority", uint64(p.Priority))
	flag(p.Granted, "mc_granted")
	flag(p.ImplicitRequest, "mc_implicit_request")
	value(p.HasSSRC, "mc_ssrc", uint64(p.SSRC))
	flag(p.NoFloorControl, "mc_no_floor_ctrl")
	value(p.HasFloorSSRC, "mc_floor_ssrc", uint64(p.FloorSSRC))

	return append(ps, p.Other...).String()
}

// withoutFloorControl is p without the parameters of floor control, for a session without it (§14.2.6, §14.3.7):
// mc_floor_ssrc stays, for the pre-established session control on the channel.
func (p Params) withoutFloorControl() Params {
	p.Queueing, p.Priority, p.Granted, p.ImplicitRequest = false, 0, false, false
	p.SSRC, p.HasSSRC = 0, false

	return p
}

// Offer is the parameters of the offer of an initial INVITE of the offerer's local ones (§14.2): without mc_ssrc,
// which only an answer carries (§12.1.2.2); without floor control parameters along with mc_no_floor_ctrl (§14.2.6).
// Later offers (§14.2.1 NOTE, §14.5) come with the features that make them (S3).
func Offer(local Params) Params {
	local.SSRC, local.HasSSRC = 0, false

	if local.NoFloorControl {
		local = local.withoutFloorControl()
	}

	return local
}

// ErrNoFloorControl is the error of an offer of mc_no_floor_ctrl to an answerer without sessions without floor
// control (§14.3.7): it is not to be answered.
var ErrNoFloorControl = errors.New("controlchannel: mc_no_floor_ctrl offered, not supported")

// Answer is the answer to an offer of what the answerer grants, local (§14.3): only parameters the offer has
// (§14.3.1); of an offered mc_priority, the lower of it and local.Priority, the highest priority the answerer grants,
// and none for a local.Priority of 0 (§14.3.3: the controlling function grants a receive-only member none, the
// client and the non-controlling function grant 255, the one offered); mc_granted and mc_ssrc only with the implicit
// floor request accepted (§14.3.4, §12.1.2.2, §14.3.6); mc_floor_ssrc only when offered (§14.3.8). An offer with
// mc_no_floor_ctrl gets no floor control parameters (§14.3.7): its "include the mc_implicit_request" contradicts
// §14.3.1 and §14.2.6, which the answer follows; an answerer without local.NoFloorControl cannot answer it.
func Answer(offer, local Params) (Params, error) {
	if offer.NoFloorControl {
		if !local.NoFloorControl {
			return Params{}, ErrNoFloorControl
		}

		offer, local = offer.withoutFloorControl(), local.withoutFloorControl()
	}

	a := Params{
		Queueing:        offer.Queueing && local.Queueing,
		ImplicitRequest: offer.ImplicitRequest && local.ImplicitRequest,
		NoFloorControl:  offer.NoFloorControl && local.NoFloorControl,
		Other:           common(local.Other, offer.Other),
	}

	if offer.Priority != 0 {
		a.Priority = min(offer.Priority, local.Priority)
	}

	// mc_granted grants the implicit floor request (§14.3.4): it needs the request accepted.
	a.Granted = offer.Granted && local.Granted && a.ImplicitRequest

	if a.ImplicitRequest && local.HasSSRC {
		a.SSRC, a.HasSSRC = local.SSRC, true
	}

	if offer.HasFloorSSRC && local.HasFloorSSRC {
		a.FloorSSRC, a.HasFloorSSRC = local.FloorSSRC, true
	}

	return a, nil
}

// ErrPriority is the error of an answer granting a higher floor priority than offered (§12.1.2.2).
var ErrPriority = errors.New("controlchannel: mc_priority above the one offered")

// Accept is an answer to the offer as the offerer takes it (§14.4): the parameters the offer did not have discarded,
// but mc_ssrc, used whether offered or not. An mc_priority above the one offered is discarded too, and reported.
func (offer Params) Accept(answer Params) (Params, error) {
	a := Params{
		Queueing:        answer.Queueing && offer.Queueing,
		Granted:         answer.Granted && offer.Granted,
		ImplicitRequest: answer.ImplicitRequest && offer.ImplicitRequest,
		SSRC:            answer.SSRC,
		HasSSRC:         answer.HasSSRC,
		NoFloorControl:  answer.NoFloorControl && offer.NoFloorControl,
		Other:           common(answer.Other, offer.Other),
	}

	if answer.HasFloorSSRC && offer.HasFloorSSRC {
		a.FloorSSRC, a.HasFloorSSRC = answer.FloorSSRC, true
	}

	if offer.Priority == 0 || answer.Priority == 0 {
		return a, nil
	}

	if answer.Priority > offer.Priority {
		return a, fmt.Errorf("%w: %d, offered %d", ErrPriority, answer.Priority, offer.Priority)
	}

	a.Priority = answer.Priority

	return a, nil
}

// common are the parameters of ps the others have too.
func common(ps, others sdp.FormatParams) sdp.FormatParams {
	var out sdp.FormatParams

	for _, fp := range ps {
		if others.Has(fp.Name) {
			out = append(out, fp)
		}
	}

	return out
}

// IsChannel reports whether a media description is a control channel (§4.3.3.1, Table 4.3.3.1-1).
func IsChannel(m *sdp.Media) bool {
	d, err := m.Desc()

	return err == nil && strings.EqualFold(d.Type, "application") && strings.EqualFold(d.Proto, "udp") &&
		len(d.Formats) == 1 && strings.EqualFold(d.Formats[0], Format)
}

// Channel is the index of the control channel of s, -1 without one.
func Channel(s *sdp.Session) int {
	for i, m := range s.Media {
		if IsChannel(m) {
			return i
		}
	}

	return -1
}

// MediaParams are the fmtp:MCPTT parameters of a control channel, none without the attribute; as ParseParams, with
// the malformed values left out and reported.
func MediaParams(m *sdp.Media) (Params, error) {
	v, ok := m.Fmtp(Format)
	if !ok {
		return Params{}, nil
	}

	return ParseParams(v)
}

// NewMedia is the media description of a control channel on the RTCP port given (§4.3.3.1), with p in its fmtp
// attribute when it has any.
func NewMedia(port uint16, p Params) *sdp.Media {
	m := &sdp.Media{}
	m.SetDesc(sdp.MediaDesc{Type: "application", Port: port, Proto: "udp", Formats: []string{Format}})

	if s := p.String(); s != "" {
		m.AddAttr("fmtp", Format+" "+s)
	}

	return m
}
