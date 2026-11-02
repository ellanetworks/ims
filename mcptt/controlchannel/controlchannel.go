package controlchannel

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/sip/sdp"
)

const Format = "MCPTT"

// TS 24.380 clause 12.1.2
type Params struct {
	Queueing bool

	Priority uint8

	Granted bool

	ImplicitRequest bool

	SSRC    uint32
	HasSSRC bool

	NoFloorControl bool

	FloorSSRC    uint32
	HasFloorSSRC bool

	Other sdp.FormatParams
}

// TS 24.380 clause 12.1.2.3
// TS 24.380 clause 4.3.3.1
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

// TS 24.380 clause 12.1.2.3
// TS 24.380 clause 12.1.2.2
const MaxPriority = 99

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

// TS 24.380 clause 14.2.6
// TS 24.380 clause 14.3.7
func (p Params) withoutFloorControl() Params {
	p.Queueing, p.Priority, p.Granted, p.ImplicitRequest = false, 0, false, false
	p.SSRC, p.HasSSRC = 0, false

	return p
}

// TS 24.380 clause 14.2
// TS 24.380 clause 12.1.2.2
// TS 24.380 clause 14.2.6
func Offer(local Params) Params {
	local.SSRC, local.HasSSRC = 0, false

	if local.NoFloorControl {
		local = local.withoutFloorControl()
	}

	return local
}

// TS 24.380 clause 14.3.7
var ErrNoFloorControl = errors.New("controlchannel: mc_no_floor_ctrl offered, not supported")

// TS 24.380 clause 14.3
// TS 24.380 clause 14.3.1
// TS 24.380 clause 14.3.3
// TS 24.380 clause 14.3.4
// TS 24.380 clause 14.3.6
// TS 24.380 clause 14.3.7
// TS 24.380 clause 14.3.8
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

	a.Granted = offer.Granted && local.Granted && a.ImplicitRequest

	if a.ImplicitRequest && local.HasSSRC {
		a.SSRC, a.HasSSRC = local.SSRC, true
	}

	if offer.HasFloorSSRC && local.HasFloorSSRC {
		a.FloorSSRC, a.HasFloorSSRC = local.FloorSSRC, true
	}

	return a, nil
}

// TS 24.380 clause 12.1.2.2
var ErrPriority = errors.New("controlchannel: mc_priority above the one offered")

// TS 24.380 clause 14.4
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

func common(ps, others sdp.FormatParams) sdp.FormatParams {
	var out sdp.FormatParams

	for _, fp := range ps {
		if others.Has(fp.Name) {
			out = append(out, fp)
		}
	}

	return out
}

// TS 24.380 clause 4.3.3.1
func IsChannel(m *sdp.Media) bool {
	d, err := m.Desc()

	return err == nil && strings.EqualFold(d.Type, "application") && strings.EqualFold(d.Proto, "udp") &&
		len(d.Formats) == 1 && strings.EqualFold(d.Formats[0], Format)
}

func Channel(s *sdp.Session) int {
	for i, m := range s.Media {
		if IsChannel(m) {
			return i
		}
	}

	return -1
}

func MediaParams(m *sdp.Media) (Params, error) {
	v, ok := m.Fmtp(Format)
	if !ok {
		return Params{}, nil
	}

	return ParseParams(v)
}

// TS 24.380 clause 4.3.3.1
func NewMedia(port uint16, p Params) *sdp.Media {
	m := &sdp.Media{}
	m.SetDesc(sdp.MediaDesc{Type: "application", Port: port, Proto: "udp", Formats: []string{Format}})

	if s := p.String(); s != "" {
		m.AddAttr("fmtp", Format+" "+s)
	}

	return m
}
