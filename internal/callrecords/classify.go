package callrecords

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/mcptt/amr"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/sdp"
)

// RFC 3261 section 21
func Outcome(status int, by db.CallParty, alerted bool) db.CallOutcome {
	switch {
	case status >= 200 && status < 300:
		return db.OutcomeAnswered
	case status == 487 && by == db.PartyCaller:
		return db.OutcomeCancelled
	case status == 486 || status == 600:
		return db.OutcomeBusy
	case status == 603:
		return db.OutcomeRejected
	case status == 480:
		return db.OutcomeUnavailable
	case status == 408 && alerted:
		return db.OutcomeNoAnswer
	}

	return db.OutcomeFailed
}

func EndedBy(cause proxy.EndCause, by proxy.Side) db.CallParty {
	switch cause {
	case proxy.EndBye, proxy.EndFailed:
		switch by {
		case proxy.Caller:
			return db.PartyCaller
		case proxy.Callee:
			return db.PartyCallee
		}
	}

	return db.PartyNetwork
}

func Duration(r db.CallRecord) (time.Duration, bool) {
	if r.Outcome != db.OutcomeAnswered || r.DeliveryEndAt.IsZero() {
		return 0, false
	}

	return r.DeliveryEndAt.Sub(r.DeliveryStartAt), true
}

// RFC 3264 section 6
// RFC 3264 section 6.1
func Negotiated(offer, answer []byte) ([]string, []db.Codec) {
	a, err := sdp.Parse(answer)
	if err != nil {
		return nil, nil
	}

	o, err := sdp.Parse(offer)
	if err != nil {
		o = &sdp.Session{}
	}

	var (
		media  []string
		codecs []db.Codec
	)

	for i, m := range a.Media {
		if !accepted(m) {
			continue
		}

		if !slices.Contains(media, m.Type()) {
			media = append(media, m.Type())
		}

		if i >= len(o.Media) {
			continue
		}

		offered := codecsOf(o.Media[i])

		for _, c := range codecsOf(m) {
			if slices.ContainsFunc(offered, c.compatible) && !slices.ContainsFunc(codecs, func(d db.Codec) bool { return sameCodec(c.Codec, d) }) {
				codecs = append(codecs, c.Codec)
			}
		}
	}

	return media, codecs
}

// RFC 3264 section 6
// RFC 8843 section 7.3
// RFC 8843 section 7.3.3
func accepted(m *sdp.Media) bool {
	return m.Port() != 0 || m.HasAttr("bundle-only")
}

// RFC 4855 section 3
func sameCodec(a, b db.Codec) bool {
	return a.Media == b.Media && strings.EqualFold(a.Encoding, b.Encoding) && a.ClockRate == b.ClockRate &&
		a.Channels == b.Channels
}

type format struct {
	db.Codec
	fmtp sdp.FormatParams
}

// RFC 4867 section 8.3.1
func (f format) compatible(g format) bool {
	if !sameCodec(f.Codec, g.Codec) {
		return false
	}

	if _, ok := amr.Of(sdp.RTPMap{Encoding: f.Encoding, ClockRate: f.ClockRate}); ok {
		return amr.SameConfiguration(f.fmtp, g.fmtp)
	}

	return true
}

// RFC 4733 section 7.1.1
// RFC 3389 section 6.1
// RFC 4588 section 8.6
// RFC 2198 section 5
// RFC 5109 section 14.1
// RFC 8627 section 5.2
var notCodecs = []string{"telephone-event", "CN", "rtx", "red", "ulpfec", "flexfec"}

func codecsOf(m *sdp.Media) []format {
	maps, _ := m.Codecs()

	var out []format

	for _, r := range maps {
		if slices.ContainsFunc(notCodecs, func(n string) bool { return strings.EqualFold(r.Encoding, n) }) {
			continue
		}

		c := db.Codec{Media: m.Type(), Encoding: r.Encoding, ClockRate: r.ClockRate}

		if c.Media == "audio" {
			n, err := strconv.Atoi(r.Channels())
			if err != nil || n < 1 {
				continue
			}

			c.Channels = n
		}

		fmtp, _ := m.Fmtp(strconv.Itoa(int(r.Payload)))
		out = append(out, format{Codec: c, fmtp: sdp.ParseFormatParams(fmtp)})
	}

	return out
}
