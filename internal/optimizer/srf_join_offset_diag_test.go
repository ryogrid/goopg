package optimizer

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// projTargetIndices returns the resolved ColumnRef.Index of each target in the
// top-level Project of the planned statement, in target order. Non-ColumnRef
// targets contribute -1.
func projTargetIndices(t *testing.T, label, sql string, cat catalog.Catalog) []int {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("%s parse: %v", label, err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("%s plan: %v", label, err)
	}
	proj, ok := plan.(*Project)
	if !ok {
		t.Fatalf("%s: top node is %T, want *Project", label, plan)
	}
	idxs := make([]int, 0, len(proj.Targets))
	for _, e := range proj.Targets {
		if cr, ok := e.(*ColumnRef); ok {
			idxs = append(idxs, cr.Index)
		} else {
			idxs = append(idxs, -1)
		}
	}
	return idxs
}

func eqIdx(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSRFJoinRightProjectionOffset is the DU-002 slice 46 regression guard
// (M0110-0001). When a FROM-clause set-returning / table function is a join's
// LEFT input, the right-side scan's projection columns must be offset by the
// SRF's output width — exactly as the *Values case already was. Before the fix
// in buildBindingsPosMap, leaf SRF/table-function nodes did not advance `off`,
// so remapTopProjection shifted right-side projection columns DOWN by the SRF
// width: pg_dump's getTableAttrs
// `unnest('{oid}'::oid[]) src JOIN pg_attribute a` returned a scrambled row
// ("invalid column numbering in table"). The VALUES-left plan is the canonical
// reference; every SRF-left plan must produce identical projection indices.
func TestSRFJoinRightProjectionOffset(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "bar"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
		{Name: "y", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}

	const tmpl = `SELECT s.tbloid, b.x, b.y FROM %s s(tbloid) JOIN bar b ON s.tbloid=b.x`

	// VALUES-left is the known-correct reference shape.
	want := projTargetIndices(t, "VALUES-left", fmt.Sprintf(tmpl, "(VALUES (16403))"), cat)
	if !eqIdx(want, []int{0, 1, 2}) {
		t.Fatalf("VALUES-left reference indices = %v, want [0 1 2]", want)
	}

	srcs := map[string]string{
		"unnest-left": "unnest('{16403}'::int[])",
		"gs-left":     "generate_series(1,1)",
	}
	for label, src := range srcs {
		got := projTargetIndices(t, label, fmt.Sprintf(tmpl, src), cat)
		if !eqIdx(got, want) {
			t.Errorf("%s projection indices = %v, want %v (right-side columns not offset by SRF width)", label, got, want)
		}
	}
}
