package ipsmgw

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
)

// Router is the S6c client of the IP-SM-GW toward the HSS.
type Router interface {
	// SendRoutingInfoForSM asks the HSS for the serving nodes of a user (TS 29.338 §5.2.1): an *s6c.ResultError of
	// the HSS, or another error of no answer.
	SendRoutingInfoForSM(ctx context.Context, req s6c.RoutingRequest) (s6c.Routing, error)
	// ReportSMDeliveryStatus reports the outcome of a delivery to the HSS (TS 29.338 §5.2.3).
	ReportSMDeliveryStatus(ctx context.Context, rep s6c.DeliveryReport) (s6c.ReportResult, error)
}

// Nodes is the SGd client of the IP-SM-GW toward the MMEs, SGSNs and SMSFs, of their Diameter identity.
type Nodes interface {
	// Forward forwards a short message to the node of host in realm (TS 29.338 §6.2.2), and returns the
	// SMS-DELIVER-REPORT of its answer, if any, or an *sgd.ResultError of the node, or another error of no answer.
	Forward(ctx context.Context, host, realm string, m sgd.MTForwardShortMessage) ([]byte, error)
}

// defaultDeliveryTimer bounds a delivery of an SMS-GMSC that gives no SM-Delivery-Timer.
const defaultDeliveryTimer = 30 * time.Second

// deliveryMargin is the time the IP-SM-GW keeps, of the SM-Delivery-Timer, for its answer to reach the SMS-GMSC.
const deliveryMargin = 2 * time.Second

// RoutingInfo answers a Send Routing Info for SM request the HSS forwarded from the SMS-GMSC (TS 23.204 §6.4 steps
// 3a to 3c; TS 24.341 §5.3.3.3; TS 29.338 §5.2.1.4, which TS 23.204 has the IP-SM-GW follow as an SMS Router): it
// asks the HSS for the serving nodes of the user, and answers with only itself as serving node, of an MT Correlation
// ID in place of the IMSI, which stands for the user and the serving nodes; the User Identifier Alert, MWD Status and
// absent user diagnostics of the HSS are transferred. An error of the HSS is returned as such, unless the IP-SM-GW
// has a public user identity of the MSISDN, which it delivers to over IMS.
func (a *AS) RoutingInfo(ctx context.Context, id diameter.Identity, req *diameter.Message) *diameter.Message {
	r, err := s6c.ParseSendRoutingInfoForSMRequest(req)
	if err != nil {
		return s6c.NewErrorAnswer(req, id, err)
	}

	log := a.log.With(slog.String("msisdn", r.MSISDN))

	number := a.number()

	switch {
	case r.MSISDN == "":
		// MSISDN-less delivery in IMS (TS 23.204 §6.0a) is not supported.
		return s6c.NewAnswer(req, id, tgpp.Experimental(tgpp.ResultErrorUserUnknown))
	case number == "":
		log.Warn("routing information request without an IP-SM-GW number")
		return s6c.NewAnswer(req, id, tgpp.Result{Code: diameter.ResultUnableToComply})
	}

	routing, err := a.router.SendRoutingInfoForSM(ctx, s6c.RoutingRequest{
		MSISDN: r.MSISDN, ServiceCentreAddress: r.ServiceCentreAddress, MTI: r.MTI, SMEA: r.SMEA, GPRSIndicator: true,
		SingleAttempt: r.SingleAttempt, SMSFSupport: true,
	})

	reg, known := a.reg.byMSISDN(r.MSISDN)

	var re *s6c.ResultError

	switch {
	case err == nil:
	case errors.As(err, &re) && !known:
		ans, aerr := s6c.NewSendRoutingInfoForSMErrorAnswer(req, id, *re, r.SMSFSupport)
		if aerr != nil {
			return s6c.NewErrorAnswer(req, id, aerr)
		}

		return ans
	case !known:
		log.Warn("routing information of the HSS unavailable", slog.Any("error", err))
		return s6c.NewAnswer(req, id, tgpp.Result{Code: diameter.ResultUnableToComply})
	default:
		// Over IMS only.
		routing = s6c.Routing{IMSI: reg.imsi}
		if re != nil {
			routing.MWDStatus, routing.Absent, routing.AlertMSISDN = re.MWDStatus, re.Absent, re.AlertMSISDN
		}
	}

	prefix := a.plmn
	if len(routing.IMSI) >= 6 {
		// The MCC and MNC, of a 2-digit MNC the first digit of the MSIN after it (TS 23.040 §3.2.7a).
		prefix = routing.IMSI[:6]
	}

	corr, err := a.corr.add(prefix, mtContext{msisdn: r.MSISDN, imsi: routing.IMSI, routing: routing, single: r.SingleAttempt},
		a.agent.Layer().Clock().Now())
	if err != nil {
		return s6c.NewErrorAnswer(req, id, err)
	}

	ans, err := s6c.NewSendRoutingInfoForSMAnswer(req, id, s6c.Routing{
		ServingNodes: s6c.ServingNodes{Serving: &s6c.ServingNode{IPSMGW: &s6c.NodeAddress{
			Name: id.OriginHost, Realm: id.OriginRealm, Number: number,
		}}},
		IMSI: corr, MWDStatus: routing.MWDStatus, Absent: routing.Absent, AlertMSISDN: routing.AlertMSISDN,
	}, r.SMSFSupport)
	if err != nil {
		return s6c.NewErrorAnswer(req, id, err)
	}

	log.Debug("routing information answered", slog.Bool("ims", known))

	return ans
}

// domain is a domain the IP-SM-GW delivers a short message in.
type domain int

const (
	domainIMS domain = iota
	domainMME
	domainSGSN
	domainSMSF3GPP
	domainSMSFNon3GPP
)

func (d domain) String() string {
	return [...]string{"ims", "mme", "sgsn", "smsf_3gpp", "smsf_non_3gpp"}[d]
}

// outcome is the outcome of a delivery attempt in a domain: the SMS-DELIVER-REPORT of a success, or the error of a
// failure, as the SMS-GMSC gets it.
type outcome struct {
	domain domain
	ok     bool
	ui     []byte
	result tgpp.Result
	// cause and diagnostic are of DIAMETER_ERROR_SM_DELIVERY_FAILURE.
	cause      *sgd.DeliveryFailureCause
	diagnostic []byte
	// absent is the diagnostic of DIAMETER_ERROR_ABSENT_USER, if any.
	absent *tgpp.AbsentUserDiagnostic
}

func (o outcome) memoryExceeded() bool {
	return o.cause != nil && *o.cause == sgd.CauseMemoryCapacityExceeded
}

// deliveryCause is the cause of the outcome for the message waiting data of the HSS (TS 29.338 §5.2.3.3): of absent
// user, unidentified user, or memory capacity exceeded; false of another.
func (o outcome) deliveryCause() (s6c.DeliveryCause, bool) {
	switch {
	case o.memoryExceeded():
		return s6c.DeliveryCauseMemoryCapacityExceeded, true
	case o.result.IsExperimental(tgpp.ResultErrorAbsentUser), o.result.IsExperimental(tgpp.ResultErrorUserUnknown):
		return s6c.DeliveryCauseAbsentUser, true
	}

	return 0, false
}

// ForwardShortMessage delivers a short message of the SMS-GMSC to the user of its MT Correlation ID (TS 23.204
// §6.4 steps 4 to 9, §6.5a): in the domains in turn, IMS first when the user can receive short messages over IP,
// then the MME, SGSN and SMSFs the HSS gave, until one delivers it or the UE's memory capacity is exceeded, within
// the SM-Delivery-Timer. When every domain failed of an absent user or a full memory, the IP-SM-GW reports it to the
// HSS (TS 29.338 §5.2.3.3), and tries again at once the serving nodes the HSS answers with, if any. The SMS-GMSC
// gets the outcome of the last attempt.
func (a *AS) ForwardShortMessage(ctx context.Context, id diameter.Identity, req *diameter.Message) *diameter.Message {
	m, err := sgd.ParseMTForwardShortMessageRequest(req)
	if err != nil {
		return tgpp.NewErrorAnswer(req, id, err)
	}

	now := a.agent.Layer().Clock().Now()

	c, ok := a.corr.get(m.IMSI, now)
	if !ok {
		a.log.Debug("short message of an unknown MT Correlation ID")
		return tgpp.NewExperimentalAnswer(req, id, tgpp.ResultErrorUserUnknown)
	}

	timer := m.DeliveryTimer
	if timer <= 0 {
		timer = defaultDeliveryTimer
	}

	// What is left of the SM-Delivery-Timer since the SM-Delivery-Start-Time of the SMS-GMSC (TS 29.338 Table
	// 6.2.2.1/1), keeping a margin for the answer.
	elapsed := time.Duration(0)
	if !m.DeliveryStartTime.IsZero() && m.DeliveryStartTime.Before(now) {
		elapsed = now.Sub(m.DeliveryStartTime)
	}

	ctx, cancel := context.WithTimeout(ctx, max(timer-elapsed-deliveryMargin, 0))
	defer cancel()

	log := a.log.With(slog.String("msisdn", c.msisdn))

	outcomes := a.attempt(ctx, c, m, c.routing.ServingNodes, true)
	last := outcomes[len(outcomes)-1]

	if !last.ok {
		alert, ok := a.report(ctx, c, m, outcomes)
		if ok && !last.memoryExceeded() && ctx.Err() == nil {
			log.Debug("retrying the delivery via the serving nodes the HSS reported")

			retry := a.attempt(ctx, c, m, alert, false)
			last = retry[len(retry)-1]
		}
	}

	a.metrics.count(directionMT, last.domain.String(), last.ok)

	log.Info("mobile terminated short message", slog.String("domain", last.domain.String()), slog.Bool("delivered", last.ok),
		slog.String("result", last.result.String()))

	return a.forwardAnswer(req, id, last)
}

// attempt delivers the short message in the domains in turn, IMS first if ims and the user can receive short
// messages over IP, then those of nodes, until one delivers it, the memory capacity is exceeded, or ctx is done; it
// returns the outcome of each attempt, of at least one.
func (a *AS) attempt(ctx context.Context, c mtContext, m sgd.MTForwardShortMessage, nodes s6c.ServingNodes, ims bool) []outcome {
	var outcomes []outcome

	done := func(o outcome) bool {
		outcomes = append(outcomes, o)
		return o.ok || o.memoryExceeded() || ctx.Err() != nil
	}

	nas := targets(nodes)
	if c.imsi == "" {
		nas = nil
	}

	if reg, ok := a.reg.availableFor(c.msisdn); ims && ok {
		// The UE has half of what is left to report the delivery, when NAS is left to try (TS 23.204 §6.5a NOTE 1).
		ictx, cancel := ctx, context.CancelFunc(func() {})
		if deadline, ok := ctx.Deadline(); ok && len(nas) > 0 {
			ictx, cancel = context.WithTimeout(ctx, time.Until(deadline)/2)
		}

		o := a.deliverIMS(ictx, reg, m)

		cancel()

		if done(o) {
			return outcomes
		}
	}

	for _, t := range nas {
		mt := m
		mt.IMSI = c.imsi
		mt.MMENumberForMTSMS, mt.SGSNNumber = t.mmeNumber, t.sgsnNumber

		ui, err := a.nodes.Forward(ctx, t.name, t.realm, mt)
		if done(outcomeOf(t.domain, ui, err)) {
			return outcomes
		}
	}

	if len(outcomes) == 0 {
		// No domain to deliver in: the user is absent (TS 23.040 §11.1: IMS not reachable).
		d := tgpp.AbsentUserDeregisteredIMS
		outcomes = append(outcomes, outcome{result: tgpp.Experimental(tgpp.ResultErrorAbsentUser), absent: &d})
	}

	return outcomes
}

// target is a serving node of a user over NAS, of its Diameter identity.
type target struct {
	domain                domain
	name, realm           string
	mmeNumber, sgsnNumber string
}

// targets are the serving nodes of nodes the IP-SM-GW delivers to over SGd: the MME or SGSN of the serving node,
// then of the additional serving node, then the SMSFs; not an MSC, reached over MAP only.
func targets(n s6c.ServingNodes) []target {
	var out []target

	for _, sn := range []*s6c.ServingNode{n.Serving, n.Additional} {
		if sn == nil {
			continue
		}

		if sn.MME != nil && sn.MME.Name != "" {
			out = append(out, target{domain: domainMME, name: sn.MME.Name, realm: sn.MME.Realm, mmeNumber: sn.MME.Number})
		}

		if sn.SGSN != nil && sn.SGSN.Name != "" {
			out = append(out, target{domain: domainSGSN, name: sn.SGSN.Name, realm: sn.SGSN.Realm, sgsnNumber: sn.SGSN.Number})
		}
	}

	if n.SMSF3GPP != nil {
		out = append(out, target{domain: domainSMSF3GPP, name: n.SMSF3GPP.Name, realm: n.SMSF3GPP.Realm})
	}

	if n.SMSFNon3GPP != nil {
		out = append(out, target{domain: domainSMSFNon3GPP, name: n.SMSFNon3GPP.Name, realm: n.SMSFNon3GPP.Realm})
	}

	return out
}

// outcomeOf is the outcome of a forwarding over SGd: the answer of the node as it is, or a system failure of no
// answer.
func outcomeOf(d domain, ui []byte, err error) outcome {
	o := outcome{domain: d}

	var re *sgd.ResultError

	switch {
	case err == nil:
		o.ok, o.ui = true, ui
	case errors.As(err, &re):
		o.result, o.cause, o.diagnostic, o.absent = re.Result, re.DeliveryFailureCause, re.DiagnosticInfo, re.AbsentUserDiagnostic
	default:
		o.result = tgpp.Result{Code: diameter.ResultUnableToComply}
	}

	return o
}

// report reports the failed delivery to the HSS (TS 29.338 §5.2.3.3): of an absent user, an unidentified user or a
// full memory, the outcome of each domain tried. It returns the serving nodes the HSS answers with, if any, which the
// IP-SM-GW tries again at once.
func (a *AS) report(ctx context.Context, c mtContext, m sgd.MTForwardShortMessage, outcomes []outcome) (s6c.ServingNodes, bool) {
	rep := s6c.DeliveryReport{
		MSISDN: c.msisdn, ServiceCentreAddress: m.ServiceCentreAddress, SingleAttempt: c.single,
		SMSFSupport: true,
	}
	reported := false

	for _, o := range outcomes {
		cause, ok := o.deliveryCause()
		if !ok {
			continue
		}

		out := &s6c.DeliveryOutcome{Cause: cause, AbsentDiagnostic: o.absent}

		switch o.domain {
		case domainIMS:
			rep.IPSMGW = out
		case domainMME:
			rep.MME = out
		case domainSGSN:
			rep.SGSN = out
		case domainSMSF3GPP:
			rep.SMSF3GPP = out
		case domainSMSFNon3GPP:
			rep.SMSFNon3GPP = out
		}

		reported = true
	}

	if !reported || c.msisdn == "" {
		return s6c.ServingNodes{}, false
	}

	res, err := a.router.ReportSMDeliveryStatus(ctx, rep)
	if err != nil {
		a.log.Warn("delivery status not reported to the HSS", slog.String("msisdn", c.msisdn), slog.Any("error", err))
		return s6c.ServingNodes{}, false
	}

	alert := res.ServingNodes

	return alert, len(targets(alert)) > 0
}

// forwardAnswer is the MT Forward Short Message Answer of o (TS 29.338 §6.2.2.1).
func (a *AS) forwardAnswer(req *diameter.Message, id diameter.Identity, o outcome) *diameter.Message {
	var (
		ans *diameter.Message
		err error
	)

	switch {
	case o.ok:
		ans, err = sgd.NewMTForwardShortMessageAnswer(req, id, o.ui)
	case o.cause != nil:
		ans, err = sgd.NewDeliveryFailureAnswer(req, id, *o.cause, o.diagnostic)
	case o.result.IsExperimental(tgpp.ResultErrorAbsentUser):
		ans = sgd.NewAbsentUserAnswer(req, id, o.absent)
	default:
		ans = tgpp.NewResultAnswer(req, id, o.result)
	}

	if err != nil {
		return tgpp.NewErrorAnswer(req, id, err)
	}

	return ans
}
