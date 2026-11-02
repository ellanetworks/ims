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
	"github.com/ellanetworks/ims/sip/lifetime"
)

type Router interface {
	SendRoutingInfoForSM(ctx context.Context, req s6c.RoutingRequest) (s6c.Routing, error)
	ReportSMDeliveryStatus(ctx context.Context, rep s6c.DeliveryReport) (s6c.ReportResult, error)
}

type Nodes interface {
	Forward(ctx context.Context, host, realm string, m sgd.MTForwardShortMessage) ([]byte, error)
}

const defaultDeliveryTimer = 30 * time.Second

const deliveryMargin = 2 * time.Second

// TS 23.204 clause 6.4
// TS 24.341 clause 5.3.3.3
// TS 29.338 clause 5.2.1.4
// TS 23.040 clause 8.1.4
// RFC 6733 section 7.1.3
func (a *AS) RoutingInfo(ctx context.Context, id diameter.Identity, req *diameter.Message) *diameter.Message {
	r, err := s6c.ParseSendRoutingInfoForSMRequest(req)
	if err != nil {
		return s6c.NewErrorAnswer(req, id, err)
	}

	log := a.log.With(slog.String("msisdn", r.MSISDN))

	number := a.number()

	switch {
	case r.MSISDN == "":
		return s6c.NewAnswer(req, id, tgpp.Experimental(tgpp.ResultErrorUserUnknown))
	case number == "":
		log.WarnContext(ctx, "routing information request without an IP-SM-GW number")
		return s6c.NewAnswer(req, id, tgpp.Result{Code: diameter.ResultUnableToComply})
	}

	routing, err := a.router.SendRoutingInfoForSM(ctx, s6c.RoutingRequest{
		MSISDN: r.MSISDN, ServiceCentreAddress: r.ServiceCentreAddress, MTI: r.MTI, SMEA: r.SMEA, GPRSIndicator: true,
		Priority: r.Priority, SingleAttempt: r.SingleAttempt, DeliveryNotIntended: r.DeliveryNotIntended,
		SMSFSupport: true,
	})

	var re *s6c.ResultError

	imsi := routing.IMSI
	if errors.As(err, &re) {
		imsi = re.IMSI
	}

	reg, known := a.reg.byUser(r.MSISDN, imsi)

	switch {
	case err == nil:
	case re != nil && re.Experimental && (!known || !re.IsExperimental(tgpp.ResultErrorAbsentUser)):
		ans, aerr := s6c.NewSendRoutingInfoForSMErrorAnswer(req, id, *re, r.SMSFSupport)
		if aerr != nil {
			return s6c.NewErrorAnswer(req, id, aerr)
		}

		return ans
	case !known || r.DeliveryNotIntended != nil:
		log.WarnContext(ctx, "routing information of the HSS unavailable", slog.Any("error", err))
		return s6c.NewAnswer(req, id, tgpp.Result{Code: diameter.ResultUnableToComply})
	default:
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

	log.DebugContext(ctx, "routing information answered", slog.Bool("ims", known))

	return ans
}

type domain int

const (
	domainIMS domain = iota
	domainMME
	domainSGSN
	domainSMSF3GPP
	domainSMSFNon3GPP
	domainNone
)

func (d domain) String() string {
	return [...]string{"ims", "mme", "sgsn", "smsf_3gpp", "smsf_non_3gpp", "none"}[d]
}

type outcome struct {
	domain     domain
	ok         bool
	ui         []byte
	result     tgpp.Result
	cause      *sgd.DeliveryFailureCause
	diagnostic []byte
	absent     *tgpp.AbsentUserDiagnostic
	target     target
	tried      bool
}

func (o outcome) memoryExceeded() bool {
	return o.cause != nil && *o.cause == sgd.CauseMemoryCapacityExceeded
}

// TS 29.338 clause 5.2.3.3
func (o outcome) deliveryCause() (s6c.DeliveryCause, bool) {
	switch {
	case o.memoryExceeded():
		return s6c.DeliveryCauseMemoryCapacityExceeded, true
	case o.result.IsExperimental(tgpp.ResultErrorAbsentUser), o.result.IsExperimental(tgpp.ResultErrorUserUnknown):
		return s6c.DeliveryCauseAbsentUser, true
	}

	return 0, false
}

// TS 24.011 clause 10
const (
	reportReserve = 3 * time.Second
	minAttempt    = time.Second
	minIMSWait    = 15 * time.Second
	nasReserve    = 5 * time.Second
)

// TS 23.204 clause 6.4
// TS 23.204 clause 6.5a
// TS 29.338 clause 5.2.3.3
// TS 23.040 clause 8.1.4
func (a *AS) ForwardShortMessage(ctx context.Context, id diameter.Identity, req *diameter.Message) *diameter.Message {
	m, err := sgd.ParseMTForwardShortMessageRequest(req)
	if err != nil {
		return tgpp.NewErrorAnswer(req, id, err)
	}

	now := a.agent.Layer().Clock().Now()

	c, ok := a.corr.get(m.IMSI, now)
	if !ok {
		a.log.DebugContext(ctx, "short message of an unknown MT Correlation ID")
		return tgpp.NewResultAnswer(req, id, tgpp.Result{Code: diameter.ResultUnableToComply})
	}

	timer := m.DeliveryTimer
	if timer <= 0 {
		timer = defaultDeliveryTimer
	}

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
			log.DebugContext(ctx, "retrying the delivery via the serving nodes the HSS reported")

			if retry := a.attempt(ctx, c, m, alert, false); len(retry) > 0 {
				last = final(append(outcomes, retry...))
			}
		}

		if !last.ok && slices.ContainsFunc(outcomes, func(o outcome) bool {
			return o.domain == domainIMS && o.tried && o.result.IsExperimental(tgpp.ResultErrorAbsentUser)
		}) {
			a.reg.unreachable(c.msisdn, c.imsi)
		}
	}

	a.metrics.count(directionMT, last.domain.String(), last.ok)

	log.InfoContext(ctx, "mobile terminated short message", slog.String("domain", last.domain.String()), slog.Bool("delivered", last.ok),
		slog.String("result", last.result.String()))

	return a.forwardAnswer(req, id, last)
}

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

// TS 23.204 clause 6.5a
// TS 23.040 clause 3.3.1 table 1a
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
		d := tgpp.AbsentUserDeregisteredIMS
		outcomes = append(outcomes, outcome{domain: domainNone, result: tgpp.Experimental(tgpp.ResultErrorAbsentUser), absent: &d})
	}

	return outcomes
}

type target struct {
	domain                domain
	name, realm           string
	mmeNumber, sgsnNumber string
}

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

func outcomeOf(d domain, ui []byte, err error) outcome {
	o := outcome{domain: d}

	var re *sgd.ResultError

	switch {
	case err == nil:
		o.ok, o.ui = true, ui
	case errors.As(err, &re) && re.Experimental:
		o.result, o.cause, o.diagnostic, o.absent = re.Result, re.DeliveryFailureCause, re.DiagnosticInfo, re.AbsentUserDiagnostic
	default:
		o.result = tgpp.Result{Code: diameter.ResultUnableToComply}
	}

	return o
}

// TS 29.338 clause 5.2.3.3
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

		ctx, cancel = context.WithTimeout(lifetime.With(a.life, ctx), reportReserve)
		defer cancel()
	}

	res, err := a.router.ReportSMDeliveryStatus(ctx, rep)
	if err != nil {
		a.log.WarnContext(ctx, "delivery status not reported to the HSS", slog.String("msisdn", c.msisdn), slog.Any("error", err))
		return s6c.ServingNodes{}, false
	}

	alert := res.ServingNodes

	return alert, len(targets(alert)) > 0
}

func servedBy(sn *s6c.ServingNode, t target) bool {
	if sn == nil {
		return false
	}

	return sn.MME != nil && t.domain == domainMME && sn.MME.Name == t.name ||
		sn.SGSN != nil && t.domain == domainSGSN && sn.SGSN.Name == t.name
}

// TS 29.338 clause 6.2.2.1
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
