package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/cluster/pki"
	"github.com/ellanetworks/ims/internal/db"
)

func TestCallRecordsAtStart(t *testing.T) {
	cfg := testConfig(t)
	now := time.Now()

	d, err := db.Open(t.Context(), cfg.DB.Path)
	if err != nil {
		t.Fatal(err)
	}

	nodeID := pki.NewID()
	raftDir := filepath.Join(filepath.Dir(cfg.DB.Path), "raft")

	if err := os.MkdirAll(raftDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(raftDir, "node-id"), []byte(nodeID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	open := &db.CallRecord{
		ICID: "OPEN", SessionID: "c1", RequestedParty: "tel:+15551230002", RequestedAt: now.Add(-time.Hour), NodeID: nodeID,
	}
	old := &db.CallRecord{
		ICID: "OLD", SessionID: "c2", RequestedParty: "tel:+15551230002", RequestedAt: now.Add(-91 * 24 * time.Hour),
		SIPStatus: 486, Outcome: db.OutcomeBusy, EndedBy: db.PartyCallee, DeliveryStartAt: now.Add(-91 * 24 * time.Hour),
	}

	if errs, err := d.SaveCallRecords(t.Context(), []*db.CallRecord{open, old}, nil); err != nil || errs != nil {
		t.Fatalf("SaveCallRecords = %v, %v", errs, err)
	}

	_ = d.Close()

	srv := startIMS(t, cfg)

	eventually(t, "the old record pruned", func() bool {
		_, err := srv.database.GetCallRecord(t.Context(), old.ID)
		return errors.Is(err, db.ErrNotFound)
	})

	if r, err := srv.database.GetCallRecord(t.Context(), open.ID); err != nil || !r.Incomplete {
		t.Fatalf("open record = %+v, %v; want it closed as incomplete", r, err)
	}
}

func TestCoreRestartClosesCallsInProgress(t *testing.T) {
	srv := startIMS(t, testConfig(t))
	before := srv.core.Load()

	srv.records.Attempt(callrecords.Attempt{ICID: "LIVE", CallID: "c1", RequestURI: "tel:+15551230002", IMPI: "a@b"})

	op := srv.settings.Get().Operator
	op.MNC = "02"

	if err := srv.settings.UpdateOperator(t.Context(), op); err != nil {
		t.Fatalf("UpdateOperator: %v", err)
	}

	eventually(t, "the core restarted", func() bool {
		c := srv.core.Load()
		return c != nil && c != before
	})

	eventually(t, "the call recorded as incomplete", func() bool {
		l, _, err := srv.database.ListCallRecords(t.Context(), db.CallRecordFilter{Search: "LIVE"}, 1, 1)
		return err == nil && len(l) == 1 && l[0].Incomplete
	})
}
