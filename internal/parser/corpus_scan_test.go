package parser

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// harvestSQLLiterals and truncForLog outlived the corpus-parity tests they
// were written for. Those tests compared the two parsers over every SQL string
// literal in this package's own test files; P7.2 deleted the legacy statement
// parsers, so the comparison has nothing to run against. The SCANNER is still
// useful — alter_table_test.go and create_function_test.go use it to prove
// ROUTING COVERAGE (a routed form that does not parse is a hard 42601, since
// routeBatch never falls back) — so it lives here now.
func harvestSQLLiterals(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "parser", "*_test.go"))
	if err != nil || len(files) == 0 {
		t.Fatal("no ../parser/*_test.go found — corpus scan is broken")
	}
	// All statement classes (user directive 2026-08-26): DML/DDL/utility are
	// harvested too. The both-parse filter below keeps them out of the
	// parity denominator until a grammar wave covers them — then the floor
	// rises without editing the scanner.
	kw := "(?:SELECT|WITH|VALUES|INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|TRUNCATE|BEGIN|COMMIT|ROLLBACK|ABORT|SET|SHOW|RESET|EXPLAIN|PREPARE|EXECUTE|DEALLOCATE|GRANT|REVOKE|VACUUM|ANALYZE|COPY|COMMENT|LOCK|LISTEN|NOTIFY|CHECKPOINT|DISCARD|REFRESH|CALL|DO)"
	reBT := regexp.MustCompile("(?s)`" + kw + " [^`]{8,600}`")
	reDQ := regexp.MustCompile("(?s)\"" + kw + " [^\"]{8,600}\"")
	strip := func(m string) string {
		m = strings.TrimSuffix(strings.TrimPrefix(m, "`"), "`")
		return strings.Trim(m, `"`)
	}
	seen := map[string]bool{}
	var out []string
	// "..." is prose, not SQL. The scanner reads whole test FILES, comments
	// included, so a doc comment that quotes a statement shape — e.g.
	// "pins `CREATE TABLE c PARTITION OF p ( ... )`" — otherwise enters the
	// corpus as a statement legacy happens to accept (its element list is lax)
	// and the grammar rightly does not.
	keep := func(one string) bool { return !strings.Contains(one, "...") }
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range reBT.FindAllString(string(src), -1) {
			sql := strip(m)
			one := strings.Join(strings.Fields(sql), " ")
			if keep(one) && !seen[one] {
				seen[one] = true
				out = append(out, one)
			}
		}
		for _, m := range reDQ.FindAllString(string(src), -1) {
			sql := strip(m)
			one := strings.Join(strings.Fields(sql), " ")
			if keep(one) && !seen[one] {
				seen[one] = true
				out = append(out, one)
			}
		}
	}
	sort.Strings(out)
	return out
}

func truncForLog(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

