package server

// Restart durability for INDEXES created under a distinct-dbOid database
// (M0127-P5.6-f-pre; the index half of M0122-0007 4e follow-up 39, whose own
// ledger row deferred it — design 0122-0018-per-database-catalog-namespace.md).
//
// Follow-up 39 routed a user TABLE's pg_class/pg_attribute rows to the
// connection database's own catalog heap but left index rows pinned to
// DefaultDBOid, and justified that as safe because "index rows have a working
// WAL-replay durability path already": RecordKindCreateIndex(20) /
// DropIndex(21). B5 Slice A then retired kinds 20/21 on the premise that
// loadUserIndexesFromHeap replaced them — but that reload only ever scanned
// ONE database. The two changes composed into a silent regression: on any
// non-default database, CREATE INDEX (and the unique index behind a PRIMARY
// KEY, and ALTER TABLE ADD CONSTRAINT) was durable NOWHERE and vanished at the
// next restart.
//
// It was measured, not hypothesised: the TPC-H bench cluster on db `tpch`
// (bench/tpch/runtime_goopg) carries ZERO indexes and ZERO constraints while
// the PG 18.3 reference cluster it is diffed against carries 16 and 8 — every
// one of HammerDB's `partsupp_pk`, `lineitem_part_supp_fkidx` &c. was created
// during the load (their names are still in that cluster's pg_wal) and gone by
// the first restart. That is why goopg's Q9 plan cannot resemble PG's, which
// index-scans lineitem through the composite FK index.
//
// These tests pin the fixed behaviour end-to-end over the real wire protocol
// plus a real data-directory round trip, in the same shape as
// table_dbid_restart_test.go's own.

import (
	"context"
	"testing"

	"github.com/goopg/goopg/internal/initdb"

	_ "github.com/lib/pq"
)

// TestDistinctDatabaseIndexSurvivesRestartInOwnNamespace: an index created
// under a distinct-dbOid database is still registered in that database's
// catalog after a full data-dir round trip — including the UNIQUE flag and the
// composite column list, which is the evidence the join-size estimator's
// superkey rule reads — and a dropped sibling index stays dropped.
func TestDistinctDatabaseIndexSurvivesRestartInOwnNamespace(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}

	ctx := context.Background()
	s1 := startDBIDRestartServer(t, dir)

	pg := s1.open(t, "postgres")
	if _, err := pg.ExecContext(ctx, "CREATE DATABASE idx_restart_db"); err != nil {
		pg.Close()
		s1.close(t)
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	pg.Close()

	db1 := s1.open(t, "idx_restart_db")
	for _, stmt := range []string{
		"CREATE TABLE ps(pkey int, skey int, cost int)",
		// Composite UNIQUE — the exact shape of TPC-H's partsupp_pk, and the
		// only evidence a two-column equi-join can be proven non-fanning-out
		// from.
		"CREATE UNIQUE INDEX ps_pk ON ps(pkey, skey)",
		"CREATE INDEX ps_cost_idx ON ps(cost)",
		// A dropped sibling proves the xmax stamp routes to the same
		// per-database heap the insert went to (tableCatalogDBOids): a stamp
		// that missed would resurrect ps_gone after the restart.
		"CREATE INDEX ps_gone ON ps(skey)",
		"DROP INDEX ps_gone",
	} {
		if _, err := db1.ExecContext(ctx, stmt); err != nil {
			db1.Close()
			s1.close(t)
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db1.Close()
	s1.close(t)

	// Full restart from the same data dir.
	s2 := startDBIDRestartServer(t, dir)
	defer s2.close(t)

	db1 = s2.open(t, "idx_restart_db")
	defer db1.Close()

	got := map[string]string{}
	rows, err := db1.QueryContext(ctx,
		"SELECT indexname, indexdef FROM pg_indexes WHERE tablename = 'ps'")
	if err != nil {
		t.Fatalf("post-restart pg_indexes: %v", err)
	}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[name] = def
	}
	rows.Close()

	if _, ok := got["ps_pk"]; !ok {
		t.Fatalf("ps_pk did not survive the restart; pg_indexes on ps = %v", got)
	}
	if _, ok := got["ps_cost_idx"]; !ok {
		t.Fatalf("ps_cost_idx did not survive the restart; pg_indexes on ps = %v", got)
	}
	if _, ok := got["ps_gone"]; ok {
		t.Fatalf("dropped index ps_gone resurrected after restart")
	}

	// UNIQUE and the composite key order must survive too: a reloaded index
	// that lost `Unique` still shows up in pg_indexes but proves nothing about
	// fan-out, so the planner evidence would be silently wrong rather than
	// silently absent.
	if def := got["ps_pk"]; !containsAll(def, "UNIQUE", "pkey", "skey") {
		t.Fatalf("ps_pk lost UNIQUE or its key columns after restart: %q", def)
	}

	// Enforcement is the behavioural half of the same fact: an index that
	// reloaded as catalog-only metadata would not reject the duplicate.
	if _, err := db1.ExecContext(ctx, "INSERT INTO ps VALUES (1,1,10)"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := db1.ExecContext(ctx, "INSERT INTO ps VALUES (1,1,20)"); err == nil {
		t.Fatalf("post-restart duplicate on the unique index (1,1) was accepted")
	}
}

// containsAll reports whether s contains every one of subs.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
