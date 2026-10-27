package executor

import (
	"math/rand"
	"testing"
)

// TestMatchLikeSegmentsEqualsWalk pins M0122-0012 C17: a pattern whose only
// metacharacter is '%' is matched as prefix / ordered leftmost substrings /
// suffix, and that must give exactly the general backtracking walk's answer.
// Exhaustive over short strings and patterns from a small alphabet (where
// overlapping and repeated segments are common), plus random longer cases,
// and patterns with '_' or '\' must decline to the walk.
func TestMatchLikeSegmentsEqualsWalk(t *testing.T) {
	alpha := []byte("ab")
	var gen func(n int, prefix []byte, out *[]string)
	gen = func(n int, prefix []byte, out *[]string) {
		*out = append(*out, string(prefix))
		if n == 0 {
			return
		}
		for _, c := range alpha {
			gen(n-1, append(prefix, c), out)
		}
	}
	var strs []string
	gen(6, nil, &strs)
	var pats []string
	alpha = []byte("ab%")
	gen(5, nil, &pats)
	for _, p := range pats {
		for _, s := range strs {
			got, ok := matchLikeSegments(s, p)
			if !ok {
				t.Fatalf("pattern %q declined the fast path", p)
			}
			if want := matchSQLLikeWalk(s, p); got != want {
				t.Fatalf("%q LIKE %q: segments %v, walk %v", s, p, got, want)
			}
		}
	}
	rng := rand.New(rand.NewSource(17))
	letters := []byte("abc%")
	for i := 0; i < 200000; i++ {
		s := make([]byte, rng.Intn(24))
		for j := range s {
			s[j] = "abc"[rng.Intn(3)]
		}
		p := make([]byte, rng.Intn(10))
		for j := range p {
			p[j] = letters[rng.Intn(len(letters))]
		}
		got, _ := matchLikeSegments(string(s), string(p))
		if want := matchSQLLikeWalk(string(s), string(p)); got != want {
			t.Fatalf("%q LIKE %q: segments %v, walk %v", s, p, got, want)
		}
	}
	for _, p := range []string{"a_b", `a\%b`, "%_%", `\\`} {
		if _, ok := matchLikeSegments("a%b", p); ok {
			t.Fatalf("pattern %q took the fast path", p)
		}
	}
	// The Q13 witness.
	if !matchSQLLike("carefully special packages requests sleep", "%special%requests%") ||
		matchSQLLike("requests are special", "%special%requests%") {
		t.Fatal("Q13 pattern misjudged")
	}
}
