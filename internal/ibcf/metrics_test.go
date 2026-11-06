package ibcf

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func recordedScene(t *testing.T, trusted bool) (*scene, *callrecords.Recorder, *db.DB) {
	t.Helper()

	d, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	rec := callrecords.New(callrecords.Config{Store: d})

	t.Cleanup(func() { _ = rec.Close(context.Background()) })

	s := newSceneWith(t, trusted, func(c *Config) {
		c.Metrics = NewMetrics()
		c.Records = rec
	})

	return s, rec, d
}

func savedRecords(t *testing.T, rec *callrecords.Recorder, d *db.DB) []db.CallRecord {
	t.Helper()

	_ = rec.Close(t.Context())

	l, _, err := d.ListCallRecords(t.Context(), db.CallRecordFilter{}, 1, 100)
	if err != nil {
		t.Fatal(err)
	}

	return l
}

func (s *scene) trunkCalls(trunk, direction string, outcome db.CallOutcome) float64 {
	return testutil.ToFloat64(s.ibcf.cfg.Metrics.calls.WithLabelValues(trunk, direction, string(outcome)))
}

func (s *scene) activeTrunkCalls(trunk, direction string) float64 {
	return testutil.ToFloat64(s.ibcf.cfg.Metrics.active.WithLabelValues(trunk, direction))
}

func TestExitCallRecordAndMetrics(t *testing.T) {
	s, rec, d := recordedScene(t, false)

	rec.Attempt(callrecords.Attempt{ICID: "icid-1", CallID: "phone-call", RequestURI: "tel:" + carol, IMPI: "alice@" + home})

	s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Progress(ua.Reply{Code: 180})
	_ = in.Accept(sdpOf("answer-1")...)

	e := s.ims.until("established")
	s.carrier.next("established")

	if got := s.activeTrunkCalls("carrier", outbound); got != 1 {
		t.Errorf("active outbound calls %v, want 1", got)
	}

	_ = e.s.Bye(context.Background(), ua.Change{}, nil)

	s.carrier.next("bye")
	s.net.Quiesce()

	if got := s.trunkCalls("carrier", outbound, db.OutcomeAnswered); got != 1 {
		t.Errorf("answered outbound calls %v, want 1", got)
	}

	if got := s.activeTrunkCalls("carrier", outbound); got != 0 {
		t.Errorf("active outbound calls %v, want 0", got)
	}

	if n := testutil.CollectAndCount(s.ibcf.cfg.Metrics.calls); n != 2*len(callrecords.Outcomes) {
		t.Errorf("%d call series, want both directions and every outcome", n)
	}

	l := savedRecords(t, rec, d)
	if len(l) != 1 || l[0].OutTrunk != "carrier" || l[0].OutTrunkName != "Carrier" || l[0].InTrunk != "" {
		t.Fatalf("records %+v, want one to the trunk", l)
	}
}

func TestEntryCallRecordAndMetrics(t *testing.T) {
	s, rec, d := recordedScene(t, false)

	s.dial(s.carrier, "offer-1")

	r := s.ims.next("invite").req
	icid := icidOf(r.Message().Header)

	_ = r.Progress(ua.Reply{Code: 180})

	s.carrier.next("progress")

	_ = r.Accept(sdpOf("v=0\r\no=- 1 1 IN IP4 10.0.0.1\r\ns=-\r\nc=IN IP4 10.0.0.1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 0\r\n")...)

	ce := s.carrier.until("established")
	s.ims.until("established")

	if got := s.activeTrunkCalls("carrier", inbound); got != 1 {
		t.Errorf("active inbound calls %v, want 1", got)
	}

	_ = ce.s.Bye(context.Background(), ua.Change{}, nil)

	s.ims.until("bye")
	s.net.Quiesce()

	if got := s.trunkCalls("carrier", inbound, db.OutcomeAnswered); got != 1 {
		t.Errorf("answered inbound calls %v, want 1", got)
	}

	l := savedRecords(t, rec, d)
	if len(l) != 1 {
		t.Fatalf("%d records, want 1", len(l))
	}

	got := l[0]

	switch {
	case got.ICID != icid, got.InTrunk != "carrier", got.InTrunkName != "Carrier", got.OutTrunk != "":
		t.Errorf("record %s in over %s %s, out over %s, want %s in over carrier", got.ICID, got.InTrunk, got.InTrunkName, got.OutTrunk, icid)
	case got.Outcome != db.OutcomeAnswered, got.EndedBy != db.PartyCaller, !got.Alerted:
		t.Errorf("record %s ended by %s, alerted %v", got.Outcome, got.EndedBy, got.Alerted)
	case got.CalledParty != "sip:"+alice+"@"+home+";user=phone", len(got.Media) != 1 || got.Media[0] != "audio":
		t.Errorf("record called %s, media %v", got.CalledParty, got.Media)
	case len(got.CallingParty) != 1 || got.CallingParty[0] != "sip:"+carol+"@sip.carrier.example;user=phone":
		t.Errorf("record calling %v", got.CallingParty)
	}
}

func TestEntryCancelledMetrics(t *testing.T) {
	s, rec, d := recordedScene(t, false)

	o := s.dial(s.carrier, "offer-1")

	s.ims.next("invite")

	_ = o.Cancel(context.Background(), ua.Change{})

	s.carrier.until("failed")
	s.net.Quiesce()

	if got := s.trunkCalls("carrier", inbound, db.OutcomeCancelled); got != 1 {
		t.Errorf("cancelled inbound calls %v, want 1", got)
	}

	if l := savedRecords(t, rec, d); len(l) != 1 || l[0].Outcome != db.OutcomeCancelled || l[0].EndedBy != db.PartyCaller {
		t.Fatalf("records %+v, want one cancelled by the caller", l)
	}
}

func TestExitFailoverAttributesTheCallToTheLastTrunk(t *testing.T) {
	s := newSceneWith(t, false, func(c *Config) { c.Metrics = NewMetrics() })
	second := withSecondTrunk(s, false)
	s.ibcf.configure()

	s.call("offer-1")

	_ = s.carrier.next("invite").req.Reject(503)
	_ = second.next("invite").req.Reject(486)

	s.ims.until("failed")
	s.net.Quiesce()

	if got := s.trunkCalls("second", outbound, db.OutcomeBusy); got != 1 {
		t.Errorf("busy calls of the second trunk %v, want 1", got)
	}

	for _, o := range callrecords.Outcomes {
		if got := s.trunkCalls("carrier", outbound, o); got != 0 {
			t.Errorf("%s calls of the first trunk %v, want 0", o, got)
		}
	}

	for _, id := range []string{"carrier", "second"} {
		if got := s.activeTrunkCalls(id, outbound); got != 0 {
			t.Errorf("active calls of %s %v, want 0", id, got)
		}
	}
}

func TestTrunkMetricsDrainBeforeDelete(t *testing.T) {
	m := NewMetrics()
	m.configure([]string{"a", "b"})
	m.enter("a", outbound)

	m.configure([]string{"b"})

	if got := testutil.ToFloat64(m.active.WithLabelValues("a", outbound)); got != 1 {
		t.Fatalf("active calls of a deleted trunk %v, want 1 until they end", got)
	}

	m.leave("a", outbound, db.OutcomeAnswered)

	if n := testutil.CollectAndCount(m.active); n != 2 {
		t.Errorf("%d active series, want the remaining trunk's two", n)
	}

	if n := testutil.CollectAndCount(m.calls); n != 2*len(callrecords.Outcomes) {
		t.Errorf("%d call series, want the remaining trunk's", n)
	}

	if n := testutil.CollectAndCount(m.failovers); n != 1 {
		t.Errorf("%d failover series, want the remaining trunk's", n)
	}
}
