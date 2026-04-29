package wal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
)

const (
	// DefaultSegmentSize matches PostgreSQL's default WAL segment size.
	DefaultSegmentSize int64 = 16 * 1024 * 1024
)

var (
	ErrClosed        = errors.New("wal: writer closed")
	ErrLSNNotWritten = errors.New("wal: requested LSN is beyond written WAL")
	// ErrEmptyPayload guards the EOS sentinel: a zero
	// (len=0, crc=0) header is reserved as "no record here yet"
	// in preallocated segments. See
	// docs/design/0007-0001-wal-segment-preallocation.md.
	ErrEmptyPayload = errors.New("wal: empty payload not allowed")
)

// Config controls writer and reader behavior.
type Config struct {
	WALDir      string
	SegmentSize int64

	// Preallocate, when true, zero-fills new segment files to
	// SegmentSize and fsyncs them at creation time so subsequent
	// commit-path syncs don't pay for inode metadata updates and
	// don't trigger filesystem allocations on the hot path.
	// Mirrors upstream's `wal_init_zero` GUC. Default false keeps
	// the legacy "grow lazily" behaviour for callers that haven't
	// migrated. See docs/design/0007-0001-wal-segment-preallocation.md.
	Preallocate bool

	// AIO is the optional AIO engine seam. When set,
	// state.writeAt submits per-segment writes through it so
	// pg_stat_aio / pg_aios show WAL writes alongside heap
	// writes. nil falls back to direct f.WriteAt — preserving
	// pre-AIO behaviour. The commit-path durability barrier
	// (`flushUpTo` → `dataSync`) remains a synchronous
	// fdatasync regardless of this field. Defined as a narrow
	// interface to avoid the wal → aio import (the engine pulls
	// in a heavier dependency graph that wal can't take on).
	AIO AIOEngine
}

// AIOEngine is the wal-side seam onto an AIO engine. Mirrors
// storage.AIOEngine; the adapter in initdb.Open wraps the same
// underlying *aio.Engine so reads, heap writes, and WAL writes
// all flow through one pool.
type AIOEngine interface {
	Submit(op AIOSubmitOp) AIOHandle
}

// AIOSubmitOp is the wal-side op shape — read/write directional
// against an AIOFile + offset. Buffer is the source for writes
// (and the destination for reads, though wal currently only
// submits writes).
type AIOSubmitOp struct {
	File      AIOFile
	Buffer    []byte
	Offset    int64
	Direction AIODirection
	// Target is a free-form descriptor (typically the WAL
	// segment path) the engine surfaces through
	// pg_aios.target_desc. Empty leaves the column blank.
	Target string
}

// AIOFile mirrors storage.AIOFile: a pread/pwrite-shaped
// handle. The wal writer passes its underlying *os.File through.
type AIOFile interface {
	ReadAt(p []byte, off int64) (int, error)
	WriteAt(p []byte, off int64) (int, error)
}

// AIOHandle mirrors storage.AIOHandle.
type AIOHandle interface {
	Wait() (n int, err error)
}

// AIODirection mirrors aio.Direction without taking the import.
type AIODirection int

const (
	AIODirRead AIODirection = iota
	AIODirWrite
)

func (c *Config) withDefaults() {
	if c.WALDir == "" {
		c.WALDir = filepath.Join(".", "pg_wal")
	}
	if c.SegmentSize <= 0 {
		c.SegmentSize = DefaultSegmentSize
	}
}

type opKind int

const (
	opAppend opKind = iota
	opFlush
	opRecycle
	opClose
)

type op struct {
	kind    opKind
	payload []byte
	lsn     uint64
	resp    chan result
}

type result struct {
	startLSN uint64
	endLSN   uint64
	removed  int
	err      error
}

// Writer serializes all WAL writes through one internal goroutine.
//
// LSN is represented as a 1-based byte position in the WAL stream:
// the first byte written has LSN 1, and the zero value means
// "no WAL record assigned".
type Writer struct {
	ops  chan op
	done chan struct{}

	// writeLSNAtomic mirrors state.writeLSN so external observers
	// (notably the checkpointer's max_wal_size trigger) can read
	// the current write position without serialising through the
	// op channel.
	writeLSNAtomic atomic.Uint64

	// subscribers receive a non-blocking wake-up after every
	// successful Append. Used by RecordIterator (and walsender
	// goroutines) to block until more WAL is available instead of
	// polling WrittenLSN. Subscribers maintain their own buffer
	// channel; Writer drops the wake-up if the channel is full —
	// the iterator will catch up on the next event or by a manual
	// re-poll.
	subMu       sync.Mutex
	subscribers map[chan<- struct{}]struct{}
}

type state struct {
	cfg Config

	writePos   int64
	writeLSN   uint64
	flushedLSN uint64

	// writeLSNMirror, when non-nil, gets the current writeLSN
	// stored after each successful append. The Writer reads it
	// without locking.
	writeLSNMirror *atomic.Uint64

	// onAppend, when non-nil, is invoked after every successful
	// append to wake subscribers (RecordIterators, walsender
	// goroutines). Non-blocking by contract.
	onAppend func()

	files map[uint64]*os.File
	dirty map[uint64]bool

	// aio mirrors Config.AIO so writeAt can submit per-segment
	// writes through the engine. nil → direct f.WriteAt path
	// (legacy / no-engine deployments).
	aio AIOEngine
}

// NewWriter creates a segmented WAL writer rooted at cfg.WALDir.
func NewWriter(cfg Config) (*Writer, error) {
	cfg.withDefaults()
	if err := os.MkdirAll(cfg.WALDir, 0o700); err != nil {
		return nil, fmt.Errorf("wal: mkdir %s: %w", cfg.WALDir, err)
	}

	st, err := loadState(cfg)
	if err != nil {
		return nil, err
	}

	w := &Writer{
		ops:  make(chan op),
		done: make(chan struct{}),
	}
	w.writeLSNAtomic.Store(uint64(st.writePos))
	st.writeLSNMirror = &w.writeLSNAtomic
	st.onAppend = w.notifyAppend
	go st.loop(w.ops, w.done)
	return w, nil
}

// WrittenLSN returns the LSN of the last byte the writer has
// appended (durable or not). Cheap and lock-free; suitable for
// the checkpointer's max_wal_size trigger.
func (w *Writer) WrittenLSN() uint64 {
	return w.writeLSNAtomic.Load()
}

// Subscribe registers ch to receive a non-blocking wake-up after
// every successful Append. Caller is expected to use a buffered
// channel of capacity ≥ 1 — the Writer drops the wake-up if the
// channel is full, since "WAL has advanced" is an idempotent signal
// (subscribers re-poll WrittenLSN, the actual advance is observable
// regardless of how many wake-ups were collapsed).
func (w *Writer) Subscribe(ch chan<- struct{}) {
	w.subMu.Lock()
	if w.subscribers == nil {
		w.subscribers = map[chan<- struct{}]struct{}{}
	}
	w.subscribers[ch] = struct{}{}
	w.subMu.Unlock()
}

// Unsubscribe removes ch from the wake-up set. Safe to call even if
// ch was never subscribed.
func (w *Writer) Unsubscribe(ch chan<- struct{}) {
	w.subMu.Lock()
	delete(w.subscribers, ch)
	w.subMu.Unlock()
}

// notifyAppend wakes every active subscriber. Called by the writer
// goroutine after a successful append. Non-blocking sends so a stuck
// subscriber can't back-pressure the WAL writer.
func (w *Writer) notifyAppend() {
	w.subMu.Lock()
	for ch := range w.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	w.subMu.Unlock()
}

// Append writes one record and returns its [startLSN, endLSN].
//
// Empty payloads are rejected because the encoded zero header
// (len=0, crc=0) is reserved as the EOS sentinel in preallocated
// segments — see
// docs/design/0007-0001-wal-segment-preallocation.md. No
// production caller emits empty records.
func (w *Writer) Append(payload []byte) (uint64, uint64, error) {
	if len(payload) == 0 {
		return 0, 0, ErrEmptyPayload
	}
	resp := make(chan result, 1)
	buf := make([]byte, len(payload))
	copy(buf, payload)
	if err := w.send(op{kind: opAppend, payload: buf, resp: resp}); err != nil {
		return 0, 0, err
	}
	r := <-resp
	return r.startLSN, r.endLSN, r.err
}

// FlushUpTo persists WAL bytes up to lsn with fdatasync semantics.
func (w *Writer) FlushUpTo(lsn uint64) error {
	if lsn == 0 {
		return nil
	}
	resp := make(chan result, 1)
	if err := w.send(op{kind: opFlush, lsn: lsn, resp: resp}); err != nil {
		return err
	}
	return (<-resp).err
}

// RemoveOldSegments unlinks any WAL segment file whose contents end
// strictly before the segment that contains keepLSN. The segment
// containing keepLSN — and every segment after it — is preserved, so
// the caller can pass `min(checkpointLSN, min(slot.RestartLSN))` and
// be sure no record needed for crash recovery or for an attached
// standby is removed.
//
// keepLSN == 0 is a no-op (no records have been written yet).
//
// The op runs on the writer's serialised loop so it cannot race with
// an in-flight Append or Flush. Open file handles for removed segments
// are closed before the unlink and dropped from the writer's cache so
// subsequent writes never re-touch a deleted file.
//
// Returns the number of segment files that were removed.
func (w *Writer) RemoveOldSegments(keepLSN uint64) (int, error) {
	if keepLSN == 0 {
		return 0, nil
	}
	resp := make(chan result, 1)
	if err := w.send(op{kind: opRecycle, lsn: keepLSN, resp: resp}); err != nil {
		return 0, err
	}
	r := <-resp
	return r.removed, r.err
}

// Close flushes dirty segments, closes files, and stops the worker.
func (w *Writer) Close() error {
	resp := make(chan result, 1)
	if err := w.send(op{kind: opClose, resp: resp}); err != nil {
		if errors.Is(err, ErrClosed) {
			return nil
		}
		return err
	}
	r := <-resp
	<-w.done
	return r.err
}

func (w *Writer) send(req op) error {
	select {
	case <-w.done:
		return ErrClosed
	case w.ops <- req:
		return nil
	}
}

func loadState(cfg Config) (*state, error) {
	writePos, err := detectWritePos(cfg.WALDir, cfg.SegmentSize)
	if err != nil {
		return nil, err
	}
	return &state{
		cfg:        cfg,
		writePos:   writePos,
		writeLSN:   uint64(writePos),
		flushedLSN: uint64(writePos),
		files:      make(map[uint64]*os.File),
		dirty:      make(map[uint64]bool),
		aio:        cfg.AIO,
	}, nil
}

func detectWritePos(walDir string, segSize int64) (int64, error) {
	entries, err := os.ReadDir(walDir)
	if err != nil {
		return 0, fmt.Errorf("wal: list %s: %w", walDir, err)
	}

	segSizes := make(map[uint64]int64)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		segNo, ok := parseSegmentName(e.Name())
		if !ok {
			continue
		}
		st, err := e.Info()
		if err != nil {
			return 0, fmt.Errorf("wal: stat %s: %w", e.Name(), err)
		}
		if st.Size() < 0 || st.Size() > segSize {
			return 0, fmt.Errorf("wal: invalid segment size %d for %s", st.Size(), e.Name())
		}
		segSizes[segNo] = st.Size()
	}

	if len(segSizes) == 0 {
		return 0, nil
	}

	segNos := make([]uint64, 0, len(segSizes))
	for seg := range segSizes {
		segNos = append(segNos, seg)
	}
	sort.Slice(segNos, func(i, j int) bool { return segNos[i] < segNos[j] })

	if segNos[0] != 0 {
		return 0, fmt.Errorf("wal: first segment is %s, expected %s", formatSegmentName(segNos[0]), formatSegmentName(0))
	}

	var writePos int64
	for i := 0; i < len(segNos); i++ {
		expected := uint64(i)
		if segNos[i] != expected {
			return 0, fmt.Errorf("wal: gap at segment %s", formatSegmentName(expected))
		}
		sz := segSizes[expected]
		if i < len(segNos)-1 && sz != segSize {
			return 0, fmt.Errorf("wal: non-final segment %s has size %d, expected %d", formatSegmentName(expected), sz, segSize)
		}
		if i < len(segNos)-1 {
			writePos += sz
			continue
		}
		// Last segment: scan for the first EOS sentinel (zero
		// header) to find the actual end of records. Works for
		// both legacy lazy-grown segments (writePos == sz, no
		// trailing zeros) and preallocated full-size segments
		// (writePos < sz, zero-fill tail).
		usedBytes, scanErr := scanLastSegmentEnd(walDir, expected, sz)
		if scanErr != nil {
			return 0, scanErr
		}
		writePos += usedBytes
	}

	return writePos, nil
}

// scanLastSegmentEnd reads the last segment from disk and returns
// the byte offset of the first EOS sentinel (a zero record header
// — `len=0 && crc=0`). Used by detectWritePos to recover the true
// write position from a preallocated full-size last segment, and
// also serves the legacy short-segment case (where the segment
// has no trailing zeros, and the scan returns the full size).
func scanLastSegmentEnd(walDir string, segNo uint64, segSize int64) (int64, error) {
	path := filepath.Join(walDir, formatSegmentName(segNo))
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("wal: read %s: %w", path, err)
	}
	if int64(len(data)) > segSize {
		return 0, fmt.Errorf("wal: segment %s too large: %d > %d", path, len(data), segSize)
	}
	off := 0
	for off < len(data) {
		if isZeroHeader(data[off:]) {
			return int64(off), nil
		}
		_, n, err := decodeRecord(data[off:])
		if err != nil {
			return 0, fmt.Errorf("wal: scan %s at offset %d: %w", path, off, err)
		}
		off += n
	}
	return int64(off), nil
}

func (s *state) loop(ops <-chan op, done chan<- struct{}) {
	defer close(done)
	for req := range ops {
		switch req.kind {
		case opAppend:
			start, end, err := s.append(req.payload)
			req.resp <- result{startLSN: start, endLSN: end, err: err}
			if err == nil && s.onAppend != nil {
				s.onAppend()
			}
		case opFlush:
			req.resp <- result{err: s.flushUpTo(req.lsn)}
		case opRecycle:
			n, err := s.removeOldSegments(req.lsn)
			req.resp <- result{removed: n, err: err}
		case opClose:
			err := s.close()
			req.resp <- result{err: err}
			return
		default:
			req.resp <- result{err: fmt.Errorf("wal: unknown operation %d", req.kind)}
		}
	}
}

func (s *state) append(payload []byte) (uint64, uint64, error) {
	record := encodeRecord(payload)
	start := uint64(s.writePos) + 1
	if err := s.writeAt(s.writePos, record); err != nil {
		return 0, 0, err
	}
	s.writePos += int64(len(record))
	end := uint64(s.writePos)
	s.writeLSN = end
	if s.writeLSNMirror != nil {
		s.writeLSNMirror.Store(end)
	}
	return start, end, nil
}

func (s *state) flushUpTo(lsn uint64) error {
	if lsn == 0 {
		return nil
	}
	if lsn > s.writeLSN {
		return fmt.Errorf("%w: have %d, need %d", ErrLSNNotWritten, s.writeLSN, lsn)
	}
	if lsn <= s.flushedLSN {
		return nil
	}

	targetSeg := segmentForLSN(lsn, s.cfg.SegmentSize)
	dirty := make([]uint64, 0, len(s.dirty))
	for seg := range s.dirty {
		if seg <= targetSeg {
			dirty = append(dirty, seg)
		}
	}
	sort.Slice(dirty, func(i, j int) bool { return dirty[i] < dirty[j] })

	for _, seg := range dirty {
		f, err := s.openSegment(seg)
		if err != nil {
			return err
		}
		// fdatasync on Linux (skips inode metadata that hasn't
		// changed thanks to preallocation), full fsync fallback
		// elsewhere. See
		// docs/design/0007-0002-fdatasync-commit-path.md.
		if err := dataSync(f); err != nil {
			return fmt.Errorf("wal: fdatasync %s: %w", f.Name(), err)
		}
		delete(s.dirty, seg)
	}

	s.flushedLSN = lsn
	return nil
}

// removeOldSegments unlinks every segment whose final byte sits
// strictly before the segment containing keepLSN. Runs on the loop
// goroutine so it serialises with append/flush and won't race with
// openSegment.
//
// Behaviour notes:
//   - The segment that contains keepLSN is preserved (the standby /
//     recovery still needs to read records inside it).
//   - Open file handles for removed segments are closed and dropped
//     from s.files so a subsequent writeAt that somehow targets a
//     stale segment number reopens fresh (it shouldn't — keepLSN
//     guarantees we only delete fully-superseded segments — but the
//     defensive close keeps the invariant explicit).
//   - dirty flags for removed segments are dropped: the bytes were
//     superseded, no fdatasync owes them anymore.
//   - Missing files are silently skipped so a partially-cleaned
//     directory (e.g. a manual rm during testing) doesn't wedge the
//     loop.
func (s *state) removeOldSegments(keepLSN uint64) (int, error) {
	if keepLSN == 0 {
		return 0, nil
	}
	keepSeg := segmentForLSN(keepLSN, s.cfg.SegmentSize)
	if keepSeg == 0 {
		return 0, nil
	}

	entries, err := os.ReadDir(s.cfg.WALDir)
	if err != nil {
		return 0, fmt.Errorf("wal: list %s: %w", s.cfg.WALDir, err)
	}

	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		segNo, ok := parseSegmentName(e.Name())
		if !ok {
			continue
		}
		if segNo >= keepSeg {
			continue
		}
		if f, open := s.files[segNo]; open {
			_ = f.Close()
			delete(s.files, segNo)
		}
		delete(s.dirty, segNo)
		path := filepath.Join(s.cfg.WALDir, e.Name())
		if err := os.Remove(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return removed, fmt.Errorf("wal: remove %s: %w", path, err)
		}
		removed++
	}
	return removed, nil
}

func (s *state) writeAt(pos int64, buf []byte) error {
	for len(buf) > 0 {
		segNo := uint64(pos / s.cfg.SegmentSize)
		segOff := pos % s.cfg.SegmentSize
		space := int(s.cfg.SegmentSize - segOff)
		chunk := len(buf)
		if chunk > space {
			chunk = space
		}

		f, err := s.openSegment(segNo)
		if err != nil {
			return err
		}

		// Per-segment write. With an AIO engine attached the
		// pwrite goes through Engine.Submit + Wait — observable
		// in pg_aios / pg_stat_aio alongside heap writes. With
		// no engine attached, fall back to the direct
		// f.WriteAt path (pre-AIO behaviour). Either way, the
		// wal writer's loop is single-threaded so the Wait is
		// inline; deferring Wait across multiple appends
		// requires restructuring the writer loop and is a
		// future slice.
		var n int
		if s.aio != nil {
			h := s.aio.Submit(AIOSubmitOp{
				File:      f,
				Buffer:    buf[:chunk],
				Offset:    segOff,
				Direction: AIODirWrite,
				Target:    f.Name(),
			})
			n, err = h.Wait()
		} else {
			n, err = f.WriteAt(buf[:chunk], segOff)
		}
		if err != nil {
			return fmt.Errorf("wal: write %s at %d: %w", f.Name(), segOff, err)
		}
		if n != chunk {
			return fmt.Errorf("wal: short write to %s: %d of %d", f.Name(), n, chunk)
		}

		s.dirty[segNo] = true
		pos += int64(chunk)
		buf = buf[chunk:]
	}
	return nil
}

func (s *state) openSegment(segNo uint64) (*os.File, error) {
	if f, ok := s.files[segNo]; ok {
		return f, nil
	}
	path := filepath.Join(s.cfg.WALDir, formatSegmentName(segNo))

	// Detect first-time creation so the preallocator only zero-fills
	// new segments. Existing files (legacy lazy mode, or a re-open
	// after server restart) are left alone — the EOS-sentinel rule
	// in decodeRecord handles their trailing zeros if any.
	wasNew := false
	if s.cfg.Preallocate {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			wasNew = true
		} else if err != nil {
			return nil, fmt.Errorf("wal: stat %s: %w", path, err)
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("wal: open %s: %w", path, err)
	}

	if wasNew {
		if err := preallocateSegment(f, s.cfg.SegmentSize); err != nil {
			_ = f.Close()
			return nil, err
		}
		// Directory fsync makes the new dirent durable. Mirrors
		// upstream's fsync_fname behaviour.
		if dir, derr := os.Open(s.cfg.WALDir); derr == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
	}

	s.files[segNo] = f
	return f, nil
}

// preallocateSegment zero-fills f to size and fsyncs it. Mirrors
// upstream's XLogFileInitInternal zero-write loop. The simple
// loop is portable across filesystems; posix_fallocate is a
// follow-up optimisation.
func preallocateSegment(f *os.File, size int64) error {
	const chunk = 1 << 16 // 64 KiB
	zero := make([]byte, chunk)
	written := int64(0)
	for written < size {
		n := int64(chunk)
		if size-written < n {
			n = size - written
		}
		if _, err := f.WriteAt(zero[:n], written); err != nil {
			return fmt.Errorf("wal: preallocate %s: %w", f.Name(), err)
		}
		written += n
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("wal: preallocate fsync %s: %w", f.Name(), err)
	}
	return nil
}

func (s *state) close() error {
	var firstErr error
	if s.writeLSN > 0 {
		if err := s.flushUpTo(s.writeLSN); err != nil {
			firstErr = err
		}
	}
	for _, f := range s.files {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.files = nil
	s.dirty = nil
	return firstErr
}
