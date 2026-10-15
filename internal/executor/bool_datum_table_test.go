package executor

import (
	"reflect"
	"testing"
)

// TestNewBoolDatumTable pins NewBoolDatum's copied-out table (M0122-0012) to
// the value the constructor used to build. It also checks that a caller
// mutating a returned Datum cannot reach the table: a shared mutable result
// would leak one row's boolean into every later comparison.
func TestNewBoolDatumTable(t *testing.T) {
	for _, b := range []bool{false, true} {
		want := Datum{Kind: KindBool}
		if b {
			want.Int = 1
		}
		got := NewBoolDatum(b)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("NewBoolDatum(%v) = %#v, want %#v", b, got, want)
		}
		if got.BoolValue() != b || got.IsNull() {
			t.Fatalf("NewBoolDatum(%v): BoolValue=%v IsNull=%v", b, got.BoolValue(), got.IsNull())
		}
		got.Int = 7
		got.Kind = KindInt
		if again := NewBoolDatum(b); !reflect.DeepEqual(again, want) {
			t.Fatalf("mutating a result changed the next NewBoolDatum(%v): %#v", b, again)
		}
	}
}
