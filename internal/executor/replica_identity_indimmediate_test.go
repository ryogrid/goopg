package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestReplicaIdentityRejectsMerelyDeferrableIndex (M0134-0161) pins the
// indimmediate rule for `ALTER TABLE ... REPLICA IDENTITY USING INDEX`: an
// index backing a constraint declared just `DEFERRABLE` — INITIALLY IMMEDIATE,
// so catalog.Index.InitiallyDeferred is false — is NON-immediate and must be
// rejected. PG derives pg_index.indimmediate from the DEFERRABLE flag alone
// (postgres/src/backend/catalog/index.c:1049 and index_set_state_flags,
// index.c:2080-2082), and ATExecReplicaIdentity gates on it
// (tablecmds.c:18550, 0A000 "cannot use non-immediate index").
//
// Before this fix goopg keyed the check on InitiallyDeferred and so silently
// ACCEPTED such an index, leaving the table at relreplident='i' where PG
// leaves it at 'd' — the divergence regress/replica_identity.sql exposed.
// Oracle-verified on a live PG 18.3.
func TestReplicaIdentityRejectsMerelyDeferrableIndex(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE ri_defer (a INTEGER NOT NULL, b INTEGER NOT NULL,
		CONSTRAINT ri_defer_u UNIQUE (a) DEFERRABLE,
		CONSTRAINT ri_defer_u2 UNIQUE (b))`); err != nil {
		t.Fatalf("CREATE TABLE ri_defer: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TABLE ri_defer REPLICA IDENTITY USING INDEX ri_defer_u`)
	requireExecError(t, err, "0A000", `cannot use non-immediate index "ri_defer_u" as replica identity`)

	// The NOT DEFERRABLE sibling on the same table must still be accepted —
	// this is a rejection of non-immediate indexes, not of constraint-backed
	// ones.
	if err := runDDL(t, ctx, `ALTER TABLE ri_defer REPLICA IDENTITY USING INDEX ri_defer_u2`); err != nil {
		t.Fatalf("REPLICA IDENTITY USING INDEX ri_defer_u2: %v", err)
	}
}

// TestPgIndexIndimmediateTracksDeferrable (M0134-0161) is the catalog-side
// sibling of TestReplicaIdentityRejectsMerelyDeferrableIndex: the pg_index row
// builder must report indimmediate='f' for a DEFERRABLE constraint index, not
// the hardcoded 't' it emitted before. The validation path and the catalog
// view have to agree — they had drifted apart, which is exactly how the
// silent-acceptance bug survived.
func TestPgIndexIndimmediateTracksDeferrable(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE ii_t (a INTEGER NOT NULL, b INTEGER NOT NULL, c INTEGER NOT NULL,
		CONSTRAINT ii_plain UNIQUE (a),
		CONSTRAINT ii_defer UNIQUE (b) DEFERRABLE,
		CONSTRAINT ii_initdefer UNIQUE (c) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatalf("CREATE TABLE ii_t: %v", err)
	}

	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "ii_t"})
	if !ok {
		t.Fatal("table ii_t not found")
	}
	want := map[string]bool{"ii_plain": true, "ii_defer": false, "ii_initdefer": false}
	seen := map[string]bool{}
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl) {
		exp, tracked := want[idx.Name]
		if !tracked {
			continue
		}
		seen[idx.Name] = true
		if idx.IsImmediate() != exp {
			t.Errorf("index %s: IsImmediate() = %v, want %v", idx.Name, idx.IsImmediate(), exp)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("index %s not found on ii_t", name)
		}
	}
}
