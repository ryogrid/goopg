package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

func runDDL(t *testing.T, ctx *Context, sql string) error {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		return err
	}
	if _, err := op.Next(); err != EOF {
		return err
	}
	return op.Close()
}

// TestDDLCreateTableEndToEnd runs CREATE TABLE through the parser ->
// planner -> executor stack and verifies the catalog grew an entry
// with the expected columns.
func TestDDLCreateTableEndToEnd(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (a int NOT NULL, b text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table not in catalog")
	}
	if len(tbl.Columns) != 2 || tbl.Columns[0].Name != "a" || !tbl.Columns[0].NotNull {
		t.Errorf("columns=%+v", tbl.Columns)
	}
	if tbl.Columns[1].Type.Name != "text" {
		t.Errorf("col[1].Type=%+v", tbl.Columns[1].Type)
	}
}

// TestDDLCreateTableDuplicateAndIfNotExists pins the 42P07 error path
// and the IF NOT EXISTS escape.
func TestDDLCreateTableDuplicateAndIfNotExists(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (a int)"); err != nil {
		t.Fatal(err)
	}
	err := runDDL(t, ctx, "CREATE TABLE t (b int)")
	if err == nil {
		t.Fatal("duplicate CREATE TABLE should fail")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42P07" {
		t.Errorf("want 42P07, got %v", err)
	}
	// IF NOT EXISTS is a no-op when the table already exists.
	if err := runDDL(t, ctx, "CREATE TABLE IF NOT EXISTS t (b int)"); err != nil {
		t.Errorf("CREATE TABLE IF NOT EXISTS: %v", err)
	}
}

// TestDDLDropTableRemovesCatalogAndFile: DROP TABLE removes the
// catalog entry and the on-disk relation file.
func TestDDLDropTableRemovesCatalogAndFile(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (a int)"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	// Force the relation file into existence.
	if _, _, err := ctx.Pool.PinNew(rel); err != nil {
		t.Fatalf("PinNew: %v", err)
	}

	if err := runDDL(t, ctx, "DROP TABLE t"); err != nil {
		t.Fatalf("DROP TABLE: %v", err)
	}
	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"}); ok {
		t.Errorf("table still in catalog after DROP")
	}
	// Re-resolving via NBlocks should now hit a re-created (empty) file.
	n, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks post-drop: %v", err)
	}
	if n != 0 {
		t.Errorf("NBlocks=%d want 0", n)
	}
}

func TestDDLCreateTempTableShadowsPermanentTable(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE shadow_t (v varchar(4))"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO shadow_t (v) VALUES ('base')"); err != nil {
		t.Fatal(err)
	}

	if err := runDDL(t, ctx, "CREATE TEMP TABLE shadow_t (v varchar(1))"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO shadow_t (v) VALUES ('x')"); err != nil {
		t.Fatal(err)
	}

	rows := runQuery(t, ctx, "SELECT v FROM shadow_t")
	if len(rows) != 1 || rows[0][0].StringValue() != "x" {
		t.Fatalf("temp shadow rows=%v, want single temp row x", rows)
	}

	if err := runDDL(t, ctx, "DROP TABLE shadow_t"); err != nil {
		t.Fatal(err)
	}
	rows = runQuery(t, ctx, "SELECT v FROM shadow_t")
	if len(rows) != 1 || rows[0][0].StringValue() != "base" {
		t.Fatalf("restored permanent rows=%v, want single base row", rows)
	}
}

func TestDDLInsertIntIntoVarcharPreservesDigits(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE varchar_t (v varchar(1))"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO varchar_t (v) VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	rows := runQuery(t, ctx, "SELECT v FROM varchar_t")
	if len(rows) != 1 || rows[0][0].StringValue() != "2" {
		t.Fatalf("varchar rows=%v, want single row \"2\"", rows)
	}
}

// TestDDLDropTableIfExists silently succeeds when the table is absent.
func TestDDLDropTableIfExists(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "DROP TABLE IF EXISTS missing"); err != nil {
		t.Errorf("IF EXISTS path: %v", err)
	}
	err := runDDL(t, ctx, "DROP TABLE missing")
	if err == nil {
		t.Fatal("DROP TABLE without IF EXISTS should fail")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42P01" {
		t.Errorf("want 42P01, got %v", err)
	}
}

// TestDDLTruncateClearsRelation: TRUNCATE shrinks the file to 0
// blocks and a subsequent SeqScan returns no rows.
func TestDDLTruncateClearsRelation(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	rel := ctx.Catalog.RelFileNode(tbl)
	n, _ := ctx.Pool.NBlocks(rel)
	if n == 0 {
		t.Fatal("expected relation file to have at least one block before truncate")
	}

	if err := runDDL(t, ctx, "TRUNCATE TABLE items"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	n2, _ := ctx.Pool.NBlocks(rel)
	if n2 != 0 {
		t.Errorf("NBlocks after TRUNCATE = %d want 0", n2)
	}

	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, _ := drainScan(scan)
	if len(rows) != 0 {
		t.Errorf("post-truncate scan returned %d rows want 0", len(rows))
	}
}

func TestDDLCreateIndexBuildsSearchableBTree(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	if err := runDDL(t, ctx, "CREATE INDEX idx_items_id ON items (id)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_items_id"})
	if !ok {
		t.Fatal("index not in catalog after CREATE INDEX")
	}
	rel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := btree.Open(ctx.Pool, rel)
	if err != nil {
		t.Fatalf("btree.Open: %v", err)
	}
	for _, k := range []int32{1, 2, 3} {
		if _, found, err := tree.Search(btree.EncodeInt4(k)); err != nil || !found {
			t.Fatalf("index search key=%d found=%v err=%v", k, found, err)
		}
	}
}

func TestDDLDropIndexRemovesCatalogAndFile(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE items (id int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX idx_items_id ON items (id)"); err != nil {
		t.Fatal(err)
	}
	idx, _ := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_items_id"})
	idxRel := ctx.Catalog.IndexRelFileNode(idx)

	if err := runDDL(t, ctx, "DROP INDEX idx_items_id"); err != nil {
		t.Fatalf("DROP INDEX: %v", err)
	}
	if _, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_items_id"}); ok {
		t.Fatal("index still in catalog after DROP INDEX")
	}
	n, err := ctx.Pool.NBlocks(idxRel)
	if err != nil {
		t.Fatalf("NBlocks on dropped index rel: %v", err)
	}
	if n != 0 {
		t.Fatalf("dropped index rel has %d blocks, want 0", n)
	}
}

func TestDDLAlterTableAddColumnKeepsExistingRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE items (id int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	if err := writeHeapRow(ctx, ctx.Catalog.RelFileNode(tbl), tbl.Columns, Row{{Kind: KindInt, Int: 7}}); err != nil {
		t.Fatalf("writeHeapRow: %v", err)
	}

	if err := runDDL(t, ctx, "ALTER TABLE items ADD COLUMN label text"); err != nil {
		t.Fatalf("ALTER TABLE ADD COLUMN: %v", err)
	}
	tbl, _ = ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	if len(tbl.Columns) != 2 || tbl.Columns[1].Name != "label" {
		t.Fatalf("columns after ADD COLUMN: %+v", tbl.Columns)
	}

	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 7 {
		t.Fatalf("id=%d want=7", rows[0][0].Int)
	}
	if !rows[0][1].IsNull() {
		t.Fatalf("new column should read NULL for old rows, got %+v", rows[0][1])
	}
}

// TestDDLAlterTableAddColumnDefaultBackfillsExistingRows pins the
// M0097-0077 "fast default" end-to-end: a row written before
// `ALTER TABLE ... ADD COLUMN c <type> DEFAULT <const>` must surface the
// constant default (not NULL) on a subsequent scan, without a table
// rewrite — mirroring PostgreSQL's attmissingval. This is the e2e
// counterpart to the decoder-level TestDecodeRowIntoMctxPGTuple* tests.
func TestDDLAlterTableAddColumnDefaultBackfillsExistingRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE items (id int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	if err := writeHeapRow(ctx, ctx.Catalog.RelFileNode(tbl), tbl.Columns, Row{{Kind: KindInt, Int: 7}}); err != nil {
		t.Fatalf("writeHeapRow: %v", err)
	}

	if err := runDDL(t, ctx, "ALTER TABLE items ADD COLUMN qty int DEFAULT 99"); err != nil {
		t.Fatalf("ALTER TABLE ADD COLUMN DEFAULT: %v", err)
	}
	tbl, _ = ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	if len(tbl.Columns) != 2 || tbl.Columns[1].Name != "qty" {
		t.Fatalf("columns after ADD COLUMN: %+v", tbl.Columns)
	}
	if mv, ok := tbl.Columns[1].MissingValue.(Datum); !ok || mv.Kind != KindInt || mv.Int != 99 {
		t.Fatalf("MissingValue = %+v, want KindInt 99", tbl.Columns[1].MissingValue)
	}

	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 7 {
		t.Fatalf("id=%d want=7", rows[0][0].Int)
	}
	if rows[0][1].IsNull() || rows[0][1].Kind != KindInt || rows[0][1].Int != 99 {
		t.Fatalf("pre-ALTER row should read DEFAULT 99, got %+v", rows[0][1])
	}
}

// TestDDLAlterTableAddColumnDuplicateErrors pins that adding a column that
// already exists on the named relation still returns 42701 (duplicate_column).
// Regression guard for M0097-0077: the inheritance-recursion refactor must
// keep erroring on the root table — only same-named columns on recursed
// inheritance children are silently merged.
func TestDDLAlterTableAddColumnDuplicateErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	err := runDDL(t, ctx, "ALTER TABLE items ADD COLUMN label text")
	if err == nil {
		t.Fatal("expected 42701 duplicate_column for ADD COLUMN of existing column")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42701" {
		t.Fatalf("error = %v, want ExecError 42701", err)
	}
}

func TestDDLAlterTableAddPrimaryKeyCreatesUniqueIndex(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	if err := runDDL(t, ctx, "ALTER TABLE items ADD PRIMARY KEY (id)"); err != nil {
		t.Fatalf("ALTER TABLE ... ADD PRIMARY KEY: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "items_pkey"})
	if !ok {
		t.Fatal("expected items_pkey index in catalog")
	}
	if !idx.Unique || !idx.Primary {
		t.Fatalf("index flags: unique=%v primary=%v", idx.Unique, idx.Primary)
	}
	tree, err := btree.Open(ctx.Pool, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := tree.Search(btree.EncodeInt4(2)); err != nil || !found {
		t.Fatalf("primary-key index search found=%v err=%v", found, err)
	}
}

func TestDDLAlterTableAddPrimaryKeyRejectsDuplicates(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE dup (id int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "dup"})
	rel := ctx.Catalog.RelFileNode(tbl)
	if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: 1}}); err != nil {
		t.Fatal(err)
	}
	err := runDDL(t, ctx, "ALTER TABLE dup ADD PRIMARY KEY (id)")
	if err == nil {
		t.Fatal("expected unique-violation on duplicate id values")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23505" {
		t.Fatalf("err=%v want ExecError 23505", err)
	}
}

// newDDLFixture is a sibling of newStorageFixture that does NOT
// pre-create a table — DDL tests do that themselves through the
// executor.
func newDDLFixture(t *testing.T) (*Context, catalog.Catalog, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat := catalog.NewInMemory()
	mgrMVCC := mvcc.NewManager()
	tx, err := mgrMVCC.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := mgrMVCC.SnapshotFor(tx)
	if err != nil {
		t.Fatal(err)
	}
	ctx := NewContext()
	ctx.Pool = pool
	ctx.Catalog = cat
	ctx.TxnMgr = mgrMVCC
	ctx.Tx = tx
	ctx.Snap = snap
	cleanup := func() {
		_ = mgrMVCC.Rollback(tx)
		_ = pool.Close()
		_ = mgr.Close()
	}
	return ctx, cat, cleanup
}

// TestDDLAlterTableClusterOnRoundTrip pins the restore side of the clustered-
// index round-trip (DU-002 slice 321): pg_dump emits `ALTER TABLE <t> CLUSTER ON
// <idx>` for a clustered table, so goopg must accept that exact clause to restore
// its own dumps. The statement marks the named index's catalog.Index.IsClustered
// (pg_index.indisclustered) and clears the flag on every other index of the
// table, mirroring mark_index_clustered (cluster.c) — the same effect as
// `CLUSTER <t> USING <idx>` (slice 320). `SET WITHOUT CLUSTER` then clears the
// selection on every index.
func TestDDLAlterTableClusterOnRoundTrip(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (a int PRIMARY KEY, b int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_b_idx ON t (b)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table t not in catalog")
	}

	clusteredName := func() string {
		for _, idx := range cat.IndexesOnTable(tbl) {
			if idx.IsClustered {
				return idx.Name
			}
		}
		return ""
	}

	// Initially no index is clustered.
	if got := clusteredName(); got != "" {
		t.Fatalf("before CLUSTER ON: clustered index = %q, want none", got)
	}

	// ALTER TABLE t CLUSTER ON t_b_idx — the dump-emitted restore form.
	if err := runDDL(t, ctx, "ALTER TABLE t CLUSTER ON t_b_idx"); err != nil {
		t.Fatalf("ALTER TABLE CLUSTER ON: %v", err)
	}
	if got := clusteredName(); got != "t_b_idx" {
		t.Errorf("after CLUSTER ON t_b_idx: clustered index = %q, want t_b_idx", got)
	}

	// Re-pointing to the PK index must clear the flag on t_b_idx (exactly one
	// index is ever clustered, mark_index_clustered).
	if err := runDDL(t, ctx, "ALTER TABLE t CLUSTER ON t_pkey"); err != nil {
		t.Fatalf("ALTER TABLE CLUSTER ON t_pkey: %v", err)
	}
	if got := clusteredName(); got != "t_pkey" {
		t.Errorf("after CLUSTER ON t_pkey: clustered index = %q, want t_pkey", got)
	}

	// CLUSTER ON an unknown index → 42704.
	err := runDDL(t, ctx, "ALTER TABLE t CLUSTER ON nope")
	if err == nil {
		t.Fatal("CLUSTER ON nope: expected error, got nil")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("CLUSTER ON nope: err = %v, want SQLSTATE 42704", err)
	}

	// SET WITHOUT CLUSTER clears the selection entirely.
	if err := runDDL(t, ctx, "ALTER TABLE t SET WITHOUT CLUSTER"); err != nil {
		t.Fatalf("ALTER TABLE SET WITHOUT CLUSTER: %v", err)
	}
	if got := clusteredName(); got != "" {
		t.Errorf("after SET WITHOUT CLUSTER: clustered index = %q, want none", got)
	}
}

// TestDDLCreatePublicationEndToEnd pins the M0008 / 0008-0003 SQL
// surface: `CREATE PUBLICATION p FOR TABLE t` parses, plans,
// and executes against a Context whose PubSub registry is wired,
// landing a row visible via Lookup.
func TestDDLCreatePublicationEndToEnd(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	// Fixture pre-registers "items"; the DDL just references it.
	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items WITH (publish = 'insert,delete')"); err != nil {
		t.Fatal(err)
	}
	pub, ok := ctx.PubSub.LookupPublication("p")
	if !ok {
		t.Fatal("publication p not registered")
	}
	if pub.AllTables {
		t.Errorf("AllTables=true want false")
	}
	if len(pub.Tables) != 1 || pub.Tables[0] != "items" {
		t.Errorf("Tables=%v want [items]", pub.Tables)
	}
	if !pub.PublishInsert || pub.PublishUpdate || !pub.PublishDelete {
		t.Errorf("publish flags=%+v want insert+delete only", pub)
	}
}

// TestDDLCreateSubscriptionEndToEnd: a SUBSCRIPTION lands in the
// registry via the SQL path with conninfo, publication list, and
// slot_name carried from the WITH option.
func TestDDLCreateSubscriptionEndToEnd(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	sql := "CREATE SUBSCRIPTION s CONNECTION 'host=remote dbname=app' PUBLICATION p1, p2 WITH (slot_name = mysub, enabled = false)"
	if err := runDDL(t, ctx, sql); err != nil {
		t.Fatal(err)
	}
	sub, ok := ctx.PubSub.LookupSubscription("s")
	if !ok {
		t.Fatal("subscription s not registered")
	}
	if sub.Conninfo != "host=remote dbname=app" {
		t.Errorf("Conninfo=%q", sub.Conninfo)
	}
	if len(sub.Publications) != 2 || sub.Publications[0] != "p1" || sub.Publications[1] != "p2" {
		t.Errorf("Publications=%v", sub.Publications)
	}
	if sub.SlotName != "mysub" {
		t.Errorf("SlotName=%q want mysub", sub.SlotName)
	}
	if sub.Enabled {
		t.Errorf("Enabled=true want false")
	}
}

// TestDDLDropPublicationIfExists: DROP IF EXISTS is a no-op when
// the publication doesn't exist.
func TestDDLDropPublicationIfExists(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()
	if err := runDDL(t, ctx, "DROP PUBLICATION IF EXISTS missing"); err != nil {
		t.Errorf("DROP PUBLICATION IF EXISTS missing returned %v want nil", err)
	}
}

// TestDDLAlterTableDropColumn pins the table-rewrite DROP COLUMN implementation:
// rows written before ALTER TABLE DROP COLUMN must lose the dropped column slot;
// subsequent SELECTs return the surviving columns only. M0097-0023.
func TestDDLAlterTableDropColumn(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE dropcol (key int, drop1 int, keep1 text, keep2 int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "dropcol"})

	// Insert a row before drop.
	row := Row{NewIntDatum(1), NewIntDatum(99), NewStringDatum("hello"), NewIntDatum(42)}
	if err := writeHeapRow(ctx, ctx.Catalog.RelFileNode(tbl), tbl.Columns, row); err != nil {
		t.Fatalf("writeHeapRow: %v", err)
	}

	// Drop the middle column.
	if err := runDDL(t, ctx, "ALTER TABLE dropcol DROP COLUMN drop1"); err != nil {
		t.Fatalf("ALTER TABLE DROP COLUMN: %v", err)
	}
	tbl, _ = ctx.Catalog.LookupTable(parser.ObjectName{Name: "dropcol"})
	if len(tbl.Columns) != 3 {
		t.Fatalf("columns after DROP COLUMN: got %d want 3: %+v", len(tbl.Columns), tbl.Columns)
	}
	if tbl.Columns[0].Name != "key" || tbl.Columns[1].Name != "keep1" || tbl.Columns[2].Name != "keep2" {
		t.Fatalf("column names wrong: %+v", tbl.Columns)
	}

	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 1 {
		t.Fatalf("key=%+v want 1", rows[0][0])
	}
	if rows[0][1].Kind != KindString || rows[0][1].StringValue() != "hello" {
		t.Fatalf("keep1=%+v want 'hello'", rows[0][1])
	}
	if rows[0][2].Kind != KindInt || rows[0][2].Int != 42 {
		t.Fatalf("keep2=%+v want 42", rows[0][2])
	}
}
