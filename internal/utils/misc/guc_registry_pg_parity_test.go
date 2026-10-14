package misc

import (
	"errors"
	"math"
	"strings"
	"testing"
)

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

// TestRegistryNamesArePGOrGoopgNamespaced pins slice 5: every registered
// parameter is either a PG 18.3 built-in (pg_gucs_gen.go, generated from
// guc_tables.c) or one of goopg's own knobs in the "goopg." custom-variable
// namespace. goopg used to register wal_sender_memory_buffer,
// enable_nestloop_index, enable_opportunistic_prune and
// vacuum_multixact_freeze_max_age as plain names, which PG rejects as
// unrecognized configuration parameters.
func TestRegistryNamesArePGOrGoopgNamespaced(t *testing.T) {
	r := BuildDefaultRegistry()
	for _, v := range r.All() {
		if _, ok := pgGUCs[strings.ToLower(v.Name)]; ok {
			continue
		}
		if !strings.HasPrefix(v.Name, "goopg.") {
			t.Errorf("%q is neither a PG 18.3 parameter nor in the goopg. namespace", v.Name)
		}
	}
	for _, old := range []string{
		"wal_sender_memory_buffer", "enable_nestloop_index", "enable_opportunistic_prune",
		"vacuum_multixact_freeze_max_age",
	} {
		if _, ok := r.Get(old); ok {
			t.Errorf("%s is registered; PG 18.3 has no such parameter", old)
		}
	}
	// The real PG parameter its bogus twin shadowed takes guc_tables.c's
	// default and range.
	v, _ := r.Get("autovacuum_multixact_freeze_max_age")
	if v.BootVal != "400000000" || v.MinVal != 10000 || v.MaxVal != 2000000000 {
		t.Errorf("autovacuum_multixact_freeze_max_age = %s [%v, %v], want 400000000 [10000, 2000000000]", v.BootVal, v.MinVal, v.MaxVal)
	}
}

// TestGUCDefaultsAndRangesFollowPG pins slice 6: boot values and ranges taken
// from guc_tables.c where goopg's registry differed. None of these shapes a
// plan; the plan-shaping differences (work_mem, max_parallel_workers_per_gather,
// enable_partitionwise_*, jit) are an owner decision and stay out.
func TestGUCDefaultsAndRangesFollowPG(t *testing.T) {
	r := BuildDefaultRegistry()
	for _, tc := range []struct {
		name     string
		boot     string
		min, max float64
	}{
		{"autovacuum_vacuum_max_threshold", "100000000", -1, math.MaxInt32},
		{"vacuum_multixact_failsafe_age", "1600000000", 0, 2100000000},
		{"io_workers", "3", 1, 32},
		{"bgwriter_lru_maxpages", "100", 0, math.MaxInt32 / 2},
		{"scram_iterations", "4096", 1, math.MaxInt32},
		{"wal_sender_timeout", "60000", 0, math.MaxInt32},
		{"seq_page_cost", "1", 0, math.MaxFloat64},
		{"random_page_cost", "4", 0, math.MaxFloat64},
		{"cpu_tuple_cost", "0.01", 0, math.MaxFloat64},
		{"cpu_index_tuple_cost", "0.005", 0, math.MaxFloat64},
		{"cpu_operator_cost", "0.0025", 0, math.MaxFloat64},
		{"jit_above_cost", "100000", -1, math.MaxFloat64},
		{"jit_inline_above_cost", "500000", -1, math.MaxFloat64},
		{"jit_optimize_above_cost", "500000", -1, math.MaxFloat64},
	} {
		v, ok := r.Get(tc.name)
		if !ok {
			t.Errorf("%s: not registered", tc.name)
			continue
		}
		if v.Value != tc.boot || v.MinVal != tc.min || v.MaxVal != tc.max {
			t.Errorf("%s = %s [%v, %v], want PG's %s [%v, %v]", tc.name, v.Value, v.MinVal, v.MaxVal, tc.boot, tc.min, tc.max)
		}
	}
	if v, _ := r.Get("compute_query_id"); v.BootVal != "auto" {
		t.Errorf("compute_query_id boot = %q, want auto", v.BootVal)
	}
	// A cost PG accepts must be accepted here too.
	s := NewSessionRegistry(r)
	if err := s.Set("random_page_cost", "1e12", false); err != nil {
		t.Errorf("SET random_page_cost = 1e12: %v (PG accepts up to DBL_MAX)", err)
	}
}

// TestPresetOptionsAreReported pins slice 7: the PGC_INTERNAL "Preset Options"
// clients read on connect (SQLAlchemy runs SHOW max_identifier_length) are
// registered with PG 18.3's values and display, and cannot be SET.
func TestPresetOptionsAreReported(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())
	for name, want := range map[string]string{
		"max_identifier_length": "63",
		"max_function_args":     "100",
		"max_index_keys":        "32",
		"segment_size":          "1GB",
		"wal_block_size":        "8192",
		"debug_assertions":      "off",
		"data_checksums":        "off",
		"huge_pages_status":     "unknown",
	} {
		v, got, ok := s.GetDisplay(name)
		if !ok {
			t.Errorf("SHOW %s: not registered", name)
			continue
		}
		if got != want {
			t.Errorf("SHOW %s = %q, want %q", name, got, want)
		}
		if v.Context != pgGUCs[name].Context {
			t.Errorf("%s context %v, want PG's %v", name, v.Context, pgGUCs[name].Context)
		}
		if err := s.Set(name, want, false); err == nil {
			t.Errorf("SET %s succeeded; PG reports it cannot be changed", name)
		}
	}
}

// TestSetRefusalFollowsContext pins set_config_option's refusal (guc.c) for
// a SET on a parameter whose context forbids it: SQLSTATE 55P02 and a message
// per context. goopg answered every context with "cannot be changed now"
// under SQLSTATE 22023.
func TestSetRefusalFollowsContext(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())
	for name, want := range map[string]string{
		"max_identifier_length": `parameter "max_identifier_length" cannot be changed`,
		"shared_buffers":        `parameter "shared_buffers" cannot be changed without restarting the server`,
		"checkpoint_timeout":    `parameter "checkpoint_timeout" cannot be changed now`,
		"jit_debugging_support": `parameter "jit_debugging_support" cannot be set after connection start`,
	} {
		err := s.Set(name, "1", false)
		var verr *ValidationError
		if !errors.As(err, &verr) {
			t.Errorf("SET %s: %v, want a ValidationError", name, err)
			continue
		}
		if verr.Msg != want || verr.SQLState() != "55P02" {
			t.Errorf("SET %s: %s %q, want 55P02 %q", name, verr.SQLState(), verr.Msg, want)
		}
	}
}
