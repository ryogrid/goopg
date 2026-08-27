package sqlparser

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// notYetPortedKeywords lists reserved / type_func_name keywords that no
// hand-written production consumes yet, with the reason each is still dark.
//
// WHY THIS GATE EXISTS: `reserved_keyword` and `type_func_name_keyword` are
// excluded from ColId/ColLabel, so a token in either list that no production
// consumes is UNREACHABLE — any routed statement using it is a guaranteed
// syntax error with no fallback. That is exactly how CURRENT_TIMESTAMP broke
// pgbench's TPC-B INSERT for the whole span between the P3.1 routing flip and
// 2026-08-27, and how table-level FOREIGN KEY was a syntax error under a
// routed CREATE TABLE.
//
// Removing an entry here is how a wave proves it closed a hole; the test fails
// on STALE entries too, so a token that becomes reachable must be deleted from
// this map in the same commit.
var notYetPortedKeywords = map[string]string{
	// reserved_keyword
	"ANALYSE":     "P6.5 VACUUM/ANALYZE",
	"ANALYZE":     "P6.5 VACUUM/ANALYZE",
	"ASYMMETRIC":  "BETWEEN ASYMMETRIC (only SYMMETRIC is ported)",
	"GRANT":       "P6.6 GRANT/REVOKE",
	"SYSTEM_USER": "DELIBERATE AND PERMANENT: parser.IsNoParenFuncName does not list it, so legacy treats it as a bare identifier. Adding it to sql_value_func_name would CREATE a parity diff, not fix one (see grammar/pg_grammar.y func_expr_common_subexpr).",

	// type_func_name_keyword
	"BINARY":        "COPY ... BINARY / BINARY cursors (P6.3, P6.5)",
	"COLLATION":     "CREATE COLLATION and COLLATION FOR (plain COLLATE is reachable)",
	"FREEZE":        "P6.5 VACUUM FREEZE",
	"OVERLAPS":      "row OVERLAPS row (:15162) — uq_cols's WITHOUT OVERLAPS does not count, see blindRules",
	"VERBOSE":       "P6.4 EXPLAIN VERBOSE / P6.5 VACUUM VERBOSE (func_name_keyword does not count — see blindRules)",
	"TABLESAMPLE":   "P1.2 FROM ... TABLESAMPLE",
}

// TestReservedKeywordsReachable asserts every reserved / type_func_name
// keyword is either consumed by a hand-written production or explicitly listed
// above.
//
// Scope caveat: this proves "referenced by SOME production", not "reachable
// from stmt". A token used only inside an otherwise-unreachable rule would
// still pass. That is deliberate — the cheap check covers the failure mode
// that has actually bitten twice.
func TestReservedKeywordsReachable(t *testing.T) {
	root := ".."
	kwPath := filepath.Join(root, "..", "grammar", "kwlists_gen.y")
	lists := parseKeywordLists(t, kwPath)

	// Guard the slicer itself: if the generator's layout drifts and the slices
	// come back empty, the whole test would vacuously pass.
	if got := len(lists["reserved_keyword"]); got < 70 {
		t.Fatalf("reserved_keyword: parsed only %d tokens — the kwlists slicer rotted", got)
	}
	if got := len(lists["type_func_name_keyword"]); got < 20 {
		t.Fatalf("type_func_name_keyword: parsed only %d tokens — the kwlists slicer rotted", got)
	}

	used := handWrittenGrammarTokens(t, filepath.Join(root, "..", "grammar"))

	var unreachable []string
	seen := map[string]bool{}
	for _, list := range []string{"reserved_keyword", "type_func_name_keyword"} {
		for _, tok := range lists[list] {
			seen[tok] = true
			if used[tok] {
				if _, allowed := notYetPortedKeywords[tok]; allowed {
					t.Errorf("STALE allowlist entry %q: it IS consumed by a production now — delete it from notYetPortedKeywords", tok)
				}
				continue
			}
			if _, allowed := notYetPortedKeywords[tok]; !allowed {
				unreachable = append(unreachable, tok)
			}
		}
	}
	sort.Strings(unreachable)
	for _, tok := range unreachable {
		t.Errorf("reserved keyword %q is UNREACHABLE: no hand-written production consumes it, and it is not in notYetPortedKeywords. "+
			"Any routed statement using it is an unconditional syntax error.", tok)
	}
	for tok := range notYetPortedKeywords {
		if !seen[tok] {
			t.Errorf("allowlist entry %q is not a reserved / type_func_name keyword any more — delete it", tok)
		}
	}
	t.Logf("reserved=%d type_func_name=%d, %d still unported",
		len(lists["reserved_keyword"]), len(lists["type_func_name_keyword"]), len(notYetPortedKeywords))
}

var kwListHead = regexp.MustCompile(`^([a-z_]+):`)
var kwTokenLine = regexp.MustCompile(`^[ \t|]*([A-Z_][A-Z0-9_]*)\s*$`)

// parseKeywordLists slices kwlists_gen.y into its per-nonterminal token lists.
func parseKeywordLists(t *testing.T, path string) map[string][]string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string][]string{}
	cur := ""
	for _, line := range strings.Split(string(src), "\n") {
		if m := kwListHead.FindStringSubmatch(line); m != nil {
			cur = m[1]
			continue
		}
		if cur == "" {
			continue
		}
		if m := kwTokenLine.FindStringSubmatch(line); m != nil {
			out[cur] = append(out[cur], m[1])
		}
	}
	return out
}

var blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
var upperToken = regexp.MustCompile(`\b[A-Z][A-Z0-9_]*\b`)

// blindRules are rule bodies whose token mentions do NOT count as evidence
// that a keyword's real grammar is ported. func_name_keyword enumerates the
// type_func_name_keyword class purely so those words can be used as FUNCTION
// NAMES (`left('ahoj', 2)`); it says nothing about whether TABLESAMPLE's
// clause, OVERLAPS' operator, BINARY's cast modifier or FREEZE's COPY option
// exist. Counting them would silently retire allowlist entries for features
// that are still unported — exactly the blinding this gate exists to prevent.
var blindRules = map[string]bool{
	"func_name_keyword": true,
	// uq_cols spells `UNIQUE (c WITHOUT OVERLAPS)` only to reproduce legacy's
	// misreading of the two words as column names; the row OVERLAPS operator
	// is still unported.
	"uq_cols": true,
}

var ruleHead = regexp.MustCompile(`^([a-z_][A-Za-z0-9_]*):`)

// handWrittenGrammarTokens collects every uppercase token mentioned in the
// HAND-WRITTEN grammar files. The generated kwlists/tokens files are excluded
// (they mention every keyword by construction), and so is every line starting
// with '%': the %token / %nonassoc precedence declarations would otherwise
// make all 469 keywords look referenced. Rule bodies named in blindRules are
// skipped for the reason given there.
func handWrittenGrammarTokens(t *testing.T, dir string) map[string]bool {
	t.Helper()
	used := map[string]bool{}
	for _, name := range []string{"header.y", "pg_grammar.y", "goopg_ext.y"} {
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := blockComment.ReplaceAllString(string(src), " ")
		blind := false
		for _, line := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(line)
			// A rule header at column 0 starts a new rule body.
			if m := ruleHead.FindStringSubmatch(line); m != nil {
				blind = blindRules[m[1]]
			}
			if blind || strings.HasPrefix(trimmed, "%") {
				continue
			}
			for _, tok := range upperToken.FindAllString(line, -1) {
				used[tok] = true
			}
		}
	}
	return used
}
