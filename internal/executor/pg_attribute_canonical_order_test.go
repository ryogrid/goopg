package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPGAttributeColumnsPG18IsCanonical is the executor half of the M0131-S14.1
// tripwire whose other half lives in internal/initdb
// (TestPGAttributeSelfDescriptionIsPG18Canonical — that package cannot import
// this one). Both must hold: this list is what the runtime row builders
// buildUserPGAttributeRow and the composite-type builder emit positionally, and
// it has to agree byte-for-byte with the initdb seed the same heap already
// holds, or a pg_attribute page ends up with two different column orders in it.
//
// The order itself is PG18's FormData_pg_attribute
// (postgres/src/include/catalog/pg_attribute.h): no attcacheoff, and
// attstattarget at #21 between attcollation and attacl.
// pgAttributeColIdxForTest resolves a pg_attribute column to its slice index in
// a built row. Tests used to hardcode these (attoptions = 21, attstattarget =
// 24, …); M0131-S14.1 moved four of the last five and every one of those
// constants went stale at once. Look them up by name instead.
func pgAttributeColIdxForTest(t *testing.T, name string) int {
	t.Helper()
	for i, c := range pgAttributeColumnsPG18() {
		if c.Name == name {
			return i
		}
	}
	t.Fatalf("no pg_attribute column named %q", name)
	return -1
}

func TestPGAttributeColumnsPG18IsCanonical(t *testing.T) {
	want := []string{
		"attrelid", "attname", "atttypid", "attlen", "attnum",
		"atttypmod", "attndims", "attbyval", "attalign", "attstorage",
		"attcompression", "attnotnull", "atthasdef", "atthasmissing", "attidentity",
		"attgenerated", "attisdropped", "attislocal", "attinhcount", "attcollation",
		"attstattarget", "attacl", "attoptions", "attfdwoptions", "attmissingval",
	}

	got := pgAttributeColumnsPG18()
	if len(got) != len(want) {
		t.Fatalf("pgAttributeColumnsPG18 has %d columns, PG18 has %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("pgAttributeColumnsPG18 column %d = %q, want %q", i+1, got[i].Name, name)
		}
	}

	// Same list, from the decoder's side. These two disagreeing means the
	// executor writes rows one schema and reads them back under another.
	dec := catalog.PGAttributeColumns()
	if len(dec) != len(got) {
		t.Fatalf("catalog.PGAttributeColumns has %d columns, pgAttributeColumnsPG18 has %d",
			len(dec), len(got))
	}
	for i := range dec {
		if dec[i].Name != got[i].Name {
			t.Errorf("column %d: decoder says %q, row builder says %q",
				i+1, dec[i].Name, got[i].Name)
		}
	}
}
