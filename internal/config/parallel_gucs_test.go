package config

import (
	"math"
	"testing"
)

// P0 of docs/design/parallel-query/10-roadmap.md — the parallel GUCs must
// report what PostgreSQL 18.3 reports. Before this stage every one of them was
// registered but two were wrong by a factor of 1024 in both value and unit, one
// was missing entirely, and the enum rejected values PG accepts.
//
// These are observable through SHOW today, independently of whether parallel
// query is ever implemented.

// TestMinParallelScanSizeDisplaysLikePG pins the unit fix. Upstream declares
// both GUCs GUC_UNIT_BLOCKS with defaults (8*1024*1024)/BLCKSZ = 1024 blocks
// and 512kB/BLCKSZ = 64 blocks (guc_tables.c:3727-3747), which SHOW renders as
// "8MB" and "512kB". goopg previously registered them as UnitKB holding the
// byte counts, so SHOW answered "8GB" and "512MB".
//
// The index one is the load-bearing case: 64 blocks is smaller than one MB, so
// it can only render via the kB row, whose multiplier is NEGATIVE (a block is
// larger than a kB, so the conversion multiplies). Without that branch in
// FormatDisplayValue it would fall through every row and print a bare "64".
func TestMinParallelScanSizeDisplaysLikePG(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())

	for _, tc := range []struct {
		name        string
		wantDisplay string
		wantStored  string // canonical value: a bare count of blocks
	}{
		{"min_parallel_table_scan_size", "8MB", "1024"},
		{"min_parallel_index_scan_size", "512kB", "64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, eff, ok := s.Get(tc.name)
			if !ok {
				t.Fatalf("%s is not registered", tc.name)
			}
			if v.Unit != UnitBlocks {
				t.Errorf("Unit = %v, want UnitBlocks (upstream GUC_UNIT_BLOCKS)", v.Unit)
			}
			if eff != tc.wantStored {
				t.Errorf("canonical value = %q, want %q (blocks)", eff, tc.wantStored)
			}
			if _, got, _ := s.GetDisplay(tc.name); got != tc.wantDisplay {
				t.Errorf("SHOW %s = %q, want %q (PG 18.3)", tc.name, got, tc.wantDisplay)
			}
		})
	}
}

// TestMinParallelScanSizeAcceptsByteSuffixes pins the parse direction: a
// blocks-valued GUC still takes ordinary byte suffixes on input, converting to
// blocks for storage. PG has no "block" suffix either.
func TestMinParallelScanSizeAcceptsByteSuffixes(t *testing.T) {
	for _, tc := range []struct{ set, wantStored, wantDisplay string }{
		{"8MB", "1024", "8MB"},
		{"16MB", "2048", "16MB"},
		{"512kB", "64", "512kB"},
		{"1GB", "131072", "1GB"},
	} {
		t.Run(tc.set, func(t *testing.T) {
			s := NewSessionRegistry(BuildDefaultRegistry())
			if err := s.Set("min_parallel_table_scan_size", tc.set, false); err != nil {
				t.Fatalf("SET = %s: %v", tc.set, err)
			}
			if _, eff, _ := s.Get("min_parallel_table_scan_size"); eff != tc.wantStored {
				t.Errorf("stored = %q, want %q blocks", eff, tc.wantStored)
			}
			if _, got, _ := s.GetDisplay("min_parallel_table_scan_size"); got != tc.wantDisplay {
				t.Errorf("SHOW = %q, want %q", got, tc.wantDisplay)
			}
		})
	}
}

// TestMaxParallelWorkersRegistered pins the GUC that was missing entirely.
// PG's default is 8 — deliberately not the 2 used by
// max_parallel_workers_per_gather, which is a different knob.
func TestMaxParallelWorkersRegistered(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())
	v, eff, ok := s.Get("max_parallel_workers")
	if !ok {
		t.Fatal("max_parallel_workers is not registered")
	}
	if v.Type != TypeInt {
		t.Errorf("Type = %v, want TypeInt", v.Type)
	}
	if v.BootVal != "8" {
		t.Errorf("BootVal = %q, want %q (PG 18.3 default)", v.BootVal, "8")
	}
	if eff != "8" {
		t.Errorf("effective = %q, want %q", eff, "8")
	}
	if v.Context != ContextUserset {
		t.Errorf("Context = %v, want ContextUserset", v.Context)
	}
	if err := s.Set("max_parallel_workers", "0", false); err != nil {
		t.Errorf("SET = 0 (disable) should be accepted: %v", err)
	}
	if err := s.Set("max_parallel_workers", "-1", false); err == nil {
		t.Error("SET = -1 should be rejected (MinVal 0)")
	}
}

// TestDebugParallelQueryAcceptsBooleanSynonyms pins PG's hidden
// config_enum_entry rows: `SET debug_parallel_query = true` is accepted
// upstream (guc_tables.c:395-404) even though "true" is not in enumvals.
// goopg rejected every synonym, which defeated the exact upstream-spec
// compatibility the GUC was registered for — and it is the lever the
// parallel-query correctness gate is built on.
func TestDebugParallelQueryAcceptsBooleanSynonyms(t *testing.T) {
	for _, tc := range []struct{ set, want string }{
		{"on", "on"},
		{"off", "off"},
		{"regress", "regress"},
		{"true", "on"},
		{"TRUE", "on"},
		{"yes", "on"},
		{"1", "on"},
		{"t", "on"},
		{"false", "off"},
		{"no", "off"},
		{"0", "off"},
		{"f", "off"},
	} {
		t.Run(tc.set, func(t *testing.T) {
			s := NewSessionRegistry(BuildDefaultRegistry())
			if err := s.Set("debug_parallel_query", tc.set, false); err != nil {
				t.Fatalf("SET = %s: %v", tc.set, err)
			}
			if _, eff, _ := s.Get("debug_parallel_query"); eff != tc.want {
				t.Errorf("SET %s -> %q, want %q", tc.set, eff, tc.want)
			}
		})
	}

	s := NewSessionRegistry(BuildDefaultRegistry())
	if err := s.Set("debug_parallel_query", "maybe", false); err == nil {
		t.Error("a non-boolean, non-enum value must still be rejected")
	}

	// The synonyms are HIDDEN: they must not leak into EnumOptions, because
	// that list is what pg_settings.enumvals and the error HINT render.
	v, _, _ := s.Get("debug_parallel_query")
	for _, opt := range v.EnumOptions {
		switch opt {
		case "on", "off", "regress":
		default:
			t.Errorf("EnumOptions leaked a hidden synonym %q; PG's enumvals is {off,on,regress}", opt)
		}
	}
}

// TestBoolSynonymsOnlyForOnOffEnums pins the guard: an enum without both "on"
// and "off" must not silently accept booleans. DateStyle is the canary.
func TestBoolSynonymsOnlyForOnOffEnums(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())
	if v, _, ok := s.Get("intervalstyle"); ok && v.Type == TypeEnum {
		if err := s.Set("intervalstyle", "true", false); err == nil {
			t.Error("intervalstyle has no on/off pair; 'true' must be rejected")
		}
	}
}

// TestParallelCostGUCCeilings pins the MaxVal correction: PG's ceiling for
// these is DBL_MAX, not the 1e15 goopg carried.
func TestParallelCostGUCCeilings(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())
	for _, name := range []string{"parallel_setup_cost", "parallel_tuple_cost"} {
		v, _, ok := s.Get(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if v.MaxVal != math.MaxFloat64 {
			t.Errorf("%s MaxVal = %v, want math.MaxFloat64 (PG's DBL_MAX)", name, v.MaxVal)
		}
	}
}
