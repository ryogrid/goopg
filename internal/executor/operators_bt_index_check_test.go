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
