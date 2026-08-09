package executor

// End-to-end tests for the bt_index_check / bt_index_parent_check scalar
// functions (slice S4 of docs/design/0110-0008). These drive the full parse ->
// plan -> execute stack so they exercise the catalog/storage plumbing this
// adapter owns; the B-tree corruption-detection logic itself is unit-tested in
// internal/amcheck (verify_nbtree*.go). The load-bearing assertion is the
// no-false-positive case: a healthy index must NOT raise through the whole
// pipeline (false positives on healthy relations are this project's most
// expensive failure mode).

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

func btIndexCheckSetup(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, cleanup := newVMFixture(t)
	for _, stmt := range []string{
		"CREATE TABLE bic (a int, b text)",
		"INSERT INTO bic VALUES (1, 'one')",
		"INSERT INTO bic VALUES (2, 'two')",
		"INSERT INTO bic VALUES (3, 'three')",
		"INSERT INTO bic VALUES (4, 'four')",
		"INSERT INTO bic VALUES (5, 'five')",
		"CREATE INDEX bic_a_idx ON bic (a)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)
	return ctx, cleanup
}

// TestBtIndexCheck_CleanIndexNoError is the no-false-positive gate: a healthy
// index must verify cleanly through both functions and every call shape
// pg_amcheck issues positionally.
func TestBtIndexCheck_CleanIndexNoError(t *testing.T) {
	ctx, cleanup := btIndexCheckSetup(t)
	defer cleanup()

	for _, sql := range []string{
		"SELECT bt_index_check('bic_a_idx')",
		"SELECT bt_index_parent_check('bic_a_idx')",
		// heapallindexed / rootdescend / checkunique args accepted (deferred tiers).
		"SELECT bt_index_check('bic_a_idx', false)",
		"SELECT bt_index_parent_check('bic_a_idx', false, false, false)",
	} {
		if _, err := runQueryWithErr(ctx, sql); err != nil {
			t.Errorf("%s: clean index raised: %v", sql, err)
		}
	}
}

// TestBtIndexCheck_SchemaQualifiedDispatch is the M0110-0003 AC-003 blocker #2
// regression: pg_amcheck qualifies the amcheck scalar builtins with the
// extension's install schema (e.g. `"public".bt_index_check(...)`), not
// pg_catalog. evalFuncCall must strip that user-schema qualifier and still
// dispatch the builtin — otherwise any table with a dependent index 42883s
// ("function public.bt_index_check does not exist"). Mirrors the FROM-clause
// SRF schema-strip for verify_heapam.
func TestBtIndexCheck_SchemaQualifiedDispatch(t *testing.T) {
	ctx, cleanup := btIndexCheckSetup(t)
	defer cleanup()

	for _, sql := range []string{
		`SELECT public.bt_index_check('bic_a_idx')`,
		`SELECT "public".bt_index_check('bic_a_idx', false)`,
		`SELECT public.bt_index_parent_check('bic_a_idx')`,
		`SELECT "public".bt_index_parent_check('bic_a_idx', false, false, false)`,
		// The exact named-argument shape pg_amcheck emits (schema-qualified by
		// the amcheck install schema, `:=` legacy named-arg spelling).
		`SELECT public.bt_index_check(index := 'bic_a_idx', heapallindexed := false)`,
		`SELECT "public".bt_index_parent_check(index := 'bic_a_idx', heapallindexed := false, rootdescend := false)`,
	} {
		if _, err := runQueryWithErr(ctx, sql); err != nil {
			t.Errorf("%s: schema-qualified clean index raised: %v", sql, err)
		}
	}
}

// TestBtIndexCheck_DetectsCorruptMetapage clobbers the metapage magic and
// confirms both functions raise (ERRCODE_INDEX_CORRUPTED) — the engine's
// "meta page is corrupt" finding propagated through the SQL surface as an error.
func TestBtIndexCheck_DetectsCorruptMetapage(t *testing.T) {
	ctx, cleanup := btIndexCheckSetup(t)
	defer cleanup()

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatal("expected in-memory catalog")
	}
	idx, ok := im.LookupIndex(parser.ObjectName{Name: "bic_a_idx"})
	if !ok {
		t.Fatal("index bic_a_idx not found")
	}
	rel := ctx.Catalog.IndexRelFileNode(idx)

	// Overwrite the metapage magic (offset 0 of the meta payload) with garbage.
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: btree.MetaBlock})
	if err != nil {
		t.Fatalf("pin metapage: %v", err)
	}
	off := storage.SizeOfPageHeaderData
	binary.LittleEndian.PutUint32(s.Page()[off:off+4], 0xDEADBEEF)
	ctx.Pool.MarkDirty(s)
	ctx.Pool.Unpin(s)

	for _, sql := range []string{
		"SELECT bt_index_check('bic_a_idx')",
		"SELECT bt_index_parent_check('bic_a_idx')",
	} {
		if _, err := runQueryWithErr(ctx, sql); err == nil {
			t.Errorf("%s: corrupt metapage not detected", sql)
		}
	}
}

// TestBtIndexCheck_NonexistentIndex confirms an unknown relation argument raises
// rather than silently succeeding.
func TestBtIndexCheck_NonexistentIndex(t *testing.T) {
	ctx, cleanup := btIndexCheckSetup(t)
	defer cleanup()

	if _, err := runQueryWithErr(ctx, "SELECT bt_index_check('no_such_index')"); err == nil {
		t.Fatal("nonexistent index did not raise")
	}
}

// TestBtIndexCheck_DetectsMissingRelationFork is the M0110-0003 file-removal
// corruption tier: it mirrors upstream bt_index_check_callback's
// smgrexists(MAIN_FORKNUM) guard (verify_nbtree.c:318) and pg_amcheck
// 003_check.pl's "reports missing main relation fork" assertion. An index whose
// backing file has been removed on disk must raise ERRCODE_INDEX_CORRUPTED with
// the verbatim "lacks a main relation fork" message — it must NOT be silently
// recreated as an empty (and therefore falsely "clean") index by NBlocks'
// O_CREATE open. Covers both bt_index_check and bt_index_parent_check.
func TestBtIndexCheck_DetectsMissingRelationFork(t *testing.T) {
	ctx, cleanup := btIndexCheckSetup(t)
	defer cleanup()

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatal("expected in-memory catalog")
	}
	idx, ok := im.LookupIndex(parser.ObjectName{Name: "bic_a_idx"})
	if !ok {
		t.Fatal("index bic_a_idx not found")
	}
	rel := ctx.Catalog.IndexRelFileNode(idx)

	// Remove the index's backing fork file out from under the engine, mimicking
	// the on-disk file removal pg_amcheck's 003_check performs while stopped.
	if err := ctx.Pool.Manager().DropRelation(rel); err != nil {
		t.Fatalf("drop index fork: %v", err)
	}

	for _, sql := range []string{
		"SELECT bt_index_check('bic_a_idx')",
		"SELECT bt_index_parent_check('bic_a_idx')",
	} {
		_, err := runQueryWithErr(ctx, sql)
		if err == nil {
			t.Errorf("%s: missing relation fork not detected", sql)
			continue
		}
		if !strings.Contains(err.Error(), "lacks a main relation fork") {
			t.Errorf("%s: got %q, want substring %q", sql, err.Error(), "lacks a main relation fork")
		}
	}
}

// TestBtIndexCheck_OpClassDamageDetected is the M0119-0006 gate for
// operator-class comparator dispatch: the mechanism pg_amcheck's upstream
// 005_opclass_damage.pl is built on.
//
// A B-tree index declared with a *user* operator class must be verified through
// that class's FUNCTION 1 support proc, resolved live from pg_amproc — not
// through the engine's own key-byte order. The test reproduces the upstream
// scenario end to end: build the index under an ascending comparator (clean),
// then repoint the pg_amproc row at a comparator that sorts descending. The
// index bytes never change; the check must nonetheless now report
// `item order invariant violated for index "fickleidx"`, because the ordering
// it is judged against did.
//
// The no-false-positive half (the clean pre-damage check) is the load-bearing
// assertion of the pair: dispatching to a user comparator must not manufacture
// findings on a healthy index.
func TestBtIndexCheck_OpClassDamageDetected(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE FUNCTION int4_asc_cmp (a int4, b int4) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN 1 ELSE -1 END; $$`,
		`CREATE FUNCTION int4_desc_cmp (a int4, b int4) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN -1 ELSE 1 END; $$`,
		`CREATE OPERATOR CLASS int4_fickle_ops FOR TYPE int4 USING btree AS
			OPERATOR 1 < (int4, int4), OPERATOR 2 <= (int4, int4),
			OPERATOR 3 = (int4, int4), OPERATOR 4 >= (int4, int4),
			OPERATOR 5 > (int4, int4), FUNCTION 1 int4_asc_cmp(int4, int4)`,
		"CREATE TABLE int4tbl (i int4)",
		"INSERT INTO int4tbl VALUES (1), (2), (3), (4), (5), (6), (7), (8)",
		"CREATE INDEX fickleidx ON int4tbl USING btree (i int4_fickle_ops)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	if _, err := runQueryWithErr(ctx, "SELECT bt_index_check('fickleidx')"); err != nil {
		t.Fatalf("healthy index under a user operator class raised: %v", err)
	}

	// Upstream's corruption injection, verbatim in shape.
	if err := runDDL(t, ctx, `UPDATE pg_catalog.pg_amproc
		SET amproc = 'int4_desc_cmp'::regproc
		WHERE amproc = 'int4_asc_cmp'::regproc`); err != nil {
		t.Fatalf("amproc damage UPDATE: %v", err)
	}

	_, err := runQueryWithErr(ctx, "SELECT bt_index_check('fickleidx')")
	if err == nil {
		t.Fatal("broken operator class not detected: bt_index_check reported clean")
	}
	const want = `item order invariant violated for index "fickleidx"`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("got %q, want substring %q", err.Error(), want)
	}
}
