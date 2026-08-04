// Command estimate-audit runs the class-(a) regression tripwire of
// docs/design/leftdeep-joins/09-verification-and-acceptance.md §5 against a
// running goopg TPC-H cluster: it EXPLAIN-ANALYZEs each TPC-H query, compares
// the planner's per-joinrel row estimate against the executor's actual count,
// and flags every joinrel more than 10³× off (10² for Q9's final joinrel,
// §5's stated demonstration that 04 §3's sizing mechanisms worked).
//
//	estimate-audit --label 2026-08-04-baseline [--queries 3,5,9,10] [--port 65433]
//
// The report is written to analysis/leftdeep-joins/<label>.txt (§5: "output
// committed under analysis/leftdeep-joins/") and the process exits non-zero
// when a joinrel is over its threshold, so the same binary serves as both the
// audit instrument and the tripwire.
//
// Two properties are deliberate:
//
//   - EXPLAIN ANALYZE **executes** the query. Unlike cmd/plan-snapshot (which
//     plans only, and finishes in seconds), an audit run costs a full TPC-H
//     power run. --timeout bounds each query; a query that exceeds it is
//     recorded as UNMEASURED rather than dropped, because a silently missing
//     query reads as an audited-and-clean one.
//   - The estimate side is read from the `rows=` of the cost parenthetical,
//     so the server must not be running with `SET costs = off`.
//   - goopg does not propagate ANALYZE instrumentation out of parallel
//     workers: under a `Gather`, the Gather node itself reports `actual
//     rows=`, but every node beneath it reports estimates only (upstream
//     collects per-worker Instrumentation in execParallel.c
//     `ExecParallelRetrieveInstrumentation` and merges it into the leader's).
//     TPC-H Q9 — the chain 09 §5 states its acceptance criterion on — plans
//     entirely below a Gather, so it is unmeasurable in a parallel plan.
//     --serial (default) therefore sets `max_parallel_workers_per_gather = 0`
//     on the audit session. The audited join TREE is the same one the
//     parallel plan builds; only the Gather disappears.
//   - goopg's ANALYZE statistics are PER-CONNECTION, and a bare `ANALYZE;`
//     is a no-op: without an explicit `ANALYZE <table>` for each table in the
//     SAME session, the planner estimates blind and the audit measures the
//     no-stats planner rather than the real one. The run therefore holds ONE
//     stats-warmed session for every query, and re-warms a fresh one whenever
//     a query error kills the old one (an errored pooled session silently
//     returns empty rows for everything after it — M0076-0006).
//
// See also docs/design/leftdeep-joins/09-verification-and-acceptance.md §5
// and the parsing rules in internal/estimateaudit.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/goopg/goopg/internal/estimateaudit"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

const defaultOutDir = "analysis/leftdeep-joins"

type flags struct {
	host     string
	port     int
	db       string
	user     string
	pass     string
	label    string
	queries  string
	outDir   string
	timeout  time.Duration
	tripwire float64
	finalMax float64
	failOn   bool
	keepPlan bool
	analyze  bool
	serial   bool
}

func main() {
	f := parseFlags(os.Args[1:])
	db := openDB(f)
	defer db.Close()

	s := &session{db: db, warmup: f.analyze, serial: f.serial, timeout: f.timeout}
	defer s.close()

	reports := make([]estimateaudit.QueryReport, 0, 22)
	var plans strings.Builder
	for _, qn := range selectQueries(f.queries) {
		name, sqlText := queryFor(qn)
		if sqlText == "" {
			reports = append(reports, estimateaudit.AuditError(name, "query SQL not found in tpch.Queries()"))
			continue
		}
		fmt.Fprintf(os.Stderr, "%s ... ", name)
		start := time.Now()
		text, err := s.explainAnalyze(sqlText)
		if err != nil {
			fmt.Fprintf(os.Stderr, "UNMEASURED (%v after %.0fs)\n", err, time.Since(start).Seconds())
			reports = append(reports, estimateaudit.AuditError(name, err.Error()))
			continue
		}
		r := estimateaudit.Audit(name, text)
		fmt.Fprintf(os.Stderr, "%d joins in %.0fs\n", len(r.Joins), time.Since(start).Seconds())
		reports = append(reports, r)
		if f.keepPlan {
			fmt.Fprintf(&plans, "=== %s\n%s\n\n", name, text)
		}
	}

	th := estimateaudit.Thresholds{
		Tripwire:          f.tripwire,
		FinalJoin:         f.tripwire,
		FinalJoinPerQuery: map[string]float64{"Q9": f.finalMax},
	}
	out := estimateaudit.Render(reports, th)
	writeReport(f, out, plans.String())
	fmt.Print(out)

	v := estimateaudit.Violations(reports, th)
	if f.failOn && len(v) > 0 {
		fmt.Fprintf(os.Stderr, "estimate-audit: %d joinrel(s) over threshold\n", len(v))
		os.Exit(1)
	}
}

func parseFlags(args []string) *flags {
	f := &flags{}
	fs := flag.NewFlagSet("estimate-audit", flag.ExitOnError)
	fs.StringVar(&f.host, "host", "127.0.0.1", "goopg host")
	fs.IntVar(&f.port, "port", 65433, "goopg port (TPC-H bench cluster)")
	fs.StringVar(&f.db, "db", "tpch", "database name")
	fs.StringVar(&f.user, "user", "tpch", "user")
	fs.StringVar(&f.pass, "password", "tpch", "password")
	fs.StringVar(&f.label, "label", "", "report filename stem (required)")
	fs.StringVar(&f.queries, "queries", "", "comma-or-range list (e.g. 3,5,9-11); empty = all 22")
	fs.StringVar(&f.outDir, "out", defaultOutDir, "directory for the committed report")
	fs.DurationVar(&f.timeout, "timeout", 10*time.Minute, "per-query EXPLAIN ANALYZE timeout (the query is EXECUTED)")
	fs.Float64Var(&f.tripwire, "tripwire", estimateaudit.DefaultTripwire, "flag any joinrel off by more than this factor")
	fs.Float64Var(&f.finalMax, "final-max", estimateaudit.DefaultFinalJoinMax, "tighter bar on Q9's final joinrel (09 §5)")
	fs.BoolVar(&f.failOn, "fail-on-violation", true, "exit 1 when a joinrel is over threshold")
	fs.BoolVar(&f.keepPlan, "keep-plans", true, "also write the raw EXPLAIN ANALYZE text next to the report")
	fs.BoolVar(&f.serial, "serial", true, "disable parallel workers (nodes under a Gather report no actual rows)")
	fs.BoolVar(&f.analyze, "warm-stats", true, "ANALYZE each TPC-H table on the audit session first (goopg stats are per-connection)")
	fs.Parse(args)
	if f.label == "" {
		fmt.Fprintln(os.Stderr, "--label is required")
		fs.Usage()
		os.Exit(2)
	}
	return f
}

// queryFor maps a TPC-H query number to its report name and SQL. Q15 is
// special-cased exactly as cmd/plan-snapshot does: its first statement is a
// CREATE VIEW the main select depends on, and running the main select alone
// errors with "relation revenue0 does not exist", so the standalone view-body
// SELECT is audited instead (M0076-0006).
func queryFor(qn int) (string, string) {
	if qn == 15 {
		return "Q15a-VIEWBODY", tpch.Q15ViewBody()
	}
	return fmt.Sprintf("Q%d", qn), tpch.Queries()[qn]
}

// session is the one stats-warmed connection every query runs on. It is a
// single connection rather than the pool because goopg keeps ANALYZE
// statistics per connection: a pooled `db.QueryContext` may land on a cold
// session and silently measure the no-stats planner.
type session struct {
	db      *sql.DB
	conn    *sql.Conn
	warmup  bool
	serial  bool
	timeout time.Duration
}

func (s *session) ensure(ctx context.Context) error {
	if s.conn != nil {
		return nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("conn: %w", err)
	}
	if s.serial {
		// Without this every joinrel under a Gather audits as "(no
		// ANALYZE)" — see the package comment.
		if _, err := conn.ExecContext(ctx, "SET max_parallel_workers_per_gather = 0"); err != nil {
			conn.Close()
			return fmt.Errorf("SET max_parallel_workers_per_gather: %w", err)
		}
	}
	if s.warmup {
		for _, t := range tpch.Tables() {
			if _, err := conn.ExecContext(ctx, "ANALYZE "+t.Name); err != nil {
				conn.Close()
				return fmt.Errorf("ANALYZE %s: %w", t.Name, err)
			}
		}
	}
	s.conn = conn
	return nil
}

func (s *session) close() {
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
}

// explainAnalyze runs one query under EXPLAIN ANALYZE. Any error drops the
// session: a goopg connection that has errored returns empty rows for every
// subsequent statement, which would turn later queries into silent
// zero-row "audits" (M0076-0006).
func (s *session) explainAnalyze(query string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if err := s.ensure(ctx); err != nil {
		return "", err
	}
	rows, err := s.conn.QueryContext(ctx, "EXPLAIN ANALYZE "+query)
	if err != nil {
		s.close()
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			s.close()
			return "", fmt.Errorf("scan: %w", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		s.close()
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

func writeReport(f *flags, report, plans string) {
	if err := os.MkdirAll(f.outDir, 0o755); err != nil {
		fatal("mkdir %s: %v", f.outDir, err)
	}
	path := filepath.Join(f.outDir, f.label+".txt")
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		fatal("write %s: %v", path, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", path)
	if plans == "" {
		return
	}
	planPath := filepath.Join(f.outDir, f.label+".plans.txt")
	if err := os.WriteFile(planPath, []byte(plans), 0o644); err != nil {
		fatal("write %s: %v", planPath, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", planPath)
}

func selectQueries(spec string) []int {
	if strings.TrimSpace(spec) == "" {
		out := make([]int, 22)
		for i := range out {
			out[i] = i + 1
		}
		return out
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, "-"); i > 0 {
			lo, _ := strconv.Atoi(strings.TrimSpace(part[:i]))
			hi, _ := strconv.Atoi(strings.TrimSpace(part[i+1:]))
			for q := lo; q <= hi; q++ {
				seen[q] = true
			}
			continue
		}
		if v, err := strconv.Atoi(part); err == nil {
			seen[v] = true
		}
	}
	out := make([]int, 0, len(seen))
	for q := range seen {
		out = append(out, q)
	}
	sort.Ints(out)
	return out
}

func openDB(f *flags) *sql.DB {
	connStr := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=disable",
		f.host, f.port, f.db, f.user, f.pass)
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		fatal("sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		fatal("ping: %v (is the TPC-H bench cluster up on %s:%d? bench/tpch/setup_goopg.sh)", err, f.host, f.port)
	}
	return db
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
