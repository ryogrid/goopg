package nbtree

import (
	"encoding/binary"
	"math/rand"
	"testing"
)

// TestProbeTupleOKMatchesParseItemBody pins M0122-0012 C28: a binary-search
// probe that compares a tuple in place must accept exactly the tuples
// parseItemBody accepts, or the search would compare an item the parser
// rejects (or skip one it accepts). Random tuples are biased so every
// rejection branch is reached: a t_info size that disagrees with the length,
// the alt-TID bit with and without the posting flag, heap-TID pivots of
// every short length, and plain tuples.
func TestProbeTupleOKMatchesParseItemBody(t *testing.T) {
	rng := rand.New(rand.NewSource(20261027))
	branches := map[string]int{}
	for i := 0; i < 200000; i++ {
		n := rng.Intn(24)
		raw := make([]byte, n)
		rng.Read(raw)
		if n >= SizeOfIndexTupleData {
			info := uint16(n)
			if rng.Intn(5) == 0 {
				info = uint16(rng.Intn(64))
			}
			if rng.Intn(2) == 0 {
				info |= IndexAltTIDMask
			}
			binary.LittleEndian.PutUint16(raw[6:8], info)
		}
		_, _, _, err := parseItemBody(raw)
		want := err == nil
		if got := probeTupleOK(raw); got != want {
			t.Fatalf("raw %x: probeTupleOK = %v, parseItemBody err = %v", raw, got, err)
		}
		switch {
		case n < SizeOfIndexTupleData:
			branches["short"]++
		case PGIndexTupleSize(raw) != n:
			branches["size"]++
		case !PGIndexTupleIsAltTID(raw):
			branches["plain"]++
		case BTreeTupleIsPosting(raw):
			branches["posting"]++
		case want:
			branches["pivot-ok"]++
		default:
			branches["pivot-short"]++
		}
	}
	for _, b := range []string{"short", "size", "plain", "posting", "pivot-ok", "pivot-short"} {
		if branches[b] == 0 {
			t.Errorf("branch %q never reached: %v", b, branches)
		}
	}
}
