package testport

// B4.6 Stage 2 regression guard: CREATE DATABASE ... TEMPLATE now PRESERVES the
// template relation's OID in the copy (real PG semantics), so a copied table
// and its template share the same relOid in two different databases. This test
// asserts strict WRITE isolation between them — both immediately and across a
// full restart — the failure mode of a wrong-DB resolution (a bare
// LookupTableByOID defaulting to DefaultDBOid) being a silent cross-database
// read/write. Runs on the real cluster harness (graceful Stop/Start) because
// the in-process server harness hangs on multi-DB-write shutdown (a pre-existing
// harness limitation unrelated to OID preservation).
//
// NB: this deliberately does NOT probe ALTER TABLE under collision — ALTER TABLE
// ADD COLUMN does not survive restart even in a single database (a pre-existing
// goopg gap: ADD COLUMN never syncs pg_attribute to the heap; see the deferral
// ledger), so it would fail regardless of the collision.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestPort_CreateDatabaseTemplateOIDCollisionWriteIsolation(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	c, err := cluster.New("db-template-oid-collision", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      dataDir,
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	host, port, err := splitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	openDB := func(dbname string) *sql.DB {
		t.Helper()
		dsn := fmt.Sprintf("host=%s port=%s user=postgres dbname=%s sslmode=disable", host, port, dbname)
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			t.Fatalf("open %s: %v", dbname, err)
		}
		return db
	}
	ctx := context.Background()
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

	// Build a template DB with a plain table + data, then copy it (OID preserved).
	if err := runSQLSimple(t, c, "CREATE DATABASE coll_src"); err != nil {
		t.Fatalf("CREATE DATABASE coll_src: %v", err)
	}
	src := openDB("coll_src")
	for _, stmt := range []string{
		"CREATE TABLE t(a int, b text)",
		"INSERT INTO t VALUES (1,'x'),(2,'y'),(3,'z')",
	} {
		if _, err := src.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// CREATE DATABASE ... TEMPLATE refuses while another backend is connected to
	// the source (PG's CountOtherDBBackends guard), so close src first.
	src.Close()
	if err := runSQLSimple(t, c, "CREATE DATABASE coll_copy TEMPLATE coll_src"); err != nil {
		t.Fatalf("CREATE DATABASE coll_copy TEMPLATE coll_src: %v", err)
	}
	src = openDB("coll_src")
	defer src.Close()
	cpy := openDB("coll_copy")
	defer cpy.Close()

	// Baseline: both start with the same 3 rows.
	if got := countRows(src, "src baseline"); got != 3 {
		t.Fatalf("src baseline = %d, want 3", got)
	}
	if got := countRows(cpy, "copy baseline"); got != 3 {
		t.Fatalf("copy baseline = %d, want 3", got)
	}

	// Diverge the two databases' data through writes; each must stay isolated
	// despite sharing the table's relOid.
	if _, err := cpy.ExecContext(ctx, "INSERT INTO t VALUES (100,'copy-only')"); err != nil {
		t.Fatalf("INSERT into copy: %v", err)
	}
	if _, err := cpy.ExecContext(ctx, "UPDATE t SET a = a + 1000 WHERE a = 1"); err != nil {
		t.Fatalf("UPDATE in copy: %v", err)
	}
	if _, err := src.ExecContext(ctx, "DELETE FROM t WHERE a = 3"); err != nil {
		t.Fatalf("DELETE in src: %v", err)
	}
	// Pre-restart isolation: copy has 4 rows (sum 1106), src has 2 (sum 3).
	if got := countRows(cpy, "copy pre-restart"); got != 4 {
		t.Fatalf("copy count pre-restart = %d, want 4 (write leaked cross-database!)", got)
	}
	if got := sumA(cpy, "copy sum pre-restart"); got != 1001+2+3+100 {
		t.Fatalf("copy sum(a) pre-restart = %d, want %d", got, 1001+2+3+100)
	}
	if got := countRows(src, "src pre-restart"); got != 2 {
		t.Fatalf("src count pre-restart = %d, want 2 (write leaked cross-database!)", got)
	}
	if got := sumA(src, "src sum pre-restart"); got != 1+2 {
		t.Fatalf("src sum(a) pre-restart = %d, want %d", got, 1+2)
	}
	src.Close()
	cpy.Close()

	// Restart: each database's distinct data must reload into its OWN namespace
	// even though the two tables share a relOid (audit breakage #1).
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}
	src2 := openDB("coll_src")
	defer src2.Close()
	cpy2 := openDB("coll_copy")
	defer cpy2.Close()
	if got := countRows(cpy2, "copy post-restart"); got != 4 {
		t.Fatalf("copy count post-restart = %d, want 4 (wrong-DB restore under OID collision!)", got)
	}
	if got := sumA(cpy2, "copy sum post-restart"); got != 1001+2+3+100 {
		t.Fatalf("copy sum(a) post-restart = %d, want %d (wrong-DB restore under OID collision!)", got, 1001+2+3+100)
	}
	if got := countRows(src2, "src post-restart"); got != 2 {
		t.Fatalf("src count post-restart = %d, want 2 (wrong-DB restore under OID collision!)", got)
	}
	if got := sumA(src2, "src sum post-restart"); got != 1+2 {
		t.Fatalf("src sum(a) post-restart = %d, want %d (wrong-DB restore under OID collision!)", got, 1+2)
	}
}
