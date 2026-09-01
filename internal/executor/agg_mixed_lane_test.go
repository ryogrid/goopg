package executor

// review/260831-2 ES-1 — sum()/avg() over a group whose rows carry BOTH
// KindInt and KindNumeric arguments.
//
// aggRuntime keeps the two kinds in separate accumulators (`sum` and
// `numericSum`) and the finalizer used to return the numeric one whenever it
// was live, dropping the int lane outright. The oracle (PG 18.3) for the shape
// below:
//
//	create table es1(a int); insert into es1 select g from generate_series(1,4) g;
//	select sum(case when a = 1 then 1 else 1.5 end) from es1;   -- 5.5
//	select avg(case when a = 1 then 1 else 1.5 end) from es1;   -- 1.375
//
// goopg returned 4.5 and 1.125 — the leading `1` was lost. The combine path
// (combineNumericSum) reaches the same state from a different direction, so
// both the serial and the split forms are pinned here.

import (
	"math/big"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// mixedLaneValues is `1, 1.5, 1.5, 1.5`: one int-lane row followed by three
// numeric-lane ones. Sum 5.5, average 1.375.
func mixedLaneValues() []Datum {
	return []Datum{
		NewIntDatum(1),
		newNumeric(big.NewInt(15), 1),
		newNumeric(big.NewInt(15), 1),
		newNumeric(big.NewInt(15), 1),
	}
}

func TestAggMixedIntAndNumericLanes(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"sum", "5.5"},
		{"avg", "1.3750000000000000"},
	} {
		h := &aggHarness{t: t, name: tc.name, input: catalog.Type{Name: "numeric"}}

		// parts=1 is the serial path; 2 and 4 route through combineNumericSum
		// with the int lane on either side of the merge.
		for _, parts := range []int{1, 2, 4} {
			serial, combined := h.run(mixedLaneValues(), parts)
			if got := serial.Format(); got != tc.want {
				t.Errorf("%s serial = %s, want %s (int lane dropped)", tc.name, got, tc.want)
			}
			if got := combined.Format(); got != tc.want {
				t.Errorf("%s combined over %d partials = %s, want %s (int lane dropped)",
					tc.name, parts, got, tc.want)
			}
		}
	}
}

// TestAggIntOnlyLaneUnaffected pins the ordinary single-lane shapes, so the
// fold cannot change an aggregate that never mixed kinds.
func TestAggIntOnlyLaneUnaffected(t *testing.T) {
	ctx := NewContext()
	for _, tc := range []struct {
		what string
		vals []Datum
		want string
	}{
		{"int lane only", ints(1, 2, 3), "6"},
		{"numeric lane only", []Datum{newNumeric(big.NewInt(15), 1), newNumeric(big.NewInt(25), 1)}, "4.0"},
	} {
		call := optimizer.AggregateCall{Name: "sum", InputType: catalog.Type{Name: "numeric"}}
		var st aggRuntime
		for _, v := range tc.vals {
			if err := applyAggValue(ctx, call, &st, v); err != nil {
				t.Fatalf("%s: transition: %v", tc.what, err)
			}
		}
		got, err := finishAggValue(ctx, call, &st)
		if err != nil {
			t.Fatalf("%s: finalize: %v", tc.what, err)
		}
		if got.Format() != tc.want {
			t.Errorf("sum(%s) = %s, want %s", tc.what, got.Format(), tc.want)
		}
	}
}
