package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

// newStorageFixture spins up a per-test buffer pool + manager + mvcc
// manager + catalog with one table seeded, ready to run heap-touching
// operators against.
func newStorageFixture(t *testing.T) (*Context, catalog.Catalog, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
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

// TestInsertThenSeqScanRoundTrip pins the heap-write/heap-read
// contract: rows inserted within the active transaction are visible
// to a SeqScan running under the same xid+snapshot.
func TestInsertThenSeqScanRoundTrip(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	// Hand-build an Insert plan rather than going through the parser
	// so this test focuses on executor mechanics, not name resolution.
	insertPlan := &optimizer.Insert{
		Table: tbl,
		Source: &optimizer.Values{
			Rows: [][]optimizer.Expr{
				{&optimizer.IntegerConst{Value: 1}, &optimizer.StringConst{Value: "alpha"}},
				{&optimizer.IntegerConst{Value: 2}, &optimizer.StringConst{Value: "beta"}},
				{&optimizer.IntegerConst{Value: 3}, &optimizer.StringConst{Value: "gamma"}},
			},
		},
		ColumnIndex: []int{0, 1},
	}
	op, err := Build(insertPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Insert.Next: %v", err)
	}
	if io := op.(*insertOp); io.RowsAffected() != 3 {
		t.Errorf("RowsAffected=%d want 3", io.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatal(err)
	}

	// SeqScan back. Refresh the snapshot so our own writes are visible
	// — the insert above used currentXID = ctx.Tx.XID, which is the
	// same xid we'll scan with. mvcc.TupleVisible's "same xact" branch
	// returns true regardless of snapshot.
	scan := newSeqScanOp(&optimizer.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("scan returned %d rows want 3", len(rows))
	}
	for i, want := range []struct {
		id    int64
		label string
	}{{1, "alpha"}, {2, "beta"}, {3, "gamma"}} {
		if rows[i][0].Int != want.id || rows[i][1].StringValue() != want.label {
			t.Errorf("rows[%d]=%+v want id=%d label=%q", i, rows[i], want.id, want.label)
		}
	}
}

// TestSeqScanRespectsVisibility: a tuple inserted by an aborted xact
// should NOT be returned to a fresh transaction.
func TestSeqScanRespectsVisibility(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	// Insert one row under ctx.Tx, then "abort" by rolling back.
	in := &optimizer.Insert{
		Table: tbl,
		Source: &optimizer.Values{
			Rows: [][]optimizer.Expr{{&optimizer.IntegerConst{Value: 99}, &optimizer.StringConst{Value: "ghost"}}},
		},
		ColumnIndex: []int{0, 1},
	}
	op, _ := Build(in)
	_ = op.Open(ctx)
	_, _ = op.Next()
	_ = op.Close()
	if err := ctx.TxnMgr.Rollback(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	// Open a fresh transaction. The aborted insert's xid is now tracked
	// in the snapshot's Aborted list (M0100-0002), so the ghost row is
	// correctly invisible (0 rows).
	tx2, _ := ctx.TxnMgr.Begin(transam.IsolationReadCommitted)
	defer ctx.TxnMgr.Rollback(tx2)
	snap2, _ := ctx.TxnMgr.SnapshotFor(tx2)
	ctx.Tx = tx2
	ctx.Snap = snap2

	scan := newSeqScanOp(&optimizer.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("scan returned %d rows after rollback; aborted rows must be invisible (M0100-0002)", len(rows))
	}
}

// TestInsertExtendsRelation checks that we extend through PinNew
// when the last block can't fit a new tuple — exercised by inserting
// enough rows to require multiple pages.
func TestInsertExtendsRelation(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	rel := cat.RelFileNode(tbl)

	const N = 600 // each row is ~34 bytes payload + tuple header; ~200/page
	rows := make([][]optimizer.Expr, N)
	for i := 0; i < N; i++ {
		rows[i] = []optimizer.Expr{
			&optimizer.IntegerConst{Value: int64(i)},
			&optimizer.StringConst{Value: "row-text-payload-padding"},
		}
	}
	in := &optimizer.Insert{
		Table:       tbl,
		Source:      &optimizer.Values{Rows: rows},
		ColumnIndex: []int{0, 1},
	}
	op, _ := Build(in)
	_ = op.Open(ctx)
	_, _ = op.Next()
	_ = op.Close()

	n, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Errorf("relation has %d blocks; expected extension", n)
	}

	// Round-trip count check.
	scan := newSeqScanOp(&optimizer.SeqScan{Table: tbl})
	_ = scan.Open(ctx)
	defer scan.Close()
	got, _ := drainScan(scan)
	if len(got) != N {
		t.Errorf("scanned %d rows want %d", len(got), N)
	}
}

// TestInsertFillsMissingColumnDefault pins M0103-0007 rung 14: a regular
// dispatcher INSERT that omits a column from the explicit column list
// must fill the omitted column with its CREATE TABLE DEFAULT expression
// before writing to the heap, mirroring upstream's ExecComputeStoredGenerated
// pre-insert pass and matching the apply worker's rung-13 behavior. The
// fixture builds a table (id, label, note) where `note` carries a string
// literal DEFAULT, runs INSERT with ColumnIndex=[0,1] (the SOURCE row
// supplies only id and label), and asserts the persisted row carries
// the DEFAULT value in the note slot.
//
// Each assertion fail-fasts a distinct regression: row-count = 1 catches
// the INSERT not firing; note == "auto" catches the DEFAULT-fill loop
// not running; not-NULL check on note catches the rung-13 helper being
// called with the wrong missing mask (every slot false ⇒ no fills).
func TestInsertFillsMissingColumnDefault(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	// Register a second table with a DEFAULT on the omitted column.
	cim := cat.(*catalog.InMemory)
	if _, err := cim.CreateTable(parser.ObjectName{Name: "withdefault"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: catalog.Type{Name: "text"}},
		// note carries a DEFAULT that must be evaluated when the column
		// is omitted from the INSERT's column list.
		{Name: "note", Type: catalog.Type{Name: "text"},
			DefaultExpr: &parser.StringConst{Value: "auto"}},
		// bare has no DEFAULT — must stay NullDatum.
		{Name: "bare", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "withdefault"})

	insertPlan := &optimizer.Insert{
		Table: tbl,
		Source: &optimizer.Values{
			Rows: [][]optimizer.Expr{
				{&optimizer.IntegerConst{Value: 1}, &optimizer.StringConst{Value: "one"}},
			},
		},
		// INSERT INTO withdefault (id, label) VALUES (1, 'one') — note and
		// bare are absent from the source row.
		ColumnIndex: []int{0, 1},
	}
	op, err := Build(insertPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Insert.Next: %v", err)
	}
	if io := op.(*insertOp); io.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1", io.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatal(err)
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
		t.Fatalf("scan returned %d rows want 1", len(rows))
	}
	got := rows[0]
	if got[0].Int != 1 {
		t.Errorf("id: got %v want 1", got[0].Int)
	}
	if got[1].StringValue() != "one" {
		t.Errorf("label: got %q want %q", got[1].StringValue(), "one")
	}
	if got[2].IsNull() {
		t.Errorf("note: got NULL want %q — DEFAULT not filled", "auto")
	} else if got[2].StringValue() != "auto" {
		t.Errorf("note: got %q want %q", got[2].StringValue(), "auto")
	}
	if !got[3].IsNull() {
		t.Errorf("bare: got %v want NULL — column without DEFAULT must stay NULL", got[3])
	}
}

// TestInsertDoesNotOverrideExplicitColumnDefault pins the negative case:
// when an INSERT explicitly supplies a value for a column that also
// carries a CREATE TABLE DEFAULT, the explicit value wins (the DEFAULT
// is NOT applied). missing[i]=false for that column ⇒ applyDefaultsForMissing
// must skip it. Without this guard, INSERT INTO t (id, note) VALUES
// (1, 'explicit') would silently overwrite 'explicit' with 'auto'.
func TestInsertDoesNotOverrideExplicitColumnDefault(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	cim := cat.(*catalog.InMemory)
	if _, err := cim.CreateTable(parser.ObjectName{Name: "withdefault2"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "note", Type: catalog.Type{Name: "text"},
			DefaultExpr: &parser.StringConst{Value: "auto"}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "withdefault2"})

	insertPlan := &optimizer.Insert{
		Table: tbl,
		Source: &optimizer.Values{
			Rows: [][]optimizer.Expr{
				{&optimizer.IntegerConst{Value: 1}, &optimizer.StringConst{Value: "explicit"}},
			},
		},
		// INSERT INTO withdefault2 (id, note) VALUES (1, 'explicit') — both
		// columns are claimed, so the DEFAULT must NOT override.
		ColumnIndex: []int{0, 1},
	}
	op, _ := Build(insertPlan)
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Insert.Next: %v", err)
	}
	_ = op.Close()

	scan := newSeqScanOp(&optimizer.SeqScan{Table: tbl})
	_ = scan.Open(ctx)
	defer scan.Close()
	rows, _ := drainScan(scan)
	if len(rows) != 1 {
		t.Fatalf("scan returned %d rows want 1", len(rows))
	}
	if got := rows[0][1].StringValue(); got != "explicit" {
		t.Errorf("note: got %q want %q — explicit value must beat DEFAULT", got, "explicit")
	}
}


// TestInsertFillsMissingColumnDefaultCurrentTimestamp pins M0103-0007 rung 18:
// a CREATE TABLE DEFAULT of current_timestamp (parser shape: *parser.FuncCall
// with Name.Name=="current_timestamp", zero Args) must evaluate to wall-clock
// at INSERT time and store a non-NULL KindTime Datum on every row that
// omitted the column. Bounded-skew window guards both correctness (the slot
// didn't get a fixed sentinel or init()-time clock) and order (the helper
// ran before the heap write, not after).
func TestInsertFillsMissingColumnDefaultCurrentTimestamp(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	cim := cat.(*catalog.InMemory)
	// DEFAULT current_timestamp — parser emits FuncCall with no Args.
	defExpr := &parser.FuncCall{
		Name: parser.ObjectName{Name: "current_timestamp"},
	}
	if _, err := cim.CreateTable(parser.ObjectName{Name: "audit_ts"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "created_at", Type: catalog.Type{Name: "timestamptz"},
			DefaultExpr: defExpr},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "audit_ts"})

	insertPlan := &optimizer.Insert{
		Table: tbl,
		Source: &optimizer.Values{
			Rows: [][]optimizer.Expr{
				{&optimizer.IntegerConst{Value: 1}},
			},
		},
		// INSERT INTO audit_ts (id) VALUES (1) — created_at omitted, DEFAULT fires.
		ColumnIndex: []int{0},
	}
	op, err := Build(insertPlan)
	if err != nil {
		t.Fatal(err)
	}

	before := time.Now().UTC()
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Insert.Next: %v", err)
	}
	_ = op.Close()
	after := time.Now().UTC()

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
		t.Fatalf("scan returned %d rows want 1", len(rows))
	}
	got := rows[0]
	if got[1].IsNull() {
		t.Fatalf("created_at: got NULL — DEFAULT current_timestamp not evaluated")
	}
	if got[1].Kind != KindTime {
		t.Fatalf("created_at.Kind: got %v want KindTime", got[1].Kind)
	}
	gotTime := got[1].TimeValue()
	// Allow 1 ms slop on each side for clock-resolution differences between
	// the pre-Open before-stamp and the actual evalGenFuncCall call site.
	loBound := before.Add(-1 * time.Millisecond)
	hiBound := after.Add(1 * time.Millisecond)
	if gotTime.Before(loBound) || gotTime.After(hiBound) {
		t.Errorf("created_at: got %v, want in [%v, %v]", gotTime, loBound, hiBound)
	}
}

// TestInsertFillsMissingColumnDefaultCurrentDate pins M0103-0007 rung 18:
// DEFAULT current_date evaluates to today at midnight UTC. The
// midnight-truncation pin catches an accidental fallthrough to the
// current_timestamp arm.
func TestInsertFillsMissingColumnDefaultCurrentDate(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	cim := cat.(*catalog.InMemory)
	defExpr := &parser.FuncCall{
		Name: parser.ObjectName{Name: "current_date"},
	}
	if _, err := cim.CreateTable(parser.ObjectName{Name: "audit_date"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "ymd", Type: catalog.Type{Name: "date"},
			DefaultExpr: defExpr},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "audit_date"})

	insertPlan := &optimizer.Insert{
		Table: tbl,
		Source: &optimizer.Values{
			Rows: [][]optimizer.Expr{
				{&optimizer.IntegerConst{Value: 1}},
			},
		},
		ColumnIndex: []int{0},
	}
	op, err := Build(insertPlan)
	if err != nil {
		t.Fatal(err)
	}
	expected := time.Now().UTC()
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Insert.Next: %v", err)
	}
	_ = op.Close()

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
		t.Fatalf("scan returned %d rows want 1", len(rows))
	}
	got := rows[0][1]
	if got.IsNull() {
		t.Fatalf("ymd: got NULL — DEFAULT current_date not evaluated")
	}
	if got.Kind != KindTime {
		t.Fatalf("ymd.Kind: got %v want KindTime", got.Kind)
	}
	gotTime := got.TimeValue()
	if gotTime.Hour() != 0 || gotTime.Minute() != 0 || gotTime.Second() != 0 || gotTime.Nanosecond() != 0 {
		t.Errorf("ymd: got %v want midnight-truncated", gotTime)
	}
	if gotTime.Year() != expected.Year() || gotTime.Month() != expected.Month() || gotTime.Day() != expected.Day() {
		t.Errorf("ymd: got date %d-%02d-%02d want %d-%02d-%02d",
			gotTime.Year(), gotTime.Month(), gotTime.Day(),
			expected.Year(), expected.Month(), expected.Day())
	}
}

// TestInsertFillsMissingColumnDefaultNextval pins M0103-0007 rung 19:
// a CREATE TABLE DEFAULT of nextval('seq_name') (parser shape:
// *parser.FuncCall with Name.Name=="nextval" and one StringConst arg)
// must evaluate against the process-global sequence registry at INSERT
// time and store a non-NULL KindInt Datum holding the advanced value
// on every row that omitted the column. Two consecutive INSERTs pin
// monotonic advance (catches an accidental fixed-sentinel or
// auto-create-without-advance fallthrough).
func TestInsertFillsMissingColumnDefaultNextval(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	// Pre-register the sequence so the test does not depend on
	// auto-create — that path is exercised by the symmetric
	// auto-create test below.
	const seqName = "test_default_nextval_seq"
	RegisterSequence(seqName, 1, 1, 1, 9223372036854775807, false)
	defer DropSequence(seqName)

	cim := cat.(*catalog.InMemory)
	defExpr := &parser.FuncCall{
		Name: parser.ObjectName{Name: "nextval"},
		Args: []parser.Expr{&parser.StringConst{Value: seqName}},
	}
	if _, err := cim.CreateTable(parser.ObjectName{Name: "audit_seq"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int8"},
			DefaultExpr: defExpr},
		{Name: "v", Type: catalog.Type{Name: "text"}, NotNull: true},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "audit_seq"})

	// Two INSERTs that each omit the DEFAULT column; expected ids are 1, 2.
	for i := 0; i < 2; i++ {
		insertPlan := &optimizer.Insert{
			Table: tbl,
			Source: &optimizer.Values{
				Rows: [][]optimizer.Expr{
					{&optimizer.StringConst{Value: "row"}},
				},
			},
			ColumnIndex: []int{1},
		}
		op, err := Build(insertPlan)
		if err != nil {
			t.Fatal(err)
		}
		if err := op.Open(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := op.Next(); err != EOF {
			t.Fatalf("Insert.Next iter %d: %v", i, err)
		}
		_ = op.Close()
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
	if len(rows) != 2 {
		t.Fatalf("scan returned %d rows want 2", len(rows))
	}
	for i, want := range []int64{1, 2} {
		got := rows[i][0]
		if got.IsNull() {
			t.Fatalf("row %d id: got NULL — DEFAULT nextval not evaluated", i)
		}
		if got.Kind != KindInt {
			t.Fatalf("row %d id.Kind: got %v want KindInt", i, got.Kind)
		}
		if got.Int != want {
			t.Errorf("row %d id: got %d want %d", i, got.Int, want)
		}
	}
}

// TestInsertFillsMissingColumnDefaultNextvalAutoCreates pins rung-19
// auto-create behaviour: an UNregistered sequence name in DEFAULT
// nextval() is auto-registered with the PG-default shape (start=1,
// increment=1) and the first INSERT returns 1. Without auto-create,
// the slow path would have returned NullDatum and the test row would
// land with id=NULL.
func TestInsertFillsMissingColumnDefaultNextvalAutoCreates(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	const seqName = "test_default_nextval_autocreate_seq"
	// Defensive: clear any prior state if a previous test left it
	// behind. We do NOT pre-register — the point of this test is
	// the auto-create path.
	DropSequence(seqName)
	defer DropSequence(seqName)

	cim := cat.(*catalog.InMemory)
	defExpr := &parser.FuncCall{
		Name: parser.ObjectName{Name: "nextval"},
		Args: []parser.Expr{&parser.StringConst{Value: seqName}},
	}
	if _, err := cim.CreateTable(parser.ObjectName{Name: "audit_seq_auto"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int8"}, DefaultExpr: defExpr},
		{Name: "v", Type: catalog.Type{Name: "text"}, NotNull: true},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "audit_seq_auto"})

	insertPlan := &optimizer.Insert{
		Table: tbl,
		Source: &optimizer.Values{
			Rows: [][]optimizer.Expr{
				{&optimizer.StringConst{Value: "row"}},
			},
		},
		ColumnIndex: []int{1},
	}
	op, err := Build(insertPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Insert.Next: %v", err)
	}
	_ = op.Close()

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
		t.Fatalf("scan returned %d rows want 1", len(rows))
	}
	got := rows[0][0]
	if got.IsNull() {
		t.Fatalf("id: got NULL — auto-create did not register sequence")
	}
	if got.Kind != KindInt || got.Int != 1 {
		t.Errorf("id: got %v want KindInt 1", got)
	}
}

func drainScan(op Operator) ([]Row, error) {
	var out []Row
	for {
		slot, err := op.Next()
		if err == EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		// Materialize at the public Run boundary so tests
		// receive owned rows (M0073-0001 promotion: arena-
		// backed Datums become regular KindString / KindBytes
		// with Buf payload). Mirrors executor.Run's contract.
		out = append(out, slot.Materialize().Row())
	}
}

// recordingExecAIOEngine counts every Submit so a test can
// assert that seqScan's prefetch loop fires through Pool.Prefetch
// → Manager.PrefetchBlock → engine.Submit. Same shape as the
// recording engine in internal/storage/storage_test.go but
// duplicated here because Go test packages can't share helpers.
type recordingExecAIOEngine struct {
	submits int
}

func (r *recordingExecAIOEngine) Submit(op storage.AIOSubmitOp) storage.AIOHandle {
	r.submits++
	var n int
	var err error
	switch op.Direction {
	case storage.AIODirWrite:
		n, err = op.File.WriteAt(op.Buffer, op.Offset)
	default:
		n, err = op.File.ReadAt(op.Buffer, op.Offset)
	}
	// Real engines call OnComplete on the completion path
	// regardless of Wait timing (see AIOSubmitOp.OnComplete);
	// PrefetchBlock/WriteBlockAIO rely on it to release relFile's
	// per-block latch (lockBlock). Skipping this wedges the latch
	// forever the next time anything touches the same block.
	if op.OnComplete != nil {
		op.OnComplete()
	}
	return execAIOHandle{n: n, err: err}
}

type execAIOHandle struct {
	n   int
	err error
}

func (h execAIOHandle) Wait() (int, error) { return h.n, h.err }

// TestSeqScanFiresPrefetchesAcrossBlocks pins the M0009 caller
// integration: with prefetching enabled, seqScan walks
// `seqScanLookahead` blocks ahead of curBlock via Pool.Prefetch.
// Constructs a fixture with a recording AIO engine, inserts
// enough rows to span 5+ blocks, runs a SeqScan to completion,
// and asserts the engine saw at least one Submit (and at most
// nBlocks Submits — we never overshoot the relation).
func TestSeqScanFiresPrefetchesAcrossBlocks(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	// Attach a recording AIO engine to the fixture's Manager
	// and opt the Pool into prefetching.
	eng := &recordingExecAIOEngine{}
	ctx.Pool.Manager().SetAIO(eng)
	ctx.Pool.SetPrefetchEnabled(true)

	// Stuff enough rows in to span several blocks.
	const N = 600
	rows := make([][]optimizer.Expr, N)
	for i := range rows {
		rows[i] = []optimizer.Expr{
			&optimizer.IntegerConst{Value: int64(i)},
			&optimizer.StringConst{Value: "row"},
		}
	}
	in := &optimizer.Insert{
		Table:       tbl,
		Source:      &optimizer.Values{Rows: rows},
		ColumnIndex: []int{0, 1},
	}
	op, _ := Build(in)
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Insert.Next: %v", err)
	}
	_ = op.Close()

	rel := cat.RelFileNode(tbl)
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		t.Fatal(err)
	}
	if nBlocks < 2 {
		t.Fatalf("test setup wrote only %d blocks; need ≥2 to exercise prefetch", nBlocks)
	}

	// Insert populated the buffer pool with every page. Flush
	// to disk THEN drop them so the SeqScan's Pool.Prefetch
	// hits the not-cached path (Pool.Prefetch silently no-ops
	// on cached tags). InvalidateRel without a prior FlushAll
	// would discard dirty pages, costing the inserted data.
	if err := ctx.Pool.FlushAll(); err != nil {
		t.Fatal(err)
	}
	ctx.Pool.InvalidateRel(rel)
	// Reset the counter so we observe only the SeqScan's
	// prefetches, not any Pin-driven background reads from the
	// insert path.
	eng.submits = 0
	scan := newSeqScanOp(&optimizer.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatal(err)
	}
	scanned, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	_ = scan.Close()
	if len(scanned) != N {
		t.Errorf("scanned %d rows, want %d", len(scanned), N)
	}
	// SeqScan should have fired at least one prefetch (the
	// initial lookahead window) and at most nBlocks — the
	// refill loop never overshoots NBlocks.
	if eng.submits == 0 {
		t.Errorf("seqScan fired 0 prefetches; expected at least 1")
	}
	if eng.submits > int(nBlocks) {
		t.Errorf("seqScan fired %d prefetches, exceeds NBlocks=%d", eng.submits, nBlocks)
	}
}
