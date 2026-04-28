package wal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
)

const (
	// DefaultSegmentSize matches PostgreSQL's default WAL segment size.
	DefaultSegmentSize int64 = 16 * 1024 * 1024
)

var (
	ErrClosed        = errors.New("wal: writer closed")
	ErrLSNNotWritten = errors.New("wal: requested LSN is beyond written WAL")
)

// Config controls writer and reader behavior.
type Config struct {
	WALDir      string
	SegmentSize int64
}

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

	files map[uint64]*os.File
	dirty map[uint64]bool
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
	go st.loop(w.ops, w.done)
	return w, nil
}

// WrittenLSN returns the LSN of the last byte the writer has
// appended (durable or not). Cheap and lock-free; suitable for
// the checkpointer's max_wal_size trigger.
func (w *Writer) WrittenLSN() uint64 {
	return w.writeLSNAtomic.Load()
}

// Append writes one record and returns its [startLSN, endLSN].
func (w *Writer) Append(payload []byte) (uint64, uint64, error) {
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
		writePos += sz
	}

	return writePos, nil
}

func (s *state) loop(ops <-chan op, done chan<- struct{}) {
	defer close(done)
	for req := range ops {
		switch req.kind {
		case opAppend:
			start, end, err := s.append(req.payload)
			req.resp <- result{startLSN: start, endLSN: end, err: err}
		case opFlush:
			req.resp <- result{err: s.flushUpTo(req.lsn)}
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
		if err := f.Sync(); err != nil {
			return fmt.Errorf("wal: fdatasync %s: %w", f.Name(), err)
		}
		delete(s.dirty, seg)
	}

	s.flushedLSN = lsn
	return nil
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

		n, err := f.WriteAt(buf[:chunk], segOff)
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
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("wal: open %s: %w", path, err)
	}
	s.files[segNo] = f
	return f, nil
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
