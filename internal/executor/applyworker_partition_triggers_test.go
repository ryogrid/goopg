package executor

import (
	"bytes"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestApplyWorkerFiresLeafPartitionTriggers pins M0149-0061c
// (subscription/t/013_partition.pl line 243) against PG 18.3's
// apply_handle_tuple_routing: a change applied to a partitioned subscriber
// table fires the row triggers of the LEAF it lands in. An UPDATE that
// stays in one leaf fires that leaf's UPDATE triggers; one that moves the
// row to another leaf fires DELETE on the old leaf and INSERT on the new
// one. Before, only the routed INSERT fired them: routed UPDATEs and
// DELETEs never did.
func TestApplyWorkerFiresLeafPartitionTriggers(t *testing.T) {
	_, pubCat, pubCleanup := newStorageFixture(t)
	defer pubCleanup()
	pubIM := pubCat.(*catalog.InMemory)
	pubTbl, _ := pubCat.LookupTable(parser.ObjectName{Name: "items"})
	if _, err := pubIM.CreateIndex(parser.ObjectName{Name: "items_pkey"}, pubTbl, []string{"id"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	snap := xlog.BuildCatalogSnapshot(pubIM, nil)
	rel := pubCat.RelFileNode(pubTbl)

	var buf bytes.Buffer
	po := xlog.NewPgOutput(snap, &buf)
	tuple := func(id int, label string) []byte {
		return wrapAsHeapTuple(t, encodeBodyV0([]any{id, label}, []string{"int4", "text"}), 2)
	}
	var frames [][]byte
	for _, s := range []func() error{
		func() error { return po.Begin(41, 0x100) },
		func() error {
			return po.Change(xlog.Change{Kind: xlog.ChangeInsert, Rel: rel, NewTuple: tuple(1, "x")})
		},
		func() error { return po.Commit(41, 0x100) },
		func() error { return po.Begin(42, 0x200) },
		// Same leaf (items_a holds 1 and 2).
		func() error {
			return po.Change(xlog.Change{Kind: xlog.ChangeUpdate, Rel: rel, OldTuple: tuple(1, "x"), NewTuple: tuple(2, "x")})
		},
		// Cross-partition: 2 → 3 moves the row to items_b.
		func() error {
			return po.Change(xlog.Change{Kind: xlog.ChangeUpdate, Rel: rel, OldTuple: tuple(2, "x"), NewTuple: tuple(3, "x")})
		},
		func() error {
			return po.Change(xlog.Change{Kind: xlog.ChangeDelete, Rel: rel, OldTuple: tuple(3, "x")})
		},
		func() error { return po.Commit(42, 0x200) },
	} {
		buf.Reset()
		if err := s(); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, append([]byte(nil), buf.Bytes()...))
	}

	subCtx, subCat, subCleanup := newDDLFixture(t)
	defer subCleanup()
	for _, q := range []string{
		"CREATE TABLE trg_log (tab text, op text, olda int, newa int)",
		`CREATE FUNCTION trg_log_func() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			IF TG_OP = 'INSERT' THEN INSERT INTO trg_log VALUES (TG_RELNAME, TG_OP, NULL, NEW.id);
			ELSIF TG_OP = 'UPDATE' THEN INSERT INTO trg_log VALUES (TG_RELNAME, TG_OP, OLD.id, NEW.id);
			ELSE INSERT INTO trg_log VALUES (TG_RELNAME, TG_OP, OLD.id, NULL); END IF;
			RETURN NULL; END $$`,
		"CREATE TABLE items (id int4 NOT NULL, label text) PARTITION BY LIST (id)",
		"CREATE TABLE items_a PARTITION OF items FOR VALUES IN (1, 2)",
		"CREATE TABLE items_b PARTITION OF items FOR VALUES IN (3, 4)",
	} {
		runSQL(t, subCtx, q)
	}
	for _, leaf := range []string{"items_a", "items_b"} {
		runSQL(t, subCtx, "CREATE TRIGGER "+leaf+"_trg AFTER INSERT OR UPDATE OR DELETE ON "+leaf+" FOR EACH ROW EXECUTE FUNCTION trg_log_func()")
		runSQL(t, subCtx, "ALTER TABLE ONLY "+leaf+" ENABLE REPLICA TRIGGER "+leaf+"_trg")
	}

	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	defer w.SafeRollback()
	for _, f := range frames {
		if f[0] == 'B' || f[0] == 'C' || f[0] == 'R' || f[0] == 'I' {
			driveStream(t, w, f)
			continue
		}
		m, err := xlog.DecodeMessage(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.ApplyMessage(m); err != nil {
			t.Fatalf("ApplyMessage(%c): %v", f[0], err)
		}
	}

	tx, err := subCtx.TxnMgr.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	defer subCtx.TxnMgr.Rollback(tx)
	subCtx.Tx = tx
	if subCtx.Snap, err = subCtx.TxnMgr.SnapshotFor(tx); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(renderRows(runSQL(t, subCtx, "SELECT tab, op, olda, newa FROM trg_log ORDER BY tab, op, olda, newa")), ";")
	want := "items_a|DELETE|2|NULL;items_a|INSERT|NULL|1;items_a|UPDATE|1|2;items_b|DELETE|3|NULL;items_b|INSERT|NULL|3"
	if got != want {
		t.Errorf("trigger log = %q\nwant         %q", got, want)
	}
	if rows := runSQL(t, subCtx, "SELECT count(*) FROM items"); len(rows) != 1 || rows[0][0].Int != 0 {
		t.Errorf("items rows = %v, want 0", rows)
	}
}
