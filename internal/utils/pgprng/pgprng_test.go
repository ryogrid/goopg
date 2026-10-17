package pgprng

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// stream renders the draw sequence testdata/prng_harness.c prints for one
// seeded state, in the same format.
func stream(label string, st State) string {
	ranges := [][2]uint64{{0, 0}, {0, 1}, {0, 4}, {1, 12}, {0, 999}, {5, 3}, {0, 2}, {1, 7}}
	var b strings.Builder
	b.WriteString(label)
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&b, " u%d", st.Uint64())
	}
	for i := 0; i < 4; i++ {
		b.WriteString(" d" + strconv.FormatFloat(st.Double(), 'g', 17, 64))
	}
	for _, r := range ranges {
		fmt.Fprintf(&b, " r%d", st.Uint64Range(r[0], r[1]))
	}
	return b.String()
}

// TestMatchesPGPrng pins the port to PG 18.3's own pg_prng.c. The golden file
// is the output of testdata/prng_harness.c linked against libpgcommon.a.
// The streams cover fseed (GEQO's geqo_set_seed path; 0 is PG's default
// geqo_seed), plain seed, every draw kind, and the no-draw one-value and
// inverted ranges of pg_prng_uint64_range.
func TestMatchesPGPrng(t *testing.T) {
	raw, err := os.ReadFile("testdata/prng_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var got []string
	for _, f := range []float64{0.0, 0.5, -0.25, 1.0, 0.123456789} {
		got = append(got, stream("fseed="+strconv.FormatFloat(f, 'g', 17, 64), FSeed(f)))
	}
	for _, s := range []uint64{0, 12345} {
		got = append(got, stream(fmt.Sprintf("seed=%d", s), Seed(s)))
	}
	if len(got) != len(want) {
		t.Fatalf("%d streams, golden has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("stream %d:\n got  %s\n want %s", i, got[i], want[i])
		}
	}
}

// TestUint64RangeOneValueConsumesNoDraw pins the property GEQO's init_tour
// relies on: a range with rmax <= rmin returns rmin without advancing the
// state.
func TestUint64RangeOneValueConsumesNoDraw(t *testing.T) {
	a, b := FSeed(0), FSeed(0)
	if v := a.Uint64Range(3, 3); v != 3 {
		t.Fatalf("Uint64Range(3, 3) = %d", v)
	}
	if a != b {
		t.Fatal("a one-value range advanced the state")
	}
}
