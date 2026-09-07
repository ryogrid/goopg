package optimizer

// C-07 (P3-06), the SEAM half — what is pinned here:
//
//   - the validator TRUNCATES rather than translating, and each of its four
//     refusal reasons is a real refusal (upperorderedinput.go rule 1);
//   - the Node walk derives an ordering only in the input's OWN output
//     coordinates, and re-checks the schema at every step (rule 2);
//   - the arm that was unreachable before this cut — `upper.ordered.input` —
//     is reachable END TO END, from a real statement through `PlanWithSettings`,
//     and the redundant ORDER BY Sort is gone from the plan it returns.
//
// The last one is deliberately an end-to-end assertion and not a unit one. The
// shared `ppiCtx` fixture sets `baseLeaf = &SeqScan{Table: inner}` with a NIL
// schema, so the whole optimizer suite is blind to anything that reads a rel's
// leaf output — measured 2026-09-06, when the suite passed identically with and
// without the widening applied. A unit test here would have proved nothing.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func upperOrderedSchema() Schema {
	return Schema{
		{Name: "k", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1},
		{Name: "v", Type: catalog.Type{Name: "text"}, SourceTableIdx: 1},
		{Name: "w", Type: catalog.Type{Name: "numeric"}, SourceTableIdx: 2},
	}
}

// TestValidatedSearchPathkeysTruncatesRatherThanTranslating is rule 1. Every
// case below is a claim the PUBLISHED schema cannot confirm, and in each the
// answer is the confirmed PREFIX — never a repaired or re-coordinated key.
// A prefix is sound because rows ordered by (a, b) are ordered by (a).
func TestValidatedSearchPathkeysTruncatesRatherThanTranslating(t *testing.T) {
	out := upperOrderedSchema()
	good := PathKey{Expr: &ColumnRef{Index: 0, Name: "k", SourceTableIdx: 1}, SortAsc: true}

	cases := []struct {
		name string
		bad  PathKey
	}{
		{"not a ColumnRef", PathKey{Expr: &BinaryOp{}, SortAsc: true}},
		{"index out of range", PathKey{Expr: &ColumnRef{Index: 9, Name: "v"}, SortAsc: true}},
		{"name disagrees with the coordinate", PathKey{Expr: &ColumnRef{Index: 1, Name: "k"}, SortAsc: true}},
		{"recorded identities disagree", PathKey{Expr: &ColumnRef{Index: 1, Name: "v", SourceTableIdx: 4}, SortAsc: true}},
		{"unnamed column", PathKey{Expr: &ColumnRef{Index: 1, Name: ""}, SortAsc: true}},
	}
	for _, c := range cases {
		got := validatedSearchPathkeys([]PathKey{good, c.bad}, out)
		if len(got) != 1 || got[0].Expr.(*ColumnRef).Name != "k" {
			t.Fatalf("%s: got %d keys, want the confirmed prefix (1) only", c.name, len(got))
		}
		// The same key FIRST must reduce the whole list to nothing, not to
		// "skip it and keep the next" — the build_index_pathkeys rule: an
		// index on (a, b, c) whose b is unusable delivers (a), not (a, c).
		if got := validatedSearchPathkeys([]PathKey{c.bad, good}, out); len(got) != 0 {
			t.Fatalf("%s leading: got %d keys, want none", c.name, len(got))
		}
	}

	// A schema that records no identity accepts a key that does: zero means
	// "not recorded", which is not evidence of disagreement.
	noIdent := Schema{{Name: "k"}}
	if got := validatedSearchPathkeys([]PathKey{good}, noIdent); len(got) != 1 {
		t.Fatalf("an unrecorded SourceTableIdx must not be read as a mismatch, got %d keys", len(got))
	}
	if got := validatedSearchPathkeys(nil, out); got != nil {
		t.Fatal("no claim must stay no claim")
	}
	if got := validatedSearchPathkeys([]PathKey{good}, nil); got != nil {
		t.Fatal("no published schema must confirm nothing")
	}
}

// TestInputNodePathkeysStaysInTheInputsOwnCoordinates is rule 2: the walk
// descends only through nodes that publish their child's schema unchanged, and
// it re-checks the schema at every step instead of trusting the node kind.
func TestInputNodePathkeysStaysInTheInputsOwnCoordinates(t *testing.T) {
	keys := upperOrderedKeys()
	srt := &Sort{Child: upperOrderedInput(10), Keys: keys}

	if got := inputNodePathkeys(srt); len(got) != len(keys) {
		t.Fatalf("a Sort at the top delivers its own keys: got %d, want %d", len(got), len(keys))
	}
	// Through the two order-preserving, schema-preserving wrappers.
	if got := inputNodePathkeys(&Limit{Child: &Filter{Child: srt}}); len(got) != len(keys) {
		t.Fatalf("the walk must descend through Filter and Limit: got %d keys", len(got))
	}
	// A Project is where coordinates are re-assigned. Even one that happens to
	// publish an agreeing schema is refused: admitting it would put the walk
	// back in the translation business the file header rules out.
	proj := &Project{Child: srt, schema: srt.Output()}
	if got := inputNodePathkeys(proj); got != nil {
		t.Fatalf("a Project must stop the walk, got %d keys", len(got))
	}
	if got := inputNodePathkeys(upperOrderedInput(10)); got != nil {
		t.Fatalf("a node that claims no order must claim none, got %d keys", len(got))
	}
	if got := inputNodePathkeys(nil); got != nil {
		t.Fatal("nil input must claim nothing")
	}
}

// TestInputNodePathkeysPrefersTheSearchedRootsValidatedClaim: a searched root
// can BE a `*Sort` or a `*Filter`, and when it is, the SEARCH's claim is the
// stronger statement — it already survived validation against this exact
// published schema — so it is read first.
func TestInputNodePathkeysPrefersTheSearchedRootsValidatedClaim(t *testing.T) {
	in := upperOrderedInput(10)
	proj := &Project{Child: in, schema: upperOrderedSchema()}
	markSearchedTree(proj)
	claim := []PathKey{{Expr: &ColumnRef{Index: 0, Name: "k", SourceTableIdx: 1}, SortAsc: true}}
	proj.setSearchPathkeys(claim)

	if got := inputNodePathkeys(proj); len(got) != 1 || got[0].Expr.(*ColumnRef).Name != "k" {
		t.Fatalf("a searched root's carried ordering must be read, got %v", got)
	}
	// ...and it is still read through the schema-preserving wrappers.
	if got := inputNodePathkeys(&Filter{Child: proj}); len(got) != 1 {
		t.Fatalf("the walk must reach a searched root below a Filter, got %d keys", len(got))
	}
	// An untagged node carrying the same field claims nothing: the tag is what
	// says the search produced this tree.
	untagged := &Project{Child: in, schema: upperOrderedSchema()}
	untagged.setSearchPathkeys(claim)
	if got := inputNodePathkeys(untagged); got != nil {
		t.Fatalf("an untagged root must claim nothing, got %v", got)
	}
}

// TestStampSearchPathkeysCannotFailThePlan: every degenerate input leaves the
// root exactly as it was. The seam's ordering claim is an optimisation; losing
// it costs a Sort, and no shape of it may cost a plan.
func TestStampSearchPathkeysCannotFailThePlan(t *testing.T) {
	proj := &Project{Child: upperOrderedInput(10), schema: upperOrderedSchema()}
	markSearchedTree(proj)
	if got := stampSearchPathkeys(proj, nil); got != Node(proj) {
		t.Fatal("a nil path must hand the root back")
	}
	if got := stampSearchPathkeys(proj, &Path{}); got != Node(proj) || len(searchedTreePathkeys(proj)) != 0 {
		t.Fatal("a path with no pathkeys must stamp nothing")
	}
	if got := stampSearchPathkeys(nil, &Path{Pathkeys: []PathKey{{Expr: &ColumnRef{Name: "k"}}}}); got != nil {
		t.Fatal("a nil root must stay nil")
	}
	// A node that cannot carry the tag is declined, not panicked on: unlike
	// `markSearchedTree`, a missing ordering claim is sound.
	lim := &Limit{Child: proj}
	if got := stampSearchPathkeys(lim, &Path{Pathkeys: []PathKey{{Expr: &ColumnRef{Name: "k"}}}}); got != Node(lim) {
		t.Fatal("a non-carrier root must be handed back untouched")
	}
}

// TestOrderedInputArmFiresEndToEndAndRemovesTheSort is the whole point of the
// cut, asserted where the fixture trap cannot hide it: through
// `PlanWithSettings`, on a real statement, against a real catalog.
//
// Before this cut the trace for BOTH queries read `upper.ordered.sort` and the
// plan carried a top-level `*Sort`, because `newPrebuiltPath` left `Pathkeys`
// nil and `pathkeysContainedIn(nil, keys)` is false for any non-empty request.
//
// The two queries differ ONLY in which column the join merges on, which is what
// makes this a test and not a demonstration: the first asks for the ordering the
// merge join delivers (Sort removed), the second asks for a DIFFERENT ordering
// on the same shape (Sort kept). A change that carried the seam's pathkeys
// carelessly — or claimed an ordering the tree does not deliver — fails the
// second case, and that failure is a wrong-answer bug caught as a plan-shape
// assertion.
func TestOrderedInputArmFiresEndToEndAndRemovesTheSort(t *testing.T) {
	cat, orders, lineitem := ppiCatalog(t)
	ppiSetStats(orders, 150_000,
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 15_000},
		catalog.ColumnStats{NDistinct: 5})
	ppiSetStats(lineitem, 600_000, catalog.ColumnStats{NDistinct: 150_000})

	// The merge join is the shape whose output ordering PG's
	// `build_join_pathkeys` records and goopg's `tryMergeJoinPath` already
	// carried on the path; the other join methods are turned off so the
	// contest is about the ORDERING, not about which join wins on cost.
	ps := DefaultPlannerSettings()
	ps.EnableHashJoin = false
	ps.EnableNestLoop = false
	ps.EnableNestLoopIndex = false

	for _, c := range []struct {
		name       string
		sql        string
		wantSort   bool
		wantMarker string
	}{
		{
			name:       "ORDER BY the merge key: the input already delivers it",
			sql:        "select o_orderkey, l_orderkey from orders, lineitem where o_orderkey = l_orderkey order by o_orderkey",
			wantSort:   false,
			wantMarker: upperOrderedInputProducer,
		},
		{
			name:       "ORDER BY a different column: the Sort must stay",
			sql:        "select o_orderkey, l_orderkey from orders, lineitem where o_custkey = l_orderkey order by o_orderkey",
			wantSort:   true,
			wantMarker: upperOrderedSortProducer,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			var node Node
			lines := captureTrace(t, func() {
				n, err := PlanWithSettings(parseOne(t, c.sql), cat, ps)
				if err != nil {
					t.Fatal(err)
				}
				node = n
			})
			seen := false
			for _, l := range lines {
				if strings.Contains(l, "producer="+c.wantMarker+" ") {
					seen = true
				}
			}
			if !seen {
				t.Fatalf("no %s line on the trace: %q", c.wantMarker, lines)
			}
			if got := planHasTopLevelSort(node); got != c.wantSort {
				t.Fatalf("top-level Sort present = %v, want %v", got, c.wantSort)
			}
		})
	}
}

// planHasTopLevelSort walks the schema-preserving wrappers above the search
// root — the same chain `inputNodePathkeys` walks — looking for the ORDER BY
// Sort. It stops at the searched root, so a Sort the SEARCH chose (a merge
// input) is not mistaken for the ORDER BY's.
func planHasTopLevelSort(n Node) bool {
	for c := n; c != nil; {
		if isSearchedTree(c) {
			return false
		}
		switch x := c.(type) {
		case *Sort:
			return true
		case *Project:
			c = x.Child
		case *Filter:
			c = x.Child
		case *Limit:
			c = x.Child
		default:
			return false
		}
	}
	return false
}

// TestBuildJoinPathkeysDropsFullAndRightOrderings is the wrong-answer guard
// C-07's seam half made necessary. A FULL or RIGHT merge join injects its
// unmatched inner rows wherever the merge reaches them, NOT at the position the
// outer ordering would put them, so `build_join_pathkeys` (pathkeys.c:1295)
// returns NIL for both. goopg kept the outer's keys for every join type, which
// was harmless while a merge path's pathkeys were read only inside the search
// (a wrong cost at worst) and is a WRONG ANSWER now that the ordering escapes
// to the ORDERED upper rel and can delete the ORDER BY Sort — rows out of order
// with a correct row count, which no row-count gate can see.
func TestBuildJoinPathkeysDropsFullAndRightOrderings(t *testing.T) {
	keys := []PathKey{{Expr: &ColumnRef{Index: 0, Name: "k"}, SortAsc: true}}
	for _, jt := range []parser.JoinType{parser.JoinFull, parser.JoinRight} {
		if got := buildJoinPathkeys(jt, keys); got != nil {
			t.Fatalf("jointype %v must claim no ordering, got %v", jt, got)
		}
	}
	for _, jt := range []parser.JoinType{parser.JoinInner, parser.JoinLeft, parser.JoinCross, parser.JoinSemi, parser.JoinAnti} {
		if got := buildJoinPathkeys(jt, keys); len(got) != 1 {
			t.Fatalf("jointype %v streams the outer in order and must keep its keys, got %v", jt, got)
		}
	}
}
