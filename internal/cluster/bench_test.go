package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
)

func BenchmarkReplicatedWrites(b *testing.B) {
	for _, where := range []string{"leader", "follower"} {
		b.Run(where, func(b *testing.B) {
			c := newCluster(b, 3)
			target := c.leader()

			if where == "follower" {
				target = c.follower()
			}

			var seq atomic.Int64

			b.SetParallelism(4)
			b.ResetTimer()

			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					i := seq.Add(1)

					if err := saveBenchRecord(target.db, i); err != nil {
						b.Error(err)
						return
					}
				}
			})

			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "writes/s")
		})
	}
}

func BenchmarkSingleNodeWrites(b *testing.B) {
	dir := b.TempDir()

	d, err := db.Open(context.Background(), filepath.Join(dir, "ims.db"))
	if err != nil {
		b.Fatal(err)
	}

	n, err := Open(context.Background(), Config{Dir: filepath.Join(dir, "raft"), Logger: slog.New(slog.DiscardHandler)}, d)
	if err != nil {
		b.Fatal(err)
	}

	defer func() { _ = n.Close(); _ = d.Close() }()

	d.SetProposer(n)
	waitLeader(b, n)

	var seq atomic.Int64

	b.SetParallelism(4)
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := seq.Add(1)
			if err := saveBenchRecord(d, i); err != nil {
				b.Error(err)
				return
			}
		}
	})

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "writes/s")
}

func benchSet(i int64) db.ImplicitSet {
	uri := fmt.Sprintf("sip:+1555%07d@ims.example.org", i)

	return db.ImplicitSet{
		Identities: []db.PublicIdentity{{URI: uri, Key: uri}},
		UserData:   make([]byte, 2048),
		HSS:        db.HSS{Host: "hss.example.org", Realm: "example.org"},
	}
}

func benchRegistration(i int64) db.Registration {
	now := time.Now()

	return db.Registration{
		IMPI: fmt.Sprintf("user%d@ims.example.org", i), IMPU: fmt.Sprintf("sip:+1555%07d@ims.example.org", i),
		Bindings: []db.Binding{{
			Contact: db.Contact{URI: fmt.Sprintf("sip:user%d@10.45.0.2:5060", i), Path: "<sip:token@10.0.0.1:5060;lr>"},
			CallID:  fmt.Sprintf("call-%d", i), CSeq: 1, ExpiresAt: now.Add(time.Hour), IMPU: "sip:+15550000000@ims.example.org",
			RegisteredAt: now,
		}},
	}
}

func saveBenchRecord(d *db.DB, i int64) error {
	r := &db.CallRecord{
		ICID: fmt.Sprintf("icid-%d", i), SessionID: fmt.Sprintf("session-%d", i),
		RequestedParty: "sip:+15551230002@ims.example.org", RequestedAt: time.Now(),
	}

	errs, err := d.SaveCallRecords(context.Background(), []*db.CallRecord{r}, nil)
	if err == nil && errs != nil {
		err = errs[0]
	}

	return err
}
