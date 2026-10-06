//go:build linux && (amd64 || arm64)

package integration

import (
	"cmp"
	"net"
	"net/http"
	"net/netip"
	"path"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/n5"
	"github.com/ellanetworks/ims/internal/pcftest"
	"github.com/ellanetworks/ims/internal/pcrftest"
)

const (
	policyRx    = "rx"
	policyN5    = "n5"
	policyN5TLS = "n5-tls"
)

// forEachPolicy runs a test against a fake PCRF over Rx, and a fake PCF over N5 without and with TLS.
func forEachPolicy(t *testing.T, f func(t *testing.T, iface string)) {
	for _, iface := range []string{policyRx, policyN5, policyN5TLS} {
		t.Run(iface, func(t *testing.T) { f(t, iface) })
	}
}

func freePort(t *testing.T) int {
	t.Helper()

	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = ln.Close() }()

	return ln.Addr().(*net.TCPAddr).Port
}

// policyRef is a policy session: its local ID, and over N5 its context.
type policyRef struct {
	id  string
	uri string
}

// mediaSession is a call session as the policy function saw it.
type mediaSession struct {
	ue       netip.Addr
	requests []mediaRequest
	ended    bool
}

// mediaRequest is the service information of a call session after one request.
type mediaRequest struct {
	ue        netip.Addr
	audio     bool
	bandwidth bool
	subs      int
	flows     []string
}

// fakePolicy drives the fake PCRF or PCF of a scene. Termination causes are only on Rx.
type fakePolicy interface {
	// signalling returns the session the next request opens for the IMS signalling of the UE.
	signalling(ue netip.Addr) policyRef
	// wantEnd fails unless the next request ends the session.
	wantEnd(ref policyRef, cause rx.TerminationCause)
	// none fails on any request within d.
	none(d time.Duration)
	// pathLost reports the loss of the signalling path.
	pathLost(ref policyRef)
	// abort has the policy function end the session.
	abort(ref policyRef) error

	refuseMedia()
	// calls reads requests until want call sessions were seen and every one of them ended, skipping signalling.
	calls(want int) map[string]*mediaSession
	// noCallEnd fails on the end of any call session within d.
	noCallEnd(d time.Duration)
	// firstCall waits for the first request of a call session of the UE, of any UE if ue is the zero Addr.
	firstCall(ue netip.Addr) policyRef
	// wantCallEnd waits for the end of the call session, skipping other requests.
	wantCallEnd(ref policyRef, cause rx.TerminationCause)
	// mediaLost reports the loss of the resources of a media component.
	mediaLost(ref policyRef, component uint32)
}

const policyWait = 15 * time.Second

type rxPolicy struct {
	s *scene
}

func (p *rxPolicy) signalling(ue netip.Addr) policyRef {
	p.s.t.Helper()

	r := p.s.pcrf.Next(p.s.t)
	if r.AAR == nil {
		p.s.t.Fatalf("got %s, want the AAR", r)
	}

	if got := cmp.Or(r.AAR.FramedIPAddress, r.AAR.FramedIPv6Address); got != ue {
		p.s.t.Fatalf("AAR %s, want the UE's address %s", r, ue)
	}

	mc := r.AAR.MediaComponents
	if len(mc) != 1 || mc[0].Number != 0 || len(mc[0].SubComponents) != 1 || mc[0].SubComponents[0].FlowNumber != 0 ||
		mc[0].SubComponents[0].FlowUsage == nil || *mc[0].SubComponents[0].FlowUsage != rx.FlowUsageAFSignalling ||
		!slices.Equal(r.AAR.SpecificActions, []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer}) {
		p.s.t.Fatalf("AAR %s, want the AF signalling subscription", r)
	}

	return policyRef{id: r.SessionID}
}

func (p *rxPolicy) wantEnd(ref policyRef, cause rx.TerminationCause) {
	p.s.t.Helper()

	r := p.s.pcrf.Next(p.s.t)
	if r.STR == nil || r.SessionID != ref.id || r.STR.Cause != cause {
		p.s.t.Fatalf("got %s, want the STR of %s with %s", r, ref.id, cause)
	}

	if len(r.STR.Class) != 1 || string(r.STR.Class[0]) != ref.id {
		p.s.t.Fatalf("STR Class = %q, want the AAA's %q", r.STR.Class, ref.id)
	}
}

func (p *rxPolicy) none(d time.Duration) {
	p.s.t.Helper()

	select {
	case r := <-p.s.pcrf.Requests():
		p.s.t.Fatalf("unexpected %s", r)
	case <-time.After(d):
	}
}

func (p *rxPolicy) pathLost(ref policyRef) {
	p.s.t.Helper()

	ans, err := p.s.pcrf.RAR(p.s.ctx(), ref.id, rx.ActionIndicationOfReleaseOfBearer)
	if err != nil || ans.Result.Code != diameter.ResultSuccess || ans.Result.Experimental {
		p.s.t.Fatalf("RAA = %+v, %v, want DIAMETER_SUCCESS", ans, err)
	}
}

func (p *rxPolicy) abort(ref policyRef) error {
	_, err := p.s.pcrf.ASR(p.s.ctx(), ref.id, rx.AbortBearerReleased)
	return err
}

func (p *rxPolicy) refuseMedia() {
	p.s.pcrf.RefuseMedia(tgpp.Result{Code: tgpp.ResultRequestedServiceNotAuthorized, Experimental: true, VendorID: tgpp.VendorID})
}

// TS 29.214 Annex A.1, A.3: INITIAL with the bearer events, then UPDATEs without them.
func (p *rxPolicy) calls(want int) map[string]*mediaSession {
	p.s.t.Helper()

	sessions := map[string]*mediaSession{}
	signalling := map[string]bool{}
	deadline := time.After(policyWait)

	for !allEnded(sessions, want) {
		var r pcrftest.Request

		select {
		case r = <-p.s.pcrf.Requests():
		case <-deadline:
			p.s.t.Fatalf("saw call sessions %+v, want %d ended", sessions, want)
		}

		switch {
		case r.AAR != nil && pcrftest.Signalling(*r.AAR):
			signalling[r.SessionID] = true
		case r.AAR != nil:
			c := sessions[r.SessionID]
			if c == nil {
				c = &mediaSession{ue: cmp.Or(r.AAR.FramedIPAddress, r.AAR.FramedIPv6Address)}
				sessions[r.SessionID] = c

				if r.AAR.RequestType == nil || *r.AAR.RequestType != rx.RequestInitial || len(r.AAR.SpecificActions) != 4 {
					p.s.t.Errorf("%s: first AAR %s, want INITIAL with the bearer events", r.SessionID, r)
				}
			} else if r.AAR.RequestType == nil || *r.AAR.RequestType != rx.RequestUpdate || len(r.AAR.SpecificActions) != 0 {
				p.s.t.Errorf("%s: later AAR %s, want UPDATE without Specific-Action", r.SessionID, r)
			}

			c.requests = append(c.requests, rxMedia(*r.AAR))
		case r.STR != nil && !signalling[r.SessionID]:
			c := sessions[r.SessionID]
			if c == nil {
				p.s.t.Fatalf("STR for the unknown session %s", r.SessionID)
			}

			if r.STR.Cause != rx.TerminationLogout {
				p.s.t.Errorf("%s: STR cause %s, want %s", r.SessionID, r.STR.Cause, rx.TerminationLogout)
			}

			c.ended = true
		}
	}

	return sessions
}

func rxMedia(r rx.AARequest) mediaRequest {
	m := mediaRequest{ue: cmp.Or(r.FramedIPAddress, r.FramedIPv6Address)}

	if len(r.MediaComponents) != 1 {
		return m
	}

	c := r.MediaComponents[0]
	m.audio = c.Type != nil && *c.Type == rx.MediaAudio
	m.bandwidth = c.MaxRequestedBandwidthUL != nil && c.MaxRequestedBandwidthDL != nil
	m.subs = len(c.SubComponents)

	if m.subs > 0 {
		m.flows = c.SubComponents[0].FlowDescriptions
	}

	return m
}

func allEnded(sessions map[string]*mediaSession, want int) bool {
	if len(sessions) < want {
		return false
	}

	for _, c := range sessions {
		if !c.ended {
			return false
		}
	}

	return true
}

func (p *rxPolicy) noCallEnd(d time.Duration) {
	p.s.t.Helper()

	signalling := map[string]bool{}
	deadline := time.After(d)

	for {
		select {
		case r := <-p.s.pcrf.Requests():
			switch {
			case r.AAR != nil && pcrftest.Signalling(*r.AAR):
				signalling[r.SessionID] = true
			case r.STR != nil && !signalling[r.SessionID]:
				p.s.t.Fatalf("unexpected STR %s", r)
			}
		case <-deadline:
			return
		}
	}
}

func (p *rxPolicy) firstCall(ue netip.Addr) policyRef {
	p.s.t.Helper()

	deadline := time.After(policyWait)

	for {
		select {
		case r := <-p.s.pcrf.Requests():
			if r.AAR != nil && !pcrftest.Signalling(*r.AAR) && (!ue.IsValid() || cmp.Or(r.AAR.FramedIPAddress, r.AAR.FramedIPv6Address) == ue) {
				return policyRef{id: r.SessionID}
			}
		case <-deadline:
			p.s.t.Fatalf("no media AAR for %s", ue)
		}
	}
}

func (p *rxPolicy) wantCallEnd(ref policyRef, cause rx.TerminationCause) {
	p.s.t.Helper()

	deadline := time.After(policyWait)

	for {
		select {
		case r := <-p.s.pcrf.Requests():
			if r.STR != nil && r.SessionID == ref.id {
				if r.STR.Cause != cause {
					p.s.t.Fatalf("STR %s, want %s", r, cause)
				}

				return
			}
		case <-deadline:
			p.s.t.Fatalf("no STR for %s", ref.id)
		}
	}
}

func (p *rxPolicy) mediaLost(ref policyRef, component uint32) {
	p.s.t.Helper()

	if _, err := p.s.pcrf.ReAuth(p.s.ctx(), ref.id, rx.ReAuthRequest{
		SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer},
		Flows:           []rx.Flows{{MediaComponentNumber: component}},
	}); err != nil {
		p.s.t.Fatalf("RAR: %v", err)
	}
}

type n5Policy struct {
	s *scene
}

// localID is the session ID the P-CSCF put at the end of the notifUri of a context.
func localID(c pcftest.Context) string {
	return path.Base(c.NotifURI)
}

func ueOf(c pcftest.Context) netip.Addr {
	return cmp.Or(c.UEIPv4, c.UEIPv6)
}

// TS 29.514 §4.2.6.7
func (p *n5Policy) signalling(ue netip.Addr) policyRef {
	p.s.t.Helper()

	r := p.s.pcf.Next(p.s.t)
	if r.Op != n5.OpCreate || r.Problem != nil {
		p.s.t.Fatalf("got %s, want the create", r)
	}

	if got := ueOf(r.Context); got != ue {
		p.s.t.Fatalf("create %s, want the UE's address %s", r, ue)
	}

	ev := r.Context.EvSubsc
	if !pcftest.Signalling(r.Context) || ev == nil || len(ev.Events) != 1 || ev.Events[0].Event != n5.EventFailedResourcesAllocation {
		p.s.t.Fatalf("create %s, want the AF signalling subscription", r)
	}

	return policyRef{id: localID(r.Context), uri: r.URI}
}

func (p *n5Policy) wantEnd(ref policyRef, _ rx.TerminationCause) {
	p.s.t.Helper()

	if r := p.s.pcf.Next(p.s.t); r.Op != n5.OpDelete || r.URI != ref.uri {
		p.s.t.Fatalf("got %s, want the delete of %s", r, ref.uri)
	}
}

func (p *n5Policy) none(d time.Duration) {
	p.s.t.Helper()

	select {
	case r := <-p.s.pcf.Requests():
		p.s.t.Fatalf("unexpected %s", r)
	case <-time.After(d):
	}
}

// TS 29.514 §4.2.5.10
func (p *n5Policy) pathLost(ref policyRef) {
	p.s.t.Helper()

	res, err := p.s.pcf.Notify(p.s.ctx(), ref.uri, n5.EventsNotification{EvNotifs: []n5.AfEventNotification{
		{Event: n5.EventFailedResourcesAllocation, Flows: []n5.Flows{{MedCompN: 0, FNums: []uint32{0}}}},
	}})
	if err != nil || res.Status != http.StatusNoContent {
		p.s.t.Fatalf("notify = %+v, %v, want 204", res, err)
	}
}

// TS 29.514 §4.2.5.3
func (p *n5Policy) abort(ref policyRef) error {
	res, err := p.s.pcf.Terminate(p.s.ctx(), ref.uri, n5.TerminationPDUSessionTermination)
	if err == nil && res.Status != http.StatusNoContent {
		p.s.t.Fatalf("terminate = %+v, want 204", res)
	}

	return err
}

func (p *n5Policy) refuseMedia() {
	p.s.pcf.RefuseMedia(pcftest.Failure{Status: http.StatusForbidden, Cause: n5.CauseRequestedServiceNotAuthorized})
}

// TS 29.514 §4.2.2.2, §4.2.3.2: a create with the call's event subscription, then patches.
func (p *n5Policy) calls(want int) map[string]*mediaSession {
	p.s.t.Helper()

	sessions := map[string]*mediaSession{}
	signalling := map[string]bool{}
	deadline := time.After(policyWait)

	for !allEnded(sessions, want) {
		var r pcftest.Request

		select {
		case r = <-p.s.pcf.Requests():
		case <-deadline:
			p.s.t.Fatalf("saw call sessions %+v, want %d ended", sessions, want)
		}

		switch {
		case r.Op == n5.OpCreate && pcftest.Signalling(r.Context):
			signalling[r.URI] = true
		case r.Op == n5.OpCreate:
			ev := r.Context.EvSubsc
			if ev == nil || len(ev.Events) != 1 || ev.Events[0].Event != n5.EventFailedResourcesAllocation {
				p.s.t.Errorf("%s: create %s, want FAILED_RESOURCES_ALLOCATION subscribed", r.URI, r)
			}

			sessions[r.URI] = &mediaSession{ue: ueOf(r.Context), requests: []mediaRequest{n5Media(r.Context)}}
		case r.Op == n5.OpModify:
			c := sessions[r.URI]
			if c == nil {
				p.s.t.Fatalf("modify of the unknown context %s", r.URI)
			}

			c.requests = append(c.requests, n5Media(r.Context))
		case r.Op == n5.OpDelete && !signalling[r.URI]:
			c := sessions[r.URI]
			if c == nil {
				p.s.t.Fatalf("delete of the unknown context %s", r.URI)
			}

			c.ended = true
		}
	}

	return sessions
}

func n5Media(c pcftest.Context) mediaRequest {
	m := mediaRequest{ue: ueOf(c)}

	mc, ok := c.MedComponents["1"]
	if len(c.MedComponents) != 1 || !ok {
		return m
	}

	m.audio = mc.MedType == n5.MediaAudio
	m.bandwidth = mc.MarBwUl != nil && mc.MarBwDl != nil
	m.subs = len(mc.MedSubComps)
	m.flows = mc.MedSubComps["1"].FDescs

	return m
}

func (p *n5Policy) noCallEnd(d time.Duration) {
	p.s.t.Helper()

	signalling := map[string]bool{}
	deadline := time.After(d)

	for {
		select {
		case r := <-p.s.pcf.Requests():
			switch {
			case r.Op == n5.OpCreate && pcftest.Signalling(r.Context):
				signalling[r.URI] = true
			case r.Op == n5.OpDelete && !signalling[r.URI]:
				p.s.t.Fatalf("unexpected %s", r)
			}
		case <-deadline:
			return
		}
	}
}

func (p *n5Policy) firstCall(ue netip.Addr) policyRef {
	p.s.t.Helper()

	deadline := time.After(policyWait)

	for {
		select {
		case r := <-p.s.pcf.Requests():
			if r.Op == n5.OpCreate && r.Problem == nil && !pcftest.Signalling(r.Context) && (!ue.IsValid() || ueOf(r.Context) == ue) {
				return policyRef{id: localID(r.Context), uri: r.URI}
			}
		case <-deadline:
			p.s.t.Fatalf("no call context for %s", ue)
		}
	}
}

func (p *n5Policy) wantCallEnd(ref policyRef, _ rx.TerminationCause) {
	p.s.t.Helper()

	deadline := time.After(policyWait)

	for {
		select {
		case r := <-p.s.pcf.Requests():
			if r.Op == n5.OpDelete && r.URI == ref.uri {
				return
			}
		case <-deadline:
			p.s.t.Fatalf("no delete of %s", ref.uri)
		}
	}
}

// TS 29.514 §4.2.5.8
func (p *n5Policy) mediaLost(ref policyRef, component uint32) {
	p.s.t.Helper()

	res, err := p.s.pcf.Notify(p.s.ctx(), ref.uri, n5.EventsNotification{
		EvNotifs: []n5.AfEventNotification{{Event: n5.EventFailedResourcesAllocation}},
		FailedResourcAllocReports: []n5.ResourcesAllocationInfo{
			{McResourcStatus: n5.ResourcesInactive, Flows: []n5.Flows{{MedCompN: component}}},
		},
	})
	if err != nil || res.Status != http.StatusNoContent {
		p.s.t.Fatalf("notify = %+v, %v, want 204", res, err)
	}
}
