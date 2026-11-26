package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
)

// TestApplyBinaryTupleColumns: a 'b' (LOGICALREP_COLUMN_BINARY) column is
// read with the type's receive function, and a field of the wrong width is
// PG's 22P03 "incorrect binary data format".
func TestApplyBinaryTupleColumns(t *testing.T) {
	local := []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}}}
	remote := []xlog.DecodedAttr{{Name: "a"}}
	row, _, _, err := decodePgoutputTupleAsRow(remote, local, []xlog.DecodedColumn{{Status: 'b', Bytes: []byte{0, 0, 0, 7}}})
	if err != nil || row[0].Int != 7 {
		t.Fatalf("row %v err %v, want 7", row, err)
	}
	_, _, _, err = decodePgoutputTupleAsRow(remote, local, []xlog.DecodedColumn{{Status: 'b', Bytes: []byte{0, 0, 0, 0, 0, 0, 0, 7}}})
	if ee, ok := err.(*ExecError); !ok || ee.Code != "22P03" || ee.Message != "incorrect binary data format" {
		t.Fatalf("int8 bytes into int4: %v, want 22P03 incorrect binary data format", err)
	}
}

// TestBinaryInputUnavailable: a binary COPY into a column of a user base type
// without a receive function is 42883, until ALTER TYPE SET (receive = …).
func TestBinaryInputUnavailable(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TYPE myvarchar"); err != nil {
		t.Fatal(err)
	}
	cols := []catalog.Column{{Name: "a", Type: catalog.Type{Name: "myvarchar"}}}
	err := BinaryInputUnavailable(ctx.Catalog, cols, ctx.CurrentDatabaseOid)
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42883" {
		t.Fatalf("no receive: %v, want 42883", err)
	}
	if err := runDDL(t, ctx, "ALTER TYPE myvarchar SET (receive = myvarcharrecv)"); err != nil {
		t.Fatal(err)
	}
	if err := BinaryInputUnavailable(ctx.Catalog, cols, ctx.CurrentDatabaseOid); err != nil {
		t.Fatalf("with receive: %v", err)
	}
}
