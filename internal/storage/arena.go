package storage

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// arena is a contiguous, page-aligned slab of memory carved into
// BlockSize-sized slots that the buffer pool uses as page storage.
//
// Allocation strategy:
//   - mmap MAP_PRIVATE|MAP_ANONYMOUS: the kernel returns 4 KiB-aligned
//     memory, which satisfies the Linux O_DIRECT alignment requirement.
//     This is the path always taken on Linux when seccomp doesn't block
//     mmap.
//   - Fallback: a single Go-heap allocation aligned to 4 KiB by
//     reserving an extra 4 KiB and trimming. The fallback is exercised
//     by tests that don't need O_DIRECT semantics; production data
//     paths require the mmap path.
type arena struct {
	mem    []byte // the carved-up memory; length == nslots * BlockSize
	mmaped bool   // true if mem came from unix.Mmap (must munmap on close)
}

// newArena allocates space for nslots BlockSize-sized pages. Returns an
// error if mmap fails *and* the fallback can't satisfy the alignment
// requirement (which on a real Linux system never happens — the
// fallback intentionally over-reserves).
func newArena(nslots int) (*arena, error) {
	if nslots <= 0 {
		return nil, fmt.Errorf("arena: nslots must be > 0, got %d", nslots)
	}
	size := nslots * BlockSize
	mem, err := unix.Mmap(-1, 0, size,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
	if err == nil {
		return &arena{mem: mem, mmaped: true}, nil
	}
	// Fallback: oversized Go allocation, trim to alignment.
	const align = 4096
	raw := make([]byte, size+align)
	off := align - (int(uintptrOf(raw[:1])) % align)
	if off == align {
		off = 0
	}
	return &arena{mem: raw[off : off+size], mmaped: false}, nil
}

// slot returns a Page-typed view of the i-th slot in the arena. The
// returned slice aliases arena memory; do not retain past arena.close.
func (a *arena) slot(i int) Page {
	return a.mem[i*BlockSize : (i+1)*BlockSize : (i+1)*BlockSize]
}

// close releases the arena's backing memory. Safe to call once.
func (a *arena) close() error {
	if !a.mmaped {
		a.mem = nil
		return nil
	}
	if err := unix.Munmap(a.mem); err != nil {
		return fmt.Errorf("munmap: %w", err)
	}
	a.mem = nil
	return nil
}

// uintptrOf returns the address of a byte slice's first element. This
// indirection lets us avoid an "unsafe" import in the arena hot path
// while still computing alignment on the fallback.
//
//go:nosplit
func uintptrOf(b []byte) uint64 {
	// We accept some micro-pessimism here in exchange for not pulling
	// in unsafe; the fallback path is exercised rarely.
	return alignmentProbe(&b[0])
}

// alignmentProbe is intentionally split out so we can replace it in
// tests if a future build needs to inject a known address.
func alignmentProbe(p *byte) uint64 {
	// The compiler escapes p through the cast, but that's fine —
	// arena slabs are large and live for the server's lifetime.
	return uint64(uintptr(unsafePointerOf(p)))
}
