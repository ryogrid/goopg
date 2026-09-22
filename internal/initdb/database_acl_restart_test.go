package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestDatabaseACLSurvivesRestartViaCatalogHeap pins the shared-catalog reload
// half of M0122-0008a. The GRANT writer already stored a PG-native aclitem[]
// in global/1262; startup must rebuild the ACL registry from it, preserving
// both the array ORDER and a grant option.
//
// The expected string is a PostgreSQL 18.3 capture, not a derivation — the
// same statement on a private cluster (2026-09-22) yields
//
//	GRANT ALL ON DATABASE d3 TO PUBLIC;
//	{=CTc/postgres,postgres=CTc/postgres}
//
// PUBLIC leads the array because a database's acldefault carries a world
// default (M0122-0008b), and the reload must preserve that ORDER, not merely
// the privilege set.
//
// The grant-option bit is deliberately NOT exercised here: PostgreSQL rejects
// `TO PUBLIC WITH GRANT OPTION` outright ("grant options can only be granted
// to roles"), goopg does not yet (filed separately), and granting to a named
// role is out of reach from this package because goopg handles CREATE ROLE in
// the postmaster rather than the grammar. The option bit is covered by
// TestDecodeACLItemArrayPreservesGrantOption in internal/executor.
func TestDatabaseACLSurvivesRestartViaCatalogHeap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "GRANT ALL ON DATABASE postgres TO PUBLIC")
	cat1 := rt1.Catalog.(*catalog.InMemory)
	want1 := "{=CTc/postgres,postgres=CTc/postgres}"
	if got, want := cat1.DatabaseACLText(cat1.DBOID()), want1; got != want {
		rt1.Close()
		t.Fatalf("datacl before restart = %q, want %q", got, want)
	}
	if err := rt1.SaveCatalog(); err != nil {
		rt1.Close()
		t.Fatal(err)
	}
	rt1.Close()

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()
	cat2 := rt2.Catalog.(*catalog.InMemory)
	if got, want := cat2.DatabaseACLText(cat2.DBOID()), want1; got != want {
		t.Errorf("datacl after restart = %q, want %q", got, want)
	}
	// Paired control: a database whose row was never granted must still
	// project as SQL NULL after the reload, so the pass above cannot come from
	// the reload inventing an array for every row it scans. (goopg's initdb
	// leaves template1's datacl NULL where PostgreSQL's seeds
	// `{=c/postgres,postgres=CTc/postgres}` — a separate, ledgered divergence
	// in initdb, not in this reload.)
	if got := cat2.DatabaseACLText(1); got != "" {
		t.Errorf("template1 datacl after restart = %q, want NULL projection", got)
	}
}
