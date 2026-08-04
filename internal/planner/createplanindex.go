package planner

// M0127-P5.5-c — `create_indexscan_plan`: the first real `createPlan` arm, and
// the leaf a searched index path must be rebuilt FROM.
//
// PG oracle: `create_indexscan_plan` (createplan.c:3006) and its callers
// `create_scan_plan` / `create_plan_recurse` (createplan.c:337, :558),
// `fix_indexqual_references` (createplan.c:5121), `is_redundant_with_indexclauses`
// (createplan.c:3075), and `IndexPath` (pathnodes.h:1840-1860). Design:
// leftdeep-joins 03 §3, §10; 04 §1.1.
//
// P5.5-a and P5.5-b landed the carrier — WHICH index, in WHICH direction, with
// WHICH clauses pushed into the probe. This slice is the consumer: the arm that
// turns a `PathIndexScan` back into the `*IndexScan` node goopg's executor runs.
// It is the point at which the search stops being bookkeeping and starts being
// a plan, and it is where the one hole the carrier opened has to close — a path
// that names an index no node can be built from is a plan the DP can CHOOSE and
// the builder cannot HONOUR.
//
// The difficulty is not the index; it is everything else the leaf carries.
//
// PG's `create_indexscan_plan` builds its `IndexScan` from `IndexPath` plus the
// `RelOptInfo` behind it, and that RelOptInfo knows the range-table entry: the
// relation, the alias, the target list, and the `baserestrictinfo` quals that
// become the scan's `qpqual` (minus the ones the probe already enforces —
// `is_redundant_with_indexclauses`). goopg's `RelOptInfo` is a search-only
// object: a relset, a row count, a width, and pathlists. It knows nothing about
// the relation it stands for.
//
// The honest answer at this seam is not to teach `RelOptInfo` the range table
// but to record WHAT THE SEARCH WAS HANDED. `buildInitialRels` already receives
// one leaf `Node` per FROM item — a node the pre-search pipeline built, with the
// access method already chosen, the alias already resolved, the output schema
// already fixed, and this relation's local quals already attached. That node is
// the search boundary's half of 03 §10's coordinate map: it is what a base
// relid MEANS. `RelOptInfo.baseLeaf` carries it, and this arm re-emits from it.
//
// Consequences worth stating, because each is a bug that would otherwise be
// silent:
//
//   - **The schema is the leaf's, not a fresh one.** Every `*ColumnRef` in
//     every clause of the search was resolved against the pipeline's coordinates.
//     Synthesising a target list here would renumber the columns underneath the
//     quals that reference them.
//   - **The alias must survive.** Later passes disambiguate a self-joined
//     relation by alias (`FROM nation n1, nation n2`); dropping it makes `n1.col`
//     bind to whichever relation sits at the matching slot base, which returns a
//     neighbouring table's rows rather than failing (the M0062-0002 finding, and
//     the reason `nl_index_join.go:643` carries the same field).
//   - **The local quals must survive.** goopg's `*IndexScan` has no qual field —
//     PG's `qpqual` has no analogue on the node — so a base relation's
//     restriction clauses live in a `*Filter` ABOVE the scan. Re-emitting the
//     leaf therefore means re-emitting that `Filter` too, which is why
//     `indexScanLeafFor` returns a rewrapper rather than just the scan.
//
// The producers and this consumer share `indexScanLeafFor` for the eligibility
// test, which is rule #2 in the form it takes here: a leaf that cannot be
// rebuilt as an index scan must not have an index path COSTED over it either,
// or the DP prices a plan the builder then refuses. One predicate, two callers,
// no drift.
//
// Still inert: `GOOPG_PGSHAPED_DP` is OFF and nothing calls the search from
// `planSelect`, so no plan and no row can move. `createplanindex_test.go` is
// where the invariants are falsifiable.

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
)

// indexScanLeafRewrap rebuilds the wrappers that sat above a base relation's
// scan node, over a replacement scan. It is the identity for a bare leaf.
type indexScanLeafRewrap func(*IndexScan) Node

// scanIdentity is the leaf scan's copied-forward state. `*SeqScan` and
// `*IndexScan` both yield one (`scanIdentityOf`); nothing else does, and that
// is the eligibility test. It exists as a struct
// rather than as six return values so that adding a field to `*SeqScan` that
// must survive the rewrite is a compile-visible edit here, not an omission
// noticed at run time.
type scanIdentity struct {
	pos                   int
	table                 *catalog.Table
	alias                 string
	schema                Schema
	smallDim              bool
	privilegeCheckRole    string
	privilegeCheckRoleSet bool
}

// indexScanLeafFor resolves the base scan inside a search leaf and returns the
// rewrapper that reproduces the leaf's structure over a replacement scan.
//
// The recognised shapes are exactly the ones the pre-search pipeline produces
// for a base-table FROM item: a bare `*SeqScan` or `*IndexScan`, optionally
// under a chain of `*Filter` wrappers (`attachRelationLocalFilters` adds one,
// M0077-0001). A leaf of any other shape — a subquery, a VALUES list, a
// function scan, an already-built join subtree — is NOT an error: it is a
// relation with no index path to build, and both callers treat the `false`
// return as "decline", never as a failure.
//
// The `*Filter` wrappers are reproduced as new nodes rather than mutated in
// place: the original leaf may still be referenced by the caller's own
// bookkeeping (`attachRelationLocalFilters` matches leaves by POINTER identity),
// so rewriting its child under it would change a node the search does not own.
// `LeafLocal` rides along because it is what tells the post-rewrite posMap
// passes that the predicate is in leaf-local coordinates (plan.go:1064-1071);
// a Filter that loses the flag has its ColumnRefs renumbered by a pass that
// assumes cumulative coordinates.
func indexScanLeafFor(leaf Node) (*scanIdentity, indexScanLeafRewrap, bool) {
	if leaf == nil {
		return nil, nil, false
	}
	// Peel the wrappers, remembering them in order so they can be rebuilt
	// outermost-last.
	var wrappers []*Filter
	n := leaf
	for {
		f, ok := n.(*Filter)
		if !ok {
			break
		}
		wrappers = append(wrappers, f)
		n = f.Child
		if n == nil {
			return nil, nil, false
		}
	}
	id := scanIdentityOf(n)
	if id == nil {
		return nil, nil, false
	}
	rewrap := func(is *IndexScan) Node {
		var out Node = is
		for i := len(wrappers) - 1; i >= 0; i-- {
			w := wrappers[i]
			out = &Filter{
				pos:       w.pos,
				Child:     out,
				Predicate: w.Predicate,
				LeafLocal: w.LeafLocal,
			}
		}
		return out
	}
	return id, rewrap, true
}

// scanIdentityOf reads the copied-forward state off a base scan node, or nil
// when the node is not one.
func scanIdentityOf(n Node) *scanIdentity {
	switch s := n.(type) {
	case *SeqScan:
		return &scanIdentity{
			pos:                   s.Pos(),
			table:                 s.Table,
			alias:                 s.Alias,
			schema:                s.Output(),
			smallDim:              s.SmallDim,
			privilegeCheckRole:    s.PrivilegeCheckRole,
			privilegeCheckRoleSet: s.PrivilegeCheckRoleSet,
		}
	case *IndexScan:
		// The pipeline had already chosen an index for this leaf. Replacing it
		// with the index the SEARCH chose is the point of the exercise: the
		// search costed both and the pipeline's choice was made without a
		// parameterisation to exploit.
		return &scanIdentity{
			pos:                   s.Pos(),
			table:                 s.Table,
			alias:                 s.Alias,
			schema:                s.Output(),
			smallDim:              s.SmallDim,
			privilegeCheckRole:    s.PrivilegeCheckRole,
			privilegeCheckRoleSet: s.PrivilegeCheckRoleSet,
		}
	default:
		return nil
	}
}

// createIndexScanPlan is `create_indexscan_plan` (createplan.c:3006) at goopg's
// fidelity: the `*IndexScan` node that reproduces the probe the search costed.
//
// Every precondition below is checked rather than assumed, and each check has a
// specific wrong answer behind it:
//
//   - a nil `IndexInfo` is a path that named no index (P5.5-a's whole subject);
//   - a non-forward direction is a scan goopg's `*IndexScan` cannot express —
//     it has no direction field, so a backward path would be built as a forward
//     one and silently return the rows in the wrong order (P5.5-a ledgered the
//     producer half: only `ForwardScanDirection` is ever generated);
//   - a clause list that is neither empty nor exactly one clause per index
//     column is a probe the executor cannot run: `IndexScan.Keys[i]` binds
//     `Index.Columns[i]` positionally and a short prefix is rejected outright
//     (plan.go:665-669);
//   - a clause list whose `indexCol` values are not 0,1,2,… is a list that lost
//     P5.5-b's index-column ORDER, which would bind the right values to the
//     wrong columns;
//   - a parameterised path (`RequiredOuter != 0`) with an EMPTY clause list is
//     the dangerous one: empty means "full index scan" (pathnodes.h:1817), so
//     building it would silently turn a costed point probe into a scan of the
//     whole relation — right answer, ruinous plan — and it would also produce a
//     node whose parameterisation nothing discharges.
//
// These are panics and not errors on purpose, and for the reason the file header
// of createplan.go already gives: reaching one means a PRODUCER built a path it
// had no business building, which is a bug in the phase that added it rather
// than a condition the planner can recover from.
func createIndexScanPlan(p *Path) Node {
	if p.Rel == nil {
		panic("createPlan: PathIndexScan with no RelOptInfo")
	}
	id, rewrap, ok := indexScanLeafFor(p.Rel.baseLeaf)
	if !ok {
		panic(fmt.Sprintf("createPlan: PathIndexScan over relset %#04x whose leaf is not a rebuildable base scan", uint16(p.Rel.Relids)))
	}
	if p.IndexInfo == nil {
		panic(fmt.Sprintf("createPlan: PathIndexScan over relset %#04x names no index", uint16(p.Rel.Relids)))
	}
	if p.IndexScanDir != ForwardScanDirection {
		panic(fmt.Sprintf("createPlan: PathIndexScan with %s; goopg's *IndexScan has no direction to set", p.IndexScanDir))
	}
	ncols := len(p.IndexInfo.Columns)
	switch {
	case len(p.IndexClauses) == 0:
		// "An empty indexclauses list implies a full index scan"
		// (pathnodes.h:1817) — the unparameterised ordered path
		// (pathindexordered.go). A parameterised one cannot mean that.
		if p.RequiredOuter != 0 {
			panic(fmt.Sprintf("createPlan: parameterised PathIndexScan on %s carries no index clauses", p.IndexInfo.Name))
		}
	case len(p.IndexClauses) != ncols:
		panic(fmt.Sprintf("createPlan: PathIndexScan on %s binds %d of %d index columns; a prefix probe is not a shape the executor emits",
			p.IndexInfo.Name, len(p.IndexClauses), ncols))
	}

	is := &IndexScan{
		pos:   id.pos,
		Table: id.table,
		// The alias, the schema and the small-dimension answer are the leaf's,
		// not re-derived — see the file header for what each one's loss costs.
		Alias:    id.alias,
		Index:    p.IndexInfo,
		schema:   id.schema,
		SmallDim: id.smallDim,
		// Carried although `mhj_input_rewrite`'s equivalent substitution does
		// not: the check role is what a view-owner scan is authorised AS
		// (M0122-0008), and a scan that loses it is checked against the wrong
		// role rather than failing to build.
		PrivilegeCheckRole:    id.privilegeCheckRole,
		PrivilegeCheckRoleSet: id.privilegeCheckRoleSet,
	}

	// `fix_indexqual_references` (createplan.c:5121): the probe's keys, one per
	// index column, in index-column order. P5.5-b already put the list in that
	// order structurally; the assertion here is that the order SURVIVED, since
	// a silently reordered list binds the right expressions to the wrong
	// columns and returns wrong rows rather than failing.
	keys := make([]Expr, 0, len(p.IndexClauses))
	for i, c := range p.IndexClauses {
		if c.indexCol != i {
			panic(fmt.Sprintf("createPlan: index clause %d of %s claims index column %d; the index-column order was lost",
				i, p.IndexInfo.Name, c.indexCol))
		}
		if c.key == nil {
			panic(fmt.Sprintf("createPlan: index clause %d of %s has no probe expression", i, p.IndexInfo.Name))
		}
		keys = append(keys, c.key)
	}
	// `Key` vs `Keys` is the executor's distinction, not a new one: a
	// single-column probe uses `Key` (every pre-existing single-column caller
	// does, nl_index_join.go:656-660) and a composite one uses `Keys`, which
	// encodes every column in declared order with no suffix padding. An empty
	// list sets neither, which is the executor's range-scan branch with both
	// bounds nil — a scan of the whole index in key order
	// (operators_index.go:415-430), exactly what "full index scan" means.
	switch len(keys) {
	case 0:
	case 1:
		is.Key = keys[0]
	default:
		is.Keys = keys
	}
	return rewrap(is)
}
