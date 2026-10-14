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

// TestFlushAfterGUCsUseBlockUnits pins slice 3: PG 18.3 declares
// checkpoint_flush_after, bgwriter_flush_after and backend_flush_after
// GUC_UNIT_BLOCKS and wal_writer_flush_after GUC_UNIT_XBLOCKS (guc_tables.c).
// goopg had no unit on any of them and kept wal_writer_flush_after in bytes,
// so SHOW printed 32 / 64 / 1048576 where PG prints 256kB / 512kB / 1MB, and
// a PG-written `wal_writer_flush_after = 1MB` line was refused.
func TestFlushAfterGUCsUseBlockUnits(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())
	for _, tc := range []struct{ name, stored, display string }{
		{"checkpoint_flush_after", "32", "256kB"},
		{"bgwriter_flush_after", "64", "512kB"},
		{"backend_flush_after", "0", "0"},
		{"wal_writer_flush_after", "128", "1MB"},
	} {
		v, eff, ok := s.Get(tc.name)
		if !ok {
			t.Errorf("%s: not registered", tc.name)
			continue
		}
		if v.Unit != UnitBlocks {
			t.Errorf("%s: Unit = %v, want UnitBlocks", tc.name, v.Unit)
		}
		if eff != tc.stored {
			t.Errorf("%s: stored %q, want %q pages", tc.name, eff, tc.stored)
		}
		if _, got, _ := s.GetDisplay(tc.name); got != tc.display {
			t.Errorf("SHOW %s = %q, want PG 18.3's %q", tc.name, got, tc.display)
		}
	}

	// The parse direction, from a configuration file and from SET.
	r := BuildDefaultRegistry()
	if err := r.ApplyConfigEntries([]ConfigEntry{
		{Name: "wal_writer_flush_after", Value: "2MB"},
		{Name: "checkpoint_flush_after", Value: "1MB"},
	}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"wal_writer_flush_after": "256", "checkpoint_flush_after": "128"} {
		if v, _ := r.Get(name); v.Value != want {
			t.Errorf("%s from the config file: stored %q, want %q pages", name, v.Value, want)
		}
	}
	if err := s.Set("backend_flush_after", "128kB", false); err != nil {
		t.Fatal(err)
	}
	if _, eff, _ := s.Get("backend_flush_after"); eff != "16" {
		t.Errorf("SET backend_flush_after = '128kB' stored %q, want 16 pages", eff)
	}
}
