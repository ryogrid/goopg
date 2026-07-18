package server

// B4.6 Stage 2 probe: with OID preservation (copyTemplateTables now keeps the
// template relation's OID), a copied table and its template share the same
// relOid in two different databases. This test exercises the WRITE path and a
// few OID-reverse-lookup operations against the copy and asserts strict
// isolation from the template — the failure mode of a wrong-DB resolution
// (LookupTableByOID defaulting to DefaultDBOid) is a silent cross-database
// read/write, which these assertions catch.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

func TestCreateDatabaseTemplateOIDCollisionWriteIsolation(t *testing.T) {
	// B4.6 Stage 2 RED-gate: this test only exercises a real OID collision once
	// copyTemplateTables PRESERVES the template's relation OIDs (the "flip" in
	// database_ddl.go). With the flip applied, steps (1)-(4) PASS (the common
	// SELECT/DML/ALTER paths resolve by name and are already per-dbOid correct)
	// but step (5) FAILS: ALTER-then-restart in the copy drops the added column
	// because the write-side catalog-heap persist routes pg_attribute by OID
	// defaulting to DefaultDBOid under collision. Un-skip when the write-side
	// dbOid routing is fixed. Without the flip, fresh OIDs mean no collision, so
	// the test would pass vacuously — skip rather than assert nothing.
	t.Skip("B4.6 Stage 2: requires the OID-preservation flip + write-side dbOid routing fix; see .ralph/deferral_ledger.md")

	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}
	ctx := context.Background()
	s1 := startDBIDRestartServer(t, dir)
	defer s1.close(t)

	pg := s1.open(t, "postgres")
	defer pg.Close()
	if _, err := pg.ExecContext(ctx, "CREATE DATABASE coll_src"); err != nil {
		t.Fatalf("CREATE DATABASE coll_src: %v", err)
	}
	src := s1.open(t, "coll_src")
	defer src.Close()
	for _, stmt := range []string{
		"CREATE TABLE t(a int, b text)",
		"INSERT INTO t VALUES (1,'x'),(2,'y'),(3,'z')",
	} {
		if _, err := src.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := pg.ExecContext(ctx, "CREATE DATABASE coll_copy TEMPLATE coll_src"); err != nil {
		t.Fatalf("CREATE DATABASE coll_copy TEMPLATE coll_src: %v", err)
	}
	cpy := s1.open(t, "coll_copy")
	defer cpy.Close()

	countRows := func(db *sql.DB, label string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM t").Scan(&n); err != nil {
			t.Fatalf("%s: count: %v", label, err)
		}
		return n
	}
	sumA := func(db *sql.DB, label string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT coalesce(sum(a),0) FROM t").Scan(&n); err != nil {
			t.Fatalf("%s: sum: %v", label, err)
		}
		return n
	}

	// Baseline: both databases start with the same 3 rows (sum = 6).
	if got := countRows(src, "src baseline"); got != 3 {
		t.Fatalf("src baseline count = %d, want 3", got)
	}
	if got := countRows(cpy, "copy baseline"); got != 3 {
		t.Fatalf("copy baseline count = %d, want 3", got)
	}

	// (1) INSERT into the copy must NOT touch the source.
	if _, err := cpy.ExecContext(ctx, "INSERT INTO t VALUES (100,'copy-only')"); err != nil {
		t.Fatalf("INSERT into copy: %v", err)
	}
	if got := countRows(cpy, "copy after insert"); got != 4 {
		t.Fatalf("copy count after insert = %d, want 4", got)
	}
	if got := countRows(src, "src after copy insert"); got != 3 {
		t.Fatalf("src count after COPY insert = %d, want 3 (INSERT leaked cross-database!)", got)
	}

	// (2) UPDATE in the copy must NOT touch the source.
	if _, err := cpy.ExecContext(ctx, "UPDATE t SET a = a + 1000 WHERE a = 1"); err != nil {
		t.Fatalf("UPDATE in copy: %v", err)
	}
	if got := sumA(src, "src after copy update"); got != 6 {
		t.Fatalf("src sum(a) after COPY update = %d, want 6 (UPDATE leaked cross-database!)", got)
	}

	// (3) DELETE in the source must NOT touch the copy.
	if _, err := src.ExecContext(ctx, "DELETE FROM t WHERE a = 3"); err != nil {
		t.Fatalf("DELETE in src: %v", err)
	}
	if got := countRows(src, "src after delete"); got != 2 {
		t.Fatalf("src count after delete = %d, want 2", got)
	}
	if got := countRows(cpy, "copy after src delete"); got != 4 {
		t.Fatalf("copy count after SRC delete = %d, want 4 (DELETE leaked cross-database!)", got)
	}

	// (4) An OID-reverse-lookup path: tableoid::regclass must resolve to the
	// copy's own relation, and ALTER TABLE (which touches the catalog by OID)
	// must apply to the copy only.
	if _, err := cpy.ExecContext(ctx, "ALTER TABLE t ADD COLUMN c int DEFAULT 7"); err != nil {
		t.Fatalf("ALTER TABLE on copy: %v", err)
	}
	// The copy sees the new column; the source must not.
	var cSum int
	if err := cpy.QueryRowContext(ctx, "SELECT coalesce(sum(c),0) FROM t").Scan(&cSum); err != nil {
		t.Fatalf("copy sum(c): %v", err)
	}
	if cSum != 4*7 {
		t.Fatalf("copy sum(c) = %d, want %d (4 rows × default 7)", cSum, 4*7)
	}
	if _, err := src.ExecContext(ctx, "SELECT c FROM t"); err == nil {
		t.Fatal("src has column c after ALTER on the COPY (catalog change leaked cross-database!)")
	}

	// (5) Durability under collision: after a full restart the two databases'
	// distinct states (copy: 4 rows + column c; src: 2 rows, no c) must be
	// restored into their OWN namespaces — this exercises the per-DB catalog
	// reload with two tables sharing a relOid (audit breakage #1).
	cpy.Close()
	src.Close()
	pg.Close()
	s1.close(t)

	s2 := startDBIDRestartServer(t, dir)
	defer s2.close(t)
	src2 := s2.open(t, "coll_src")
	defer src2.Close()
	cpy2 := s2.open(t, "coll_copy")
	defer cpy2.Close()
	if got := countRows(src2, "src post-restart"); got != 2 {
		t.Fatalf("src count post-restart = %d, want 2 (wrong-DB restore under OID collision!)", got)
	}
	if got := countRows(cpy2, "copy post-restart"); got != 4 {
		t.Fatalf("copy count post-restart = %d, want 4 (wrong-DB restore under OID collision!)", got)
	}
	// The column c must have survived only in the copy.
	var cSum2 int
	if err := cpy2.QueryRowContext(ctx, "SELECT coalesce(sum(c),0) FROM t").Scan(&cSum2); err != nil {
		t.Fatalf("copy sum(c) post-restart: %v", err)
	}
	if cSum2 != 4*7 {
		t.Fatalf("copy sum(c) post-restart = %d, want %d", cSum2, 4*7)
	}
	if _, err := src2.ExecContext(ctx, "SELECT c FROM t"); err == nil {
		t.Fatal("src has column c post-restart (wrong-DB catalog restore under OID collision!)")
	}
}
