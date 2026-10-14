package misc

import "testing"

// TestGUCRegistryPrivilegeContextsAndPredLockNames pins the first slice of the
// "goopg's GUC registry diverges from PG 18.3's parameter set" task against
// guc_tables.c (pg_gucs_gen.go is generated from it):
//
//   - commit_delay, compute_query_id and track_io_timing are PGC_SUSET, so an
//     ordinary role may not SET them; goopg registered them user-settable.
//   - the SSI sizing parameters are max_pred_locks_per_{transaction,relation,
//     page}; goopg registered them as max_predicate_locks_per_{xact,...}, names
//     PG rejects, so a postgresql.conf written for PG could not set them.
func TestGUCRegistryPrivilegeContextsAndPredLockNames(t *testing.T) {
	r := BuildDefaultRegistry()
	for _, name := range []string{
		"commit_delay", "compute_query_id", "track_io_timing",
		"max_pred_locks_per_transaction", "max_pred_locks_per_relation", "max_pred_locks_per_page",
	} {
		v, ok := r.Get(name)
		if !ok {
			t.Errorf("%s: not registered", name)
			continue
		}
		pg, ok := pgGUCs[name]
		if !ok {
			t.Fatalf("%s: not a PG 18.3 parameter", name)
		}
		if v.Name != pg.Name || v.Context != pg.Context {
			t.Errorf("%s: goopg %q/%v, PG 18.3 %q/%v", name, v.Name, v.Context, pg.Name, pg.Context)
		}
	}
	for _, old := range []string{
		"max_predicate_locks_per_xact", "max_predicate_locks_per_relation", "max_predicate_locks_per_page",
	} {
		if _, ok := r.Get(old); ok {
			t.Errorf("%s is registered; PG 18.3 has no such parameter", old)
		}
	}
}
