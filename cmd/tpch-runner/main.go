// Command tpch-runner is a minimal TPC-H query driver that submits the
// HammerDB Q1-Q22 SQL against a running goopg cluster, one query at a
// time, with a per-query wall-clock budget. Designed for development
// debugging when HammerDB's tclsh-based driver is too coarse-grained
// (its 7200 s budget runs the entire stream and aborts on the first
// stall) — this tool sets `context.WithTimeout(ctx, perQueryTimeout)`
// per query so each Q's success / failure / timeout is observable in
// isolation.
//
// Not a TPC-H benchmark: only the per-query timing is reported. The
// canonical HammerDB driver remains the reference for compliance.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/goopg/goopg/internal/testutil/tpch"
)

func main() {
	host := flag.String("host", "127.0.0.1", "goopg host")
	port := flag.Int("port", 65433, "goopg port")
	dbName := flag.String("db", "tpch", "database name")
	user := flag.String("user", "tpch", "user")
	pass := flag.String("password", "tpch", "password")
	queriesFlag := flag.String("queries", "", "comma-separated query numbers (1..22). empty = all")
	perQueryTimeout := flag.Duration("per-query-timeout", 600*time.Second, "per-query wall clock budget")
	doExplain := flag.Bool("explain", false, "issue EXPLAIN <query> instead of the query body")
	doCheckpoint := flag.Bool("checkpoint", false, "issue a CHECKPOINT and exit (ignore -queries)")
	flag.Parse()

	connStr := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=disable",
		*host, *port, *dbName, *user, *pass)
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		fail("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		fail("ping: %v", err)
	}

	if *doCheckpoint {
		ctx2, cancel2 := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel2()
		if _, err := db.ExecContext(ctx2, "CHECKPOINT"); err != nil {
			fail("CHECKPOINT: %v", err)
		}
		fmt.Println("CHECKPOINT OK")
		return
	}

	queries := tpch.Queries()
	wantQs := selectQueries(*queriesFlag)
	for _, qn := range wantQs {
		runOne(db, qn, queries, *perQueryTimeout, *doExplain)
	}
}

// runOne dispatches a single Q. Q15 is special-cased into
// CREATE VIEW + main SELECT + DROP VIEW (matches HammerDB's
// shape); the timing reports for Q15a (view body) and Q15b
// (main SELECT) are emitted separately.
func runOne(db *sql.DB, qn int, queries map[int]string, budget time.Duration, doExplain bool) {
	body, ok := queries[qn]
	if !ok {
		fmt.Printf("Q%d: no query body — skipping\n", qn)
		return
	}
	if qn == 15 {
		runQ15(db, budget, doExplain)
		return
	}
	timeOne(db, fmt.Sprintf("Q%d", qn), body, budget, doExplain)
}

// runQ15 issues the CREATE VIEW, the main SELECT, and the DROP
// VIEW; the per-statement timings are reported individually so
// the SELECT (the EXPLAIN-able shape that matters for plan
// inspection) is observable in isolation.
func runQ15(db *sql.DB, budget time.Duration, doExplain bool) {
	if !doExplain {
		// CREATE VIEW must succeed first so the main SELECT
		// resolves the view reference.
		timeOne(db, "Q15-CREATEVIEW", tpch.Q15ViewBody()[:0]+"create or replace view revenue0 (supplier_no, total_revenue) as "+tpch.Q15ViewBody(), budget, false)
	}
	timeOne(db, "Q15a-VIEWBODY", tpch.Q15ViewBody(), budget, doExplain)
	timeOne(db, "Q15b-MAIN", tpch.Q15MainSelect(), budget, doExplain)
	if !doExplain {
		_, _ = db.Exec("drop view if exists revenue0")
	}
}

// timeOne wraps the per-query plumbing: build context with
// budget, run query (or EXPLAIN of it), measure wall-clock,
// drain rows, and print a one-line summary.
func timeOne(db *sql.DB, label, body string, budget time.Duration, doExplain bool) {
	stmt := body
	if doExplain {
		stmt = "EXPLAIN " + body
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	t0 := time.Now()
	rows, err := db.QueryContext(ctx, stmt)
	if err != nil {
		fmt.Printf("%s: ERROR after %.2fs — %v\n", label, time.Since(t0).Seconds(), err)
		return
	}
	rowCount := 0
	for rows.Next() {
		rowCount++
	}
	closeErr := rows.Err()
	rows.Close()
	elapsed := time.Since(t0)
	status := "OK"
	if closeErr != nil {
		status = "ERROR"
		fmt.Printf("%s: %s after %.2fs (%d rows scanned) — %v\n", label, status, elapsed.Seconds(), rowCount, closeErr)
		return
	}
	fmt.Printf("%s: %s elapsed=%.2fs rows=%d\n", label, status, elapsed.Seconds(), rowCount)
}

// selectQueries parses the -queries flag into a sorted list of
// query numbers. An empty value selects every Q from 1 to 22 in
// canonical order; a comma-separated list (e.g., "20,6,17") runs
// the named queries in the given order.
func selectQueries(s string) []int {
	if strings.TrimSpace(s) == "" {
		out := make([]int, 0, 22)
		for i := 1; i <= 22; i++ {
			out = append(out, i)
		}
		return out
	}
	out := []int{}
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			fail("invalid query number %q: %v", part, err)
		}
		if n < 1 || n > 22 {
			fail("query number out of range [1,22]: %d", n)
		}
		out = append(out, n)
	}
	if len(out) > 1 {
		sortedCopy := append([]int(nil), out...)
		sort.Ints(sortedCopy)
		// Keep user-supplied order for transparency.
		_ = sortedCopy
	}
	return out
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "tpch-runner: "+format+"\n", args...)
	os.Exit(1)
}
