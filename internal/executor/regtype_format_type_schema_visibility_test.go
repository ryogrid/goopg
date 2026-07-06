package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestRegtypeFormatTypeSchemaVisibility pins the schema-visibility fix to
// userTypeNameForOID (shared by the `::regtype` cast's OID->name direction
// and format_type's built-in-fallback path): a user-defined type's name is
// only schema-qualified with "public." when "public" is NOT visible on the
// session's effective search_path, matching real PostgreSQL's
// regtypeout/format_type instead of unconditionally prefixing "public."
// regardless of search_path — the gap recorded in the 2026-07-06
// pg_typeof()::oid deferral-ledger row (discovered via the pre-existing,
// untouched 'mood'::regtype cast).
func TestRegtypeFormatTypeSchemaVisibility(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im := cat.(*catalog.InMemory)
	et, err := im.RegisterEnum("mood", []string{"sad", "ok", "happy"})
	if err != nil {
		t.Fatalf("RegisterEnum: %v", err)
	}

	cases := []struct {
		name       string
		searchPath string
		want       string
	}{
		{"search_path='' (pg_dump) qualifies", "", "public.mood"},
		{"search_path without public qualifies", "other_schema", "public.mood"},
		{`default "$user", public unqualified`, `"$user", public`, "mood"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx.GetSetting = func(name string) (string, bool) {
				if name == "search_path" {
					return tc.searchPath, true
				}
				return "", false
			}

			castRows := runQuery(t, ctx, fmt.Sprintf("SELECT %d::regtype", et.OID))
			if len(castRows) != 1 || castRows[0][0].StringValue() != tc.want {
				t.Errorf("%d::regtype = %v, want %q", et.OID, castRows, tc.want)
			}

			ftRows := runQuery(t, ctx, fmt.Sprintf("SELECT format_type(%d, -1)", et.OID))
			if len(ftRows) != 1 || ftRows[0][0].StringValue() != tc.want {
				t.Errorf("format_type(%d, -1) = %v, want %q", et.OID, ftRows, tc.want)
			}
		})
	}
}
