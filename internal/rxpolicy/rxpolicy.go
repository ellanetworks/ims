// Package rxpolicy is the policy backend over Rx (TS 29.214).
package rxpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/policy"
)

type Diameter interface {
	Identity() diameter.Identity
	NewSessionID() string
	Send(ctx context.Context, req *diameter.Message, opts ...diameter.RequestOption) (*diameter.Message, error)
}

type Config struct {
	Diameter Diameter
	// Realm is the realm of the PCRF, which the Diameter node routes Rx to.
	Realm func() string
}

type Backend struct {
	cfg  Config
	sink atomic.Pointer[policy.Sink]
}

var _ policy.Backend = (*Backend)(nil)

func New(cfg Config) *Backend {
	return &Backend{cfg: cfg}
}

// Endpoint is Rx: each session keeps the PCRF that holds it in its ref, so stored sessions survive a change of the
// PCRF realm and go on to their PCRF.
func (b *Backend) Endpoint() string {
	return "rx"
}

func (b *Backend) NewSessionID() string {
	return b.cfg.Diameter.NewSessionID()
}

func (b *Backend) Bind(s policy.Sink) {
	if s == nil {
		b.sink.Store(nil)
		return
	}

	b.sink.Store(&s)
}

// session is what the P-CSCF keeps of an Rx session, its ref: the PCRF that holds it, how the PCRF binds it
// (RFC 6733 §8.17, §8.18), and the Class AVPs to send back (§8.20).
type session struct {
	Class    [][]byte                       `json:"class,omitempty"`
	PCRF     string                         `json:"pcrf,omitempty"`
	Realm    string                         `json:"realm,omitempty"`
	Binding  diameter.SessionBinding        `json:"binding,omitempty"`
	Failover diameter.SessionServerFailover `json:"failover,omitempty"`
}

func decodeRef(ref string) (session, error) {
	var s session

	if ref == "" {
		return s, nil
	}

	return s, json.Unmarshal([]byte(ref), &s)
}

func (s session) ref() (string, error) {
	b, err := json.Marshal(s)

	return string(b), err
}

// answered is the session as the PCRF that answered an AA-Request holds it.
func (s session) answered(ans *diameter.Message, a rx.AAAnswer) session {
	origin := tgpp.ParseEnvelope(ans).Origin

	// RFC 6733 §8.20: the Class of a PCRF is its own; another PCRF that takes the session over replaces it.
	if len(a.Class) > 0 || !strings.EqualFold(origin.OriginHost, s.PCRF) {
		s.Class = a.Class
	}

	s.PCRF, s.Realm = origin.OriginHost, origin.OriginRealm
	s.Binding, s.Failover = a.SessionBinding, a.SessionServerFailover

	return s
}

// undelivered is a request of a session that no path reaches its PCRF with, and that no other PCRF took: the session
// is lost, or, with ALLOW_SERVICE, goes on unbound as if the request succeeded (RFC 6733 §8.18).
type undelivered struct {
	allow bool
	err   error
}

func (e *undelivered) Error() string {
	return "the PCRF of the session is unreachable: " + e.err.Error()
}

func (e *undelivered) Unwrap() error {
	return e.err
}

// send sends a request of a session: to the PCRF that holds it, unless bound is false, else to the PCRF realm
// (RFC 6733 §8.17).
//
// A request its PCRF does not get yet, because its connection is down or it is busy, stays pending: the error is
// transient and the caller sends it again later (RFC 6733 §5.5.4). A request no path reaches its PCRF with is
// undelivered (§8.18): it goes once to the realm if the PCRF allows it, and the session then belongs to the PCRF that
// answers; otherwise the session is lost or, with ALLOW_SERVICE, goes on unbound. With TRY_AGAIN, a pending request
// goes to the realm too. A PCRF that does not answer in time is neither: its connection's watchdog decides
// (RFC 3539 §3.10).
func (b *Backend) send(ctx context.Context, id string, s session, bound bool,
	build func(tgpp.Envelope) (*diameter.Message, error), wait bool,
) (*diameter.Message, error) {
	realm := b.cfg.Realm()
	env := tgpp.Envelope{SessionID: id, Origin: b.cfg.Diameter.Identity(), DestinationRealm: realm}

	if !bound || s.PCRF == "" {
		return b.do(ctx, env, build, wait)
	}

	ans, err := b.sendBound(ctx, env, s, build, wait)

	failure := deliveryFailure(ans, err)
	if failure == settled || failure == pending && !s.Failover.TriesAgain() {
		return ans, err
	}

	if s.Failover.TriesAgain() {
		retried, retryErr := b.do(ctx, env, build, wait)
		if deliveryFailure(retried, retryErr) == settled {
			return retried, retryErr
		}

		if failure == pending {
			return ans, err
		}
	}

	return nil, &undelivered{allow: s.Failover.AllowsService(), err: deliveryError(ans, err)}
}

// sendBound sends a request to the PCRF of the session. A PCRF behind an agent may be in another realm than the
// configured one, which then has no route of its own: the request goes through the configured realm, still to the
// PCRF (TS 29.213 §7.3.5).
func (b *Backend) sendBound(ctx context.Context, env tgpp.Envelope, s session,
	build func(tgpp.Envelope) (*diameter.Message, error), wait bool,
) (*diameter.Message, error) {
	configured := env.DestinationRealm
	env.DestinationHost = s.PCRF

	if s.Realm != "" {
		env.DestinationRealm = s.Realm
	}

	ans, err := b.do(ctx, env, build, wait)

	if errors.Is(err, diameter.ErrUnableToDeliver) && !strings.EqualFold(env.DestinationRealm, configured) {
		env.DestinationRealm = configured
		ans, err = b.do(ctx, env, build, wait)
	}

	return ans, err
}

func (b *Backend) do(ctx context.Context, env tgpp.Envelope, build func(tgpp.Envelope) (*diameter.Message, error),
	wait bool,
) (*diameter.Message, error) {
	req, err := build(env)
	if err != nil {
		return nil, err
	}

	var opts []diameter.RequestOption
	if !wait {
		opts = append(opts, diameter.FailFast())
	}

	return b.cfg.Diameter.Send(ctx, req, opts...)
}

type failure int

const (
	// settled: the PCRF answered, or the request failed for another reason than its delivery.
	settled failure = iota
	pending
	unreachable
)

// deliveryFailure classifies how a request did not reach its PCRF. Pending: no connection to it now, or the PCRF
// answering it is too busy (RFC 6733 §7.1.3: it got the request). Unreachable: no path to it, or an agent answering
// that it cannot deliver the request.
func deliveryFailure(ans *diameter.Message, err error) failure {
	switch {
	case errors.Is(err, diameter.ErrUnableToDeliver), errors.Is(err, diameter.ErrApplicationUnsupported):
		return unreachable
	case errors.Is(err, diameter.ErrNotConnected):
		return pending
	case err != nil:
		return settled
	}

	switch protocolError(ans) {
	case diameter.ResultUnableToDeliver:
		return unreachable
	case diameter.ResultTooBusy:
		return pending
	}

	return settled
}

func deliveryError(ans *diameter.Message, err error) error {
	if err != nil {
		return err
	}

	return errors.New(diameter.ResultName(protocolError(ans)))
}

func protocolError(ans *diameter.Message) uint32 {
	if ans == nil || ans.Flags&diameter.FlagError == 0 {
		return 0
	}

	a, ok := ans.Find(diameter.AVPResultCode, 0)
	if !ok {
		return 0
	}

	code, err := a.Unsigned32()
	if err != nil {
		return 0
	}

	return code
}

// TS 29.214 §4.4.5, §5.3.13
func (b *Backend) OpenSignalling(ctx context.Context, id string, s policy.Signalling, wait bool) (string, error) {
	initial := rx.RequestInitial
	control := rx.MediaControl
	signalling := rx.FlowUsageAFSignalling

	r := rx.AARequest{
		MediaComponents: []rx.MediaComponent{{
			Number:        0,
			Type:          &control,
			SubComponents: []rx.MediaSubComponent{{FlowNumber: 0, FlowUsage: &signalling}},
		}},
		SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer},
		RequestType:     &initial,
	}

	framed(&r, s.UE)

	ans, err := b.send(ctx, id, session{}, false, func(env tgpp.Envelope) (*diameter.Message, error) {
		return rx.NewAARequest(env, r)
	}, wait)
	if err != nil {
		return "", classify(err)
	}

	a, err := rx.ParseAAAnswer(ans)
	if err != nil {
		return "", classify(err)
	}

	return session{}.answered(ans, a).ref()
}

// TS 29.214 §5.3.13, §5.4.1: FAILED_RESOURCES_ALLOCATION is a Rel8 feature, advertised in the same AA-Request.
var callActions = []rx.SpecificAction{
	rx.ActionChargingCorrelationExchange, rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer,
	rx.ActionIndicationOfFailedResourcesAllocation,
}

// TS 29.214 §4.4.1, §4.4.2, Annex A.3. An update goes to the PCRF of the session. One that cannot reach the PCRF of
// a session that allows the service succeeds, unbound (RFC 6733 §8.18 ALLOW_SERVICE: "assume that
// re-authorization succeeded").
func (b *Backend) Authorize(ctx context.Context, id, ref string, r policy.Request) (policy.Grant, error) {
	s, err := decodeRef(ref)
	if err != nil {
		return policy.Grant{}, err
	}

	kind := rx.RequestUpdate

	aar := rx.AARequest{
		AFApplicationIdentifier: r.Service,
		MediaComponents:         MediaComponents(r.Components),
		SubscriptionIDs:         SubscriptionIDs(r.Subscribers),
		SIPForkingIndication:    forking(r.Forking),
		RequestType:             &kind,
	}

	if r.Initial {
		kind = rx.RequestInitial
		aar.SpecificActions = callActions
		aar.Features = rx.FeatureRel8
	}

	framed(&aar, r.UE)

	if r.Initial {
		s = session{}
	}

	ans, err := b.send(ctx, id, s, !s.Binding.Has(diameter.SessionBindingReAuth), func(env tgpp.Envelope) (*diameter.Message, error) {
		return rx.NewAARequest(env, aar)
	}, false)

	var lost *undelivered
	if errors.As(err, &lost) && lost.allow {
		return unbound(s)
	}

	if err != nil {
		return policy.Grant{}, classify(err)
	}

	a, err := rx.ParseAAAnswer(ans)
	if err != nil {
		return policy.Grant{}, classify(err)
	}

	next, err := s.answered(ans, a).ref()
	if err != nil {
		return policy.Grant{}, err
	}

	return policy.Grant{Ref: next, Charging: AnswerCharging(a)}, nil
}

// unbound is the grant of a session whose PCRF could not be reached but allows the service: the next requests go to
// the PCRF realm.
func unbound(s session) (policy.Grant, error) {
	s.PCRF, s.Realm = "", ""

	ref, err := s.ref()
	if err != nil {
		return policy.Grant{}, err
	}

	return policy.Grant{Ref: ref}, nil
}

// TS 29.214 §4.4.4. An STR its PCRF does not get ends the session there too (RFC 6733 §8.18): it is not sent again,
// and, with ALLOW_SERVICE, succeeds.
func (b *Backend) Terminate(ctx context.Context, id, ref string, cause policy.Termination, wait bool) error {
	s, err := decodeRef(ref)
	if err != nil {
		return err
	}

	ans, err := b.send(ctx, id, s, !s.Binding.Has(diameter.SessionBindingSTR), func(env tgpp.Envelope) (*diameter.Message, error) {
		return rx.NewSessionTerminationRequest(env, rx.SessionTerminationRequest{Cause: termination(cause), Class: s.Class})
	}, wait)

	var lost *undelivered
	switch {
	case errors.As(err, &lost) && lost.allow:
		return nil
	case errors.As(err, &lost):
		return &policy.Error{Kind: policy.ErrSessionLost, Err: err}
	}

	if err == nil {
		_, err = rx.ParseSessionTerminationAnswer(ans)
	}

	if err != nil {
		return classify(err)
	}

	return nil
}

// TS 29.214 §4.4.6.3
func (b *Backend) ReAuth(id string, r rx.ReAuthRequest) bool {
	s := b.sink.Load()
	if s == nil {
		return false
	}

	return (*s).Notify(id, event(r))
}

// TS 29.214 §4.4.6.1
func (b *Backend) AbortSession(id string, r rx.AbortSessionRequest) (terminate func(), known bool) {
	s := b.sink.Load()
	if s == nil {
		return nil, false
	}

	return (*s).Abort(id, policy.Abort{Cause: r.Cause.String(), InsufficientResources: r.Cause == rx.AbortInsufficientBearerResources})
}

func framed(r *rx.AARequest, ue netip.Addr) {
	if ue.Is4() {
		r.FramedIPAddress = ue
	} else {
		r.FramedIPv6Address = ue
	}
}

// TS 29.214 §4.4.1, §4.4.4, §4.4.5
func classify(err error) error {
	e := &policy.Error{Err: err}

	result, answered := tgpp.ResultOf(err)
	if answered {
		e.Result = result.String()
	}

	var (
		refused *rx.ResultError
		lost    *undelivered
	)

	switch {
	case errors.As(err, &lost):
		e.Kind = policy.ErrSessionLost
	case answered && !result.Experimental && result.Code == diameter.ResultUnknownSessionID:
		e.Kind = policy.ErrUnknownSession
	case errors.As(err, &refused), errors.Is(err, diameter.ErrApplicationUnsupported):
		e.Kind = policy.ErrRefused
	case errors.Is(err, rx.ErrMalformedAnswer):
		e.Kind = policy.ErrMalformed
	case errors.Is(err, diameter.ErrNotConnected), errors.Is(err, diameter.ErrUnableToDeliver):
		e.Kind = policy.ErrUnreachable
	}

	var aa *rx.AAError
	if errors.As(err, &aa) && aa.Code == tgpp.ResultRequestedServiceTemporarilyNotAuthorized && aa.RetryInterval > 0 {
		e.RetryAfter = aa.RetryInterval
	}

	e.Transient = unanswered(err)

	return e
}

// RFC 6733 §7.1.3, §7.1.4, §8.4.2: any answer ends the request at the PCRF, except one that asks for a retry.
func unanswered(err error) bool {
	var lost *undelivered

	if err == nil || errors.As(err, &lost) || errors.Is(err, rx.ErrMalformedAnswer) || errors.Is(err, diameter.ErrApplicationUnsupported) ||
		errors.Is(err, diameter.ErrClosed) {
		return false
	}

	r, ok := tgpp.ResultOf(err)
	if !ok {
		return true
	}

	return r.Transient() || !r.Experimental && (r.Code == diameter.ResultUnableToDeliver || r.Code == diameter.ResultTooBusy)
}

func termination(t policy.Termination) rx.TerminationCause {
	switch t {
	case policy.TerminationLogout:
		return rx.TerminationLogout
	case policy.TerminationExpired:
		return rx.TerminationAuthExpired
	case policy.TerminationBadAnswer:
		return rx.TerminationBadAnswer
	}

	return rx.TerminationAdministrative
}

func forking(f policy.Forking) rx.SIPForkingIndication {
	if f == policy.ForkingSeveralDialogues {
		return rx.ForkingSeveralDialogues
	}

	return rx.ForkingSingleDialogue
}

// event returns what the RAR reports (TS 29.214 §4.4.6.2, §4.4.6.5). A FAILED_RESOURCES_ALLOCATION is about the
// media components whose PCC/QoS rules are INACTIVE, the default without Media-Component-Status (§4.4.2, §5.3.48):
// a failed modification keeps the previous rules active (TS 29.212 §4.5.12). No flows means every media component.
func event(r rx.ReAuthRequest) policy.Event {
	var e policy.Event

	for _, a := range r.SpecificActions {
		e.Kinds = append(e.Kinds, eventKind(a))
	}

	failedOnly := slices.Contains(r.SpecificActions, rx.ActionIndicationOfFailedResourcesAllocation) &&
		!slices.Contains(r.SpecificActions, rx.ActionIndicationOfLossOfBearer) &&
		!slices.Contains(r.SpecificActions, rx.ActionIndicationOfReleaseOfBearer)

	for _, f := range r.Flows {
		if failedOnly && f.MediaComponentStatus != nil && *f.MediaComponentStatus == rx.MediaComponentActive {
			continue
		}

		e.Components = append(e.Components, f.MediaComponentNumber)
	}

	if failedOnly && len(r.Flows) > 0 && len(e.Components) == 0 {
		e.Kinds = slices.DeleteFunc(e.Kinds, func(k policy.EventKind) bool { return k == policy.EventResourcesFailed })
	}

	if slices.Contains(r.SpecificActions, rx.ActionChargingCorrelationExchange) {
		e.Charging = new(charging(r.AccessNetworkChargingIdentifiers, r.AccessNetworkChargingAddress, r.AccessNetwork))
	}

	return e
}

func eventKind(a rx.SpecificAction) policy.EventKind {
	switch a {
	case rx.ActionChargingCorrelationExchange:
		return policy.EventChargingCorrelation
	case rx.ActionIndicationOfLossOfBearer:
		return policy.EventBearerLost
	case rx.ActionIndicationOfReleaseOfBearer:
		return policy.EventBearerReleased
	case rx.ActionIndicationOfFailedResourcesAllocation:
		return policy.EventResourcesFailed
	}

	return policy.EventOther
}

// AnswerCharging returns the access network charging information of an AA-Answer (TS 29.214 §5.3.2, §5.3.3).
func AnswerCharging(a rx.AAAnswer) policy.Charging {
	return charging(a.AccessNetworkChargingIdentifiers, a.AccessNetworkChargingAddress, a.AccessNetwork)
}

func charging(ids []rx.AccessNetworkChargingIdentifier, addr netip.Addr, n rx.AccessNetwork) policy.Charging {
	c := policy.Charging{Address: addr}

	switch {
	case n.IPCANType == nil:
	// TS 24.229 §7.2A.5.2.10 covers 5GS whatever its access (TS 29.214 Table E.2-1). EPS over non-3GPP access
	// is §7.2A.5.2.3, which defines no ecid.
	case *n.IPCANType == rx.IPCAN3GPPEPS:
		c.Access = policy.AccessEPS
	case *n.IPCANType == rx.IPCAN3GPP5GS, *n.IPCANType == rx.IPCANNon3GPP5GS:
		c.Access = policy.Access5GS
	default:
		c.Access = policy.AccessOther
	}

	for _, id := range ids {
		cid := policy.ChargingID{Value: id.Value}

		for _, f := range id.Flows {
			cid.Flows = append(cid.Flows, policy.Flows{Component: f.MediaComponentNumber, FlowNumbers: f.FlowNumbers})
		}

		c.Identifiers = append(c.Identifiers, cid)
	}

	return c
}

// SubscriptionIDs returns the Subscription-Id AVPs for the subscribers (TS 29.214 §5.4, RFC 4006 §8.46).
func SubscriptionIDs(subs []policy.Subscriber) []rx.SubscriptionID {
	var out []rx.SubscriptionID

	for _, s := range subs {
		t := rx.SubscriptionIDSIPURI
		if s.Kind == policy.SubscriberE164 {
			t = rx.SubscriptionIDE164
		}

		out = append(out, rx.SubscriptionID{Type: t, Data: s.ID})
	}

	return out
}

// MediaComponents returns the Media-Component-Description AVPs for the components (TS 29.214 §5.3.7).
func MediaComponents(cs []policy.MediaComponent) []rx.MediaComponent {
	if cs == nil {
		return nil
	}

	out := make([]rx.MediaComponent, 0, len(cs))

	for _, c := range cs {
		out = append(out, mediaComponent(c))
	}

	return out
}

func mediaComponent(c policy.MediaComponent) rx.MediaComponent {
	out := rx.MediaComponent{Number: c.Number, Type: new(mediaType(c.Type)), RRBandwidth: c.RR, RSBandwidth: c.RS}

	if c.Status != 0 {
		out.FlowStatus = new(flowStatus(c.Status))
	}

	if c.MaxRequestedUL != nil {
		out.MaxRequestedBandwidthUL = new(rx.Bandwidth(*c.MaxRequestedUL))
	}

	if c.MaxRequestedDL != nil {
		out.MaxRequestedBandwidthDL = new(rx.Bandwidth(*c.MaxRequestedDL))
	}

	for _, codec := range c.Codecs {
		d := rx.CodecData{Direction: rx.CodecDownlink, Kind: rx.CodecOffer, SDP: codec.SDP}

		if codec.Uplink {
			d.Direction = rx.CodecUplink
		}

		if codec.Answer {
			d.Kind = rx.CodecAnswer
		}

		out.CodecData = append(out.CodecData, d)
	}

	for _, s := range c.SubComponents {
		sub := rx.MediaSubComponent{FlowNumber: s.FlowNumber}

		for _, f := range s.Flows {
			sub.FlowDescriptions = append(sub.FlowDescriptions, flowDescription(f))
		}

		switch s.Usage {
		case policy.FlowUsageRTCP:
			sub.FlowUsage = new(rx.FlowUsageRTCP)
		case policy.FlowUsageAFSignalling:
			sub.FlowUsage = new(rx.FlowUsageAFSignalling)
		}

		out.SubComponents = append(out.SubComponents, sub)
	}

	return out
}

// TS 29.214 §5.3.8
func flowDescription(f policy.Flow) string {
	d := rx.FlowDescription{
		Direction: rx.FlowDirectionOut, Protocol: rx.Protocol(f.Protocol),
		Source: f.Source, Destination: f.Destination, DestinationPort: f.DestinationPort,
	}

	if f.Uplink {
		d.Direction = rx.FlowDirectionIn
	}

	return d.String()
}

func mediaType(t policy.MediaType) rx.MediaType {
	switch t {
	case policy.MediaAudio:
		return rx.MediaAudio
	case policy.MediaVideo:
		return rx.MediaVideo
	case policy.MediaData:
		return rx.MediaData
	case policy.MediaApplication:
		return rx.MediaApplication
	case policy.MediaControl:
		return rx.MediaControl
	case policy.MediaText:
		return rx.MediaText
	case policy.MediaMessage:
		return rx.MediaMessage
	}

	return rx.MediaOther
}

func flowStatus(s policy.FlowStatus) rx.FlowStatus {
	switch s {
	case policy.FlowEnabledUplink:
		return rx.FlowStatusEnabledUplink
	case policy.FlowEnabledDownlink:
		return rx.FlowStatusEnabledDownlink
	case policy.FlowDisabled:
		return rx.FlowStatusDisabled
	case policy.FlowRemoved:
		return rx.FlowStatusRemoved
	}

	return rx.FlowStatusEnabled
}
