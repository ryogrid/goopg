package storage

import "fmt"

// arena is a contiguous, 4 KiB-aligned slab of memory carved into
// BlockSize-sized slots that the buffer pool uses as page storage.
//
// Allocation strategy: a single Go-heap allocation over-reserved by
// one extra page so the start can be trimmed to 4 KiB alignment.
// 4 KiB alignment matches the natural alignment of most Linux
// filesystems and is preferable for efficient page I/O.
type arena struct {
	mem []byte // the carved-up memory; length == nslots * BlockSize
}

// newArena allocates space for nslots BlockSize-sized pages. Returns
// an error if nslots <= 0.
func newArena(nslots int) (*arena, error) {
	if nslots <= 0 {
		return nil, fmt.Errorf("arena: nslots must be > 0, got %d", nslots)
	}
	size := nslots * BlockSize
	const align = 4096
	raw := make([]byte, size+align)
	off := align - (int(uintptrOf(raw[:1])) % align)
	if off == align {
		off = 0
	}
	return &arena{mem: raw[off : off+size]}, nil
}

// slot returns a Page-typed view of the i-th slot in the arena. The
// returned slice aliases arena memory; do not retain past arena.close.
func (a *arena) slot(i int) Page {
	return a.mem[i*BlockSize : (i+1)*BlockSize : (i+1)*BlockSize]
}

// close releases the arena's backing memory. Safe to call once.
func (a *arena) close() error {
	a.mem = nil
	return nil
}

// uintptrOf returns the address of a byte slice's first element. This
// indirection lets us avoid an "unsafe" import in the arena hot path
// while still computing alignment.
//
//go:nosplit
func uintptrOf(b []byte) uint64 {
	return alignmentProbe(&b[0])
}

// alignmentProbe is intentionally split out so we can replace it in
// tests if a future build needs to inject a known address.
func alignmentProbe(p *byte) uint64 {
	return uint64(uintptr(unsafePointerOf(p)))
}
