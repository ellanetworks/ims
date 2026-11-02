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

// TS 24.341 clause 5.3.3.4.1
type pendingReport struct {
	callID    string
	reference uint8
	ids       map[string]bool
	report    chan sms.RPMessage
}

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

// TS 24.011 clause 8.2.3
func (p *pendingReports) reference() uint8 {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.next++

	return p.next
}

func (p *pendingReports) byCallID(callID string) (*pendingReport, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	r, ok := p.byID[callID]

	return r, ok
}

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

func (r *pendingReport) deliver(rp sms.RPMessage) {
	select {
	case r.report <- rp:
	default:
	}
}

// TS 24.341 clause 5.3.3.4.2
// TS 23.040 clause 11.1
// TS 29.311 clause 6.1.4.4.1 table 6.1.4.4.1.1
func (a *AS) deliverIMS(ctx context.Context, reg registration, m sgd.MTForwardShortMessage) outcome {
	ref := a.pending.reference()

	body, err := (&sms.RPData{
		Direction: nas.DirectionDownlink, Reference: ref, Originator: sms.E164Address(m.ServiceCentreAddress),
		UserData: m.SMRPUI,
	}).MarshalBinary()
	if err != nil {
		a.log.WarnContext(ctx, "short message cannot be delivered over IMS", slog.Any("error", err))
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

	if err := a.agent.Request(ctx, req, func(res *sip.Response, err error) {
		if err != nil {
			res = nil
		}

		responded <- res
	}); err != nil {
		a.log.WarnContext(ctx, "short message not sent over IMS", slog.Any("error", err))
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

// TS 23.040 clause 11.1
// TS 23.040 clause 3.3.1 table 1
func absentOverIMS() outcome {
	d := tgpp.AbsentUserNoResponseIPSMGW

	return outcome{domain: domainIMS, result: tgpp.Experimental(tgpp.ResultErrorAbsentUser), absent: &d}
}

// TS 29.311 clause 6.1.4.4.1 table 6.1.4.4.1.1
// TS 29.338 clause 6.2.2.1
// RFC 6733 section 7.1.5
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

// TS 23.040 clause 11.1
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
