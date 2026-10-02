package optimizer

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestQueryIsDistinctForFirstColumn pins M0145-0008ab's port of PG's
// query_is_distinct_for (analyzejoins.c:1117) and the estimator's isunique rule
// (selfuncs.c examine_simple_variable, RTE_SUBQUERY arm) for a one-column
// sub-select. The two answer different questions, and the rows where they
// disagree are the point: a HAVING-only aggregate or a UNION is distinct (free
// unique path) yet not isunique (no estimator override).
//
// Every "false" row is a place where claiming distinctness would let the
// unique-ified inner join return duplicate rows, so a regression to "true"
// there is a wrong-results bug, not a costing change.
func TestQueryIsDistinctForFirstColumn(t *testing.T) {
	cat := jtpCatalog(t)
	cases := []struct {
		name           string
		sql            string
		distinct, uniq bool
	}{
		{"group-by-target", `select j from jtp_i group by j having sum(v) > 3`, true, true},
		{"group-by-ordinal", `select j from jtp_i group by 1`, true, true},
		{"group-by-two-keys", `select j from jtp_i group by j, v`, false, false},
		{"group-by-other-col", `select max(j) from jtp_i group by v`, false, false},
		// PG resolves a bare GROUP BY name to an input column before an
		// output alias, so `v AS j … GROUP BY j` groups by the input j.
		{"group-by-alias-shadowed", `select v as j from jtp_i group by j, v`, false, false},
		{"distinct", `select distinct j from jtp_i`, true, true},
		{"distinct-on-target", `select distinct on (j) j from jtp_i`, true, true},
		{"distinct-on-other", `select distinct on (v) j from jtp_i`, false, false},
		{"having-only", `select max(j) from jtp_i having count(*) > 1`, true, false},
		{"aggregate-only", `select max(j) from jtp_i`, true, false},
		{"union", `select j from jtp_i union select j2 from jtp_i2`, true, false},
		{"union-all", `select j from jtp_i union all select j2 from jtp_i2`, false, false},
		{"union-then-union-all", `select j from jtp_i union select j2 from jtp_i2 union all select v from jtp_i`, false, false},
		{"plain", `select j from jtp_i`, false, false},
		{"limit", `select j from jtp_i order by j limit 3`, false, false},
		{"srf-grouped", `select generate_series(1, j) from jtp_i group by j`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ok := parseOne(t, tc.sql).(*parser.SelectStmt)
			if !ok {
				t.Fatalf("not a SELECT: %s", tc.sql)
			}
			if got := queryIsDistinctForFirstColumn(s, cat); got != tc.distinct {
				t.Errorf("queryIsDistinctForFirstColumn = %v, want %v", got, tc.distinct)
			}
			if got := subqueryOutputIsUnique(s); got != tc.uniq {
				t.Errorf("subqueryOutputIsUnique = %v, want %v", got, tc.uniq)
			}
		})
	}
}

// TestCreatePulledUniquePathNoop pins create_unique_path for a pulled RHS:
// one proven distinct unique-ifies to its own path (PG's UNIQUE_PATH_NOOP —
// same rows, same cost); a non-distinct base relation pays for the cheaper
// of sort+unique and hashed aggregation, hashing refused when the groups do
// not fit the hash memory (M0146-0005dk); a uniq expr that does not name the
// leaf's column declines.
func TestCreatePulledUniquePathNoop(t *testing.T) {
	leaf := &SeqScan{schema: Schema{{Name: "k"}}}
	mk := func() (*RelOptInfo, *Path) {
		rel := newRelOptInfo(RelSet(1)<<2, 100, 8)
		rel.baseLeaf = leaf
		rel.baseOffset = 5
		p := newPrebuiltPath(rel, leaf)
		p.Cost = Cost{Startup: 0, Total: 10}
		rel.CheapestTotal = p
		return rel, p
	}
	sj := &SpecialJoinInfo{
		Jointype: parser.JoinSemi, SemiCanBtree: true, SemiCanHash: true,
		SemiRhsExprs:        []Expr{&ColumnRef{Index: 5, Name: "k"}},
		SemiRhsProblemSpace: true,
	}
	cp := costParams{cpuOperatorCost: 0.0025, cpuTupleCost: 0.01, seqPageCost: 1, randomPageCost: 4}
	// No hash memory: the groups cannot fit, so only sort+unique is left.
	rel, p := mk()
	got := createUniquePath(rel, p, sj, cp)
	if got == nil || got.Kind != PathUnique || got.UniqueHashed || len(got.Children) != 1 || got.Children[0] == p {
		t.Fatalf("non-distinct base RHS without hash memory: got %+v, want a sort+unique over a Sort", got)
	}
	if fmt.Sprint(got.UniqueKeyCols) != "[0]" {
		t.Fatalf("unique key %v, want the leaf-local column [0]", got.UniqueKeyCols)
	}
	// Ample hash memory: hashing 100 rows beats sorting them.
	cp.workMem = 4 << 20
	rel, p = mk()
	if got := createUniquePath(rel, p, sj, cp); got == nil || !got.UniqueHashed || got.Children[0] != p {
		t.Fatalf("non-distinct base RHS with hash memory: got %+v, want the hashed unique over the subpath", got)
	}
	sj.SemiRhsDistinct = true
	rel, p = mk()
	if got := createUniquePath(rel, p, sj, cp); got != p {
		t.Fatalf("distinct pulled RHS: got %v, want the subpath itself (UNIQUE_PATH_NOOP)", got)
	}
	// A uniq expr that does not name the leaf's column is a desync, under
	// both arms.
	sj.SemiRhsExprs = []Expr{&ColumnRef{Index: 4, Name: "k"}}
	for _, distinct := range []bool{true, false} {
		sj.SemiRhsDistinct = distinct
		rel, p = mk()
		if got := createUniquePath(rel, p, sj, cp); got != nil {
			t.Fatalf("uniq expr outside the leaf still unique-ified (distinct=%v)", distinct)
		}
	}
}
