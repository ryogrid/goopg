package xlog

import "testing"

// TestIsPreallocatedTail covers the M0088-0001 preallocated-zero-tail check used
// by readAllPageAware's graceful end-of-WAL detection (a corrupt/short record
// followed by an all-zero tail is treated as EOS, not fatal corruption).
//
// The legacy-frame ReadAll torn-tail tests (TestReadAllStopsAtTornTail*,
// TestReadAllPropagatesRealMidStreamCorruption) were removed in A9 together with
// the IEEE-CRC frame (encodeRecord/decodeRecord). The equivalent PG-frame
// torn-tail handling lives in readAllPageAware and is exercised end-to-end by
// internal/initdb crash-recovery (recovery over a preallocated-zero-tail segment).
func TestIsPreallocatedTail(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
		want bool
	}{
		{"empty", []byte{}, true},
		{"all zero short", make([]byte, 100), true},
		{"all zero one chunk", make([]byte, 65536), true},
		{"all zero multi chunk", make([]byte, 200_000), true},
		{"trailing nonzero", append(make([]byte, 99), 0x01), false},
		{"leading nonzero", append([]byte{0x42}, make([]byte, 99)...), false},
		{"middle nonzero", func() []byte {
			b := make([]byte, 200)
			b[100] = 0xFF
			return b
		}(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPreallocatedTail(c.b); got != c.want {
				t.Fatalf("isPreallocatedTail = %v, want %v", got, c.want)
			}
		})
	}
}
