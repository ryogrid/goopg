package nbtree

import (
	"fmt"
	"testing"
	"unsafe"
)

func TestBTreeSizeIsSane(t *testing.T) {
	sz := unsafe.Sizeof(BTree{})
	fmt.Printf("sizeof(BTree) = %d bytes (%.1f KiB)\n", sz, float64(sz)/1024)
	// After M0107-0008g revert: BTree must be < 1 KiB (was 32 KiB with stats.Counter)
	if sz > 1024 {
		t.Errorf("sizeof(BTree) = %d bytes — too large; stats.Counter may have crept back in", sz)
	}
}
