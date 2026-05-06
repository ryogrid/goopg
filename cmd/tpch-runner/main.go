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
	"database/sql/driver"
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
	cancelAfter := flag.Duration("cancel-after", 0, "send a CancelRequest after this duration (0 = disabled). Uses lib/pq context cancel; server-side query returns SQLSTATE 57014.")
	signalFile := flag.String("signal-file", "", "path to a sentinel file; when the file appears the current query is cancelled (context cancel + CancelRequest) and the runner moves to the next query. The file is removed after detection. Useful for manual mid-run interruption without stopping the whole process.")
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
		runOneWithCancel(db, qn, queries, *perQueryTimeout, *cancelAfter, *signalFile, *doExplain)
	}
}

// runOneWithCancel is a thin wrapper around runOne that passes the
// cancelAfter duration and signalFile through to timeOne.
func runOneWithCancel(db *sql.DB, qn int, queries map[int]string, budget, cancelAfter time.Duration, signalFile string, doExplain bool) {
	body, ok := queries[qn]
	if !ok {
		fmt.Printf("Q%d: no query body — skipping\n", qn)
		return
	}
	if qn == 15 {
		runQ15WithCancel(db, budget, cancelAfter, signalFile, doExplain)
		return
	}
	timeOneWithCancel(db, fmt.Sprintf("Q%d", qn), body, budget, cancelAfter, signalFile, doExplain)
}

// runQ15WithCancel is the Q15 special case with cancel support.
func runQ15WithCancel(db *sql.DB, budget, cancelAfter time.Duration, signalFile string, doExplain bool) {
	if !doExplain {
		timeOneWithCancel(db, "Q15-CREATEVIEW", "create or replace view revenue0 (supplier_no, total_revenue) as "+tpch.Q15ViewBody(), budget, cancelAfter, signalFile, false)
	}
	timeOneWithCancel(db, "Q15a-VIEWBODY", tpch.Q15ViewBody(), budget, cancelAfter, signalFile, doExplain)
	timeOneWithCancel(db, "Q15b-MAIN", tpch.Q15MainSelect(), budget, cancelAfter, signalFile, doExplain)
	if !doExplain {
		_, _ = db.Exec("drop view if exists revenue0")
	}
}

// timeOneWithCancel runs a single SQL statement with two independent
// timers:
//
//   - budget: if the query is still running after budget, close the
//     connection (old behaviour — lib/pq sends CancelRequest then
//     closes).
//   - cancelAfter: if >0 and < budget, fire a context cancel after
//     cancelAfter so lib/pq sends a CancelRequest while the
//     connection stays alive for the next query.
//
// Server-side: goopg already implements CancelRequest (server.go line
// ~751). When the context is cancelled, lib/pq sends the cancel
// packet; the backend returns SQLSTATE 57014; the connection is not
// closed.
//
// Connection isolation: every query acquires a *fresh* `*sql.Conn`
// via db.Conn(ctx) and Close()s it on exit. Combined with
// MaxOpenConns=1, this guarantees a pristine TCP/protocol state
// across queries; one query's error (e.g. SQLSTATE 42883 from Q9's
// LIKE) cannot leave the next query staring at a stale row stream
// (a multi-query M0061-0003 sweep observation).
func timeOneWithCancel(db *sql.DB, label, body string, budget, cancelAfter time.Duration, signalFile string, doExplain bool) {
	stmt := body
	if doExplain {
		stmt = "EXPLAIN " + body
	}
	// Outer context governs the hard wall-clock budget.
	outerCtx, outerCancel := context.WithTimeout(context.Background(), budget)
	defer outerCancel()

	// Per-query cancellable context for the --cancel-after path.
	queryCtx, queryCancel := context.WithCancel(outerCtx)
	defer queryCancel()

	// Fire cancel after cancelAfter (if set and shorter than budget).
	var cancelTimer *time.Timer
	if cancelAfter > 0 && cancelAfter < budget {
		cancelTimer = time.AfterFunc(cancelAfter, queryCancel)
		defer cancelTimer.Stop()
	}

	// Signal-file poller: check every 500ms whether the sentinel file
	// appeared. When found, remove it and cancel the query context so
	// lib/pq sends a CancelRequest while the primary connection stays
	// alive for the next query. The goroutine exits when queryCtx is
	// done (either by this poller, cancelAfter, or the budget timeout).
	if signalFile != "" {
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-queryCtx.Done():
					return
				case <-ticker.C:
					if _, err := os.Stat(signalFile); err == nil {
						fmt.Printf("%s: signal-file detected (%s) — cancelling query\n", label, signalFile)
						os.Remove(signalFile) // best-effort
						queryCancel()
						return
					}
				}
			}
		}()
	}

	// Acquire a dedicated connection for this query and force
	// it to be DISCARDED on return (not pooled). conn.Raw with
	// driver.ErrBadConn marks the underlying connection bad so
	// the subsequent Close() drops it from the pool. The next
	// query then opens a fresh TCP+startup. This isolation
	// prevents a broken-pipe / dangling-row-stream from one
	// query's error path bleeding into the next (a multi-query
	// M0061-0003 sweep observation).
	connCtx, connCancel := context.WithTimeout(context.Background(), 30*time.Second)
	conn, connErr := db.Conn(connCtx)
	connCancel()
	if connErr != nil {
		fmt.Printf("%s: ERROR — db.Conn: %v\n", label, connErr)
		return
	}
	defer func() {
		// Mark the conn bad so it isn't pooled, then close it.
		_ = conn.Raw(func(driverConn interface{}) error { return driver.ErrBadConn })
		conn.Close()
	}()

	t0 := time.Now()
	rows, err := conn.QueryContext(queryCtx, stmt)
	if err != nil {
		fmt.Printf("%s: ERROR after %.2fs — %v\n", label, time.Since(t0).Seconds(), err)
		return
	}
	rowCount := 0
	var explainLines []string
	cols, _ := rows.Columns()
	for rows.Next() {
		rowCount++
		if doExplain {
			// EXPLAIN output: collect all columns of each row (goopg
			// returns a single text column; collect defensively).
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err == nil {
				line := ""
				for i, v := range vals {
					if i > 0 {
						line += "\t"
					}
					if v != nil {
						line += fmt.Sprintf("%v", v)
					}
				}
				explainLines = append(explainLines, line)
			}
		}
	}
	closeErr := rows.Err()
	rows.Close()
	elapsed := time.Since(t0)
	if closeErr != nil {
		fmt.Printf("%s: ERROR after %.2fs (%d rows scanned) — %v\n", label, elapsed.Seconds(), rowCount, closeErr)
		return
	}
	if doExplain {
		fmt.Printf("%s: EXPLAIN plan:\n", label)
		for _, l := range explainLines {
			fmt.Printf("  %s\n", l)
		}
		fmt.Printf("%s: OK elapsed=%.2fs\n", label, elapsed.Seconds())
	} else {
		fmt.Printf("%s: OK elapsed=%.2fs rows=%d\n", label, elapsed.Seconds(), rowCount)
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
