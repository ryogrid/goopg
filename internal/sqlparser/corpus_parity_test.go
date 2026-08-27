package sqlparser

// TestLegacyCorpusParity is the fast grammar-regression gate (user directive
// 2026-08-26): TPC-H spotcheck runs are too slow to iterate on, so the
// parser package's own test SQL doubles as the new parser's parity corpus.
//
// The test harvests SELECT/WITH/VALUES-shaped SQL literals from
// internal/parser/*_test.go at runtime, parses each through BOTH parsers,
// and compares their canonical dumps via diffParse. Legacy-only syntax
// (DML, DDL, exotic types) is expected to fail on one side — those are
// counted and logged, not fatal. The floor only guards against REGRESSION:
// any drop below the pinned baseline means a grammar edit broke queries the
// yacc parser previously handled identically to legacy.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

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
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range reBT.FindAllString(string(src), -1) {
			sql := strip(m)
			one := strings.Join(strings.Fields(sql), " ")
			if !seen[one] {
				seen[one] = true
				out = append(out, one)
			}
		}
		for _, m := range reDQ.FindAllString(string(src), -1) {
			sql := strip(m)
			one := strings.Join(strings.Fields(sql), " ")
			if !seen[one] {
				seen[one] = true
				out = append(out, one)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Pinned 2026-08-26 (all-statement-class harvest): SELECT 127/166 +
// WITH 8/8 = 135 identical of 1485 harvested; DML/DDL both-parse counts
// rise automatically as waves land. Floor keeps 5-headroom; NEVER lower
// without a documented reason.
// 2026-08-26 P4.1 v0 (CREATE TABLE basic forms): 167 total.
// 2026-08-27: 223 total after the CREATE TABLE constraint repairs (column
// typmods, the PRIMARY KEY/UNIQUE/DEFAULT alternatives that had been
// misattached to fk_kw, column CHECK/REFERENCES plumbing), the NOT_LA
// follower-set fix (NOT EXISTS), and diffParse switching to ParseOneSrc so
// raw-source spans are actually compared. Floor keeps the usual 5 headroom.
// 2026-08-27 (ALTER narrow flip): 229 after the DROP/VALIDATE CONSTRAINT
// ConstraintName fixes and ADD COLUMN NotNullExplicit.
// 2026-08-27 (routed AST-carrier fixes): 247 after the SET value, ON CONFLICT
// arbiter Exprs, transaction-mode-list, zero-arg FuncCall.Args and
// fragEndPos/endMark quoted-tail repairs.
// 2026-08-27 (table-level constraints wired): 250.
// 2026-08-27 (table-level CHECK / FOREIGN KEY ported): 252.
// 2026-08-27 (CAST typmods, simple CASE, keyword aliases): 255.
// 2026-08-27 (FOR UPDATE locking clauses): 266.
// 2026-08-27 (constraint attrs, INCLUDE, index CONCURRENTLY, SET TRANSACTION): 314.
// 2026-08-27 (unqualified UPDATE/DELETE): 316.
// 2026-08-27 (transaction forms + SET value surface): 321.
// 2026-08-27 (empty col list, DEFAULT in VALUES, partial index, expr arbiter, typed literals): 328.
// 2026-08-27 (data-modifying CTEs, multi-word typed literals, misc): 336.
// 2026-08-27 (index column surface): 349.
// 2026-08-27 (ADD PRIMARY KEY USING INDEX, SET SESSION AUTHORIZATION): 350.
// 2026-08-27 (VARIADIC args, aggregate ORDER BY): 352.
// 2026-08-27 (EXCLUDE constraints): 361.
const legacyCorpusParityFloor = 436

func TestLegacyCorpusParity(t *testing.T) {
	queries := harvestSQLLiterals(t)
	if len(queries) < 50 {
		t.Fatalf("harvested only %d SQL literals — scanner regex rotted", len(queries))
	}
	matched := 0
	var mismatched []string
	byClass := map[string][3]int{} // class -> {harvested, bothParsed, identical}
	for _, q := range queries {
		class := strings.ToUpper(strings.Fields(q)[0])
		c := byClass[class]
		c[0]++
		l, n, err := diffParse(q)
		if err != nil {
			byClass[class] = c
			continue // one side rejects: fine, not part of parity
		}
		c[1]++
		if l == n {
			matched++
			c[2]++
		} else {
			mismatched = append(mismatched, fmt.Sprintf("%s\n  L=%s\n  N=%s", q, truncForLog(l), truncForLog(n)))
		}
		byClass[class] = c
	}
	classes := make([]string, 0, len(byClass))
	for k := range byClass {
		classes = append(classes, k)
	}
	sort.Strings(classes)
	for _, k := range classes {
		c := byClass[k]
		t.Logf("%-10s harvested=%3d both=%3d identical=%3d", k, c[0], c[1], c[2])
	}
	t.Logf("legacy-test corpus: %d harvested, %d identical", len(queries), matched)
	for _, m := range mismatched {
		t.Logf("AST MISMATCH: %s", m)
	}
	if matched < legacyCorpusParityFloor {
		t.Fatalf("parity %d < floor %d — a grammar edit regressed queries the yacc parser used to match legacy on", matched, legacyCorpusParityFloor)
	}
}

func truncForLog(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
