package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// benchAssembleInputs builds one record's inputs. image is "" (no image),
// "hole" (a standard page, so the free-space hole is omitted) or "full" (a page
// with no usable standard header, so all 8192 bytes are emitted).
func benchAssembleInputs(image string) ([]byte, []BlockRef) {
	mainData := make([]byte, 24)
	for i := range mainData {
		mainData[i] = byte(i)
	}
	data := make([]byte, 96)
	for i := range data {
		data[i] = byte(i)
	}
	ref := BlockRef{ID: 0, Rel: storage.RelFileNode{DBOid: 1, RelOid: 16384}, Block: 42, Data: data}
	switch image {
	case "hole":
		page := make(storage.Page, storage.BlockSize)
		if err := storage.InitPage(page); err != nil {
			panic(err)
		}
		ref.Image = &FullPageImage{Page: page}
	case "full":
		page := make(storage.Page, storage.BlockSize)
		for i := range page {
			page[i] = 0xFF // no usable standard header: the whole page ships
		}
		ref.Image = &FullPageImage{Page: page}
	}
	return mainData, []BlockRef{ref}
}

// BenchmarkAssembleXLogRecord measures WAL record assembly (review/260831
// XL-68): the header and payload regions used to grow from nil and then be
// concatenated into a third buffer, and a full-page image was built in its own
// page-sized buffer before being copied into the payload.
func BenchmarkAssembleXLogRecord(b *testing.B) {
	for _, image := range []string{"", "hole", "full"} {
		name := "data-only"
		if image != "" {
			name = "fpi-" + image
		}
		mainData, blocks := benchAssembleInputs(image)
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				out, err := assembleXLogRecord(mainData, blocks)
				if err != nil || len(out) == 0 {
					b.Fatalf("assembleXLogRecord: %v", err)
				}
			}
		})
	}
}

// BenchmarkEncodeRecordXLog measures the goopg-record encode path
// (review/260831 XL-14): the main-data chunk used to be wrapped in its own
// buffer and then copied into the output record, so every WAL record copied its
// payload twice.
func BenchmarkEncodeRecordXLog(b *testing.B) {
	for _, size := range []int{64, 4096} {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(i)
		}
		b.Run(map[bool]string{true: "payload=64", false: "payload=4096"}[size == 64], func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				out, n, err := encodeRecordXLog(payload, 0)
				if err != nil || n == 0 || len(out) == 0 {
					b.Fatalf("encodeRecordXLog: %v", err)
				}
			}
		})
	}
}

// TestFoldChangesNoFoldReturnsInput pins review/260831 XL-38: when no
// delete+insert pair folds, foldChanges must return the input untouched (and,
// as the point of the change, without copying it).
func TestFoldChangesNoFoldReturnsInput(t *testing.T) {
	in := []Change{
		{Kind: ChangeInsert, Rel: benchRel(1)},
		{Kind: ChangeInsert, Rel: benchRel(1)},
		{Kind: ChangeDelete, Rel: benchRel(1)},
		{Kind: ChangeInsert, Rel: benchRel(2)}, // different relation: not a fold
	}
	out := foldChanges(in)
	if len(out) != len(in) {
		t.Fatalf("folded %d changes into %d, want no fold", len(in), len(out))
	}
	for i := range in {
		if out[i].Kind != in[i].Kind || out[i].Rel != in[i].Rel {
			t.Fatalf("change %d changed: %+v vs %+v", i, out[i], in[i])
		}
	}
}

// TestFoldChangesStillFolds keeps the folding behaviour itself pinned, with
// the pair in the middle so the "copy the prefix" path runs.
func TestFoldChangesStillFolds(t *testing.T) {
	in := []Change{
		{Kind: ChangeInsert, Rel: benchRel(1)},
		{Kind: ChangeDelete, Rel: benchRel(2), Block: 7},
		{Kind: ChangeInsert, Rel: benchRel(2), Block: 9},
		{Kind: ChangeInsert, Rel: benchRel(3)},
	}
	out := foldChanges(in)
	if len(out) != 3 {
		t.Fatalf("got %d changes, want 3", len(out))
	}
	if out[1].Kind != ChangeUpdate || out[1].Rel != benchRel(2) || out[1].Block != 9 {
		t.Fatalf("middle change = %+v, want an UPDATE on rel 2 block 9", out[1])
	}
	if out[0].Kind != ChangeInsert || out[0].Rel != benchRel(1) || out[2].Rel != benchRel(3) {
		t.Fatalf("surrounding changes were not preserved: %+v", out)
	}
}

// BenchmarkFoldChangesNoFold is the common shape: a transaction of inserts
// with nothing to fold.
func BenchmarkFoldChangesNoFold(b *testing.B) {
	in := make([]Change, 256)
	for i := range in {
		in[i] = Change{Kind: ChangeInsert, Rel: benchRel(1)}
	}
	b.ReportAllocs()
	for b.Loop() {
		if len(foldChanges(in)) != len(in) {
			b.Fatal("unexpected fold")
		}
	}
}

// benchRel builds a distinct RelFileNode for the fold tests.
func benchRel(oid uint32) storage.RelFileNode {
	return storage.RelFileNode{DBOid: 1, RelOid: oid}
}

// BenchmarkFramedAssemble measures assembling and framing one pre-assembled PG
// record (review/260831 XL-21): the record used to be assembled and then copied
// again into a buffer carrying the 7-byte goopg envelope.
func BenchmarkFramedAssemble(b *testing.B) {
	mainData, blocks := benchAssembleInputs("")

	b.Run("assemble+frame", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			body, err := assembleXLogRecord(mainData, blocks)
			if err != nil {
				b.Fatal(err)
			}
			if out := framePGAssembled(RmgrHeap, 0, 7, body); len(out) == 0 {
				b.Fatal("empty record")
			}
		}
	})
	b.Run("framed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			out, err := framedAssemble(RmgrHeap, 0, 7, mainData, blocks)
			if err != nil || len(out) == 0 {
				b.Fatalf("framedAssemble: %v", err)
			}
		}
	})
}

// TestFramedAssembleMatchesFrameOfAssemble pins that framing through the
// prefix produces the same bytes as assembling then framing (review/260831
// XL-21).
func TestFramedAssembleMatchesFrameOfAssemble(t *testing.T) {
	for _, image := range []string{"", "hole", "full"} {
		mainData, blocks := benchAssembleInputs(image)
		body, err := assembleXLogRecord(mainData, blocks)
		if err != nil {
			t.Fatal(err)
		}
		want := framePGAssembled(RmgrHeap, 3, 42, body)
		got, err := framedAssemble(RmgrHeap, 3, 42, mainData, blocks)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("image=%q: framedAssemble produced %d bytes, assemble+frame %d", image, len(got), len(want))
		}
	}
}
