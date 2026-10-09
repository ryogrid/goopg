package optimizer

import (
	"bufio"
	"io"
	"os"
	"strings"
	"testing"
)

// TestMaterialNestLoopOfferedAfterIndexProbes pins M0146-0005ea:
// match_unsorted_outer offers, per outer path, the
// cheapest_parameterized_paths loop (the bare inner, then the parameterised
// probes and their Memoize) BEFORE the nested loop over the materialised
// cheapest inner (joinpath.c). add_path keeps the incumbent when two paths
// tie within STD_FUZZ_FACTOR, so the order decides those ties — TPC-DS Q10's
// customer_address probe ties its Materialize(Seq Scan) and PG keeps the
// probe. goopg filed the matpath first, so its nested loop won instead.
func TestMaterialNestLoopOfferedAfterIndexProbes(t *testing.T) {
	cp := defaultCostParams()
	a, b := relsetOf(0), relsetOf(1)
	outer := scanRel(a, 100, estScanPages(100, 32))
	inner := nliInnerRel(b, 1000, a, indexProbeCost(cp))
	if materialInnerPathFor(inner.CheapestTotal, inner, cp) == nil {
		t.Fatal("fixture: the inner's cheapest total must be materialisable")
	}
	joinrel := newRelOptInfo(a|b, 100, 64)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prevErr, prevOn := os.Stderr, pathTraceEnabled
	os.Stderr, pathTraceEnabled = w, true
	done := make(chan []string)
	go func() {
		var out []string
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, pathTraceTag) || !strings.Contains(line, "relids="+relSetBits(joinrel.Relids)) {
				continue
			}
			for _, f := range strings.Fields(line) {
				if strings.HasPrefix(f, "producer=") {
					out = append(out, strings.TrimPrefix(f, "producer="))
				}
			}
		}
		_, _ = io.Copy(io.Discard, r)
		done <- out
	}()
	err = addPathsToJoinrel(nil, joinrel, outer, inner, nil, cp, nil)
	os.Stderr, pathTraceEnabled = prevErr, prevOn
	_ = w.Close()
	producers := <-done
	if err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	firstProbe, lastPlain := -1, -1
	for i, p := range producers {
		if firstProbe < 0 && p == "nestloop.index" {
			firstProbe = i
		}
		if p == "join.nestloop" {
			lastPlain = i
		}
	}
	if firstProbe < 0 || lastPlain < 0 {
		t.Fatalf("want both a probe and a plain nested loop offered, got %v", producers)
	}
	if lastPlain < firstProbe {
		t.Fatalf("the materialised-inner nested loop must follow the index probes, got %v", producers)
	}
}
