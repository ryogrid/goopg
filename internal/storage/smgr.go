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
// v0 opens primary data files with O_RDWR|O_CREATE; the O_DIRECT flag
// is requested when AlignedIO is true (the production path) and
// omitted when false (test-friendly path). Direct I/O alignment is
// the caller's responsibility — buffer pool slots come from the
// page-aligned arena, so callers using the buffer manager always
// satisfy the alignment requirement.
//
// The Manager is goroutine-safe.
type Manager struct {
	cfg ManagerConfig

	mu    sync.Mutex
	files map[RelFileNode]*relFile
}

// ManagerConfig controls how files are opened.
type ManagerConfig struct {
	// DataDir is the root data directory. Files live at
	// <DataDir>/base/<dbOid>/<relOid>[<fork-suffix>].
	DataDir string

	// AlignedIO requests O_DIRECT|O_DSYNC. When true, all read/write
	// buffers must be 4 KiB-aligned and reads/writes must be at
	// 4 KiB-multiple offsets and lengths. The buffer-pool arena
	// guarantees this; callers using their own buffers must too.
	AlignedIO bool
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

// ReadBlock reads block #blk of rel into buf. buf must be exactly
// BlockSize bytes. Returns ErrShortRead if the file ends before the
// block (i.e. blk is beyond the current relation size).
func (m *Manager) ReadBlock(rel RelFileNode, blk BlockNumber, buf []byte) error {
	if len(buf) != BlockSize {
		return fmt.Errorf("ReadBlock: buf is %d bytes, want %d", len(buf), BlockSize)
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
	f, err := m.relFile(rel)
	if err != nil {
		return err
	}
	return f.writeBlock(blk, buf)
}

// Extend appends buf as a new block at the end of rel and returns the
// block number that was assigned. buf must be BlockSize bytes.
func (m *Manager) Extend(rel RelFileNode, buf []byte) (BlockNumber, error) {
	if len(buf) != BlockSize {
		return InvalidBlockNumber, fmt.Errorf("Extend: buf is %d bytes, want %d", len(buf), BlockSize)
	}
	f, err := m.relFile(rel)
	if err != nil {
		return InvalidBlockNumber, err
	}
	return f.extend(buf)
}

// NBlocks returns the current size of rel in blocks.
func (m *Manager) NBlocks(rel RelFileNode) (BlockNumber, error) {
	f, err := m.relFile(rel)
	if err != nil {
		return 0, err
	}
	return f.nBlocks(), nil
}

// Sync issues fdatasync(2) on rel's backing file. Used by the
// checkpointer to make sure dirty buffers we already wrote are
// durable before we advance the redo pointer.
func (m *Manager) Sync(rel RelFileNode) error {
	f, err := m.relFile(rel)
	if err != nil {
		return err
	}
	return f.sync()
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
	if err := setDirectIOIfRequested(osFile, m.cfg.AlignedIO); err != nil {
		_ = osFile.Close()
		return nil, fmt.Errorf("set O_DIRECT %s: %w", path, err)
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
		f:       osFile,
		path:    path,
		nblocks: BlockNumber(st.Size() / BlockSize),
	}
	m.files[rel] = f
	return f, nil
}

func (m *Manager) relPath(rel RelFileNode) string {
	base := filepath.Join(m.cfg.DataDir, "base", fmt.Sprint(rel.DBOid))
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

type relFile struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	nblocks BlockNumber
}

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
	return nil
}

func (r *relFile) writeBlock(blk BlockNumber, buf []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if blk >= r.nblocks {
		return fmt.Errorf("write %s blk %d: out of range (nblocks=%d)", r.path, blk, r.nblocks)
	}
	off := int64(blk) * BlockSize
	n, err := r.f.WriteAt(buf, off)
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
	n, err := r.f.WriteAt(buf, off)
	if err != nil {
		return InvalidBlockNumber, fmt.Errorf("extend %s: %w", r.path, err)
	}
	if n != BlockSize {
		return InvalidBlockNumber, fmt.Errorf("extend %s: short %d of %d", r.path, n, BlockSize)
	}
	r.nblocks++
	return blk, nil
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
