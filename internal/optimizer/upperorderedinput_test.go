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
	// A Project is where coordinates are re-assigned. One that publishes an
	// agreeing schema but states no targets is still refused: M0144-0011a-2
	// admits a Project only on POSITIVE evidence that it re-assigns nothing
	// (`projectIsPositionalIdentity`), never on the absence of evidence that
	// it does.
	proj := &Project{Child: srt, schema: srt.Output()}
	if got := inputNodePathkeys(proj); got != nil {
		t.Fatalf("a Project with no stated targets must stop the walk, got %d keys", len(got))
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
		noParallel bool
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
			// M0146-0027: with parallel workers available this case no
			// longer keeps its Sort — the `gather.merge.sort` arm of
			// `generateUsefulGatherPaths` (allpaths.c:3304-3328) sorts the
			// cheapest partial path per worker by the ORDER BY keys and the
			// Gather Merge delivers that order for free, exactly as PG does.
			// MaxParallelWorkersPerGather = 0 removes that arm so the case
			// still pins "an ORDER BY nothing delivers keeps its Sort".
			name:       "ORDER BY a different column: the Sort must stay",
			sql:        "select o_orderkey, l_orderkey from orders, lineitem where o_custkey = l_orderkey order by o_orderkey",
			noParallel: true,
			wantSort:   true,
			wantMarker: upperOrderedSortProducer,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := ps
			if c.noParallel {
				run.MaxParallelWorkersPerGather = 0
			}
			var node Node
			lines := captureTrace(t, func() {
				n, err := PlanWithSettings(parseOne(t, c.sql), cat, run)
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

// upperOrderedSortedAgg builds the shape M0144-0011a's arm exists for: a
// sorted `*Aggregate` over a `*Sort` whose leading keys ARE the group keys,
// publishing the group-prefix output layout (`[groups|aggs|…]`). This is the
// finished-Node form of the Q8 slice's winning `PathAgg`
// (`analysis/m0144/m0144-0011-q8-slice-trace.md`).
func upperOrderedSortedAgg() *Aggregate {
	group := &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}
	return &Aggregate{
		Child: &Sort{
			Child: upperOrderedInput(10),
			Keys:  []SortKey{{Expr: group, Desc: true, NullsFirst: true}},
		},
		GroupExprs: []Expr{group},
		Strategy:   AggStrategySorted,
		schema: Schema{
			{Name: "k", Type: catalog.Type{Name: "int4"}},
			{Name: "count", Type: catalog.Type{Name: "int8"}},
		},
	}
}

// TestInputNodePathkeysReadsASortedAggregatesEmissionOrder is M0144-0011a:
// PG's `create_agg_path` copies the subpath's pathkeys onto an AGG_SORTED
// `AggPath` (pathnode.c:3412-3416) and `create_ordered_paths` reads them
// (planner.c:5337, :5344-5348), so a GroupAggregate already emitting the
// ORDER BY order is taken as-is. goopg's seam publishes a Node, and the walk's
// `default: nil` used to swallow `*Aggregate` — re-seeding the ORDERED step
// with keys=0 and forcing the redundant Sort the Q8 trace measured.
//
// The claim must come back in the aggregate's OUTPUT coordinates (position 0,
// named by the group key) and must carry the child sort's direction, not a
// default one.
func TestInputNodePathkeysReadsASortedAggregatesEmissionOrder(t *testing.T) {
	agg := upperOrderedSortedAgg()

	got := inputNodePathkeys(agg)
	if len(got) != 1 {
		t.Fatalf("a sorted aggregate emits its group-key order: got %d keys, want 1", len(got))
	}
	cr, ok := got[0].Expr.(*ColumnRef)
	if !ok || cr.Index != 0 || cr.Name != "k" {
		t.Fatalf("the claim must be in OUTPUT coordinates, got %#v", got[0].Expr)
	}
	if got[0].SortAsc || !got[0].NullsFirst {
		t.Fatalf("the child sort's direction must be carried, got %+v", got[0])
	}
	// ...and still through the schema-preserving wrappers (a HAVING Filter is
	// exactly what sits over the aggregate in production).
	if got := inputNodePathkeys(&Limit{Child: &Filter{Child: agg}}); len(got) != 1 {
		t.Fatalf("the walk must reach the aggregate below Filter/Limit, got %d keys", len(got))
	}
}

// TestAggregateEmissionPathkeysDeclinesTheSameShapesAsItsPathTwin pins the
// sibling-agreement rule: `aggregateEmissionPathkeys` (Node) and
// `groupingEmissionPathkeys` (Path) must refuse the same shapes, or the
// ORDERED step would claim an order on one route that the other denies.
// Every case is a shape whose emission order is either absent or unnameable
// in output coordinates, and the answer is always "no claim" — never a
// repaired or partial one.
func TestAggregateEmissionPathkeysDeclinesTheSameShapesAsItsPathTwin(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Aggregate)
	}{
		{"hashed strategy emits no order", func(a *Aggregate) { a.Strategy = AggStrategyHashed }},
		{"a partial/finalize stage is not the simple mode", func(a *Aggregate) { a.Mode = AggModePartial }},
		{"grouping sets run the hashed executor arm", func(a *Aggregate) { a.GroupingSets = [][]int{{0}} }},
		{"no group keys is no order", func(a *Aggregate) { a.GroupExprs = nil }},
		{"the index-remapped permutation is not the written order", func(a *Aggregate) { a.GroupKeyOrder = []int{0} }},
		{"a child that states no order states none", func(a *Aggregate) { a.Child = upperOrderedInput(10) }},
		{"the child sorts on something else", func(a *Aggregate) {
			a.Child = &Sort{Child: upperOrderedInput(10), Keys: []SortKey{
				{Expr: &ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "text"}}},
			}}
		}},
		{"an expression group key the child does not sort on", func(a *Aggregate) {
			a.GroupExprs = []Expr{&BinaryOp{}}
		}},
		{"the output position does not carry the group key's name", func(a *Aggregate) {
			a.schema = Schema{
				{Name: "other", Type: catalog.Type{Name: "int4"}},
				{Name: "count", Type: catalog.Type{Name: "int8"}},
			}
		}},
	}
	for _, c := range cases {
		agg := upperOrderedSortedAgg()
		c.mutate(agg)
		if got := inputNodePathkeys(agg); got != nil {
			t.Fatalf("%s: got %d keys, want no claim", c.name, len(got))
		}
	}
	if got := aggregateEmissionPathkeys(nil); got != nil {
		t.Fatal("nil aggregate must claim nothing")
	}
}

// TestAggregateEmissionPathkeysReadsAGatherMergeChild: R56's no-split arm
// delivers group-key order through a `*GatherMerge`, not a `*Sort` — a merge
// emits the merged order of its sorted inputs, the same delivery contract
// `groupingEmissionPathkeys` already accepts for a `PathGatherMerge` child.
func TestAggregateEmissionPathkeysReadsAGatherMergeChild(t *testing.T) {
	agg := upperOrderedSortedAgg()
	srt := agg.Child.(*Sort)
	agg.Child = &GatherMerge{Child: srt, WorkersPlanned: 2, Keys: srt.Keys, schema: srt.Output()}

	if got := inputNodePathkeys(agg); len(got) != 1 {
		t.Fatalf("a Gather Merge child delivers its merged order: got %d keys, want 1", len(got))
	}
}

// TestAggregateEmissionPathkeysReadsASearchedRootChild is M0146-0027: the
// grouping stage's `upper.groupagg.searchcand` arm can elect a searched
// candidate as the sorted aggregate's input directly — no `*Sort` node —
// because that path already delivers the group ordering. The rebuilt searched
// root carries the WINNING searched path's validated pathkeys
// (`stampSearchPathkeys`), so `inputNodePathkeys` must read that stamped claim
// through the `*Aggregate` exactly as it reads a `*Sort` child's `Keys`.
func TestAggregateEmissionPathkeysReadsASearchedRootChild(t *testing.T) {
	agg := upperOrderedSortedAgg()
	group := agg.GroupExprs[0]
	root := &searchedPricedNode{pricedNode: pricedNode{sch: Schema{
		{Name: "k", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "text"}},
	}}}
	root.markFromJoinSearch()
	// A DESC/NULLS-FIRST claim — direction must survive the round trip.
	root.setSearchPathkeys([]PathKey{{Expr: group, SortAsc: false, NullsFirst: true}})
	agg.Child = root

	got := inputNodePathkeys(agg)
	if len(got) != 1 {
		t.Fatalf("a searched root carrying the group order delivers it to the sorted aggregate: got %d keys, want 1", len(got))
	}
	if got[0].SortAsc || !got[0].NullsFirst {
		t.Fatalf("the searched path's direction must be carried, got %+v", got[0])
	}

	// A searched root that does NOT satisfy the group order claims nothing.
	root.setSearchPathkeys([]PathKey{{Expr: &ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "text"}}}})
	if got := inputNodePathkeys(agg); got != nil {
		t.Fatalf("a searched root sorted on a non-group column is not a sorted input, got %d keys", len(got))
	}
	// Neither does an untagged node — the stamped claim is what the walk
	// trusts, not "this might have come from a search".
	root.fromJoinSearch = false
	if got := inputNodePathkeys(agg); got != nil {
		t.Fatalf("an unmarked node carries no searched ordering claim, got %d keys", len(got))
	}
}

// identityProject builds the shape M0144-0011a-2 admits: target `j` is the
// child's column `j` for every `j`, so the projection can only RENAME. `names`
// is the published output vocabulary, which is what makes the case
// interesting — it deliberately differs from the child's.
func identityProject(child Node, names ...string) *Project {
	childOut := child.Output()
	targets := make([]Expr, len(childOut))
	sch := make(Schema, len(childOut))
	for j, c := range childOut {
		targets[j] = &ColumnRef{Index: j, Name: c.Name, Type: c.Type}
		sch[j] = SchemaColumn{Name: names[j], Type: c.Type}
	}
	return &Project{Child: child, Targets: targets, schema: sch}
}

// TestInputNodePathkeysCrossesAPositionalIdentityProject is M0144-0011a-2, and
// it is the shape TPC-DS SF0.25 Q21 actually reaches the ORDERED step with:
// `Project{Aggregate}` whose only job is to rename the two aggregate outputs
// (`sum` -> `inv_before`/`inv_after`). Before this cut the walk stopped at the
// Project, the `*Aggregate` arm was never consulted, and a redundant Sort
// survived M0144-0011a.
//
// PG needs no special case (`create_projection_path` copies `subpath->pathkeys`
// for ANY projection — pathnode.c:2936-2937) because a PG pathkey names an
// EquivalenceClass; goopg's names a position, so only the identity case ports.
// The delivered claim must come back in the PROJECT's vocabulary, at the same
// index.
func TestInputNodePathkeysCrossesAPositionalIdentityProject(t *testing.T) {
	agg := upperOrderedSortedAgg()
	proj := identityProject(agg, "renamed_k", "n")

	got := inputNodePathkeys(proj)
	if len(got) != 1 {
		t.Fatalf("a pure rename must not destroy the claim below it: got %d keys, want 1", len(got))
	}
	cr, ok := got[0].Expr.(*ColumnRef)
	if !ok || cr.Index != 0 {
		t.Fatalf("the index must not move across a rename, got %#v", got[0].Expr)
	}
	if cr.Name != "renamed_k" {
		t.Fatalf("the claim must be spelled in the published vocabulary, got %q want %q", cr.Name, "renamed_k")
	}
	if got[0].SortAsc || !got[0].NullsFirst {
		t.Fatalf("direction must survive the rename, got %+v", got[0])
	}

	// The same relabelling applies to the *Sort top and to a searched root,
	// and the walk still reaches them below the order-preserving wrappers.
	srt := &Sort{Child: upperOrderedInput(10), Keys: upperOrderedKeys()}
	overSort := identityProject(srt, "kk", "vv", "ww")
	if got := inputNodePathkeys(&Filter{Child: overSort}); len(got) != 2 {
		t.Fatalf("a Sort's keys must survive a rename below a Filter, got %d keys", len(got))
	}
}

// TestProjectIsPositionalIdentityRefusesEverythingElse pins the arm's
// admission rule: a Project is crossed on POSITIVE evidence that it
// re-assigns no position, never because nothing proves it does. Each case is a
// Project that can move, drop, compute or re-index a column.
func TestProjectIsPositionalIdentityRefusesEverythingElse(t *testing.T) {
	agg := upperOrderedSortedAgg()

	// wantAt is the output position the child's group-key ordering must be
	// claimed at once the Project is crossed by convert_subquery_pathkeys
	// (M0146-0005ax: a permutation or a narrowing still carries the column
	// that holds the key), or -1 when no claim may survive.
	cases := []struct {
		name   string
		mutate func(*Project)
		wantAt int
	}{
		{"a permutation moves the key to its new position", func(p *Project) {
			p.Targets[0] = &ColumnRef{Index: 1, Name: "count"}
			p.Targets[1] = &ColumnRef{Index: 0, Name: "k"}
		}, 1},
		{"a computed column is a new value", func(p *Project) {
			p.Targets[0] = &BinaryOp{}
		}, -1},
		{"an isolated scope indexes its targets in another space", func(p *Project) {
			p.IsolatedScope = true
		}, -1},
		{"a narrowing projection keeps the key column", func(p *Project) {
			p.Targets = p.Targets[:1]
			p.schema = p.schema[:1]
		}, 0},
		{"no stated targets is no evidence", func(p *Project) { p.Targets = nil }, -1},
	}
	for _, c := range cases {
		p := identityProject(agg, "renamed_k", "n")
		c.mutate(p)
		if projectIsPositionalIdentity(p) {
			t.Fatalf("%s: admitted, want refused", c.name)
		}
		got := inputNodePathkeys(p)
		if c.wantAt < 0 {
			if got != nil {
				t.Fatalf("%s: the walk must stop, got %d keys", c.name, len(got))
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("%s: want one key at position %d, got %v", c.name, c.wantAt, got)
		}
		if cr, ok := got[0].Expr.(*ColumnRef); !ok || cr.Index != c.wantAt {
			t.Fatalf("%s: want the key at position %d, got %#v", c.name, c.wantAt, got[0].Expr)
		}
	}
}

// TestRelabelPathkeysToTruncatesRatherThanGuessing: the relabel obeys the same
// rule validatedSearchPathkeys does — an unusable key ends the list instead of
// being skipped, because an ordering by (a, b, c) whose b is unusable delivers
// (a), never (a, c).
func TestRelabelPathkeysToTruncatesRatherThanGuessing(t *testing.T) {
	out := Schema{{Name: "x"}, {Name: ""}}
	keys := []PathKey{
		{Expr: &ColumnRef{Index: 0, Name: "a"}, SortAsc: true},
		{Expr: &ColumnRef{Index: 1, Name: "b"}, SortAsc: true},
	}
	got := relabelPathkeysTo(keys, out)
	if len(got) != 1 || got[0].Expr.(*ColumnRef).Name != "x" {
		t.Fatalf("an unnameable column must truncate the claim, got %v", got)
	}
	if got := relabelPathkeysTo([]PathKey{{Expr: &ColumnRef{Index: 9, Name: "a"}}}, out); got != nil {
		t.Fatal("an out-of-range index must claim nothing")
	}
	if got := relabelPathkeysTo(nil, out); got != nil {
		t.Fatal("no claim must stay no claim")
	}
}

// TestAggregateEmissionPathkeysClaimsAnExpressionGroupKey is M0146-0005ae: a
// sorted aggregate over an EXPRESSION group key (TPC-H Q7/Q8's `EXTRACT(year
// FROM …)`) emits in that key's order exactly as over a column key, so the
// ORDER BY on it needs no second Sort — PG's pathkey is the key's
// EquivalenceClass either way. The claim names the output position.
func TestAggregateEmissionPathkeysClaimsAnExpressionGroupKey(t *testing.T) {
	agg := upperOrderedSortedAgg()
	expr := &BinaryOp{Op: parser.OpMod, Left: &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}, Right: &IntegerConst{Value: 3}}
	agg.GroupExprs = []Expr{expr}
	agg.Child.(*Sort).Keys = []SortKey{{Expr: expr}}
	agg.schema[0].Name = "?column?"
	got := inputNodePathkeys(agg)
	if len(got) != 1 {
		t.Fatalf("an expression group key sorted by its child is an emission order: got %d keys", len(got))
	}
	if cr, ok := got[0].Expr.(*ColumnRef); !ok || cr.Index != 0 || !got[0].SortAsc {
		t.Fatalf("claim must be output position 0 ascending, got %+v", got[0])
	}
}

// TestInputNodePathkeysCrossesASortedUnique is M0146-0133:
// create_upper_unique_path sets `pathkeys = subpath->pathkeys`, and
// distinctOnOp streams the first row of each run in arrival order, so a sorted
// Unique delivers its Sort's keys to the ORDER BY above (TPC-DS Q49's union
// dedup under an Incremental Sort). A hashed DistinctOn (a semijoin RHS's
// HashAggregate) emits in no order and claims none.
func TestInputNodePathkeysCrossesASortedUnique(t *testing.T) {
	keys := upperOrderedKeys()
	srt := &Sort{Child: upperOrderedInput(10), Keys: keys}
	uniq := &DistinctOn{Child: srt, KeyCols: []int{0}, schema: srt.Output()}
	if got := inputNodePathkeys(uniq); len(got) != len(keys) {
		t.Fatalf("a sorted Unique keeps its input's keys: got %d, want %d", len(got), len(keys))
	}
	hashed := &DistinctOn{Child: srt, KeyCols: []int{0}, schema: srt.Output(), Hashed: true}
	if got := inputNodePathkeys(hashed); got != nil {
		t.Fatalf("a hashed DistinctOn must claim no order, got %d keys", len(got))
	}
}
