package optimizer

import (
	"bufio"
	"io"
	"os"
	"strings"
	"testing"
)

// offeredProducers runs addPathsToJoinrel with the path trace on and returns
// the producer of every offered join path, in offer order.
func offeredProducers(t *testing.T) []string {
	t.Helper()
	a, b := relsetOf(0), relsetOf(1)
	outer := scanRel(a, 5, 1)
	inner := scanRel(b, 5, 1)
	joinrel := newRelOptInfo(a|b, 5, 32)

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
	err = addPathsToJoinrel(nil, joinrel, outer, inner, []*restrictInfo{equiClauseOn(a, b, 10, 11)}, defaultCostParams(), nil)
	os.Stderr, pathTraceEnabled = prevErr, prevOn
	_ = w.Close()
	got := <-done
	if err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	return got
}

// TestJoinArmsOfferNestLoopBeforeHash pins M0146-0005bk: add_paths_to_joinrel
// offers match_unsorted_outer's nested loops before hash_inner_and_outer, and
// add_path keeps the incumbent when two paths tie within STD_FUZZ_FACTOR — so
// the offer order decides those ties (TPC-DS Q8: PG keeps its 28512.75 nested
// loop over its 28305.93 hash join, 0.7% apart).
func TestJoinArmsOfferNestLoopBeforeHash(t *testing.T) {
	producers := offeredProducers(t)
	firstNL, firstHash := -1, -1
	for i, p := range producers {
		if firstNL < 0 && strings.Contains(p, "nestloop") {
			firstNL = i
		}
		if firstHash < 0 && strings.Contains(p, "hash") {
			firstHash = i
		}
	}
	if firstNL < 0 || firstHash < 0 {
		t.Fatalf("want both a nestloop and a hash offer, got %v", producers)
	}
	if firstNL > firstHash {
		t.Fatalf("hash offered before nestloop: %v", producers)
	}
}
