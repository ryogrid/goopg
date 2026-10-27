package executor

import (
	"fmt"
	"math/big"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

// TestSkipColumnRunMatchesPerColumnSkip pins M0122-0012 C35: walking a run
// of skipped columns in skipColumnRun lands on exactly the offsets the
// per-column skip arm (catalog.AttAlignPointer + skipAttrLen) reaches, so
// every decoded column and the returned resume offset are identical, and
// every skipped cell is NULL. Random rows mix every alignment class, fixed
// widths, short (1-byte) and long (4-byte header) varlenas, NULLs anywhere
// and tuples shorter than the descriptor (columns added after the row was
// written); random skip masks and windows exercise runs of every length,
// starting and ending anywhere.
func TestSkipColumnRunMatchesPerColumnSkip(t *testing.T) {
	ty := func(name string) catalog.Type { return catalog.Type{Name: name} }
	cols := []catalog.Column{
		{Name: "b", Type: ty("bool")}, {Name: "i2", Type: ty("int2")}, {Name: "tx", Type: ty("text")},
		{Name: "i8", Type: ty("int8")}, {Name: "by", Type: ty("bytea")}, {Name: "i4", Type: ty("int4")},
		{Name: "n", Type: ty("numeric")}, {Name: "d", Type: ty("date")}, {Name: "big", Type: ty("text")},
		{Name: "f8", Type: ty("int8")}, {Name: "b2", Type: ty("bool")}, {Name: "tz", Type: ty("timestamptz")},
		{Name: "vc", Type: ty("varchar")}, {Name: "last", Type: ty("int4")},
	}
	rng := rand.New(rand.NewSource(35))
	gen := func(i int) Datum {
		if rng.Intn(5) == 0 {
			return NullDatum
		}
		switch cols[i].Type.Name {
		case "bool":
			return NewBoolDatum(rng.Intn(2) == 0)
		case "int2":
			return NewIntDatum(int64(rng.Intn(60000) - 30000))
		case "int4":
			return NewIntDatum(int64(rng.Int31()) - 1<<30)
		case "int8":
			return NewIntDatum(rng.Int63n(1<<40) - 1<<39)
		case "numeric":
			return NewNumericBigDatum(big.NewInt(rng.Int63()), int16(rng.Intn(6)))
		case "date":
			return NewDateDatum(time.Date(2000+rng.Intn(30), 1, 1+rng.Intn(28), 0, 0, 0, 0, time.UTC))
		case "timestamptz":
			return NewTimestampTZDatum(time.Unix(rng.Int63n(2e9), 0).UTC())
		case "bytea":
			b := make([]byte, rng.Intn(200))
			rng.Read(b)
			return NewBytesDatum(b)
		default:
			n := rng.Intn(20)
			if cols[i].Name == "big" {
				n = 100 + rng.Intn(400) // past 126 bytes: the 4-byte header
			}
			return NewStringDatum(strings.Repeat(string(rune('a'+rng.Intn(26))), n))
		}
	}
	base := resolveColTypeInfo(cols)
	for i := range base {
		if base[i].skipLen == 0 {
			t.Fatalf("column %s has no skip length", cols[i].Name)
		}
	}
	defer func() { skipColumnRunDisabled = false }()
	walked := 0
	for iter := 0; iter < 4000; iter++ {
		stored := len(cols)
		if rng.Intn(4) == 0 {
			stored = 1 + rng.Intn(len(cols))
		}
		row := make(Row, stored)
		for i := range row {
			row[i] = gen(i)
		}
		data, err := EncodeRowPG(cols[:stored], row)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		bitmap := NullBitmapPG(row)
		info := append([]colTypeInfo(nil), base...)
		for i := range info {
			info[i].skip = rng.Intn(3) != 0
		}
		from := 0
		if rng.Intn(3) == 0 {
			from = rng.Intn(len(cols))
		}
		to := from + rng.Intn(len(cols)-from+1)
		// The window must start where a previous decode left off.
		off := 0
		if from > 0 {
			pre := make(Row, len(cols))
			skipColumnRunDisabled = true
			off, err = decodeRowRangeInfo(pre, cols, base, data, bitmap, stored, nil, array.DefaultOutputStyle(), 0, from, 0)
			if err != nil {
				t.Fatalf("prefix: %v", err)
			}
		}
		decode := func(disabled bool) (Row, int) {
			dst := make(Row, len(cols))
			for i := range dst {
				dst[i] = NewIntDatum(-7) // stale cell: must be overwritten in the window
			}
			skipColumnRunDisabled = disabled
			end, err := decodeRowRangeInfo(dst, cols, info, data, bitmap, stored, nil, array.DefaultOutputStyle(), from, to, off)
			if err != nil {
				t.Fatalf("decode (disabled=%v): %v", disabled, err)
			}
			return dst, end
		}
		got, gotOff := decode(false)
		want, wantOff := decode(true)
		if gotOff != wantOff || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("iter %d stored %d window [%d,%d) skip %v:\nrun  off %d %v\ncol  off %d %v", iter, stored, from, to,
				skipMask(info), gotOff, got, wantOff, want)
		}
		for i := from; i < to && i < stored; i++ {
			if info[i].skip && !got[i].IsNull() {
				t.Fatalf("iter %d: skipped column %d holds %v", iter, i, got[i])
			}
			if info[i].skip {
				walked++
			}
		}
	}
	if walked == 0 {
		t.Fatal("no column was walked")
	}
}

func skipMask(info []colTypeInfo) []bool {
	out := make([]bool, len(info))
	for i := range info {
		out[i] = info[i].skip
	}
	return out
}
