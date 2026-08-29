package executor

// pg_authid shared helpers — the column layout and row/format builders shared
// by the per-row heap writer (sys_pg_authid.go, B4.5) and the initdb heap
// reload (reloadRolesFromAuthidHeap). The whole-file rewriter (SyncPgAuthidFile)
// and its raw reader (ReadPgAuthidRows) were retired in B4.5: CREATE/ALTER/DROP
// ROLE now journal real pg_authid heap rows (XLOG_HEAP_INSERT/DELETE on
// global/1260) that a PG18 standby replays.
//
// The row builder duplicates bootstrapPostgresRoleWithPassword's shape
// (internal/initdb/initdb.go) — bootstrap-style rows are non-null with xmin=1
// so a real PG18 standby reading the file sees the same Form_pg_authid layout
// the byte-level regression test pins (pg_authid_heap_row_test.go). Keep the
// two in sync (sibling paths).
//
// Upstream: postgres/src/include/catalog/pg_authid.h (layout),
// postgres/src/backend/commands/user.c (CreateRole/AlterRole writing
// pg_authid), postgres/src/backend/libpq/crypt.c (rolpassword shapes).

import (
	"time"

	"github.com/goopg/goopg/internal/catalog"
)

// PGAuthidColumnsPG18 is the exported accessor for the 12-column pg_authid
// schema, used by the initdb heap reload (reloadRolesFromAuthidHeap).
func PGAuthidColumnsPG18() []catalog.Column { return pgAuthidSyncCols() }

// pgAuthidSyncCols returns the 12-column pg_authid schema
// (postgres/src/include/catalog/pg_authid.h). Twin of the slice inside
// bootstrapPostgresRoleWithPassword — keep both in sync.
func pgAuthidSyncCols() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
		{Name: "rolname", Type: catalog.Type{Name: "name"}, Ordinal: 1},
		{Name: "rolsuper", Type: catalog.Type{Name: "bool"}, Ordinal: 2},
		{Name: "rolinherit", Type: catalog.Type{Name: "bool"}, Ordinal: 3},
		{Name: "rolcreaterole", Type: catalog.Type{Name: "bool"}, Ordinal: 4},
		{Name: "rolcreatedb", Type: catalog.Type{Name: "bool"}, Ordinal: 5},
		{Name: "rolcanlogin", Type: catalog.Type{Name: "bool"}, Ordinal: 6},
		{Name: "rolreplication", Type: catalog.Type{Name: "bool"}, Ordinal: 7},
		{Name: "rolbypassrls", Type: catalog.Type{Name: "bool"}, Ordinal: 8},
		{Name: "rolconnlimit", Type: catalog.Type{Name: "int4"}, Ordinal: 9},
		{Name: "rolpassword", Type: catalog.Type{Name: "text"}, Ordinal: 10},
		{Name: "rolvaliduntil", Type: catalog.Type{Name: "timestamptz"}, Ordinal: 11},
	}
}

// buildAuthidUserRow builds a bootstrap-shaped (non-null, xmin=1) pg_authid
// row for a runtime role. Mirrors buildBootstrapRow in initdb.go.
// rolvaliduntil is encoded as a real timestamptz when validUntil parses as a
// concrete instant (DU-002 slice 439 triage item 1 follow-up — closes the
// "always NULL" gap this doc comment used to describe); PG's `infinity`/
// `-infinity` sentinels and any other unparseable literal still fall back to
// NULL, since goopg's timestamptz type has no infinity representation yet
// (see the deferral ledger) — narrower than the original gap, which lost
// every VALID UNTIL value, not just the sentinel ones.
func buildAuthidUserRow(oid int64, rolname string, super, canLogin, inherit, createDB, createRole, replication, bypassRLS bool, connLimit int32, rolpassword, validUntil string) Row {
	validUntilDatum := NullDatum
	if validUntil != "" {
		if t, err := parseCopyTimestamp(validUntil); err == nil {
			// rolvaliduntil is declared `timestamptz` by pgAuthidSyncCols above,
			// so the type is a compile-time constant here and the datum owes the
			// tag. M0119-0006 (41st slice).
			validUntilDatum = NewTimestampTZDatum(t)
		}
	}
	return Row{
		NewIntDatum(oid),
		NewStringDatum(rolname),
		NewBoolDatum(super),
		NewBoolDatum(inherit), // rolinherit (M0134-0162; was hardcoded to PG's 't' default)
		NewBoolDatum(createRole),
		NewBoolDatum(createDB),
		NewBoolDatum(canLogin),
		NewBoolDatum(replication),
		NewBoolDatum(bypassRLS),
		NewIntDatum(int64(connLimit)),
		NewStringDatum(rolpassword),
		validUntilDatum,
	}
}

// FormatValidUntilText is the exported wrapper used by the initdb heap reload
// (reloadRolesFromAuthidHeap) to render a decoded rolvaliduntil back to text.
func FormatValidUntilText(t time.Time) string { return formatValidUntilText(t) }

// formatValidUntilText renders a decoded rolvaliduntil timestamptz back into
// the same "YYYY-MM-DD HH:MM:SS[.ffffff]+00" text form extractRoleValidUntil
// captures from a `VALID UNTIL '...'` literal (goopg stores rolvaliduntil as
// UTC internally, so the zone suffix is always "+00").
func formatValidUntilText(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond() != 0 {
		return t.Format("2006-01-02 15:04:05.000000+00")
	}
	return t.Format("2006-01-02 15:04:05+00")
}
