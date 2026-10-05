// Package rxpolicy is the policy backend over Rx (TS 29.214).
package rxpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"sync/atomic"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/policy"
)

type Diameter interface {
	Identity() diameter.Identity
	NewSessionID() string
	Do(ctx context.Context, peerID string, req *diameter.Message, opts ...diameter.DoOption) (*diameter.Message, error)
}

type PCRF struct {
	ID    string
	Host  string
	Realm string
}

type Config struct {
	Diameter Diameter
	PCRF     PCRF
}

type Backend struct {
	cfg  Config
	sink atomic.Pointer[policy.Sink]
}

var _ policy.Backend = (*Backend)(nil)

func New(cfg Config) *Backend {
	return &Backend{cfg: cfg}
}

func (b *Backend) Endpoint() string {
	return "rx:" + b.cfg.PCRF.Host
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

func (b *Backend) envelope(id string) tgpp.Envelope {
	return tgpp.Envelope{
		SessionID:        id,
		Origin:           b.cfg.Diameter.Identity(),
		DestinationHost:  b.cfg.PCRF.Host,
		DestinationRealm: b.cfg.PCRF.Realm,
	}
}

func (b *Backend) do(ctx context.Context, req *diameter.Message, wait bool) (*diameter.Message, error) {
	var opts []diameter.DoOption
	if !wait {
		opts = append(opts, diameter.FailFast())
	}

	return b.cfg.Diameter.Do(ctx, b.cfg.PCRF.ID, req, opts...)
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

	req, err := rx.NewAARequest(b.envelope(id), r)
	if err != nil {
		return "", err
	}

	ans, err := b.do(ctx, req, wait)
	if err != nil {
		return "", classify(err)
	}

	a, err := rx.ParseAAAnswer(ans)
	if err != nil {
		return "", classify(err)
	}

	return encodeClass(a.Class)
}

// TS 29.214 §5.3.13, §5.4.1: FAILED_RESOURCES_ALLOCATION is a Rel8 feature, advertised in the same AA-Request.
var callActions = []rx.SpecificAction{
	rx.ActionChargingCorrelationExchange, rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer,
	rx.ActionIndicationOfFailedResourcesAllocation,
}

// TS 29.214 §4.4.1, §4.4.2, Annex A.3
func (b *Backend) Authorize(ctx context.Context, id, _ string, r policy.Request) (policy.Grant, error) {
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

	req, err := rx.NewAARequest(b.envelope(id), aar)
	if err != nil {
		return policy.Grant{}, err
	}

	ans, err := b.do(ctx, req, false)
	if err != nil {
		return policy.Grant{}, classify(err)
	}

	a, err := rx.ParseAAAnswer(ans)
	if err != nil {
		return policy.Grant{}, classify(err)
	}

	ref, err := encodeClass(a.Class)
	if err != nil {
		return policy.Grant{}, err
	}

	return policy.Grant{Ref: ref, Charging: AnswerCharging(a)}, nil
}

// TS 29.214 §4.4.4
func (b *Backend) Terminate(ctx context.Context, id, ref string, cause policy.Termination, wait bool) error {
	class, err := decodeClass(ref)
	if err != nil {
		return err
	}

	req, err := rx.NewSessionTerminationRequest(b.envelope(id), rx.SessionTerminationRequest{
		Cause: termination(cause), Class: class,
	})
	if err != nil {
		return err
	}

	ans, err := b.do(ctx, req, wait)
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

	var refused *rx.ResultError

	switch {
	case answered && !result.Experimental && result.Code == diameter.ResultUnknownSessionID:
		e.Kind = policy.ErrUnknownSession
	case errors.As(err, &refused), errors.Is(err, diameter.ErrUnknownPeer), errors.Is(err, diameter.ErrApplicationUnsupported):
		e.Kind = policy.ErrRefused
	case errors.Is(err, rx.ErrMalformedAnswer):
		e.Kind = policy.ErrMalformed
	case errors.Is(err, diameter.ErrNotConnected):
		e.Kind = policy.ErrUnreachable
	}

	var aa *rx.AAError
	if errors.As(err, &aa) && aa.Code == tgpp.ResultRequestedServiceTemporarilyNotAuthorized && aa.RetryInterval > 0 {
		e.RetryAfter = aa.RetryInterval
	}

	return e
}

// The Class AVPs (RFC 6733 §8.20) the PCRF returned, sent back in the STR.
func encodeClass(class [][]byte) (string, error) {
	if len(class) == 0 {
		return "", nil
	}

	b, err := json.Marshal(class)

	return string(b), err
}

func decodeClass(ref string) ([][]byte, error) {
	if ref == "" {
		return nil, nil
	}

	var class [][]byte

	return class, json.Unmarshal([]byte(ref), &class)
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

// TS 29.214 §4.4.6.2, §4.4.6.5
func event(r rx.ReAuthRequest) policy.Event {
	var e policy.Event

	for _, a := range r.SpecificActions {
		e.Kinds = append(e.Kinds, eventKind(a))
	}

	for _, f := range r.Flows {
		e.Components = append(e.Components, f.MediaComponentNumber)
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
	case *n.IPCANType == rx.IPCAN3GPPEPS:
		c.Access = policy.AccessEPS
	case *n.IPCANType == rx.IPCAN3GPP5GS:
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
