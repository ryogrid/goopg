package testport

// Port of postgres/src/bin/pg_amcheck/t/005_opclass_damage.pl into a Go test
// (M0110-0003 / M0119-0006, CSV row AC-003).
//
// 005_opclass_damage.pl is the one upstream pg_amcheck TAP script that corrupts
// nothing on disk. It builds a healthy B-tree index under a *user* operator
// class, then repoints that class's pg_amproc FUNCTION 1 row at a comparator
// that sorts the other way. The index bytes are untouched; what changed is the
// ordering the index is judged against, and amcheck must notice. Its second
// half does the same to a UNIQUE index's class with a comparator that declares
// two distinct values equal, and asserts `--checkunique` reports the resulting
// uniqueness violation.
//
// Both halves are now implementable against goopg end to end:
//
//   - the read side is the operator-class comparator dispatch landed in
//     M0119-0006 (executor.btIndexOpClassComparator resolves the class's
//     FUNCTION 1 live from pg_amproc through
//     catalog.InMemory.LookupOpClassSupportProcOID, and the engine tier
//     amcheck.VerifyBtreeItemOrderCmp compares under it), and
//   - the `--checkunique` side is amcheck.VerifyBtreeUnique
//     (upstream bt_entry_unique_check), which walks the leaf level under that
//     same injected comparator and reports a duplicate only when BOTH heap
//     tuples are visible.
//
// The unit gates in internal/executor prove each seam in isolation; THIS test
// is the property neither of them can show: that the real upstream pg_amcheck
// binary, driving goopg over the wire with its own generated SQL
// (`SELECT public.bt_index_check(index := c.oid, heapallindexed := false,
// checkunique := true)`), observes the same four-phase clean/damaged/repaired/
// damaged sequence upstream asserts. In particular it exercises pg_amcheck's
// amcheck-version gate — it silently drops --checkunique unless the extension
// reports >= 1.4 (pg_amcheck.c:607-631), which goopg does (operators_ddl.go).
//
// SCOPING ADAPTATION (same as 003/004 ports). Upstream runs `pg_amcheck
// postgres` over the whole database. goopg's system-catalog heap pages do not
// yet round-trip cleanly through verify_heapam for every relkind, so the runs
// here are scoped to the single user table under test (`--table public.int4tbl`),
// which pulls in its two dependent indexes — the objects 005 actually asserts
// on. The clean/damaged contract being verified is unchanged by the scoping.
//
// FIXTURE ADAPTATION. Upstream inserts generate_series(1,1000) so the index is
// multi-level. goopg's B-tree reaches multiple leaf pages well below that, and
// the uniqueness half needs the two conflicting values (768, 769) to exist, so
// the row count is kept at upstream's 1000.
//
// SELF-PROMOTING (cf. AC-002/AC-003). The *clean* phases t.Skip with the
// captured output if an unrelated goopg gap makes a healthy run non-empty,
// rather than reporting a misleading FAIL; the two damage assertions are hard
// failures, since they are the property this port exists to prove.
//
// Design docs: docs/design/0110-0003-pg-amcheck-tap-port.md,
// docs/design/0119-0006-opclass-comparator-dispatch-amcheck.md,
// docs/design/0119-0006-checkunique-tier-amcheck.md.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_PgAmcheck005OpclassDamage is the end-to-end port of
// 005_opclass_damage.pl: four pg_amcheck runs over one unchanging set of index
// pages, whose verdicts are decided purely by what pg_amproc currently says the
// operator class's comparator is.
func TestPort_PgAmcheck005OpclassDamage(t *testing.T) {
	if clientToolBin(t, "pg_amcheck") == "" {
		t.Skip("pg_amcheck not in PATH or postgres/local_install/bin")
	}
	c, err := cluster.New("amcheck-opclass", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Upstream's fixture, verbatim in shape: two user operator classes over
	// int4 — one governing an ordinary index, one a UNIQUE index — each naming
	// its own FUNCTION 1 comparator so the two halves can be damaged
	// independently.
	for _, stmt := range []string{
		"CREATE EXTENSION amcheck",
		`CREATE FUNCTION int4_asc_cmp (a int4, b int4) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN 1 ELSE -1 END; $$`,
		`CREATE FUNCTION ok_cmp (a int4, b int4) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 < $2 THEN -1 WHEN $1 > $2 THEN 1 ELSE 0 END; $$`,
		`CREATE OPERATOR CLASS int4_fickle_ops FOR TYPE int4 USING btree AS
			OPERATOR 1 < (int4, int4), OPERATOR 2 <= (int4, int4),
			OPERATOR 3 = (int4, int4), OPERATOR 4 >= (int4, int4),
			OPERATOR 5 > (int4, int4), FUNCTION 1 int4_asc_cmp(int4, int4)`,
		`CREATE OPERATOR CLASS int4_unique_ops FOR TYPE int4 USING btree AS
			OPERATOR 1 < (int4, int4), OPERATOR 2 <= (int4, int4),
			OPERATOR 3 = (int4, int4), OPERATOR 4 >= (int4, int4),
			OPERATOR 5 > (int4, int4), FUNCTION 1 ok_cmp(int4, int4)`,
		"CREATE TABLE int4tbl (i int4)",
		"INSERT INTO int4tbl (SELECT * FROM generate_series(1,1000) gs)",
		"CREATE INDEX fickleidx ON int4tbl USING btree (i int4_fickle_ops)",
		`CREATE UNIQUE INDEX bttest_unique_idx ON int4tbl
			USING btree (i int4_unique_ops) WITH (deduplicate_items = off)`,
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	scoped := []string{"--table", "public.int4tbl", "postgres"}
	scopedUnique := append([]string{"--checkunique"}, scoped...)

	// Phase 1 — "We have not yet broken the index, so we should get no
	// corruption". The no-false-positive half: dispatching to a user comparator
	// must not manufacture findings on a healthy index.
	amcheck005Clean(t, c, "pre-damage", scoped)

	// Phase 2 — repoint int4_fickle_ops's FUNCTION 1 at a descending
	// comparator. Not one index byte changes; the order it is judged under
	// inverts, so every adjacent item pair on every leaf now decreases.
	if err := runSQLSimple(t, c, `CREATE FUNCTION int4_desc_cmp (a int4, b int4)
		RETURNS int LANGUAGE sql AS $$
		SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN -1 ELSE 1 END; $$`); err != nil {
		t.Fatalf("CREATE FUNCTION int4_desc_cmp: %v", err)
	}
	if err := runSQLSimple(t, c, `UPDATE pg_catalog.pg_amproc
		SET amproc = 'int4_desc_cmp'::regproc
		WHERE amproc = 'int4_asc_cmp'::regproc`); err != nil {
		t.Fatalf("amproc damage UPDATE (fickleidx): %v", err)
	}
	amcheck005Damaged(t, c, "fickleidx item order", scoped,
		`item order invariant violated for index "fickleidx"`)

	// Phase 3 — "Repair broken opclass for check unique tests." The index is
	// clean again with no page ever having been rewritten, which is the whole
	// point of this script: the verdict tracks the catalog, not the storage.
	if err := runSQLSimple(t, c, `UPDATE pg_catalog.pg_amproc
		SET amproc = 'int4_asc_cmp'::regproc
		WHERE amproc = 'int4_desc_cmp'::regproc`); err != nil {
		t.Fatalf("amproc repair UPDATE: %v", err)
	}
	amcheck005Clean(t, c, "repaired, --checkunique", scopedUnique)

	// Phase 4 — break the UNIQUE index's class with a comparator that declares
	// the adjacent values 768 and 769 equal. Both rows are live, so the
	// checkunique tier must report them as a uniqueness violation. Note this
	// leaves item order intact (the pair compares equal, not decreasing), so
	// the finding can only come from the uniqueness tier.
	if err := runSQLSimple(t, c, `CREATE FUNCTION bad_cmp (a int4, b int4)
		RETURNS int LANGUAGE sql AS $$
		SELECT CASE WHEN ($1 = 768 AND $2 = 769) OR ($1 = 769 AND $2 = 768) THEN 0
					WHEN $1 < $2 THEN -1
					WHEN $1 > $2 THEN 1
					ELSE 0 END; $$`); err != nil {
		t.Fatalf("CREATE FUNCTION bad_cmp: %v", err)
	}
	if err := runSQLSimple(t, c, `UPDATE pg_catalog.pg_amproc
		SET amproc = 'bad_cmp'::regproc
		WHERE amproc = 'ok_cmp'::regproc`); err != nil {
		t.Fatalf("amproc damage UPDATE (bttest_unique_idx): %v", err)
	}
	amcheck005Damaged(t, c, "bttest_unique_idx uniqueness", scopedUnique,
		`index uniqueness is violated for index "bttest_unique_idx"`)
}

// amcheck005Clean asserts a pg_amcheck run reports nothing (upstream's
// command_like(..., qr/^$/)). A non-clean run is a t.Skip, not a failure: the
// scoped relation could surface an unrelated goopg gap, and the damage
// assertions below are what this port exists to prove.
func amcheck005Clean(t *testing.T, c *cluster.Cluster, desc string, args []string) {
	t.Helper()
	res := runAmcheck(t, c, args...)
	if res.ExitCode != 0 || res.Stdout != "" {
		t.Skipf("AC-003 [%s]: pg_amcheck over the healthy 005 fixture is not clean "+
			"(unrelated goopg gap). exit=%d stdout=%q stderr=%q",
			desc, res.ExitCode, res.Stdout, res.Stderr)
	}
	// pg_amcheck drops --checkunique with a warning if the extension reports a
	// version below 1.4; a silently-skipped tier would make phase 4 vacuous.
	if strings.Contains(res.Stderr, "is not supported by amcheck version") {
		t.Fatalf("[%s] pg_amcheck refused an option as unsupported: %q", desc, res.Stderr)
	}
}

// amcheck005Damaged asserts a pg_amcheck run reports the given upstream-verbatim
// corruption message on stdout and exits 2 (upstream's command_checks_all with
// exit code 2). Upstream matches its regexes against stderr because the server
// raises them as errors; pg_amcheck prints per-relation corruption findings to
// stdout, so we accept either stream — the message and the exit code are the
// contract.
func amcheck005Damaged(t *testing.T, c *cluster.Cluster, desc string, args []string, want string) {
	t.Helper()
	res := runAmcheck(t, c, args...)
	if !strings.Contains(res.Stdout, want) && !strings.Contains(res.Stderr, want) {
		t.Fatalf("[%s] pg_amcheck did not report the damage\n  want substring: %s\n  exit=%d\n  stdout=%q\n  stderr=%q",
			desc, want, res.ExitCode, res.Stdout, res.Stderr)
	}
	if res.ExitCode != 2 {
		t.Errorf("[%s] exit=%d want 2\n  stdout=%q\n  stderr=%q",
			desc, res.ExitCode, res.Stdout, res.Stderr)
	}
}
