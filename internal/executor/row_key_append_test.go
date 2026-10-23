package executor

import (
	"math/big"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// M0122-0012 slice 70 guard: appendRowKeyTrimmed produces exactly the key the
// string-building rowKeyTrimmed produced, so every DISTINCT / UNION /
// INTERSECT / EXCEPT / recursive-CTE dedup keeps its equivalence classes. The
// oracle is the pre-slice implementation.

func rowKeyTrimmedOracle(row Row, skip map[int]bool, trims []bool) string {
	var sb strings.Builder
	for i, d := range row {
		if skip[i] {
			continue
		}
		if i < len(trims) && trims[i] {
			d = trimStringDatum(d)
		}
		if sb.Len() > 0 {
			sb.WriteByte('|')
		}
		if d.IsNull() {
			sb.WriteString("<NULL>")
		} else {
			s := d.Format()
			if strings.Contains(s, ".") {
				s = strings.TrimRight(s, "0")
				s = strings.TrimRight(s, ".")
				if s == "" || s == "-" {
					s = "0"
				}
			}
			sb.WriteString(s)
		}
	}
	return sb.String()
}

func rowKeyTestValues() []Datum {
	big1, _ := new(big.Int).SetString("-123456789012345678901230", 10)
	ts := time.Date(2001, 2, 3, 4, 5, 6, 700000000, time.UTC)
	return []Datum{
		NullDatum, NewBoolDatum(true), NewBoolDatum(false),
		NewIntDatum(0), NewIntDatum(-42), NewIntDatum(1 << 62),
		{Kind: KindNumeric, Int: 0, Scale: 2}, {Kind: KindNumeric, Int: -1, Scale: 3}, {Kind: KindNumeric, Int: 1500, Scale: 3},
		{Kind: KindNumeric, Int: -1000, Scale: 3}, {Kind: KindNumeric, Int: 12, Scale: 0},
		NewNumericBigDatum(big1, 4),
		NewStringDatum(""), NewStringDatum("abc"), NewStringDatum("a.10"), NewStringDatum("0."), NewStringDatum("-."),
		NewStringDatum("x  "), NewStringDatum("..."), NewStringDatum("a|b"),
		NewBytesDatum([]byte{0, 'z', '.'}), NewBytesDatum(nil),
		NewDateDatum(ts), NewTimeDatum(ts), NewTimestampTZDatum(ts), NewIntervalDatum(14, 3),
		NewEnumDatum(1.5, "lbl.0"),
	}
}

func TestAppendRowKeyTrimmedMatchesOracle(t *testing.T) {
	vals := rowKeyTestValues()
	r := rand.New(rand.NewSource(70))
	for range 20000 {
		row := make(Row, 1+r.Intn(5))
		trims := make([]bool, len(row))
		for i := range row {
			row[i] = vals[r.Intn(len(vals))]
			trims[i] = r.Intn(3) == 0
		}
		var skip map[int]bool
		if r.Intn(4) == 0 {
			skip = map[int]bool{r.Intn(len(row)): true}
		}
		if r.Intn(3) == 0 {
			trims = nil
		}
		want := rowKeyTrimmedOracle(row, skip, trims)
		if got := rowKeyTrimmed(row, skip, trims); got != want {
			t.Fatalf("rowKeyTrimmed(%v) = %q, want %q", row, got, want)
		}
		// Appending after a prefix leaves the prefix and appends the same key.
		got := string(appendRowKeyTrimmed([]byte("prefix|"), row, skip, trims))
		if got != "prefix|"+want {
			t.Fatalf("appendRowKeyTrimmed after a prefix = %q, want %q", got, "prefix|"+want)
		}
	}
}

func TestAppendFormatNumericMatchesFormatNumeric(t *testing.T) {
	r := rand.New(rand.NewSource(7070))
	mants := []int64{0, 1, -1, 9, -10, 100, 123456789, -987654321, 1<<63 - 1, -1 << 63, -(1<<63 - 1)}
	for range 50000 {
		m := r.Int63() >> uint(r.Intn(63))
		if r.Intn(2) == 0 {
			m = -m
		}
		mants = append(mants, m)
	}
	for _, m := range mants {
		for _, sc := range []int16{0, 1, 2, 3, 5, 16, 19, 20, 25} {
			want := formatNumeric(m, sc)
			if got := string(appendFormatNumeric([]byte("p"), m, sc)); got != "p"+want {
				t.Fatalf("appendFormatNumeric(%d, %d) = %q, want %q", m, sc, got[1:], want)
			}
		}
	}
}
