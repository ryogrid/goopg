package parser

import (
	"strings"
	"testing"
)

// TestIsColNameKeyword verifies the three-way split: unreserved and col_name
// keywords return true; reserved keywords return false.
func TestIsColNameKeyword(t *testing.T) {
	cases := []struct {
		kw   Keyword
		want bool
		note string
	}{
		// ── unreserved — always OK as identifiers ──────────────────────
		{KwBegin, true, "begin"},
		{KwCommit, true, "commit"},
		{KwRollback, true, "rollback"},
		{KwIndex, true, "index"},
		{KwKey, true, "key"},
		{KwPartition, true, "partition"},
		{KwFunction, true, "function"},
		{KwLanguage, true, "language"},
		{KwShare, true, "share"},
		{KwCascade, true, "cascade"},
		{KwRestrict, true, "restrict"},
		{KwReplace, true, "replace"},
		{KwView, true, "view"},
		{KwSavepoint, true, "savepoint"},
		{KwRelease, true, "release"},
		{KwOver, true, "over"},
		{KwRecursive, true, "recursive"},
		{KwUpdate, true, "update"},
		{KwDelete, true, "delete"},
		{KwInsert, true, "insert"},
		// ── col_name — OK in most identifier positions ─────────────────
		{KwValues, true, "values"},
		{KwExists, true, "exists"},
		{KwBetween, true, "between"},
		{KwOut, true, "out"},
		{KwInout, true, "inout"},
		// ── type_func_name — OK as column/table names ──────────────────
		{KwVerbose, true, "verbose"},
		{KwJoin, true, "join"},
		{KwInner, true, "inner"},
		{KwLeft, true, "left"},
		{KwRight, true, "right"},
		{KwFull, true, "full"},
		{KwCross, true, "cross"},
		{KwNatural, true, "natural"},
		{KwOuter, true, "outer"},
		{KwIs, true, "is"},
		{KwLike, true, "like"},
		// ── reserved — must be quoted as identifiers ───────────────────
		{KwSelect, false, "select"},
		{KwFrom, false, "from"},
		{KwWhere, false, "where"},
		{KwAnd, false, "and"},
		{KwOr, false, "or"},
		{KwNot, false, "not"},
		{KwNull, false, "null"},
		{KwTrue, false, "true"},
		{KwFalse, false, "false"},
		{KwCreate, false, "create"},
		{KwTable, false, "table"},
		{KwUnique, false, "unique"},
		{KwPrimary, false, "primary"},
		{KwConstraint, false, "constraint"},
		{KwColumn, false, "column"},
		{KwForeign, false, "foreign"},
		{KwReferences, false, "references"},
		{KwCase, false, "case"},
		{KwWhen, false, "when"},
		{KwUnion, false, "union"},
		{KwIntersect, false, "intersect"},
		{KwExcept, false, "except"},
		{KwOrder, false, "order"},
		{KwGroup, false, "group"},
		{KwHaving, false, "having"},
		{KwIn, false, "in"},
		{KwWith, false, "with"},
		{KwReturning, false, "returning"},
		{KwFor, false, "for"},
		{KwEnd, false, "end"},
		{KwAnalyze, false, "analyze"},
		{KwAll, false, "all"},
		{KwDefault, false, "default"},
	}
	for _, tc := range cases {
		got := IsColNameKeyword(tc.kw)
		if got != tc.want {
			t.Errorf("IsColNameKeyword(%q) = %v, want %v", tc.note, got, tc.want)
		}
	}
}

// TestColNameKeywordsAsColumnNamesDoD is the DoD regression test: a fresh
// CREATE TABLE that uses col_name / unreserved keywords as column names must
// parse successfully. These are the specific words called out in the milestone
// spec (0051-0001) plus a sample of goopg's own unreserved keywords.
func TestColNameKeywordsAsColumnNamesDoD(t *testing.T) {
	// Words that are plain TokenIdent (not keywords) — always parseable.
	alwaysOK := []string{"name", "value", "type", "user", "data", "time",
		"month", "year", "hour", "minute", "second", "day", "zone", "version"}
	for _, col := range alwaysOK {
		sql := "CREATE TABLE t (" + col + " text)"
		_, err := Parse(sql)
		if err != nil {
			t.Errorf("col %q (non-keyword): Parse(%q) error: %v", col, sql, err)
		}
	}

	// Words that ARE keywords in goopg with category unreserved/col_name/
	// type_func — they must also be accepted as column names.
	keywordCols := []struct {
		col string
		kw  Keyword
	}{
		{"index", KwIndex},
		{"key", KwKey},
		{"partition", KwPartition},
		{"function", KwFunction},
		{"language", KwLanguage},
		{"share", KwShare},
		{"cascade", KwCascade},
		{"restrict", KwRestrict},
		{"view", KwView},
		{"savepoint", KwSavepoint},
		{"release", KwRelease},
		{"over", KwOver},
		{"recursive", KwRecursive},
		{"update", KwUpdate},
		{"delete", KwDelete},
		{"insert", KwInsert},
		{"values", KwValues},
		{"exists", KwExists},
		{"between", KwBetween},
		{"verbose", KwVerbose},
	}
	for _, tc := range keywordCols {
		sql := "CREATE TABLE t (" + tc.col + " text)"
		_, err := Parse(sql)
		if err != nil {
			t.Errorf("col %q (keyword %q, cat unreserved/col_name): Parse(%q) error: %v",
				tc.col, tc.kw, sql, err)
		}
	}
}

// TestReservedKeywordsRejectedAsColumnNames verifies that truly reserved
// keywords produce a parse error when used unquoted as column names.
func TestReservedKeywordsRejectedAsColumnNames(t *testing.T) {
	reserved := []string{
		"select", "from", "where", "and", "or", "not",
		"null", "true", "false", "create", "table",
		"union", "intersect", "except", "order", "group", "having",
		"case", "when", "with", "in", "for", "returning",
	}
	for _, col := range reserved {
		sql := "CREATE TABLE t (" + col + " text)"
		_, err := Parse(sql)
		if err == nil {
			t.Errorf("reserved keyword %q as column name: expected parse error, got none", col)
		}
	}
}

// TestQuotedReservedKeywordsStillWork verifies that double-quoting a reserved
// keyword still allows it as a column name (standard SQL behaviour).
func TestQuotedReservedKeywordsStillWork(t *testing.T) {
	for _, col := range []string{"select", "from", "where", "null", "true"} {
		sql := `CREATE TABLE t ("` + col + `" text)`
		_, err := Parse(sql)
		if err != nil {
			t.Errorf("quoted reserved %q: Parse(%q) error: %v", col, sql, err)
		}
	}
}

// TestAllKeywordsHaveCategories checks that every Keyword constant in the
// keywords map has an explicit entry in keywordCategory. This prevents a
// newly added keyword from silently defaulting to KwCatReserved and blocking
// identifier usage.
func TestAllKeywordsHaveCategories(t *testing.T) {
	// keywords (from token.go) lists all registered keywords.
	for _, kw := range keywords {
		if _, ok := keywordCategory[kw]; !ok {
			t.Errorf("keyword %q has no entry in keywordCategory; add it to keywords.go", kw)
		}
	}
}

// TestParseIdentRejectsReservedKeyword verifies that the parser returns an
// error when a reserved keyword appears where an identifier is expected, e.g.
// as a column alias.
func TestParseIdentRejectsReservedKeyword(t *testing.T) {
	cases := []string{
		"SELECT 1 AS select",   // reserved keyword as alias
		"SELECT 1 AS from",     // reserved keyword as alias
		"SELECT 1 AS where",    // reserved keyword as alias
	}
	for _, sql := range cases {
		_, err := Parse(sql)
		if err == nil {
			t.Errorf("Parse(%q): expected error for reserved alias, got none", sql)
		} else if !strings.Contains(err.Error(), "expected") {
			t.Logf("Parse(%q) error: %v", sql, err) // error form may vary
		}
	}
}
