package executor

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

// advanceStmtCounter advances the command counter between statements, matching
// PG's per-statement CommandCounterIncrement. GetCurrentCommandId(true) pins
// es_output_cid so cmin/cmax stamps carry the current command id. Test helpers
// that bypass the dispatch path must call this before each statement execution.
// M0129-S8.3.
func advanceStmtCounter(ctx *Context) {
	ctx.CommandCounterIncrement()
	ctx.CmdID = ctx.GetCurrentCommandId(true)
}

func runDDL(t *testing.T, ctx *Context, sql string) error {
	t.Helper()
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
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

// TestDDLCreateTableStorageClause verifies the CREATE TABLE column-level
// `STORAGE <mode>` clause (M0134-0002 C2 slice 9): a valid mode threads onto
// the catalog column, and a non-plain mode on a plain-storage type raises the
// PG-exact 0A000 error "column data type integer can only have storage PLAIN"
// (GetAttributeStorage, tablecmds.c:22082-22112).
func TestDDLCreateTableStorageClause(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// text is TOAST-aware (typstorage 'x'): STORAGE plain is accepted and the
	// mode threads onto the catalog column (attstorage reports it for pg_dump).
	if err := runDDL(t, ctx, "CREATE TABLE t (a text, c text storage plain)"); err != nil {
		t.Fatalf("CREATE TABLE (text storage plain): %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table t not in catalog")
	}
	if len(tbl.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(tbl.Columns))
	}
	if got := tbl.Columns[1].Storage; got != "plain" {
		t.Errorf("col c Storage=%q, want %q", got, "plain")
	}

	// int is plain-storage (typstorage 'p'): STORAGE extended is 0A000 with the
	// format_type_be-rendered type name ("integer", not "int4").
	err := runDDL(t, ctx, "CREATE TABLE f (a text, b int storage extended)")
	if err == nil {
		t.Fatal("expected error for int storage extended, got nil")
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *ExecError, got %T (%v)", err, err)
	}
	if ee.Code != "0A000" {
		t.Errorf("Code=%q, want %q", ee.Code, "0A000")
	}
	if ee.Message != "column data type integer can only have storage PLAIN" {
		t.Errorf("Message=%q, want %q", ee.Message, "column data type integer can only have storage PLAIN")
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

// TestDDLDropAggregateRemovesUserAggregate confirms DROP AGGREGATE actually
// removes a registered user aggregate from the catalog (loop #56 ledger
// resume point: previously this arm only validated arg types and always
// reported "does not exist", M0097-regress-era gap, never wired to
// catalog.InMemory.userAggregates at all).
func TestDDLDropAggregateRemovesUserAggregate(t *testing.T) {
	ctx, catIface, cleanup := newDDLFixture(t)
	defer cleanup()
	cat := catIface.(*catalog.InMemory)

	cat.RegisterUserAggregate(&catalog.UserAggregate{
		Name:     "newavg",
		ArgTypes: []string{"int4"},
		SType:    "_int8",
		SFunc:    "int4_avg_accum",
	})
	if _, ok := cat.LookupUserAggregateByName("newavg"); !ok {
		t.Fatal("aggregate not registered before DROP")
	}

	if err := runDDL(t, ctx, "DROP AGGREGATE newavg(int4)"); err != nil {
		t.Fatalf("DROP AGGREGATE: %v", err)
	}
	if _, ok := cat.LookupUserAggregateByName("newavg"); ok {
		t.Errorf("aggregate still registered after DROP AGGREGATE")
	}

	// A second DROP AGGREGATE without IF EXISTS must now hit the
	// "does not exist" error path (the aggregate is really gone).
	err := runDDL(t, ctx, "DROP AGGREGATE newavg(int4)")
	if err == nil {
		t.Fatal("DROP AGGREGATE on an already-dropped aggregate should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42883" {
		t.Errorf("want 42883, got %v", err)
	}

	// IF EXISTS on a missing aggregate is a no-op.
	if err := runDDL(t, ctx, "DROP AGGREGATE IF EXISTS newavg(int4)"); err != nil {
		t.Errorf("DROP AGGREGATE IF EXISTS on missing aggregate: %v", err)
	}
}

// TestDDLAlterAggregateOwner pins ALTER AGGREGATE ... OWNER TO (M0119-0004,
// loop #57 ledger follow-up): previously this form silently fell into the
// "other ALTER AGGREGATE forms: consume as no-op" parser branch and never
// reached the catalog at all.
func TestDDLAlterAggregateOwner(t *testing.T) {
	ctx, catIface, cleanup := newDDLFixture(t)
	defer cleanup()
	cat := catIface.(*catalog.InMemory)

	cat.RegisterUserAggregate(&catalog.UserAggregate{
		Name:     "newavg",
		ArgTypes: []string{"int4"},
		SType:    "_int8",
		SFunc:    "int4_avg_accum",
	})
	agg, ok := cat.LookupUserAggregateByName("newavg")
	if !ok {
		t.Fatal("aggregate not registered before ALTER")
	}
	if got := agg.OwnerOrDefault(); got != 10 {
		t.Fatalf("default owner = %d, want 10 (bootstrap superuser)", got)
	}

	cat.RegisterRole("agg_owner")
	wantOID, ok := cat.RoleOID("agg_owner")
	if !ok {
		t.Fatal("agg_owner role not registered")
	}

	if err := runDDL(t, ctx, "ALTER AGGREGATE newavg(int4) OWNER TO agg_owner"); err != nil {
		t.Fatalf("ALTER AGGREGATE OWNER TO: %v", err)
	}
	if got := agg.OwnerOrDefault(); got != wantOID {
		t.Errorf("owner after ALTER = %d, want %d", got, wantOID)
	}

	// Unknown role must raise 42704.
	err := runDDL(t, ctx, "ALTER AGGREGATE newavg(int4) OWNER TO no_such_role")
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("want 42704 for unknown role, got %v", err)
	}

	// Unknown aggregate must raise 42883.
	err = runDDL(t, ctx, "ALTER AGGREGATE no_such_agg(int4) OWNER TO agg_owner")
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42883" {
		t.Errorf("want 42883 for unknown aggregate, got %v", err)
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

// TestDDLFailedTempShadowCreateRestoresPermanentTable pins M0134-0018b: a
// CREATE TEMP TABLE that shadows-and-drops a same-named permanent table but
// then fails (self-shadowing CTAS — the SELECT can no longer resolve the
// permanent source it just dropped) must not strand the permanent table.
// Before the fix, execCreateTable dropped the permanent relation up front and
// only restored it from the DROP TABLE path, which is unreachable once the
// temp relation was never created — the permanent relation vanished from the
// live catalog for the rest of the process. docs/design/m0134-0018-temp-shadow-drop-rollback.md.
func TestDDLFailedTempShadowCreateRestoresPermanentTable(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE zz (i int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO zz (i) VALUES (1), (2)"); err != nil {
		t.Fatal(err)
	}

	err := runDDL(t, ctx, "CREATE TEMP TABLE zz AS SELECT * FROM public.zz")
	if err == nil {
		t.Fatal("self-shadowing CTAS should error (source relation was shadow-dropped)")
	}

	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "zz"}); !ok {
		t.Fatalf("permanent table zz was lost after the failing CREATE TEMP TABLE errored: %v", err)
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

	scan := newSeqScanOp(&optimizer.SeqScan{Table: tbl})
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
	tree, err := openIndexBTree(ctx, idx, rel)
	if err != nil {
		t.Fatalf("openIndexBTree: %v", err)
	}
	for _, k := range []int32{1, 2, 3} {
		if _, found, err := tree.Search(indexProbeForTest(t, ctx, idx, Datum{Kind: KindInt, Int: int64(k)})); err != nil || !found {
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

	scan := newSeqScanOp(&optimizer.SeqScan{Table: tbl})
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

	scan := newSeqScanOp(&optimizer.SeqScan{Table: tbl})
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
	tree, err := openIndexBTree(ctx, idx, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := tree.Search(indexProbeForTest(t, ctx, idx, Datum{Kind: KindInt, Int: 2})); err != nil || !found {
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
	// M0127-P5.9: install the live block-count reader the server installs
	// (`initdb.Open`, open.go), so a fixture table with rows on disk plans
	// like one. Without it `RelationBlocks` answers "no estimate" and the
	// relation-size fallback (M0125-0003) — goopg's `estimate_rel_size` —
	// cannot run, so every never-ANALYZEd fixture relation is sized at the
	// 1-row floor. That was invisible while the default enumerator promoted
	// operators by RULE; the PG-shaped search picks them by COST, so a blind
	// fixture silently plans nested loops over thousands of rows and the test
	// measures the fixture rather than the engine.
	cat.SetRelationSizer(func(rfn storage.RelFileNode) (int64, bool) {
		n, err := pool.NBlocks(rfn)
		if err != nil {
			return 0, false
		}
		return int64(n), true
	})
	mgrMVCC := transam.NewManager()
	tx, err := mgrMVCC.Begin(transam.IsolationReadCommitted)
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

// TestDDLAlterTableRowSecurityRoundTrip pins DU-002 slice 322: ENABLE/DISABLE
// ROW LEVEL SECURITY toggles catalog.Table.RowSecurity (pg_class.relrowsecurity)
// and [NO] FORCE ROW LEVEL SECURITY toggles ForceRowSecurity
// (relforcerowsecurity). goopg enforces no RLS — these are recorded purely so
// pg_dump can re-emit the clauses. The two flags are independent.
func TestDDLAlterTableRowSecurityRoundTrip(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table t not in catalog")
	}

	if tbl.RowSecurity || tbl.ForceRowSecurity {
		t.Fatalf("fresh table: RowSecurity=%v ForceRowSecurity=%v, want both false", tbl.RowSecurity, tbl.ForceRowSecurity)
	}

	if err := runDDL(t, ctx, "ALTER TABLE t ENABLE ROW LEVEL SECURITY"); err != nil {
		t.Fatalf("ENABLE ROW LEVEL SECURITY: %v", err)
	}
	if !tbl.RowSecurity || tbl.ForceRowSecurity {
		t.Errorf("after ENABLE: RowSecurity=%v ForceRowSecurity=%v, want true/false", tbl.RowSecurity, tbl.ForceRowSecurity)
	}

	if err := runDDL(t, ctx, "ALTER TABLE t FORCE ROW LEVEL SECURITY"); err != nil {
		t.Fatalf("FORCE ROW LEVEL SECURITY: %v", err)
	}
	if !tbl.RowSecurity || !tbl.ForceRowSecurity {
		t.Errorf("after FORCE: RowSecurity=%v ForceRowSecurity=%v, want true/true", tbl.RowSecurity, tbl.ForceRowSecurity)
	}

	if err := runDDL(t, ctx, "ALTER TABLE t NO FORCE ROW LEVEL SECURITY"); err != nil {
		t.Fatalf("NO FORCE ROW LEVEL SECURITY: %v", err)
	}
	if !tbl.RowSecurity || tbl.ForceRowSecurity {
		t.Errorf("after NO FORCE: RowSecurity=%v ForceRowSecurity=%v, want true/false", tbl.RowSecurity, tbl.ForceRowSecurity)
	}

	if err := runDDL(t, ctx, "ALTER TABLE t DISABLE ROW LEVEL SECURITY"); err != nil {
		t.Fatalf("DISABLE ROW LEVEL SECURITY: %v", err)
	}
	if tbl.RowSecurity || tbl.ForceRowSecurity {
		t.Errorf("after DISABLE: RowSecurity=%v ForceRowSecurity=%v, want both false", tbl.RowSecurity, tbl.ForceRowSecurity)
	}
}

// TestDDLCreatePolicyRoundTrip pins that CREATE POLICY records a row-security
// policy on its table and that the pg_policy virtual catalog projects it in the
// shape pg_dump's getPolicies reads: polcmd, polpermissive, polroles ({0} for
// PUBLIC), and the fully-parenthesized polqual / polwithcheck pg_get_expr
// renders. goopg does NOT enforce RLS — this is dump fidelity only. DU-002
// slice 323.
func TestDDLCreatePolicyRoundTrip(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table t not in catalog")
	}

	if err := runDDL(t, ctx, "CREATE POLICY p_simple ON t USING (a > 0)"); err != nil {
		t.Fatalf("CREATE POLICY p_simple: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE POLICY p_restr ON t AS RESTRICTIVE FOR SELECT USING (a > 5)"); err != nil {
		t.Fatalf("CREATE POLICY p_restr: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE POLICY p_check ON t FOR INSERT WITH CHECK (a < 100)"); err != nil {
		t.Fatalf("CREATE POLICY p_check: %v", err)
	}
	if len(tbl.Policies) != 3 {
		t.Fatalf("got %d policies, want 3", len(tbl.Policies))
	}

	// Project through the virtual catalog and key the rows by polname.
	pgPolicy, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_policy"})
	if !ok {
		t.Fatal("pg_policy not in catalog")
	}
	rows := map[string][]string{}
	for _, r := range pgPolicy.VirtualRows() {
		rows[r[1]] = r // r[1] = polname
	}
	if len(rows) != 3 {
		t.Fatalf("pg_policy projected %d rows, want 3", len(rows))
	}

	// columns: oid, polname, polrelid, polcmd, polpermissive, polroles, polqual, polwithcheck
	check := func(name string, cmd, perm, roles, qual, withcheck string) {
		t.Helper()
		r := rows[name]
		if r == nil {
			t.Fatalf("policy %q missing from pg_policy", name)
		}
		if r[3] != cmd {
			t.Errorf("%s polcmd = %q, want %q", name, r[3], cmd)
		}
		if r[4] != perm {
			t.Errorf("%s polpermissive = %q, want %q", name, r[4], perm)
		}
		if r[5] != roles {
			t.Errorf("%s polroles = %q, want %q", name, r[5], roles)
		}
		if r[6] != qual {
			t.Errorf("%s polqual = %q, want %q", name, r[6], qual)
		}
		if r[7] != withcheck {
			t.Errorf("%s polwithcheck = %q, want %q", name, r[7], withcheck)
		}
	}
	// pg_get_expr is a pass-through in goopg; polqual stores the fully
	// parenthesized form so pg_dump emits `USING ((a > 0))`.
	check("p_simple", "*", "t", "{0}", "(a > 0)", "")
	check("p_restr", "r", "f", "{0}", "(a > 5)", "")
	check("p_check", "a", "t", "{0}", "", "(a < 100)")

	// DROP POLICY removes it.
	if err := runDDL(t, ctx, "DROP POLICY p_restr ON t"); err != nil {
		t.Fatalf("DROP POLICY p_restr: %v", err)
	}
	if len(tbl.Policies) != 2 {
		t.Fatalf("after DROP: got %d policies, want 2", len(tbl.Policies))
	}
	// Named-role TO clause is not yet supported (no per-role OID registry).
	if err := runDDL(t, ctx, "CREATE POLICY p_role ON t TO alice USING (a > 0)"); err == nil {
		t.Errorf("CREATE POLICY ... TO alice should error (named roles unsupported)")
	}
}

// TestDDLCreateRuleRoundTrip verifies an unconditional DO-NOTHING CREATE RULE
// is recorded on its table, projected into pg_rewrite, reconstructed by
// pg_get_ruledef byte-identically to real PG, and removed by DROP RULE. DU-002
// slice 324.
func TestDDLCreateRuleRoundTrip(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table t not in catalog")
	}

	if err := runDDL(t, ctx, "CREATE RULE r_noins AS ON INSERT TO t DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("CREATE RULE r_noins: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE RULE r_also AS ON UPDATE TO t DO ALSO NOTHING"); err != nil {
		t.Fatalf("CREATE RULE r_also: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE RULE r_del AS ON DELETE TO t DO NOTHING"); err != nil {
		t.Fatalf("CREATE RULE r_del: %v", err)
	}
	if len(tbl.Rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(tbl.Rules))
	}

	// Duplicate rule name on the same table is a 42710 error.
	if err := runDDL(t, ctx, "CREATE RULE r_noins AS ON INSERT TO t DO INSTEAD NOTHING"); err == nil {
		t.Errorf("duplicate CREATE RULE r_noins should error")
	}

	// Project through pg_rewrite and key rows by rulename.
	pgRewrite, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_rewrite"})
	if !ok {
		t.Fatal("pg_rewrite not in catalog")
	}
	rows := map[string][]string{}
	for _, r := range pgRewrite.VirtualRows() {
		rows[r[1]] = r // r[1] = rulename
	}
	if len(rows) != 3 {
		t.Fatalf("pg_rewrite projected %d rows, want 3", len(rows))
	}
	// columns: oid, rulename, ev_class, ev_type, ev_enabled, is_instead, ev_qual, ev_action
	check := func(name, evType, isInstead string) {
		t.Helper()
		r := rows[name]
		if r == nil {
			t.Fatalf("rule %q missing from pg_rewrite", name)
		}
		if r[3] != evType {
			t.Errorf("%s ev_type = %q, want %q", name, r[3], evType)
		}
		if r[4] != "O" {
			t.Errorf("%s ev_enabled = %q, want %q", name, r[4], "O")
		}
		if r[5] != isInstead {
			t.Errorf("%s is_instead = %q, want %q", name, r[5], isInstead)
		}
	}
	check("r_noins", "3", "t") // INSERT, INSTEAD
	check("r_also", "2", "f")  // UPDATE, ALSO
	check("r_del", "4", "f")   // DELETE, plain DO NOTHING

	// pg_get_ruledef reconstruction is byte-identical to PG 18.3.
	want := map[string]string{
		"r_noins": "CREATE RULE r_noins AS\n    ON INSERT TO public.t DO INSTEAD NOTHING;",
		"r_also":  "CREATE RULE r_also AS\n    ON UPDATE TO public.t DO NOTHING;",
		"r_del":   "CREATE RULE r_del AS\n    ON DELETE TO public.t DO NOTHING;",
	}
	for _, r := range tbl.Rules {
		got := buildRuleDefString(tbl, r)
		if got != want[r.Name] {
			t.Errorf("pg_get_ruledef(%s) =\n%q\nwant\n%q", r.Name, got, want[r.Name])
		}
	}

	// DROP RULE removes it from both the compat registry and the dumped set.
	if err := runDDL(t, ctx, "DROP RULE r_also ON t"); err != nil {
		t.Fatalf("DROP RULE r_also: %v", err)
	}
	if len(tbl.Rules) != 2 {
		t.Fatalf("after DROP: got %d rules, want 2", len(tbl.Rules))
	}
}

// TestDDLAlterRuleRename verifies `ALTER RULE name ON table RENAME TO
// newname` renames the catalog.RuleInfo entry in place, and errors 42704 for
// a nonexistent rule name. Mirrors
// postgres/src/backend/rewrite/rewriteDefine.c:793 RenameRewriteRule.
// M0134-0065.
func TestDDLAlterRuleRename(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table t not in catalog")
	}
	if err := runDDL(t, ctx, "CREATE RULE somerule AS ON INSERT TO t DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("CREATE RULE somerule: %v", err)
	}

	// Success: renames the RuleInfo in place.
	if err := runDDL(t, ctx, "ALTER RULE somerule ON t RENAME TO newname"); err != nil {
		t.Fatalf("ALTER RULE ... RENAME TO: %v", err)
	}
	if len(tbl.Rules) != 1 || tbl.Rules[0].Name != "newname" {
		t.Fatalf("after rename: tbl.Rules = %+v, want single rule named newname", tbl.Rules)
	}

	// Nonexistent rule name is 42704.
	err := runDDL(t, ctx, "ALTER RULE missing ON t RENAME TO x")
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Fatalf("ALTER RULE missing ... RENAME TO: err = %v, want *ExecError{Code: 42704}", err)
	}

	// Renaming to a name that collides with another rule on the same table
	// is 42710.
	if err := runDDL(t, ctx, "CREATE RULE other AS ON UPDATE TO t DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("CREATE RULE other: %v", err)
	}
	err = runDDL(t, ctx, "ALTER RULE newname ON t RENAME TO other")
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42710" {
		t.Fatalf("ALTER RULE ... RENAME TO (duplicate): err = %v, want *ExecError{Code: 42710}", err)
	}
}

// TestDDLCreateRuleConditionalRoundTrip verifies a conditional DO-NOTHING
// CREATE RULE (`WHERE (qual) DO INSTEAD NOTHING`) records its deparsed qual and
// pg_get_ruledef reconstructs the WHERE clause byte-identically to real PG 18.3
// (captured from a live PG cluster). DU-002 slice 359.
func TestDDLCreateRuleConditionalRoundTrip(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE rcond (a int, b int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "rcond"})
	if !ok {
		t.Fatal("table rcond not in catalog")
	}

	// Parenthesized qual (UPDATE) and no-paren qual (DELETE); both must deparse to
	// the canonical single-paren WHERE form.
	if err := runDDL(t, ctx, "CREATE RULE rcond_upd AS ON UPDATE TO rcond WHERE (old.a <> new.a) DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("CREATE RULE rcond_upd: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE RULE rcond_del AS ON DELETE TO rcond WHERE old.b > 0 DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("CREATE RULE rcond_del: %v", err)
	}

	want := map[string]string{
		"rcond_upd": "CREATE RULE rcond_upd AS\n    ON UPDATE TO public.rcond\n   WHERE (old.a <> new.a) DO INSTEAD NOTHING;",
		"rcond_del": "CREATE RULE rcond_del AS\n    ON DELETE TO public.rcond\n   WHERE (old.b > 0) DO INSTEAD NOTHING;",
	}
	if len(tbl.Rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(tbl.Rules))
	}
	for _, r := range tbl.Rules {
		if r.Qual == "" {
			t.Errorf("rule %s has empty Qual", r.Name)
		}
		got := buildRuleDefString(tbl, r)
		if got != want[r.Name] {
			t.Errorf("pg_get_ruledef(%s) =\n%q\nwant\n%q", r.Name, got, want[r.Name])
		}
	}
}

// TestDDLAlterTableRuleEnabledState verifies `ALTER TABLE … {ENABLE|DISABLE}
// [REPLICA|ALWAYS] RULE name` sets pg_rewrite.ev_enabled for the named rule
// (origin 'O', disabled 'D', replica 'R', always 'A') so pg_dump's dumpRule
// re-emits the ALTER TABLE clause, and that an unknown rule name errors 42704.
// DU-002 slice 325.
func TestDDLAlterTableRuleEnabledState(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE RULE r_ins AS ON INSERT TO t DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("CREATE RULE r_ins: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE RULE r_upd AS ON UPDATE TO t DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("CREATE RULE r_upd: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE RULE r_del AS ON DELETE TO t DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("CREATE RULE r_del: %v", err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "t"})

	// A freshly created rule is ev_enabled 'O' (EvEnabled maps the zero value).
	for _, r := range tbl.Rules {
		if r.EvEnabled() != 'O' {
			t.Errorf("fresh rule %s EvEnabled=%q want 'O'", r.Name, r.EvEnabled())
		}
	}

	if err := runDDL(t, ctx, "ALTER TABLE t DISABLE RULE r_ins"); err != nil {
		t.Fatalf("DISABLE RULE r_ins: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE t ENABLE REPLICA RULE r_upd"); err != nil {
		t.Fatalf("ENABLE REPLICA RULE r_upd: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE t ENABLE ALWAYS RULE r_del"); err != nil {
		t.Fatalf("ENABLE ALWAYS RULE r_del: %v", err)
	}

	// pg_rewrite.ev_enabled (column index 4) reflects the new states live.
	pgRewrite, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_rewrite"})
	enabled := map[string]string{}
	for _, r := range pgRewrite.VirtualRows() {
		enabled[r[1]] = r[4]
	}
	if enabled["r_ins"] != "D" {
		t.Errorf("r_ins ev_enabled=%q want D", enabled["r_ins"])
	}
	if enabled["r_upd"] != "R" {
		t.Errorf("r_upd ev_enabled=%q want R", enabled["r_upd"])
	}
	if enabled["r_del"] != "A" {
		t.Errorf("r_del ev_enabled=%q want A", enabled["r_del"])
	}

	// ENABLE RULE restores 'O' (origin).
	if err := runDDL(t, ctx, "ALTER TABLE t ENABLE RULE r_ins"); err != nil {
		t.Fatalf("ENABLE RULE r_ins: %v", err)
	}
	enabled = map[string]string{}
	for _, r := range pgRewrite.VirtualRows() {
		enabled[r[1]] = r[4]
	}
	if enabled["r_ins"] != "O" {
		t.Errorf("r_ins ev_enabled after ENABLE=%q want O", enabled["r_ins"])
	}

	// Unknown rule name → 42704.
	err := runDDL(t, ctx, "ALTER TABLE t DISABLE RULE no_such_rule")
	if err == nil {
		t.Fatal("DISABLE RULE no_such_rule should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("DISABLE RULE no_such_rule err=%v want ExecError 42704", err)
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

	// M0129-S8.3: advance the command counter so the scan sees the heap row.
	advanceStmtCounter(ctx)
	scan := newSeqScanOp(&optimizer.SeqScan{Table: tbl})
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

// TestUpdateViaIndexFansOutToInheritanceChild closes the M0119-0004
// discovery recorded in `.ralph/deferral_ledger.md` (root-0025 item 5
// follow-up): updateOp.Next took the IndexScan fast path
// (updateViaIndex) whenever the planner produced one, regardless of
// whether the target table had partition/inheritance children.
// updateViaIndex only ever walks a single B-tree scoped to the named
// table's own storage, so a row that in fact lives in a plain-INHERITS
// child was silently left untouched — `UPDATE parent SET ... WHERE id =
// X` (id being the child's PK, hitting the index path) reported success
// but performed no write. This mirrors PostgreSQL's requirement that an
// UPDATE against an inheritance-parent target update every row in the
// hierarchy that matches the WHERE clause, not just rows physically
// stored in the named table.
func TestUpdateViaIndexFansOutToInheritanceChild(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	must("CREATE TABLE uvix_parent (id int primary key, val int)")
	must("CREATE TABLE uvix_child (extra text) INHERITS (uvix_parent)")
	must("INSERT INTO uvix_child (id, val, extra) VALUES (1, 20, 'x')")

	// WHERE id = 1 matches uvix_parent's PK index, so the planner picks
	// an IndexScan and updateOp.Next takes the updateViaIndex fast path.
	must("UPDATE uvix_parent SET val = 99 WHERE id = 1")

	rows := runQueryRows(t, ctx, "SELECT val, extra FROM uvix_child WHERE id = 1")
	if len(rows) != 1 {
		t.Fatalf("uvix_child row count = %d, want 1 (row must survive the UPDATE)", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 99 {
		t.Fatalf("uvix_child.val = %+v, want 99 (indexed UPDATE on the inheritance parent must fan out to the child)", rows[0][0])
	}
	if rows[0][1].Kind != KindString || rows[0][1].StringValue() != "x" {
		t.Fatalf("uvix_child.extra = %+v, want unchanged 'x'", rows[0][1])
	}

	// The parent's own storage never had a row for id=1, so a direct
	// SELECT against uvix_parent alone (not through the inheritance
	// expansion) should still find nothing — confirms the row really
	// lives only in the child, exercising the gap this test targets.
	rows = runQueryRows(t, ctx, "SELECT val FROM ONLY uvix_parent WHERE id = 1")
	if len(rows) != 0 {
		t.Fatalf("ONLY uvix_parent unexpectedly has a row for id=1: %v", rows)
	}
}

// TestUpdateViaIndexFansOutToInheritanceChildWithParentRow additionally
// covers the case where the parent ALSO owns a row with a distinct key,
// bounding that the fix above does not regress the (already-working)
// direct-parent-row case while a sibling child row exists.
func TestUpdateViaIndexFansOutToInheritanceChildWithParentRow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	must("CREATE TABLE uvix2_parent (id int primary key, val int)")
	must("CREATE TABLE uvix2_child (extra text) INHERITS (uvix2_parent)")
	must("INSERT INTO uvix2_parent (id, val) VALUES (1, 10)")
	must("INSERT INTO uvix2_child (id, val, extra) VALUES (2, 20, 'x')")

	must("UPDATE uvix2_parent SET val = val + 1 WHERE id = 1")
	must("UPDATE uvix2_parent SET val = val + 1 WHERE id = 2")

	rows := runQueryRows(t, ctx, "SELECT val FROM uvix2_parent WHERE id = 1")
	if len(rows) != 1 || rows[0][0].Format() != "11" {
		t.Fatalf("direct parent-row PK-equality UPDATE regressed: got %v", rows)
	}
	rows = runQueryRows(t, ctx, "SELECT val FROM uvix2_child WHERE id = 2")
	if len(rows) != 1 || rows[0][0].Format() != "21" {
		t.Fatalf("child-row PK-equality UPDATE via parent name must still fan out: got %v", rows)
	}
}

// TestSelectIndexScanFansOutToInheritanceChild closes the SELECT-side twin
// of the updateViaIndex inheritance-child fan-out gap (root-0026 discovery,
// M0119-0004): the planner's IndexScan path (planIndexScanFromWhere,
// internal/planner/planner.go) opened exactly one B-tree scoped to the
// named table's own storage whenever a WHERE clause matched an equality or
// range predicate on an indexed column, with no awareness of plain-INHERITS
// children — unlike the Filter+UNION ALL fallback planScanRangeVar already
// builds for a non-indexed predicate. `SELECT ... FROM parent WHERE
// indexed_col = X` (no ONLY) therefore silently returned zero rows for a
// row that in fact lives only in a child's own heap file, while the
// identical query against a non-indexed column correctly found it.
func TestSelectIndexScanFansOutToInheritanceChild(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	must("CREATE TABLE sixc_parent (id int primary key, val int)")
	must("CREATE TABLE sixc_child (extra text) INHERITS (sixc_parent)")
	// This row lives ONLY in the child's own heap file.
	must("INSERT INTO sixc_child (id, val, extra) VALUES (1, 20, 'x')")

	// `WHERE id = 1` is an equality predicate on the PK — the planner used
	// to pick an IndexScan here (planIndexScanFromWhere), which pre-fix
	// never consulted inheritance children.
	rows := runQueryRows(t, ctx, "SELECT val FROM sixc_parent WHERE id = 1")
	if len(rows) != 1 {
		t.Fatalf("indexed-predicate SELECT must fan out to inheritance children: got %d rows, want 1", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 20 {
		t.Fatalf("val=%+v want 20", rows[0][0])
	}

	// A range predicate on the same indexed column must fan out too
	// (tryRangeIndexScan, reached from the same planIndexScanFromWhere gate).
	rows = runQueryRows(t, ctx, "SELECT val FROM sixc_parent WHERE id > 0 AND id < 5")
	if len(rows) != 1 {
		t.Fatalf("indexed range-predicate SELECT must fan out to inheritance children: got %d rows, want 1", len(rows))
	}

	// FROM ONLY must still exclude the child (PostgreSQL semantics) — the
	// index path is safe to take here since there is nothing to fan out to.
	rows = runQueryRows(t, ctx, "SELECT val FROM ONLY sixc_parent WHERE id = 1")
	if len(rows) != 0 {
		t.Fatalf("FROM ONLY must not see child rows: got %v", rows)
	}
}
