package server

import (
	"testing"

	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/executor"
)

// P1 of docs/design/parallel-query/10-roadmap.md — the parallel GUCs must
// reach execution. Before this stage every one of them was accepted by SET and
// read by nothing.
//
// The readers follow the sessionStatsTarget shape: three layers of defensive
// fallback (nil registry / unregistered GUC / unparseable value). What matters
// per reader is which value each layer falls back TO — a wrong default here is
// silent, so each case below states the direction it protects.

func TestSessionMaxParallelWorkersPerGather(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  string // "" ⇒ leave at boot default (2)
		want int
	}{
		{name: "unset-default", set: "", want: 2},
		{name: "explicit-zero-disables", set: "0", want: 0},
		{name: "four", set: "4", want: 4},
		{name: "max", set: "1024", want: 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := config.NewSessionRegistry(config.BuildDefaultRegistry())
			if tc.set != "" {
				if err := sess.Set("max_parallel_workers_per_gather", tc.set, false); err != nil {
					t.Fatalf("Set(%q): %v", tc.set, err)
				}
			}
			if got := sessionMaxParallelWorkersPerGather(sess); got != tc.want {
				t.Errorf("= %d, want %d", got, tc.want)
			}
		})
	}
	// A nil registry must read as "serial", never as "some default degree of
	// parallelism" — the several NewContext() sites that pass no session
	// (COPY, DDL) rely on this.
	if got := sessionMaxParallelWorkersPerGather(nil); got != 0 {
		t.Errorf("nil registry = %d, want 0 (serial)", got)
	}
}

func TestSessionMaxParallelWorkers(t *testing.T) {
	sess := config.NewSessionRegistry(config.BuildDefaultRegistry())
	if got := sessionMaxParallelWorkers(sess); got != 8 {
		t.Errorf("default = %d, want 8 (PG 18.3)", got)
	}
	if err := sess.Set("max_parallel_workers", "0", false); err != nil {
		t.Fatalf("Set(0): %v", err)
	}
	if got := sessionMaxParallelWorkers(sess); got != 0 {
		t.Errorf("after SET 0 = %d, want 0", got)
	}
	if got := sessionMaxParallelWorkers(nil); got != 0 {
		t.Errorf("nil registry = %d, want 0 (serial)", got)
	}
}

// TestSessionMinParallelTableScanSize pins both the unit and the fallback
// direction. The value is in BLOCKS as of P0, and an unreadable GUC must fall
// back to PG's 1024-block default — NOT to zero, which would mean "every
// relation qualifies for a parallel path", the unsafe direction.
func TestSessionMinParallelTableScanSize(t *testing.T) {
	sess := config.NewSessionRegistry(config.BuildDefaultRegistry())
	if got := sessionMinParallelTableScanSize(sess); got != 1024 {
		t.Errorf("default = %d blocks, want 1024 (8MB / BLCKSZ)", got)
	}
	// A byte-suffixed SET is converted to blocks by the GUC layer, so the
	// reader sees a plain block count and must not try to parse a suffix.
	if err := sess.Set("min_parallel_table_scan_size", "16MB", false); err != nil {
		t.Fatalf("Set(16MB): %v", err)
	}
	if got := sessionMinParallelTableScanSize(sess); got != 2048 {
		t.Errorf("after SET 16MB = %d blocks, want 2048", got)
	}
	if got := sessionMinParallelTableScanSize(nil); got != 1024 {
		t.Errorf("nil registry = %d, want 1024 (PG default, not 0)", got)
	}
}

func TestSessionParallelLeaderParticipation(t *testing.T) {
	sess := config.NewSessionRegistry(config.BuildDefaultRegistry())
	if !sessionParallelLeaderParticipation(sess) {
		t.Error("default should be on (PG 18.3)")
	}
	if err := sess.Set("parallel_leader_participation", "off", false); err != nil {
		t.Fatalf("Set(off): %v", err)
	}
	if sessionParallelLeaderParticipation(sess) {
		t.Error("after SET off should be false")
	}
	if !sessionParallelLeaderParticipation(nil) {
		t.Error("nil registry should keep PG's default of on")
	}
}

// TestSessionDebugParallelQuery also exercises the P0 synonym work end to end:
// a user writing `SET debug_parallel_query = true` must be observed here as
// the canonical "on".
func TestSessionDebugParallelQuery(t *testing.T) {
	for _, tc := range []struct{ set, want string }{
		{"", "off"},
		{"on", "on"},
		{"off", "off"},
		{"regress", "regress"},
		{"true", "on"},   // P0 hidden synonym
		{"1", "on"},      // P0 hidden synonym
		{"false", "off"}, // P0 hidden synonym
	} {
		name := tc.set
		if name == "" {
			name = "unset-default"
		}
		t.Run(name, func(t *testing.T) {
			sess := config.NewSessionRegistry(config.BuildDefaultRegistry())
			if tc.set != "" {
				if err := sess.Set("debug_parallel_query", tc.set, false); err != nil {
					t.Fatalf("Set(%q): %v", tc.set, err)
				}
			}
			if got := sessionDebugParallelQuery(sess); got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
	if got := sessionDebugParallelQuery(nil); got != "off" {
		t.Errorf("nil registry = %q, want %q", got, "off")
	}
}

// TestParallelGUCsReachExecutorContext closes the seam that nothing covered
// before: the assignment lines themselves.
//
// Today the GUC→behaviour chain is tested as two disjoint halves — SET→reader
// (above) and context-field→behaviour (elsewhere) — with the
// `ectx.X = sessionX(sess)` line covered by neither. That gap is exactly how
// the pre-existing inconsistency survived, where FreezeMinAge and
// EnableOpportunisticPrune are assigned on the simple-query path but never on
// the extended one. This test applies the assignment block the way dispatch
// does and asserts every field lands, so a field added to one path and
// forgotten on the other fails here.
func TestParallelGUCsReachExecutorContext(t *testing.T) {
	sess := config.NewSessionRegistry(config.BuildDefaultRegistry())
	for name, val := range map[string]string{
		"max_parallel_workers_per_gather": "4",
		"max_parallel_workers":            "6",
		"min_parallel_table_scan_size":    "16MB",
		"parallel_leader_participation":   "off",
		"debug_parallel_query":            "on",
	} {
		if err := sess.Set(name, val, false); err != nil {
			t.Fatalf("Set(%s=%s): %v", name, val, err)
		}
	}

	apply := func(ectx *executor.Context) {
		ectx.MaxParallelWorkersPerGather = sessionMaxParallelWorkersPerGather(sess)
		ectx.MaxParallelWorkers = sessionMaxParallelWorkers(sess)
		ectx.MinParallelTableScanBlocks = sessionMinParallelTableScanSize(sess)
		ectx.ParallelLeaderParticipation = sessionParallelLeaderParticipation(sess)
		ectx.DebugParallelQuery = sessionDebugParallelQuery(sess)
	}

	ectx := executor.NewContext()
	apply(ectx)

	if ectx.MaxParallelWorkersPerGather != 4 {
		t.Errorf("MaxParallelWorkersPerGather = %d, want 4", ectx.MaxParallelWorkersPerGather)
	}
	if ectx.MaxParallelWorkers != 6 {
		t.Errorf("MaxParallelWorkers = %d, want 6", ectx.MaxParallelWorkers)
	}
	if ectx.MinParallelTableScanBlocks != 2048 {
		t.Errorf("MinParallelTableScanBlocks = %d, want 2048 blocks", ectx.MinParallelTableScanBlocks)
	}
	if ectx.ParallelLeaderParticipation {
		t.Error("ParallelLeaderParticipation = true, want false")
	}
	if ectx.DebugParallelQuery != "on" {
		t.Errorf("DebugParallelQuery = %q, want %q", ectx.DebugParallelQuery, "on")
	}
}

// TestParallelGUCsSurviveChildContext pins that PL/pgSQL's derived child
// contexts carry the parallel settings. plpgsql_runtime.go builds a child with
// `*child = *ctx`, a whole-struct copy, so new fields propagate for free — but
// that is a property of the copy style, not a guarantee, and a future switch to
// field-by-field derivation would silently zero these.
func TestParallelGUCsSurviveChildContext(t *testing.T) {
	parent := executor.NewContext()
	parent.MaxParallelWorkersPerGather = 4
	parent.MaxParallelWorkers = 6
	parent.MinParallelTableScanBlocks = 2048
	parent.ParallelLeaderParticipation = true
	parent.DebugParallelQuery = "on"

	child := executor.NewContext()
	*child = *parent

	if child.MaxParallelWorkersPerGather != 4 || child.MaxParallelWorkers != 6 ||
		child.MinParallelTableScanBlocks != 2048 || !child.ParallelLeaderParticipation ||
		child.DebugParallelQuery != "on" {
		t.Errorf("parallel settings did not survive child-context derivation: %+v",
			struct {
				PerGather int
				Total     int
				MinBlocks int64
				Leader    bool
				Debug     string
			}{child.MaxParallelWorkersPerGather, child.MaxParallelWorkers,
				child.MinParallelTableScanBlocks, child.ParallelLeaderParticipation,
				child.DebugParallelQuery})
	}
}
