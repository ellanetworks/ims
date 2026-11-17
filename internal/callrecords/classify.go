package callrecords

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/db"
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
func AcceptedMedia(answer []byte) []string {
	s, err := sdp.Parse(answer)
	if err != nil {
		return nil
	}

	var types []string

	for _, m := range s.Media {
		if m.Port() != 0 && !slices.Contains(types, m.Type()) {
			types = append(types, m.Type())
		}
	}

	return types
}

// RFC 3264 section 6.1
// RFC 8866 section 6.6
func AcceptedCodecs(offer, answer []byte) []db.Codec {
	o, err := sdp.Parse(offer)
	if err != nil {
		return nil
	}

	a, err := sdp.Parse(answer)
	if err != nil {
		return nil
	}

	var codecs []db.Codec

	for i, m := range a.Media {
		if i >= len(o.Media) || m.Port() == 0 {
			continue
		}

		offered := codecsOf(o.Media[i])

		for _, c := range codecsOf(m) {
			same := func(d db.Codec) bool { return sameCodec(c, d) }
			if slices.ContainsFunc(offered, same) && !slices.ContainsFunc(codecs, same) {
				codecs = append(codecs, c)
			}
		}
	}

	return codecs
}

// RFC 4855 section 3
func sameCodec(a, b db.Codec) bool {
	return a.Media == b.Media && strings.EqualFold(a.Encoding, b.Encoding) && a.ClockRate == b.ClockRate &&
		a.Channels == b.Channels
}

// RFC 4733 section 7.1.1
// RFC 3389 section 6.1
func codecsOf(m *sdp.Media) []db.Codec {
	maps, _ := m.Codecs()

	var out []db.Codec

	for _, r := range maps {
		if strings.EqualFold(r.Encoding, "telephone-event") || strings.EqualFold(r.Encoding, "CN") {
			continue
		}

		c := db.Codec{Media: m.Type(), Encoding: r.Encoding, ClockRate: r.ClockRate}

		if c.Media == "audio" {
			c.Channels = 1

			if r.Params != "" {
				n, err := strconv.Atoi(r.Params)
				if err != nil || n < 1 {
					continue
				}

				c.Channels = n
			}
		}

		out = append(out, c)
	}

	return out
}
