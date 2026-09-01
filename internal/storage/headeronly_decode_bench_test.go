package storage

import "testing"

// Benchmarks for the header-only decode conversions of review/260831
// ST-1..ST-4: CollectDeadHeapSlots, PageAllVisible/PageAllFrozen,
// pagePruneCore and pruneChainTip inspect only HeapTuple.Header, so they use
// parseHeapTupleAlias instead of ParseHeapTuple's copying decode. These pin
// the resulting "zero allocations per page" property — a regression here means
// a copying decode crept back onto a VACUUM/prune page loop.
func benchHeapPage(tb testing.TB, ntup, datalen int) Page {
	tb.Helper()
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		tb.Fatal(err)
	}
	data := make([]byte, datalen)
	for i := 0; i < ntup; i++ {
		t := HeapTuple{
			Header: HeapTupleHeader{Xmin: TransactionID(100 + i), CTID: ItemPointer{Offset: uint16(i + 1)}},
			Data:   data,
		}
		if _, err := PageAddHeapTuple(p, t); err != nil {
			break
		}
	}
	return p
}

func BenchmarkCollectDeadHeapSlots(b *testing.B) {
	p := benchHeapPage(b, 200, 24)
	isDead := func(h HeapTupleHeader) bool { return h.Xmax != InvalidTransactionID }
	b.ReportAllocs()
	for b.Loop() {
		if _, err := CollectDeadHeapSlots(p, isDead); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPageAllVisible(b *testing.B) {
	p := benchHeapPage(b, 200, 24)
	b.ReportAllocs()
	for b.Loop() {
		PageAllVisible(p, TransactionID(1<<20))
	}
}

func BenchmarkPagePruneOpt(b *testing.B) {
	p := benchHeapPage(b, 200, 24)
	scratch := make(Page, BlockSize)
	b.ReportAllocs()
	for b.Loop() {
		copy(scratch, p)
		if _, _, err := pagePruneCore(scratch, TransactionID(50)); err != nil {
			b.Fatal(err)
		}
	}
}
