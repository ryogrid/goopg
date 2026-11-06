package testport

// Server-level pin for the M-NIGHTLY task "Post-TRUNCATE empty-table cost
// estimate diverges from PG" (filed 2026-10-31 by the relpages recon loop).
// The filed rig (tmp/mnightly-trunc-rig.sh) measured, on
// `(id int, pad text)` with 100-char pads, after ANALYZE + TRUNCATE:
//
//	PG 18.3:    pg_class (reltuples,relpages) = (-1,0), Seq Scan cost=0.00..16.10 rows=610
//	goopg:      (0,0), Seq Scan cost=0.00..22.70 rows=1270
//
// Two causes, both fixed: (a) TRUNCATE now persists PG's new-relfilenode
// reltuples=-1 sentinel (sidecar rowcount=-1, decode flips Analyzed off)
// instead of a measured zero; (b) the probe's tuple width comes from the
// SURVIVING pg_statistic average widths (PG keeps pg_statistic across
// TRUNCATE — the relation OID is unchanged), where goopg probed the
// get_typavgwidth type defaults (36+28=64 B/tuple → 127 rows/page → 1270;
// PG measures 4+101=105, +28 overhead → 133 B/tuple → 61 rows/page → 610).
//
// This test runs the fixture on goopg and on a stock PG 18.3 cluster,
// truncates, and compares the Seq Scan cost line (the width= rendering
// column stays a documented divergence — see the ledger row; it is stripped
// before comparison). The goopg arm then RESTARTS the server: the sentinel
// must survive the sidecar reload (pg_class parity persistence, like PG's
// own pg_class tuple), and the probe must land on 610 again from the
// restored column stats.

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

// costLine extracts the cost/rows pair from a Seq Scan plan line.
var costLine = regexp.MustCompile(`cost=([0-9.]+)\.\.([0-9.]+) rows=([0-9]+)`)

func seqScanCost(t *testing.T, queryRow func(query string, args ...any) (*sql.Rows, error), table string) string {
	t.Helper()
	rows, err := queryRow("EXPLAIN SELECT count(*) FROM " + table)
	if err != nil {
		t.Fatalf("EXPLAIN on %s: %v", table, err)
	}
	defer rows.Close()
	var scanLine string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan EXPLAIN line: %v", err)
		}
		if strings.Contains(line, "Seq Scan on "+table) {
			scanLine = strings.TrimSpace(line)
		}
	}
	m := costLine.FindString(scanLine)
	if m == "" {
		t.Fatalf("no cost pair in Seq Scan line %q", scanLine)
	}
	return m
}

func relStatsPair(t *testing.T, queryRow func(query string, args ...any) (*sql.Rows, error), table string) string {
	t.Helper()
	rows, err := queryRow("SELECT reltuples::text || '|' || relpages::text FROM pg_class WHERE relname = '" + table + "'")
	if err != nil {
		t.Fatalf("pg_class pair on %s: %v", table, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("pg_class: no row for %s", table)
	}
	var pair string
	if err := rows.Scan(&pair); err != nil {
		t.Fatalf("scan pg_class pair: %v", err)
	}
	return pair
}

func TestTruncateCostParityWithPG(t *testing.T) {
	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	pgcluster.Available(t, binDir)

	const table = "tcost_tr"
	const nRows = 5000
	payload := strings.Repeat("x", 100)
	fixture := []string{
		"CREATE TABLE " + table + "(id int, pad text)",
		"INSERT INTO " + table + " SELECT g, '" + payload + "' FROM generate_series(1, " + strconv.Itoa(nRows) + ") g",
		"ANALYZE " + table,
	}

	// --- PG 18.3 oracle arm ---
	pg, err := pgcluster.New("trunc-cost-pg", pgcluster.Options{RepoRoot: repo})
	if err != nil {
		t.Fatalf("pgcluster.New: %v", err)
	}
	if err := pg.Start(); err != nil {
		t.Fatalf("pg start: %v", err)
	}
	defer func() { _ = pg.Stop() }()
	for _, stmt := range append(fixture, "TRUNCATE "+table) {
		pg.Exec(t, stmt)
	}
	pgPair := pg.QueryScalar(t, "SELECT reltuples::text || '|' || relpages::text FROM pg_class WHERE relname = '"+table+"'")
	pgDB, err := pg.OpenDB()
	if err != nil {
		t.Fatalf("pg OpenDB: %v", err)
	}
	defer pgDB.Close()
	pgCost := seqScanCost(t, pgDB.Query, table)
	if pgPair != "-1|0" {
		t.Fatalf("PG oracle pair after TRUNCATE = %q, want -1|0 (fixture drift?)", pgPair)
	}

	// --- goopg arm ---
	c, err := cluster.New("trunc-cost-parity", cluster.Options{
		RepoRoot:     repo,
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	for _, stmt := range fixture {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	// Sanity: ANALYZE landed (pre-truncate pair is a measured positive).
	preDB, err := sql.Open("postgres", c.DSN())
	if err != nil {
		t.Fatal(err)
	}
	prePair := relStatsPair(t, preDB.Query, table)
	preDB.Close()
	if prePair == "-1|0" || !strings.Contains(prePair, "|") || strings.HasPrefix(prePair, "0|") {
		t.Fatalf("goopg pre-TRUNCATE pg_class pair = %q, want analyzed positive counts", prePair)
	}
	if err := runSQLSimple(t, c, "TRUNCATE "+table); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}

	db, err := sql.Open("postgres", c.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// (1) Post-TRUNCATE: PG's sentinel pair and PG's probe cost.
	if pair := relStatsPair(t, db.Query, table); pair != pgPair {
		t.Errorf("goopg post-TRUNCATE pg_class pair = %q, PG = %q", pair, pgPair)
	}
	goCost := seqScanCost(t, db.Query, table)
	if goCost != pgCost {
		t.Errorf("goopg post-TRUNCATE Seq Scan %q, PG %q", goCost, pgCost)
	}

	// (2) Restart: the -1 sentinel persists through the sidecar reload and
	// the probe still lands on PG's answer from the restored column widths.
	if err := c.Restart(cluster.ShutdownFast); err != nil {
		t.Fatalf("restart: %v", err)
	}
	rdb, err := sql.Open("postgres", c.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	if pair := relStatsPair(t, rdb.Query, table); pair != pgPair {
		t.Errorf("goopg post-restart pg_class pair = %q, PG = %q", pair, pgPair)
	}
	goCost2 := seqScanCost(t, rdb.Query, table)
	if goCost2 != pgCost {
		t.Errorf("goopg post-restart Seq Scan %q, PG %q", goCost2, pgCost)
	}
}
