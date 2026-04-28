package storage

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	itemIDSize = 4

	// SizeOfHeapTupleHeaderData mirrors the fixed-size header fields in
	// postgres/src/include/access/htup_details.h.
	SizeOfHeapTupleHeaderData = 23

	// DefaultHeapTupleHoff is the aligned tuple data offset used by v0
	// tuples without null bitmap/OID.
	DefaultHeapTupleHoff = 24
)

var (
	ErrNoSpaceInPage   = errors.New("storage: not enough free space in page")
	ErrInvalidSlot     = errors.New("storage: invalid tuple slot")
	ErrUnsupportedItem = errors.New("storage: unsupported line pointer state")
	ErrCorruptTuple    = errors.New("storage: corrupt heap tuple")
)

// TransactionID is a 32-bit xid. Mirrors PostgreSQL's TransactionId.
type TransactionID uint32

const (
	// InvalidTransactionID is xid 0.
	InvalidTransactionID TransactionID = 0
)

// ItemPointer identifies a tuple location (block, line-pointer slot).
type ItemPointer struct {
	Block  BlockNumber
	Offset uint16
}

// HeapTupleHeader is the fixed tuple header subset used in milestone 5.
// It carries xmin/xmax visibility metadata and ctid linkage.
type HeapTupleHeader struct {
	Xmin      TransactionID
	Xmax      TransactionID
	Xvac      TransactionID
	CTID      ItemPointer
	Infomask  uint16
	Infomask2 uint16
	Hoff      uint8
}

// HeapTuple is one on-page tuple body.
type HeapTuple struct {
	Header HeapTupleHeader
	Data   []byte
}

// NewHeapTuple constructs a tuple with v0 defaults.
func NewHeapTuple(xmin, xmax TransactionID, data []byte) HeapTuple {
	out := make([]byte, len(data))
	copy(out, data)
	return HeapTuple{
		Header: HeapTupleHeader{
			Xmin: xmin,
			Xmax: xmax,
			Xvac: InvalidTransactionID,
			CTID: ItemPointer{Block: InvalidBlockNumber, Offset: 0},
			Hoff: DefaultHeapTupleHoff,
		},
		Data: out,
	}
}

// MarshalBinary encodes the tuple into the on-page layout.
func (t HeapTuple) MarshalBinary() ([]byte, error) {
	hoff := int(t.Header.Hoff)
	if hoff == 0 {
		hoff = DefaultHeapTupleHoff
	}
	if hoff < SizeOfHeapTupleHeaderData || hoff > 255 {
		return nil, fmt.Errorf("invalid t_hoff=%d", hoff)
	}
	out := make([]byte, hoff+len(t.Data))
	binary.LittleEndian.PutUint32(out[0:4], uint32(t.Header.Xmin))
	binary.LittleEndian.PutUint32(out[4:8], uint32(t.Header.Xmax))
	binary.LittleEndian.PutUint32(out[8:12], uint32(t.Header.Xvac))
	binary.LittleEndian.PutUint32(out[12:16], uint32(t.Header.CTID.Block))
	binary.LittleEndian.PutUint16(out[16:18], t.Header.CTID.Offset)
	binary.LittleEndian.PutUint16(out[18:20], t.Header.Infomask2)
	binary.LittleEndian.PutUint16(out[20:22], t.Header.Infomask)
	out[22] = byte(hoff)
	copy(out[hoff:], t.Data)
	return out, nil
}

// ParseHeapTuple decodes one on-page tuple payload.
func ParseHeapTuple(raw []byte) (HeapTuple, error) {
	if len(raw) < SizeOfHeapTupleHeaderData {
		return HeapTuple{}, fmt.Errorf("%w: raw len=%d", ErrCorruptTuple, len(raw))
	}
	hoff := int(raw[22])
	if hoff < SizeOfHeapTupleHeaderData || hoff > len(raw) {
		return HeapTuple{}, fmt.Errorf("%w: invalid t_hoff=%d len=%d", ErrCorruptTuple, hoff, len(raw))
	}
	t := HeapTuple{
		Header: HeapTupleHeader{
			Xmin:      TransactionID(binary.LittleEndian.Uint32(raw[0:4])),
			Xmax:      TransactionID(binary.LittleEndian.Uint32(raw[4:8])),
			Xvac:      TransactionID(binary.LittleEndian.Uint32(raw[8:12])),
			CTID:      ItemPointer{Block: BlockNumber(binary.LittleEndian.Uint32(raw[12:16])), Offset: binary.LittleEndian.Uint16(raw[16:18])},
			Infomask2: binary.LittleEndian.Uint16(raw[18:20]),
			Infomask:  binary.LittleEndian.Uint16(raw[20:22]),
			Hoff:      uint8(hoff),
		},
		Data: append([]byte(nil), raw[hoff:]...),
	}
	return t, nil
}

// ItemIDFlags mirrors PostgreSQL ItemId state bits.
type ItemIDFlags uint8

const (
	ItemIDUnused   ItemIDFlags = 0
	ItemIDNormal   ItemIDFlags = 1
	ItemIDRedirect ItemIDFlags = 2
	ItemIDDead     ItemIDFlags = 3
)

// ItemID is one 4-byte line pointer.
type ItemID struct {
	Offset uint16
	Flags  ItemIDFlags
	Length uint16
}

func (i ItemID) pack() (uint32, error) {
	if i.Offset > 0x7FFF {
		return 0, fmt.Errorf("itemid offset out of range: %d", i.Offset)
	}
	if i.Length > 0x7FFF {
		return 0, fmt.Errorf("itemid length out of range: %d", i.Length)
	}
	if i.Flags > 0x3 {
		return 0, fmt.Errorf("itemid flags out of range: %d", i.Flags)
	}
	raw := uint32(i.Offset&0x7FFF) |
		(uint32(i.Flags&0x3) << 15) |
		(uint32(i.Length&0x7FFF) << 17)
	return raw, nil
}

func unpackItemID(raw uint32) ItemID {
	return ItemID{
		Offset: uint16(raw & 0x7FFF),
		Flags:  ItemIDFlags((raw >> 15) & 0x3),
		Length: uint16((raw >> 17) & 0x7FFF),
	}
}

// PageLinePointerCount returns the number of line pointers present.
func PageLinePointerCount(p Page) (int, error) {
	h, err := Header(p)
	if err != nil {
		return 0, err
	}
	lower := int(h.Lower())
	if lower < SizeOfPageHeaderData {
		return 0, fmt.Errorf("invalid pd_lower=%d", lower)
	}
	if (lower-SizeOfPageHeaderData)%itemIDSize != 0 {
		return 0, fmt.Errorf("invalid line pointer area size: lower=%d", lower)
	}
	return (lower - SizeOfPageHeaderData) / itemIDSize, nil
}

// PageAddHeapTuple appends a tuple to the page and returns the 1-based
// line-pointer slot number.
func PageAddHeapTuple(p Page, t HeapTuple) (uint16, error) {
	h, err := Header(p)
	if err != nil {
		return 0, err
	}
	raw, err := t.MarshalBinary()
	if err != nil {
		return 0, err
	}
	if len(raw) > 0x7FFF {
		return 0, fmt.Errorf("tuple too large for line pointer len=%d", len(raw))
	}
	lower := int(h.Lower())
	upper := int(h.Upper())
	needed := itemIDSize + len(raw)
	if upper-lower < needed {
		return 0, ErrNoSpaceInPage
	}

	newUpper := upper - len(raw)
	copy(p[newUpper:upper], raw)

	count, err := PageLinePointerCount(p)
	if err != nil {
		return 0, err
	}
	item := ItemID{Offset: uint16(newUpper), Flags: ItemIDNormal, Length: uint16(len(raw))}
	if err := writeItemID(p, count, item); err != nil {
		return 0, err
	}
	h.SetLower(uint16(lower + itemIDSize))
	h.SetUpper(uint16(newUpper))
	return uint16(count + 1), nil
}

// PageGetHeapTuple reads the tuple stored in a 1-based line-pointer slot.
func PageGetHeapTuple(p Page, slot uint16) (HeapTuple, error) {
	if slot == 0 {
		return HeapTuple{}, ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return HeapTuple{}, err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return HeapTuple{}, ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return HeapTuple{}, err
	}
	if item.Flags != ItemIDNormal {
		return HeapTuple{}, fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	ln := int(item.Length)
	if off < 0 || ln < 0 || off+ln > len(p) {
		return HeapTuple{}, fmt.Errorf("%w: slot=%d off=%d len=%d", ErrCorruptTuple, slot, off, ln)
	}
	raw := append([]byte(nil), p[off:off+ln]...)
	return ParseHeapTuple(raw)
}

func readItemID(p Page, idx int) (ItemID, error) {
	off := SizeOfPageHeaderData + idx*itemIDSize
	if off < 0 || off+itemIDSize > len(p) {
		return ItemID{}, fmt.Errorf("line pointer index out of range: idx=%d", idx)
	}
	raw := binary.LittleEndian.Uint32(p[off : off+itemIDSize])
	return unpackItemID(raw), nil
}

func writeItemID(p Page, idx int, item ItemID) error {
	off := SizeOfPageHeaderData + idx*itemIDSize
	if off < 0 || off+itemIDSize > len(p) {
		return fmt.Errorf("line pointer index out of range: idx=%d", idx)
	}
	raw, err := item.pack()
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(p[off:off+itemIDSize], raw)
	return nil
}
