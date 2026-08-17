package postmaster

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// _ keeps fmt import if unused
var _ = fmt.Sprintf

// TestHOTUpdateEncodingConsistency verifies that rows remain correctly
// readable after HOT updates (M0107 HOT-update encoding parity fix). Before
// the fix, tryApplyHOTUpdate always used EncodeRow (goopg format) while
// writeHeapRowReturning used EncodeRowPG (PG format) for PG physical-format
// rows. The mixed encoding caused decodeGoopgRowIntoMctx to occasionally
// "succeed" on PG-encoded rows with wrong values (silent data corruption),
// surfacing as wrong abalance values or "truncated 4-byte varlena header" errors
// during pgbench runs at c=100 SU.
func TestHOTUpdateEncodingConsistency(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	setup, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TABLE IF EXISTS hot_enc_test`,
		// Schema mirrors pgbench_accounts: int PK + int counter + varchar filler
		`CREATE TABLE hot_enc_test (
			id      int PRIMARY KEY,
			counter int NOT NULL DEFAULT 0,
			filler  varchar(84)
		)`,
		// Insert rows with non-trivial filler. Use concat of literals so the
		// INSERT doesn't depend on lpad (which may not be implemented).
		`INSERT INTO hot_enc_test VALUES (1,0,'hello'), (2,0,'world'), (3,0,'goopg'),
			(4,0,'test'), (5,0,'abc'), (6,0,'xyz'), (7,0,'foo'), (8,0,'bar'),
			(9,0,'baz'), (10,0,'qux'), (11,0,'one'), (12,0,'two'), (13,0,'three'),
			(14,0,'four'), (15,0,'five'), (16,0,'six'), (17,0,'seven'), (18,0,'eight'),
			(19,0,'nine'), (20,0,'ten')`,
	} {
		if _, err := setup.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = setup.Close()

	// Run many sequential updates on the same rows to accumulate HOT chains.
	// This exercises the HOT update path and verifies values are correct.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	const updates = 50
	for i := 0; i < updates; i++ {
		id := (i % 20) + 1
		if _, err := conn.ExecContext(ctx,
			`UPDATE hot_enc_test SET counter = counter + 1 WHERE id = $1`, id); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}

	// Verify counter values: each id was updated floor(updates/20) or
	// ceil(updates/20) times.
	expectedFillers := map[int]string{
		1: "hello", 2: "world", 3: "goopg", 4: "test", 5: "abc",
		6: "xyz", 7: "foo", 8: "bar", 9: "baz", 10: "qux",
		11: "one", 12: "two", 13: "three", 14: "four", 15: "five",
		16: "six", 17: "seven", 18: "eight", 19: "nine", 20: "ten",
	}

	rows, err := conn.QueryContext(ctx, `SELECT id, counter, filler FROM hot_enc_test ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, counter int
		var filler sql.NullString
		if err := rows.Scan(&id, &counter, &filler); err != nil {
			t.Fatal(err)
		}
		// counter >= 2 (each id updated at least twice with 50 updates / 20 ids)
		if counter < 2 {
			t.Errorf("id=%d: counter=%d, want ≥ 2 (HOT chain may have lost updates)", id, counter)
		}
		// filler must not be NULL and must match initial value.
		if !filler.Valid {
			t.Errorf("id=%d: filler=NULL (HOT update encoding corruption — filler lost)", id)
			continue
		}
		if want := expectedFillers[id]; filler.String != want {
			t.Errorf("id=%d: filler=%q, want %q (encoding corruption)", id, filler.String, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestHOTUpdateEncodingConsistencyConcurrent stress-tests concurrent updates
// on the same rows to expose HOT-update encoding races.  Before the fix,
// concurrent pgbench at c=100 SU surfaced "truncated 4-byte varlena header"
// errors from the mixed goopg/PG encoding on the HOT vs non-HOT paths.
func TestHOTUpdateEncodingConsistencyConcurrent(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	ctx := context.Background()

	setup, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TABLE IF EXISTS hot_conc_test`,
		`CREATE TABLE hot_conc_test (
			id      int PRIMARY KEY,
			counter int NOT NULL DEFAULT 0,
			filler  varchar(84)
		)`,
		`INSERT INTO hot_conc_test VALUES
			(1,0,'aaa'), (2,0,'bb'), (3,0,'ccc'), (4,0,'dddd'), (5,0,'eeeee'),
			(6,0,'ff'), (7,0,'ggg'), (8,0,'hhhh'), (9,0,'iiiii'), (10,0,'jj')`,
	} {
		if _, err := setup.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = setup.Close()

	const workers = 10
	const opsPerWorker = 30
	var wg sync.WaitGroup
	var unexpectedErrors sync.Map

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			conn, err := db.Conn(ctx)
			if err != nil {
				unexpectedErrors.Store(fmt.Sprintf("w%d-conn", wid), err)
				return
			}
			defer conn.Close()
			for op := 0; op < opsPerWorker; op++ {
				id := (wid+op)%10 + 1
				if _, err := conn.ExecContext(ctx,
					`UPDATE hot_conc_test SET counter = counter + 1 WHERE id = $1`, id); err != nil {
					// 40001 (serialization failure) is an expected concurrent-update
					// outcome and not a structural corruption bug; skip those.
					if !strings.Contains(err.Error(), "40001") && !strings.Contains(err.Error(), "serialize") {
						unexpectedErrors.Store(fmt.Sprintf("w%d-op%d", wid, op), err)
					}
				}
			}
		}(w)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent updates timed out")
	}

	unexpectedErrors.Range(func(k, v any) bool {
		t.Errorf("unexpected error %v: %v", k, v)
		return true
	})

	// The key invariant: filler must not become NULL due to encoding corruption.
	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	rows, err := conn2.QueryContext(ctx, `SELECT id, filler FROM hot_conc_test ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var filler sql.NullString
		if err := rows.Scan(&id, &filler); err != nil {
			t.Fatal(err)
		}
		if !filler.Valid {
			t.Errorf("id=%d: filler=NULL after concurrent updates (HOT encoding corruption)", id)
		}
	}
	_ = rows.Err()
}
