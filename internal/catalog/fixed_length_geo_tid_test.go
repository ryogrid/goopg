package catalog

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestFixedLengthGeoTidMatchesPostgresBKI pins M0149-0001k: in a cluster with
// the fixed_length_geometric_tid capability, PhysicalTypeIsVarlena and
// PhysicalTypeAlign answer PostgreSQL 18.3's pg_type typlen / typalign for
// point, lseg, box, line, circle and tid (postgres.bki), so a PG node deforms
// goopg-written rows at the same offsets. Without the capability the six stay
// varlena at 'i' — the format older clusters wrote, and the
// knownAlignGaps entries of TestPhysicalTypeAlignMatchesPostgresBKI.
func TestFixedLengthGeoTidMatchesPostgresBKI(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "postgres", "src", "include", "catalog", "postgres.bki"))
	if err != nil {
		t.Skipf("PG oracle postgres.bki not available: %v", err)
	}
	defer f.Close()
	want := map[string][2]int{} // typlen, align bytes
	bytes := map[string]int{"c": 1, "s": 2, "i": 4, "d": 8}
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "create pg_type "):
			in = true
		case l == "close pg_type":
			in = false
		case in && strings.HasPrefix(l, "insert ( "):
			fl := strings.Fields(strings.TrimPrefix(l, "insert ( "))
			if isFixedGeoTidName(fl[1]) {
				n, _ := strconv.Atoi(fl[4])
				want[fl[1]] = [2]int{n, bytes[fl[22]]}
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 6 {
		t.Fatalf("found %d of the 6 pg_type rows", len(want))
	}
	defer SetFixedLengthGeoTid(FixedLengthGeoTid())
	for _, on := range []bool{true, false} {
		SetFixedLengthGeoTid(on)
		for name, w := range want {
			typ := Type{Name: name}
			gotVarlena, gotAlign := PhysicalTypeIsVarlena(typ), PhysicalTypeAlignName(typ)
			if on && (gotVarlena || gotAlign != w[1] || w[0] <= 0) {
				t.Errorf("capable cluster: %s varlena=%v align=%d, PG typlen %d typalign %d", name, gotVarlena, gotAlign, w[0], w[1])
			}
			if !on && (!gotVarlena || gotAlign != 4) {
				t.Errorf("legacy cluster: %s varlena=%v align=%d, want the varlena 'i' format", name, gotVarlena, gotAlign)
			}
		}
	}
}
