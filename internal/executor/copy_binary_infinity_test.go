package executor

import (
	"encoding/hex"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCopyBinaryTimestampInfinity pins the ±infinity wire form of the binary
// COPY codec for date / timestamp / timestamptz against the bytes real PG 18.3
// emits for
//
//	COPY (SELECT 'infinity'::date, '-infinity'::date,
//	             'infinity'::timestamp, '-infinity'::timestamptz)
//	  TO STDOUT (FORMAT binary)
//
// i.e. DATEVAL_NOEND/NOBEGIN = 7fffffff / 80000000 and DT_NOEND/NOBEGIN =
// 7fffffffffffffff / 8000000000000000. Before review/260831-2 EC-7 these arms
// had no sentinel intercept and shipped the KindTime carrier's INT64-extreme
// nanoseconds as a finite instant (+infinity came out as 2262-04-11), while the
// heap codec (codec.go) had handled the sentinels all along.
func TestCopyBinaryTimestampInfinity(t *testing.T) {
	cases := []struct {
		name    string
		typ     string
		datum   Datum
		wantHex string
	}{
		{"date +infinity", "date", NewDateInfinity(true), "7fffffff"},
		{"date -infinity", "date", NewDateInfinity(false), "80000000"},
		{"timestamp +infinity", "timestamp", NewTimestampInfinity(true), "7fffffffffffffff"},
		{"timestamp -infinity", "timestamp", NewTimestampInfinity(false), "8000000000000000"},
		{"timestamptz +infinity", "timestamptz", NewTimestampInfinity(true), "7fffffffffffffff"},
		{"timestamptz -infinity", "timestamptz", NewTimestampInfinity(false), "8000000000000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			typ := catalog.Type{Name: tc.typ}
			b, err := datumToCopyBinary(typ, tc.datum)
			if err != nil {
				t.Fatalf("datumToCopyBinary: %v", err)
			}
			if got := hex.EncodeToString(b); got != tc.wantHex {
				t.Fatalf("wire bytes = %s, want %s (PG 18.3)", got, tc.wantHex)
			}
			back, err := copyBinaryToDatum(typ, b)
			if err != nil {
				t.Fatalf("copyBinaryToDatum: %v", err)
			}
			if !back.IsTimestampNotFinite() {
				t.Fatalf("round trip lost the sentinel: %q", back.Format())
			}
			if back.IsTimestampPosInf() != tc.datum.IsTimestampPosInf() {
				t.Fatalf("round trip flipped the sign: %q", back.Format())
			}
		})
	}
}
