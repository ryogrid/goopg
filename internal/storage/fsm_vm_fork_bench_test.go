package storage

import "testing"

// TestFSMPageMaxCatMatchesParse pins review/260831 ST-7: reading fp_nodes[0]
// must equal the maximum parseFSMPage reports after scanning every node.
func TestFSMPageMaxCatMatchesParse(t *testing.T) {
	for _, n := range []int{1, 7, fsmSlotsPerPage / 2, fsmSlotsPerPage} {
		cats := make([]uint8, n)
		for i := range cats {
			cats[i] = uint8((i * 37) % 256)
		}
		pg := buildFSMPage(cats, n)
		_, want := parseFSMPage(pg)
		if got := fsmPageMaxCat(pg); got != want {
			t.Errorf("n=%d: fsmPageMaxCat = %d, parseFSMPage max = %d", n, got, want)
		}
	}
	// An all-zero page has no free space anywhere.
	if got := fsmPageMaxCat(buildFSMPage(make([]uint8, fsmSlotsPerPage), fsmSlotsPerPage)); got != 0 {
		t.Errorf("empty page max = %d, want 0", got)
	}
}

// BenchmarkBuildFSMTree measures building a relation's FSM fork
// (review/260831 ST-7): every page built used to be re-parsed to recover one
// byte, which allocated a full leaf-category slice per page.
func BenchmarkBuildFSMTree(b *testing.B) {
	cats := make([]uint8, 100000)
	for i := range cats {
		cats[i] = uint8(i % 250)
	}
	b.ReportAllocs()
	for b.Loop() {
		if len(buildFSMTree(cats)) == 0 {
			b.Fatal("no pages")
		}
	}
}

// BenchmarkReadVMForkDecode measures decoding a visibility-map fork's pages
// (review/260831 ST-8).
func BenchmarkReadVMForkDecode(b *testing.B) {
	const npages = 16
	data := make([]byte, npages*BlockSize)
	for i := range data {
		data[i] = byte(i)
	}
	b.ReportAllocs()
	for b.Loop() {
		masks := make([]uint8, 0, npages*vmMaxHeapPagesPerPage)
		for i := 0; i < npages; i++ {
			masks = appendVMPage(masks, data[i*BlockSize:(i+1)*BlockSize])
		}
		if len(masks) != npages*vmMaxHeapPagesPerPage {
			b.Fatalf("decoded %d masks", len(masks))
		}
	}
}
