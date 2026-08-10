package btree

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-i guards. The seam's whole job is to be an EXACT
// no-op today and the single switch tomorrow, so the tests pin both halves:
// the nil-descriptor branch must be byte-for-byte CompareKeys, and the
// descriptor branch must actually reach _bt_compare (i.e. produce an order
// bytes.Compare does NOT).

// TestKeyComparerNilDescIsBytewise pins the no-op half: with no descriptor the
// seam must agree with CompareKeys on every pair, including the prefix and
// empty-key cases the descent path relies on (the minus-infinity leftmost
// downlink carries an empty key).
func TestKeyComparerNilDescIsBytewise(t *testing.T) {
	var c keyComparer
	operands := [][]byte{
		nil, {}, {0x00}, {0x00, 0x00}, {0x01}, {0x01, 0x00}, {0x7f}, {0x80},
		{0xff}, {0xff, 0xff}, []byte("abc"), []byte("abcd"), []byte("abd"),
	}
	for _, a := range operands {
		for _, b := range operands {
			got := c.compare(a, b)
			want := CompareKeys(a, b)
			if got != want {
				t.Fatalf("compare(%x, %x) = %d, CompareKeys = %d", a, b, got, want)
			}
		}
	}
}

// TestKeyComparerDescRoutesToBtCompare pins the switch half. int4 is the
// sharpest case available: PG stores the datum as a little-endian native
// int32, so -1 (ff ff ff ff) is bytewise GREATER than 1 (01 00 00 00) while
// btint4cmp orders it smaller. If the seam ever stopped consulting the
// descriptor, this reverts to the bytewise answer.
func TestKeyComparerDescRoutesToBtCompare(t *testing.T) {
	attr := int4Attr()
	attr.Compare = PGCompareInt4
	desc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{attr}}
	c := keyComparer{desc: desc}

	tid := storage.ItemPointer{Block: 1, Offset: 1}
	neg := tup(t, desc.Attrs, [][]byte{int4Val(-1)}, tid)
	pos := tup(t, desc.Attrs, [][]byte{int4Val(1)}, tid)

	if got := c.compare(neg, pos); got >= 0 {
		t.Fatalf("compare(-1, 1) = %d, want < 0 (int4 order)", got)
	}
	if got := c.compare(pos, neg); got <= 0 {
		t.Fatalf("compare(1, -1) = %d, want > 0 (int4 order)", got)
	}
	// And confirm the bytewise order really is the opposite one, so the test
	// above is discriminating rather than accidentally agreeing.
	if bytes.Compare(neg, pos) <= 0 {
		t.Fatalf("premise broken: bytewise already orders -1 before 1")
	}
}

// TestKeyComparerFallsBackOnUndecodableOperand pins the no-error contract.
// ComparePGIndexTuples refuses a posting-list tuple (it cannot know which of
// the posting's heap TIDs to tiebreak on), but the descent and split loops
// have nowhere to put an error and need a TOTAL, deterministic order or a
// split will not terminate. The seam must therefore fall back to bytewise
// rather than panic, return 0 for everything, or leak the error.
func TestKeyComparerFallsBackOnUndecodableOperand(t *testing.T) {
	attr := int4Attr()
	attr.Compare = PGCompareInt4
	desc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{attr}}
	c := keyComparer{desc: desc}

	plain := tup(t, desc.Attrs, [][]byte{int4Val(7)}, storage.ItemPointer{Block: 1, Offset: 1})
	posting := append([]byte(nil), plain...)
	posting = append(posting, make([]byte, 12)...)
	if err := BTreeTupleSetPosting(posting, 2, len(plain)); err != nil {
		t.Fatalf("BTreeTupleSetPosting: %v", err)
	}
	if !BTreeTupleIsPosting(posting) {
		t.Fatalf("fixture is not a posting tuple")
	}

	got := c.compare(plain, posting)
	if want := CompareKeys(plain, posting); got != want {
		t.Fatalf("compare on a posting operand = %d, want the bytewise fallback %d", got, want)
	}
	// Antisymmetry is the property a split actually depends on.
	if rev := c.compare(posting, plain); rev != -got {
		t.Fatalf("fallback is not antisymmetric: %d vs %d", got, rev)
	}
}

// TestBTreeKeyCmpCarriesOptionsDescriptor pins the wiring: a descriptor
// supplied through Options must reach the tree's comparer, and its absence
// must leave the tree bytewise. Without this the seam could be perfectly
// correct and still never be consulted by a real index.
func TestBTreeKeyCmpCarriesOptionsDescriptor(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 32})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9100, Fork: storage.MainFork}

	desc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{int4Attr()}}
	bt, err := CreateWithOptions(pool, rel, Options{KeyDesc: desc})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}
	if bt.keyCmp().desc != desc {
		t.Fatalf("CreateWithOptions dropped Options.KeyDesc")
	}
	reopened, err := OpenWithOptions(pool, rel, Options{KeyDesc: desc})
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	if reopened.keyCmp().desc != desc {
		t.Fatalf("OpenWithOptions dropped Options.KeyDesc")
	}
	// And the default path — every caller today — must stay bytewise.
	plain, err := Open(pool, rel)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if plain.keyCmp().desc != nil {
		t.Fatalf("a tree opened without a descriptor must compare bytewise")
	}
}
