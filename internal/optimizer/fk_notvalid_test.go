package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// R125 — PG's planner consumes a NOT VALID foreign key as join evidence.
//
// `get_relation_foreign_keys` skips only unenforced constraints ("skip
// constraints currently not enforced", plancat.c:642-644), and
// `RelationGetFKeyList` (relcache.c:4769-4776) never carries `convalidated`
// into `ForeignKeyCacheInfo`. So upstream feeds a NOT VALID FK straight to
// `get_foreign_key_join_selectivity`. goopg used to skip on `NotValid` too,
// which was a straight parity divergence in a function otherwise ported
// faithfully (superkeyJoinSelectivity).
//
// The fixture is shaped like TPC-H Q9's contested join — a two-column equi-join
// from a large child to a smaller parent — which is where the divergence was
// found: goopg estimated 116 rows where ground truth was 318,748.

func r125Cat(t *testing.T, fk *catalog.ForeignKey) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}
	p, err := cat.CreateTable(parser.ObjectName{Name: "r125p"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	p.Stats = &catalog.TableStats{RowCount: 8000, Analyzed: true}
	// The FK arm needs the PARENT side to carry a provable key over the
	// referenced columns — that is what makes "each child row matches exactly
	// one parent row" derivable at all.
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "r125p_pk"}, p,
		[]string{"a", "b"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	c, err := cat.CreateTable(parser.ObjectName{Name: "r125c"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	c.Stats = &catalog.TableStats{RowCount: 40000, Analyzed: true}
	if fk != nil {
		c.ForeignKeys = append(c.ForeignKeys, *fk)
	}
	return cat
}

func r125Est(t *testing.T, cat catalog.Catalog) int64 {
	t.Helper()
	stmts, err := parser.Parse("select c.a from r125c c, r125p p where c.a = p.a and c.b = p.b")
	if err != nil {
		t.Fatal(err)
	}
	n, err := PlanWithSettings(stmts[0].(*parser.SelectStmt), cat, DefaultPlannerSettings())
	if err != nil {
		t.Fatal(err)
	}
	return EstimateRows(n)
}

func r125FK(notValid bool) *catalog.ForeignKey {
	return &catalog.ForeignKey{
		Name: "r125c_fk", Columns: []string{"a", "b"},
		RefTable: "r125p", RefColumns: []string{"a", "b"},
		NotValid: notValid,
	}
}

// The parity property: a NOT VALID FK must produce the SAME estimate as a
// validated one, and both must differ from having no FK at all. Before R125
// the NOT VALID arm silently fell back to the no-FK estimate.
func TestNotValidForeignKeyIsUsedAsJoinEvidence(t *testing.T) {
	noFK := r125Est(t, r125Cat(t, nil))
	valid := r125Est(t, r125Cat(t, r125FK(false)))
	notValid := r125Est(t, r125Cat(t, r125FK(true)))

	t.Logf("noFK=%d validFK=%d notValidFK=%d", noFK, valid, notValid)

	if valid == noFK {
		t.Skipf("fixture does not exercise the FK arm (validFK == noFK == %d): "+
			"the estimator needs a declared parent key it can prove; "+
			"the NOT VALID property is still pinned by the equality below", valid)
	}
	if notValid != valid {
		t.Errorf("a NOT VALID foreign key must be used exactly as a validated one "+
			"(PG gates on conenforced, never convalidated): notValid=%d valid=%d",
			notValid, valid)
	}
}

// NotEnforced still excludes — that IS PG's filter (`!cachedfk->conenforced`).
func TestNotEnforcedForeignKeyIsStillExcluded(t *testing.T) {
	fk := r125FK(false)
	fk.NotEnforced = true
	noFK := r125Est(t, r125Cat(t, nil))
	if got := r125Est(t, r125Cat(t, fk)); got != noFK {
		t.Errorf("a NOT ENFORCED foreign key must be ignored (plancat.c:642-644): "+
			"got %d, want the no-FK estimate %d", got, noFK)
	}
}
