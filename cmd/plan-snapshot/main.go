// Command plan-snapshot captures and diffs the EXPLAIN
// output of TPC-H queries against a running goopg cluster.
// It exists so planner-side iterations (cost-model
// refinement, transitivity hooks, rebind tweaks) can be
// regression-tested via plan-equivalence in seconds
// instead of the full 21-q sweep cost (~25 min).
//
// Two subcommands:
//
//	plan-snapshot capture --label <name>
//	    runs EXPLAIN against each TPC-H query, normalises
//	    per-query output, writes plan_snapshots/<name>.txt.
//
//	plan-snapshot diff --label <name> [--mode structural|strict-text|semantic-cost]
//	    re-captures + compares with the named baseline,
//	    prints per-query MATCH/DIFFER, exits non-zero on
//	    any diff.
//
// Equality levels:
//
//	structural (default) — strips `(rows=N)` cost-estimate
//	    suffixes before comparison; ignores cost variance
//	    (typical fluctuation from M0076-0004 cost-model
//	    tweaks).
//	strict-text — byte-for-byte diff (high false-positive
//	    rate; opt-in only).
//	semantic-cost — structural + cost ±10 % tolerance per
//	    matched line.
//
// Decision tree (when plan-diff is sufficient vs full sweep):
//   - planner-only changes (selectivity, transitivity,
//     rebind, cost-model): plan-diff sufficient + targeted
//     sweep on diverging queries.
//   - executor / Datum / arena / catalog / wire-protocol
//     changes: 21-q sweep MANDATORY (M0075-0003 lesson).
//
// See docs/design/0076-0006-plan-snapshot-harness.md.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/goopg/goopg/internal/testutil/tpch"
)

const snapshotDir = "plan_snapshots"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "capture":
		runCapture(os.Args[2:])
	case "diff":
		runDiff(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: plan-snapshot capture|diff [flags]")
	fmt.Fprintln(os.Stderr, "  capture --label <name> [--queries 1,2,...]")
	fmt.Fprintln(os.Stderr, "  diff    --label <name> [--queries 1,2,...] [--mode structural|strict-text|semantic-cost]")
	os.Exit(2)
}

type captureFlags struct {
	host    string
	port    int
	db      string
	user    string
	pass    string
	label   string
	queries string
	timeout time.Duration
}

func parseCaptureFlags(args []string) *captureFlags {
	cf := &captureFlags{}
	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	fs.StringVar(&cf.host, "host", "127.0.0.1", "goopg host")
	fs.IntVar(&cf.port, "port", 65433, "goopg port")
	fs.StringVar(&cf.db, "db", "tpch", "database name")
	fs.StringVar(&cf.user, "user", "tpch", "user")
	fs.StringVar(&cf.pass, "password", "tpch", "password")
	fs.StringVar(&cf.label, "label", "", "baseline label (filename suffix)")
	fs.StringVar(&cf.queries, "queries", "", "comma-or-range list (e.g. 1,3,5-9). empty = all 22")
	fs.DurationVar(&cf.timeout, "timeout", 30*time.Second, "per-EXPLAIN timeout (planner+EXPLAIN render only; no execution)")
	fs.Parse(args)
	if cf.label == "" {
		fmt.Fprintln(os.Stderr, "--label is required")
		fs.Usage()
		os.Exit(2)
	}
	return cf
}

func runCapture(args []string) {
	cf := parseCaptureFlags(args)
	qs := selectQueries(cf.queries)
	db := openDB(cf)
	defer db.Close()

	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		fatal("mkdir %s: %v", snapshotDir, err)
	}
	path := filepath.Join(snapshotDir, cf.label+".txt")
	f, err := os.Create(path)
	if err != nil {
		fatal("create %s: %v", path, err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	queries := tpch.Queries()
	for _, qn := range qs {
		sql := queries[qn]
		if sql == "" {
			fmt.Fprintf(w, "=== Q%d\n[ERROR: query SQL not found in tpch.Queries()]\n", qn)
			continue
		}
		// Q15 is special — its first sub-statement is a CREATE VIEW;
		// EXPLAIN on that returns "EXPLAIN not applicable" in goopg.
		// Use Q15ViewBody / Q15MainSelect for plan capture instead.
		if qn == 15 {
			fmt.Fprintf(w, "=== Q15a-VIEWBODY\n")
			fmt.Fprintln(w, captureExplain(db, tpch.Q15ViewBody(), cf.timeout))
			fmt.Fprintf(w, "=== Q15b-MAIN\n")
			fmt.Fprintln(w, captureExplain(db, tpch.Q15MainSelect(), cf.timeout))
			continue
		}
		fmt.Fprintf(w, "=== Q%d\n", qn)
		fmt.Fprintln(w, captureExplain(db, sql, cf.timeout))
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d queries)\n", path, len(qs))
}

func captureExplain(db *sql.DB, query string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, "EXPLAIN "+query)
	if err != nil {
		return fmt.Sprintf("[ERROR: %v]", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Sprintf("[SCAN ERROR: %v]", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return fmt.Sprintf("[ROWS ERROR: %v]", err)
	}
	return strings.Join(lines, "\n")
}

type diffFlags struct {
	host    string
	port    int
	db      string
	user    string
	pass    string
	label   string
	queries string
	mode    string
	timeout time.Duration
}

func parseDiffFlags(args []string) *diffFlags {
	df := &diffFlags{}
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	fs.StringVar(&df.host, "host", "127.0.0.1", "goopg host")
	fs.IntVar(&df.port, "port", 65433, "goopg port")
	fs.StringVar(&df.db, "db", "tpch", "database name")
	fs.StringVar(&df.user, "user", "tpch", "user")
	fs.StringVar(&df.pass, "password", "tpch", "password")
	fs.StringVar(&df.label, "label", "", "baseline label to compare against")
	fs.StringVar(&df.queries, "queries", "", "comma-or-range list (e.g. 1,3,5-9). empty = all in baseline")
	fs.StringVar(&df.mode, "mode", "structural", "structural | strict-text | semantic-cost")
	fs.DurationVar(&df.timeout, "timeout", 30*time.Second, "per-EXPLAIN timeout")
	fs.Parse(args)
	if df.label == "" {
		fmt.Fprintln(os.Stderr, "--label is required")
		fs.Usage()
		os.Exit(2)
	}
	switch df.mode {
	case "structural", "strict-text", "semantic-cost":
	default:
		fmt.Fprintf(os.Stderr, "unknown --mode %q\n", df.mode)
		os.Exit(2)
	}
	return df
}

func runDiff(args []string) {
	df := parseDiffFlags(args)
	baselinePath := filepath.Join(snapshotDir, df.label+".txt")
	baseline, err := readSnapshot(baselinePath)
	if err != nil {
		fatal("read baseline %s: %v", baselinePath, err)
	}
	cfQueries := df.queries
	cf := &captureFlags{
		host: df.host, port: df.port, db: df.db, user: df.user,
		pass: df.pass, queries: cfQueries, timeout: df.timeout,
	}
	db := openDB(cf)
	defer db.Close()

	queries := tpch.Queries()
	wantSet := map[string]bool{}
	for _, q := range selectQueries(cfQueries) {
		if q == 15 {
			wantSet["Q15a-VIEWBODY"] = true
			wantSet["Q15b-MAIN"] = true
		} else {
			wantSet["Q"+strconv.Itoa(q)] = true
		}
	}

	keys := make([]string, 0, len(baseline))
	for k := range baseline {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	diverging := 0
	for _, name := range keys {
		if cfQueries != "" && !wantSet[name] {
			continue
		}
		base := baseline[name]
		var sql string
		switch name {
		case "Q15a-VIEWBODY":
			sql = tpch.Q15ViewBody()
		case "Q15b-MAIN":
			sql = tpch.Q15MainSelect()
		default:
			qn, err := strconv.Atoi(strings.TrimPrefix(name, "Q"))
			if err != nil {
				fmt.Printf("%s: SKIP (unparseable label)\n", name)
				continue
			}
			sql = queries[qn]
		}
		if sql == "" {
			fmt.Printf("%s: SKIP (no SQL)\n", name)
			continue
		}
		current := captureExplain(db, sql, df.timeout)
		if planEqual(base, current, df.mode) {
			fmt.Printf("%s: MATCH\n", name)
			continue
		}
		fmt.Printf("%s: DIFFER (mode=%s)\n", name, df.mode)
		emitDiff(base, current)
		diverging++
	}
	if diverging > 0 {
		fmt.Fprintf(os.Stderr, "%d / %d queries diverged\n", diverging, len(keys))
		os.Exit(1)
	}
}

// readSnapshot parses the capture file format: "=== <name>\n<plan
// text...>\n" repeated. Returns map[name]planText.
func readSnapshot(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := make(map[string]string)
	var curName string
	var curBuf strings.Builder
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // allow long EXPLAIN lines
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, "=== ") {
			if curName != "" {
				out[curName] = strings.TrimSpace(curBuf.String())
			}
			curName = strings.TrimPrefix(line, "=== ")
			curBuf.Reset()
			continue
		}
		curBuf.WriteString(line)
		curBuf.WriteByte('\n')
	}
	if curName != "" {
		out[curName] = strings.TrimSpace(curBuf.String())
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// rowsRegexp captures the `(rows=N)` cost-estimate suffix.
// Used by structural mode to strip cost annotations before
// comparison and by semantic-cost mode to extract them for
// tolerance check.
var rowsRegexp = regexp.MustCompile(`\s*\(rows=(\d+)\)`)

// planEqual compares two EXPLAIN texts under the chosen mode.
// (M0076-0006.)
func planEqual(a, b, mode string) bool {
	switch mode {
	case "strict-text":
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	case "structural":
		return rowsRegexp.ReplaceAllString(a, "") == rowsRegexp.ReplaceAllString(b, "")
	case "semantic-cost":
		// Structural match plus per-line cost ±10% tolerance.
		la := splitLines(rowsRegexp.ReplaceAllString(a, ""))
		lb := splitLines(rowsRegexp.ReplaceAllString(b, ""))
		if len(la) != len(lb) {
			return false
		}
		for i := range la {
			if la[i] != lb[i] {
				return false
			}
		}
		// Compare cost annotations per matched line.
		ca := extractCosts(a)
		cb := extractCosts(b)
		if len(ca) != len(cb) {
			// If costs disappear / appear, treat as DIFFER.
			return false
		}
		for i := range ca {
			if ca[i] == 0 || cb[i] == 0 {
				continue // either side missing cost — cannot compare; skip
			}
			ratio := float64(ca[i]) / float64(cb[i])
			if ratio < 0.9 || ratio > 1.1 {
				return false
			}
		}
		return true
	}
	return false
}

func splitLines(s string) []string {
	out := strings.Split(strings.TrimSpace(s), "\n")
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return out
}

func extractCosts(s string) []int64 {
	var out []int64
	for _, m := range rowsRegexp.FindAllStringSubmatch(s, -1) {
		if v, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// emitDiff prints a unified-diff-ish view of base vs current
// for a single query. Compact: prefixes lines with `-` / `+`.
func emitDiff(base, current string) {
	la := splitLines(base)
	lb := splitLines(current)
	maxN := len(la)
	if len(lb) > maxN {
		maxN = len(lb)
	}
	fmt.Println("---")
	for i := 0; i < maxN; i++ {
		var av, bv string
		if i < len(la) {
			av = la[i]
		}
		if i < len(lb) {
			bv = lb[i]
		}
		if av == bv {
			fmt.Printf("  %s\n", av)
			continue
		}
		if av != "" {
			fmt.Printf("- %s\n", av)
		}
		if bv != "" {
			fmt.Printf("+ %s\n", bv)
		}
	}
	fmt.Println("---")
}

// selectQueries parses --queries flag (comma + range).
// Empty → all 22 (Q1..Q22).
func selectQueries(spec string) []int {
	if strings.TrimSpace(spec) == "" {
		out := make([]int, 22)
		for i := 0; i < 22; i++ {
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
		if i := strings.Index(part, "-"); i >= 0 {
			lo, _ := strconv.Atoi(strings.TrimSpace(part[:i]))
			hi, _ := strconv.Atoi(strings.TrimSpace(part[i+1:]))
			for q := lo; q <= hi; q++ {
				seen[q] = true
			}
			continue
		}
		v, err := strconv.Atoi(part)
		if err == nil {
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

func openDB(cf *captureFlags) *sql.DB {
	connStr := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=disable",
		cf.host, cf.port, cf.db, cf.user, cf.pass)
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		fatal("sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		fatal("ping: %v (is goopg-bench-bin running on %s:%d?)", err, cf.host, cf.port)
	}
	return db
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
