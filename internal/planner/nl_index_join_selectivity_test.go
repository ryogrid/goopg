package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestOrigTypeMatchesDetectsRuntimeTypeMismatch pins the
// M0077-0001 type-mismatch override that bypasses the
// M0075-0002 selectivity guard for chained-NLI rebinds.
//
// Background: Q8 SF=1 cancelled at 336 s with
// `pq: column "c_nationkey" is not numeric at runtime
// (42804)` even after the takeover's outer-is-Join
// schema-refresh fix landed. Diagnosis (live trace
// 2026-05-10) showed the chained-NLI rebind path
// (`outerIsNLI`) finding the correct numeric slot at
// index 38 but skipping the rebind because the
// selectivity guard estimated `outerScanRowCount /
// NDistinct(c_nationkey) = ~150000 / 25 = 6000` —
// far above the 100-row threshold. The original
// stale slot at index 2 was `n_name` (char), which
// the numeric IndexScan probe encoder rejects.
//
// origTypeMatches lets tryBuildNLI detect the runtime-
// fatal case and override the selectivity guard so
// the rebind lands on the correctly-typed slot.
func TestOrigTypeMatchesDetectsRuntimeTypeMismatch(t *testing.T) {
	numericType := catalog.Type{Name: "numeric"}
	charType := catalog.Type{Name: "char"}

	schema := Schema{
		{Name: "n_name", Type: charType},
		{Name: "c_nationkey", Type: numericType},
	}

	// Stale binding: cr.Index points at slot 0
	// (n_name/char) but cr.Type is numeric — runtime
	// 42804 territory.
	staleNumeric := &ColumnRef{
		Index: 0,
		Name:  "c_nationkey",
		Type:  numericType,
	}
	if origTypeMatches(staleNumeric, schema) {
		t.Error("stale numeric ColumnRef pointing at char slot must report MISMATCH")
	}

	// Correct binding: cr.Index points at slot 1
	// (c_nationkey/numeric) and cr.Type is numeric —
	// no rebind needed.
	correctNumeric := &ColumnRef{
		Index: 1,
		Name:  "c_nationkey",
		Type:  numericType,
	}
	if !origTypeMatches(correctNumeric, schema) {
		t.Error("ColumnRef pointing at matching-typed slot must report MATCH")
	}

	// Out-of-range index (e.g., schema shrunk after a
	// downstream rewrite). Treat as MISMATCH so the
	// caller forces a rebind rather than crashing.
	oob := &ColumnRef{
		Index: 99,
		Name:  "c_nationkey",
		Type:  numericType,
	}
	if origTypeMatches(oob, schema) {
		t.Error("out-of-range cr.Index must report MISMATCH so tryBuildNLI forces a rebind")
	}

	// Negative index: same conservative treatment.
	neg := &ColumnRef{
		Index: -1,
		Name:  "c_nationkey",
		Type:  numericType,
	}
	if origTypeMatches(neg, schema) {
		t.Error("negative cr.Index must report MISMATCH")
	}

	// Nil cr (defensive): MISMATCH, never panic.
	if origTypeMatches(nil, schema) {
		t.Error("nil ColumnRef must report MISMATCH")
	}
}
