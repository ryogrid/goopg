package storage

import "testing"

// fsmBenchRel is one relation's worth of FSM state for the lookup benchmarks:
// npages pages of which only a handful (the last few) have room left, which is
// the steady state of an append-heavy relation.
func fsmBenchFSM(npages int) (*FSM, RelFileNode) {
	f := NewFSM()
	rel := RelFileNode{DBOid: 1, RelOid: 16384}
	for blk := 0; blk < npages; blk++ {
		free := uint16(0)
		if blk >= npages-4 {
			free = uint16(2000 + blk%100)
		}
		f.RecordFreeSpace(rel, BlockNumber(blk), free)
	}
	return f, rel
}

// BenchmarkFSMGetCandidates measures the insert path's page choice
// (review/260831 ST-6). Both lookups used to scan every registered page of the
// relation, so the cost of picking an insert target grew with the table.
func BenchmarkFSMGetCandidates(b *testing.B) {
	for _, npages := range []int{64, 100000} {
		f, rel := fsmBenchFSM(npages)
		b.Run(fsmBenchLabel(npages), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if got := f.GetCandidates(rel, 1000, 4); len(got) == 0 {
					b.Fatal("no candidate")
				}
			}
		})
	}
}

// BenchmarkFSMGetPageWithFreeSpace is GetPageWithFreeSpace's half of ST-6.
func BenchmarkFSMGetPageWithFreeSpace(b *testing.B) {
	for _, npages := range []int{64, 100000} {
		f, rel := fsmBenchFSM(npages)
		b.Run(fsmBenchLabel(npages), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, ok := f.GetPageWithFreeSpace(rel, 1000); !ok {
					b.Fatal("no page")
				}
			}
		})
	}
}

// BenchmarkFSMInsertCycle is the shape an INSERT actually drives: pick a
// target, then record the page's reduced free space. It is the guard against
// paying for a faster lookup with a slower record.
func BenchmarkFSMInsertCycle(b *testing.B) {
	for _, npages := range []int{64, 100000} {
		f, rel := fsmBenchFSM(npages)
		b.Run(fsmBenchLabel(npages), func(b *testing.B) {
			b.ReportAllocs()
			free := uint16(8000)
			for b.Loop() {
				cands := f.GetCandidates(rel, 100, 4)
				if len(cands) == 0 {
					// Refill: the benchmark must not measure an empty map.
					free = 8000
					f.RecordFreeSpace(rel, BlockNumber(npages-1), free)
					continue
				}
				if free > 100 {
					free -= 100
				} else {
					free = 8000
				}
				f.RecordFreeSpace(rel, cands[0], free)
			}
		})
	}
}

func fsmBenchLabel(npages int) string {
	if npages < 1000 {
		return "pages=64"
	}
	return "pages=100k"
}
