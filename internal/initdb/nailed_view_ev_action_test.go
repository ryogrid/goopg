package initdb

import (
	"testing"
)

// TestNailedViewEvActionBlobSetMatchesSeededViews is the guard M0131-S9.0 owes
// for replacing six per-view //go:embed lines with one glob.
//
// The hand-written form failed loudly: a missing .dat file broke the build. The
// glob does not — an embed.FS pattern that matches five files instead of six
// compiles fine, and the sixth view's seed row then panics at initdb time, or
// (worse) a stale .dat lingers for a view nobody seeds any more and silently
// inflates the compiled binary while the manifest says otherwise. So the set
// equality has to be asserted somewhere, and here is it: every seeded view owns
// a blob, and every blob belongs to a seeded view.
func TestNailedViewEvActionBlobSetMatchesSeededViews(t *testing.T) {
	seeded := map[string]bool{}
	for _, r := range nailedViewSeedRels() {
		seeded[r.RelName] = true
	}
	embedded := map[string]bool{}
	for _, v := range nailedViewEvActionBlobs() {
		embedded[v] = true
	}
	for v := range seeded {
		if !embedded[v] {
			t.Errorf("view %s is seeded into pg_class but has no %s "+
				"(capture it with scripts/capture-ev-action.sh %s)", v, nailedViewEvActionFile(v), v)
		}
	}
	for v := range embedded {
		if !seeded[v] {
			t.Errorf("%s is embedded but %s is in no manifest rel row — "+
				"delete the blob or re-run cmd/gen-nailed-view-tables", nailedViewEvActionFile(v), v)
		}
	}
	if len(embedded) == 0 {
		t.Fatal("no ev_action blobs embedded at all — the //go:embed glob matched nothing")
	}
}

// TestNailedViewRewriteEntriesCoverEverySeededView pins the other half of the
// same invariant, on the pg_rewrite side. A hosted PG opens a view with
// relhasrules=true and resolves its rule through
// SearchSysCache2(RULERELNAME, view_oid, "_RETURN"); a pg_class row seeded
// without its rule row FATALs there with "cache lookup failed for rule …",
// which is the failure M0106-0010 Step 3dm phase B existed to prevent and the
// one a widening pass (M0131-S9.1, 23 more views) is most likely to reintroduce.
func TestNailedViewRewriteEntriesCoverEverySeededView(t *testing.T) {
	rules := map[uint32]pgRewriteEntry{}
	for _, e := range nailedViewRewriteEntries() {
		if prev, dup := rules[e.EvClass]; dup {
			t.Errorf("two _RETURN rules (%d, %d) for ev_class %d", prev.OID, e.OID, e.EvClass)
		}
		rules[e.EvClass] = e
	}
	for _, r := range nailedViewSeedRels() {
		e, ok := rules[r.OID]
		if !ok {
			t.Errorf("view %s (OID %d) is seeded with no _RETURN rule", r.RelName, r.OID)
			continue
		}
		// The rule form is fixed for ON-SELECT views; the generator writes it
		// as literals, so this asserts the generator, not the manifest.
		if e.RuleName != "_RETURN" || e.EvType != '1' || e.EvEnabled != 'O' || !e.IsInstead || e.EvQual != "<>" {
			t.Errorf("%s: rule form = {%q %q %q instead=%v qual=%q}, want {_RETURN '1' 'O' instead=true qual=\"<>\"}",
				r.RelName, e.RuleName, e.EvType, e.EvEnabled, e.IsInstead, e.EvQual)
		}
		if e.EvAction != nailedViewEvAction(r.RelName) {
			t.Errorf("%s: ev_action is not the view's own captured blob", r.RelName)
		}
	}
	if len(rules) != len(nailedViewSeedRels()) {
		t.Errorf("%d rules for %d seeded views", len(rules), len(nailedViewSeedRels()))
	}
}

// TestNailedViewEvActionRejectsUnknownView pins the loud-failure contract. The
// panic is the point: nailedViewEvAction is called while building bootstrap
// seed rows, and returning "" there would write a pg_rewrite row violating
// ev_action's BKI_FORCE_NOT_NULL whose damage only surfaces inside a hosted
// PG's stringToNode, far from the cause.
func TestNailedViewEvActionRejectsUnknownView(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nailedViewEvAction returned normally for a view with no blob; want panic")
		}
	}()
	_ = nailedViewEvAction("pg_stat_no_such_view")
}
