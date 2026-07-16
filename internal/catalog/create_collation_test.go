package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCreateCollationVirtualRows verifies that a collation created via
// CreateCollation is surfaced as an extra pg_collation virtual row with the
// attributes pg_dump's getCollations / dumpCollation read, while the seven
// BKI-pinned built-ins remain present. The user row carries a user namespace
// (public=2200) and an OID >= FirstUserOID so pg_dump selects it for dump and
// skips the pg_catalog built-ins. DU-002 (M0119-0004).
func TestCreateCollationVirtualRows(t *testing.T) {
	c := NewInMemory()

	uc := &UserCollation{
		Name: "mycoll", Owner: 10, Provider: 'c', Encoding: -1,
		Collate: "C", Ctype: "C", Deterministic: true,
	}
	oid, err := c.CreateCollation(uc, "public", false)
	if err != nil {
		t.Fatalf("CreateCollation: %v", err)
	}
	if oid < FirstUserOID {
		t.Errorf("collation OID = %d, want >= %d (FirstUserOID)", oid, FirstUserOID)
	}

	pgColl, ok := c.LookupTable(parser.ObjectName{Name: "pg_collation"})
	if !ok {
		t.Fatal("pg_collation view missing")
	}
	rows := pgColl.VirtualRows()
	// 7 built-ins + 1 user collation.
	if len(rows) != 8 {
		t.Fatalf("pg_collation rows = %d, want 8 (7 builtin + 1 user)", len(rows))
	}
	var found []string
	for _, r := range rows {
		if r[1] == "mycoll" {
			found = r
		}
	}
	if found == nil {
		t.Fatal("user collation 'mycoll' not surfaced in pg_collation")
	}
	// cols: oid, collname, collnamespace, collowner, collprovider,
	//       collisdeterministic, collencoding, collcollate, collctype,
	//       colllocale, collicurules, collversion
	if found[2] != "2200" {
		t.Errorf("collnamespace = %q, want 2200 (public)", found[2])
	}
	if found[4] != "c" {
		t.Errorf("collprovider = %q, want c (libc)", found[4])
	}
	if found[5] != "t" {
		t.Errorf("collisdeterministic = %q, want t", found[5])
	}
	if found[7] != "C" || found[8] != "C" {
		t.Errorf("collcollate/collctype = %q/%q, want C/C", found[7], found[8])
	}
	// A collation with no ICU rules must surface collicurules (index 10) as SQL
	// NULL (VirtualNull), not '' — otherwise dumpCollation's ICU branch would
	// emit a spurious `, rules = ''`. Slice 392.
	if found[10] != VirtualNull {
		t.Errorf("collicurules = %q, want VirtualNull (no rules)", found[10])
	}

	// Slice 392: an ICU collation created WITH tailoring rules must surface them
	// verbatim in collicurules so dumpCollation re-emits `, rules = '...'`.
	cir := &UserCollation{
		Name: "cir", Owner: 10, Provider: 'i', Encoding: -1,
		Locale: "und", Rules: "&V << w <<< W", Deterministic: true,
	}
	if _, err := c.CreateCollation(cir, "public", false); err != nil {
		t.Fatalf("CreateCollation cir: %v", err)
	}
	var cirRow []string
	for _, r := range pgColl.VirtualRows() {
		if r[1] == "cir" {
			cirRow = r
		}
	}
	if cirRow == nil {
		t.Fatal("user collation 'cir' not surfaced in pg_collation")
	}
	if cirRow[10] != "&V << w <<< W" {
		t.Errorf("collicurules = %q, want %q", cirRow[10], "&V << w <<< W")
	}
	// The FROM path copies the source's rules too.
	if got, ok := c.CollationAttrsByName("cir"); !ok || got.Rules != "&V << w <<< W" {
		t.Errorf("CollationAttrsByName(cir).Rules = %q, ok=%v", got.Rules, ok)
	}

	// CollationAttrsByName resolves both the built-in and the user collation
	// (used by CREATE COLLATION ... FROM existing).
	if got, ok := c.CollationAttrsByName("C"); !ok || got.Collate != "C" || got.Provider != 'c' {
		t.Errorf("CollationAttrsByName(C) = %+v, ok=%v", got, ok)
	}
	if got, ok := c.CollationAttrsByName("mycoll"); !ok || got.Collate != "C" {
		t.Errorf("CollationAttrsByName(mycoll) = %+v, ok=%v", got, ok)
	}

	// CollationAttrsByName must preserve a non-deterministic flag so the
	// executor's `CREATE COLLATION ... FROM existing` path can copy it (slice
	// 391). A non-deterministic ICU source resolved by name must report
	// Deterministic=false, not the default true.
	ndet := &UserCollation{
		Name: "ci_coll", Owner: 10, Provider: 'i', Encoding: -1,
		Locale: "und-u-ks-level2", Deterministic: false,
	}
	if _, err := c.CreateCollation(ndet, "public", false); err != nil {
		t.Fatalf("CreateCollation ci_coll: %v", err)
	}
	if got, ok := c.CollationAttrsByName("ci_coll"); !ok || got.Deterministic {
		t.Errorf("CollationAttrsByName(ci_coll).Deterministic = %v, want false (ok=%v)", got.Deterministic, ok)
	}

	// Duplicate without IF NOT EXISTS errors; with IF NOT EXISTS is a no-op.
	if _, err := c.CreateCollation(&UserCollation{Name: "mycoll", Provider: 'c'}, "public", false); err == nil {
		t.Error("duplicate CreateCollation should error")
	}
	if _, err := c.CreateCollation(&UserCollation{Name: "mycoll", Provider: 'c'}, "public", true); err != nil {
		t.Errorf("CreateCollation IF NOT EXISTS on existing: %v", err)
	}
}

// TestDropCollation verifies DropCollation removes a user-created collation
// from the registry (and its pg_collation virtual row) and reports
// found/not-found correctly. Before M0119-0004's DROP COLLATION slice, no
// DropCollation method existed at all, so `DROP COLLATION <name>` on an
// existing user collation always reported "does not exist" — this pins the
// fix.
func TestDropCollation(t *testing.T) {
	c := NewInMemory()
	uc := &UserCollation{
		Name: "mycoll", Owner: 10, Provider: 'c', Encoding: -1,
		Collate: "C", Ctype: "C", Deterministic: true,
	}
	if _, err := c.CreateCollation(uc, "public", false); err != nil {
		t.Fatalf("CreateCollation: %v", err)
	}

	// A different (schema, name) pair must not match. Register a real second
	// schema — an unregistered schema name resolves to public (same fallback
	// as CreateCollation), which would falsely match.
	c.RegisterSchema("other_schema")
	if c.DropCollation("mycoll", "other_schema") {
		t.Error("DropCollation matched across a different schema")
	}
	if c.DropCollation("nonexistent", "public") {
		t.Error("DropCollation reported found for a name that was never created")
	}

	if !c.DropCollation("mycoll", "public") {
		t.Fatal("DropCollation(mycoll, public) = false, want true")
	}
	if _, ok := c.CollationAttrsByName("mycoll"); ok {
		t.Error("mycoll still resolvable via CollationAttrsByName after DropCollation")
	}
	// A second drop of the same name is a no-op reporting not-found (mirrors
	// DropConversion/DropCast/DropTransform).
	if c.DropCollation("mycoll", "public") {
		t.Error("DropCollation on an already-dropped collation reported true")
	}

	// The virtual pg_collation view drops back to only the 7 built-ins.
	pgColl, ok := c.LookupTable(parser.ObjectName{Name: "pg_collation"})
	if !ok {
		t.Fatal("pg_collation view missing")
	}
	if rows := pgColl.VirtualRows(); len(rows) != 7 {
		t.Errorf("pg_collation rows after drop = %d, want 7 (builtins only)", len(rows))
	}

	// A built-in collation is never in userCollations, so DropCollation on one
	// always reports not-found (PG likewise refuses to drop a pinned row).
	if c.DropCollation("C", "public") {
		t.Error("DropCollation on a built-in collation reported true")
	}
}

// TestCreateCollationCrossDatabaseIsolation verifies that CREATE COLLATION
// under one database does not collide with a same-named, same-schema
// collation already created under a different database — the specific gap
// the DU-002 dump+restore round-trip probe (TestPort_PgDumpConnectionSetup)
// hit: restoring a dump into a fresh database re-issues `CREATE COLLATION
// public.builtin_coll ...`, which previously errored "already exists"
// because every UserCollation shared one flat, dbOid-less registry. Mirrors
// the ForeignServer/UserMapping cross-database isolation precedent.
// M0122-0007 4e follow-up (DU-002 round-trip probe unblock).
func TestCreateCollationCrossDatabaseIsolation(t *testing.T) {
	c := NewInMemory()
	const otherDB = uint32(99999)

	first := &UserCollation{Name: "builtin_coll", Provider: 'b', Encoding: -1, Locale: "C", Deterministic: true}
	if _, err := c.CreateCollation(first, "public", false, DefaultDBOid); err != nil {
		t.Fatalf("CreateCollation under DefaultDBOid: %v", err)
	}

	// The exact same (schema, name) under a distinct database must NOT
	// collide, unlike a genuine same-database duplicate.
	second := &UserCollation{Name: "builtin_coll", Provider: 'b', Encoding: -1, Locale: "C", Deterministic: true}
	if _, err := c.CreateCollation(second, "public", false, otherDB); err != nil {
		t.Fatalf("CreateCollation under a distinct dbOid falsely collided: %v", err)
	}

	// A genuine same-database duplicate still errors (regression guard for
	// the existing single-database behavior).
	if _, err := c.CreateCollation(&UserCollation{Name: "builtin_coll", Provider: 'b'}, "public", false, DefaultDBOid); err == nil {
		t.Error("same-database duplicate CreateCollation should still error")
	}

	// Each database's pg_collation view sees only its own row, plus the
	// shared builtins.
	rowsDefault := c.PGCollationRowsForDBOid(DefaultDBOid)
	rowsOther := c.PGCollationRowsForDBOid(otherDB)
	if len(rowsDefault) != 8 {
		t.Errorf("PGCollationRowsForDBOid(DefaultDBOid) rows = %d, want 8 (7 builtin + 1 user)", len(rowsDefault))
	}
	if len(rowsOther) != 8 {
		t.Errorf("PGCollationRowsForDBOid(otherDB) rows = %d, want 8 (7 builtin + 1 user)", len(rowsOther))
	}

	// Dropping under one database must not remove the other's row.
	if !c.DropCollation("builtin_coll", "public", DefaultDBOid) {
		t.Fatal("DropCollation(builtin_coll) under DefaultDBOid = false, want true")
	}
	if _, ok := c.CollationAttrsByName("builtin_coll", otherDB); !ok {
		t.Error("otherDB's builtin_coll vanished after dropping DefaultDBOid's own copy")
	}
	if _, ok := c.CollationAttrsByName("builtin_coll", DefaultDBOid); ok {
		t.Error("DefaultDBOid's builtin_coll still resolvable after DropCollation")
	}
}
