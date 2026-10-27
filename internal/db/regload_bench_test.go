package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// BenchmarkLoadRegistration runs the three statements of ListRegistrationsByIMPI against 20,000 registrations, with
// and without an index of bindings by registration and the driver's statement cache.
func BenchmarkLoadRegistration(b *testing.B) {
	const n = 20000

	for _, index := range []bool{false, true} {
		for _, cache := range []int{0, 64} {
			b.Run(fmt.Sprintf("index=%v/stmtcache=%d", index, cache), func(b *testing.B) {
				ctx := context.Background()
				path := filepath.Join(b.TempDir(), "ims.db")

				d, err := Open(ctx, path)
				if err != nil {
					b.Fatal(err)
				}

				_, err = d.conn.ExecContext(ctx, `
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO implicit_sets (id,user_data,hss_host,hss_realm) SELECT i, randomblob(2000),'hss','realm' FROM n;`, n)
				must(b, err)
				_, err = d.conn.ExecContext(ctx, `
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO set_identities SELECT i, p, 'sip:'||i||'-'||p||'@x', i||'-'||p, NULL, 0 FROM n, (SELECT 0 p UNION SELECT 1 UNION SELECT 2)`, n)
				must(b, err)
				_, err = d.conn.ExecContext(ctx, `
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO registrations SELECT i, 'impi'||i, 'sip:'||i, i FROM n`, n)
				must(b, err)
				_, err = d.conn.ExecContext(ctx, `
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO bindings (registration_id, uri, instance_id, reg_id, params, path, call_id, cseq, expires_at, event, impu, registered_at)
SELECT i, 'sip:ue'||i||'@10.0.0.1:5060', '<urn:gsma:imei:'||i||'>', 1, ';+sip.instance="<urn:gsma:imei:x>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";video', '<sip:pcscf;lr>', 'callid'||i, 1, 9000000000000000000, 'registered','sip:'||i, 1 FROM n`, n)
				must(b, err)

				// Without the index: the bindings only by flow or by URI, as the partial indexes have them.
				if !index {
					_, err = d.conn.ExecContext(ctx, `DROP INDEX bindings_registration`)
					must(b, err)
				}

				_ = d.Close()

				conn, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_stmt_cache_size=%d", path, cache))
				must(b, err)
				conn.SetMaxOpenConns(1)
				defer conn.Close()

				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					impi := fmt.Sprintf("impi%d", i%n+1)
					regs, err := queryRegistrations(ctx, conn, `SELECT `+registrationColumns+registrationFrom+` WHERE r.impi = ? ORDER BY r.id`, impi)
					if err != nil || len(regs) != 1 || len(regs[0].Bindings) != 1 {
						b.Fatal(err, len(regs))
					}
				}
			})
		}
	}
}

func must(b *testing.B, err error) {
	b.Helper()

	if err != nil {
		b.Fatal(err)
	}
}
