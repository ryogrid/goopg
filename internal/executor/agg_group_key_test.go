package executor

import (
	"math/big"
	"math/rand"
	"testing"
	"time"
)

// TestAppendGroupKeyMatchesSetGroupKey pins the allocation-free group key
// (M0122-0012) to the string path it replaced in the hashed grouping loop:
// datumKey per column, with bpchar padding stripped by trimStringDatum where
// gkTrims says so, joined by setGroupKey. The bytes must be identical: they
// are the group map's keys and the emission order's sort keys.
func TestAppendGroupKeyMatchesSetGroupKey(t *testing.T) {
	rng := rand.New(rand.NewSource(122012))
	bigM, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	pick := func() Datum {
		switch rng.Intn(11) {
		case 0:
			return NullDatum
		case 1:
			return NewBoolDatum(rng.Intn(2) == 0)
		case 2:
			return NewIntDatum(int64(rng.Intn(2000) - 1000))
		case 3:
			return Datum{Kind: KindNumeric, Int: int64(rng.Intn(100000) - 50000), Scale: int16(rng.Intn(4))}
		case 4:
			return newNumeric(bigM, rng.Intn(3))
		case 5:
			return NewStringDatum("A")
		case 6:
			return NewStringDatum("ab  ") // bpchar padding
		case 7:
			return NewStringDatum("")
		case 8:
			return NewBytesDatum([]byte{1, 2, byte(rng.Intn(256))})
		case 9:
			return NewTimeDatum(time.Unix(int64(rng.Intn(1e9)), 0).UTC())
		default:
			return NewIntervalDatumFull(int32(rng.Intn(24)), int32(rng.Intn(30)), int64(rng.Intn(1e6)))
		}
	}
	sets := [][]int{nil, {0}, {1}, {0, 1}, {2, 0}, {0, 1, 2, 3}}
	for trial := 0; trial < 3000; trial++ {
		o := &aggregateOp{gkTrims: []bool{rng.Intn(2) == 0, rng.Intn(2) == 0, false, true}}
		vals := Row{pick(), pick(), pick(), pick()}
		keys := make([]string, len(vals))
		for i, v := range vals {
			if o.gkTrims[i] {
				v = trimStringDatum(v)
			}
			keys[i] = datumKey(v)
		}
		for si, set := range sets {
			for _, multi := range []bool{false, true} {
				want := o.setGroupKey(si, set, keys, multi)
				got := string(o.appendGroupKey(nil, si, set, vals, multi))
				if got != want {
					t.Fatalf("set %v multi=%v vals %v: appendGroupKey %q, setGroupKey %q", set, multi, vals, got, want)
				}
			}
		}
		for _, v := range vals {
			if got, want := string(appendDatumKey([]byte("pre"), v)), "pre"+datumKey(v); got != want {
				t.Fatalf("appendDatumKey(%v) = %q, want %q", v, got, want)
			}
		}
	}
}
