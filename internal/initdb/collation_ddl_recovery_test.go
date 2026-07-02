package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// findUserCollation looks up a user collation by (schema, name) in the
// registry snapshot returned by ListUserCollations, resolving schema via the
// catalog's schema-name map (mirrors CreateCollation's own resolution).
func findUserCollation(cat *catalog.InMemory, name, schema string) *catalog.UserCollation {
	nsOID := cat.SchemaOID(schema)
	for _, uc := range cat.ListUserCollations() {
		if uc.Name == name && uc.NamespaceOID == nsOID {
			return uc
		}
	}
	return nil
}

// TestCollationDDLRecoveryReplaysCreate confirms the DU-002
// restart-persistence hook (M0119-0004 follow-up): a CREATE COLLATION WAL
// record written by a pre-crash run is replayed into the catalog's
// collation registry on the post-crash Open path, so pg_collation (pg_dump's
// getCollations/dumpCollation) round-trips after restart even though the
// goopg process never wrote a JSON snapshot. The OID is preserved across the
// restart, and NamespaceOID is re-resolved from the schema name against the
// post-restart schema map.
func TestCollationDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40460)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateCollation("mycoll", "public", "C", "C", "", "", wantOID, 10, -1, 'c', true)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-collation: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the collation in the registry
	// without any JSON snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", rt2.Catalog)
	}
	uc := findUserCollation(cat, "mycoll", "public")
	if uc == nil {
		t.Fatalf("after WAL replay, collation \"mycoll\" not found; registry = %+v", cat.ListUserCollations())
	}
	if uc.OID != wantOID || uc.Provider != 'c' || uc.Collate != "C" || uc.Ctype != "C" || uc.Encoding != -1 || !uc.Deterministic {
		t.Errorf("after WAL replay, collation = %+v, want OID=%d Provider='c' Collate=C Ctype=C Encoding=-1 Deterministic=true", uc, wantOID)
	}
}

// TestCollationDDLRecoveryReplaysDropAfterCreate confirms that a CREATE
// followed by a DROP cancels out — the registry agrees with the most recent
// durable record, not the first one.
func TestCollationDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateCollation("mycoll", "public", "C", "C", "", "", 40500, 10, -1, 'c', true)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropCollation("mycoll", "public")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append drop: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat := rt2.Catalog.(*catalog.InMemory)
	if uc := findUserCollation(cat, "mycoll", "public"); uc != nil {
		t.Errorf("after CREATE + DROP replay, collation \"mycoll\" = %+v, want nil", uc)
	}
}

// TestCollationDDLRecoveryReplaysRenameAfterCreate confirms the DU-002
// restart-persistence follow-up (M0119-0004, loop #50 ledger resume point
// (a)): a CREATE COLLATION followed by an ALTER COLLATION ... RENAME TO
// replays in order, so the post-restart registry has the collation under its
// new name (not the original), with its OID preserved.
func TestCollationDDLRecoveryReplaysRenameAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40510)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateCollation("mycoll", "public", "C", "C", "", "", wantOID, 10, -1, 'c', true)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterCollationRename("mycoll", "public", "renamedcoll")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append rename: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat := rt2.Catalog.(*catalog.InMemory)
	if uc := findUserCollation(cat, "mycoll", "public"); uc != nil {
		t.Errorf("after CREATE + RENAME replay, old name \"mycoll\" = %+v, want nil", uc)
	}
	uc := findUserCollation(cat, "renamedcoll", "public")
	if uc == nil {
		t.Fatalf("after CREATE + RENAME replay, \"renamedcoll\" not found; registry = %+v", cat.ListUserCollations())
	}
	if uc.OID != wantOID {
		t.Errorf("after CREATE + RENAME replay, OID = %d, want %d (renamed collation must keep its original OID)", uc.OID, wantOID)
	}
}

// TestCollationDDLRecoveryReplaysOwnerAfterCreate mirrors the rename test for
// ALTER COLLATION ... OWNER TO.
func TestCollationDDLRecoveryReplaysOwnerAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40520)
	const wantOwnerOID = uint32(16401)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateCollation("mycoll", "public", "C", "C", "", "", wantOID, 10, -1, 'c', true)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterCollationOwner("mycoll", "public", wantOwnerOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append owner: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat := rt2.Catalog.(*catalog.InMemory)
	uc := findUserCollation(cat, "mycoll", "public")
	if uc == nil {
		t.Fatalf("after CREATE + OWNER replay, \"mycoll\" not found; registry = %+v", cat.ListUserCollations())
	}
	if uc.Owner != wantOwnerOID {
		t.Errorf("after CREATE + OWNER replay, Owner = %d, want %d", uc.Owner, wantOwnerOID)
	}
}

// TestReplayCollationDDLRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (brand
// new initdb).
func TestReplayCollationDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayCollationDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if len(cat.ListUserCollations()) != 0 {
		t.Errorf("no-op replay should not register any collation, got %+v", cat.ListUserCollations())
	}
}
