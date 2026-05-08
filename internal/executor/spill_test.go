package executor

import (
	"os"
	"testing"
)

func TestSpillRoundTrip(t *testing.T) {
	dir, err := os.MkdirTemp("", "spilltest-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	w, err := newSpillWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	rows := []Row{
		{Datum{Kind: KindInt, Int: 1}, NewStringDatum("hello")},
		{Datum{Kind: KindInt, Int: 2}, Datum{Kind: KindNull}},
		{Datum{Kind: KindNumeric, Int: 12345, Scale: 2}, NewStringDatum("world")},
	}
	for _, row := range rows {
		if err := w.WriteRow(row); err != nil {
			w.Close()
			t.Fatal(err)
		}
	}
	w.Close()

	r, err := newSpillReader(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var readBack []Row
	for {
		row, err := r.ReadRow()
		if err != nil {
			break // EOF
		}
		readBack = append(readBack, row)
	}
	if len(readBack) != len(rows) {
		t.Fatalf("read %d rows, expected %d", len(readBack), len(rows))
	}
	for i, row := range rows {
		if len(row) != len(readBack[i]) {
			t.Fatalf("row %d: got %d cols, expected %d", i, len(readBack[i]), len(row))
		}
		for j := range row {
			if row[j].Kind != KindNull {
				if row[j].Kind != readBack[i][j].Kind {
					t.Errorf("row %d col %d: kind mismatch", i, j)
				} else if row[j].Kind == KindInt && row[j].Int != readBack[i][j].Int {
					t.Errorf("row %d col %d: int mismatch", i, j)
				} else if row[j].Kind == KindString && row[j].StringValue() != readBack[i][j].StringValue() {
					t.Errorf("row %d col %d: string mismatch", i, j)
				}
			}
		}
	}
}

func TestDrainRowsBoundedNoSpill(t *testing.T) {
	// 10K small rows, 10 MB budget → should not spill.
	rows := make([]Row, 10000)
	for i := range rows {
		rows[i] = Row{Datum{Kind: KindInt, Int: int64(i)}}
	}
	op := &rowsOp{rows: rows}
	result, err := drainRowsBounded(op, 100*1024*1024) // 100 MB
	if err != nil {
		t.Fatal(err)
	}
	if sp, ok := result.(*spillOp); ok {
		t.Error("small data should not have spilled")
		_ = sp
	}
	if rop, ok := result.(*rowsOp); ok {
		if len(rop.rows) != 10000 {
			t.Errorf("rowsOp has %d rows, expected 10000", len(rop.rows))
		}
	}
}

func TestDrainRowsBoundedSpill(t *testing.T) {
	// 10K rows with 512-byte strings, 1 KB budget → should spill.
	rows := make([]Row, 10000)
	for i := range rows {
		rows[i] = Row{NewStringDatum(makeString(512))}
	}
	op := &rowsOp{rows: rows}
	result, err := drainRowsBounded(op, 1024) // 1 KB — tiny budget forces spill
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(*spillOp); !ok {
		t.Error("large data with tiny budget should have spilled")
	}
	// Read back and verify.
	ctx := &Context{}
	result.Open(ctx)
	var count int
	for {
		_, err := NextRow(result)
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	result.Close()
	if count != 10000 {
		t.Errorf("spill read back %d rows, expected 10000", count)
	}
}

func makeString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
