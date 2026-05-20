package executor

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"os"
	"time"

	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/planner"
)

// spillWriter writes Row slices to a temporary file using a binary
// format. Each row is prefixed with a 4-byte little-endian length.
type spillWriter struct {
	f    *os.File
	path string
	buf  []byte // reusable encode buffer
}

func newSpillWriter(dir string) (*spillWriter, error) {
	f, err := os.CreateTemp(dir, "goopg-spill-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("spillWriter: create temp file: %w", err)
	}
	return &spillWriter{f: f, path: f.Name()}, nil
}

func (w *spillWriter) WriteRow(row Row) error {
	w.buf = w.buf[:0]
	w.buf = binary.AppendUvarint(w.buf, uint64(len(row)))
	for _, d := range row {
		w.buf = encodeDatum(d, w.buf)
	}
	// Prefix with total length for framing.
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, uint32(len(w.buf)))
	reg, procNum, okReg := activity.LookupCurrentGoroutine()
	if okReg {
		reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitBuffileWrite)
	}
	_, err1 := w.f.Write(lenBuf)
	_, err2 := w.f.Write(w.buf)
	if okReg {
		reg.WaitEventEnd(procNum)
	}
	if err1 != nil {
		return err1
	}
	return err2
}

func (w *spillWriter) Close() error {
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
}

func newSpillReader(path string) (*spillReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("spillReader: open %s: %w", path, err)
	}
	return &spillReader{f: f, path: path}, nil
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
	var lenBuf [4]byte
	reg, procNum, okReg := activity.LookupCurrentGoroutine()
	if okReg {
		reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitBuffileRead)
	}
	_, errLen := io.ReadFull(r.f, lenBuf[:])
	if okReg {
		reg.WaitEventEnd(procNum)
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
	if okReg {
		reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitBuffileRead)
	}
	_, errData := io.ReadFull(r.f, data)
	if okReg {
		reg.WaitEventEnd(procNum)
	}
	if errData != nil {
		return nil, fmt.Errorf("spillReader: truncated row: %w", errData)
	}
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
		if pos+8 > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated interval at %d", pos)
		}
		months := int32(binary.LittleEndian.Uint32(data[pos:]))
		days := int32(binary.LittleEndian.Uint32(data[pos+4:]))
		return NewIntervalDatum(months, days), pos + 8, nil
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
func drainRowsBounded(op Operator, maxBytes int64) (Operator, error) {
	if maxBytes <= 0 {
		return drainRowsToOp(op)
	}
	rows := make([]Row, 0, 1024)
	var totalBytes int64
	spilled := false
	var w *spillWriter
	tmpDir := os.TempDir()

	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			if w != nil {
				w.Close()
			}
			return nil, err
		}
		row := slotRow(slot)
		totalBytes += estimatedRowBytes(row)
		if totalBytes > maxBytes && !spilled {
			// Flush accumulated rows to spill file.
			var werr error
			w, werr = newSpillWriter(tmpDir)
			if werr != nil {
				return nil, werr
			}
			for _, r := range rows {
				if werr = w.WriteRow(r); werr != nil {
					w.Close()
					return nil, werr
				}
			}
			rows = nil // free memory
			spilled = true
		}
		if spilled {
			if err := w.WriteRow(row); err != nil {
				w.Close()
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
			return nil, err
		}
		r, err := newSpillReader(w.Path())
		if err != nil {
			return nil, err
		}
		return &spillOp{r: r}, nil
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
	o.r.Close()
	o.r = nil
	return nil
}
