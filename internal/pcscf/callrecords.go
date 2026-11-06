package pcscf

import (
	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

type callRecord struct {
	r       *callrecords.Recorder
	attempt callrecords.Attempt
	opened  bool
}

func (p *PCSCF) attempt(req *sip.Request, impi string, asserted []string, icid string) *callRecord {
	if req.Method != "INVITE" || p.cfg.Records == nil {
		return nil
	}

	return &callRecord{r: p.cfg.Records, attempt: callrecords.Attempt{
		ICID: icid, CallID: req.Header.CallID(), RequestURI: req.URI.String(), IMPI: impi, Asserted: asserted,
	}}
}

func (c *callRecord) open() {
	if c == nil || c.opened {
		return
	}

	c.opened = true
	c.r.Attempt(c.attempt)
}

func (c *callRecord) rejecting(tx *transaction.ServerTransaction, code int) {
	if c == nil {
		return
	}

	c.open()
	c.r.Rejecting(c.attempt.ICID, code, tx.ToTag())
}

func (c *callRecord) closed(tx *transaction.ServerTransaction) {
	if c == nil {
		return
	}

	c.open()
	c.r.Ended(c.attempt.ICID, callrecords.End{
		Code: tx.Status(), Tag: tx.ToTag(), By: proxy.Caller, Cause: proxy.EndFailed,
	})
}

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
		c.r.Media(c.attempt.ICID, callrecords.AcceptedMedia(e.Exchange.Answer.Data))
	case proxy.EventEnded:
		c.r.Ended(c.attempt.ICID, callrecords.End{Code: e.Code, Tag: e.Tag, By: e.By, Cause: e.End})
	}
}

func (p *PCSCF) terminatingRecord(c *call, e proxy.DialogEvent) {
	icid := sip.Unquote(c.icid)

	switch {
	case e.Kind == proxy.EventAnswered:
		p.cfg.Records.Reached(icid, c.impi)
	case e.Kind == proxy.EventEnded && e.End == proxy.EndReleased:
		p.cfg.Records.Released(icid)
	}
}
