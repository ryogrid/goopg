// Package mctx provides PG-style hierarchical memory contexts.
//
// Three primary tiers mirror PostgreSQL's MemoryContext discipline:
//
//	SessionContext  (per-connection)
//	└── TxnContext  (per-transaction)
//	    └── StmtContext  (per-statement)
//	        └── ExprContext  (per-row scratch; optional)
//
// Each Context owns a set of 64 KiB chunk slabs. Allocations bump a
// pointer inside the current chunk; when the chunk is full a new one is
// obtained from a per-size sync.Pool (first call) or reused (steady-state).
// Reset() rewinds all chunks to length 0 but retains the backing arrays;
// Release() returns them to the pool and removes the context from the registry.
//
// Offset encoding: every allocation is identified by an (offset, length uint32)
// pair where offset = chunkIdx*defaultChunkSize + byteOffsetWithinChunk.
// This matches the legacy executor.Arena encoding so KindStringArena /
// KindBytesArena Datums that store the packed int64 continue to work after
// the field type changes from *Arena to *Context.
//
// Design: docs/design/perf-optimize/01-memory-context.md (M0107-0001).
package mctx

import (
	"sync"
	"unsafe"
)

const (
	defaultChunkSize = 64 * 1024  // 64 KiB — matches legacy arenaPageSize
	smallChunkSize   = 4 * 1024   // for ExprContext per-row scratch
	largeChunkSize   = 256 * 1024 // for sort/hash-join build sides
)

// ContextID is a dense 16-bit handle into the global context registry.
// Datum stores the ContextID to reference payload bytes without holding
// a Go pointer (enabling pointer-free Datum in Phase B).
type ContextID uint16

const (
	InvalidContextID ContextID = 0
	PermContextID    ContextID = 1 // process-global permanent context; never reset
)

// Kind classifies the lifetime tier of a Context.
type Kind uint8

const (
	KindInvalid Kind = iota
	KindSession
	KindTxn
	KindStmt
	KindExpr
)

// chunk is a single backing slab.
type chunk struct {
	buf []byte // len == bytes consumed; cap == chunk size
}

// Context is a bump-allocator with explicit Reset/Release semantics.
// Each backend owns its own context tree exclusively; concurrent access
// is a contract violation.
type Context struct {
	parent   *Context
	children []*Context
	chunks   []chunk
	head     int32 // index of current active chunk
	id       ContextID
	gen      uint32 // bumped on Reset; for debug use-after-Reset detection
	kind     Kind
	cs       uint32 // chunkSize for this context
}

// --- registry ---

var (
	ctxRegistry     [65536]*Context
	ctxRegistryNext = ContextID(2) // 0=invalid, 1=perm
	ctxFreeList     []ContextID
	ctxMu           sync.Mutex
)

// permMu guards permanentContext's chunks/head. It lives at package level
// rather than on Context because exactly one context is ever shared, and
// Context is size-constrained to <=96 B by the design (see TestContextSizeof);
// an embedded RWMutex would cost 24 B on every per-statement and per-expression
// context to protect a single process-global one.
//
// RWMutex rather than Mutex because a permanent payload is written once and
// read many times: concurrent readers of distinct big numerics must not
// serialise on each other.
var permMu sync.RWMutex

// isShared reports whether c may be touched by more than one goroutine. Only
// the permanent context is: every per-session, per-statement and
// per-expression context is single-owner by contract (see the type comment),
// so they keep the lock-free fast path.
func (c *Context) isShared() bool { return c.id == PermContextID }

// permanentContext is the process-global literal-payload context (slot 1).
//
// It is the ONE Context that is legitimately shared across goroutines: every
// backend's big-mantissa numerics allocate from it (newBigNumericInCtx), and
// it is never Reset or Released, which is precisely why arena-backed numerics
// are safe to pass between goroutines at all.
//
// That sharing was previously unsynchronised. Because allocation is a bump
// pointer that appends to c.chunks, and Bytes reads that same slice, two
// concurrent SESSIONS doing arithmetic on numerics too large for an int64
// mantissa already raced here — a pre-existing defect, rare only because such
// values are rare. Parallel query would turn it from rare into routine, so it
// is fixed here rather than worked around.
//
// `shared` opts this one context into locking; every per-session, per-statement
// and per-expression context keeps the unsynchronised fast path, since those
// are single-owner by contract.
var permanentContext = func() *Context {
	c := &Context{cs: defaultChunkSize, id: PermContextID, kind: KindSession}
	ctxRegistry[PermContextID] = c
	return c
}()

func acquireID() ContextID {
	ctxMu.Lock()
	var id ContextID
	if n := len(ctxFreeList); n > 0 {
		id = ctxFreeList[n-1]
		ctxFreeList = ctxFreeList[:n-1]
	} else {
		id = ctxRegistryNext
		ctxRegistryNext++
	}
	ctxMu.Unlock()
	return id
}

func releaseID(id ContextID) {
	ctxMu.Lock()
	ctxRegistry[id] = nil
	ctxFreeList = append(ctxFreeList, id)
	ctxMu.Unlock()
}

// Lookup returns the Context for the given id, or nil if invalid / released.
// Used by Datum to resolve payload bytes via ContextID.
func Lookup(id ContextID) *Context {
	if id == InvalidContextID {
		return nil
	}
	ctxMu.Lock()
	c := ctxRegistry[id]
	ctxMu.Unlock()
	return c
}

// --- chunk pools ---

var (
	defaultPool sync.Pool // 64 KiB chunks
	smallPool   sync.Pool // 4 KiB chunks
	largePool   sync.Pool // 256 KiB chunks
)

func getChunk(cs uint32) []byte {
	var p *sync.Pool
	switch cs {
	case defaultChunkSize:
		p = &defaultPool
	case smallChunkSize:
		p = &smallPool
	case largeChunkSize:
		p = &largePool
	}
	if p != nil {
		if v := p.Get(); v != nil {
			b := v.([]byte)
			return b[:0]
		}
	}
	return make([]byte, 0, cs)
}

func putChunk(cs uint32, b []byte) {
	b = b[:0]
	switch cs {
	case defaultChunkSize:
		defaultPool.Put(b)
	case smallChunkSize:
		smallPool.Put(b)
	case largeChunkSize:
		largePool.Put(b)
	}
	// non-standard sizes are not pooled
}

// --- Acquire / Release / Reset ---

// Acquire returns a new Context of the given kind, parented to parent
// (nil for a root SessionContext). Draws a chunk from the per-size pool.
func Acquire(parent *Context, kind Kind) *Context {
	cs := uint32(defaultChunkSize)
	if kind == KindExpr {
		cs = smallChunkSize
	}
	id := acquireID()
	c := &Context{parent: parent, cs: cs, id: id, kind: kind}
	ctxMu.Lock()
	ctxRegistry[id] = c
	ctxMu.Unlock()
	if parent != nil {
		parent.children = append(parent.children, c)
	}
	return c
}

// ID returns c's registry slot.
func (c *Context) ID() ContextID { return c.id }

// Generation returns c's current generation counter (debug use-after-Reset detection).
func (c *Context) Generation() uint32 { return c.gen }

// Reset rewinds all chunks to length 0, cascades to children, and bumps gen.
// Backing arrays are retained for the next allocation cycle.
func (c *Context) Reset() {
	for i := range c.chunks {
		c.chunks[i].buf = c.chunks[i].buf[:0]
	}
	c.head = 0
	c.gen++
	for _, child := range c.children {
		child.Reset()
	}
}

// Release returns chunks to the pool, cascades Release to children, and
// removes c from the registry. After Release, c must not be used.
func (c *Context) Release() {
	for _, child := range c.children {
		child.Release()
	}
	c.children = c.children[:0]
	for i := range c.chunks {
		putChunk(c.cs, c.chunks[i].buf)
		c.chunks[i].buf = nil
	}
	c.chunks = c.chunks[:0]
	c.head = 0
	if c.id != InvalidContextID && c.id != PermContextID {
		releaseID(c.id)
	}
	c.id = InvalidContextID
	if c.parent != nil {
		// Remove self from parent's children slice.
		p := c.parent
		for i, ch := range p.children {
			if ch == c {
				p.children = append(p.children[:i], p.children[i+1:]...)
				break
			}
		}
		c.parent = nil
	}
}

// --- Allocation ---

// growChunk adds a new chunk of size cs (or n if oversized) AFTER the
// current head, preserving the small-chunk tail (mirrors Arena.Allocate).
func (c *Context) growChunk(n int) {
	var buf []byte
	if n > int(c.cs) {
		buf = make([]byte, 0, n)
	} else {
		buf = getChunk(c.cs)
	}
	newIdx := int(c.head) + 1
	// Insert after head.
	c.chunks = append(c.chunks, chunk{})
	copy(c.chunks[newIdx+1:], c.chunks[newIdx:])
	c.chunks[newIdx] = chunk{buf: buf}
	c.head = int32(newIdx)
}

// Alloc returns n zero-initialized bytes owned by c.
func (c *Context) Alloc(n int) []byte {
	if n <= 0 {
		return nil
	}
	if c.isShared() {
		permMu.Lock()
		defer permMu.Unlock()
	}
	if len(c.chunks) == 0 {
		c.chunks = append(c.chunks, chunk{buf: getChunk(c.cs)})
		c.head = 0
	}
	cur := &c.chunks[c.head]
	if cap(cur.buf)-len(cur.buf) < n {
		c.growChunk(n)
		cur = &c.chunks[c.head]
	}
	start := len(cur.buf)
	cur.buf = cur.buf[:start+n]
	result := cur.buf[start : start+n]
	// Zero-initialize (getChunk returns a reused slice; cap bytes may be stale).
	for i := range result {
		result[i] = 0
	}
	return result
}

// AllocAligned returns aligned storage. align must be a power of two in [1, 64].
func (c *Context) AllocAligned(n, align int) []byte {
	if c.isShared() {
		permMu.Lock()
		defer permMu.Unlock()
	}
	if len(c.chunks) == 0 {
		c.chunks = append(c.chunks, chunk{buf: getChunk(c.cs)})
		c.head = 0
	}
	cur := &c.chunks[c.head]
	pad := 0
	if align > 1 {
		offset := len(cur.buf)
		pad = (align - (offset & (align - 1))) & (align - 1)
	}
	total := n + pad
	if cap(cur.buf)-len(cur.buf) < total {
		c.growChunk(n) // oversized chunks start aligned at offset 0
		cur = &c.chunks[c.head]
		pad = 0
		total = n
	}
	start := len(cur.buf) + pad
	cur.buf = cur.buf[:start+n]
	result := cur.buf[start : start+n]
	for i := range result {
		result[i] = 0
	}
	return result
}

// allocBytes copies b into c and returns the absolute (offset, length).
// offset = chunkIdx * defaultChunkSize + byteOffsetWithinChunk.
// This matches executor.Arena's Allocate offset semantics exactly.
func (c *Context) allocBytes(b []byte) (offset, length uint32) {
	n := len(b)
	if n == 0 {
		return 0, 0
	}
	if c.isShared() {
		permMu.Lock()
		defer permMu.Unlock()
	}
	if len(c.chunks) == 0 {
		c.chunks = append(c.chunks, chunk{buf: getChunk(c.cs)})
		c.head = 0
	}
	cur := &c.chunks[c.head]
	if cap(cur.buf)-len(cur.buf) < n {
		c.growChunk(n)
		cur = &c.chunks[c.head]
	}
	start := len(cur.buf)
	cur.buf = append(cur.buf, b...)
	// offset = chunkIdx * chunkSize + byteOffsetWithinChunk
	off := uint32(c.head)*c.cs + uint32(start)
	return off, uint32(n)
}

// AllocBytes copies b into c and returns (offset, length) for later Bytes() resolution.
func (c *Context) AllocBytes(b []byte) (offset, length uint32) {
	return c.allocBytes(b)
}

// AllocString copies s into c and returns (offset, length).
func (c *Context) AllocString(s string) (offset, length uint32) {
	return c.allocBytes([]byte(s))
}

// Bytes resolves (offset, length) into the backing slice.
// Returns nil when length == 0.
func (c *Context) Bytes(offset, length uint32) []byte {
	if length == 0 {
		return nil
	}
	// The read path needs the lock too, not just allocation: growChunk both
	// appends to c.chunks and memmoves its tail, so a concurrent reader can
	// otherwise observe a torn or relocated slice header.
	if c.isShared() {
		permMu.RLock()
		defer permMu.RUnlock()
	}
	chunkIdx := offset / c.cs
	chunkStart := offset % c.cs
	if int(chunkIdx) >= len(c.chunks) {
		return nil
	}
	page := c.chunks[chunkIdx].buf
	if int(chunkStart)+int(length) > cap(page) {
		return nil
	}
	// Use cap so we can read bytes allocated but past len — same as Arena.Bytes.
	return page[chunkStart : chunkStart+length : cap(page)]
}

// --- Generic allocation helpers ---

// AllocFor returns a *T allocated from c. The returned pointer is valid
// until c.Reset() or c.Release(). T must not contain GC-traced fields
// that point outside c, or the pointer-free invariant is violated.
func AllocFor[T any](c *Context) *T {
	var zero T
	size := int(unsafe.Sizeof(zero))
	align := int(unsafe.Alignof(zero))
	buf := c.AllocAligned(size, align)
	return (*T)(unsafe.Pointer(&buf[0]))
}

// AllocSlice returns []T of length n allocated from c.
//
// T MUST be pointer-free. The backing slab is allocated as []byte, i.e. a GC
// noscan span, so any Go pointer stored in a T placed here (e.g. a string
// header) is invisible to the mark phase and will be collected out from under
// you; arena memory is also explicitly recycled, so it cannot back any value
// whose lifetime may exceed c. Storing parser.Token ([]Token) here is the
// canonical violation — see docs/design/0107-0003d-token-pool-gc-safety.md.
func AllocSlice[T any](c *Context, n int) []T {
	if n <= 0 {
		return nil
	}
	var zero T
	size := int(unsafe.Sizeof(zero))
	align := int(unsafe.Alignof(zero))
	buf := c.AllocAligned(size*n, align)
	return unsafe.Slice((*T)(unsafe.Pointer(&buf[0])), n)
}

// Perm returns the process-global permanent context (slot 1).
// It is never reset or released; use it for literal-string payloads
// that must survive for the process lifetime.
func Perm() *Context { return permanentContext }
