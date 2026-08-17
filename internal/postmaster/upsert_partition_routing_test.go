package postmaster

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

// TestUpsertPartitioned_RoutesToLeafAndProbesLeafArbiter pins the
// M0100-0005t fix: INSERT … ON CONFLICT DO NOTHING against a partitioned
// table must (a) route the row to the correct leaf, (b) probe the
// leaf's matching unique/PK index (not the parent's empty index), and
// (c) write the row to the leaf's heap so a subsequent SELECT on the
// parent (which scans children) returns it.
//
// Before the fix, partition children inherited no indexes from the
// parent, the parent's PK index never received entries (writes route
// to leaves), upsertOp's arbiter probe found nothing on every INSERT,
// and applyInsert wrote to the parent's heap (invisible to SELECTs
// that walk partition children).  Net effect: ON CONFLICT either
// missed live duplicates outright, or silently dropped inserted rows.
func TestUpsertPartitioned_RoutesToLeafAndProbesLeafArbiter(t *testing.T) {
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
	defer setup.Close()
	for _, q := range []string{
		`DROP TABLE IF EXISTS up_part`,
		`CREATE TABLE up_part (a int primary key, b text) PARTITION BY LIST(a)`,
		`CREATE TABLE up_part1 PARTITION OF up_part FOR VALUES IN (1)`,
		`CREATE TABLE up_part2 PARTITION OF up_part FOR VALUES IN (2)`,
		`INSERT INTO up_part VALUES (1, 'initial')`,
	} {
		if _, err := setup.ExecContext(ctx, q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}

	// (a) Conflicting INSERT on the existing key — DO NOTHING must
	// detect the conflict via the leaf's PK index and skip the row.
	if _, err := setup.ExecContext(ctx,
		`INSERT INTO up_part VALUES (1, 'dup') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("INSERT(1) DO NOTHING: %v", err)
	}

	// (b) Non-conflicting INSERT into the second partition — must
	// route to up_part2 and write the row through.
	if _, err := setup.ExecContext(ctx,
		`INSERT INTO up_part VALUES (2, 'second') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("INSERT(2) DO NOTHING: %v", err)
	}

	// Final state: rows (1,'initial') and (2,'second') — the duplicate
	// (1,'dup') must NOT have overwritten or been silently dropped.
	rows, err := setup.QueryContext(ctx, `SELECT a, b FROM up_part ORDER BY a`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()
	got := make([]string, 0, 2)
	for rows.Next() {
		var a int
		var b string
		if err := rows.Scan(&a, &b); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, b)
	}
	want := []string{"initial", "second"}
	if len(got) != len(want) {
		t.Fatalf("rows: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("row %d: got %q, want %q", i, got[i], want[i])
		}
	}

	// (c) A second duplicate INSERT against the second partition must
	// also be detected: a successful routing fix without leaf-index
	// maintenance would let the duplicate through.  This pins that
	// applyInsert's maintainArbiter is operating against the leaf's
	// tree (not the parent's empty one).
	if _, err := setup.ExecContext(ctx,
		`INSERT INTO up_part VALUES (2, 'dup-2') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("INSERT(2) DO NOTHING (second): %v", err)
	}
	var n int
	if err := setup.QueryRowContext(ctx, `SELECT count(*) FROM up_part`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("row count after two duplicate INSERTs: got %d, want 2", n)
	}
}
