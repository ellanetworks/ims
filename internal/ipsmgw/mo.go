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
	"github.com/ellanetworks/ims/sip/ua"
)

// ContentType is the media type of the RPDUs of short messages over IP (TS 24.341 §7.1).
const ContentType = "application/vnd.3gpp.sms"

// moTimeout bounds the forwarding of a short message to the SMSC: within the TR1M of the UE, 35 to 45 s (TS 24.011
// §10).
const moTimeout = 30 * time.Second

// ErrUnknownServiceCentre is of a service centre address that no SMSC serves.
var ErrUnknownServiceCentre = errors.New("ipsmgw: no SMSC of the service centre address")

// SMSCs are the SMSCs of the IP-SM-GW, over SGd.
type SMSCs interface {
	// Any reports whether an SMSC is configured: without one, the IP-SM-GW interworks no short message.
	Any() bool
	// Forward sends a mobile originated short message to an SMSC of its service centre address (TS 29.338
	// §6.2.1), and returns its answer: the SM-RP-UI of the SMS-SUBMIT-REPORT, if any, or an *sgd.ResultError of the
	// SMSC, ErrUnknownServiceCentre, or another error of no answer.
	Forward(ctx context.Context, m sgd.MOForwardShortMessage) ([]byte, error)
}

// handleMessage receives a SIP MESSAGE of an RPDU from the S-CSCF, of a short message, a notification of available
// memory, or a delivery report (TS 24.341 §5.3.3.4.1). One of an In-Reply-To answers a short message the IP-SM-GW
// delivered: of none, it gets a 488. Without an SMSC, one of a short message or a notification gets the 403 the
// S-CSCF gives without an IP-SM-GW, so that the UE sends its short messages over NAS. Otherwise, it gets a 202, and
// the IP-SM-GW answers a short message or a notification in a MESSAGE of its own.
func (a *AS) handleMessage(r *ua.Request) {
	req := r.Message()

	h, _ := sms.ParseRPHeader(req.Content().Body, nas.DirectionUplink)
	report := h.MTI == sms.MTIAckMSToNetwork || h.MTI == sms.MTIErrorMSToNetwork

	// Every response has the P-Charging-Vector of an AS acting as terminating UA (TS 24.229 §5.7.2).
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
		// A delivery report answers a short message of an SMSC, whatever the SMSCs of the short messages of the UE.
		_ = r.Respond(ua.Reply{Code: 403, Reason: "SMS over IP Not Supported", Header: header})
		return
	}

	_ = r.Respond(ua.Reply{Code: 202, Header: header})

	sender := a.sender(req)
	rpdu := req.Content().Body

	a.work.Go(func() { a.receive(sender, rpdu) })
}

// handleReport receives the delivery report of a MESSAGE of an In-Reply-To (TS 24.341 §5.3.3.4.1): of the Call-ID
// of a short message the IP-SM-GW delivered, a 202, and the RP-ACK or RP-ERROR to the delivery; of another, a 488.
func (a *AS) handleReport(r *ua.Request, header sip.Header) {
	req := r.Message()

	var (
		p  *pendingReport
		ok bool
	)

	// In-Reply-To lists Call-IDs (RFC 3261 §20.21).
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

// undecodableReport is the delivery report of a UE whose RPDU does not decode: a protocol error (TS 23.040 §11.1:
// "Other error causes").
func undecodableReport(ref uint8) sms.RPMessage {
	return &sms.RPError{Direction: nas.DirectionUplink, Reference: ref, Cause: sms.RPCauseProtocolErrorUnspecified}
}

// sender is the SM-over-IP sender of a MESSAGE: its public user identities asserted by the P-CSCF or S-CSCF (RFC
// 3325), its MSISDN and IMSI, the IMPU of its registration if any, and the S-CSCF to answer it through.
type sender struct {
	callID string
	ids    []sip.URI
	msisdn string
	imsi   string
	impu   sip.URI
	scscf  sip.URI
}

// sender is the sender of req: its MSISDN of the tel URI of its P-Asserted-Identity (TS 24.341 §5.3.3.4.1 NOTE 1),
// else of its registration; the S-CSCF of its registration, else the one the MESSAGE came from.
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

// receive handles the RPDU of a MESSAGE of s, and sends the RPDU that answers it, if any.
func (a *AS) receive(s sender, rpdu []byte) {
	msg, err := sms.ParseRP(rpdu, nas.DirectionUplink)
	if msg == nil {
		// An RP-DATA or RP-SMMA that does not decode gets an RP-ERROR (§5.3.3.4.1 steps 2, 3), as one of an unknown
		// type; a delivery report, nothing.
		h, herr := sms.ParseRPHeader(rpdu, nas.DirectionUplink)

		switch {
		case errors.Is(herr, sms.ErrUnknownMessageType):
			a.answer(s, rpErrorFor(rpdu, herr))
		case herr == nil && (h.MTI == sms.MTIDataMSToNetwork || h.MTI == sms.MTISMMAMSToNetwork):
			a.answer(s, rpErrorFor(rpdu, err))
		case herr == nil:
			if p, ok := a.pending.of(h.Reference, s.ids); ok {
				p.deliver(undecodableReport(h.Reference))
			}
		default:
			a.log.Debug("undecodable RPDU dropped", slog.String("call_id", s.callID), slog.Any("error", err))
		}

		return
	}

	switch m := msg.(type) {
	case *sms.RPData:
		report := a.submit(s, m)
		_, ok := report.(*sms.RPAck)
		a.metrics.count(directionMO, "smsc", ok)
		a.answer(s, report)
	case *sms.RPSMMA:
		// The notification goes to the HSS in a ReadyForSM (TS 23.204 §6.6 step 3), of MAP only (TS 29.002 §12.4):
		// no Diameter application has it. Its error FacilityNotSupported is RP-Cause #69 (TS 23.040 §11.2).
		a.answer(s, &sms.RPError{
			Direction: nas.DirectionDownlink, Reference: m.Reference,
			Cause: sms.RPCauseRequestedFacilityNotImplemented,
		})
	default:
		// A delivery report without In-Reply-To: of the short message of its reference to the sender.
		if p, ok := a.pending.of(msg.MessageReference(), s.ids); ok {
			p.deliver(msg)
			return
		}

		a.log.Debug("delivery report of no short message the IP-SM-GW delivered", slog.String("call_id", s.callID))
	}
}

// rpErrorFor is the RP-ERROR of an RPDU that does not decode (TS 24.011 §9.3): #97 of an unknown message type, #96
// otherwise.
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

// submit forwards the short message of m to an SMSC of its service centre address, an international E.164 number
// (TS 24.341 §5.3.3.4.1 step 2), and returns the RPDU of its submit report (TS 24.341 §5.3.3.4.3): an RP-ACK of the
// SMS-SUBMIT-REPORT of the SMSC, or an RP-ERROR of the cause of TS 23.040 §11.3.
func (a *AS) submit(s sender, m *sms.RPData) sms.RPMessage {
	log := a.log.With(slog.String("call_id", s.callID), slog.Uint64("rp_reference", uint64(m.Reference)))

	reject := func(cause sms.RPCause, diagnostic []byte, reason string, err error) sms.RPMessage {
		log.Info("mobile originated short message rejected", slog.String("reason", reason),
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

	ctx, cancel := context.WithTimeout(a.life, moTimeout)
	defer cancel()

	ui, err := a.smscs.Forward(ctx, sgd.MOForwardShortMessage{
		ServiceCentreAddress: d.Digits,
		User:                 tgpp.UserIdentifier{MSISDN: s.msisdn, IMSI: s.imsi},
		SMRPUI:               m.UserData,
	})

	var failure *sgd.ResultError

	switch {
	case err == nil:
		log.Info("mobile originated short message forwarded", slog.String("service_centre", d.Digits))

		var ud []byte
		if len(ui) > 0 {
			ud = ui
		}

		return &sms.RPAck{Direction: nas.DirectionDownlink, Reference: m.Reference, UserData: ud}
	case errors.Is(err, ErrUnknownServiceCentre):
		return reject(sms.RPCauseUnassignedNumber, nil, "no SMSC of the service centre address", err)
	case errors.As(err, &failure):
		// The SMS-SUBMIT-REPORT of the error, of the diagnostic (TS 24.011 §8.2.5.3), else of the SM-RP-UI.
		diagnostic := failure.DiagnosticInfo
		if len(diagnostic) == 0 {
			diagnostic = failure.SMRPUI
		}

		return reject(moFailureCause(failure), diagnostic, "the SMSC refused it", err)
	default:
		return reject(sms.RPCauseNetworkOutOfOrder, nil, "no SMSC answered", err)
	}
}

// moFailureCause is the RP-Cause of a negative outcome of the forwarding of a short message to the SMSC (TS 23.040
// §11.3).
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

// answer sends the RPDU that answers a MESSAGE of s in a MESSAGE of its own (TS 24.341 §5.3.3.4.1, §5.3.3.4.3): to
// a public user identity of the sender, the IMPU of its registration, else the tel URI of its MSISDN, else another,
// through its S-CSCF, which serves it as terminating; of the In-Reply-To of the MESSAGE answered, and the IP-SM-GW
// asserted. An RPDU whose user data is too long goes without it.
func (a *AS) answer(s sender, msg sms.RPMessage) {
	body, err := msg.MarshalBinary()
	if err != nil {
		switch m := msg.(type) {
		case *sms.RPAck:
			m.UserData = nil
		case *sms.RPError:
			m.UserData = nil
		}

		if body, err = msg.MarshalBinary(); err != nil {
			a.log.Error("failed to encode an RPDU", slog.String("call_id", s.callID), slog.Any("error", err))
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
		a.log.Warn("no public user identity to answer a short message to", slog.String("call_id", s.callID))
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

	err = a.agent.Request(req, func(res *sip.Response, err error) {
		switch {
		case err != nil:
			a.log.Warn("short message answer not sent", slog.String("call_id", s.callID), slog.Any("error", err))
		case res.StatusCode >= 300:
			a.log.Warn("short message answer refused", slog.String("call_id", s.callID), slog.Int("status", res.StatusCode))
		}
	})
	if err != nil {
		a.log.Warn("short message answer not sent", slog.String("call_id", s.callID), slog.Any("error", err))
	}
}

// scscfOf is the URI of the S-CSCF at addr.
func scscfOf(addr netip.AddrPort) sip.URI {
	return sip.URI{Scheme: "sip", Host: sip.FormatHost(addr.Addr()), Port: addr.Port()}
}
