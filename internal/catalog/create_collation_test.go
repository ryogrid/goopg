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
