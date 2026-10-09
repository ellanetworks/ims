package pcscf

import (
	"slices"

	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/transaction"
)

// callRecord reports a call from a UE to the call records. A nil callRecord, for a request that is not an INVITE
// or without a recorder, reports nothing.
type callRecord struct {
	r       *callrecords.Recorder
	attempt callrecords.Attempt
	opened  bool
}

// attempt is the record of an INVITE from a registered UE, which open, rejecting or closed opens.
func (p *PCSCF) attempt(req *sip.Request, impi string, asserted []string, icid string) *callRecord {
	if req.Method != "INVITE" || p.cfg.Records == nil {
		return nil
	}

	return &callRecord{r: p.cfg.Records, attempt: callrecords.Attempt{
		ICID: icid, CallID: req.Header.CallID(), RequestURI: req.URI.String(), IMPI: impi, Asserted: asserted,
	}}
}

// open opens the record, once the INVITE is known to be a call.
func (c *callRecord) open() {
	if c == nil || c.opened {
		return
	}

	c.opened = true
	c.r.Attempt(c.attempt)
}

// rejecting reports that the P-CSCF answers the INVITE itself with an error status, before it does.
func (c *callRecord) rejecting(code int) {
	if c == nil {
		return
	}

	c.open()
	c.r.Rejecting(c.attempt.ICID, code)
}

// closed ends the record of an INVITE that the P-CSCF answered without forwarding it, with the final response the
// caller got: the P-CSCF's own, or the 487 to a CANCEL that came first.
func (c *callRecord) closed(tx *transaction.ServerTransaction) {
	if c == nil {
		return
	}

	c.open()
	c.r.Ended(c.attempt.ICID, callrecords.End{Code: tx.Status(), By: proxy.Caller, Cause: proxy.EndFailed})
}

// alerted reports a 180 toward the UE.
func (c *callRecord) alerted() {
	if c != nil {
		c.r.Alerted(c.attempt.ICID)
	}
}

func (c *callRecord) event(e proxy.DialogEvent) {
	if c == nil {
		return
	}

	switch e.Kind {
	case proxy.EventAnswered:
		c.r.Answered(c.attempt.ICID, e.Code)
	case proxy.EventNegotiated:
		c.r.Media(c.attempt.ICID, acceptedMedia(e.Exchange.Answer.Data))
	case proxy.EventEnded:
		c.r.Ended(c.attempt.ICID, callrecords.End{Code: e.Code, By: e.By, Cause: e.End})
	}
}

// terminatingRecord reports to the call records the UE that answers a call, and the release of a call toward a
// UE, which the proxy publishes before releasing it, so that the caller's end of it follows.
func (p *PCSCF) terminatingRecord(c *call, e proxy.DialogEvent) {
	// The call records know a call by its ICID unquoted, and the P-CSCF keeps it as on the wire.
	icid := sip.Unquote(c.icid)

	switch {
	case e.Kind == proxy.EventAnswered:
		p.cfg.Records.Reached(icid, c.impi)
	case e.Kind == proxy.EventEnded && e.End == proxy.EndReleased:
		p.cfg.Records.Released(icid)
	}
}

// acceptedMedia are the media types of the m-lines that an SDP answer accepts, those with a port (RFC 3264 §6).
func acceptedMedia(answer []byte) []string {
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
