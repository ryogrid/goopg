package executor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0130-S11.4 slice 3b-2c-ii-B2-c-ii guards — the upper-bound funnel.
//
// Six probe sites (index scan ×2, index-only scan ×2, bitmap index scan,
// the storage UPDATE-by-index path) used to build a composite-index prefix
// upper bound by calling `appendCompositeUpperPadding` themselves. That is a
// BLOB-format repair: 64 trailing 0xFF bytes chosen to sort above any
// plausible suffix encoding under `bytes.Compare`. Under the tuple format the
// same bound is the prefix pivot UNCHANGED, read as plus infinity beyond the
// attributes it names by the `compareHigh` end that slice B2-c-i introduced;
// 0xFF padding there is not a large key but a malformed one.
//
// So the choice is per-format, and after this slice exactly one place makes
// it. These tests pin the two branches and the funnel itself.

func upperBoundTestKey() []byte { return []byte{0x01, 0x02, 0x03} }

func TestCompositeUpperBoundBlobIsPaddingByteForByte(t *testing.T) {
	// Gate off is the shipped state: every index is blob, so the funnel must
	// reproduce the pre-slice bytes exactly. Anything else is an on-disk
	// behaviour change smuggled into a behaviour-preserving slice.
	tbl := keyDescTable(col("a", "int4"), col("b", "int4"))
	idx := &catalog.Index{OID: 200, Name: "i", Table: tbl, Method: "btree", Columns: []string{"a", "b"}}
	ctx := &Context{}
	key := upperBoundTestKey()

	got := ctx.compositeUpperBound(idx, key)
	want := appendCompositeUpperPadding(key)
	if !bytes.Equal(got, want) {
		t.Fatalf("blob bound = %x, want %x", got, want)
	}
	if len(got) != len(key)+compositeUpperPaddingLen {
		t.Fatalf("blob bound length = %d, want %d", len(got), len(key)+compositeUpperPaddingLen)
	}
	// The blob branch hands back a fresh slice; callers pass it to a scan
	// while still holding `key` as the LOW bound, so aliasing would make the
	// two ends of the range the same array.
	if len(got) > 0 && &got[0] == &key[0] {
		t.Error("blob bound aliases the caller's key")
	}
	if !bytes.Equal(key, upperBoundTestKey()) {
		t.Errorf("blob bound mutated the caller's key: %x", key)
	}
}

func TestCompositeUpperBoundTupleIsThePrefixItself(t *testing.T) {
	withPGIndexTupleKeys(t)
	tbl := keyDescTable(col("a", "int4"), col("b", "text"))
	idx := &catalog.Index{OID: 201, Name: "i", Table: tbl, Method: "btree", Columns: []string{"a", "b"}}
	ctx := &Context{}
	if ctx.pgIndexKeyDesc(idx) == nil {
		t.Fatal("fixture index is not describable; the test would pass for the wrong reason")
	}
	key := upperBoundTestKey()

	got := ctx.compositeUpperBound(idx, key)
	if !bytes.Equal(got, key) {
		t.Fatalf("tuple bound = %x, want the prefix itself %x", got, key)
	}
	// Explicit, because this is the whole point: no 0xFF run may reach a
	// tuple-format comparator, which would decode it as an attribute image.
	if bytes.Contains(got, bytes.Repeat([]byte{0xFF}, 8)) {
		t.Errorf("tuple bound carries blob padding: %x", got)
	}
}

func TestCompositeUpperBoundUndescribableIndexKeepsPadding(t *testing.T) {
	withPGIndexTupleKeys(t)
	tbl := keyDescTable(col("a", "int4"))
	// An expression key is the canonical refusal (see
	// TestPGIndexKeyDescUndescribableIsNilNotError). Such an index keeps the
	// blob key path even once the gate is on, so its bound must keep the
	// padding — the dual-format property, asserted here rather than assumed.
	idx := &catalog.Index{OID: 202, Name: "i", Table: tbl, Method: "btree", Columns: []string{"", "a"}}
	ctx := &Context{}
	if ctx.pgIndexKeyDesc(idx) != nil {
		t.Fatal("fixture index unexpectedly describable")
	}
	key := upperBoundTestKey()
	if got, want := ctx.compositeUpperBound(idx, key), appendCompositeUpperPadding(key); !bytes.Equal(got, want) {
		t.Fatalf("undescribable index bound = %x, want blob padding %x", got, want)
	}
}

func TestCompositeUpperBoundIsTheOnlyPaddingCaller(t *testing.T) {
	// The funnel is only worth anything if it cannot be bypassed: a seventh
	// probe site calling appendCompositeUpperPadding directly would emit
	// 0xFF into a tuple-format tree, and the failure would surface as wrong
	// ROWS in a scan, not as a compile error. Grep-enforce it instead.
	const needle = "appendCompositeUpperPadding("
	allowed := map[string]bool{
		"operators_index.go":         true, // the definition
		"pgindex_btree.go":           true, // the funnel
		"pgindex_upperbound_test.go": true, // this file
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, f := range files {
		if allowed[filepath.Base(f)] {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // prose may name it
			}
			if strings.Contains(line, needle) {
				t.Errorf("%s:%d bypasses (*Context).compositeUpperBound: %s", f, i+1, trimmed)
			}
		}
	}
}
