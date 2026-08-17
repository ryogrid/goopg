package postmaster

// Restart durability for ANALYZE statistics — M0125-0029 (warm-statistics
// programme, design docs/design/0125-0028-warm-stats-programme.md §-0029).
//
// Three measured gaps close together and these tests pin each end-to-end
// over the real wire protocol + a real data-dir round trip:
//
//  1. Per-DB routing: persistStatsToPGStatistic hardcoded
//     DBOid=DefaultDBOid and loadStatisticsFromHeap read only cat.DBOID(),
//     so a distinct-dbOid database's ANALYZE never round-tripped a restart
//     (written to a heap whose relids the reload could not resolve).
//  2. The size itself: pg_statistic has no reltuples/relpages slot and
//     goopg's pg_class renders them virtually from Table.Stats, so even the
//     default database forgot RowCount/Pages on restart (ledger pq-P6).
//     They now persist via the goopg-private sidecar heap
//     (catalog.GoopgRelStatsRelationId), under the 2026-07-30(b) directive's
//     explicit PG-faithfulness waiver.
//  3. Cross-connection visibility: a NEW connection (and a restarted
//     server's first connection) must plan with the restored stats — the
//     2026-07-23 "per-connection stats" symptom.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/initdb"

	_ "github.com/lib/pq"
)

// explainText renders EXPLAIN output as one comparable string. Plain EXPLAIN
// (no ANALYZE) is deterministic given identical statistics, so equality
// across a restart proves the planner consumed the restored stats rather
// than re-deriving defaults.
func explainText(t *testing.T, ctx context.Context, q func(context.Context, string, ...any) (rowsScanner, error), sql string) string {
	t.Helper()
	rows, err := q(ctx, "EXPLAIN "+sql)
	if err != nil {
		t.Fatalf("EXPLAIN %s: %v", sql, err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("EXPLAIN scan: %v", err)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// rowsScanner is the subset of *sql.Rows the helper needs (keeps the helper
// signature honest about what it uses).
type rowsScanner interface {
	Next() bool
	Scan(...any) error
	Close() error
}

// TestAnalyzeStatsSurviveRestartPerDatabase: ANALYZE in a distinct-dbOid
// database → full restart → a NEW connection reads reltuples/relpages > 0
// from pg_class and plans identically to the pre-restart session, with zero
// re-ANALYZE. This is the M0125-0029 acceptance shape at unit scale.
func TestAnalyzeStatsSurviveRestartPerDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}

	ctx := context.Background()
	s1 := startDBIDRestartServer(t, dir)

	pg := s1.open(t, "postgres")
	if _, err := pg.ExecContext(ctx, "CREATE DATABASE stats_restart_db"); err != nil {
		pg.Close()
		s1.close(t)
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	pg.Close()

	db1 := s1.open(t, "stats_restart_db")
	if _, err := db1.ExecContext(ctx, "CREATE TABLE t_stats(a int, b text)"); err != nil {
		db1.Close()
		s1.close(t)
		t.Fatalf("CREATE TABLE: %v", err)
	}
	// Skewed data so the per-column stats are non-trivial (MCV a=1).
	var vals []string
	for i := 0; i < 60; i++ {
		a := 1
		if i%4 == 0 {
			a = 2 + i%7
		}
		vals = append(vals, fmt.Sprintf("(%d,'v%d')", a, i))
	}
	if _, err := db1.ExecContext(ctx, "INSERT INTO t_stats VALUES "+strings.Join(vals, ",")); err != nil {
		db1.Close()
		s1.close(t)
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := db1.ExecContext(ctx, "ANALYZE t_stats"); err != nil {
		db1.Close()
		s1.close(t)
		t.Fatalf("ANALYZE: %v", err)
	}

	const sizeQ = "SELECT reltuples, relpages FROM pg_class WHERE relname = 't_stats'"
	var preTuples, prePages float64
	if err := db1.QueryRowContext(ctx, sizeQ).Scan(&preTuples, &prePages); err != nil {
		db1.Close()
		s1.close(t)
		t.Fatalf("pre-restart size probe: %v", err)
	}
	if preTuples != 60 || prePages < 1 {
		db1.Close()
		s1.close(t)
		t.Fatalf("pre-restart reltuples=%v relpages=%v, want 60 and >=1 — ANALYZE itself is broken", preTuples, prePages)
	}
	const planSQL = "SELECT * FROM t_stats WHERE a = 1"
	prePlan := explainText(t, ctx, func(c context.Context, q string, args ...any) (rowsScanner, error) {
		return db1.QueryContext(c, q, args...)
	}, planSQL)
	db1.Close()
	s1.close(t)

	// Full restart from the same data dir; NO re-ANALYZE from here on.
	s2 := startDBIDRestartServer(t, dir)
	defer s2.close(t)

	db1 = s2.open(t, "stats_restart_db")
	defer db1.Close()
	var postTuples, postPages float64
	if err := db1.QueryRowContext(ctx, sizeQ).Scan(&postTuples, &postPages); err != nil {
		t.Fatalf("post-restart size probe: %v", err)
	}
	if postTuples != preTuples || postPages != prePages {
		t.Fatalf("post-restart reltuples=%v relpages=%v, want %v/%v — the sidecar/per-DB routing lost the size",
			postTuples, postPages, preTuples, prePages)
	}
	postPlan := explainText(t, ctx, func(c context.Context, q string, args ...any) (rowsScanner, error) {
		return db1.QueryContext(c, q, args...)
	}, planSQL)
	// A heap-reloaded table registers with an explicit "public" schema while
	// a freshly created one registers with "", so post-restart EXPLAIN
	// schema-qualifies the relation name (real PG qualifies only under
	// VERBOSE — deferral-ledger row, M0125-0029). Cosmetic; normalize so the
	// comparison pins what this test is about: costs and row ESTIMATES,
	// which only match if the restored stats were consumed.
	postPlan = strings.ReplaceAll(postPlan, "public.t_stats", "t_stats")
	if postPlan != prePlan {
		t.Fatalf("post-restart plan differs from pre-restart plan — planner did not consume restored stats.\npre:\n%s\npost:\n%s",
			prePlan, postPlan)
	}

	// Gap 3: a SECOND, separately dialed connection must see the same
	// restored stats (per-connection catalog views over the shared table).
	db2 := s2.open(t, "stats_restart_db")
	defer db2.Close()
	var tuples2 float64
	if err := db2.QueryRowContext(ctx, "SELECT reltuples FROM pg_class WHERE relname = 't_stats'").Scan(&tuples2); err != nil {
		t.Fatalf("second-connection probe: %v", err)
	}
	if tuples2 != preTuples {
		t.Fatalf("second connection reltuples=%v, want %v — stats invisible across connections", tuples2, preTuples)
	}
}

// TestAnalyzeStatsSurviveRestartDefaultDatabase: the same round trip for a
// table in the default `postgres` database. Before M0125-0029 the per-COLUMN
// stats reloaded here (main-DB pass existed) but reltuples/relpages read 0
// after every restart because nothing persisted them (ledger pq-P6) — this
// pins the sidecar mechanism on the main-DB pass.
func TestAnalyzeStatsSurviveRestartDefaultDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}

	ctx := context.Background()
	s1 := startDBIDRestartServer(t, dir)

	pg := s1.open(t, "postgres")
	for _, stmt := range []string{
		"CREATE TABLE t_defstats(a int)",
		"INSERT INTO t_defstats VALUES (1),(1),(2),(3),(3)",
		"ANALYZE t_defstats",
	} {
		if _, err := pg.ExecContext(ctx, stmt); err != nil {
			pg.Close()
			s1.close(t)
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	var preTuples float64
	if err := pg.QueryRowContext(ctx, "SELECT reltuples FROM pg_class WHERE relname = 't_defstats'").Scan(&preTuples); err != nil {
		pg.Close()
		s1.close(t)
		t.Fatalf("pre-restart probe: %v", err)
	}
	pg.Close()
	s1.close(t)
	if preTuples < 1 {
		t.Fatalf("pre-restart reltuples=%v, want >=1", preTuples)
	}

	s2 := startDBIDRestartServer(t, dir)
	defer s2.close(t)
	pg = s2.open(t, "postgres")
	defer pg.Close()
	var postTuples float64
	if err := pg.QueryRowContext(ctx, "SELECT reltuples FROM pg_class WHERE relname = 't_defstats'").Scan(&postTuples); err != nil {
		t.Fatalf("post-restart probe: %v", err)
	}
	if postTuples != preTuples {
		t.Fatalf("post-restart reltuples=%v, want %v — default-DB relation size not persisted", postTuples, preTuples)
	}
}
