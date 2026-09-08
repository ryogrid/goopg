package optimizer

// onerelsearch.go — E-21 Cut 1 (design:
// docs/design/planner-e20-e21-parallel-path-search/DESIGN.md §4 Cut 1).
//
// PG builds every base relation's paths — the serial scan, the index scans,
// and `create_plain_partial_paths`' partial seq scan — in
// `set_base_rel_pathlists` (allpaths.c:221), which runs BEFORE
// `make_rel_from_joinlist` (allpaths.c:226) in the same function. The joinlist
// is consulted only to choose a JOIN ORDER; `make_rel_from_joinlist`'s
// `levels_needed == 1` return (allpaths.c:3400-3405) hands back a rel that is
// already fully pathed, because path generation already happened.
//
// goopg's seam inherited the return without the step before it: a statement
// with one FROM item is declined at `tryPGShapedJoinSearch`
// (joinsearchseam.go), so it never gets a `searchCtx`, never gets a
// `RelOptInfo`, and therefore never gets a `PartialPathlist` for
// `generateUsefulGatherPaths` to read. Its parallelism comes only from the
// `MaybeAddGather` post-pass, and its access method only from the legacy
// rule-based planner.
//
// NOTE on the citation this replaces: the in-tree comments that justified the
// decline cited `bms_membership(root->all_baserels) != BMS_SINGLETON` as PG's
// rule. That condition does not exist anywhere in PG 18.3's optimizer. PG's
// actual gather predicate is `!bms_equal(rel->relids, root->all_query_rels)`
// (allpaths.c:555-557) — "not the topmost scan/join rel" — and the topmost
// rel's gather is DEFERRED to `apply_scanjoin_target_to_paths`
// (planner.c:8036), not skipped. See DESIGN §1.2.
//
// WHY A FLAG. Admitting one-relation statements to the path search changes
// ACCESS-METHOD SELECTION for every single-table query on both corpora — seq
// vs index vs index-only decided by `add_path` instead of by the legacy rules
// — which is a far larger blast radius than the Gather this row is about.
// The mechanism and the flip are therefore separate commits with separate
// measurements. Default OFF until the corpora have been run.

import (
	"os"
	"strings"
)

// oneRelSearchEnabled is read once at process start, like every other
// plan-shaping knob in this package (joinsearch.go:52's rule), so a plan
// cannot change shape mid-statement.
var oneRelSearch = oneRelSearchFromEnv(os.Getenv("GOOPG_ONEREL_SEARCH"))

// oneRelSearchFromEnv resolves the knob. Fail-closed: anything unrecognised is
// OFF, so a typo cannot silently enable a plan shape whose measurement has not
// been run. Same convention as `gatherPathModeFromEnv`.
func oneRelSearchFromEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "1", "true", "yes":
		return true
	default:
		return false
	}
}

// oneRelSearchEnabled reports whether a statement with a single FROM item may
// enter the PG-shaped path search.
func oneRelSearchEnabled() bool { return oneRelSearch }

// oneRelSearchLabel spells the knob the way an operator would export it, so the
// flag-provenance label round-trips (flaglabels.go's contract).
func oneRelSearchLabel(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// setOneRelSearchForTest pins the knob for one test and returns the restore
// func. The knob is process-global by design, so a test that flips it must put
// it back — the established shape in this package.
func setOneRelSearchForTest(on bool) func() {
	prev := oneRelSearch
	oneRelSearch = on
	return func() { oneRelSearch = prev }
}

// minSearchRels is the smallest FROM-item count the seam will plan. It is the
// one place the E-21 decision is spelled, so both of the seam's size guards
// read the same number rather than each carrying its own literal.
//
// PG has no such floor at all: `set_base_rel_pathlists` runs for every base rel
// and `make_rel_from_joinlist` merely declines to SEARCH a one-item joinlist.
// Returning 1 here is therefore the PG-faithful value and 2 is the historical
// goopg one.
func minSearchRels() int {
	if oneRelSearchEnabled() {
		return 1
	}
	return 2
}
