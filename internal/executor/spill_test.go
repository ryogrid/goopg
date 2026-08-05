package executor

import (
	"os"
	"testing"
	"time"
)

func TestSpillRoundTrip(t *testing.T) {
	dir, err := os.MkdirTemp("", "spilltest-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	w, err := newSpillWriterInDir(dir)
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

// TestSpillPreservesTheDateDiscriminator is the regression guard for the defect
// M0127-P5.9-s found in TPC-DS Q72: a spilled DATE came back as a bare
// timestamp, because `encodeDatum` wrote the value and never the `Flags` byte.
//
// The two assertions are the two halves of the bug's signature, and the second
// is the one that let it hide for so long. `flagDate` gone means `date + integer`
// raises `operator + requires integer operands` (expr.go's `date_pli` arm
// dispatches on the flag) and `Format()` renders MDY as ISO — while a COMPARISON
// of two spilled dates keeps working, because `Int` survives intact. So a
// round-trip test that only compared values reported success on a datum that had
// lost its type.
//
// Both spellings are exercised: an ordinary date and the `±infinity` sentinel,
// whose carrier IS `KindTime + flagDate` with an out-of-range `Int`
// (`NewDateInfinity`) — it would decode as a timestamp far past the end of time.
func TestSpillPreservesTheDateDiscriminator(t *testing.T) {
	day := NewDateDatum(time.Date(1998, 3, 15, 0, 0, 0, 0, time.UTC))
	ts := NewTimeDatum(time.Date(1998, 3, 15, 12, 34, 56, 0, time.UTC))
	cases := []struct {
		name string
		in   Datum
		date bool
	}{
		{"date", day, true},
		{"timestamp", ts, false},
		{"date +infinity", NewDateInfinity(true), true},
		{"date -infinity", NewDateInfinity(false), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n, err := decodeDatum(encodeDatum(tc.in, nil))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if n == 0 {
				t.Fatal("decode consumed no bytes")
			}
			if got.Kind != KindTime || got.Int != tc.in.Int {
				t.Fatalf("round trip = %v/%d, want KindTime/%d", got.Kind, got.Int, tc.in.Int)
			}
			if isDate := got.Flags&flagDate != 0; isDate != tc.date {
				t.Fatalf("flagDate = %v, want %v — a spilled date that forgets it is a date "+
					"fails `d_date + 5` with \"operator + requires integer operands\" and "+
					"renders in the wrong DateStyle, while still comparing correctly",
					isDate, tc.date)
			}
		})
	}
}

// TestSpillDoesNotForgeANumericRepresentation: `flagBigNumeric` describes an
// arena-backed mantissa the DECODER never produces (it rebuilds every numeric
// through `newNumeric`), so the flags byte must not carry it back. A forged bit
// would make a plain int64 mantissa read as an arena offset.
func TestSpillDoesNotForgeANumericRepresentation(t *testing.T) {
	in := Datum{Kind: KindNumeric, Int: 12345, Scale: 2, Flags: flagBigNumeric}
	got, _, err := decodeDatum(encodeDatum(in, nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Flags&flagBigNumeric != 0 {
		t.Fatal("the decoded numeric claims an arena mantissa it does not have")
	}
	if got.Scale != in.Scale {
		t.Fatalf("scale = %d, want %d", got.Scale, in.Scale)
	}
}

func TestDrainRowsBoundedNoSpill(t *testing.T) {
	// 10K small rows, 10 MB budget → should not spill.
	rows := make([]Row, 10000)
	for i := range rows {
		rows[i] = Row{Datum{Kind: KindInt, Int: int64(i)}}
	}
	op := &rowsOp{rows: rows}
	ctx := NewContext()
	ctx.DataDir = t.TempDir()
	defer ctx.ReleaseSpillFiles()
	result, err := drainRowsBounded(ctx, op, 100*1024*1024) // 100 MB
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
	ctx := NewContext()
	ctx.DataDir = t.TempDir()
	defer ctx.ReleaseSpillFiles()
	result, err := drainRowsBounded(ctx, op, 1024) // 1 KB — tiny budget forces spill
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(*spillOp); !ok {
		t.Error("large data with tiny budget should have spilled")
	}
	// Read back and verify.
	result.Open(ctx)
	var count int
	for {
		_, err := result.Next()
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
