package postmaster

import (
	"io"
	"log/slog"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// TestTypeOIDForRegtype covers the M0122-0005 pg_typeof()::oid follow-up's
// wire-protocol RowDescription TypeOID: a "regtype"-typed column (pg_typeof()'s
// declared SQL return type) must advertise the real regtype OID (2206), not
// fall through to the text default (25).
func TestTypeOIDForRegtype(t *testing.T) {
	if got := typeOIDFor(catalog.Type{Name: "regtype"}); got != catalog.OIDRegtype {
		t.Errorf("typeOIDFor(regtype) = %d, want %d", got, catalog.OIDRegtype)
	}
}

// TestAppendTypedCellTextRegtypeRendersName pins the M0122-0005
// pg_typeof()::oid follow-up: pg_typeof() now evaluates to a KindInt Datum
// holding the resolved type's OID (mirroring regclass/regproc, so a further
// `::oid` cast is a binary-compatible reinterpretation instead of misparsing
// display text) — the wire-output layer must render that OID back to the
// type's display name for a plain `SELECT pg_typeof(...)`, exactly like the
// pre-existing regclass/regproc cases in this same switch. InvalidOid (0)
// renders "-", and the "unknown" pseudo-type OID (705, UNKNOWNOID) renders
// "unknown", both matching regtypeout (postgres/src/backend/utils/adt/regproc.c).
// A nil getSetting (no session) falls back to the default search_path
// ("$user", public), under which a public-schema user type is visible and
// therefore renders unqualified — see TestAppendTypedCellTextRegtypeSchemaQualification
// below for the search_path=''-qualifies / explicit-non-public-path-qualifies cases.
func TestAppendTypedCellTextRegtypeRendersName(t *testing.T) {
	cat := catalog.NewInMemory()
	et, err := cat.RegisterEnum("mood", []string{"sad", "ok", "happy"})
	if err != nil {
		t.Fatalf("RegisterEnum: %v", err)
	}

	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: cat,
	})

	regType := catalog.Type{Name: "regtype"}
	tests := []struct {
		name string
		oid  int64
		want string
	}{
		{"invalid oid renders dash", 0, "-"},
		{"unknown pseudo-type", 705, "unknown"},
		{"built-in integer", 23, "integer"},
		{"built-in bigint", 20, "bigint"},
		{"built-in numeric", 1700, "numeric"},
		{"built-in text", 25, "text"},
		{"user-defined enum resolves via catalog, unqualified under default search_path", int64(et.OID), "mood"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(srv.appendTypedCellText(nil, executor.NewIntDatum(tt.oid), regType, nil))
			if got != tt.want {
				t.Errorf("appendTypedCellText(oid=%d, regtype) = %q, want %q", tt.oid, got, tt.want)
			}
		})
	}
}

// TestAppendTypedCellTextRegtypeSchemaQualification pins the schema-
// visibility fix itself: a resolved user-defined type name is only
// schema-qualified with "public." when "public" is NOT visible on the
// effective search_path (e.g. pg_dump's search_path=''), matching real
// PostgreSQL's regtypeout/format_type instead of unconditionally prefixing
// "public." regardless of search_path — the gap recorded in the 2026-07-06
// pg_typeof()::oid deferral-ledger row.
func TestAppendTypedCellTextRegtypeSchemaQualification(t *testing.T) {
	cat := catalog.NewInMemory()
	et, err := cat.RegisterEnum("mood", []string{"sad", "ok", "happy"})
	if err != nil {
		t.Fatalf("RegisterEnum: %v", err)
	}

	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: cat,
	})
	regType := catalog.Type{Name: "regtype"}

	tests := []struct {
		name       string
		searchPath string
		want       string
	}{
		{"search_path='' (pg_dump) qualifies", "", "public.mood"},
		{"search_path without public qualifies", "other_schema", "public.mood"},
		{"search_path with public unqualified", `"$user", public`, "mood"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getSetting := func(name string) (string, bool) {
				if name == "search_path" {
					return tt.searchPath, true
				}
				return "", false
			}
			got := string(srv.appendTypedCellText(nil, executor.NewIntDatum(int64(et.OID)), regType, getSetting))
			if got != tt.want {
				t.Errorf("appendTypedCellText(search_path=%q) = %q, want %q", tt.searchPath, got, tt.want)
			}
		})
	}
}
