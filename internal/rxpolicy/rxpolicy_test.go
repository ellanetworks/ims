package rxpolicy

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/policy"
)

func TestClassify(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		kind error
	}{
		"result":          {&rx.ResultError{Result: tgpp.Result{Code: tgpp.ResultInvalidServiceInformation, Experimental: true}}, policy.ErrRefused},
		"unknown peer":    {diameter.ErrUnknownPeer, policy.ErrRefused},
		"no application":  {diameter.ErrApplicationUnsupported, policy.ErrRefused},
		"malformed":       {fmt.Errorf("%w: no Result-Code", rx.ErrMalformedAnswer), policy.ErrMalformed},
		"not connected":   {diameter.ErrNotConnected, policy.ErrUnreachable},
		"context expired": {context.DeadlineExceeded, nil},
		"unknown session": {&rx.ResultError{Result: tgpp.Result{Code: diameter.ResultUnknownSessionID}}, policy.ErrUnknownSession},
	} {
		t.Run(name, func(t *testing.T) {
			err := classify(tc.err)

			if !errors.Is(err, tc.err) {
				t.Fatalf("%v does not wrap %v", err, tc.err)
			}

			for _, k := range []error{policy.ErrRefused, policy.ErrMalformed, policy.ErrUnreachable, policy.ErrUnknownSession} {
				if errors.Is(err, k) != (k == tc.kind) {
					t.Fatalf("errors.Is(%v, %v) = %t, want kind %v", err, k, k != tc.kind, tc.kind)
				}
			}
		})
	}
}

// TS 29.214 §4.4.1: only REQUESTED_SERVICE_TEMPORARILY_NOT_AUTHORIZED holds the same service information back.
func TestClassifyRetryInterval(t *testing.T) {
	temporary := &rx.AAError{
		ResultError:   rx.ResultError{Result: tgpp.Result{Code: tgpp.ResultRequestedServiceTemporarilyNotAuthorized, Experimental: true}},
		RetryInterval: 30 * time.Second,
	}

	err := classify(temporary)
	if got := policy.RetryAfter(err); got != 30*time.Second || !errors.Is(err, policy.ErrRefused) {
		t.Fatalf("RetryAfter = %s, refused %t; want 30s, refused", got, errors.Is(err, policy.ErrRefused))
	}

	if r, ok := policy.ResultOf(err); !ok || r == "" {
		t.Fatalf("ResultOf = %q, %t; want the result", r, ok)
	}

	other := &rx.AAError{
		ResultError:   rx.ResultError{Result: tgpp.Result{Code: tgpp.ResultInvalidServiceInformation, Experimental: true}},
		RetryInterval: 30 * time.Second,
	}

	if got := policy.RetryAfter(classify(other)); got != 0 {
		t.Fatalf("RetryAfter = %s for another result, want none", got)
	}
}

func TestClassRoundTrip(t *testing.T) {
	for _, class := range [][][]byte{nil, {[]byte("pcrf-state"), {0xff, 0x00}}} {
		ref, err := encodeClass(class)
		if err != nil {
			t.Fatal(err)
		}

		got, err := decodeClass(ref)
		if err != nil || !reflect.DeepEqual(got, class) {
			t.Fatalf("decodeClass(%q) = %q, %v; want %q", ref, got, err, class)
		}
	}
}

// TS 29.214 §4.4.6.2, §4.4.6.5
func TestEvent(t *testing.T) {
	e := event(rx.ReAuthRequest{
		SpecificActions: []rx.SpecificAction{rx.ActionChargingCorrelationExchange, rx.ActionIndicationOfLossOfBearer},
		Flows:           []rx.Flows{{MediaComponentNumber: 2}},
		AccessNetworkChargingIdentifiers: []rx.AccessNetworkChargingIdentifier{
			{Value: []byte{1}, Flows: []rx.Flows{{MediaComponentNumber: 2, FlowNumbers: []uint32{1}}}},
		},
		AccessNetworkChargingAddress: netip.MustParseAddr("192.0.2.1"),
	})

	want := policy.Event{
		Kinds:      []policy.EventKind{policy.EventChargingCorrelation, policy.EventBearerLost},
		Components: []uint32{2},
		Charging: &policy.Charging{
			Address: netip.MustParseAddr("192.0.2.1"),
			Identifiers: []policy.ChargingID{
				{Value: []byte{1}, Flows: []policy.Flows{{Component: 2, FlowNumbers: []uint32{1}}}},
			},
		},
	}

	if !reflect.DeepEqual(e, want) {
		t.Fatalf("event = %+v, want %+v", e, want)
	}

	if e := event(rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfReleaseOfBearer}}); e.Charging != nil {
		t.Fatalf("charging %+v without CHARGING_CORRELATION_EXCHANGE", e.Charging)
	}
}

func TestChargingAccess(t *testing.T) {
	for in, want := range map[rx.IPCANType]policy.Access{
		rx.IPCAN3GPPEPS:    policy.AccessEPS,
		rx.IPCANNon3GPPEPS: policy.AccessOther,
		rx.IPCAN3GPP5GS:    policy.Access5GS,
		rx.IPCANNon3GPP5GS: policy.Access5GS,
		rx.IPCAN3GPPGPRS:   policy.AccessOther,
	} {
		if got := AnswerCharging(rx.AAAnswer{AccessNetwork: rx.AccessNetwork{IPCANType: &in}}).Access; got != want {
			t.Errorf("IP-CAN-Type %s: access %d, want %d", in, got, want)
		}
	}

	if got := AnswerCharging(rx.AAAnswer{}).Access; got != policy.AccessUnknown {
		t.Errorf("no IP-CAN-Type: access %d, want unknown", got)
	}
}

// RFC 6733 §4.3.1, RFC 4343: the PCRF's DiameterIdentity is an FQDN, compared without case.
func TestEndpoint(t *testing.T) {
	b := New(Config{PCRF: PCRF{Host: "PCRF.Example.org"}})

	if got := b.Endpoint(); got != "rx:pcrf.example.org" {
		t.Fatalf("Endpoint() = %q, want it lower-cased", got)
	}
}

func TestTermination(t *testing.T) {
	for in, want := range map[policy.Termination]rx.TerminationCause{
		policy.TerminationLogout:         rx.TerminationLogout,
		policy.TerminationExpired:        rx.TerminationAuthExpired,
		policy.TerminationAdministrative: rx.TerminationAdministrative,
		policy.TerminationBadAnswer:      rx.TerminationBadAnswer,
	} {
		if got := termination(in); got != want {
			t.Errorf("termination(%s) = %s, want %s", in, got, want)
		}
	}
}

type sink struct {
	notified []string
	aborted  []policy.Abort
}

func (s *sink) Notify(id string, _ policy.Event) bool {
	s.notified = append(s.notified, id)
	return true
}

func (s *sink) Abort(_ string, a policy.Abort) (func(), bool) {
	s.aborted = append(s.aborted, a)
	return func() {}, true
}

func TestInboundNeedsASink(t *testing.T) {
	b := New(Config{})

	if b.ReAuth("s", rx.ReAuthRequest{}) {
		t.Fatal("RAR known without a sink")
	}

	if _, known := b.AbortSession("s", rx.AbortSessionRequest{}); known {
		t.Fatal("ASR known without a sink")
	}

	s := &sink{}
	b.Bind(s)

	if !b.ReAuth("s", rx.ReAuthRequest{}) || len(s.notified) != 1 {
		t.Fatal("RAR not delivered to the sink")
	}

	if _, known := b.AbortSession("s", rx.AbortSessionRequest{Cause: rx.AbortInsufficientBearerResources}); !known ||
		len(s.aborted) != 1 || !s.aborted[0].InsufficientResources {
		t.Fatalf("aborts %+v, want one for insufficient resources", s.aborted)
	}

	b.Bind(nil)

	if b.ReAuth("s", rx.ReAuthRequest{}) {
		t.Fatal("RAR known after unbinding")
	}
}

// RFC 6733 §8.4: only an unanswered request, or one answered with a retry, is transient.
func TestClassifyTransient(t *testing.T) {
	result := func(code uint32) error { return &rx.ResultError{Result: tgpp.Result{Code: code}} }

	for _, c := range []struct {
		err  error
		want bool
	}{
		{diameter.ErrNotConnected, true},
		{context.DeadlineExceeded, true},
		{diameter.ErrUnknownPeer, false},
		{diameter.ErrApplicationUnsupported, false},
		{diameter.ErrClosed, false},
		{result(diameter.ResultUnknownSessionID), false},
		{result(diameter.ResultUnableToComply), false},
		{result(diameter.ResultTooBusy), true},
		{result(diameter.ResultUnableToDeliver), true},
		{result(4001), true},
		{fmt.Errorf("%w: no Result-Code", rx.ErrMalformedAnswer), false},
	} {
		if got := policy.Transient(classify(c.err)); got != c.want {
			t.Errorf("Transient(%v) = %v, want %v", c.err, got, c.want)
		}
	}

	if policy.Transient(nil) {
		t.Error("Transient(nil) = true")
	}
}
