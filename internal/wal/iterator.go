// Streaming WAL record iterator for replication and tail-following.
//
// Unlike `ReadAll` (used by crash recovery), `RecordIterator` yields
// records one at a time and blocks when caught up to the writer's
// current `WrittenLSN`. The walsender goroutine uses this to feed
// each record into a `WAL-data` CopyData frame as soon as it lands;
// future tooling (`pg_recvwal`-equivalent) can use the same API.
//
// See docs/design/0005-0001-streaming-replication-architecture.md.
package wal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
)

// RecordIterator walks the WAL stream forward from a starting LSN.
// Record framing depends on writer mode:
//   - legacy mode: 8-byte len+CRC frame + payload
//   - page-header mode: 24-byte XLogRecord + wrapped main-data chunk
//
// When the iterator catches up to the writer's WrittenLSN
// it blocks on `Next` until either:
//   - the writer advances (via the subscription wake-up channel), or
//   - the supplied context is cancelled, or
//   - the writer is closed (Append returns ErrClosed).
//
// Each yielded record is a fresh copy — the underlying segment
// buffer is reused, so callers must not retain the slice past the
// next `Next` call without copying.
type RecordIterator struct {
	writer  *Writer
	walDir  string
	segSize int64
	wake    chan struct{}
	pos     int64 // 0-based byte offset corresponding to the LSN cursor
	// closed is an atomic flag so Close (e.g. called by walsender
	// teardown) and Next (running in a separate goroutine) don't race.
	// M0098-0002 exposed this via LockOSThread scheduling change.
	closed atomic.Bool
	// pageHeaders mirrors Writer.PageHeadersEnabled() at iterator
	// construction time. When true, the cursor walks page-header
	// bytes transparently: at every page boundary the iterator
	// skips a 24- or 40-byte XLogPageHeader, and record-byte reads
	// can span page boundaries (the next page's header sits between
	// fragments). See M0014-0001 step 2.
	pageHeaders bool
}

// NewRecordIterator constructs a streaming iterator anchored at
// startLSN. startLSN must be a record-boundary LSN (the
// `start_lsn` of a record previously emitted by the writer); 0 is
// accepted as "from the beginning of segment 0". Callers obtain
// boundary LSNs from `Append` return values, the
// `WALDataMessage.EndLSN` field, or replication slot state.
func NewRecordIterator(w *Writer, walDir string, segSize int64, startLSN uint64) (*RecordIterator, error) {
	if w == nil {
		return nil, errors.New("wal: NewRecordIterator: nil writer")
	}
	if segSize <= 0 {
		segSize = DefaultSegmentSize
	}
	// startLSN==0 maps to byte offset 0; startLSN==N maps to offset N-1.
	var pos int64
	if startLSN > 0 {
		pos = int64(startLSN) - 1
	}
	wake := make(chan struct{}, 1)
	w.Subscribe(wake)
	return &RecordIterator{
		writer:      w,
		walDir:      walDir,
		segSize:     segSize,
		wake:        wake,
		pos:         pos,
		pageHeaders: w.PageHeadersEnabled(),
	}, nil
}

// Close releases the iterator's subscription. Safe to call multiple
// times. After Close, Next returns io.EOF.
func (it *RecordIterator) Close() error {
	if !it.closed.CompareAndSwap(false, true) {
		return nil // already closed
	}
	it.writer.Unsubscribe(it.wake)
	return nil
}

// Next returns the next record. Blocks (until ctx is cancelled or
// the writer closes) when the cursor has caught up to WrittenLSN.
// io.EOF is returned only after Close. Other errors are wrapped
// `ErrCorruptRecord` from the segment decoder.
func (it *RecordIterator) Next(ctx context.Context) (Record, error) {
	if it.closed.Load() {
		return Record{}, io.EOF
	}
	for {
		written := it.writer.WrittenLSN()
		// In page-headers mode, advance the cursor past any
		// page header that sits at the current boundary. The
		// header bytes are part of the WAL stream byte count
		// (so writePos covers them) but carry no record data —
		// transparent skip on the read side mirrors the
		// transparent insertion on the writer side.
		if it.pageHeaders {
			for it.pos < int64(written) && it.pos%XLOGBlockSize == 0 {
				hsize := int64(pageHeaderSizeAt(it.pos, it.segSize))
				if it.pos+hsize > int64(written) {
					break
				}
				it.pos += hsize
			}
		}
		// `written` is the LSN of the last byte appended; the next
		// record's start byte is at offset `written` (0-based) =
		// LSN `written + 1`. So if our cursor (0-based pos) equals
		// or exceeds `written`, we're at the tail.
		if int64(written) > it.pos {
			rec, n, err := it.readOneAt(it.pos)
			if err != nil {
				return Record{}, err
			}
			it.pos += int64(n)
			return rec, nil
		}
		// At tail: drain any stale wake-up so the next signal is
		// guaranteed to be post-cursor, then block.
		select {
		case <-it.wake:
			continue
		case <-ctx.Done():
			return Record{}, ctx.Err()
		case <-it.writer.done:
			return Record{}, ErrClosed
		}
	}
}

// readOneAt opens the segment containing pos, reads one record
// header (legacy 8-byte frame or PG-compatible 24-byte XLogRecord
// header), then reads and decodes the full body and returns the
// Record + its on-wire byte count. Header / payload that span
// segments are pasted together by reading the trailing bytes of one
// segment and the leading bytes of the next.
//
// In page-headers mode, reads transparently span page boundaries:
// the iterator extracts only record bytes (skipping XLogPageHeader
// bytes) and the returned advance count includes those header
// bytes so the cursor lands on the start of the next page's
// content. `pos` must point at a record byte (not a page header)
// — Next() advances past leading headers before calling here.
func (it *RecordIterator) readOneAt(pos int64) (Record, int, error) {
	if it.pageHeaders {
		headerBytes, _, err := it.readRecordBytesAt(pos, xlogRecordHeaderSize)
		if err != nil {
			return Record{}, 0, err
		}
		h, err := DecodeXLogRecordHeader(headerBytes)
		if err != nil {
			return Record{}, 0, err
		}
		total := int(h.TotLen)
		if total < xlogRecordHeaderSize {
			return Record{}, 0, fmt.Errorf("%w: bad xlog total length %d", ErrCorruptRecord, total)
		}
		// Read MAXALIGN(total) physical bytes to also pick up the
		// trailing zero pad — the upstream record alignment.
		paddedTotal := maxAlignXLog(total)
		body, advance, err := it.readRecordBytesAt(pos, paddedTotal)
		if err != nil {
			return Record{}, 0, err
		}
		payload, n, err := decodeRecordXLog(body)
		if err != nil {
			return Record{}, 0, err
		}
		if n != len(body) {
			return Record{}, 0, fmt.Errorf("wal: iterator xlog size mismatch: %d vs %d", n, len(body))
		}
		startLSN := uint64(pos) + 1
		endLSN := uint64(pos) + uint64(advance)
		return Record{StartLSN: startLSN, EndLSN: endLSN, Payload: payload}, advance, nil
	}

	header, _, err := it.readRecordBytesAt(pos, recordHeaderSize)
	if err != nil {
		return Record{}, 0, err
	}
	payloadLen := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16 | uint32(header[3])<<24)
	if payloadLen < 0 {
		return Record{}, 0, fmt.Errorf("%w: negative payload length %d", ErrCorruptRecord, payloadLen)
	}
	total := recordHeaderSize + payloadLen
	body, advance, err := it.readRecordBytesAt(pos, total)
	if err != nil {
		return Record{}, 0, err
	}
	payload, n, err := decodeRecord(body)
	if err != nil {
		return Record{}, 0, err
	}
	if n != total {
		return Record{}, 0, fmt.Errorf("wal: iterator size mismatch: %d vs %d", n, total)
	}
	startLSN := uint64(pos) + 1
	endLSN := uint64(pos) + uint64(advance)
	return Record{StartLSN: startLSN, EndLSN: endLSN, Payload: payload}, advance, nil
}

// readRecordBytesAt reads `n` logical record bytes starting at
// stream byte offset `pos`. In legacy mode this is identical to
// readBytesAt; in page-headers mode the read transparently steps
// over any 24- or 40-byte XLogPageHeader that sits between record
// bytes, returning the number of physical stream bytes consumed
// (record bytes + skipped page-header bytes) as `advance`.
func (it *RecordIterator) readRecordBytesAt(pos int64, n int) ([]byte, int, error) {
	if !it.pageHeaders {
		out, err := it.readBytesAt(pos, n)
		if err != nil {
			return nil, 0, err
		}
		return out, n, nil
	}
	out := make([]byte, 0, n)
	advance := 0
	for len(out) < n {
		cur := pos + int64(advance)
		if cur%XLOGBlockSize == 0 {
			hsize := pageHeaderSizeAt(cur, it.segSize)
			advance += hsize
			continue
		}
		space := XLOGBlockSize - int(cur%XLOGBlockSize)
		want := n - len(out)
		if want > space {
			want = space
		}
		buf, err := it.readBytesAt(cur, want)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, buf...)
		advance += want
	}
	return out, advance, nil
}

// readBytesAt fetches `n` bytes starting at byte offset `pos` in the
// WAL stream, transparently spanning segment boundaries. Returns a
// fresh buffer the caller may retain.
//
// Hot path: when the writer has a non-nil in-memory ring AND the
// requested range is fully resident, the bytes come straight from
// RAM — no syscall. M0010-0002 introduced this path so walsenders
// don't depend on OS page cache residency, especially with
// `wal_direct_io=on` keeping recent WAL out of the cache. On a
// miss (range not resident, or no ring) we fall through to the
// per-segment pread loop. Hit/miss counters live on the ring and
// surface via the writer's observability accessors.
func (it *RecordIterator) readBytesAt(pos int64, n int) ([]byte, error) {
	out := make([]byte, n)
	if ring := it.writer.MemRing(); ring != nil {
		if got, ok := ring.ReadAt(pos, out); ok && got == n {
			return out, nil
		}
	}
	read := 0
	for read < n {
		segNo := uint64((pos + int64(read)) / it.segSize)
		segOff := (pos + int64(read)) % it.segSize
		want := int(it.segSize - segOff)
		if want > n-read {
			want = n - read
		}
		buf, err := readSegmentSlice(it.walDir, segNo, segOff, want)
		if err != nil {
			return nil, err
		}
		copy(out[read:read+len(buf)], buf)
		read += len(buf)
		if len(buf) < want {
			// Segment shorter than expected — likely a torn write at
			// the live tail. The Writer guarantees published WrittenLSN
			// covers complete records, but be defensive.
			return nil, fmt.Errorf("wal: short read in segment %d at off %d: got %d/%d",
				segNo, segOff, len(buf), want)
		}
	}
	return out, nil
}

// readSegmentSlice opens segment `segNo`, reads up to `n` bytes at
// `off`, and returns whatever was actually read (may be short if
// the file ends inside the requested window).
func readSegmentSlice(walDir string, segNo uint64, off int64, n int) ([]byte, error) {
	path := filepath.Join(walDir, formatSegmentName(segNo))
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("wal: iterator open %s: %w", path, err)
	}
	defer f.Close()
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("wal: iterator read %s @ %d: %w", path, off, err)
	}
	return buf[:got], nil
}
