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

// TestBtIndexCheck_OpClassDamageDetectedText is the type-generalization half of
// the M0119-0006 comparator dispatch: the same 005_opclass_damage shape on a
// *text* key column. The first slice could only invert int4 key bytes, so a
// user opclass on any other type silently fell back to the engine's byte order
// and the damage went unreported. The key decode now runs through the shared
// decodeIndexKeyColumn, so every type the B-tree can invert dispatches.
func TestBtIndexCheck_OpClassDamageDetectedText(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE FUNCTION text_asc_cmp (a text, b text) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN 1 ELSE -1 END; $$`,
		`CREATE FUNCTION text_desc_cmp (a text, b text) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN -1 ELSE 1 END; $$`,
		`CREATE OPERATOR CLASS text_fickle_ops FOR TYPE text USING btree AS
			OPERATOR 1 < (text, text), OPERATOR 2 <= (text, text),
			OPERATOR 3 = (text, text), OPERATOR 4 >= (text, text),
			OPERATOR 5 > (text, text), FUNCTION 1 text_asc_cmp(text, text)`,
		"CREATE TABLE texttbl (t text)",
		"INSERT INTO texttbl VALUES ('a'), ('b'), ('c'), ('d'), ('e'), ('f'), ('g'), ('h')",
		"CREATE INDEX ficklet ON texttbl USING btree (t text_fickle_ops)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	if _, err := runQueryWithErr(ctx, "SELECT bt_index_check('ficklet')"); err != nil {
		t.Fatalf("healthy text index under a user operator class raised: %v", err)
	}

	if err := runDDL(t, ctx, `UPDATE pg_catalog.pg_amproc
		SET amproc = 'text_desc_cmp'::regproc
		WHERE amproc = 'text_asc_cmp'::regproc`); err != nil {
		t.Fatalf("amproc damage UPDATE: %v", err)
	}

	_, err := runQueryWithErr(ctx, "SELECT bt_index_check('ficklet')")
	if err == nil {
		t.Fatal("broken text operator class not detected: bt_index_check reported clean")
	}
	const want = `item order invariant violated for index "ficklet"`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("got %q, want substring %q", err.Error(), want)
	}
}

// TestBtIndexCheck_OpClassDamageDetectedComposite covers the composite-key half:
// a two-column index whose *second* key column carries the user opclass. The
// comparator must walk the key column by column — decoding and skipping the
// leading variable-length text column to reach the int4 column the damaged
// support proc governs. The first slice declined any index with more than one
// key column outright.
func TestBtIndexCheck_OpClassDamageDetectedComposite(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE FUNCTION c_asc_cmp (a int4, b int4) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN 1 ELSE -1 END; $$`,
		`CREATE FUNCTION c_desc_cmp (a int4, b int4) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN -1 ELSE 1 END; $$`,
		`CREATE OPERATOR CLASS int4_fickle2_ops FOR TYPE int4 USING btree AS
			OPERATOR 1 < (int4, int4), OPERATOR 2 <= (int4, int4),
			OPERATOR 3 = (int4, int4), OPERATOR 4 >= (int4, int4),
			OPERATOR 5 > (int4, int4), FUNCTION 1 c_asc_cmp(int4, int4)`,
		"CREATE TABLE comptbl (t text, i int4)",
		// One text value throughout, so the leading column is always equal and
		// the damaged second-column comparator alone decides the order.
		"INSERT INTO comptbl VALUES ('k',1), ('k',2), ('k',3), ('k',4), ('k',5), ('k',6), ('k',7), ('k',8)",
		"CREATE INDEX ficklec ON comptbl USING btree (t, i int4_fickle2_ops)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	if _, err := runQueryWithErr(ctx, "SELECT bt_index_check('ficklec')"); err != nil {
		t.Fatalf("healthy composite index under a user operator class raised: %v", err)
	}

	if err := runDDL(t, ctx, `UPDATE pg_catalog.pg_amproc
		SET amproc = 'c_desc_cmp'::regproc
		WHERE amproc = 'c_asc_cmp'::regproc`); err != nil {
		t.Fatalf("amproc damage UPDATE: %v", err)
	}

	_, err := runQueryWithErr(ctx, "SELECT bt_index_check('ficklec')")
	if err == nil {
		t.Fatal("broken composite operator class not detected: bt_index_check reported clean")
	}
	const want = `item order invariant violated for index "ficklec"`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("got %q, want substring %q", err.Error(), want)
	}
}

// TestBtIndexCheck_OpClassDamageDetectedExpression covers an *expression* key
// column: `CREATE INDEX … ((lower(t)) text_fickle_ops)`. Every earlier slice
// declined an index with any expression key column outright — there was no
// catalog column to read the key's type off, so the whole index fell back to
// the engine's byte order and a damaged support proc went unreported.
//
// The type now comes from planner.ExprResultType over the same resolved
// expression the build path indexed with, mapped through exprKeyDecodeType onto
// the encoding the expression-key encoder actually produced. Both halves are
// asserted: no false positive on the healthy index, and the item-order
// violation once the amproc row is repointed at a descending comparator.
func TestBtIndexCheck_OpClassDamageDetectedExpression(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE FUNCTION x_asc_cmp (a text, b text) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN 1 ELSE -1 END; $$`,
		`CREATE FUNCTION x_desc_cmp (a text, b text) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN -1 ELSE 1 END; $$`,
		`CREATE OPERATOR CLASS text_fickle_expr_ops FOR TYPE text USING btree AS
			OPERATOR 1 < (text, text), OPERATOR 2 <= (text, text),
			OPERATOR 3 = (text, text), OPERATOR 4 >= (text, text),
			OPERATOR 5 > (text, text), FUNCTION 1 x_asc_cmp(text, text)`,
		"CREATE TABLE exprtbl (t text)",
		"INSERT INTO exprtbl VALUES ('A'), ('B'), ('C'), ('D'), ('E'), ('F'), ('G'), ('H')",
		"CREATE INDEX fickleexpr ON exprtbl USING btree ((lower(t)) text_fickle_expr_ops)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	if _, err := runQueryWithErr(ctx, "SELECT bt_index_check('fickleexpr')"); err != nil {
		t.Fatalf("healthy expression index under a user operator class raised: %v", err)
	}

	if err := runDDL(t, ctx, `UPDATE pg_catalog.pg_amproc
		SET amproc = 'x_desc_cmp'::regproc
		WHERE amproc = 'x_asc_cmp'::regproc`); err != nil {
		t.Fatalf("amproc damage UPDATE: %v", err)
	}

	_, err := runQueryWithErr(ctx, "SELECT bt_index_check('fickleexpr')")
	if err == nil {
		t.Fatal("broken operator class on an expression index not detected: bt_index_check reported clean")
	}
	const want = `item order invariant violated for index "fickleexpr"`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("got %q, want substring %q", err.Error(), want)
	}
}

// TestBtIndexCheck_ExpressionKeyCleanUnderPlainOpClass is the no-false-positive
// half in isolation, and the one that would catch a wrong DECODE WIDTH: a
// composite key whose leading column is an integer-valued expression (encoded
// through EncodeInt8 by kind dispatch, NOT as the 4-byte int4 its SQL type
// suggests) followed by a plain int4 column carrying the user opclass. If the
// expression column's surrogate consumed the wrong number of bytes, the walk
// would land mid-key on the second column and the healthy index would be
// reported corrupt.
func TestBtIndexCheck_ExpressionKeyCleanUnderPlainOpClass(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE FUNCTION xw_asc_cmp (a int4, b int4) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN 1 ELSE -1 END; $$`,
		`CREATE FUNCTION xw_desc_cmp (a int4, b int4) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN -1 ELSE 1 END; $$`,
		`CREATE OPERATOR CLASS int4_fickle_expr_ops FOR TYPE int4 USING btree AS
			OPERATOR 1 < (int4, int4), OPERATOR 2 <= (int4, int4),
			OPERATOR 3 = (int4, int4), OPERATOR 4 >= (int4, int4),
			OPERATOR 5 > (int4, int4), FUNCTION 1 xw_asc_cmp(int4, int4)`,
		"CREATE TABLE exprwtbl (a int4, i int4)",
		// Constant leading expression value: the damaged second-column
		// comparator alone decides the order, as in the composite test.
		"INSERT INTO exprwtbl VALUES (5,1), (5,2), (5,3), (5,4), (5,5), (5,6), (5,7), (5,8)",
		"CREATE INDEX ficklexw ON exprwtbl USING btree ((a * 2), i int4_fickle_expr_ops)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	if _, err := runQueryWithErr(ctx, "SELECT bt_index_check('ficklexw')"); err != nil {
		t.Fatalf("healthy expression+column index raised (wrong expression key "+
			"decode width would look exactly like this): %v", err)
	}

	if err := runDDL(t, ctx, `UPDATE pg_catalog.pg_amproc
		SET amproc = 'xw_desc_cmp'::regproc
		WHERE amproc = 'xw_asc_cmp'::regproc`); err != nil {
		t.Fatalf("amproc damage UPDATE: %v", err)
	}

	_, err := runQueryWithErr(ctx, "SELECT bt_index_check('ficklexw')")
	if err == nil {
		t.Fatal("broken opclass after an expression key column not detected: reported clean")
	}
	const want = `item order invariant violated for index "ficklexw"`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("got %q, want substring %q", err.Error(), want)
	}
}

// TestBtIndexCheck_OpClassDamageDetectedInclude covers a *covering* index: a
// user opclass on the key column of an index that also carries INCLUDE
// (non-key) columns. The earlier slices declined such an index outright,
// assuming goopg's covering-key layout was outside the decoder's contract; it
// is not — encodeCompositeBTreeKey builds the stored key from idx.Columns only,
// so the key bytes of a covering index are exactly its key columns, which is
// also the upstream rule (_bt_compare stops at the number of KEY attributes).
//
// Both halves are asserted: the healthy covering index verifies clean (no false
// positive from decoding a key that has trailing bytes we mis-modelled), and
// repointing the amproc row makes the physically-unchanged index report the
// item-order violation.
func TestBtIndexCheck_OpClassDamageDetectedInclude(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE FUNCTION inc_asc_cmp (a int4, b int4) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN 1 ELSE -1 END; $$`,
		`CREATE FUNCTION inc_desc_cmp (a int4, b int4) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN -1 ELSE 1 END; $$`,
		`CREATE OPERATOR CLASS int4_fickle3_ops FOR TYPE int4 USING btree AS
			OPERATOR 1 < (int4, int4), OPERATOR 2 <= (int4, int4),
			OPERATOR 3 = (int4, int4), OPERATOR 4 >= (int4, int4),
			OPERATOR 5 > (int4, int4), FUNCTION 1 inc_asc_cmp(int4, int4)`,
		"CREATE TABLE inctbl (i int4, payload text)",
		"INSERT INTO inctbl VALUES (1,'a'), (2,'b'), (3,'c'), (4,'d'), (5,'e'), (6,'f'), (7,'g'), (8,'h')",
		"CREATE INDEX fickleinc ON inctbl USING btree (i int4_fickle3_ops) INCLUDE (payload)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	if _, err := runQueryWithErr(ctx, "SELECT bt_index_check('fickleinc')"); err != nil {
		t.Fatalf("healthy covering index under a user operator class raised: %v", err)
	}

	if err := runDDL(t, ctx, `UPDATE pg_catalog.pg_amproc
		SET amproc = 'inc_desc_cmp'::regproc
		WHERE amproc = 'inc_asc_cmp'::regproc`); err != nil {
		t.Fatalf("amproc damage UPDATE: %v", err)
	}

	_, err := runQueryWithErr(ctx, "SELECT bt_index_check('fickleinc')")
	if err == nil {
		t.Fatal("broken operator class on a covering index not detected: bt_index_check reported clean")
	}
	const want = `item order invariant violated for index "fickleinc"`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("got %q, want substring %q", err.Error(), want)
	}
}

// TestBtIndexCheck_CheckUniqueDetectsLiveDuplicate is the M0119-0006 gate for
// the `checkunique` tier (upstream bt_entry_unique_check, verify_nbtree.c).
//
// It reproduces the corruption the tier exists to find: a UNIQUE index holding
// two entries with the same key, both pointing at heap tuples that are live
// under the checker's snapshot. The heap is untouched and internally consistent
// — the index alone is wrong — so no other tier can see it: the key run
// 1,1,3,4,5 is still non-decreasing, so item order is satisfied.
//
// Three assertions, in the order that makes the gate non-vacuous:
//  1. the damaged index passes with checkunique OFF (proving the finding comes
//     from the new tier and not from a pre-existing structural check),
//  2. it fails with checkunique ON, naming the upstream message, and
//  3. an undamaged unique index passes with checkunique ON (no false positive).
func TestBtIndexCheck_CheckUniqueDetectsLiveDuplicate(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		"CREATE TABLE bicu (a int)",
		"INSERT INTO bicu VALUES (1), (2), (3), (4), (5)",
		"CREATE UNIQUE INDEX bicu_a_idx ON bicu (a)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	// (3) first, while the index is still healthy: checkunique on a clean unique
	// index must not manufacture a finding.
	for _, sql := range []string{
		"SELECT bt_index_check('bicu_a_idx', false, true)",
		"SELECT bt_index_parent_check('bicu_a_idx', false, false, true)",
		"SELECT bt_index_check(index := 'bicu_a_idx', heapallindexed := false, checkunique := true)",
	} {
		if _, err := runQueryWithErr(ctx, sql); err != nil {
			t.Fatalf("%s: clean unique index raised: %v", sql, err)
		}
	}

	// Rewrite the leaf entry for a=2 so its key encodes 1: the index now claims
	// two live rows share key 1, while both heap tuples stay live and distinct.
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatal("expected in-memory catalog")
	}
	idx, ok := im.LookupIndex(parser.ObjectName{Name: "bicu_a_idx"})
	if !ok {
		t.Fatal("index bicu_a_idx not found")
	}
	rel := ctx.Catalog.IndexRelFileNode(idx)
	key1, key2 := btree.EncodeInt4(1), btree.EncodeInt4(2)
	patched := false
	nblocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	for blk := btree.MetaBlock + 1; blk < nblocks && !patched; blk++ {
		s, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if perr != nil {
			t.Fatalf("pin block %d: %v", blk, perr)
		}
		if btree.ParseOpaque(s.Page()).IsLeaf() {
			count, cerr := storage.PageLinePointerCount(s.Page())
			if cerr != nil {
				t.Fatalf("line pointer count: %v", cerr)
			}
			for slot := uint16(1); slot <= uint16(count); slot++ {
				raw, rerr := storage.PageGetItemRawNoCopy(s.Page(), slot)
				if rerr != nil || len(raw) != 8+len(key2) {
					continue
				}
				if string(raw[8:]) == string(key2) {
					copy(raw[8:], key1) // in-place: raw aliases the page
					patched = true
					break
				}
			}
		}
		if patched {
			ctx.Pool.MarkDirty(s)
		}
		ctx.Pool.Unpin(s)
	}
	if !patched {
		t.Fatal("could not locate the leaf entry for a=2 to damage")
	}

	// (1) The damage is invisible to every other tier.
	for _, sql := range []string{
		"SELECT bt_index_check('bicu_a_idx')",
		"SELECT bt_index_check('bicu_a_idx', false, false)",
		"SELECT bt_index_parent_check('bicu_a_idx', false, false, false)",
	} {
		if _, err := runQueryWithErr(ctx, sql); err != nil {
			t.Fatalf("%s: duplicate-key damage leaked into a non-checkunique tier: %v", sql, err)
		}
	}

	// (2) checkunique finds it, with the upstream message and a detail naming
	// both heap TIDs.
	for _, sql := range []string{
		"SELECT bt_index_check('bicu_a_idx', false, true)",
		"SELECT bt_index_parent_check('bicu_a_idx', false, false, true)",
	} {
		qerr := func() error { _, e := runQueryWithErr(ctx, sql); return e }()
		if qerr == nil {
			t.Errorf("%s: live duplicate not detected", sql)
			continue
		}
		const want = `index uniqueness is violated for index "bicu_a_idx"`
		if !strings.Contains(qerr.Error(), want) {
			t.Errorf("%s: got %q, want substring %q", sql, qerr.Error(), want)
		}
		// The heap-TID errdetail rides the ExecError's Detail (the wire DETAIL
		// field), not the primary message, exactly as upstream's
		// errdetail_internal does.
		ee, ok := qerr.(*ExecError)
		if !ok {
			t.Errorf("%s: error is %T, want *ExecError", sql, qerr)
			continue
		}
		if !strings.Contains(ee.Detail, "point to heap tid=") {
			t.Errorf("%s: DETAIL %q lacks the duplicate errdetail", sql, ee.Detail)
		}
	}
}

// TestBtIndexCheck_CheckUniqueSkipsNonUniqueIndex pins upstream's
// `state->indexinfo->ii_Unique` gate: the same duplicate-key layout on a
// NON-unique index is not corruption at all, so checkunique must stay silent.
func TestBtIndexCheck_CheckUniqueSkipsNonUniqueIndex(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		"CREATE TABLE bicn (a int)",
		// Genuine duplicates, permitted because the index is not unique.
		"INSERT INTO bicn VALUES (1), (1), (2), (3)",
		"CREATE INDEX bicn_a_idx ON bicn (a)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	for _, sql := range []string{
		"SELECT bt_index_check('bicn_a_idx', false, true)",
		"SELECT bt_index_parent_check('bicn_a_idx', false, false, true)",
	} {
		if _, err := runQueryWithErr(ctx, sql); err != nil {
			t.Errorf("%s: duplicates on a non-unique index reported: %v", sql, err)
		}
	}
}

// TestBtIndexCheck_OpClassDamageDetectedNumeric covers the NUMERIC key type —
// the fifth M0119-0006 slice. NUMERIC was the one common built-in type the
// column-by-column walk could not invert: its key encoding is variable-length
// (sign || biased exponent || digit run || terminator), so before
// btree.DecodeNumericKey existed the walk fell through to the default branch,
// mis-read 8 bytes as a float8 and returned a bogus enum Datum. The comparator
// would then hand the user's FUNCTION 1 routine garbage.
//
// Non-vacuity: the healthy assertion below fails if the comparator silently
// declines, because a declining comparator can never report damage either —
// the damaged assertion is what proves the routine is actually consulted.
func TestBtIndexCheck_OpClassDamageDetectedNumeric(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE FUNCTION n_asc_cmp (a numeric, b numeric) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN 1 ELSE -1 END; $$`,
		`CREATE FUNCTION n_desc_cmp (a numeric, b numeric) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN -1 ELSE 1 END; $$`,
		`CREATE OPERATOR CLASS numeric_fickle_ops FOR TYPE numeric USING btree AS
			OPERATOR 1 < (numeric, numeric), OPERATOR 2 <= (numeric, numeric),
			OPERATOR 3 = (numeric, numeric), OPERATOR 4 >= (numeric, numeric),
			OPERATOR 5 > (numeric, numeric), FUNCTION 1 n_asc_cmp(numeric, numeric)`,
		"CREATE TABLE numtbl (n numeric)",
		// Mixed scales and signs so the decode has to normalise, not just read
		// a fixed-width field: -2.50, -1, 0, 0.125, 1.5, 10, 100.25, 1000.
		`INSERT INTO numtbl VALUES (-2.50), (-1), (0), (0.125), (1.5), (10), (100.25), (1000)`,
		"CREATE INDEX ficklen ON numtbl USING btree (n numeric_fickle_ops)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	if _, err := runQueryWithErr(ctx, "SELECT bt_index_check('ficklen')"); err != nil {
		t.Fatalf("healthy numeric index under a user operator class raised: %v", err)
	}

	if err := runDDL(t, ctx, `UPDATE pg_catalog.pg_amproc
		SET amproc = 'n_desc_cmp'::regproc
		WHERE amproc = 'n_asc_cmp'::regproc`); err != nil {
		t.Fatalf("amproc damage UPDATE: %v", err)
	}

	_, err := runQueryWithErr(ctx, "SELECT bt_index_check('ficklen')")
	if err == nil {
		t.Fatal("broken numeric operator class not detected: bt_index_check reported clean")
	}
	const want = `item order invariant violated for index "ficklen"`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("got %q, want substring %q", err.Error(), want)
	}
}
