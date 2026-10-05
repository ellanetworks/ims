package n5policy

import (
	"strconv"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/internal/n5"
	"github.com/ellanetworks/ims/internal/policy"
)

// medComponents returns the medComponents for the components (TS 29.513 §7.2.3). A removed component is the one
// the PCF holds, prev, with fStatus REMOVED: a null would remove it from the context instead (TS 29.501 §5.3.8.2).
func medComponents(cs []policy.MediaComponent, prev *n5.AppSessionContextUpdateData) map[string]n5.MediaComponent {
	if len(cs) == 0 {
		return nil
	}

	out := make(map[string]n5.MediaComponent, len(cs))

	for _, c := range cs {
		key := strconv.FormatUint(uint64(c.Number), 10)

		if c.Status == policy.FlowRemoved && prev != nil {
			if old, ok := prev.MedComponents[key]; ok {
				old.FStatus = n5.FlowRemoved
				out[key] = old

				continue
			}
		}

		out[key] = medComponent(c)
	}

	return out
}

// TS 29.514 §5.6.2.7, TS 29.513 §7.2.3
func medComponent(c policy.MediaComponent) n5.MediaComponent {
	out := n5.MediaComponent{
		MedCompN: c.Number,
		MedType:  mediaType(c.Type),
		FStatus:  flowStatus(c.Status),
		MarBwUl:  bitRate(c.MaxRequestedUL),
		MarBwDl:  bitRate(c.MaxRequestedDL),
	}

	if c.RR != nil {
		out.RRBw = new(n5.BitRate(*c.RR))
	}

	if c.RS != nil {
		out.RSBw = new(n5.BitRate(*c.RS))
	}

	for _, codec := range c.Codecs {
		out.Codecs = append(out.Codecs, codecData(codec))
	}

	for _, s := range c.SubComponents {
		sub := n5.MediaSubComponent{FNum: s.FlowNumber}

		for _, f := range s.Flows {
			sub.FDescs = append(sub.FDescs, flowDescription(f))
		}

		switch s.Usage {
		case policy.FlowUsageRTCP:
			sub.FlowUsage = n5.FlowUsageRTCP
		case policy.FlowUsageAFSignalling:
			sub.FlowUsage = n5.FlowUsageAFSignalling
		}

		if out.MedSubComps == nil {
			out.MedSubComps = make(map[string]n5.MediaSubComponent, len(c.SubComponents))
		}

		out.MedSubComps[strconv.FormatUint(uint64(s.FlowNumber), 10)] = sub
	}

	return out
}

func bitRate(v *uint64) *n5.BitRate {
	if v == nil {
		return nil
	}

	return new(n5.BitRate(*v))
}

// TS 29.514 §5.6.3.2: CodecData is encoded as the Codec-Data AVP (TS 29.214 §5.3.7).
func codecData(c policy.Codec) n5.CodecData {
	direction, kind := rx.CodecDownlink, rx.CodecOffer

	if c.Uplink {
		direction = rx.CodecUplink
	}

	if c.Answer {
		kind = rx.CodecAnswer
	}

	return direction.String() + "\n" + kind.String() + "\n" + c.SDP
}

// TS 29.514 §5.6.3.2: FlowDescription is encoded as the Flow-Description AVP (TS 29.214 §5.3.8).
func flowDescription(f policy.Flow) n5.FlowDescription {
	d := rx.FlowDescription{
		Direction: rx.FlowDirectionOut, Protocol: rx.Protocol(f.Protocol),
		Source: f.Source, Destination: f.Destination, DestinationPort: f.DestinationPort,
	}

	if f.Uplink {
		d.Direction = rx.FlowDirectionIn
	}

	return d.String()
}

func mediaType(t policy.MediaType) n5.MediaType {
	switch t {
	case policy.MediaAudio:
		return n5.MediaAudio
	case policy.MediaVideo:
		return n5.MediaVideo
	case policy.MediaData:
		return n5.MediaData
	case policy.MediaApplication:
		return n5.MediaApplication
	case policy.MediaControl:
		return n5.MediaControl
	case policy.MediaText:
		return n5.MediaText
	case policy.MediaMessage:
		return n5.MediaMessage
	}

	return n5.MediaOther
}

func flowStatus(s policy.FlowStatus) n5.FlowStatus {
	switch s {
	case 0:
		return ""
	case policy.FlowEnabledUplink:
		return n5.FlowEnabledUplink
	case policy.FlowEnabledDownlink:
		return n5.FlowEnabledDownlink
	case policy.FlowDisabled:
		return n5.FlowDisabled
	case policy.FlowRemoved:
		return n5.FlowRemoved
	}

	return n5.FlowEnabled
}
