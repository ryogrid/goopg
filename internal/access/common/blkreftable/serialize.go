package blkreftable

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
)

// Magic is BLOCKREFTABLE_MAGIC, the first four bytes of a serialized table.
const Magic uint32 = 0x652b137b

// serializedEntrySize is sizeof(BlockRefTableSerializedEntry): spcOid,
// dbOid, relNumber, forknum, limit_block and nchunks, four bytes each with no
// padding. An all-zero entry terminates the file.
const serializedEntrySize = 24

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Serialized integers are host-endian in PG; goopg targets little-endian
// hosts only, like the rest of its on-disk formats.
var le = binary.LittleEndian

// crcWriter feeds every byte written into the running CRC-32C, the way
// BlockRefTableWrite does with COMP_CRC32C.
type crcWriter struct {
	w   *bufio.Writer
	crc hash.Hash32
	err error
}

func newCRCWriter(w io.Writer) *crcWriter {
	return &crcWriter{w: bufio.NewWriterSize(w, 65536), crc: crc32.New(castagnoli)}
}

func (c *crcWriter) write(b []byte) {
	if c.err != nil {
		return
	}
	c.crc.Write(b)
	_, c.err = c.w.Write(b)
}

func (c *crcWriter) u32(v uint32) {
	var b [4]byte
	le.PutUint32(b[:], v)
	c.write(b[:])
}

func (c *crcWriter) u16s(vs []uint16) {
	b := make([]byte, 2*len(vs))
	for i, v := range vs {
		le.PutUint16(b[2*i:], v)
	}
	c.write(b)
}

// entry writes one serialized entry with its chunk usage array and the data
// of every chunk in use (the body of BlockRefTableWriteEntry and of
// WriteBlockRefTable's loop).
func (c *crcWriter) entry(e *Entry) {
	n := e.serializedChunks()
	c.u32(e.key.Locator.SpcOid)
	c.u32(e.key.Locator.DbOid)
	c.u32(e.key.Locator.RelNumber)
	c.u32(uint32(e.key.Fork))
	c.u32(e.limitBlock)
	c.u32(uint32(n))
	if n == 0 {
		return
	}
	c.u16s(e.chunkUsage[:n])
	for i := 0; i < n; i++ {
		if u := e.chunkUsage[i]; u != 0 {
			c.u16s(e.chunkData[i][:u])
		}
	}
}

// terminate writes the all-zero entry and the CRC of everything before it,
// then flushes (BlockRefTableFileTerminate).
func (c *crcWriter) terminate() error {
	c.write(make([]byte, serializedEntrySize))
	if c.err != nil {
		return c.err
	}
	var b [4]byte
	le.PutUint32(b[:], c.crc.Sum32())
	if _, err := c.w.Write(b[:]); err != nil {
		return err
	}
	return c.w.Flush()
}

// Write serializes the table to w in PostgreSQL's format
// (WriteBlockRefTable).
func (t *Table) Write(w io.Writer) error {
	c := newCRCWriter(w)
	c.u32(Magic)
	for _, e := range t.SortedEntries() {
		c.entry(e)
	}
	return c.terminate()
}

// Writer serializes a table one entry at a time, for callers that build an
// entry, write it and drop it (CreateBlockRefTableWriter /
// BlockRefTableWriteEntry / DestroyBlockRefTableWriter). The caller must
// supply entries in SortedEntries order; the format does not record it, but
// readers merge files assuming it.
type Writer struct {
	c *crcWriter
}

// NewWriter writes the magic number and returns a Writer.
func NewWriter(w io.Writer) *Writer {
	c := newCRCWriter(w)
	c.u32(Magic)
	return &Writer{c: c}
}

// WriteEntry appends one entry.
func (w *Writer) WriteEntry(e *Entry) { w.c.entry(e) }

// Close terminates the file and flushes it. It does not close the
// underlying io.Writer.
func (w *Writer) Close() error { return w.c.terminate() }

// ErrCorrupt wraps every format error a Reader reports. PG raises these as
// ERRCODE_DATA_CORRUPTED through the caller's error callback
// (ReportWalSummaryError); callers choose the SQLSTATE.
var ErrCorrupt = errors.New("corrupt WAL summary")

// Reader reads a serialized table entry by entry
// (CreateBlockRefTableReader and friends). Errors carry PG's messages.
type Reader struct {
	r        *bufio.Reader
	crc      hash.Hash32
	filename string

	chunkSizes []uint16
	consumed   int
	chunkData  []uint16
	position   int
}

func (rd *Reader) corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrCorrupt}, args...)...)
}

// read fills b and adds it to the running CRC (BlockRefTableRead).
func (rd *Reader) read(b []byte) error {
	if _, err := io.ReadFull(rd.r, b); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return rd.corrupt("file %q ends unexpectedly", rd.filename)
		}
		return err
	}
	rd.crc.Write(b)
	return nil
}

func (rd *Reader) u16s(n int) ([]uint16, error) {
	b := make([]byte, 2*n)
	if err := rd.read(b); err != nil {
		return nil, err
	}
	out := make([]uint16, n)
	for i := range out {
		out[i] = le.Uint16(b[2*i:])
	}
	return out, nil
}

// NewReader checks the magic number and returns a Reader positioned at the
// first entry. filename is used in error messages only.
func NewReader(r io.Reader, filename string) (*Reader, error) {
	rd := &Reader{r: bufio.NewReaderSize(r, 65536), crc: crc32.New(castagnoli), filename: filename}
	var b [4]byte
	if err := rd.read(b[:]); err != nil {
		return nil, err
	}
	if m := le.Uint32(b[:]); m != Magic {
		return nil, rd.corrupt("file %q has wrong magic number: expected %d, found %d", filename, Magic, m)
	}
	return rd, nil
}

// NextRelation reads the next entry's header and chunk sizes
// (BlockRefTableReaderNextRelation). It returns ok=false at the terminator,
// after checking the CRC. The previous relation's blocks must have been
// fully consumed with Blocks.
func (rd *Reader) NextRelation() (key Key, limit BlockNumber, ok bool, err error) {
	if rd.consumed != len(rd.chunkSizes) {
		return Key{}, 0, false, errors.New("blkreftable: NextRelation before the previous relation's blocks were read")
	}
	var b [serializedEntrySize]byte
	if err := rd.read(b[:]); err != nil {
		return Key{}, 0, false, err
	}
	if b == [serializedEntrySize]byte{} {
		expected := rd.crc.Sum32()
		var c [4]byte
		if _, err := io.ReadFull(rd.r, c[:]); err != nil {
			return Key{}, 0, false, rd.corrupt("file %q ends unexpectedly", rd.filename)
		}
		if actual := le.Uint32(c[:]); actual != expected {
			return Key{}, 0, false, rd.corrupt("file %q has wrong checksum: expected %08X, found %08X",
				rd.filename, expected, actual)
		}
		return Key{}, 0, false, nil
	}
	key = Key{
		Locator: RelFileLocator{SpcOid: le.Uint32(b[0:]), DbOid: le.Uint32(b[4:]), RelNumber: le.Uint32(b[8:])},
		Fork:    int32(le.Uint32(b[12:])),
	}
	limit = le.Uint32(b[16:])
	nchunks := le.Uint32(b[20:])
	sizes, err := rd.u16s(int(nchunks))
	if err != nil {
		return Key{}, 0, false, err
	}
	rd.chunkSizes = sizes
	rd.consumed = 0
	rd.position = 0
	rd.chunkData = rd.chunkData[:0]
	return key, limit, true, nil
}

// Blocks reads up to max more modified blocks of the current relation and
// appends them to dst (BlockRefTableReaderGetBlocks). It returns dst
// unchanged once the relation is exhausted. Array chunks yield their blocks
// in stored (insertion) order.
func (rd *Reader) Blocks(dst []BlockNumber, max int) ([]BlockNumber, error) {
	found := 0
	for found < max {
		if rd.consumed > 0 {
			chunk := rd.consumed - 1
			base := BlockNumber(chunk) * blocksPerChunk
			if rd.chunkSizes[chunk] == maxEntriesPerChunk {
				for rd.position < blocksPerChunk && found < max {
					off := rd.position
					if rd.chunkData[off/blocksPerEntry]&(1<<(off%blocksPerEntry)) != 0 {
						dst = append(dst, base+BlockNumber(off))
						found++
					}
					rd.position++
				}
			} else {
				for rd.position < int(rd.chunkSizes[chunk]) && found < max {
					dst = append(dst, base+BlockNumber(rd.chunkData[rd.position]))
					found++
					rd.position++
				}
			}
		}
		if found >= max || rd.consumed == len(rd.chunkSizes) {
			break
		}
		next := int(rd.chunkSizes[rd.consumed])
		if next > maxEntriesPerChunk {
			return dst, rd.corrupt("file %q has invalid chunk size %d", rd.filename, next)
		}
		rd.chunkData = rd.chunkData[:0]
		if next > 0 {
			data, err := rd.u16s(next)
			if err != nil {
				return dst, err
			}
			rd.chunkData = data
		}
		rd.consumed++
		rd.position = 0
	}
	return dst, nil
}
