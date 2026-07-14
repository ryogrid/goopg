package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPgIndexIndpredPartialVsPlain covers the live pg_index renderer
// (catalog.InMemory.PGIndexRowsForDBOid) for the pg_node_tree columns
// indexprs/indpred. Before the fix these were hardcoded to the empty string
// "", which for a `text` column reads back as a non-NULL empty string, not
// SQL NULL. That diverged from the executor heap-row twin
// (buildUserPGIndexRow in pg18_user_catalog_rows.go, which already emits the
// predicate text for a partial index and NULL otherwise) and broke two things
// on a live SELECT:
//   - `indpred IS NOT NULL` (the canonical "is this a partial index?" probe
//     tools use) matched EVERY index.
//   - `pg_get_expr(indpred, indrelid)` returned '' instead of the WHERE
//     predicate for a partial index, and '' instead of NULL for a plain one.
//
// This asserts the corrected behavior end-to-end through pg_get_expr.
func TestPgIndexIndpredPartialVsPlain(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE idxpred_t (id int4, active bool)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX idxpred_plain ON idxpred_t (id)"); err != nil {
		t.Fatalf("CREATE INDEX plain: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX idxpred_partial ON idxpred_t (id) WHERE active"); err != nil {
		t.Fatalf("CREATE INDEX partial: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}
	var plainOID, partialOID uint32
	var wantPred string
	for _, idx := range im.AllIndexes(catalog.DefaultDBOid) {
		switch idx.Name {
		case "idxpred_plain":
			plainOID = idx.OID
		case "idxpred_partial":
			partialOID = idx.OID
			wantPred = idx.PredicateString
		}
	}
	if plainOID == 0 || partialOID == 0 {
		t.Fatalf("index OIDs not found: plain=%d partial=%d", plainOID, partialOID)
	}
	if wantPred == "" {
		t.Fatal("partial index has empty PredicateString — cannot assert pg_get_expr output")
	}

	// Column layout: indexrelid, indpred, pg_get_expr(indpred, indrelid).
	rows := runQueryUnderDBOid(t, ctx,
		"SELECT indexrelid, indpred, pg_get_expr(indpred, indrelid) FROM pg_index")

	var sawPlain, sawPartial bool
	for _, r := range rows {
		if len(r) < 3 {
			t.Fatalf("row has %d cols, want 3", len(r))
		}
		oid := uint32(r[0].Int)
		switch oid {
		case plainOID:
			sawPlain = true
			// A plain (non-partial) index: indpred is SQL NULL, and
			// pg_get_expr(NULL) is NULL — NOT the empty string.
			if !r[1].IsNull() {
				t.Errorf("plain index indpred = %q, want NULL", r[1].StringValue())
			}
			if !r[2].IsNull() {
				t.Errorf("plain index pg_get_expr(indpred) = %q, want NULL", r[2].StringValue())
			}
		case partialOID:
			sawPartial = true
			// A partial index: indpred carries the WHERE predicate text and
			// pg_get_expr passes it through verbatim.
			if r[1].IsNull() {
				t.Errorf("partial index indpred is NULL, want %q", wantPred)
			} else if got := r[1].StringValue(); got != wantPred {
				t.Errorf("partial index indpred = %q, want %q", got, wantPred)
			}
			if r[2].IsNull() {
				t.Errorf("partial index pg_get_expr(indpred) is NULL, want %q", wantPred)
			} else if got := r[2].StringValue(); got != wantPred {
				t.Errorf("partial index pg_get_expr(indpred) = %q, want %q", got, wantPred)
			}
		}
	}
	if !sawPlain {
		t.Error("plain index row missing from pg_index")
	}
	if !sawPartial {
		t.Error("partial index row missing from pg_index")
	}
}

// TestPgIndexRowsIndprIndexprsNullSentinel is a direct row-level guard on the
// PGIndexRowsForDBOid cells so a future refactor cannot silently revert the
// VirtualNull sentinel back to a bare "" (which renders as a non-NULL empty
// string). indexprs is always NULL in this path; indpred is NULL for a plain
// index and the predicate text for a partial one.
func TestPgIndexRowsIndprIndexprsNullSentinel(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	for _, ddl := range []string{
		"CREATE TABLE idxsent_t (id int4, active bool)",
		"CREATE INDEX idxsent_plain ON idxsent_t (id)",
		"CREATE INDEX idxsent_partial ON idxsent_t (id) WHERE active",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	im := ctx.Catalog.(*catalog.InMemory)
	var plainOID, partialOID uint32
	for _, idx := range im.AllIndexes(catalog.DefaultDBOid) {
		switch idx.Name {
		case "idxsent_plain":
			plainOID = idx.OID
		case "idxsent_partial":
			partialOID = idx.OID
		}
	}

	// indexprs = ordinal 19, indpred = ordinal 20.
	const indexprsCol, indpredCol = 19, 20
	for _, r := range im.PGIndexRowsForDBOid(catalog.DefaultDBOid) {
		if len(r) <= indpredCol {
			t.Fatalf("row has %d cells, want > %d", len(r), indpredCol)
		}
		if r[indexprsCol] != catalog.VirtualNull {
			t.Errorf("indexprs cell = %q, want VirtualNull sentinel", r[indexprsCol])
		}
		oid := parseUint32(t, r[0])
		switch oid {
		case plainOID:
			if r[indpredCol] != catalog.VirtualNull {
				t.Errorf("plain index indpred cell = %q, want VirtualNull sentinel", r[indpredCol])
			}
		case partialOID:
			if r[indpredCol] == catalog.VirtualNull || r[indpredCol] == "" {
				t.Errorf("partial index indpred cell = %q, want predicate text", r[indpredCol])
			}
		}
	}
}

func parseUint32(t *testing.T, s string) uint32 {
	t.Helper()
	var n uint32
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + uint32(c-'0')
	}
	return n
}
