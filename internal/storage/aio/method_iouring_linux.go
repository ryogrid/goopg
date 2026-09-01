//go:build linux

// io_uring I/O method (Linux-only). Submission goroutines write
// SQEs under a mutex (the kernel's submission queue is a single-
// producer ring) and call `io_uring_enter(2)` to hand them off.
// A dedicated reaper goroutine drains the completion queue with
// blocking `io_uring_enter(0, min_complete=1, GETEVENTS)` so an
// idle ring costs one parked goroutine — not a busy spin.
//
// Why not vendor a full io_uring library: goopg's policy is to
// reimplement the upstream subsystem in Go without third-party
// dependencies. The surface area we need is narrow (READ + WRITE
// with explicit offsets, blocking GETEVENTS reap) and maps
// cleanly onto two raw syscalls (425 io_uring_setup, 426
// io_uring_enter) plus four mmaps. The kernel headers'
// `io_uring_sqe` and `io_uring_cqe` layouts are pinned ABI;
// reproducing them in Go is a matter of byte-for-byte field
// alignment.
//
// The probe lives in newMethodIOUring: if io_uring_setup returns
// ENOSYS / EPERM (sysctl-disabled or container-blocked) /
// EINVAL the caller in NewEngine catches errProbeFailed and
// silently falls back to MethodWorker — mirroring upstream's
// "io_method = io_uring" behaviour when the kernel can't honour
// the request. Successful probes keep the ring open for the
// engine's lifetime.

package aio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// io_uring opcodes we use. From <linux/io_uring.h>; the read /
// write opcodes take fd + offset + addr + len directly (no
// iovec wrapping) so we don't need IORING_OP_READV/WRITEV.
// NOP is used as a Close-time wakeup poke so the reaper can
// observe shutdown without busy-polling.
const (
	iorOpNop   uint8 = 0
	iorOpRead  uint8 = 22
	iorOpWrite uint8 = 23
)

// closeWakeUserData is the sentinel user_data Close uses to
// wake the reaper. Picked at the top of the uint64 range so it
// cannot collide with the monotonically-incrementing counter
// nextUD hands out for real ops.
const closeWakeUserData uint64 = 0xFFFFFFFFFFFFFFFF

// io_uring_enter flags.
const iorEnterGetEvents uint32 = 1 << 0

// mmap offsets from <linux/io_uring.h>. SQ ring at 0, CQ ring at
// 0x08000000, SQE array at 0x10000000. The kernel may return
// IORING_FEAT_SINGLE_MMAP in params.Features indicating SQ + CQ
// share one allocation; we don't bother with that fast-path
// (two mmaps is fine on every supported kernel).
const (
	iorOffSQRing uint64 = 0
	iorOffCQRing uint64 = 0x08000000
	iorOffSQEs   uint64 = 0x10000000
)

// io_uring syscall numbers (Linux only — these constants are
// stable across architectures via SYS_IO_URING_*).
const (
	sysIoUringSetup = 425
	sysIoUringEnter = 426
)

// errProbeFailed signals NewEngine to fall back to the worker
// method. Wraps the real cause for diagnostics.
var errProbeFailed = errors.New("aio: io_uring probe failed")

// io_sqring_offsets / io_cqring_offsets — byte-offset table the
// kernel returns from io_uring_setup so userspace knows where
// the head/tail/array fields live within the mmap'd ring.
type sqRingOffsets struct {
	Head        uint32
	Tail        uint32
	RingMask    uint32
	RingEntries uint32
	Flags       uint32
	Dropped     uint32
	Array       uint32
	Resv1       uint32
	Resv2       uint64
}

type cqRingOffsets struct {
	Head        uint32
	Tail        uint32
	RingMask    uint32
	RingEntries uint32
	Overflow    uint32
	Cqes        uint32
	Flags       uint32
	Resv1       uint32
	Resv2       uint64
}

// io_uring_params — argument to io_uring_setup. Layout is
// kernel-stable; do not reorder fields.
type ioUringParams struct {
	SqEntries    uint32
	CqEntries    uint32
	Flags        uint32
	SqThreadCpu  uint32
	SqThreadIdle uint32
	Features     uint32
	WqFd         uint32
	Resv         [3]uint32
	SqOff        sqRingOffsets
	CqOff        cqRingOffsets
}

// io_uring_sqe — 64-byte submission queue entry. We use the
// "small" layout (no big_sqe extension) which matches every
// kernel from 5.1 onward.
type ioSqe struct {
	Opcode      uint8
	Flags       uint8
	IoPrio      uint16
	Fd          int32
	Off         uint64
	Addr        uint64
	Len         uint32
	OpFlags     uint32
	UserData    uint64
	BufIndex    uint16
	Personality uint16
	SpliceFdIn  int32
	Pad2        [2]uint64
}

// io_uring_cqe — 16-byte completion queue entry.
type ioCqe struct {
	UserData uint64
	Res      int32
	Flags    uint32
}

// fdHaver lets the io_uring path extract the kernel fd from an
// otherwise-opaque aio.File. Implemented by *os.File and by the
// storage / wal adapters that wrap it. Files that don't satisfy
// it fall through to the synchronous slow path inside Submit.
type fdHaver interface {
	Fd() uintptr
}

// methodIOUring is the io_uring-backed I/O method.
type methodIOUring struct {
	engine *Engine

	ringFd int

	// Mmap'd ring memory. Keeping the slice references alive
	// keeps the runtime from GCing them out from under the
	// kernel-visible pointers we derive below.
	sqRingMmap []byte
	cqRingMmap []byte
	sqesMmap   []byte

	// SQ pointers (into sqRingMmap).
	sqHead    *uint32
	sqTail    *uint32
	sqMask    uint32
	sqEntries uint32
	sqArray   []uint32 // index ring → SQE slot

	// CQ pointers (into cqRingMmap).
	cqHead *uint32
	cqTail *uint32
	cqMask uint32
	cqes   []ioCqe

	// SQE slot array (the actual entries the kernel reads).
	sqes []ioSqe

	// submitMu serialises SQE writes + tail bumps + enter syscalls.
	// io_uring's SQ is documented as single-producer; without
	// this lock concurrent Submits would race the tail update.
	submitMu sync.Mutex

	// pending maps user_data → Handle so the reaper can finish
	// the right caller when a CQE arrives. Independent mutex
	// from submitMu because the reaper inserts (under submit)
	// and removes (under reap) on different paths.
	pendingMu sync.Mutex
	pending   map[uint64]pendingOp
	nextUD    atomic.Uint64

	// slots throttles concurrent in-flight ops to MaxConcurrency
	// (or sqEntries when no cap is set). Submit blocks on slot
	// acquisition when the ring is saturated.
	slots chan struct{}

	closed   chan struct{}
	closeOne sync.Once
	reaperWg sync.WaitGroup
}

// pendingOp pairs the Handle with the op metadata the reaper
// needs to finish it. We keep len(buf) here because the kernel
// reports a byte count (cqe.Res) but completing an EOF read
// also needs the original buffer length to detect truncation.
// buf is the slice actually submitted to the kernel (op.Buffer
// for reads; op.File's ChecksumFile.PrepareWrite result for
// writes when the File implements it, else op.Buffer) — kept
// here so the reaper has a live reference for the duration of
// the kernel op and so a read completion can be verified against
// the exact bytes that landed in it.
type pendingOp struct {
	h    *Handle
	dir  Direction
	want int
	buf  []byte
}

// newMethodIOUring runs the io_uring_setup probe and, on
// success, returns a wired-up engine method. On any kernel
// error (ENOSYS / EPERM / EINVAL / ENOMEM) it returns
// errProbeFailed so NewEngine can fall back to MethodWorker.
func newMethodIOUring(e *Engine, maxConcurrency int) (*methodIOUring, error) {
	// Pick the SQ depth. Round up to the nearest power of 2
	// (kernel rounds anyway — being explicit lets us size the
	// slot semaphore correctly). Default 32 entries is enough
	// to keep a small worker pool fed without burning kernel
	// memory; matches upstream's `io_uring_size` default.
	depth := uint32(32)
	if maxConcurrency > 0 {
		depth = nextPow2(uint32(maxConcurrency))
	}
	if depth < 4 {
		depth = 4
	}

	var params ioUringParams
	r1, _, errno := syscall.Syscall(sysIoUringSetup, uintptr(depth), uintptr(unsafe.Pointer(&params)), 0)
	if errno != 0 {
		return nil, fmt.Errorf("%w: io_uring_setup: %w", errProbeFailed, errno)
	}
	ringFd := int(r1)

	m := &methodIOUring{
		engine:  e,
		ringFd:  ringFd,
		pending: make(map[uint64]pendingOp),
		closed:  make(chan struct{}),
	}

	if err := m.mmapRings(&params); err != nil {
		_ = syscall.Close(ringFd)
		return nil, fmt.Errorf("%w: %w", errProbeFailed, err)
	}

	// Slot semaphore — caps in-flight ops. min(maxConcurrency,
	// sqEntries) since the kernel can't queue more than
	// sqEntries SQEs at once anyway. With maxConcurrency==0
	// we still cap at sqEntries to avoid Submit-side OOM.
	cap := int(m.sqEntries)
	if maxConcurrency > 0 && maxConcurrency < cap {
		cap = maxConcurrency
	}
	m.slots = make(chan struct{}, cap)

	m.reaperWg.Add(1)
	go m.reap()

	return m, nil
}

// mmapRings maps the SQ ring, CQ ring, and SQE array based on
// the offsets the kernel returned. Splits the SQ array slice
// + sqes slice using unsafe.Slice over the mmap pointers; the
// slices alias kernel-visible memory, so reads on the head/tail
// pointers see the kernel's most recent stores.
func (m *methodIOUring) mmapRings(params *ioUringParams) error {
	sqRingSize := int(params.SqOff.Array) + int(params.SqEntries)*4
	cqRingSize := int(params.CqOff.Cqes) + int(params.CqEntries)*int(unsafe.Sizeof(ioCqe{}))
	sqesSize := int(params.SqEntries) * int(unsafe.Sizeof(ioSqe{}))

	sqMap, err := syscall.Mmap(m.ringFd, int64(iorOffSQRing), sqRingSize,
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED|syscall.MAP_POPULATE)
	if err != nil {
		return fmt.Errorf("mmap sq ring: %w", err)
	}
	m.sqRingMmap = sqMap

	cqMap, err := syscall.Mmap(m.ringFd, int64(iorOffCQRing), cqRingSize,
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED|syscall.MAP_POPULATE)
	if err != nil {
		_ = syscall.Munmap(sqMap)
		return fmt.Errorf("mmap cq ring: %w", err)
	}
	m.cqRingMmap = cqMap

	sqesMap, err := syscall.Mmap(m.ringFd, int64(iorOffSQEs), sqesSize,
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED|syscall.MAP_POPULATE)
	if err != nil {
		_ = syscall.Munmap(sqMap)
		_ = syscall.Munmap(cqMap)
		return fmt.Errorf("mmap sqes: %w", err)
	}
	m.sqesMmap = sqesMap

	// Resolve SQ pointers. Kernel returns byte offsets; we
	// just point into the mmap.
	sqBase := unsafe.Pointer(&sqMap[0])
	m.sqHead = (*uint32)(unsafe.Add(sqBase, params.SqOff.Head))
	m.sqTail = (*uint32)(unsafe.Add(sqBase, params.SqOff.Tail))
	m.sqMask = *(*uint32)(unsafe.Add(sqBase, params.SqOff.RingMask))
	m.sqEntries = *(*uint32)(unsafe.Add(sqBase, params.SqOff.RingEntries))
	arrayPtr := unsafe.Add(sqBase, params.SqOff.Array)
	m.sqArray = unsafe.Slice((*uint32)(arrayPtr), m.sqEntries)

	// Resolve CQ pointers.
	cqBase := unsafe.Pointer(&cqMap[0])
	m.cqHead = (*uint32)(unsafe.Add(cqBase, params.CqOff.Head))
	m.cqTail = (*uint32)(unsafe.Add(cqBase, params.CqOff.Tail))
	m.cqMask = *(*uint32)(unsafe.Add(cqBase, params.CqOff.RingMask))
	cqesPtr := unsafe.Add(cqBase, params.CqOff.Cqes)
	m.cqes = unsafe.Slice((*ioCqe)(cqesPtr), params.CqEntries)

	// SQEs slice over the third mmap.
	m.sqes = unsafe.Slice((*ioSqe)(unsafe.Pointer(&sqesMap[0])), m.sqEntries)

	// Pre-fill sqArray so SQE slot N is at array index N. The
	// kernel reads array[head..tail] to find which SQEs are
	// active; we always submit to the slot at the same index
	// as the array position so this fixed mapping holds.
	for i := uint32(0); i < m.sqEntries; i++ {
		m.sqArray[i] = i
	}

	return nil
}

func (m *methodIOUring) Name() string { return MethodIOUring }

// Submit hands the op to io_uring. If the op's File doesn't
// expose Fd() (eg. an in-memory test file), Submit performs the
// I/O inline — same outcome the engine surfaces, just without
// driving an io_uring_enter syscall. This keeps the public
// engine contract (any aio.File works) intact.
func (m *methodIOUring) Submit(op *Op) *Handle {
	h := &Handle{op: *op, done: make(chan struct{}), engine: m.engine}
	m.engine.inFlight.Add(1)
	m.engine.registerInFlight(h, op)

	// Acquire a slot or return an engine-closed handle on
	// shutdown — never block forever.
	select {
	case m.slots <- struct{}{}:
	case <-m.closed:
		m.engine.finishHandle(h, Result{Err: errEngineClosed})
		return h
	}

	fdh, ok := op.File.(fdHaver)
	if !ok {
		// Non-fd-backed file (typically a test memFile). Run
		// inline so the test still observes a finished handle.
		<-m.slots
		r := runOp(op)
		m.engine.finishHandle(h, r)
		return h
	}

	// The raw-fd path below hands the kernel a pointer straight
	// into buf, bypassing File.WriteAt entirely — so a File that
	// stamps checksums inside WriteAt (storage.relFile, when
	// checksums are enabled) never gets the chance for writes
	// submitted this way. ChecksumFile is the explicit hook that
	// lets it stamp the checksum here instead, into a copy the
	// kernel actually writes; buf is what we submit and pin alive
	// until the write completes.
	buf := op.Buffer
	if op.Direction == DirWrite {
		if cf, ok := op.File.(ChecksumFile); ok {
			buf = cf.PrepareWrite(op.Buffer, op.Offset)
		}
	}

	userData := m.nextUD.Add(1)
	m.pendingMu.Lock()
	m.pending[userData] = pendingOp{h: h, dir: op.Direction, want: len(op.Buffer), buf: buf}
	m.pendingMu.Unlock()

	if err := m.submitOne(op, buf, fdh.Fd(), userData); err != nil {
		// Submission failure: roll back pending + slot, finish
		// synchronously with the error so the caller's Wait
		// returns rather than blocking.
		m.pendingMu.Lock()
		delete(m.pending, userData)
		m.pendingMu.Unlock()
		<-m.slots
		m.engine.finishHandle(h, Result{Err: err})
	}
	return h
}

// submitOne writes one SQE and bumps the SQ tail, then issues
// io_uring_enter to nudge the kernel. Caller must NOT hold
// pendingMu (we take submitMu instead). buf is what the kernel
// actually reads from / writes into — op.Buffer for reads, or a
// ChecksumFile-stamped copy for writes (see Submit) — so the SQE
// must point at buf, not op.Buffer.
func (m *methodIOUring) submitOne(op *Op, buf []byte, fd uintptr, userData uint64) error {
	m.submitMu.Lock()
	defer m.submitMu.Unlock()

	// Compute slot. tail mod entries is the kernel-side
	// convention; matches the pre-filled sqArray identity map.
	tail := atomic.LoadUint32(m.sqTail)
	idx := tail & m.sqMask

	sqe := &m.sqes[idx]
	*sqe = ioSqe{}
	switch op.Direction {
	case DirRead:
		sqe.Opcode = iorOpRead
	case DirWrite:
		sqe.Opcode = iorOpWrite
	default:
		return fmt.Errorf("aio: unknown direction %d", op.Direction)
	}
	sqe.Fd = int32(fd)
	sqe.Off = uint64(op.Offset)
	if len(buf) > 0 {
		sqe.Addr = uint64(uintptr(unsafe.Pointer(&buf[0])))
	}
	sqe.Len = uint32(len(buf))
	sqe.UserData = userData

	// Release-store the new tail so the kernel sees the SQE.
	atomic.StoreUint32(m.sqTail, tail+1)

	// io_uring_enter(ring_fd, to_submit=1, min_complete=0, flags=0)
	// Submit-only — completions are reaped on the dedicated
	// goroutine via GETEVENTS.
	r, _, errno := syscall.Syscall6(sysIoUringEnter,
		uintptr(m.ringFd), 1, 0, 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("io_uring_enter(submit): %w", errno)
	}
	if int32(r) < 0 {
		return fmt.Errorf("io_uring_enter(submit) returned %d", int32(r))
	}
	return nil
}

// reap is the completion-draining loop. Blocks in
// io_uring_enter(0, 1, GETEVENTS) until at least one CQE is
// available, drains every CQE, and finishes each handle. Exits
// when Close closes the ring fd (io_uring_enter returns EBADF).
func (m *methodIOUring) reap() {
	defer m.reaperWg.Done()
	for {
		// Block until at least one completion. flags=GETEVENTS
		// drives the wait; to_submit=0 because all submissions
		// happen inline in Submit. Returning early is fine —
		// we just loop back and re-block.
		_, _, errno := syscall.Syscall6(sysIoUringEnter,
			uintptr(m.ringFd), 0, 1, uintptr(iorEnterGetEvents), 0, 0)
		if errno != 0 {
			// EINTR: signal interrupted the wait. Loop. EBADF /
			// other: the ring is gone — typically Close. Exit.
			if errno == syscall.EINTR {
				continue
			}
			select {
			case <-m.closed:
				return
			default:
				return
			}
		}

		head := atomic.LoadUint32(m.cqHead)
		tail := atomic.LoadUint32(m.cqTail)
		for head != tail {
			cqe := m.cqes[head&m.cqMask]
			head++
			atomic.StoreUint32(m.cqHead, head)
			// Close-poke sentinel: a NOP submitted by Close to
			// wake this loop. Do not try to look up a pending
			// op for it.
			if cqe.UserData == closeWakeUserData {
				continue
			}
			m.completeOne(cqe)
		}
		select {
		case <-m.closed:
			return
		default:
		}
	}
}

// sqRingFull reports whether the submission queue has no free SQE at `tail`.
// The kernel consumes entries at sqHead, so the queue holds tail-head entries
// (unsigned subtraction, so the 32-bit wrap both counters take is handled).
func (m *methodIOUring) sqRingFull(tail uint32) bool {
	return tail-atomic.LoadUint32(m.sqHead) >= m.sqEntries
}

// pokeWake submits a NOP SQE with the closeWake sentinel so a
// reaper currently blocked in io_uring_enter(GETEVENTS) drains
// it as a CQE and reaches the m.closed select branch. Closing
// the ring fd alone does not unblock the syscall on every
// kernel version, so this explicit wake is the safe approach.
func (m *methodIOUring) pokeWake() error {
	m.submitMu.Lock()
	defer m.submitMu.Unlock()

	tail := atomic.LoadUint32(m.sqTail)
	// The wake NOP is submitted outside the slot semaphore, so unlike
	// submitOne it has no budget guaranteeing a free SQE: with the ring full
	// (slots is sized to sqEntries) tail&sqMask aliases an SQE the kernel has
	// not consumed yet, and overwriting it would turn a real read/write into
	// a NOP whose completion never arrives. A full ring means there ARE ops
	// in flight, and their CQEs wake the reaper by themselves, so skipping
	// the NOP loses nothing (review/260831-2 ST-9).
	if m.sqRingFull(tail) {
		return nil
	}
	idx := tail & m.sqMask
	sqe := &m.sqes[idx]
	*sqe = ioSqe{Opcode: iorOpNop, UserData: closeWakeUserData}
	atomic.StoreUint32(m.sqTail, tail+1)

	_, _, errno := syscall.Syscall6(sysIoUringEnter,
		uintptr(m.ringFd), 1, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// completeOne resolves one CQE: looks up the handle by
// user_data, maps cqe.Res to a Result, releases the slot, and
// finishes the handle. Negative Res is `-errno` per kernel
// convention; positive Res is bytes transferred (short reads
// at EOF report N < want and Err = io.EOF, matching pread's
// io.ReadAt contract).
func (m *methodIOUring) completeOne(cqe ioCqe) {
	m.pendingMu.Lock()
	po, ok := m.pending[cqe.UserData]
	if ok {
		delete(m.pending, cqe.UserData)
	}
	m.pendingMu.Unlock()
	if !ok {
		return
	}
	<-m.slots

	var r Result
	switch {
	case cqe.Res < 0:
		r.Err = syscall.Errno(-cqe.Res)
	default:
		r.N = int(cqe.Res)
		// EOF: a read returning 0 bytes (or fewer than asked
		// for) at end-of-file. WriteAt never short-writes
		// without erroring on Linux, so only DirRead has this
		// shape. Mirrors os.File.ReadAt's contract that the
		// engine relies on for the worker-method path.
		if po.dir == DirRead && r.N < po.want {
			if r.N == 0 {
				r.Err = io.EOF
			} else {
				r.Err = io.EOF
			}
		}
	}
	if r.Err == nil && po.dir == DirRead {
		if cf, ok := po.h.op.File.(ChecksumFile); ok {
			if verr := cf.VerifyRead(po.buf[:r.N], po.h.op.Offset); verr != nil {
				r.Err = verr
			}
		}
	}
	m.engine.finishHandle(po.h, r)
}

// Close shuts the method down. Closes the ring fd to wake the
// reaper, waits for it, then unmaps. Idempotent.
func (m *methodIOUring) Close() error {
	var firstErr error
	m.closeOne.Do(func() {
		close(m.closed)
		// Wake the reaper out of its GETEVENTS wait by
		// submitting a NOP. Closing the ring fd alone does not
		// reliably unblock the syscall (the kernel keeps the
		// in-flight reference open). After the reaper exits we
		// close the fd + munmap below.
		if err := m.pokeWake(); err != nil {
			firstErr = err
		}
		m.reaperWg.Wait()
		if err := syscall.Close(m.ringFd); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := syscall.Munmap(m.sqRingMmap); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := syscall.Munmap(m.cqRingMmap); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := syscall.Munmap(m.sqesMmap); err != nil && firstErr == nil {
			firstErr = err
		}
		// Drain any still-pending ops (Close raced an in-flight
		// op): finish them with engine-closed so callers'
		// Waits return.
		m.pendingMu.Lock()
		left := m.pending
		m.pending = nil
		m.pendingMu.Unlock()
		for _, po := range left {
			<-m.slots
			m.engine.finishHandle(po.h, Result{Err: errEngineClosed})
		}
	})
	return firstErr
}

// nextPow2 rounds n up to the next power of two; used to size
// the io_uring depth (the kernel rounds anyway, but we want
// the slot semaphore to match).
func nextPow2(n uint32) uint32 {
	if n <= 1 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	return n + 1
}

// _ pulls os into the import set conditionally — only used
// from tests today, but keeps the door open for a future probe
// helper that wants to exercise io_uring against a real fd.
var _ = os.Stdin
