package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestApplyWorkerFiresReplicaTriggers pins M0149-0040c
// (subscription/t/003_constraints.pl): the apply worker fires a table's row
// triggers as ExecSimpleRelationInsert does, under session_replication_role
// = replica. An ENABLE REPLICA BEFORE INSERT trigger replaces or skips the
// applied row (RETURN NULL), and an ordinary (ORIGIN) trigger does not fire
// at all — here one that would raise an error.
func TestApplyWorkerFiresReplicaTriggers(t *testing.T) {
	_, pubCat, pubCleanup := newStorageFixture(t)
	defer pubCleanup()
	pubTbl, _ := pubCat.LookupTable(parser.ObjectName{Name: "items"})
	snap := xlog.BuildCatalogSnapshot(pubCat.(*catalog.InMemory), nil)
	pubRel := pubCat.RelFileNode(pubTbl)

	subCtx, subCat, subCleanup := newDDLFixture(t)
	defer subCleanup()
	for _, q := range []string{
		"CREATE TABLE items (id int4 NOT NULL, label text)",
		`CREATE FUNCTION items_filter() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			IF new.id >= 10 THEN RETURN NULL; END IF;
			new.label := new.label || '!'; RETURN new; END $$`,
		`CREATE FUNCTION items_origin() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			RAISE EXCEPTION 'origin trigger fired'; END $$`,
		"CREATE TRIGGER items_filter_trg BEFORE INSERT ON items FOR EACH ROW EXECUTE FUNCTION items_filter()",
		"ALTER TABLE items ENABLE REPLICA TRIGGER items_filter_trg",
		"CREATE TRIGGER items_origin_trg BEFORE INSERT ON items FOR EACH ROW EXECUTE FUNCTION items_origin()",
	} {
		runSQL(t, subCtx, q)
	}

	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	defer w.SafeRollback()
	driveStream(t, w, pgoutputBIRC(t, snap, pubRel, 7, "kept", 0x100))
	driveStream(t, w, pgoutputBIRC(t, snap, pubRel, 12, "skipped", 0x200))

	// The apply worker committed in its own transactions; read them with a
	// fresh snapshot.
	tx, err := subCtx.TxnMgr.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	defer subCtx.TxnMgr.Rollback(tx)
	subCtx.Tx = tx
	if subCtx.Snap, err = subCtx.TxnMgr.SnapshotFor(tx); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(renderRows(runSQL(t, subCtx, "SELECT id, label FROM items ORDER BY id")), ";")
	if got != "7|kept!" {
		t.Fatalf("subscriber rows = %q, want %q (replica trigger replaces row 7, skips row 12; origin trigger silent)", got, "7|kept!")
	}

}
