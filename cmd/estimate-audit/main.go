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
//     query reads as an audited-and-clean one. --plan-only drops back to plain
//     EXPLAIN for the questions that are decided at PLANNING time — the §4
//     clause-6 spine diff and the enumeration provenance — and omits the two
//     sections that cannot be scored without actuals.
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

	"github.com/goopg/goopg/internal/testutil/estimateaudit"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

const defaultOutDir = "analysis/leftdeep-joins"

// planOnlyBanner replaces the §5 estimate block in a --plan-only run, and says
// what such a run is NOT.
//
// The temptation is to print the §5 table anyway — every row would simply read
// `actual=? (no ANALYZE)`, which the renderer already supports for nodes under
// a Gather. That is the one way this artifact could lie: `Violations` counts
// only joinrels that HAVE an actual, so a plan-only §5 block is a table of
// unmeasured rows followed by a clean verdict, indistinguishable at a glance
// from an audit that ran and found nothing. The section is therefore omitted,
// not emptied, and --fail-on-violation is forced off with it.
const planOnlyBanner = "" +
	"=== PLAN-ONLY RUN — §4 clause 6 channel only (09 §5 and the §4 parity ratchet are NOT measured)\n" +
	"  Queries were EXPLAINed, not EXPLAIN ANALYZEd. Both the §5 estimate audit\n" +
	"  and the §4 parity ratchet score an estimate against an ACTUAL row count,\n" +
	"  which this run does not have, so both sections are omitted rather than\n" +
	"  printed empty, and --fail-on-violation is off. Do not cite this artifact\n" +
	"  as a §5 or §4 result.\n" +
	"  What IS measured: the join-spine pairing diff and, with --enum-trace, the\n" +
	"  clause-6 enumeration provenance. Both are properties of PLANNING, decided\n" +
	"  before a single tuple moves, so a plan-only run answers clause 6 exactly\n" +
	"  as a full audit would — in seconds instead of a power run, and without\n" +
	"  timing content that a co-resident workload could contaminate.\n"

type flags struct {
	host      string
	port      int
	db        string
	user      string
	pass      string
	label     string
	queries   string
	outDir    string
	timeout   time.Duration
	tripwire  float64
	finalMax  float64
	failOn    bool
	keepPlan  bool
	analyze   bool
	serial    bool
	fromPlans string
	refPlans  string
	refPort   int
	refDB     string
	refUser   string
	refPass   string
	slack     float64
	floor     float64
	enumTrace string
	planOnly  bool
}

func main() {
	f := parseFlags(os.Args[1:])

	var reports []estimateaudit.QueryReport
	var plans string
	if f.fromPlans != "" {
		// Offline replay of a committed .plans.txt. The estimator is not
		// consulted, so this re-derives an old run's audit exactly — which
		// is how a NEW instrument (the §4 parity column) gets applied to
		// evidence captured before it existed, without a 13-minute rerun.
		reports = replayPlans(f.fromPlans)
	} else {
		reports, plans = capture(f, f.port, f.db, f.user, f.pass, "goopg")
	}

	th := estimateaudit.Thresholds{
		Tripwire:          f.tripwire,
		FinalJoin:         f.tripwire,
		FinalJoinPerQuery: estimateaudit.DefaultThresholds().FinalJoinPerQuery,
	}
	th.FinalJoinPerQuery["Q9"] = estimateaudit.FinalBar{Max: f.finalMax, Why: th.FinalJoinPerQuery["Q9"].Why}
	var out string
	if f.planOnly {
		out = planOnlyBanner
	} else {
		out = estimateaudit.Render(reports, th)
	}

	// §4's parity column, when a PG 18.3 reference is available.
	ref, refPlans, haveRef := referenceReports(f)
	if haveRef {
		if !f.planOnly {
			bar := estimateaudit.ParityBar{Slack: f.slack, Floor: f.floor}
			rows := estimateaudit.Parity(reports, ref)
			out += "\n" + estimateaudit.RenderParity(reports, ref, rows, bar)
		}
		// §4's spine diff — the pairing question clause 6 is stated on, which
		// the parity column above cannot answer: it keys on the relset a node
		// builds, and two engines that partition the same relset differently
		// both report it MATCHED (09 §3.10, M0127-P5.9-l).
		spine := estimateaudit.SpineDiff(reports, ref)
		out += "\n" + estimateaudit.RenderSpine(reports, ref, spine)
		// …and the SEARCH side of the same clause, when the arm was run with
		// `GOOPG_PGSHAPED_DP_TRACE=1` and its server log is handed over. The
		// spine diff can only NAME the bushy partitions PG chose and goopg did
		// not; whether the goopg search ever OFFERED them — cost divergence,
		// which §4 admits, versus a shape it cannot enumerate, which §4 fails —
		// is only in the search's own record (M0127-P5.9-l-ii).
		if f.enumTrace != "" {
			out += "\n" + renderEnum(f.enumTrace, spine)
		}
	}

	writeReport(f, out, plans, refPlans)
	fmt.Print(out)

	v := estimateaudit.Violations(reports, th)
	if f.failOn && len(v) > 0 {
		fmt.Fprintf(os.Stderr, "estimate-audit: %d joinrel(s) over threshold\n", len(v))
		os.Exit(1)
	}
}

// capture runs the audit against a live server. label is only for progress
// output — the same function drives goopg and the PG 18.3 reference, because
// the reference has to be measured the same way (same queries, same serial
// setting, ANALYZE first) or the comparison is between two protocols rather
// than two planners.
func capture(f *flags, port int, dbName, user, pass, engine string) ([]estimateaudit.QueryReport, string) {
	db := openDB(f, port, dbName, user, pass)
	defer db.Close()

	// The per-connection-stats warmup is a goopg property (see the package
	// comment); on upstream ANALYZE is global and persistent, so re-running
	// it per session is merely redundant, not wrong.
	s := &session{db: db, warmup: f.analyze, serial: f.serial, timeout: f.timeout, planOnly: f.planOnly}
	defer s.close()

	reports := make([]estimateaudit.QueryReport, 0, 22)
	var plans strings.Builder
	for _, qn := range selectQueries(f.queries) {
		name, sqlText := queryFor(qn)
		if sqlText == "" {
			reports = append(reports, estimateaudit.AuditError(name, "query SQL not found in tpch.Queries()"))
			continue
		}
		fmt.Fprintf(os.Stderr, "[%s] %s ... ", engine, name)
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
		fmt.Fprintf(&plans, "=== %s\n%s\n\n", name, text)
	}
	if !f.keepPlan {
		return reports, ""
	}
	return reports, plans.String()
}

// referenceReports supplies the PG 18.3 side of the parity gate, either from a
// committed plans file (--reference) or by capturing it now (--ref-port). A
// freshly captured reference is written next to the report so the comparison
// stays re-derivable from committed evidence.
func referenceReports(f *flags) ([]estimateaudit.QueryReport, string, bool) {
	if f.refPlans != "" {
		return replayPlans(f.refPlans), "", true
	}
	if f.refPort == 0 {
		return nil, "", false
	}
	reports, plans := capture(f, f.refPort, f.refDB, f.refUser, f.refPass, "pg18.3")
	return reports, plans, true
}

// renderEnum harvests the join search's own enumeration record from an arm's
// server log and adjudicates clause 6's bushy partitions against it.
//
// An unreadable log is reported into the artifact rather than fatal: the audit
// this section hangs off is a 13-minute measurement, and losing it because a
// secondary channel's path was mistyped would be the expensive failure.
func renderEnum(path string, spine []estimateaudit.SpineRow) string {
	fh, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("=== JOIN-SEARCH ENUMERATION PROVENANCE (09 §4 clause 6, P5.9-l-ii)\n"+
			"  NOT HARVESTED: open %s: %v\n", path, err)
	}
	defer fh.Close()
	t := estimateaudit.ParseEnumTrace(fh)
	return estimateaudit.RenderEnum(t, t.Adjudicate(estimateaudit.EnumChecks(spine)))
}

func replayPlans(path string) []estimateaudit.QueryReport {
	b, err := os.ReadFile(path)
	if err != nil {
		fatal("read %s: %v", path, err)
	}
	r := estimateaudit.SplitPlansFile(string(b))
	if len(r) == 0 {
		fatal("no `=== <query>` blocks in %s — is it a .plans.txt artifact?", path)
	}
	return r
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
	fs.StringVar(&f.fromPlans, "from-plans", "", "replay a committed <label>.plans.txt instead of connecting to goopg")
	fs.StringVar(&f.refPlans, "reference", "", "PG 18.3 reference plans file for the 09 §4 parity gate")
	fs.IntVar(&f.refPort, "ref-port", 0, "capture the PG 18.3 reference from this port instead (65432 = TPC-H reference cluster; 0 = off)")
	fs.StringVar(&f.refDB, "ref-db", "tpch", "reference database name")
	fs.StringVar(&f.refUser, "ref-user", "tpch", "reference user")
	fs.StringVar(&f.refPass, "ref-password", "tpch", "reference password")
	fs.Float64Var(&f.slack, "parity-slack", estimateaudit.DefaultParitySlack, "09 §4 ratchet: how many times worse than PG a joinrel may be")
	fs.Float64Var(&f.floor, "parity-floor", estimateaudit.DefaultParityFloor, "09 §4 ratchet: goopg's own ratio must also exceed this")
	fs.StringVar(&f.enumTrace, "enum-trace", "", "goopg server log from a GOOPG_PGSHAPED_DP_TRACE=1 arm; adjudicates clause 6's bushy partitions against what the join search actually enumerated")
	fs.BoolVar(&f.planOnly, "plan-only", false, "EXPLAIN without ANALYZE: emit the clause-6 spine/enumeration channel only, and omit the §5 audit and §4 parity ratchet (they need actual rows)")
	fs.Parse(args)
	if f.planOnly {
		// A plan-only run has no actuals, so `Violations` is empty by
		// construction; leaving the tripwire armed would turn "nothing was
		// measured" into a green exit code.
		f.failOn = false
	}
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
	db       *sql.DB
	conn     *sql.Conn
	warmup   bool
	serial   bool
	timeout  time.Duration
	planOnly bool
}

// prefix is the EXPLAIN form this run uses. `warmup` stays on in either mode:
// the stats decide which plan is CHOSEN, so a plan-only run without them would
// measure the spine of the no-stats planner (see the package comment).
func (s *session) prefix() string {
	if s.planOnly {
		return "EXPLAIN "
	}
	return "EXPLAIN ANALYZE "
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
	rows, err := s.conn.QueryContext(ctx, s.prefix()+query)
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

func writeReport(f *flags, report, plans, refPlans string) {
	if err := os.MkdirAll(f.outDir, 0o755); err != nil {
		fatal("mkdir %s: %v", f.outDir, err)
	}
	write := func(suffix, content string) {
		if content == "" {
			return
		}
		path := filepath.Join(f.outDir, f.label+suffix)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			fatal("write %s: %v", path, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", path)
	}
	write(".txt", report)
	write(".plans.txt", plans)
	write(".pg.plans.txt", refPlans)
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
			// Both bounds must parse: with the errors dropped, `--queries 5-`
			// gave hi=0 and the 5..0 loop selected NOTHING, auditing zero
			// queries without a word of complaint. (review/260831-2 CM-5)
			lo, loErr := strconv.Atoi(strings.TrimSpace(part[:i]))
			hi, hiErr := strconv.Atoi(strings.TrimSpace(part[i+1:]))
			if loErr != nil || hiErr != nil {
				fatal("--queries: cannot parse range %q (want e.g. 3-9)", part)
			}
			if lo < 1 || hi < lo {
				fatal("--queries: invalid range %q (want 1 <= lo <= hi)", part)
			}
			for q := lo; q <= hi; q++ {
				seen[q] = true
			}
			continue
		}
		// A bare number likewise: `--queries -5` parsed to -5 and asked for a
		// nonexistent "Q-5", and a typo parsed to nothing at all.
		v, err := strconv.Atoi(part)
		if err != nil {
			fatal("--queries: cannot parse query number %q", part)
		}
		if v < 1 {
			fatal("--queries: query number %d is out of range (queries start at 1)", v)
		}
		seen[v] = true
	}
	out := make([]int, 0, len(seen))
	for q := range seen {
		out = append(out, q)
	}
	sort.Ints(out)
	return out
}

func openDB(f *flags, port int, dbName, user, pass string) *sql.DB {
	connStr := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=disable",
		f.host, port, dbName, user, pass)
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		fatal("sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		fatal("ping: %v (is the cluster up on %s:%d? bench/tpch/setup_goopg.sh / setup_pg.sh)", err, f.host, port)
	}
	return db
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
