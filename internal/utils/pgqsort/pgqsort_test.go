package pgqsort

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type elem struct {
	worth float64
	id    int
}

// geqoCompare is geqo_pool.c's compare().
func geqoCompare(a, b *elem) int {
	switch {
	case a.worth == b.worth:
		return 0
	case a.worth > b.worth:
		return 1
	default:
		return -1
	}
}

// TestMatchesPGQsort pins Sort to PG 18.3's pg_qsort permutation. The golden
// file is the output of testdata/qsort_harness.c linked against
// libpgport.a. Its inputs are heavy-tie, light-tie and tie-free random arrays,
// plus presorted and reversed shapes, at sizes on both sides of every
// threshold in sort_template.h (7, 40). Ties make the comparison exact: any
// deviation in pivot choice or partition order shows up as a different id
// order.
func TestMatchesPGQsort(t *testing.T) {
	raw, err := os.ReadFile("testdata/qsort_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	cases := 0
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			t.Fatalf("malformed golden line %q", line)
		}
		ws := strings.TrimPrefix(f[1], "w=")
		is := strings.TrimPrefix(f[2], "ids=")
		var in []elem
		if ws != "" {
			for i, w := range strings.Split(ws, ",") {
				v, err := strconv.ParseFloat(w, 64)
				if err != nil {
					t.Fatal(err)
				}
				in = append(in, elem{worth: v, id: i})
			}
		}
		Sort(in, geqoCompare)
		var got []string
		for _, e := range in {
			got = append(got, strconv.Itoa(e.id))
		}
		if g := strings.Join(got, ","); g != is {
			t.Errorf("%s: got ids=%s", f[0], g)
		}
		if !sort.SliceIsSorted(in, func(i, j int) bool { return in[i].worth < in[j].worth }) {
			t.Errorf("%s: not sorted", f[0])
		}
		cases++
	}
	if cases != 75 {
		t.Fatalf("checked %d cases, want 75", cases)
	}
}
