package catalog

import "testing"

// TestBuildIndexDefDeclaredHash pins BuildIndexDef's USING-method rendering for a
// `CREATE INDEX … USING hash` index (DU-002 slice 361). goopg has no native hash
// access method, so a hash index is built on the B-tree substrate: catalog.Index
// .Method stays "btree" and DeclaredHash records the declared method. Real
// pg_dump 18.3 / pg_get_indexdef_worker (ruleutils.c) print `USING %s` from
// pg_am.amname, so a hash index must dump `USING hash (col)`, not the substrate's
// `USING btree`. The DeclaredHash flag must therefore surface in the dump.
func TestBuildIndexDefDeclaredHash(t *testing.T) {
	tbl := &Table{Schema: "public", Name: "foo"}
	// Declared hash → USING hash regardless of the stored btree Method.
	hashIdx := &Index{
		Name:         "foo_qty_hash_idx",
		Schema:       "public",
		Table:        tbl,
		Columns:      []string{"qty"},
		Method:       "btree",
		DeclaredHash: true,
	}
	if got, want := BuildIndexDef(hashIdx), "CREATE INDEX foo_qty_hash_idx ON public.foo USING hash (qty)"; got != want {
		t.Errorf("hash BuildIndexDef = %q, want %q", got, want)
	}
	// Contrast: a plain btree index (DeclaredHash false) keeps USING btree.
	btreeIdx := &Index{
		Name:    "foo_qty_idx",
		Schema:  "public",
		Table:   tbl,
		Columns: []string{"qty"},
		Method:  "btree",
	}
	if got, want := BuildIndexDef(btreeIdx), "CREATE INDEX foo_qty_idx ON public.foo USING btree (qty)"; got != want {
		t.Errorf("btree BuildIndexDef = %q, want %q", got, want)
	}
}
