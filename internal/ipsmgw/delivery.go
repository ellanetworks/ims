package ipsmgw

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/nas"
	"github.com/ellanetworks/core/nas/sms"
	"github.com/ellanetworks/ims/sip"
)

// pendingReport is a short message the IP-SM-GW delivered in a SIP MESSAGE, which waits for the delivery report of
// the UE (TS 24.341 §5.3.3.4.1): by the Call-ID of the MESSAGE, which the In-Reply-To of the report has, else by the
// RP message reference and the public user identities of the user.
type pendingReport struct {
	callID    string
	reference uint8
	ids       map[string]bool
	report    chan sms.RPMessage
}

// pendingReports are the short messages that wait for a delivery report.
type pendingReports struct {
	mu   sync.Mutex
	byID map[string]*pendingReport
	next uint8
}

func (p *pendingReports) add(r *pendingReport) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.byID == nil {
		p.byID = map[string]*pendingReport{}
	}

	p.byID[r.callID] = r
}

func (p *pendingReports) remove(r *pendingReport) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.byID, r.callID)
}

// reference is the RP message reference of the next short message (TS 24.011 §8.2.3).
func (p *pendingReports) reference() uint8 {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.next++

	return p.next
}

// byCallID is the short message of the MESSAGE of callID, if it waits.
func (p *pendingReports) byCallID(callID string) (*pendingReport, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	r, ok := p.byID[callID]

	return r, ok
}

// of is the short message of the RP message reference ref to one of ids, if it waits.
func (p *pendingReports) of(ref uint8, ids []sip.URI) (*pendingReport, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, r := range p.byID {
		if r.reference != ref {
			continue
		}

		for _, u := range ids {
			if r.ids[key(u)] {
				return r, true
			}
		}
	}

	return nil, false
}

// deliver hands the delivery report rp to r, once.
func (r *pendingReport) deliver(rp sms.RPMessage) {
	select {
	case r.report <- rp:
	default:
	}
}

// deliverIMS delivers the short message in a SIP MESSAGE to the user of reg (TS 24.341 §5.3.3.4.2): to its tel URI
// if any, else its IMPU, through its S-CSCF, to the contacts of the smsip feature tag, not forked, of the IP-SM-GW
// asserted; of an RP-DATA of the service centre address. The outcome is the delivery report of the UE: of an RP-ACK,
// the SMS-DELIVER-REPORT it carries; of an RP-ERROR, the failure of TS 23.040 §11.1. A failure response to the
// MESSAGE is the error of TS 29.311 Table 6.1.4.4.1.1, as TS 24.341 §5.3.3.4.2 has it; no report before ctx is done,
// an absent subscriber (TS 23.040 §11.1: SIP transaction timeout).
func (a *AS) deliverIMS(ctx context.Context, reg registration, m sgd.MTForwardShortMessage) outcome {
	ref := a.pending.reference()

	body, err := (&sms.RPData{
		Direction: nas.DirectionDownlink, Reference: ref, Originator: sms.E164Address(m.ServiceCentreAddress),
		UserData: m.SMRPUI,
	}).MarshalBinary()
	if err != nil {
		a.log.Warn("short message cannot be delivered over IMS", slog.Any("error", err))
		return outcome{domain: domainIMS, result: tgpp.Result{Code: diameter.ResultUnableToComply}}
	}

	target := reg.impu

	for _, u := range reg.set {
		if u.IsTel() {
			target = u
			break
		}
	}

	self := sip.URI{Scheme: "sip", Host: Host(a.home)}

	req := sip.NewRequest("MESSAGE", target)
	req.Header.Set("From", sip.Address{URI: self}.String()+";tag="+strings.TrimPrefix(sip.NewBranch(), "z9hG4bK"))
	req.Header.Set("To", sip.Address{URI: target}.String())
	req.Header.Set("Accept-Contact", "*;+g.3gpp.smsip;explicit;require")
	req.Header.Set("Request-Disposition", "no-fork")
	req.Header.Set("P-Asserted-Identity", sip.Address{URI: self}.String())
	req.Header.Set("P-Charging-Vector", newChargingVector(a.home))

	route := reg.scscf.Clone()
	route.Params.Set("lr", "")
	req.Header.Set("Route", sip.Address{URI: route}.String())
	// The Call-ID the In-Reply-To of the delivery report has.
	req.Header.Set("Call-ID", sip.NewTag())
	req.SetBody(ContentType, body)

	p := &pendingReport{
		callID: req.Header.CallID(), reference: ref, ids: map[string]bool{reg.key: true},
		report: make(chan sms.RPMessage, 1),
	}
	for _, u := range reg.set {
		p.ids[key(u)] = true
	}

	a.pending.add(p)
	defer a.pending.remove(p)

	responded := make(chan *sip.Response, 1)

	if err := a.agent.Request(req, func(res *sip.Response, err error) {
		if err != nil {
			res = nil
		}

		responded <- res
	}); err != nil {
		a.log.Warn("short message not sent over IMS", slog.Any("error", err))
		return absentOverIMS()
	}

	for {
		select {
		case res := <-responded:
			switch {
			case res == nil:
				return absentOverIMS()
			case res.StatusCode >= 300:
				return sipFailure(res.StatusCode)
			}

			responded = nil
		case rp := <-p.report:
			return reportOutcome(rp)
		case <-ctx.Done():
			return absentOverIMS()
		}
	}
}

// absentOverIMS is the outcome of no delivery report: an absent subscriber, of no response via the IP-SM-GW (TS
// 23.040 §11.1, §3.3.1 Table 1).
func absentOverIMS() outcome {
	d := tgpp.AbsentUserNoResponseIPSMGW

	return outcome{domain: domainIMS, result: tgpp.Experimental(tgpp.ResultErrorAbsentUser), absent: &d}
}

// sipFailure is the outcome of a failure response to the MESSAGE of a short message (TS 29.311 Table 6.1.4.4.1.1),
// its user error the Diameter error of the same name (TS 29.338 Table 6.2.2.1/2), a System Failure
// DIAMETER_UNABLE_TO_COMPLY (RFC 6733 §7.1.5).
func sipFailure(code int) outcome {
	o := outcome{domain: domainIMS}

	switch code {
	case 401, 407:
		o.result = tgpp.Experimental(tgpp.ResultErrorIllegalUser)
	case 404, 604:
		o.result = tgpp.Experimental(tgpp.ResultErrorUserUnknown)
	case 480:
		o.result = tgpp.Experimental(tgpp.ResultErrorAbsentUser)
	case 486, 600, 603:
		o.result = tgpp.Experimental(tgpp.ResultErrorUserBusyForMTSMS)
	default:
		o.result = tgpp.Result{Code: diameter.ResultUnableToComply}
	}

	return o
}

// reportOutcome is the outcome of the delivery report of the UE (TS 23.040 §11.1): of an RP-ACK, a success of its
// SMS-DELIVER-REPORT; of an RP-ERROR of cause #22, the memory capacity exceeded, of another, an equipment protocol
// error, each of the SMS-DELIVER-REPORT it carries as the diagnostic.
func reportOutcome(rp sms.RPMessage) outcome {
	o := outcome{domain: domainIMS}

	switch m := rp.(type) {
	case *sms.RPAck:
		o.ok, o.ui = true, m.UserData
	case *sms.RPError:
		cause := sgd.CauseEquipmentProtocolError
		if m.Cause.Effective(sms.TransferMobileTerminated) == sms.RPCauseMemoryCapacityExceeded {
			cause = sgd.CauseMemoryCapacityExceeded
		}

		o.result, o.cause, o.diagnostic = tgpp.Experimental(tgpp.ResultErrorSMDeliveryFailure), &cause, m.UserData
	}

	return o
}
