package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// P1-14b patternsel slice: LIKE/ILIKE restriction selectivity.

// matchLikePattern is exercised directly: it must agree with SQL LIKE on
// the metacharacter set (backtracking %, single _, backslash escapes).
func TestMatchLikePattern(t *testing.T) {
	for _, tc := range []struct {
		value, pattern string
		icase          bool
		want           bool
	}{
		{"green widget", "%green%", false, true},
		{"red thing", "%green%", false, false},
		{"GREEN WIDGET", "%green%", false, false},
		{"GREEN WIDGET", "%green%", true, true},
		{"abc", "abc", false, true},
		{"abcd", "abc", false, false},
		{"abc", "abc%", false, true},
		{"abc", "a_c", false, true},
		{"ac", "a_c", false, false},
		{"ac", "a%c", false, true},
		{"", "%", false, true},
		{"", "", false, true},
		{"a", "", false, false},
		{"100%", "100\\%", false, true},
		{"100x", "100\\%", false, false},
		{"a%b", `a\%b`, false, true},
		{`a\b`, `a\\b`, false, true},
		{"axbyc", "a%b%c", false, true},
		{"axc", "a%b%c", false, false},
		{"ab", "a%%b", false, true},
	} {
		if got := matchLikePattern(tc.value, tc.pattern, tc.icase); got != tc.want {
			t.Errorf("matchLikePattern(%q, %q, icase=%t) = %t, want %t",
				tc.value, tc.pattern, tc.icase, got, tc.want)
		}
	}
}

// patternStatsTable builds a text column with an MCV list and a 102-bound
// histogram (the >= 100 regime TPC-H Q9's `part` side sits in, where PG
// trusts histogram+MCV outright with no heuristic blend), the two
// populations patternsel_common merges.
func patternStatsTable() *catalog.Table {
	bounds := make([]string, 0, 102)
	bounds = append(bounds, "a")
	for i := 0; i < 30; i++ {
		bounds = append(bounds, "green item")
	}
	for i := 0; i < 70; i++ {
		bounds = append(bounds, "red item")
	}
	bounds = append(bounds, "z")
	return makeStatsTable(&catalog.TableStats{
		RowCount: 1000, Analyzed: true,
		Columns: []catalog.ColumnStats{
			{NDistinct: 50, NullFrac: 0,
				MCV: []catalog.MCVEntry{
					{Value: "green widget", Frequency: 0.1},
					{Value: "red thing", Frequency: 0.2},
				},
				Histogram: bounds},
		},
	}, []catalog.Column{{Name: "p_name", Type: catalog.Type{Name: "text"}, Ordinal: 0}})
}

func likeClause(op parser.OpCode, pattern string) Expr {
	return &BinaryOp{
		Op:    op,
		Left:  &ColumnRef{Index: 0, Name: "p_name", Type: catalog.Type{Name: "text"}},
		Right: &StringConst{Value: pattern},
	}
}

// TestPatternSelectivityMCVPlusHistogram: '%green%' over the fixture above.
// MCV mass 0.1 matches; 102-bound histogram (>= 100: no heuristic blend),
// 100 interior bounds with 30 matching -> 0.3 of the (1 - 0 - 0.3) = 0.7
// non-MCV mass = 0.21; total 0.31.
func TestPatternSelectivityMCVPlusHistogram(t *testing.T) {
	tbl := patternStatsTable()
	got := clauseSelectivity(likeClause(parser.OpLike, "%green%"), &SeqScan{Table: tbl})
	if math.Abs(got-0.31) > 1e-9 {
		t.Errorf("patternsel('%%green%%') = %v, want 0.31", got)
	}
	// ILIKE folds case before matching: same answer here.
	got = clauseSelectivity(likeClause(parser.OpILike, "%GREEN%"), &SeqScan{Table: tbl})
	if math.Abs(got-0.31) > 1e-9 {
		t.Errorf("patternsel ILIKE('%%GREEN%%') = %v, want 0.31", got)
	}
	// NOT LIKE negates less the (zero) null fraction.
	got = clauseSelectivity(likeClause(parser.OpNotLike, "%green%"), &SeqScan{Table: tbl})
	if math.Abs(got-0.69) > 1e-9 {
		t.Errorf("patternsel NOT LIKE('%%green%%') = %v, want 0.69", got)
	}
}

// TestPatternSelectivityExactDelegates: a wildcard-free pattern estimates
// as equality (MCV hit -> frequency).
func TestPatternSelectivityExactDelegates(t *testing.T) {
	tbl := patternStatsTable()
	got := clauseSelectivity(likeClause(parser.OpLike, "green widget"), &SeqScan{Table: tbl})
	if math.Abs(got-0.1) > 1e-9 {
		t.Errorf("patternsel exact = %v, want MCV frequency 0.1", got)
	}
}

// TestPatternSelectivityDefaults: no statistics -> DEFAULT_MATCH_SEL, and
// the WithSource twin reports it unreliable; a NULL pattern is strict.
func TestPatternSelectivityDefaults(t *testing.T) {
	bare := makeStatsTable(nil, []catalog.Column{{Name: "p_name", Type: catalog.Type{Name: "text"}, Ordinal: 0}})
	got := clauseSelectivity(likeClause(parser.OpLike, "%green%"), &SeqScan{Table: bare})
	if got != defaultMatchSelectivity {
		t.Errorf("no-stats patternsel = %v, want default %v", got, defaultMatchSelectivity)
	}
	est := clauseSelectivityWithSource(likeClause(parser.OpLike, "%green%"), &SeqScan{Table: bare})
	if est.reliable {
		t.Errorf("no-stats patternsel marked reliable: %+v", est)
	}
	tbl := patternStatsTable()
	est = clauseSelectivityWithSource(likeClause(parser.OpLike, "%green%"), &SeqScan{Table: tbl})
	if !est.reliable || math.Abs(est.value-0.31) > 1e-9 {
		t.Errorf("stats patternsel = %+v, want {0.31 true}", est)
	}
	nullClause := &BinaryOp{
		Op:    parser.OpLike,
		Left:  &ColumnRef{Index: 0, Name: "p_name", Type: catalog.Type{Name: "text"}},
		Right: &NullConst{},
	}
	if got := clauseSelectivity(nullClause, &SeqScan{Table: tbl}); got != 0.0 {
		t.Errorf("NULL pattern = %v, want 0.0 (strict)", got)
	}
}

// TestPatternSelectivitySmallHistogramBlend exercises the <100-bound blend
// path (heuristic + histogram). It pins the deterministic result, not a
// PG-oracle value: the text-scalar limitation in the prefix half is
// documented in patternsel.go.
func TestPatternSelectivitySmallHistogramBlend(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000, Analyzed: true,
		Columns: []catalog.ColumnStats{
			{NDistinct: 20, NullFrac: 0,
				Histogram: []string{"a", "m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9", "x", "z"}},
		},
	}, []catalog.Column{{Name: "p_name", Type: catalog.Type{Name: "text"}, Ordinal: 0}})
	got := clauseSelectivity(likeClause(parser.OpLike, "%m%"), &SeqScan{Table: tbl})
	if !(got > 0 && got < 1) {
		t.Fatalf("small-hist patternsel = %v, want a proper fraction", got)
	}
	again := clauseSelectivity(likeClause(parser.OpLike, "%m%"), &SeqScan{Table: tbl})
	if got != again {
		t.Errorf("patternsel not deterministic: %v vs %v", got, again)
	}
}
