package ipsmgw

import (
	"context"
	"errors"
	"log/slog"
	"slices"
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
// 3a to 3c; TS 24.341 §5.3.3.3; TS 29.338 §5.2.1.4 and TS 23.040 §8.1.4, which TS 23.204 has the IP-SM-GW follow as an
// SMS Router): it asks the HSS for the serving nodes of the user, and answers with only itself as serving node, of an
// MT Correlation ID in place of the IMSI, which stands for the user and the serving nodes; the User Identifier Alert,
// MWD Status and absent user diagnostics of the HSS are transferred. An error of the HSS is forwarded with the same
// result, but an absent user, or no answer, of a user the IP-SM-GW has a public user identity of, which it delivers
// to over IMS; a protocol error of the HSS is a System Failure (RFC 6733 §7.1.3: per hop). Of a request of no intended
// delivery, the IP-SM-GW answers with the IMSI, or MCC+MNC+dummy MSIN, of the HSS, of no MT Correlation ID.
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
		Priority: r.Priority, SingleAttempt: r.SingleAttempt, DeliveryNotIntended: r.DeliveryNotIntended,
		SMSFSupport: true,
	})

	reg, known := a.reg.byUser(r.MSISDN, routing.IMSI)

	var re *s6c.ResultError

	switch {
	case err == nil:
	case errors.As(err, &re) && re.Experimental && (!known || !re.IsExperimental(tgpp.ResultErrorAbsentUser)):
		ans, aerr := s6c.NewSendRoutingInfoForSMErrorAnswer(req, id, *re, r.SMSFSupport)
		if aerr != nil {
			return s6c.NewErrorAnswer(req, id, aerr)
		}

		return ans
	case !known || r.DeliveryNotIntended != nil:
		log.Warn("routing information of the HSS unavailable", slog.Any("error", err))
		return s6c.NewAnswer(req, id, tgpp.Result{Code: diameter.ResultUnableToComply})
	default:
		// Over IMS only.
		routing = s6c.Routing{IMSI: reg.imsi}
		if re != nil {
			routing.MWDStatus, routing.Absent, routing.AlertMSISDN = re.MWDStatus, re.Absent, re.AlertMSISDN
		}
	}

	self := s6c.ServingNodes{Serving: &s6c.ServingNode{IPSMGW: &s6c.NodeAddress{
		Name: id.OriginHost, Realm: id.OriginRealm, Number: number,
	}}}

	corr := routing.IMSI

	if r.DeliveryNotIntended == nil {
		prefix := a.plmn
		if len(routing.IMSI) >= 6 {
			// The MCC and MNC, of a 2-digit MNC the first digit of the MSIN after it (TS 23.040 §3.2.7a).
			prefix = routing.IMSI[:6]
		}

		if corr, err = a.corr.add(prefix, mtContext{
			msisdn: r.MSISDN, imsi: routing.IMSI, routing: routing,
			single: r.SingleAttempt,
		}, a.agent.Layer().Clock().Now()); err != nil {
			return s6c.NewErrorAnswer(req, id, err)
		}
	}

	ans, err := s6c.NewSendRoutingInfoForSMAnswer(req, id, s6c.Routing{
		ServingNodes: self, IMSI: corr, MWDStatus: routing.MWDStatus, Absent: routing.Absent,
		AlertMSISDN: routing.AlertMSISDN,
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
	// domainNone is of no domain to deliver in.
	domainNone
)

func (d domain) String() string {
	return [...]string{"ims", "mme", "sgsn", "smsf_3gpp", "smsf_non_3gpp", "none"}[d]
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
	// target is the serving node of a NAS domain.
	target target
	// tried is false of IMS when the user could not receive short messages over IP.
	tried bool
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

// The time the IP-SM-GW keeps, of the SM-Delivery-Timer: for the delivery report to the HSS, and the least an attempt
// needs to be made; and the least the UE has to report a delivery over IMS, past its TR2M (TS 24.011 §10: "greater
// than 12 seconds").
const (
	reportReserve = 3 * time.Second
	minAttempt    = time.Second
	minIMSWait    = 15 * time.Second
	// nasReserve is the least the IP-SM-GW keeps for NAS when the UE has less than minIMSWait over IMS.
	nasReserve = 5 * time.Second
)

// ForwardShortMessage delivers a short message of the SMS-GMSC to the user of its MT Correlation ID (TS 23.204
// §6.4 steps 4 to 9, §6.5a): in the domains in turn, IMS first when the user can receive short messages over IP,
// then the MME, SGSN and SMSFs the HSS gave, until one delivers it or the UE's memory capacity is exceeded, within
// the SM-Delivery-Timer less reportReserve. When every domain failed of an absent user or a full memory, the
// IP-SM-GW reports it to the HSS (TS 29.338 §5.2.3.3), and tries again at once the serving nodes the HSS answers
// with, if any. The SMS-GMSC gets the outcome of the last attempt, of an error of a domain rather than of a system
// failure. An MT Correlation ID of no context is invalid: a System Failure (TS 23.040 §8.1.4).
func (a *AS) ForwardShortMessage(ctx context.Context, id diameter.Identity, req *diameter.Message) *diameter.Message {
	m, err := sgd.ParseMTForwardShortMessageRequest(req)
	if err != nil {
		return tgpp.NewErrorAnswer(req, id, err)
	}

	now := a.agent.Layer().Clock().Now()

	c, ok := a.corr.get(m.IMSI, now)
	if !ok {
		a.log.Debug("short message of an unknown MT Correlation ID")
		return tgpp.NewResultAnswer(req, id, tgpp.Result{Code: diameter.ResultUnableToComply})
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

	left := max(timer-elapsed-deliveryMargin, 0)

	ctx, cancel := context.WithTimeout(ctx, left)
	defer cancel()

	actx, acancel := context.WithTimeout(ctx, max(left-reportReserve, 0))
	defer acancel()

	log := a.log.With(slog.String("msisdn", c.msisdn))

	outcomes := a.attempt(actx, c, m, c.routing.ServingNodes, true)
	last := final(outcomes)

	if !last.ok {
		alert, ok := a.report(ctx, c, m, outcomes)
		if ok && !last.memoryExceeded() && ctx.Err() == nil {
			log.Debug("retrying the delivery via the serving nodes the HSS reported")

			if retry := a.attempt(ctx, c, m, alert, false); len(retry) > 0 {
				last = final(append(outcomes, retry...))
			}
		}

		// The UE not reachable for IP (TS 23.204 §6.5a step 17: UNRI set locally).
		if !last.ok && slices.ContainsFunc(outcomes, func(o outcome) bool {
			return o.domain == domainIMS && o.tried && o.result.IsExperimental(tgpp.ResultErrorAbsentUser)
		}) {
			a.reg.unreachable(c.msisdn, c.imsi)
		}
	}

	a.metrics.count(directionMT, last.domain.String(), last.ok)

	log.Info("mobile terminated short message", slog.String("domain", last.domain.String()), slog.Bool("delivered", last.ok),
		slog.String("result", last.result.String()))

	return a.forwardAnswer(req, id, last)
}

// final is the outcome the SMS-GMSC gets of outcomes: the last, unless a System Failure, as of a node not connected,
// after the error of a domain, which is the user's.
func final(outcomes []outcome) outcome {
	last := outcomes[len(outcomes)-1]
	if last.ok || last.result.Experimental || last.cause != nil {
		return last
	}

	for i := len(outcomes) - 2; i >= 0; i-- {
		if o := outcomes[i]; o.result.Experimental && o.tried {
			return o
		}
	}

	return last
}

// attempt delivers the short message in the domains in turn, IMS first if ims and the user can receive short
// messages over IP, then those of nodes, until one delivers it, the memory capacity is exceeded, or too little of ctx
// is left to make an attempt; it returns the outcome of each attempt, of at least one. Over IMS, the UE has half of
// what is left to report the delivery when NAS is left to try (TS 23.204 §6.5a NOTE 1), but no less than minIMSWait
// if NAS keeps nasReserve.
// Without IMS, the outcome of IMS is an absent user, deregistered for IMS (TS 23.040 §3.3.1 Table 1a), for the HSS
// to know the UE not reachable for IP (TS 23.204 §6.5a step 17).
func (a *AS) attempt(ctx context.Context, c mtContext, m sgd.MTForwardShortMessage, nodes s6c.ServingNodes, ims bool) []outcome {
	var outcomes []outcome

	left := func() time.Duration {
		if deadline, ok := ctx.Deadline(); ok {
			return time.Until(deadline)
		}

		return defaultDeliveryTimer
	}

	done := func(o outcome) bool {
		outcomes = append(outcomes, o)
		return o.ok || o.memoryExceeded()
	}

	nas := targets(nodes)
	if c.imsi == "" {
		nas = nil
	}

	if ims {
		reg, ok := a.reg.availableFor(c.msisdn, c.imsi)

		switch {
		case !ok:
			d := tgpp.AbsentUserDeregisteredIMS
			outcomes = append(outcomes, outcome{domain: domainIMS, result: tgpp.Experimental(tgpp.ResultErrorAbsentUser), absent: &d})
		case left() >= minAttempt:
			wait := left()
			if len(nas) > 0 {
				wait = max(wait/2, min(minIMSWait, wait-nasReserve))
			}

			ictx, cancel := context.WithTimeout(ctx, wait)
			o := a.deliverIMS(ictx, reg, m)

			cancel()

			o.tried = true

			if done(o) {
				return outcomes
			}
		}
	}

	for _, t := range nas {
		if left() < minAttempt {
			break
		}

		mt := m
		mt.IMSI = c.imsi
		mt.MMENumberForMTSMS, mt.SGSNNumber = t.mmeNumber, t.sgsnNumber

		ui, err := a.nodes.Forward(ctx, t.name, t.realm, mt)

		o := outcomeOf(t.domain, ui, err)
		o.target, o.tried = t, true

		if done(o) {
			return outcomes
		}
	}

	if len(outcomes) == 0 || !slices.ContainsFunc(outcomes, func(o outcome) bool { return o.tried }) {
		// No domain to deliver in, or no time to: the user is absent.
		d := tgpp.AbsentUserDeregisteredIMS
		outcomes = append(outcomes, outcome{domain: domainNone, result: tgpp.Experimental(tgpp.ResultErrorAbsentUser), absent: &d})
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

// outcomeOf is the outcome of a forwarding over SGd: the answer of the node as it is, of an error of the application,
// else a System Failure.
func outcomeOf(d domain, ui []byte, err error) outcome {
	o := outcome{domain: d}

	var re *sgd.ResultError

	switch {
	case err == nil:
		o.ok, o.ui = true, ui
	case errors.As(err, &re) && re.Experimental:
		o.result, o.cause, o.diagnostic, o.absent = re.Result, re.DeliveryFailureCause, re.DiagnosticInfo, re.AbsentUserDiagnostic
	default:
		// No answer, or a protocol error, which is of the hop (RFC 6733 §7.1.3): a System Failure.
		o.result = tgpp.Result{Code: diameter.ResultUnableToComply}
	}

	return o
}

// report reports the failed delivery to the HSS (TS 29.338 §5.2.3.3): of an absent user, an unidentified user or a
// full memory, the outcome of each domain, and the serving nodes that failed of an absent user, as the HSS gave them,
// for it to answer with others it may have. It returns those, if any, which the IP-SM-GW tries again at once. It has
// what is left of ctx, past the attempts, and no less than reportReserve.
func (a *AS) report(ctx context.Context, c mtContext, m sgd.MTForwardShortMessage, outcomes []outcome) (s6c.ServingNodes, bool) {
	rep := s6c.DeliveryReport{
		MSISDN: c.msisdn, ServiceCentreAddress: m.ServiceCentreAddress, SingleAttempt: c.single,
		SMSFSupport: true,
	}
	reported := false
	nodes := c.routing.ServingNodes

	for _, o := range outcomes {
		cause, ok := o.deliveryCause()
		if !ok {
			continue
		}

		out := &s6c.DeliveryOutcome{Cause: cause, AbsentDiagnostic: o.absent}
		absent := cause == s6c.DeliveryCauseAbsentUser

		switch o.domain {
		case domainIMS:
			rep.IPSMGW = out
		case domainMME, domainSGSN:
			if o.domain == domainMME {
				rep.MME = out
			} else {
				rep.SGSN = out
			}

			if absent {
				switch {
				case servedBy(nodes.Serving, o.target):
					rep.Failed.Serving = nodes.Serving
				case servedBy(nodes.Additional, o.target):
					rep.Failed.Additional = nodes.Additional
				}
			}
		case domainSMSF3GPP:
			rep.SMSF3GPP = out

			if absent {
				rep.Failed.SMSF3GPP = nodes.SMSF3GPP
			}
		case domainSMSFNon3GPP:
			rep.SMSFNon3GPP = out

			if absent {
				rep.Failed.SMSFNon3GPP = nodes.SMSFNon3GPP
			}
		default:
			continue
		}

		reported = true
	}

	if rep.Failed.Additional != nil && rep.Failed.Serving == nil {
		rep.Failed.Additional = nil
	}

	if !reported || c.msisdn == "" {
		return s6c.ServingNodes{}, false
	}

	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) < reportReserve {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), reportReserve)
		defer cancel()
	}

	res, err := a.router.ReportSMDeliveryStatus(ctx, rep)
	if err != nil {
		a.log.Warn("delivery status not reported to the HSS", slog.String("msisdn", c.msisdn), slog.Any("error", err))
		return s6c.ServingNodes{}, false
	}

	alert := res.ServingNodes

	return alert, len(targets(alert)) > 0
}

// servedBy reports whether t is the MME or SGSN of sn.
func servedBy(sn *s6c.ServingNode, t target) bool {
	if sn == nil {
		return false
	}

	return sn.MME != nil && t.domain == domainMME && sn.MME.Name == t.name ||
		sn.SGSN != nil && t.domain == domainSGSN && sn.SGSN.Name == t.name
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
