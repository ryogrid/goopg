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

// TestGUCRegistryReloadableContexts pins slice 2 of the same task: seven
// parameters goopg registered with a context PG 18.3 does not use.
//
//   - wal_writer_delay, wal_writer_flush_after, bgwriter_delay and
//     bgwriter_lru_maxpages are PGC_SIGHUP; goopg required a restart. The
//     reload hook now pushes them into the running loops
//     (initdb.Runtime.ApplyWriterSettings).
//   - autovacuum_max_workers and autovacuum_work_mem are PGC_SIGHUP and
//     wal_sender_timeout is PGC_USERSET. Nothing in goopg reads them yet (the
//     autovacuum launcher runs a fixed single worker; the walsender has no
//     timeout), so only the context the server reports and enforces changes.
func TestGUCRegistryReloadableContexts(t *testing.T) {
	r := BuildDefaultRegistry()
	for _, name := range []string{
		"wal_writer_delay", "wal_writer_flush_after", "bgwriter_delay", "bgwriter_lru_maxpages",
		"autovacuum_max_workers", "autovacuum_work_mem", "wal_sender_timeout",
	} {
		v, ok := r.Get(name)
		if !ok {
			t.Errorf("%s: not registered", name)
			continue
		}
		if pg := pgGUCs[name]; v.Context != pg.Context {
			t.Errorf("%s: goopg context %v, PG 18.3 %v", name, v.Context, pg.Context)
		}
	}
}
