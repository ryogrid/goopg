package executor

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"os"
	"time"

	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/hashsize"
	"github.com/goopg/goopg/internal/pgtemp"
	"github.com/goopg/goopg/internal/planner"
)

// spillWriter writes Row slices to a temporary file using a binary
// format. Each row is prefixed with a 4-byte little-endian length.
type spillWriter struct {
	f    *os.File
	bw   *bufio.Writer
	path string
	buf  []byte // reusable encode buffer

	// Cached activity registry reference and procNum for IO wait-event
	// recording. Populated once at construction via LookupCurrentGoroutine;
	// safe because the spillWriter is single-goroutine and the goroutine
	// is registered (SetCurrentGoroutine in server.go) before any spill
	// writer is created. See docs/design/tpch-round5-fixes/01.
	reg     *activity.ActivityRegistry
	procNum int32
	hasReg  bool

	// lenBuf is the 4-byte frame header. It is a field rather than a
	// local because it is handed to w.f.Write as an io.Writer argument,
	// which makes a local array escape and cost one allocation per
	// spilled row.
	lenBuf [4]byte
}

// newSpillWriter creates one spill file for ctx's statement.
//
// M0127-P3.3: the directory and the file name both come from PG's convention
// now (`<datadir>/base/pgsql_tmp/pgsql_tmp<pid>.*`, see internal/pgtemp) and
// the path is registered with ctx so the statement's end unlinks it even if
// this writer's owner never reaches Close. A nil ctx — unit-test operators
// built without one — keeps the old behaviour: the OS temp directory and no
// registration, so the caller stays responsible for its own file.
func newSpillWriter(ctx *Context) (*spillWriter, error) {
	dir, err := ctx.spillDir()
	if err != nil {
		return nil, err
	}
	w, err := newSpillWriterInDir(dir)
	if err != nil {
		return nil, err
	}
	ctx.registerSpillFile(w.path)
	return w, nil
}

// newSpillWriterInDir is the directory-explicit form. It exists for the
// registry itself (which resolves the directory once per statement) and for
// tests that want a t.TempDir() with no Context at all.
func newSpillWriterInDir(dir string) (*spillWriter, error) {
	f, err := os.CreateTemp(dir, pgtemp.FilePattern(os.Getpid()))
	if err != nil {
		return nil, fmt.Errorf("spillWriter: create temp file: %w", err)
	}
	// M0127-P3.2: the write buffer hashsize's walk-back already assumes.
	// hashsize.FileBufferBytes prices one BLCKSZ-sized buffer per batch file
	// when it decides whether more batches are worth their I/O (P3.1's
	// deferral row noted the buffer did not exist yet); sizing the buffer at
	// exactly that constant makes the assumption true instead of aspirational.
	// It also collapses the two write syscalls WriteRow used to make per row.
	w := &spillWriter{f: f, path: f.Name(), bw: bufio.NewWriterSize(f, hashsize.FileBufferBytes)}
	// Cache the registry reference once at construction time instead
	// of calling LookupCurrentGoroutine (→ runtime.Stack) on every
	// spilled row.  See docs/design/tpch-round5-fixes/01.
	if reg, procNum, ok := activity.LookupCurrentGoroutine(); ok {
		w.reg = reg
		w.procNum = procNum
		w.hasReg = true
	}
	return w, nil
}

func (w *spillWriter) WriteRow(row Row) error {
	w.buf = w.buf[:0]
	w.buf = appendRowPayload(w.buf, row)
	return w.writeFrame()
}

// WriteRowHashed writes a row preceded by its 32-bit join hash value
// (M0127-P3.2; design leftdeep-joins/06 §2.2). It is the analogue of PG's
// ExecHashJoinSaveTuple (nodeHashjoin.c:1414), which stores the hashvalue
// ahead of the MinimalTuple so a reloaded tuple never has to re-evaluate the
// join keys to learn which batch it belongs to.
//
// The two writers share appendRowPayload/writeFrame on purpose: a hashed frame
// is a plain frame with four leading bytes, and the encode/decode pair here is
// exactly the sibling-path class that has to change together.
func (w *spillWriter) WriteRowHashed(hashValue uint32, row Row) error {
	w.buf = w.buf[:0]
	w.buf = binary.LittleEndian.AppendUint32(w.buf, hashValue)
	w.buf = appendRowPayload(w.buf, row)
	return w.writeFrame()
}

// appendRowPayload encodes one Row (column count + datums) onto buf.
func appendRowPayload(buf []byte, row Row) []byte {
	buf = binary.AppendUvarint(buf, uint64(len(row)))
	for _, d := range row {
		buf = encodeDatum(d, buf)
	}
	return buf
}

// writeFrame emits w.buf prefixed by its little-endian 4-byte length.
func (w *spillWriter) writeFrame() error {
	binary.LittleEndian.PutUint32(w.lenBuf[:], uint32(len(w.buf)))
	if w.hasReg {
		w.reg.WaitEventStart(w.procNum, activity.WaitTypeIO, activity.WaitBuffileWrite)
	}
	_, err1 := w.bw.Write(w.lenBuf[:])
	_, err2 := w.bw.Write(w.buf)
	if w.hasReg {
		w.reg.WaitEventEnd(w.procNum)
	}
	if err1 != nil {
		return err1
	}
	return err2
}

func (w *spillWriter) Close() error {
	if err := w.bw.Flush(); err != nil {
		w.f.Close()
		return err
	}
	return w.f.Close()
}

func (w *spillWriter) Path() string { return w.path }

// spillReader reads rows written by spillWriter.
type spillReader struct {
	f    *os.File
	path string

	// M0054-0005b: reusable byte buffer for ReadRow's per-row
	// payload. The pre-fix path called `make([]byte, dataLen)`
	// per row, contributing to the cumulative byte-buffer churn
	// the M0054-0004 pprof survey flagged. The buffer grows
	// monotonically to fit the largest row seen and is never
	// shrunk — typical TPC-H rows are well-bounded so the steady
	// state is small.
	dataBuf []byte

	// Cached activity registry reference and procNum (mirrors spillWriter).
	// See docs/design/tpch-round5-fixes/01.
	reg     *activity.ActivityRegistry
	procNum int32
	hasReg  bool
}

func newSpillReader(path string) (*spillReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("spillReader: open %s: %w", path, err)
	}
	r := &spillReader{f: f, path: path}
	if reg, procNum, ok := activity.LookupCurrentGoroutine(); ok {
		r.reg = reg
		r.procNum = procNum
		r.hasReg = true
	}
	return r, nil
}

func (r *spillReader) ReadRow() (Row, error) {
	// Backwards-compatible thin wrapper around ReadRowInto: allocate
	// a fresh Row and fill. Used by callers that need an OwnedRow
	// (e.g., parent retains the row).
	row, err := r.ReadRowInto(nil)
	if err != nil {
		return nil, err
	}
	return row, nil
}

// ReadRowInto (M0054-0005b-followup) decodes the next spilled row
// into the caller-provided `dst` slice when its capacity is
// sufficient; otherwise allocates a fresh slice and returns it.
// Either way, the returned Row's contents are invalidated by the
// next ReadRowInto call, so callers that retain rows must clone
// the result. Pipeline-pass callers pass a single reusable `dst`
// across calls.
func (r *spillReader) ReadRowInto(dst Row) (Row, error) {
	data, err := r.readFrame()
	if err != nil {
		return nil, err
	}
	return decodeRowPayload(data, dst)
}

// ReadRowHashedInto is ReadRowInto for frames written by WriteRowHashed
// (M0127-P3.2): it returns the stored 32-bit hash value alongside the row.
// The hash is what lets a reloaded batch tuple be re-routed to a later batch
// (design leftdeep-joins/06 §2.3, PG's nodeHashjoin.c:1172-1202 rules 2 and 3)
// without evaluating a single key expression.
func (r *spillReader) ReadRowHashedInto(dst Row) (uint32, Row, error) {
	data, err := r.readFrame()
	if err != nil {
		return 0, nil, err
	}
	if len(data) < 4 {
		return 0, nil, fmt.Errorf("spillReader: hashed frame shorter than its hash prefix")
	}
	hashValue := binary.LittleEndian.Uint32(data[:4])
	row, err := decodeRowPayload(data[4:], dst)
	if err != nil {
		return 0, nil, err
	}
	return hashValue, row, nil
}

// readFrame reads one length-prefixed frame into r.dataBuf and returns it.
// The returned slice is only valid until the next readFrame call.
func (r *spillReader) readFrame() ([]byte, error) {
	var lenBuf [4]byte
	if r.hasReg {
		r.reg.WaitEventStart(r.procNum, activity.WaitTypeIO, activity.WaitBuffileRead)
	}
	_, errLen := io.ReadFull(r.f, lenBuf[:])
	if r.hasReg {
		r.reg.WaitEventEnd(r.procNum)
	}
	if errLen != nil {
		return nil, errLen
	}
	dataLen := binary.LittleEndian.Uint32(lenBuf[:])
	// M0054-0005b: reuse r.dataBuf across calls to avoid the
	// per-row `make([]byte, dataLen)` allocation.
	if cap(r.dataBuf) < int(dataLen) {
		r.dataBuf = make([]byte, dataLen)
	} else {
		r.dataBuf = r.dataBuf[:dataLen]
	}
	data := r.dataBuf
	if r.hasReg {
		r.reg.WaitEventStart(r.procNum, activity.WaitTypeIO, activity.WaitBuffileRead)
	}
	_, errData := io.ReadFull(r.f, data)
	if r.hasReg {
		r.reg.WaitEventEnd(r.procNum)
	}
	if errData != nil {
		return nil, fmt.Errorf("spillReader: truncated row: %w", errData)
	}
	return data, nil
}

// decodeRowPayload decodes a column count + datums payload into dst when dst
// has the capacity, else into a fresh Row.
func decodeRowPayload(data []byte, dst Row) (Row, error) {
	nCols, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, fmt.Errorf("spillReader: invalid column count")
	}
	// M0054-0005b-followup: reuse the caller-provided Row slot
	// when it has sufficient capacity. Saves the per-row
	// `make(Row, nCols)` allocation that the M0054-0004 in-use
	// heap pprof flagged as 1.65 GB live in Q9.
	row := dst
	if cap(row) < int(nCols) {
		row = make(Row, nCols)
	} else {
		row = row[:nCols]
	}
	pos := n
	for i := uint64(0); i < nCols; i++ {
		d, n2, err := decodeDatum(data[pos:])
		if err != nil {
			return nil, fmt.Errorf("spillReader: decode col %d: %w", i, err)
		}
		row[i] = d
		pos += n2
	}
	return row, nil
}

func (r *spillReader) Close() error {
	err := r.f.Close()
	os.Remove(r.path)
	return err
}

// encodeDatum serialises one Datum into buf, returning the extended slice.
func encodeDatum(d Datum, buf []byte) []byte {
	// Kind byte.
	buf = append(buf, byte(d.Kind))
	switch d.Kind {
	case KindNull:
		// nothing else
	case KindBool:
		if d.BoolValue() {
			buf = append(buf, 1)
		} else {
			buf = append(buf, 0)
		}
	case KindInt:
		buf = binary.LittleEndian.AppendUint64(buf, uint64(d.Int))
	case KindString:
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(d.StringValue())))
		buf = append(buf, d.StringValue()...)
	case KindBytes:
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(d.BytesValue())))
		buf = append(buf, d.BytesValue()...)
	case KindTime:
		n := d.TimeValue().UTC().UnixNano()
		buf = binary.LittleEndian.AppendUint64(buf, uint64(n))
	case KindInterval:
		buf = binary.LittleEndian.AppendUint32(buf, uint32(d.IntervalMonthsValue()))
		buf = binary.LittleEndian.AppendUint32(buf, uint32(d.IntervalDaysValue()))
		buf = binary.LittleEndian.AppendUint64(buf, uint64(d.IntervalMicrosValue()))
	case KindNumeric:
		// 2-byte int16 scale + length-prefixed signed-magnitude
		// big.Int bytes (1 sign byte + magnitude). Per-query
		// spill files have no on-disk back-compat constraint,
		// so the wider layout is safe; fits-int64 values still
		// pack into 2 + 4 + 1 + ≤8 bytes ≈ 15 bytes.
		buf = binary.LittleEndian.AppendUint16(buf, uint16(d.Scale))
		mant := numericMant(d)
		signByte := byte(0)
		if mant.Sign() < 0 {
			signByte = 1
		}
		mag := new(big.Int).Abs(mant).Bytes()
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(mag)))
		buf = append(buf, signByte)
		buf = append(buf, mag...)
	}
	return buf
}

// decodeDatum reads one Datum from data, returning the value, bytes consumed, and error.
func decodeDatum(data []byte) (Datum, int, error) {
	if len(data) == 0 {
		return Datum{}, 0, fmt.Errorf("empty data")
	}
	kind := DatumKind(data[0])
	pos := 1
	switch kind {
	case KindNull:
		return NullDatum, pos, nil
	case KindBool:
		if pos >= len(data) {
			return Datum{}, 0, fmt.Errorf("truncated bool at %d", pos)
		}
		return NewBoolDatum(data[pos] != 0), pos + 1, nil
	case KindInt:
		if pos+8 > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated int at %d", pos)
		}
		return Datum{Kind: KindInt, Int: int64(binary.LittleEndian.Uint64(data[pos:]))}, pos + 8, nil
	case KindString:
		if pos+4 > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated string len at %d", pos)
		}
		slen := binary.LittleEndian.Uint32(data[pos:])
		pos += 4
		if pos+int(slen) > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated string body at %d (want %d)", pos, slen)
		}
		s := string(data[pos : pos+int(slen)])
		return NewStringDatum(s), pos + int(slen), nil
	case KindBytes:
		if pos+4 > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated bytes len at %d", pos)
		}
		blen := binary.LittleEndian.Uint32(data[pos:])
		pos += 4
		if pos+int(blen) > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated bytes body at %d (want %d)", pos, blen)
		}
		b := make([]byte, blen)
		copy(b, data[pos:pos+int(blen)])
		return NewBytesDatum(b), pos + int(blen), nil
	case KindTime:
		if pos+8 > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated time at %d", pos)
		}
		n := int64(binary.LittleEndian.Uint64(data[pos:]))
		return NewTimeDatum(time.Unix(0, n).UTC()), pos + 8, nil
	case KindInterval:
		if pos+16 > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated interval at %d", pos)
		}
		months := int32(binary.LittleEndian.Uint32(data[pos:]))
		days := int32(binary.LittleEndian.Uint32(data[pos+4:]))
		micros := int64(binary.LittleEndian.Uint64(data[pos+8:]))
		return NewIntervalDatumFull(months, days, micros), pos + 16, nil
	case KindNumeric:
		if pos+6 > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated numeric at %d", pos)
		}
		s := int16(binary.LittleEndian.Uint16(data[pos:]))
		pos += 2
		mlen := int(binary.LittleEndian.Uint32(data[pos:]))
		pos += 4
		if pos+1+mlen > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated numeric body at %d (want %d)", pos, mlen)
		}
		signByte := data[pos]
		pos++
		mag := new(big.Int).SetBytes(data[pos : pos+mlen])
		if signByte != 0 {
			mag.Neg(mag)
		}
		pos += mlen
		return newNumeric(mag, int(s)), pos, nil
	default:
		return Datum{}, 0, fmt.Errorf("unknown datum kind %d", kind)
	}
}

// estimatedRowBytes returns a rough size estimate for a Row.
func estimatedRowBytes(row Row) int64 {
	n := int64(len(row) * 48) // Datum struct fixed overhead
	for _, d := range row {
		switch d.Kind {
		case KindString:
			n += int64(len(d.StringValue()))
		case KindBytes:
			n += int64(len(d.BytesValue()))
		}
	}
	return n
}

// drainRowsBounded is like drainRows but enforces a memory budget.
// When accumulated rows exceed maxBytes, they are spilled to a temp
// file and the in-memory slice is freed. Returns an operator that
// yields rows either from memory (if no spill occurred) or from the
// spill file.
func drainRowsBounded(ctx *Context, op Operator, maxBytes int64) (Operator, error) {
	if maxBytes <= 0 {
		return drainRowsToOp(op)
	}
	rows := make([]Row, 0, 1024)
	var totalBytes int64
	spilled := false
	var w *spillWriter

	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			if w != nil {
				w.Close()
				ctx.removeSpillFile(w.Path())
			}
			return nil, err
		}
		row := slotRow(slot)
		totalBytes += estimatedRowBytes(row)
		if totalBytes > maxBytes && !spilled {
			// Flush accumulated rows to spill file.
			var werr error
			w, werr = newSpillWriter(ctx)
			if werr != nil {
				return nil, werr
			}
			for _, r := range rows {
				if werr = w.WriteRow(r); werr != nil {
					w.Close()
					ctx.removeSpillFile(w.Path())
					return nil, werr
				}
			}
			rows = nil // free memory
			spilled = true
		}
		if spilled {
			if err := w.WriteRow(row); err != nil {
				w.Close()
				ctx.removeSpillFile(w.Path())
				return nil, err
			}
			continue
		}
		// M0073-0004 retention boundary: arena-backed Datums must
		// be promoted to owned []byte before we accumulate, since
		// the producer's next Next may trigger arena.Reset and
		// invalidate the previous page's bytes. The non-arena
		// fast path preserves the legacy O(width) struct copy.
		var dup Row
		if rowHasArena(row) {
			dup = cloneRowOwned(row)
		} else {
			dup = make(Row, len(row))
			copy(dup, row)
		}
		rows = append(rows, dup)
	}

	if spilled {
		if err := w.Close(); err != nil {
			ctx.removeSpillFile(w.Path())
			return nil, err
		}
		r, err := newSpillReader(w.Path())
		if err != nil {
			ctx.removeSpillFile(w.Path())
			return nil, err
		}
		// The spillOp inherits the file: its Close unlinks it (M0127-P3.3
		// — before this it closed the reader and left the file behind for
		// the OS tempdir cleaner, which is the leak 06 §3 names).
		return &spillOp{r: r, ctx: ctx}, nil
	}
	// No spill — return a simple in-memory operator.
	return &rowsOp{rows: rows}, nil
}

// drainRowsToOp is the legacy path: drains all rows into memory.
func drainRowsToOp(op Operator) (Operator, error) {
	rs, err := drainRows(op)
	if err != nil {
		return nil, err
	}
	return &rowsOp{rows: rs}, nil
}

// rowsOp implements Operator over a pre-drained []Row.
type rowsOp struct {
	rows   []Row
	idx    int
	schema planner.Schema
}

func (o *rowsOp) Open(*Context) error    { o.idx = 0; return nil }
func (o *rowsOp) Schema() planner.Schema { return o.schema } // schema comes from joinOp/upstream
func (o *rowsOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	return asSlot(o.schema, r), nil
}
func (o *rowsOp) Close() error {
	o.rows = nil
	return nil
}

// spillOp implements Operator over a spillReader. M0071-0015 Stage E:
// always cloneRow; consumers materialize via the slot pipeline.
type spillOp struct {
	r   *spillReader
	ctx *Context
	out Row
}

func (o *spillOp) Open(*Context) error    { return nil }
func (o *spillOp) Schema() planner.Schema { return nil }

func (o *spillOp) Next() (TupleSlot, error) {
	row, err := o.r.ReadRowInto(o.out)
	if err == io.EOF {
		return nil, EOF
	}
	if err != nil {
		return nil, err
	}
	o.out = row // re-cap for the next call
	return asSlot(nil, cloneRow(row)), nil
}
func (o *spillOp) Close() error {
	// M0127-P3.3: unlink, not just close. The file is this operator's
	// alone — drainRowsBounded handed it over — and before the fix the
	// reader was closed while the file stayed on disk until the OS
	// tempdir cleaner (or never, once the files moved into the datadir).
	// The registry would still reclaim it at statement end; doing it here
	// keeps a long statement from accumulating one file per bounded drain.
	path := o.r.path
	o.r.Close()
	o.r = nil
	o.ctx.removeSpillFile(path)
	return nil
}
