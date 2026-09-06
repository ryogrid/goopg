package optimizer

// C-11 (P4-02) — the upper-rel registry, landed inert. These tests pin the
// three decisions `docs/design/planner-p4-upper-rels/DESIGN.md` §3.2 settled
// (per-scope identity, `Relids = 0` rendering, exclusion from the search's
// registries) and the one extra duty §4.3 found (size population), so a later
// producer cannot skip any of them silently.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestFetchUpperRelIsFindOrCreatePerKind: `fetch_upper_rel` returns the SAME
// rel for the same (kind, relids) and a different one for either key changed,
// with exactly the initial state relnode.c:1480-1494 gives it.
func TestFetchUpperRelIsFindOrCreatePerKind(t *testing.T) {
	u := newUpperRels()

	ordered := fetchUpperRel(u, UpperOrdered, 0, 0)
	if again := fetchUpperRel(u, UpperOrdered, 0, 0); again != ordered {
		t.Fatalf("second fetch of (ORDERED, -) returned a different rel")
	}
	if final := fetchUpperRel(u, UpperFinal, 0, 0); final == ordered {
		t.Fatalf("(FINAL, -) must not alias (ORDERED, -)")
	}
	if other := fetchUpperRel(u, UpperOrdered, relsetOf(0), 0); other == ordered {
		t.Fatalf("(ORDERED, {0}) must not alias (ORDERED, -)")
	}
	if n := len(u.rels[UpperOrdered]); n != 2 {
		t.Fatalf("ORDERED list holds %d rels, want 2 (one per relids)", n)
	}

	if ordered.Relids != 0 || ordered.ConsiderStartup || ordered.ConsiderParamStartup || ordered.ConsiderParallel {
		t.Fatalf("fresh upper rel has flags set: %+v", ordered)
	}
	if len(ordered.Pathlist) != 0 || ordered.CheapestTotal != nil || ordered.CheapestStartup != nil || len(ordered.CheapestParameterized) != 0 {
		t.Fatalf("fresh upper rel carries paths or cheapest slots")
	}
	if ordered.baseLeaf != nil {
		t.Fatalf("an upper rel must have no baseLeaf: that nil is what makes createSortPlan emit keys untranslated (DESIGN §4.1)")
	}
	// `consider_startup = (root->tuple_fraction > 0)` — the ONLY
	// tuple_fraction fact an upper rel carries.
	if limited := fetchUpperRel(newUpperRels(), UpperOrdered, 0, 0.1); !limited.ConsiderStartup {
		t.Fatalf("tuple_fraction > 0 must set ConsiderStartup")
	}
}

// TestUpperRelRegistryIsPerPlanningScope: two registries — two planning
// scopes — never share a rel, however equal the keys. That is the C-10d
// boundary expressed in data (DESIGN §3.2 decision 1).
func TestUpperRelRegistryIsPerPlanningScope(t *testing.T) {
	outer, inner := newUpperRels(), newUpperRels()
	if fetchUpperRel(outer, UpperOrdered, 0, 0) == fetchUpperRel(inner, UpperOrdered, 0, 0) {
		t.Fatalf("an upper rel leaked across planning scopes")
	}
}

// TestUpperFinalIsTheLastKind pins pathnodes.h:80's "UPPERREL_FINAL must be
// last enum entry; it's used to size arrays" — the array here is `upperRels`.
func TestUpperFinalIsTheLastKind(t *testing.T) {
	if len(upperRelKindNames) != int(UpperFinal)+1 {
		t.Fatalf("%d kind names for %d kinds", len(upperRelKindNames), int(UpperFinal)+1)
	}
	if got := UpperOrdered.String(); got != "ORDERED" {
		t.Fatalf("UpperOrdered renders as %q", got)
	}
}

// TestUpperRelPathTracesAsRelidsDash: an upper-rel path's DPPATH line carries
// `relids=-` with NO trace-format change (decision 2) — the reader that
// adjudicates "was the sort path offered" is a grep on that field.
func TestUpperRelPathTracesAsRelidsDash(t *testing.T) {
	rel := fetchUpperRel(newUpperRels(), UpperOrdered, 0, 0)
	if got := relSetBits(rel.Relids); got != "-" {
		t.Fatalf("relSetBits(upper.Relids) = %q, want \"-\"", got)
	}
	lines := captureTrace(t, func() {
		addPath(rel, &Path{Kind: PathSort, Rel: rel, Rows: 1, Cost: Cost{Total: 1}}, "upper.ordered.sort")
	})
	if len(lines) != 1 || !strings.Contains(lines[0], "producer=upper.ordered.sort relids=- ") {
		t.Fatalf("DPPATH line for an upper-rel path = %q, want producer=upper.ordered.sort relids=-", lines)
	}
}

// TestUpperRelsStayOutOfTheSearch (decision 3): fetching an upper rel beside a
// live search context files NOTHING in `relMap` or `joinrels`. `makeJoinRel`
// is a find-or-create over `relMap` and `finalRel` asserts one rel at the top
// level; an upper rel in either corrupts the search. The registry has no
// pointer to a searchCtx, so this pins the absence structurally.
func TestUpperRelsStayOutOfTheSearch(t *testing.T) {
	_, orders, _ := ppiCatalog(t)
	s := orderedCtx(t, orders, 1000)
	before := len(s.relMap)
	var levels []int
	for _, lv := range s.joinrels {
		levels = append(levels, len(lv))
	}

	u := newUpperRels()
	rel := fetchUpperRel(u, UpperOrdered, 0, s.tupleFraction)
	addPath(rel, &Path{Kind: PathSort, Rel: rel, Rows: 1, Cost: Cost{Total: 1}}, "upper.ordered.sort")
	setCheapest(rel)

	if s.findRel(0) != nil {
		t.Fatalf("relMap resolves relset 0 to an upper rel")
	}
	if len(s.relMap) != before {
		t.Fatalf("relMap grew from %d to %d", before, len(s.relMap))
	}
	for lv, n := range levels {
		if len(s.joinrels[lv]) != n {
			t.Fatalf("joinrels[%d] grew from %d to %d", lv, n, len(s.joinrels[lv]))
		}
	}
	if rel.CheapestTotal == nil || rel.CheapestTotal.Kind != PathSort {
		t.Fatalf("setCheapest on the upper rel did not pick the sort path")
	}
}

// TestSizeUpperRelFromNodePopulatesEveryCostInput (DESIGN §4.3): `NCols == 0`
// silently suppresses `costSortRun`'s disk arm, so a producer that sizes an
// upper rel must set all four fields, and each from the one source EXPLAIN
// already prints for the node.
func TestSizeUpperRelFromNodePopulatesEveryCostInput(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	text := catalog.Type{Name: "text"}
	vc20 := catalog.Type{Name: "varchar", Args: []int64{20}}
	child := &Values{
		Rows:   [][]Expr{{nil, nil, nil}, {nil, nil, nil}, {nil, nil, nil}},
		schema: Schema{{Name: "a", Type: int4}, {Name: "b", Type: text}, {Name: "c", Type: vc20}},
	}
	rel := fetchUpperRel(newUpperRels(), UpperOrdered, 0, 0)
	sizeUpperRelFromNode(rel, child)

	if rel.Rows != float64(EstimateRows(child)) || rel.Rows != 3 {
		t.Fatalf("Rows = %v, want the legacy estimate %d", rel.Rows, EstimateRows(child))
	}
	if rel.NCols != 3 {
		t.Fatalf("NCols = %d, want 3 — a zero here prices a spilling sort as in-memory", rel.NCols)
	}
	if want := nodeTupleWidth(child); rel.Width != want {
		t.Fatalf("Width = %d, want EXPLAIN's %d", rel.Width, want)
	}
	// The variable-width share: everything but the int4.
	if want := float64(typeWidth(text) + typeWidth(vc20)); rel.AvgVarBytes != want {
		t.Fatalf("AvgVarBytes = %v, want the varlena columns' width %v", rel.AvgVarBytes, want)
	}
	if relNCols(rel) != 3 || relAvgVarBytes(rel) != rel.AvgVarBytes {
		t.Fatalf("the cost-side readers do not see the populated fields")
	}
	// And through the cost function itself: the sized rel reaches the disk
	// arm where the unsized one cannot.
	cp := defaultCostParams()
	rows := sortRowsFillingBudget(cp.workMem, rel.NCols, 4.0)
	unsized := costSortRun(cp, rows, 0, 0, -1)
	sized := costSortRun(cp, rows, relNCols(rel), relAvgVarBytes(rel), -1)
	if !(sized.Startup > unsized.Startup) {
		t.Fatalf("a sized upper rel must be charged its spill: sized %v, unsized %v", sized.Startup, unsized.Startup)
	}
}

// TestTypeIsFixedWidthAgreesWithTypeWidth: the two switches are siblings
// (rule: sibling code paths must stay in sync). Every type the predicate calls
// fixed must price at a size `typeWidth` does not take from the varlena
// fallbacks, and every varlena family must be rejected.
func TestTypeIsFixedWidthAgreesWithTypeWidth(t *testing.T) {
	for _, name := range []string{"bool", "int2", "int4", "int8", "float4", "float8", "date", "timestamp", "timestamptz", "money", "oid", "name"} {
		ty := catalog.Type{Name: name}
		if !typeIsFixedWidth(ty) {
			t.Errorf("%s: want fixed-width", name)
		}
		if w := typeWidth(ty); w == varlenaDefaultWidth && name != "name" {
			t.Errorf("%s: typeWidth fell through to the varlena default", name)
		}
	}
	for _, ty := range []catalog.Type{{Name: "text"}, {Name: "varchar"}, {Name: "numeric"}, {Name: "bytea"}, {Name: "bpchar", Args: []int64{10}}, {Name: "int4", IsArray: true}} {
		if typeIsFixedWidth(ty) {
			t.Errorf("%+v: want variable-width", ty)
		}
	}
}
