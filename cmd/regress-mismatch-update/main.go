// Command regress-mismatch-update records per-case regress mismatch sizes in
// the inventory CSV (docs/test-port/postgres-oracle-target-inventory.csv).
//
// Usage:
//
//	go run ./cmd/regress-mismatch-update --counts <tsv> [--csv <inventory.csv>]
//
// The counts TSV has one line per case: `name<TAB>result<TAB>mismatch<TAB>expected`
// where result is PASS|FAIL|ERROR|TIMEOUT|SKIP; blank lines and `#` comments
// are ignored. mismatch = expected-side `-` lines of the unified diff between
// the normalised expected .out and goopg's output (excluding the `---`
// header); expected = line count of the normalised expected .out.
//
// The optional `mismatch_lines`/`expected_lines` columns are appended to the
// header (with an empty pair on every row) when absent. Only `regress-sql`
// rows whose status is `failed` are written, and only from PASS/FAIL lines;
// status is never changed. ERROR/TIMEOUT/SKIP lines keep the previous value.
// The CSV is rewritten with encoding/csv via a temp file + rename.
package main

import (
	"bufio"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	regressSuite   = "regress-sql"
	regressPrefix  = "postgres/src/test/regress/sql/"
	colMismatch    = "mismatch_lines"
	colExpected    = "expected_lines"
	exitMalformed  = 2
	exitOtherError = 1
)

// count is one parsed TSV line.
type count struct {
	name     string
	result   string
	mismatch int
	expected int
}

// errMalformed marks a counts-TSV parse failure (distinct exit code).
type errMalformed struct{ msg string }

func (e errMalformed) Error() string { return e.msg }

var validResults = map[string]bool{"PASS": true, "FAIL": true, "ERROR": true, "TIMEOUT": true, "SKIP": true}

// parseCounts reads the counts TSV. PASS/FAIL lines must carry non-negative
// integer mismatch/expected with mismatch <= expected; other results ignore
// the numeric fields.
func parseCounts(r io.Reader) ([]count, error) {
	var out []count
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			return nil, errMalformed{fmt.Sprintf("counts line %d: want 4 tab-separated fields, got %d: %q", lineNo, len(f), line)}
		}
		c := count{name: strings.TrimSpace(f[0]), result: strings.TrimSpace(f[1])}
		if c.name == "" {
			return nil, errMalformed{fmt.Sprintf("counts line %d: empty name", lineNo)}
		}
		if !validResults[c.result] {
			return nil, errMalformed{fmt.Sprintf("counts line %d: unknown result %q", lineNo, c.result)}
		}
		if c.result == "PASS" || c.result == "FAIL" {
			m, err1 := strconv.Atoi(strings.TrimSpace(f[2]))
			e, err2 := strconv.Atoi(strings.TrimSpace(f[3]))
			if err1 != nil || err2 != nil || m < 0 || e < 0 {
				return nil, errMalformed{fmt.Sprintf("counts line %d: mismatch/expected must be non-negative integers: %q", lineNo, line)}
			}
			if m > e {
				return nil, errMalformed{fmt.Sprintf("counts line %d: mismatch %d exceeds expected %d", lineNo, m, e)}
			}
			c.mismatch, c.expected = m, e
		}
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		return nil, errMalformed{fmt.Sprintf("read counts: %v", err)}
	}
	return out, nil
}

func fmtPair(m, e string) string {
	if m == "" && e == "" {
		return "-"
	}
	return m + "/" + e
}

// update applies counts to the CSV at csvPath, writing a report to w.
func update(csvPath string, counts []count, w io.Writer) error {
	f, err := os.Open(csvPath)
	if err != nil {
		return err
	}
	recs, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil {
		return fmt.Errorf("read csv %q: %w", csvPath, err)
	}
	if len(recs) == 0 {
		return fmt.Errorf("csv %q is empty", csvPath)
	}
	col := func(name string) int {
		for i, h := range recs[0] {
			if h == name {
				return i
			}
		}
		return -1
	}
	suiteIdx, pathIdx, statusIdx := col("suite_id"), col("item_path"), col("status")
	if suiteIdx < 0 || pathIdx < 0 || statusIdx < 0 {
		return fmt.Errorf("csv %q missing suite_id/item_path/status column", csvPath)
	}
	mIdx, eIdx := col(colMismatch), col(colExpected)
	if (mIdx < 0) != (eIdx < 0) {
		return fmt.Errorf("csv %q has only one of %s/%s", csvPath, colMismatch, colExpected)
	}
	if mIdx < 0 {
		n := len(recs[0])
		mIdx, eIdx = n, n+1
		for i := range recs {
			if i == 0 {
				recs[i] = append(recs[i], colMismatch, colExpected)
			} else {
				recs[i] = append(recs[i], "", "")
			}
		}
		fmt.Fprintf(w, "added columns %s,%s\n", colMismatch, colExpected)
	}

	byName := map[string]int{}
	for i, rec := range recs[1:] {
		if rec[suiteIdx] != regressSuite || !strings.HasPrefix(rec[pathIdx], regressPrefix) {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(rec[pathIdx], regressPrefix), ".sql")
		byName[name] = i + 1
	}

	changed := 0
	for _, c := range counts {
		ri, ok := byName[c.name]
		if !ok {
			fmt.Fprintf(w, "unknown case %s (no regress-sql row), skipped\n", c.name)
			continue
		}
		rec := recs[ri]
		old := fmtPair(rec[mIdx], rec[eIdx])
		if c.result != "PASS" && c.result != "FAIL" {
			fmt.Fprintf(w, "%s: %s, kept %s\n", c.name, c.result, old)
			continue
		}
		if rec[statusIdx] != "failed" {
			fmt.Fprintf(w, "%s-status row %s measured %d/%d (%s), not recorded\n", rec[statusIdx], c.name, c.mismatch, c.expected, c.result)
			continue
		}
		nm, ne := strconv.Itoa(c.mismatch), strconv.Itoa(c.expected)
		if rec[mIdx] != nm || rec[eIdx] != ne {
			fmt.Fprintf(w, "%s: %s→%s\n", c.name, old, fmtPair(nm, ne))
			rec[mIdx], rec[eIdx] = nm, ne
			changed++
		} else {
			fmt.Fprintf(w, "%s: unchanged %s\n", c.name, old)
		}
		if c.mismatch == 0 {
			fmt.Fprintf(w, "%s: pass-flip candidate (failed row measures 0 mismatched lines, result %s)\n", c.name, c.result)
		}
	}
	fmt.Fprintf(w, "%d row(s) updated\n", changed)
	return writeCSVAtomic(csvPath, recs)
}

// writeCSVAtomic writes recs to a temp file next to path and renames it over
// path, preserving the original file mode.
func writeCSVAtomic(path string, recs [][]string) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	cw := csv.NewWriter(tmp)
	if err := cw.WriteAll(recs); err != nil {
		tmp.Close()
		return fmt.Errorf("write csv: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func main() {
	countsPath := flag.String("counts", "", "counts TSV (name\\tresult\\tmismatch\\texpected)")
	csvPath := flag.String("csv", "docs/test-port/postgres-oracle-target-inventory.csv", "path to inventory CSV")
	flag.Parse()
	if *countsPath == "" {
		fmt.Fprintln(os.Stderr, "regress-mismatch-update: --counts is required")
		os.Exit(exitMalformed)
	}
	cf, err := os.Open(*countsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "regress-mismatch-update: %v\n", err)
		os.Exit(exitOtherError)
	}
	counts, err := parseCounts(cf)
	cf.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "regress-mismatch-update: %v\n", err)
		os.Exit(exitMalformed)
	}
	if err := update(*csvPath, counts, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "regress-mismatch-update: %v\n", err)
		os.Exit(exitOtherError)
	}
}
