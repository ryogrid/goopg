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
	re := regexp.MustCompile("(?s)`(SELECT [^`]{8,600})`|\"(SELECT [^\"]{8,600})\"|`(WITH [^`]{8,600})`|\"(WITH [^\"]{8,600})\"|`(VALUES [^`]{8,600})`|\"(VALUES [^\"]{8,600})\"")
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			sql := ""
			for _, g := range m[1:] {
				if g != "" {
					sql = g
					break
				}
			}
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

// Pinned 2026-08-26: first measurement 139/178 identical (360 harvested).
// Raise as grammar waves land; NEVER lower without a documented reason.
const legacyCorpusParityFloor = 135

func TestLegacyCorpusParity(t *testing.T) {
	queries := harvestSQLLiterals(t)
	if len(queries) < 50 {
		t.Fatalf("harvested only %d SQL literals — scanner regex rotted", len(queries))
	}
	matched, bothParsed := 0, 0
	var mismatched []string
	for _, q := range queries {
		l, n, err := diffParse(q)
		if err != nil {
			continue // one side rejects: fine, not part of parity
		}
		bothParsed++
		if l == n {
			matched++
		} else {
			mismatched = append(mismatched, fmt.Sprintf("%s\n  L=%s\n  N=%s", q, truncForLog(l), truncForLog(n)))
		}
	}
	t.Logf("legacy-test corpus: %d harvested, %d parsed by both, %d identical", len(queries), bothParsed, matched)
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
