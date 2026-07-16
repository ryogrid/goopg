package executor

import (
	"testing"
)

// TestArrayAggStringAggHonorDateStyle pins the next M-NIGHTLY DateStyle
// follow-up slice (resume point recorded after the `||` fix): array_agg,
// string_agg, and the variadic user-defined-aggregate element bundler
// (operators_join_agg.go's applyAgg) all called Datum.Format() directly on
// DATE/TIMESTAMP elements, hardcoding ISO/Postgres-MDY regardless of `SET
// datestyle` — diverging from the already-fixed SELECT/COPY/CAST-to-text/||
// output paths. Fixed by routing those call sites through
// formatDatumDateStyle(d, o.ctx).
func TestArrayAggStringAggHonorDateStyle(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "datestyle" {
			return "German, DMY", true
		}
		return "", false
	}

	if err := runDDL(t, ctx, "CREATE TABLE dagg (id int, d date)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO dagg VALUES (1, '2026-07-14')"); err != nil {
		t.Fatalf("INSERT 1: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO dagg VALUES (2, '2026-07-15')"); err != nil {
		t.Fatalf("INSERT 2: %v", err)
	}

	rows := runQuery(t, ctx, "SELECT string_agg(d::text, ', ' ORDER BY id), array_agg(d ORDER BY id) FROM dagg")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if got, want := rows[0][0].Format(), "14.07.2026, 15.07.2026"; got != want {
		t.Errorf("string_agg = %q, want %q (German DateStyle)", got, want)
	}
	if got, want := rows[0][1].Format(), "{14.07.2026,15.07.2026}"; got != want {
		t.Errorf("array_agg = %q, want %q (German DateStyle)", got, want)
	}
}

// TestArrayAggStringAggNilCtxDefaultsISO confirms callers without a live
// session GUC (nil GetSetting) still default to ISO/MDY, matching Format()'s
// pre-existing hardcoded behavior — so non-session call sites are
// behavior-unchanged by this fix.
func TestArrayAggStringAggNilCtxDefaultsISO(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE dagg2 (id int, d date)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO dagg2 VALUES (1, '2026-07-14')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows := runQuery(t, ctx, "SELECT array_agg(d) FROM dagg2")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if got, want := rows[0][0].Format(), "{2026-07-14}"; got != want {
		t.Errorf("array_agg (no session GUC) = %q, want %q (ISO default)", got, want)
	}
}
