package executor

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"

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
	if _, err := w.f.Write(lenBuf); err != nil {
		return err
	}
	if _, err := w.f.Write(w.buf); err != nil {
		return err
	}
	return nil
}

func (w *spillWriter) Close() error {
	return w.f.Close()
}

func (w *spillWriter) Path() string { return w.path }

// spillReader reads rows written by spillWriter.
type spillReader struct {
	f    *os.File
	path string
}

func newSpillReader(path string) (*spillReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("spillReader: open %s: %w", path, err)
	}
	return &spillReader{f: f, path: path}, nil
}

func (r *spillReader) ReadRow() (Row, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r.f, lenBuf[:]); err != nil {
		return nil, err
	}
	dataLen := binary.LittleEndian.Uint32(lenBuf[:])
	data := make([]byte, dataLen)
	if _, err := io.ReadFull(r.f, data); err != nil {
		return nil, fmt.Errorf("spillReader: truncated row: %w", err)
	}
	nCols, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, fmt.Errorf("spillReader: invalid column count")
	}
	row := make(Row, nCols)
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
		if d.Bool {
			buf = append(buf, 1)
		} else {
			buf = append(buf, 0)
		}
	case KindInt:
		buf = binary.LittleEndian.AppendUint64(buf, uint64(d.Int))
	case KindString:
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(d.String)))
		buf = append(buf, d.String...)
	case KindBytes:
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(d.Bytes)))
		buf = append(buf, d.Bytes...)
	case KindTime:
		n := d.Time.UTC().UnixNano()
		buf = binary.LittleEndian.AppendUint64(buf, uint64(n))
	case KindInterval:
		buf = binary.LittleEndian.AppendUint32(buf, uint32(d.IntervalMonths))
		buf = binary.LittleEndian.AppendUint32(buf, uint32(d.IntervalDays))
	case KindNumeric:
		buf = binary.LittleEndian.AppendUint64(buf, uint64(d.NumericMantissa))
		buf = append(buf, byte(d.NumericScale))
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
		return Datum{Kind: KindBool, Bool: data[pos] != 0}, pos + 1, nil
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
		return Datum{Kind: KindString, String: s}, pos + int(slen), nil
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
		return Datum{Kind: KindBytes, Bytes: b}, pos + int(blen), nil
	case KindTime:
		if pos+8 > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated time at %d", pos)
		}
		n := int64(binary.LittleEndian.Uint64(data[pos:]))
		return Datum{Kind: KindTime, Time: time.Unix(0, n).UTC()}, pos + 8, nil
	case KindInterval:
		if pos+8 > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated interval at %d", pos)
		}
		months := int32(binary.LittleEndian.Uint32(data[pos:]))
		days := int32(binary.LittleEndian.Uint32(data[pos+4:]))
		return Datum{Kind: KindInterval, IntervalMonths: months, IntervalDays: days}, pos + 8, nil
	case KindNumeric:
		if pos+9 > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated numeric at %d", pos)
		}
		m := int64(binary.LittleEndian.Uint64(data[pos:]))
		s := int8(data[pos+8])
		return Datum{Kind: KindNumeric, NumericMantissa: m, NumericScale: s}, pos + 9, nil
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
			n += int64(len(d.String))
		case KindBytes:
			n += int64(len(d.Bytes))
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
		row, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			if w != nil {
				w.Close()
			}
			return nil, err
		}
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
		dup := make(Row, len(row))
		copy(dup, row)
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
	rows []Row
	idx  int
}

func (o *rowsOp) Open(*Context) error { o.idx = 0; return nil }
func (o *rowsOp) Schema() planner.Schema { return nil } // schema comes from joinOp
func (o *rowsOp) Next() (Row, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	return r, nil
}
func (o *rowsOp) Close() error {
	o.rows = nil
	return nil
}

// spillOp implements Operator over a spillReader.
type spillOp struct {
	r    *spillReader
	ctx  *Context
}

func (o *spillOp) Open(*Context) error { return nil }
func (o *spillOp) Schema() planner.Schema { return nil }
func (o *spillOp) Next() (Row, error) {
	row, err := o.r.ReadRow()
	if err == io.EOF {
		return nil, EOF
	}
	return row, err
}
func (o *spillOp) Close() error {
	o.r.Close()
	o.r = nil
	return nil
}
