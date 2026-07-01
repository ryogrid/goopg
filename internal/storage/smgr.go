package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Manager is the storage manager. It owns one open file per loaded
// (RelFileNode) and routes ReadBlock/WriteBlock/Extend through them.
//
// Data files are opened with plain O_RDWR|O_CREATE (buffered I/O);
// durability comes from fdatasync at checkpoint time, not per-write.
// This mirrors upstream PostgreSQL's relation-file I/O model.
//
// The Manager is goroutine-safe.
// AIOEngine is the optional AIO seam. When attached via
// SetAIO, PrefetchBlock submits reads through it so callers
// (read-stream-driven heap scans, ANALYZE sample reads,
// vacuum's free-space-map walk) can pipeline I/O without
// blocking the consumer. Defined here as a narrow interface
// to avoid the storage→aio import (the engine would then
// pull aio.File semantics into bufpool, which already
// imports storage).
type AIOEngine interface {
	Submit(op AIOSubmitOp) AIOHandle
}

// AIOSubmitOp mirrors aio.Op's submit shape. Direction picks
// pread vs pwrite on the underlying File. Keeping this type
// local keeps storage independent of internal/aio.
type AIOSubmitOp struct {
	File   AIOFile
	Buffer []byte
	Offset int64
	// Direction controls read vs write. The zero value
	// (AIODirRead) preserves the original prefetch-only
	// semantics so callers that don't set it explicitly
	// continue to issue reads.
	Direction AIODirection
	// Target is a free-form descriptor (typically the relfile
	// path) the engine surfaces through pg_aios.target_desc.
	// Empty leaves the column blank.
	Target string
}

// AIODirection mirrors aio.Direction without taking the import.
// AIODirRead is the zero value so callers that don't care about
// writes get the same behaviour as before this field existed.
type AIODirection int

const (
	AIODirRead AIODirection = iota
	AIODirWrite
)

// AIOFile is the engine's view of the underlying *os.File.
// Implementations expose both pread and pwrite so the engine
// can dispatch on Direction; the storage relfile satisfies it.
type AIOFile interface {
	ReadAt(p []byte, off int64) (int, error)
	WriteAt(p []byte, off int64) (int, error)
}

// AIOHandle is the engine's completion handle. Wait blocks
// until the I/O lands.
type AIOHandle interface {
	Wait() (n int, err error)
}

// Manager provides synchronous file I/O for relation data files.
// Each method blocks until the I/O completes.
type Manager struct {
	cfg ManagerConfig

	mu    sync.Mutex
	files map[RelFileNode]*relFile

	aio AIOEngine

	// OnReadWait / OnWriteWait / OnExtendWait / OnSyncWait are optional
	// hooks called before blocking I/O operations.  The server sets them
	// from initdb.Open so the activity package can record DataFileRead /
	// DataFileWrite / DataFileExtend / DataFileSync wait events.
	OnReadWait   func()
	OnWriteWait  func()
	OnExtendWait func()
	OnSyncWait   func()

	// OnReadDone / OnWriteDone / OnExtendDone / OnSyncDone are
	// optional hooks called immediately after the corresponding I/O
	// operation completes (including on error). Paired with the
	// `*Wait` hooks so observability code can balance every
	// WaitEventStart with a WaitEventEnd. (M0058-0006.)
	OnReadDone   func()
	OnWriteDone  func()
	OnExtendDone func()
	OnSyncDone   func()

	// OnBlockWritten invalidates any external page cache that shadows
	// data-file writes done directly through smgr (for example WAL replay).
	OnBlockWritten func(rel RelFileNode, blk BlockNumber)
}

// ManagerConfig controls how files are opened.
type ManagerConfig struct {
	// DataDir is the root data directory. Files live at
	// <DataDir>/base/<dbOid>/<relOid>[<fork-suffix>].
	DataDir string

	// ChecksumsEnabled turns on PG-compatible data-page checksums for
	// every block this Manager reads and writes. When false (the
	// default) the read/write path is byte-identical to a checksum-less
	// cluster — no copy, no verification, no overhead. The runtime sets
	// this from pg_control's data_checksum_version (1 ⇒ enabled), which
	// initdb writes from the --data-checksums option. See
	// docs/design/0102-0019-initdb-data-checksums.md.
	ChecksumsEnabled bool

	// IgnoreChecksumFailure, when true, downgrades a checksum mismatch on
	// read from a hard error to a non-fatal event: the page is returned
	// as read and OnChecksumFailure (if set) is invoked. Mirrors the
	// ignore_checksum_failure GUC. Has no effect unless ChecksumsEnabled.
	IgnoreChecksumFailure bool

	// OnChecksumFailure, if set, is invoked when a block fails checksum
	// verification on read (whether or not the failure is fatal). The
	// server wires this to its log / pg_stat checksum-failure counters.
	OnChecksumFailure func(rel RelFileNode, blk BlockNumber)
}

// NewManager constructs an empty Manager. Files open lazily on first
// access.
func NewManager(cfg ManagerConfig) *Manager {
	return &Manager{
		cfg:   cfg,
		files: map[RelFileNode]*relFile{},
	}
}

// Close flushes and closes every open file.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	for rel, f := range m.files {
		if err := f.close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close %v: %w", rel, err)
		}
	}
	m.files = nil
	return firstErr
}

// DataDir returns the root data directory this Manager was configured with.
func (m *Manager) DataDir() string { return m.cfg.DataDir }

// SetAIO attaches an AIO engine to the manager. PrefetchBlock
// submits through it. nil clears any previously-set engine
// (PrefetchBlock then falls back to a synchronous read).
// Idempotent; safe to call before or after the first ReadBlock.
func (m *Manager) SetAIO(eng AIOEngine) {
	m.mu.Lock()
	m.aio = eng
	m.mu.Unlock()
}

// PrefetchBlock submits a read of block #blk into buf and
// returns an AIOHandle the caller Waits on. With an engine
// attached the read happens off the calling goroutine; without
// one, the read runs synchronously and the returned handle's
// Wait is already complete. buf must be exactly BlockSize
// bytes.
//
// PrefetchBlock does NOT track buf-aliasing across the lifetime
// of the handle: the caller MUST keep buf alive until Wait
// returns. Mirrors the aio.Op contract.
//
// Out-of-range blocks return an already-complete handle whose
// Wait surfaces ErrShortRead, matching ReadBlock's behaviour.
func (m *Manager) PrefetchBlock(rel RelFileNode, blk BlockNumber, buf []byte) (AIOHandle, error) {
	if len(buf) != BlockSize {
		return nil, fmt.Errorf("PrefetchBlock: buf is %d bytes, want %d", len(buf), BlockSize)
	}
	f, err := m.relFile(rel)
	if err != nil {
		return nil, err
	}
	if blk >= f.nblocks {
		return preCompletedHandle{err: ErrShortRead}, nil
	}
	off := int64(blk) * BlockSize

	m.mu.Lock()
	eng := m.aio
	m.mu.Unlock()
	if eng == nil {
		// Synchronous fallback: run the read inline and hand
		// back a pre-completed handle. Keeps callers'
		// Submit/Wait code path identical regardless of
		// whether the engine is attached.
		err := f.readBlock(blk, buf)
		var n int
		if err == nil {
			n = BlockSize
		}
		return preCompletedHandle{n: n, err: err}, nil
	}
	return eng.Submit(AIOSubmitOp{
		File:   f,
		Buffer: buf,
		Offset: off,
		Target: f.path,
	}), nil
}

// preCompletedHandle is the AIOHandle a synchronous-fallback
// PrefetchBlock returns. Wait never blocks.
type preCompletedHandle struct {
	n   int
	err error
}

func (h preCompletedHandle) Wait() (int, error) { return h.n, h.err }

// WriteBlockAIO submits a write of buf as block #blk of rel
// through the AIO engine when one is attached, otherwise falls
// back to a synchronous WriteBlock and returns a pre-completed
// handle. buf must be exactly BlockSize bytes; the block must
// already exist (use Extend to grow the relation). The caller
// MUST keep buf alive until Wait returns.
//
// This is the primary write seam future checkpointer / WAL
// writer caller integrations layer on. Engine-attached
// deployments pipeline writes via the AIO worker pool;
// engine-less ones see the same blocking-pwrite behaviour
// they had before.
func (m *Manager) WriteBlockAIO(rel RelFileNode, blk BlockNumber, buf []byte) (AIOHandle, error) {
	if len(buf) != BlockSize {
		return nil, fmt.Errorf("WriteBlockAIO: buf is %d bytes, want %d", len(buf), BlockSize)
	}
	f, err := m.relFile(rel)
	if err != nil {
		return nil, err
	}
	if blk >= f.nblocks {
		return preCompletedHandle{
			err: fmt.Errorf("WriteBlockAIO %s blk %d: out of range (nblocks=%d)", f.path, blk, f.nblocks),
		}, nil
	}
	off := int64(blk) * BlockSize

	m.mu.Lock()
	eng := m.aio
	m.mu.Unlock()
	if eng == nil {
		err := f.writeBlock(blk, buf)
		var n int
		if err == nil {
			n = BlockSize
		}
		return preCompletedHandle{n: n, err: err}, nil
	}
	return eng.Submit(AIOSubmitOp{
		File:      f,
		Buffer:    buf,
		Offset:    off,
		Direction: AIODirWrite,
		Target:    f.path,
	}), nil
}

// ReadBlock reads block #blk of rel into buf. buf must be exactly
// BlockSize bytes. Returns ErrShortRead if the file ends before the
// block (i.e. blk is beyond the current relation size).
func (m *Manager) ReadBlock(rel RelFileNode, blk BlockNumber, buf []byte) error {
	if len(buf) != BlockSize {
		return fmt.Errorf("ReadBlock: buf is %d bytes, want %d", len(buf), BlockSize)
	}
	if m.OnReadWait != nil {
		m.OnReadWait()
	}
	if m.OnReadDone != nil {
		defer m.OnReadDone()
	}
	f, err := m.relFile(rel)
	if err != nil {
		return err
	}
	return f.readBlock(blk, buf)
}

// WriteBlock writes buf as block #blk of rel. buf must be BlockSize
// bytes. The block must already exist (i.e. blk < NBlocks); use
// Extend to grow the relation.
func (m *Manager) WriteBlock(rel RelFileNode, blk BlockNumber, buf []byte) error {
	if len(buf) != BlockSize {
		return fmt.Errorf("WriteBlock: buf is %d bytes, want %d", len(buf), BlockSize)
	}
	if m.OnWriteWait != nil {
		m.OnWriteWait()
	}
	if m.OnWriteDone != nil {
		defer m.OnWriteDone()
	}
	f, err := m.relFile(rel)
	if err != nil {
		return err
	}
	if err := f.writeBlock(blk, buf); err != nil {
		return err
	}
	if m.OnBlockWritten != nil {
		m.OnBlockWritten(rel, blk)
	}
	return nil
}

// Extend appends buf as a new block at the end of rel and returns the
// block number that was assigned. buf must be BlockSize bytes.
func (m *Manager) Extend(rel RelFileNode, buf []byte) (BlockNumber, error) {
	if len(buf) != BlockSize {
		return InvalidBlockNumber, fmt.Errorf("Extend: buf is %d bytes, want %d", len(buf), BlockSize)
	}
	if m.OnExtendWait != nil {
		m.OnExtendWait()
	}
	if m.OnExtendDone != nil {
		defer m.OnExtendDone()
	}
	f, err := m.relFile(rel)
	if err != nil {
		return InvalidBlockNumber, err
	}
	return f.extend(buf)
}

// ExtendBatch appends n contiguous new blocks (each filled with buf) and
// returns the block number of the first new block; subsequent blocks
// occupy firstBlk+1 .. firstBlk+n-1. One smgr-level lock and one WriteAt
// covers the whole batch.
//
// FSM bookkeeping for the new blocks is the caller's responsibility — the
// heap-insert path documented in
// `docs/design/perf-optimize/07-wal-fsm-insert.md` §3 registers blocks
// firstBlk+1 .. firstBlk+n-1 in FSM and uses firstBlk for its own insert.
func (m *Manager) ExtendBatch(rel RelFileNode, buf []byte, n int) (BlockNumber, error) {
	if n <= 0 {
		return InvalidBlockNumber, fmt.Errorf("ExtendBatch: n=%d must be > 0", n)
	}
	if len(buf) != BlockSize {
		return InvalidBlockNumber, fmt.Errorf("ExtendBatch: buf is %d bytes, want %d", len(buf), BlockSize)
	}
	if m.OnExtendWait != nil {
		m.OnExtendWait()
	}
	if m.OnExtendDone != nil {
		defer m.OnExtendDone()
	}
	f, err := m.relFile(rel)
	if err != nil {
		return InvalidBlockNumber, err
	}
	return f.extendBatch(buf, n)
}

// NBlocks returns the current size of rel in blocks.
func (m *Manager) NBlocks(rel RelFileNode) (BlockNumber, error) {
	f, err := m.relFile(rel)
	if err != nil {
		return 0, err
	}
	return f.nBlocks(), nil
}

// Exists reports whether rel's backing fork file is present on disk, mirroring
// PostgreSQL's smgrexists(MAIN_FORKNUM). It must NOT consult the open-file
// cache or go through relFile: relFile opens with O_CREATE and would silently
// recreate a fork that was removed out from under us (e.g. the pg_amcheck
// missing-relation-fork corruption scenario), turning a removed file into an
// empty one. A pure stat of the on-disk path is the faithful test — every open
// relation always has an on-disk file (relFile creates it eagerly), so a
// stat-only check never reports a live relation as absent.
func (m *Manager) Exists(rel RelFileNode) bool {
	_, err := os.Stat(m.relPath(rel))
	return err == nil
}

// RelPath returns rel's fork path relative to the data directory, using forward
// slashes (e.g. "base/5/16407"). It mirrors PostgreSQL's relpath() so callers
// can build the upstream-verbatim `could not open file "%s"` message when a
// fork is found missing (the pg_amcheck heap file-removal corruption scenario).
func (m *Manager) RelPath(rel RelFileNode) string {
	base := sharedOrPerDBRelDir(rel.DBOid)
	switch rel.Fork {
	case MainFork:
		return base + "/" + fmt.Sprint(rel.RelOid)
	case FSMFork:
		return base + "/" + fmt.Sprintf("%d_fsm", rel.RelOid)
	case VisibilityMapFork:
		return base + "/" + fmt.Sprintf("%d_vm", rel.RelOid)
	case InitFork:
		return base + "/" + fmt.Sprintf("%d_init", rel.RelOid)
	}
	return base + "/" + fmt.Sprintf("%d_fork%d", rel.RelOid, rel.Fork)
}

// Sync issues fdatasync(2) on rel's backing file. Used by the
// checkpointer to make sure dirty buffers we already wrote are
// durable before we advance the redo pointer.
func (m *Manager) Sync(rel RelFileNode) error {
	if m.OnSyncWait != nil {
		m.OnSyncWait()
	}
	if m.OnSyncDone != nil {
		defer m.OnSyncDone()
	}
	f, err := m.relFile(rel)
	if err != nil {
		return err
	}
	return f.sync()
}

// SyncAll issues fdatasync(2) on every open data file. Used by the
// checkpointer after FlushAllPaced has pwrite'd dirty buffers, so a
// power-loss between checkpoint completion and the next OS-level
// flush leaves the data files durable. Without this, the pre-fix
// behaviour was: checkpoint wrote dirty pages via buffered pwrite,
// the bytes sat in the OS page cache, and a sudden host loss could
// rewind the heap to its last-fsync'd state — but the WAL had
// already advanced its redo pointer past those records. M0089-0001.
func (m *Manager) SyncAll() error {
	if m.OnSyncWait != nil {
		m.OnSyncWait()
	}
	if m.OnSyncDone != nil {
		defer m.OnSyncDone()
	}
	m.mu.Lock()
	files := make([]*relFile, 0, len(m.files))
	for _, f := range m.files {
		files = append(files, f)
	}
	m.mu.Unlock()
	var firstErr error
	for _, f := range files {
		if err := f.sync(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("sync %s: %w", f.path, err)
		}
	}
	return firstErr
}

// DropRelation closes the open file (if any) and removes it from
// disk. The executor's DROP TABLE path calls this after the catalog
// entry is gone; the buffer pool's InvalidateRel must run beforehand
// to flush any pinned slots.
func (m *Manager) DropRelation(rel RelFileNode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.files[rel]; ok {
		_ = f.f.Close()
		delete(m.files, rel)
	}
	path := m.relPath(rel)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// TruncateRelation truncates rel's backing file to zero blocks. The
// caller is responsible for invalidating buffer-pool slots that hold
// pages of rel before calling this.
func (m *Manager) TruncateRelation(rel RelFileNode) error {
	f, err := m.relFile(rel)
	if err != nil {
		return err
	}
	return f.truncateToZero()
}

func (m *Manager) relFile(rel RelFileNode) (*relFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.files[rel]; ok {
		return f, nil
	}
	path := m.relPath(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	flags := os.O_RDWR | os.O_CREATE
	osFile, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	st, err := osFile.Stat()
	if err != nil {
		_ = osFile.Close()
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if st.Size()%BlockSize != 0 {
		_ = osFile.Close()
		return nil, fmt.Errorf("file %s size %d not multiple of %d", path, st.Size(), BlockSize)
	}
	f := &relFile{
		f:              osFile,
		path:           path,
		nblocks:        BlockNumber(st.Size() / BlockSize),
		rel:            rel,
		checksums:      m.cfg.ChecksumsEnabled,
		ignoreChecksum: m.cfg.IgnoreChecksumFailure,
		onChecksumFail: m.cfg.OnChecksumFailure,
	}
	m.files[rel] = f
	return f, nil
}

// sharedOrPerDBRelDir returns the "base/<dbOid>" (or, for the shared-catalog
// sentinel dbOid==0, "global") directory a RelFileNode's DBOid resolves to.
// DBOid 0 is never a live database OID (catalog.DefaultDBOid == 1; no
// pg_database row is ever assigned OID 0), so it is free to mean "this is a
// cluster-wide shared catalog" (pg_database, pg_authid, ...) mirroring
// PostgreSQL's relpath() global/ tablespace path. M0117-0008 Part B.
func sharedOrPerDBRelDir(dbOid uint32) string {
	if dbOid == 0 {
		return "global"
	}
	return "base/" + fmt.Sprint(dbOid)
}

func (m *Manager) relPath(rel RelFileNode) string {
	base := filepath.Join(m.cfg.DataDir, sharedOrPerDBRelDir(rel.DBOid))
	switch rel.Fork {
	case MainFork:
		return filepath.Join(base, fmt.Sprint(rel.RelOid))
	case FSMFork:
		return filepath.Join(base, fmt.Sprintf("%d_fsm", rel.RelOid))
	case VisibilityMapFork:
		return filepath.Join(base, fmt.Sprintf("%d_vm", rel.RelOid))
	case InitFork:
		return filepath.Join(base, fmt.Sprintf("%d_init", rel.RelOid))
	}
	return filepath.Join(base, fmt.Sprintf("%d_fork%d", rel.RelOid, rel.Fork))
}

// ErrShortRead indicates a ReadBlock landed past EOF.
var ErrShortRead = errors.New("short read at block")

// ChecksumError is returned by a read when data-page checksums are enabled
// and a block's stored pd_checksum does not match its recomputed value.
// Mirrors upstream's "page verification failed, calculated checksum %u but
// expected %u" ERRCODE_DATA_CORRUPTED report.
type ChecksumError struct {
	Path  string
	Block BlockNumber
}

func (e *ChecksumError) Error() string {
	return fmt.Sprintf("invalid page in block %d of relation %s: data-page checksum verification failed", e.Block, e.Path)
}

// verifyOnRead checks buf's checksum after a read of block blk. It is a
// no-op unless checksums are enabled. On mismatch it always notifies
// onChecksumFail (if set), then returns a ChecksumError unless
// ignoreChecksum is set (mirrors the ignore_checksum_failure GUC).
func (r *relFile) verifyOnRead(blk BlockNumber, buf []byte) error {
	if !r.checksums {
		return nil
	}
	if VerifyPage(buf, blk) {
		return nil
	}
	if r.onChecksumFail != nil {
		r.onChecksumFail(r.rel, blk)
	}
	if r.ignoreChecksum {
		return nil
	}
	return &ChecksumError{Path: r.path, Block: blk}
}

// checksummedForWrite returns the byte slice to write for block blk: when
// checksums are enabled it is a fresh copy of buf with pd_checksum set (the
// caller's buffer is never mutated), otherwise buf itself (zero-copy, the
// checksum-less fast path).
func (r *relFile) checksummedForWrite(blk BlockNumber, buf []byte) []byte {
	if !r.checksums {
		return buf
	}
	return PageSetChecksumCopy(buf, blk)
}

type relFile struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	nblocks BlockNumber

	// rel identifies this file for OnChecksumFailure callbacks.
	rel RelFileNode
	// checksums mirrors ManagerConfig.ChecksumsEnabled: when set, every
	// block written through this file carries a fresh pd_checksum and
	// every block read is verified. When clear the I/O path is
	// byte-identical to a checksum-less cluster.
	checksums      bool
	ignoreChecksum bool
	onChecksumFail func(rel RelFileNode, blk BlockNumber)
}

// ReadAt is the engine-facing offset-shaped read. The relFile
// itself satisfies storage.AIOFile this way so the engine can
// submit reads against it without smgr exposing *os.File.
// Mirrors os.File.ReadAt's contract: short reads at end-of-
// file surface io.EOF.
func (r *relFile) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, err := r.f.ReadAt(p, off)
	if err == nil && r.checksums && n == BlockSize && off%BlockSize == 0 {
		if verr := r.verifyOnRead(BlockNumber(off/BlockSize), p[:n]); verr != nil {
			return n, verr
		}
	}
	return n, err
}

// WriteAt is the engine-facing offset-shaped write. Mirrors
// os.File.WriteAt's contract; pairs with ReadAt so a single
// relFile satisfies storage.AIOFile end-to-end. Bounds /
// nblocks tracking still happens in writeBlock — callers
// reaching directly through the engine path (Manager.WriteBlockAIO)
// validate against nblocks before submitting.
func (r *relFile) WriteAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.checksums && len(p) == BlockSize && off%BlockSize == 0 {
		out := r.checksummedForWrite(BlockNumber(off/BlockSize), p)
		n, err := r.f.WriteAt(out, off)
		// Report the byte count against the caller's buffer length.
		if n > len(p) {
			n = len(p)
		}
		return n, err
	}
	return r.f.WriteAt(p, off)
}

// Fd exposes the underlying *os.File's kernel descriptor so an
// AIO method that talks to the kernel directly (notably
// io_uring) can submit ops by fd. The mutex above is the
// userspace serialiser that keeps Extend/Close coherent — the
// fd itself is stable across pread/pwrite. Returns the same
// uintptr for the lifetime of the relFile.
func (r *relFile) Fd() uintptr { return r.f.Fd() }

func (r *relFile) readBlock(blk BlockNumber, buf []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if blk >= r.nblocks {
		return ErrShortRead
	}
	off := int64(blk) * BlockSize
	n, err := r.f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read %s blk %d: %w", r.path, blk, err)
	}
	if n != BlockSize {
		return fmt.Errorf("read %s blk %d: short %d of %d", r.path, blk, n, BlockSize)
	}
	return r.verifyOnRead(blk, buf)
}

func (r *relFile) writeBlock(blk BlockNumber, buf []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if blk >= r.nblocks {
		return fmt.Errorf("write %s blk %d: out of range (nblocks=%d)", r.path, blk, r.nblocks)
	}
	off := int64(blk) * BlockSize
	n, err := r.f.WriteAt(r.checksummedForWrite(blk, buf), off)
	if err != nil {
		return fmt.Errorf("write %s blk %d: %w", r.path, blk, err)
	}
	if n != BlockSize {
		return fmt.Errorf("write %s blk %d: short %d of %d", r.path, blk, n, BlockSize)
	}
	return nil
}

func (r *relFile) extend(buf []byte) (BlockNumber, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	blk := r.nblocks
	off := int64(blk) * BlockSize
	n, err := r.f.WriteAt(r.checksummedForWrite(blk, buf), off)
	if err != nil {
		return InvalidBlockNumber, fmt.Errorf("extend %s: %w", r.path, err)
	}
	if n != BlockSize {
		return InvalidBlockNumber, fmt.Errorf("extend %s: short %d of %d", r.path, n, BlockSize)
	}
	r.nblocks++
	return blk, nil
}

// extendBatch appends n copies of buf as contiguous new blocks at the end
// of the relation in a single locked WriteAt. Returns the block number of
// the first new block; the remaining blocks occupy firstBlk+1 .. firstBlk+n-1.
// buf must be exactly BlockSize bytes (the same initialized page content is
// written for every block — the heap-insert caller relies on FSM to allocate
// tuples into these pages on subsequent inserts).
func (r *relFile) extendBatch(buf []byte, n int) (BlockNumber, error) {
	if n <= 0 {
		return InvalidBlockNumber, fmt.Errorf("extendBatch: n=%d must be > 0", n)
	}
	if len(buf) != BlockSize {
		return InvalidBlockNumber, fmt.Errorf("extendBatch: buf is %d bytes, want %d", len(buf), BlockSize)
	}
	// Build the batched write buffer once: n copies of the empty page.
	// Typical n=8 → 64 KiB; large bulk-insert callers may pass more.
	big := make([]byte, n*BlockSize)
	for i := 0; i < n; i++ {
		copy(big[i*BlockSize:(i+1)*BlockSize], buf)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	first := r.nblocks
	off := int64(first) * BlockSize
	if r.checksums {
		// big is freshly allocated and not shared, so set each block's
		// pd_checksum in place. Block numbers are only known now that the
		// starting block (first) is assigned under the lock.
		for i := 0; i < n; i++ {
			blkBuf := big[i*BlockSize : (i+1)*BlockSize]
			if !IsNew(blkBuf) {
				MustHeader(blkBuf).SetChecksum(PageChecksum(blkBuf, first+BlockNumber(i)))
			}
		}
	}
	w, err := r.f.WriteAt(big, off)
	if err != nil {
		return InvalidBlockNumber, fmt.Errorf("extendBatch %s: %w", r.path, err)
	}
	if w != len(big) {
		return InvalidBlockNumber, fmt.Errorf("extendBatch %s: short %d of %d", r.path, w, len(big))
	}
	r.nblocks += BlockNumber(n)
	return first, nil
}

func (r *relFile) nBlocks() BlockNumber {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nblocks
}

func (r *relFile) sync() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Sync()
}

func (r *relFile) truncateToZero() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.f.Truncate(0); err != nil {
		return fmt.Errorf("truncate %s: %w", r.path, err)
	}
	r.nblocks = 0
	return nil
}

func (r *relFile) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
