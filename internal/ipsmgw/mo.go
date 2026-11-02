package ipsmgw

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/nas"
	"github.com/ellanetworks/core/nas/sms"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/lifetime"
	"github.com/ellanetworks/ims/sip/ua"
)

// TS 24.341 clause 7.1
const ContentType = "application/vnd.3gpp.sms"

// TS 24.011 clause 10
const moTimeout = 30 * time.Second

var ErrUnknownServiceCentre = errors.New("ipsmgw: no SMSC of the service centre address")

type SMSCs interface {
	Any() bool
	Forward(ctx context.Context, m sgd.MOForwardShortMessage) ([]byte, error)
}

// TS 24.341 clause 5.3.3.4.1
func (a *AS) handleMessage(ctx context.Context, r *ua.Request) {
	req := r.Message()

	h, _ := sms.ParseRPHeader(req.Content().Body, nas.DirectionUplink)
	report := h.MTI == sms.MTIAckMSToNetwork || h.MTI == sms.MTIErrorMSToNetwork

	var header sip.Header
	if v := chargingVector(req, a.home); v != "" {
		header = append(header, sip.Field{Name: "P-Charging-Vector", Value: v})
	}

	switch {
	case req.Content().MediaType() != ContentType:
		_ = r.Reject(415, append(header, sip.Field{Name: "Accept", Value: ContentType})...)
		return
	case req.Header.Has("In-Reply-To"):
		a.handleReport(r, header)
		return
	case !report && !a.smscs.Any():
		_ = r.Respond(ua.Reply{Code: 403, Reason: "SMS over IP Not Supported", Header: header})
		return
	}

	_ = r.Respond(ua.Reply{Code: 202, Header: header})

	sender := a.sender(req)
	rpdu := req.Content().Body

	a.work.Go(func() { a.receive(ctx, sender, rpdu) })
}

// TS 24.341 clause 5.3.3.4.1
func (a *AS) handleReport(r *ua.Request, header sip.Header) {
	req := r.Message()

	var (
		p  *pendingReport
		ok bool
	)

	for v := range strings.SplitSeq(req.Header.Get("In-Reply-To"), ",") {
		if p, ok = a.pending.byCallID(strings.TrimSpace(v)); ok {
			break
		}
	}

	if !ok {
		_ = r.Reject(488, header...)
		return
	}

	_ = r.Respond(ua.Reply{Code: 202, Header: header})

	switch rp, _ := sms.ParseRP(req.Content().Body, nas.DirectionUplink); rp.(type) {
	case *sms.RPAck, *sms.RPError:
		p.deliver(rp)
	default:
		p.deliver(undecodableReport(p.reference))
	}
}

// TS 23.040 clause 11.1
func undecodableReport(ref uint8) sms.RPMessage {
	return &sms.RPError{Direction: nas.DirectionUplink, Reference: ref, Cause: sms.RPCauseProtocolErrorUnspecified}
}

type sender struct {
	callID string
	ids    []sip.URI
	msisdn string
	imsi   string
	impu   sip.URI
	scscf  sip.URI
}

// TS 24.341 clause 5.3.3.4.1
func (a *AS) sender(req *sip.Request) sender {
	s := sender{callID: req.Header.CallID()}

	pai, _ := req.Header.Addresses("P-Asserted-Identity")
	for _, p := range pai {
		s.ids = append(s.ids, p.URI)

		if n := p.URI.Identity().User; p.URI.IsTel() && s.msisdn == "" && e164(strings.TrimPrefix(n, "+")) &&
			strings.HasPrefix(n, "+") {
			s.msisdn = strings.TrimPrefix(n, "+")
		}
	}

	if reg, ok := a.reg.lookup(s.ids); ok {
		s.scscf, s.imsi, s.impu = reg.scscf, reg.imsi, reg.impu

		if s.msisdn == "" {
			s.msisdn = reg.msisdn
		}
	} else {
		s.scscf = scscfOf(req.Flow.Remote)
	}

	return s
}

func (a *AS) receive(ctx context.Context, s sender, rpdu []byte) {
	msg, err := sms.ParseRP(rpdu, nas.DirectionUplink)
	if msg == nil {
		h, herr := sms.ParseRPHeader(rpdu, nas.DirectionUplink)

		switch {
		case errors.Is(herr, sms.ErrUnknownMessageType):
			a.answer(ctx, s, rpErrorFor(rpdu, herr))
		case herr == nil && (h.MTI == sms.MTIDataMSToNetwork || h.MTI == sms.MTISMMAMSToNetwork):
			a.answer(ctx, s, rpErrorFor(rpdu, err))
		case herr == nil:
			if p, ok := a.pending.of(h.Reference, s.ids); ok {
				p.deliver(undecodableReport(h.Reference))
			}
		default:
			a.log.DebugContext(ctx, "undecodable RPDU dropped", slog.String("call_id", s.callID), slog.Any("error", err))
		}

		return
	}

	switch m := msg.(type) {
	case *sms.RPData:
		report := a.submit(ctx, s, m)
		_, ok := report.(*sms.RPAck)
		a.metrics.count(directionMO, "smsc", ok)
		a.answer(ctx, s, report)
	case *sms.RPSMMA:
		a.answer(ctx, s, &sms.RPError{
			Direction: nas.DirectionDownlink, Reference: m.Reference,
			Cause: sms.RPCauseRequestedFacilityNotImplemented,
		})
	default:
		if p, ok := a.pending.of(msg.MessageReference(), s.ids); ok {
			p.deliver(msg)
			return
		}

		a.log.DebugContext(ctx, "delivery report of no short message the IP-SM-GW delivered", slog.String("call_id", s.callID))
	}
}

// TS 24.011 clause 9.3
func rpErrorFor(rpdu []byte, err error) *sms.RPError {
	var reference uint8
	if len(rpdu) > 1 {
		reference = rpdu[1]
	}

	cause := sms.RPCauseInvalidMandatoryInformation
	if errors.Is(err, sms.ErrUnknownMessageType) {
		cause = sms.RPCauseMessageTypeNonExistent
	}

	return &sms.RPError{Direction: nas.DirectionDownlink, Reference: reference, Cause: cause}
}

// TS 24.341 clause 5.3.3.4.1
// TS 24.341 clause 5.3.3.4.3
// TS 23.040 clause 11.3
func (a *AS) submit(ctx context.Context, s sender, m *sms.RPData) sms.RPMessage {
	log := a.log.With(slog.String("call_id", s.callID), slog.Uint64("rp_reference", uint64(m.Reference)))

	reject := func(cause sms.RPCause, diagnostic []byte, reason string, err error) sms.RPMessage {
		log.InfoContext(ctx, "mobile originated short message rejected", slog.String("reason", reason),
			slog.String("rp_cause", cause.String()), slog.Any("error", err))

		var ud []byte
		if len(diagnostic) > 0 {
			ud = diagnostic
		}

		return &sms.RPError{Direction: nas.DirectionDownlink, Reference: m.Reference, Cause: cause, UserData: ud}
	}

	d := m.Destination
	if d == nil || d.Raw != nil || d.TypeOfNumber != sms.TypeOfNumberInternational ||
		d.NumberingPlan != sms.NumberingPlanISDN || !e164(d.Digits) {
		return reject(sms.RPCauseUnassignedNumber, nil, "the service centre address is not an international E.164 number", nil)
	}

	if s.msisdn == "" {
		return reject(sms.RPCauseRequestedFacilityNotSubscribed, nil, "the sender has no MSISDN", nil)
	}

	if len(m.UserData) == 0 {
		return reject(sms.RPCauseInvalidMandatoryInformation, nil, "RP-DATA without a TPDU", nil)
	}

	ctx, cancel := context.WithTimeout(lifetime.With(a.life, ctx), moTimeout)
	defer cancel()

	ui, err := a.smscs.Forward(ctx, sgd.MOForwardShortMessage{
		ServiceCentreAddress: d.Digits,
		User:                 tgpp.UserIdentifier{MSISDN: s.msisdn, IMSI: s.imsi},
		SMRPUI:               m.UserData,
	})

	var failure *sgd.ResultError

	switch {
	case err == nil:
		log.InfoContext(ctx, "mobile originated short message forwarded", slog.String("service_centre", d.Digits))

		var ud []byte
		if len(ui) > 0 {
			ud = ui
		}

		return &sms.RPAck{Direction: nas.DirectionDownlink, Reference: m.Reference, UserData: ud}
	case errors.Is(err, ErrUnknownServiceCentre):
		return reject(sms.RPCauseUnassignedNumber, nil, "no SMSC of the service centre address", err)
	case errors.As(err, &failure):
		diagnostic := failure.DiagnosticInfo
		if len(diagnostic) == 0 {
			diagnostic = failure.SMRPUI
		}

		return reject(moFailureCause(failure), diagnostic, "the SMSC refused it", err)
	default:
		return reject(sms.RPCauseNetworkOutOfOrder, nil, "no SMSC answered", err)
	}
}

// TS 23.040 clause 11.3
func moFailureCause(e *sgd.ResultError) sms.RPCause {
	switch {
	case e.IsExperimental(tgpp.ResultErrorSMDeliveryFailure) && e.DeliveryFailureCause != nil:
		switch *e.DeliveryFailureCause {
		case sgd.CauseUnknownServiceCentre:
			return sms.RPCauseUnassignedNumber
		case sgd.CauseSCCongestion:
			return sms.RPCauseCongestion
		case sgd.CauseInvalidSMEAddress:
			return sms.RPCauseShortMessageTransferRejected
		case sgd.CauseUserNotSCUser:
			return sms.RPCauseUnidentifiedSubscriber
		}
	case e.IsExperimental(tgpp.ResultErrorFacilityNotSupported):
		return sms.RPCauseRequestedFacilityNotImplemented
	}

	return sms.RPCauseNetworkOutOfOrder
}

// TS 24.341 clause 5.3.3.4.1
// TS 24.341 clause 5.3.3.4.3
func (a *AS) answer(ctx context.Context, s sender, msg sms.RPMessage) {
	body, err := msg.MarshalBinary()
	if err != nil {
		switch m := msg.(type) {
		case *sms.RPAck:
			m.UserData = nil
		case *sms.RPError:
			m.UserData = nil
		}

		if body, err = msg.MarshalBinary(); err != nil {
			a.log.ErrorContext(ctx, "failed to encode an RPDU", slog.String("call_id", s.callID), slog.Any("error", err))
			return
		}
	}

	target := s.impu

	for _, u := range s.ids {
		if target.Scheme == "" && u.IsTel() {
			target = u
		}
	}

	if target.Scheme == "" && len(s.ids) > 0 {
		target = s.ids[0]
	}

	if target.Scheme == "" {
		a.log.WarnContext(ctx, "no public user identity to answer a short message to", slog.String("call_id", s.callID))
		return
	}

	self := sip.URI{Scheme: "sip", Host: Host(a.home)}

	req := sip.NewRequest("MESSAGE", target)
	req.Header.Set("From", sip.Address{URI: self}.String()+";tag="+strings.TrimPrefix(sip.NewBranch(), "z9hG4bK"))
	req.Header.Set("To", sip.Address{URI: target}.String())
	req.Header.Set("Accept-Contact", "*;+g.3gpp.smsip;explicit;require")
	req.Header.Set("Request-Disposition", "fork")
	req.Header.Set("In-Reply-To", s.callID)
	req.Header.Set("P-Asserted-Identity", sip.Address{URI: self}.String())
	req.Header.Set("P-Charging-Vector", newChargingVector(a.home))

	route := s.scscf.Clone()
	route.Params.Set("lr", "")
	req.Header.Set("Route", sip.Address{URI: route}.String())

	req.SetBody(ContentType, body)

	err = a.agent.Request(ctx, req, func(res *sip.Response, err error) {
		switch {
		case err != nil:
			a.log.WarnContext(ctx, "short message answer not sent", slog.String("call_id", s.callID), slog.Any("error", err))
		case res.StatusCode >= 300:
			a.log.WarnContext(ctx, "short message answer refused", slog.String("call_id", s.callID),
				slog.Int("status", res.StatusCode))
		}
	})
	if err != nil {
		a.log.WarnContext(ctx, "short message answer not sent", slog.String("call_id", s.callID), slog.Any("error", err))
	}
}

func scscfOf(addr netip.AddrPort) sip.URI {
	return sip.URI{Scheme: "sip", Host: sip.FormatHost(addr.Addr()), Port: addr.Port()}
}
