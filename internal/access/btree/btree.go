// Package btree implements goopg's v0 B-tree index access method.
//
// Scope and growth path are documented in docs/design/0009-btree.md.
// v0 supports single-column int4 keys, descend/insert/search/range-scan,
// recursive splits up to a new root. WAL records for inserts/splits and
// page deletion (VACUUM merge) are deferred — see the doc for the bridge.
package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/goopg/goopg/internal/storage"
)

// SizeOfBTPageOpaque is the on-disk size of the per-page B-tree opaque
// area. v3 grew it from 24 to 48 bytes to carry a variable-length
// HighKey (M0011-0002 — see
// docs/design/0011-0002-btree-numeric-build-and-uniqueness.md), which
// is required for NUMERIC keys whose encoded form exceeds the
// previous 4-byte field. v4 widened the HighKey field from 32 to 256
// bytes to accommodate text/varchar B-tree keys (e.g. road.name in
// create_index regress). Layout, in little-endian:
//
//	offset 0  Prev        (4 bytes)
//	offset 4  Next        (4 bytes)
//	offset 8  Level       (4 bytes)
//	offset 12 Flags       (2 bytes)
//	offset 14 HighKeyLen  (2 bytes)
//	offset 16 HighKey     (256 bytes; bytes 0..HighKeyLen valid)
const SizeOfBTPageOpaque = 272

// MaxHighKeyLen bounds the on-disk HighKey field. v4 widened this
// from 32 to 256 bytes so text/varchar B-tree keys (which can be
// longer than int4=4 or NUMERIC≤25) are stored without truncation.
// Keys longer than 256 bytes are not supported by the bulk-loader or
// split path and will return an error; in practice all regress-test
// and TPC-H index keys fit comfortably within this bound.
const MaxHighKeyLen = 256

// btSpecialOffset is where the opaque area starts on every B-tree page.
const btSpecialOffset = storage.BlockSize - SizeOfBTPageOpaque

// PageFlag values for btpo_flags.
const (
	BTLeaf       uint16 = 0x0001
	BTRoot       uint16 = 0x0002
	BTDeleted    uint16 = 0x0004
	BTHasHighKey uint16 = 0x0008
	// BTIncompleteSplit (M0055-0004 Phase C) marks a page as the
	// LEFT half of a freshly-completed split whose parent
	// downlink has not yet been inserted. Writers descending to
	// such a page MUST run `(*BTree).finishSplit` before
	// inserting further. Cleared by finishSplit after the parent
	// downlink lands.
	BTIncompleteSplit uint16 = 0x0010
	// BTHalfDead (M0055-0005-followup-two-phase-del) marks a
	// page as the target of a two-phase deletion whose Phase 1
	// (mark + WAL) has completed but whose Phase 2 (unlink +
	// recycle) has not. Vacuum and writer paths that encounter
	// a half-dead page MUST run `(*BTree).completeDeletion` to
	// finish Phase 2 before treating the page as live or
	// recycled. Crash-replay restores the half-dead state from
	// the Phase 1 WAL record.
	BTHalfDead uint16 = 0x0020
	// BTHasGarbage (C3, PG BTP_HAS_GARBAGE) hints that the page carries at
	// least one ItemIDDead line pointer set by the on-access kill pass.
	// Purely advisory: stale-set is harmless (the purge scan finds no Dead
	// items and clears it); cleared by the pre-split purge and by VACUUM.
	BTHasGarbage uint16 = 0x0040
)

// MetaBlock is always block 0 — the metapage. RootStart is the first
// block holding actual key data; on a freshly created tree it is also
// block 1 (root + leaf).
const (
	MetaBlock storage.BlockNumber = 0
	rootStart storage.BlockNumber = 1

	btreeMagic   uint32 = 0x053162
	btreeVersion uint32 = 4 // bumped by M0011-0002 (v3: variable-length HighKey, 48-byte opaque); v4: widened HighKey field to 256 bytes for text keys (272-byte opaque)

	// BTreeMagic and BTreeVersion expose the on-disk metapage magic and
	// version for out-of-package readers that validate a metapage without
	// opening the tree — notably the amcheck verify engine
	// (internal/amcheck). Keeping a single exported source of truth prevents
	// the magic/version from drifting between the writer (writeMeta) and any
	// independent validator.
	BTreeMagic   = btreeMagic
	BTreeVersion = btreeVersion
)

// BTreeMeta is the v0 metapage payload.
type BTreeMeta struct {
	Magic     uint32
	Version   uint32
	Root      storage.BlockNumber
	Level     uint32
	FastRoot  storage.BlockNumber
	FastLevel uint32
}

// BTPageOpaque is the typed view over the special area at the end of
// each B-tree page. HighKey is meaningful only when BTHasHighKey is
// set in Flags; from v3 onwards it carries a variable-length key
// bounded by MaxHighKeyLen so NUMERIC and other variable-width
// encodings fit (M0011-0002).
type BTPageOpaque struct {
	Prev    storage.BlockNumber
	Next    storage.BlockNumber
	Level   uint32
	Flags   uint16
	HighKey []byte
}

// IsLeaf reports whether the page carries leaf items.
func (o BTPageOpaque) IsLeaf() bool { return o.Flags&BTLeaf != 0 }

// HasIncompleteSplit reports whether this page is the left half
// of a split that has not yet propagated its parent downlink.
// (M0055-0004 Phase C.)
func (o BTPageOpaque) HasIncompleteSplit() bool { return o.Flags&BTIncompleteSplit != 0 }

// IsHalfDead reports whether this page is mid-deletion — Phase 1
// has marked it but Phase 2 has not unlinked it yet.
// (M0055-0005-followup-two-phase-del.)
func (o BTPageOpaque) IsHalfDead() bool { return o.Flags&BTHalfDead != 0 }

// IsRoot reports whether the page is the current root.
func (o BTPageOpaque) IsRoot() bool { return o.Flags&BTRoot != 0 }

// IsDeleted reports whether this page has been fully deleted (unlinked from
// the tree, awaiting recycle). Deleted pages carry no items and may type-pun
// fields such as the level, so structural validators must exempt them.
func (o BTPageOpaque) IsDeleted() bool { return o.Flags&BTDeleted != 0 }

// HasHighKey reports whether this page advertises a high-key
// boundary. Pages without a high key are rightmost on their level
// (or freshly created) and cover all remaining keys.
func (o BTPageOpaque) HasHighKey() bool { return o.Flags&BTHasHighKey != 0 }

// HasGarbage reports the BTHasGarbage hint (C3: at least one ItemIDDead
// line pointer may be present).
func (o BTPageOpaque) HasGarbage() bool { return o.Flags&BTHasGarbage != 0 }

// ParseOpaque exposes the page-bytes → BTPageOpaque decode for out-of-package
// readers (notably the amcheck verify engine, internal/amcheck) so they share
// this package's single definition of the opaque layout rather than
// re-implementing it — the opaque format has changed across versions (v3 grew
// it for variable-length HighKeys, v4 widened that field), and a duplicated
// decoder would silently drift on the next bump.
func ParseOpaque(p storage.Page) BTPageOpaque { return readOpaque(p) }

// readOpaque returns the parsed opaque from page bytes.
func readOpaque(p storage.Page) BTPageOpaque {
	off := btSpecialOffset
	o := BTPageOpaque{
		Prev:  storage.BlockNumber(binary.LittleEndian.Uint32(p[off : off+4])),
		Next:  storage.BlockNumber(binary.LittleEndian.Uint32(p[off+4 : off+8])),
		Level: binary.LittleEndian.Uint32(p[off+8 : off+12]),
		Flags: binary.LittleEndian.Uint16(p[off+12 : off+14]),
	}
	hkLen := int(binary.LittleEndian.Uint16(p[off+14 : off+16]))
	if hkLen > MaxHighKeyLen {
		hkLen = MaxHighKeyLen // defensive: truncate corrupt length to bounded slice
	}
	if hkLen > 0 {
		o.HighKey = append([]byte(nil), p[off+16:off+16+hkLen]...)
	}
	return o
}

// writeOpaque persists the opaque into page bytes. Panics if HighKey
// exceeds MaxHighKeyLen — by construction (int4=4, NUMERIC≤25) this
// can only fire for a future encoding that grew past the format
// budget.
func writeOpaque(p storage.Page, o BTPageOpaque) {
	if len(o.HighKey) > MaxHighKeyLen {
		panic(fmt.Sprintf("btree: HighKey length %d exceeds MaxHighKeyLen %d", len(o.HighKey), MaxHighKeyLen))
	}
	off := btSpecialOffset
	binary.LittleEndian.PutUint32(p[off:off+4], uint32(o.Prev))
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(o.Next))
	binary.LittleEndian.PutUint32(p[off+8:off+12], o.Level)
	binary.LittleEndian.PutUint16(p[off+12:off+14], o.Flags)
	binary.LittleEndian.PutUint16(p[off+14:off+16], uint16(len(o.HighKey)))
	// Zero the HighKey region first so leftover bytes from a longer
	// previous HighKey don't survive a shorter rewrite.
	for i := 0; i < MaxHighKeyLen; i++ {
		p[off+16+i] = 0
	}
	copy(p[off+16:off+16+len(o.HighKey)], o.HighKey)
}

// keyExceedsHighKey reports whether `key` is strictly greater than
// `op.HighKey` — i.e., the search has overshot this page and must
// follow op.Next under right-link recovery.
func keyExceedsHighKey(op BTPageOpaque, key []byte) bool {
	if !op.HasHighKey() || op.Next == storage.InvalidBlockNumber {
		return false
	}
	return CompareKeys(key, op.HighKey) > 0
}

// itemOvershootsHighKey reports whether a new item keyed `key` belongs
// strictly to the right of the page described by `op` rather than on it —
// the Lehman-Yao "move right" test applied to a structural INSERT rather
// than a search descent. It differs from keyExceedsHighKey in the
// boundary case: a search key equal to HighKey may legitimately stay on
// this page (HighKey is inclusive for routing), but a stored ITEM equal
// to HighKey is only valid on a leaf (leaf: key<=HighKey); on an internal
// page it must move right (internal: key<HighKey), matching amcheck's
// stored-item invariant in VerifyBtreeItemOrder (verify_nbtree.go) —
// HighKey is itself the separator that was pushed up when this page last
// split, so a downlink equal to it belongs to the right sibling.
func itemOvershootsHighKey(op BTPageOpaque, key []byte) bool {
	if !op.HasHighKey() || op.Next == storage.InvalidBlockNumber {
		return false
	}
	cmp := CompareKeys(key, op.HighKey)
	if op.IsLeaf() {
		return cmp > 0
	}
	return cmp >= 0
}

// initPage prepares a freshly extended block as a B-tree page with the
// given opaque content. The page starts empty (no items).
func initPage(p storage.Page, o BTPageOpaque) {
	if err := storage.InitPage(p); err != nil {
		panic(err)
	}
	h := storage.MustHeader(p)
	h.SetSpecial(uint16(btSpecialOffset))
	h.SetUpper(uint16(btSpecialOffset))
	writeOpaque(p, o)
}

// item is one B-tree item: a key plus the heap or child pointer.
//
// On internal pages the pointer's Block is the child page; on leaf
// pages the pointer is the heap row (block, line-pointer-slot).
type item struct {
	keyLen uint16
	ptr    storage.ItemPointer
	key    []byte
}

// itemPrefixSize is the fixed bytes before key_bytes.
const itemPrefixSize = 2 + 4 + 2

// MaxItemsPerPage is the goopg analogue of upstream's MaxIndexTuplesPerPage
// (postgres/src/include/access/itup.h): an upper bound on how many line-pointer
// items can physically fit on one B-tree page. amcheck's page-structural check
// (amcheck.VerifyBtreePage) flags any page whose line-pointer count exceeds this
// bound, mirroring palloc_btree_page's `maxoffset > MaxIndexTuplesPerPage` test
// (postgres/contrib/amcheck/verify_nbtree.c:3397).
//
// The divisor is goopg's minimum per-item footprint: a 4-byte line pointer (the
// itemIDSize that pageHasSpaceFor reserves) plus the smallest possible item
// body. The smallest body is a bare itemPrefix with a zero-length key — an
// internal page's negative-infinity downlink. goopg stores items unaligned
// (pageHasSpaceFor reserves exactly itemIDSize+itemPrefixSize+len(key)), so
// there is no MAXALIGN term, unlike upstream's MAXALIGN(sizeof(IndexTupleData)+1).
// Like upstream the bound is deliberately conservative: it ignores the per-page
// special (opaque) area, so the true maximum is a little lower — that headroom is
// exactly what keeps the corruption check free of false positives. Defined here,
// alongside itemPrefixSize, so the tuple-size accounting has a single source of
// truth (the engine never re-derives the inline item layout).
const MaxItemsPerPage = (storage.BlockSize - storage.SizeOfPageHeaderData) / (4 + itemPrefixSize)

func (it item) marshal() []byte {
	out := make([]byte, itemPrefixSize+len(it.key))
	binary.LittleEndian.PutUint16(out[0:2], it.keyLen)
	binary.LittleEndian.PutUint32(out[2:6], uint32(it.ptr.Block))
	binary.LittleEndian.PutUint16(out[6:8], it.ptr.Offset)
	copy(out[itemPrefixSize:], it.key)
	return out
}

func parseItem(raw []byte) (item, error) {
	if len(raw) < itemPrefixSize {
		return item{}, fmt.Errorf("btree: item too short (%d bytes)", len(raw))
	}
	keyLen := binary.LittleEndian.Uint16(raw[0:2])
	if int(keyLen)+itemPrefixSize != len(raw) {
		return item{}, fmt.Errorf("btree: item length mismatch keyLen=%d total=%d", keyLen, len(raw))
	}
	return item{
		keyLen: keyLen,
		ptr: storage.ItemPointer{
			Block:  storage.BlockNumber(binary.LittleEndian.Uint32(raw[2:6])),
			Offset: binary.LittleEndian.Uint16(raw[6:8]),
		},
		key: append([]byte(nil), raw[itemPrefixSize:]...),
	}, nil
}

// parseItemNoCopy returns an `item` whose `key` field ALIASES `raw`
// (no allocation). The caller MUST NOT retain key beyond the
// lifetime of raw. Used by RangeScan (M0091-0002) — its CAT-1
// callers don't retain key, so we can skip the per-slot
// allocation that the regular `parseItem` does.
func parseItemNoCopy(raw []byte) (item, error) {
	if len(raw) < itemPrefixSize {
		return item{}, fmt.Errorf("btree: item too short (%d bytes)", len(raw))
	}
	keyLen := binary.LittleEndian.Uint16(raw[0:2])
	if int(keyLen)+itemPrefixSize != len(raw) {
		return item{}, fmt.Errorf("btree: item length mismatch keyLen=%d total=%d", keyLen, len(raw))
	}
	return item{
		keyLen: keyLen,
		ptr: storage.ItemPointer{
			Block:  storage.BlockNumber(binary.LittleEndian.Uint32(raw[2:6])),
			Offset: binary.LittleEndian.Uint16(raw[6:8]),
		},
		key: raw[itemPrefixSize:], // alias — caller must not retain
	}, nil
}

// EncodeInt4 is the canonical key encoding for v0 (4-byte big-endian int32).
// Big-endian preserves numeric order as bytewise lexicographic order.
func EncodeInt4(key int32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(key)^0x80000000)
	return b[:]
}

// DecodeInt4 inverts EncodeInt4.
func DecodeInt4(b []byte) (int32, error) {
	if len(b) != 4 {
		return 0, fmt.Errorf("btree: int4 key must be 4 bytes, got %d", len(b))
	}
	return int32(binary.BigEndian.Uint32(b) ^ 0x80000000), nil
}

// EncodeInt8 encodes an int64 into a sortable byte representation.
// Uses big-endian with the sign bit flipped so that negative values
// sort before positive values, matching the same approach as EncodeInt4.
func EncodeInt8(key int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(key)^0x8000000000000000)
	return b[:]
}

// DecodeInt8 inverts EncodeInt8.
func DecodeInt8(b []byte) (int64, error) {
	if len(b) != 8 {
		return 0, fmt.Errorf("btree: int8 key must be 8 bytes, got %d", len(b))
	}
	return int64(binary.BigEndian.Uint64(b) ^ 0x8000000000000000), nil
}

// EncodeNumericKey encodes a NUMERIC value (mantissa * 10^(-scale)) into
// a sortable byte string such that bytewise comparison matches numeric
// order. The encoding is scale-invariant: numerically equal inputs
// (e.g. (10,1) and (100,2), both representing 1.0) produce identical
// bytes — required for UNIQUE/PRIMARY KEY equality on NUMERIC.
//
// Layout, semantics, and worked examples are documented in
// docs/design/0011-0001-btree-numeric-key-ordering.md. Briefly:
//
//	zero          -> [0x01]
//	non-zero      -> sign(1) || biased exp(4 BE) || digits || terminator(1)
//	positive sign -> 0x02; exp = E + 0x80000000;       digits = '0'+d;       term = 0x00
//	negative sign -> 0x00; exp = 0x7FFFFFFF - E;       digits = '0'+(9-d);   term = 0xFF
//
// Where d_1.d_2…d_n × 10^E is the value's scientific normalisation
// after stripping trailing zeros from the mantissa.
//
// Decoding is intentionally not provided: the B-tree never inverts the
// encoding; index probes always re-encode from the live datum.
//
// M0041-0004 widens the mantissa parameter from int64 to *big.Int so
// arbitrary-precision NUMERIC values (e.g. results of TPC-H Q8's
// `1.00000000000000000000` produced by upstream-compatible division)
// can be indexed without overflow. The on-page byte layout is
// unchanged — sort order is preserved by the variable-length
// digit-rebase trick, and any value that fits in int64 produces the
// same bytes as before the widening.
func EncodeNumericKey(mantissa *big.Int, scale int16) []byte {
	if mantissa.Sign() == 0 {
		return []byte{0x01}
	}
	negative := mantissa.Sign() < 0
	abs := new(big.Int).Abs(mantissa)
	// Strip trailing zeros so numerically-equal values normalise to
	// the same digit string.
	s := int32(scale)
	ten := big.NewInt(10)
	zero := big.NewInt(0)
	rem := new(big.Int)
	for {
		new(big.Int).QuoRem(abs, ten, rem)
		if rem.Cmp(zero) != 0 {
			break
		}
		abs.Quo(abs, ten)
		s--
		if abs.Sign() == 0 {
			break
		}
	}
	digits := abs.Text(10)
	ndig := int32(len(digits))
	E := ndig - 1 - s

	out := make([]byte, 0, 6+int(ndig))
	var b [4]byte
	if negative {
		out = append(out, 0x00)
		// Inverted bias so a larger E (more negative value) sorts smaller.
		binary.BigEndian.PutUint32(b[:], uint32(int32(0x7FFFFFFF)-E))
		out = append(out, b[:]...)
		for i := 0; i < int(ndig); i++ {
			d := digits[i] - '0'
			out = append(out, '0'+(9-d))
		}
		out = append(out, 0xFF) // greater than any digit byte; longer sorts first
	} else {
		out = append(out, 0x02)
		// Standard bias so a larger E sorts larger.
		binary.BigEndian.PutUint32(b[:], uint32(E)+0x80000000)
		out = append(out, b[:]...)
		for i := 0; i < int(ndig); i++ {
			out = append(out, digits[i])
		}
		out = append(out, 0x00) // less than any digit byte; shorter sorts first
	}
	return out
}

// EncodeVarchar encodes a variable-length string as a self-terminating
// byte sequence whose bytewise lexicographic order matches the C-locale
// (byte-wise) SQL ordering of the original strings. Suitable as a
// B-tree key component in both single-column and composite indexes.
//
// Encoding rules:
//   - 0x01 is the escape introducer.
//   - Each 0x00 byte in the payload is replaced by [0x01, 0x01].
//   - Each 0x01 byte in the payload is replaced by [0x01, 0x02].
//   - All other bytes are passed through unchanged.
//   - A single 0x00 byte is appended as the end-of-key terminator.
//
// This ensures 0x00 appears only as the terminator (never inside the
// payload after escaping), making the encoding self-terminating:
// concatenating two EncodeVarchar results in a composite key whose
// bytewise comparison correctly implements multi-column SQL ordering.
//
// Bytewise order of encoded strings matches bytewise (C-locale) order
// of the original inputs. Decoding is intentionally not provided:
// B-tree probe paths always re-encode from the live Datum.
func EncodeVarchar(payload []byte) []byte {
	out := make([]byte, 0, len(payload)+1)
	for _, b := range payload {
		switch b {
		case 0x00:
			out = append(out, 0x01, 0x01)
		case 0x01:
			out = append(out, 0x01, 0x02)
		default:
			out = append(out, b)
		}
	}
	out = append(out, 0x00)
	return out
}

// EncodeChar encodes a CHAR(N) (blank-padded character) value as a
// sortable, self-terminating byte sequence matching PostgreSQL's
// blank-padded comparison semantics.
//
// Trailing 0x20 (space) bytes are stripped before encoding, so
// 'A' and 'A         ' (any number of trailing spaces) produce
// identical bytes — required for index correctness when goopg stores
// the declared-length-padded form in the heap but the query probes
// with an unpadded literal.
//
// After trimming, the encoding is identical to EncodeVarchar.
func EncodeChar(payload []byte) []byte {
	return EncodeVarchar(bytes.TrimRight(payload, " "))
}

// EncodeTimestamp encodes a timestamp value as a sortable 8-byte
// sequence whose bytewise comparison matches chronological order.
//
// The input is the number of microseconds since the PostgreSQL epoch
// (2000-01-01 00:00:00 UTC); negative values are valid and represent
// timestamps before the epoch.
//
// The layout is identical to EncodeInt8: 8-byte big-endian with the
// sign bit flipped so negative values (pre-epoch timestamps) sort
// before positive values (post-epoch timestamps) under bytewise
// comparison.
func EncodeTimestamp(microsSince2000 int64) []byte {
	return EncodeInt8(microsSince2000)
}

// DecodeTimestamp inverts EncodeTimestamp (identical to DecodeInt8).
func DecodeTimestamp(b []byte) (int64, error) { return DecodeInt8(b) }

// EncodeFloat8 encodes a float64 into a sortable 8-byte sequence.
// IEEE 754 bit layout: flip sign bit for positives (so positives > negatives
// under unsigned comparison), flip all bits for negatives (so more-negative
// values sort smaller). NaN sorts above +Inf, which is acceptable.
func EncodeFloat8(key float64) []byte {
	bits := math.Float64bits(key)
	if bits>>63 != 0 {
		bits = ^bits
	} else {
		bits ^= 0x8000000000000000
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], bits)
	return b[:]
}

// DecodeFloat8 inverts EncodeFloat8.
func DecodeFloat8(b []byte) (float64, error) {
	if len(b) < 8 {
		return 0, fmt.Errorf("btree: float8 key truncated")
	}
	bits := binary.BigEndian.Uint64(b[:8])
	if bits>>63 != 0 {
		bits ^= 0x8000000000000000 // was positive float: undo sign-bit flip
	} else {
		bits = ^bits // was negative float: undo all-bits flip
	}
	return math.Float64frombits(bits), nil
}

// DecodeVarcharLen is like DecodeVarchar but also returns the number of bytes
// consumed from b (including the 0x00 terminator). Used by the multi-column
// index-only scan decoder to advance the key offset after variable-length fields.
func DecodeVarcharLen(b []byte) ([]byte, int, error) {
	// Find the unescaped 0x00 terminator.
	n := 0
	for i := 0; i < len(b); {
		if b[i] == 0x01 {
			i += 2
			continue
		}
		if b[i] == 0x00 {
			n = i + 1
			break
		}
		i++
	}
	if n == 0 {
		return nil, 0, fmt.Errorf("btree: varchar key missing 0x00 terminator")
	}
	decoded, err := DecodeVarchar(b[:n])
	return decoded, n, err
}

// DecodeVarchar inverts EncodeVarchar: strips the 0x00 terminator and
// unescapes 0x01-prefixed bytes. Used by index-only scan (M0046-0004)
// to reconstruct string values from B-tree key bytes without a heap fetch.
func DecodeVarchar(b []byte) ([]byte, error) {
	if len(b) == 0 || b[len(b)-1] != 0x00 {
		return nil, fmt.Errorf("btree: varchar key missing 0x00 terminator")
	}
	out := make([]byte, 0, len(b)-1)
	src := b[:len(b)-1] // strip terminator
	for i := 0; i < len(src); {
		c := src[i]
		if c == 0x01 {
			if i+1 >= len(src) {
				return nil, fmt.Errorf("btree: varchar key: truncated escape at byte %d", i)
			}
			switch src[i+1] {
			case 0x01:
				out = append(out, 0x00)
			case 0x02:
				out = append(out, 0x01)
			default:
				return nil, fmt.Errorf("btree: varchar key: invalid escape %02x at byte %d", src[i+1], i)
			}
			i += 2
		} else {
			out = append(out, c)
			i++
		}
	}
	return out, nil
}

// CompareKeys is straight bytewise lexicographic comparison.
//
// For int4 keys (all 4 bytes) this matches numeric order by
// construction of EncodeInt4. For NUMERIC keys (variable length)
// it matches numeric order via the sign + biased-exponent + digits
// + terminator layout produced by EncodeNumericKey — see
// docs/design/0011-0001-btree-numeric-key-ordering.md for why
// straight lex compare (not length-first) is the right contract
// for variable-length keys.
func CompareKeys(a, b []byte) int {
	return bytes.Compare(a, b)
}

// BTree is the access-method handle for one index relation.
//
// Concurrency (Landing 2 of milestone 0002 — see
// docs/design/0002-0002-btree-concurrency.md):
//
//   - Readers (Search, RangeScan) take no tree-wide lock; they
//     synchronise through per-page Slot.RLock() latches and use
//     Lehman-Yao right-link recovery to recover from concurrent
//     splits.
//   - Inserts take one of two paths:
//   - fast path: non-splitting leaf inserts run under the target
//     page's Slot.Lock only, so writers on different pages proceed
//     in parallel.
//   - split path: page splits (and their parent/root/meta updates)
//     are serialised through splitMu to keep tree-structure changes
//     atomic while Landing 3's full writer-coupling protocol is
//     still staged.
type BTree struct {
	pool     *storage.Pool
	rel      storage.RelFileNode
	logSplit LogSplitFunc

	// splitMu serialises split-path inserts and metapage
	// publication. (M0055-0004-followup-stage2-splitmu-removal:
	// experimentally removed splitMu from the slow path; the
	// existing buffer-pool eviction protocol made concurrent
	// pin/unpin paths surface "unpin underflow" panics under
	// 32-writer stress. The Stage 2 work needs an additional
	// reader/writer interaction-safety guarantee in the
	// buffer pool itself before the structural mutex can be
	// fully retired. The protocol-level INCOMPLETE_SPLIT
	// lifecycle from `(*BTree).finishSplit` runs unchanged;
	// this commit lands the race-safe createNewRoot half of
	// Stage 2 — concurrent root-lifts no longer orphan a
	// separator, even when both writers see the same OLD
	// root.)
	splitMu sync.Mutex

	// M0055-0001: write-path counters used by the baseline
	// harness and Phase A/B regression tests. Backed by per-P
	// sharded counters (M0107-0008 loop 7) so the Insert/Split
	// hot path does not contend on a single cache line; Stats
	// reads them via Counter.Sum, ResetStats via Counter.Reset.
	stats btreeStatsCounters

	// M0055-0002-followup-rightmost-cache: cached rightmost leaf
	// pointer for append-shaped workloads. Written by the insert
	// path's slow-path descent; read by the fast path to skip
	// re-descent when the next insert's key is ≥ the highest key
	// observed at the cache's leaf. atomic so concurrent readers
	// don't tear the value. Zero (BlockNumber 0) means "uncached"
	// — block 0 is the metapage so no real leaf has block 0.
	rightmostLeafBlk atomic.Uint64

	// M0055-0005 Phase D: recycled-page free list. Pages that
	// were unlinked by vacuum get pushed here; future
	// allocations (PinNew calls) check this list first before
	// extending the file. Guarded by `freeListMu` to keep the
	// pop/push atomic.
	freeListMu sync.Mutex
	freeList   []storage.BlockNumber

	// Test-only fault injection for the stranded-latch guard: fires inside
	// insertIntoBlock's leaf-write window, where insertItemSorted really
	// panicked. See latch_release.go.
	panicBeforeLeafWrite func(storage.BlockNumber) // test-only fault injection

	// DebugTraceInserts (M-NIGHTLY AI-20260708-064334-001 investigation
	// aid, 5th loop): when true, every insertItemSorted call anywhere in
	// this BTree (fast-path, cached-rightmost, split-left-refill,
	// split-right-fill, dedup-recovery-rebuild) appends a record to
	// insertLog: which block the item physically landed on, at which
	// 0-based line-pointer slot, with the item's key/TID. Off by default,
	// a single bool check when unset — same zero-cost-when-off pattern as
	// storage.Pool.DebugTraceSlotEvents. Lets a caller cross-reference
	// "was this (key,TID) ever actually written, and where" against the
	// final on-disk leaf walk to localize a lost entry to an exact
	// (block, slot) instead of only knowing it vanished somewhere.
	DebugTraceInserts bool
	insertLogMu       sync.Mutex
	insertLog         []btreeInsertLogEvent
	// rewriteLog records insertIntoBlock's page-rewrite checkpoints (see
	// RewriteLogEvent); guarded by insertLogMu alongside insertLog.
	rewriteLog []RewriteLogEvent
	// DebugVerifyFastPathInserts (M-NIGHTLY AI-20260708-064334-001
	// investigation aid, 7th loop): when true, every single-item fast-path
	// insertItemSorted call (tryInsertNoSplit, insertIntoBlock's own
	// no-split branch, tryInsertOnCachedRightmost -- deliberately NOT the
	// split/dedup-rewrite path's insertItemSorted calls, which resetPageItems
	// first and are already covered by RewriteLogEvent) snapshots
	// pageItems() immediately before and after the call and verifies every
	// pre-existing (key,TID) pair is still present afterward -- a plain
	// insert only ever adds a line pointer, so any pre-existing entry that
	// vanishes is captured as a FastPathViolation with the exact call site,
	// block, and the item that was inserted alongside it. Off by default,
	// zero cost when unset -- same pattern as DebugTraceInserts.
	DebugVerifyFastPathInserts bool
	fastPathViolations         []FastPathViolation
	// DebugTraceFlushes (M-NIGHTLY AI-20260708-064334-001 investigation
	// aid, 8th loop): when true, arms RecordFlushSnapshot as the target for
	// storage.Pool.OnFlushSnapshot — every dirty-page flush of a block
	// belonging to this BTree's relation (evictVictim's wasDirty branch,
	// via flushSlot) is decoded with pageItems() and appended to flushLog,
	// sharing insertLog/rewriteLog's Seq counter. The 7th loop isolated the
	// loss window to an unpin/re-pin gap on the same block — a flush is the
	// only thing that touches a page's bytes while nobody holds a pin on
	// it, so this lets a caller directly compare "what a flush wrote to
	// disk" against the last known-good insert/rewrite/fast-path snapshot
	// for the same block. Off by default, zero cost when unset — same
	// pattern as DebugTraceInserts.
	DebugTraceFlushes bool
	flushLog          []FlushSnapshotEvent
	// DebugTraceReloads (M-NIGHTLY AI-20260708-064334-001 investigation
	// aid, 9th loop): when true, arms RecordReloadSnapshot as the target
	// for storage.Pool.OnBlockReload — every disk reload of a block
	// belonging to this BTree's relation (pinLoad's cache-miss branch,
	// right after Manager.ReadBlock, before the slot is published for Pin)
	// is decoded with pageItems() and appended to reloadLog, sharing
	// insertLog/rewriteLog/flushLog's Seq counter. The 8th loop proved the
	// dirty-flush WRITE side always faithfully writes whatever bytes are
	// in memory (18/18), narrowing the loss window to the READ side: does
	// a reload ever serve stale or wrong bytes for a block that was just
	// correctly flushed? This lets a caller compare "what a reload
	// actually read" against the immediately preceding FlushSnapshotEvent
	// for the same block. Off by default, zero cost when unset — same
	// pattern as DebugTraceFlushes.
	DebugTraceReloads bool
	reloadLog         []ReloadSnapshotEvent
	// DebugTraceContentMu (M-NIGHTLY AI-20260708-064334-001 investigation
	// aid, 11th loop): when true, arms pinW/unpinW to snapshot pageItems()
	// for the block being locked, bracketing the FULL contentMu hold (the
	// "before" snapshot is taken right after s.Lock(), the "after"
	// snapshot right before s.Unlock()) rather than any one caller's
	// fast-path/split/dedup call. pinW/unpinW is every CALLER-SIDE
	// mutation's choke point, but NOT the only code that takes
	// storage.Slot.contentMu — storage.Pool.pinLoad (bufpool.go
	// ~1561-1572) independently Lock()s/Unlock()s the same mutex around
	// its own ReadBlock call during a cache-miss reload, so a hold
	// recorded here can show a page's content valid at Unlock and already
	// different at the very next traced Lock with nothing in between at
	// THIS layer — that gap is real and lives inside pinLoad's reload
	// hold, observable via DebugTraceReloads/OnBlockReload instead (see
	// the 11th loop's writeup in
	// TestVerifyBtreeEngineSilentOnRealConcurrentContended's skip message
	// for how this was used to narrow the loss window further). Shares
	// insertLog/rewriteLog/flushLog/reloadLog's Seq counter. Off by
	// default, zero cost when unset — same pattern as DebugTraceFlushes/
	// DebugTraceReloads.
	DebugTraceContentMu bool
	contentMuLog        []ContentMuEvent
	// contentMuBefore holds, per in-flight pinW hold, the pageItems()
	// snapshot taken right after acquiring the exclusive content latch, so
	// unpinW can pair it with a post-mutation snapshot into one
	// ContentMuEvent. Keyed by block number: safe without extra
	// synchronization beyond insertLogMu because storage.Slot.contentMu is
	// itself exclusive per block, so only one goroutine can hold a pinW on
	// a given block at a time — no two concurrent holds for the same key
	// can race this map.
	contentMuBefore map[storage.BlockNumber][]InsertLogRecord
	// DebugTraceBufmap (M-NIGHTLY AI-20260708-064334-001 investigation aid,
	// 14th loop): when true, arms RecordBufmapInsert/RecordBufmapDelete as
	// the target for storage.Pool.OnBufmapInsert/OnBufmapDelete — every
	// bufmap mutation (Insert success/failure, Delete) for a block
	// belonging to this BTree's relation is appended to bufmapLog, sharing
	// insertLog/rewriteLog/flushLog/reloadLog/contentMuLog's Seq counter.
	// Unlike those other logs, the hook fires synchronously INSIDE
	// storage.Pool.bmInsert/bmDelete while bufmap's own internal mu is
	// still held, so bufmapLog's recorded order is bufmap's TRUE
	// serialization order for the tag — not subject to the Seq-vs-real-
	// completion-order drift the 11th loop found in the flush/reload hooks
	// (those stamp Seq from a later, separately-locked call). This is the
	// only way, after 13 loops of hypothesis-refinement without ever
	// directly instrumenting bufmap itself, to prove or refute whether two
	// different slots simultaneously believe they own the same tag — scan
	// bufmapLog for a tag whose Insert(slotA) has no intervening Delete
	// before a LATER Insert(slotB) for the same tag succeeds. Off by
	// default, zero cost when unset — same pattern as DebugTraceFlushes/
	// DebugTraceReloads/DebugTraceContentMu.
	DebugTraceBufmap bool
	bufmapLog        []BufmapEvent
	// logSeqNext is a single monotonic counter shared by insertLog and
	// rewriteLog (instead of each using its own len()-based sequence) so a
	// caller can compare Seq values ACROSS the two logs to establish
	// temporal ordering — e.g. "did any rewrite event on this block happen
	// after this specific insert". Guarded by insertLogMu.
	logSeqNext uint64
}

// btreeInsertLogEvent is one recorded insertItemSorted call (see
// BTree.DebugTraceInserts).
type btreeInsertLogEvent struct {
	Seq     uint64
	Block   storage.BlockNumber
	LineIdx int // 0-based line-pointer index the item was inserted at
	Key     []byte
	Ptr     storage.ItemPointer
}

// traceInsert records one insertItemSorted call when DebugTraceInserts is
// enabled; a no-op otherwise.
func (bt *BTree) traceInsert(blk storage.BlockNumber, lineIdx int, it item) {
	if !bt.DebugTraceInserts {
		return
	}
	bt.insertLogMu.Lock()
	seq := bt.logSeqNext
	bt.logSeqNext++
	bt.insertLog = append(bt.insertLog, btreeInsertLogEvent{
		Seq:     seq,
		Block:   blk,
		LineIdx: lineIdx,
		Key:     append([]byte(nil), it.key...),
		Ptr:     it.ptr,
	})
	bt.insertLogMu.Unlock()
}

// FastPathViolation records a fast-path insertItemSorted call (see
// BTree.DebugVerifyFastPathInserts) after which a (key,TID) pair that was
// present on the page immediately BEFORE the call is no longer present
// immediately after it -- despite a fast-path insert only ever adding a
// line pointer, never removing one. M-NIGHTLY AI-20260708-064334-001, 7th
// loop: localizes a lost entry to the exact fast-path call that dropped
// it, complementing RewriteLogEvent's coverage of the split/dedup-rewrite
// path (already cleared -- see the 6th loop's update in
// verify_nbtree_realtree_test.go).
type FastPathViolation struct {
	Seq       uint64
	Block     storage.BlockNumber
	Site      string // "tryInsertNoSplit" | "insertIntoBlock-nosplit" | "tryInsertOnCachedRightmost"
	Inserted  InsertLogRecord
	PreCount  int
	PostCount int
	Missing   []InsertLogRecord
}

// fastPathItemIdent identifies an item by its (key,TID) pair for the
// pre/post survivor-set comparison in insertItemSortedVerified.
type fastPathItemIdent struct {
	key string
	ptr storage.ItemPointer
}

// insertItemSortedVerified wraps insertItemSorted with the pre/post
// pageItems()-snapshot check described by FastPathViolation. A no-op
// wrapper (falls straight through to insertItemSorted) when
// DebugVerifyFastPathInserts is false.
func (bt *BTree) insertItemSortedVerified(site string, blk storage.BlockNumber, p storage.Page, it item) (int, error) {
	if !bt.DebugVerifyFastPathInserts {
		return insertItemSorted(p, it)
	}
	pre, err := pageItems(p)
	if err != nil {
		panic(err)
	}
	preSnap := append([]item(nil), pre...)
	lineIdx, err := insertItemSorted(p, it)
	if err != nil {
		return 0, err
	}
	post, err := pageItems(p)
	if err != nil {
		panic(err)
	}
	bt.checkFastPathSurvivors(site, blk, it, preSnap, post)
	return lineIdx, nil
}

// checkFastPathSurvivors is the pre/post survivor-set comparison behind
// insertItemSortedVerified, split out so it can be unit-tested and read
// independently of the pinning/locking around it.
func (bt *BTree) checkFastPathSurvivors(site string, blk storage.BlockNumber, it item, pre, post []item) {
	postSet := make(map[fastPathItemIdent]int, len(post))
	for _, x := range post {
		postSet[fastPathItemIdent{string(x.key), x.ptr}]++
	}
	var missing []InsertLogRecord
	for _, x := range pre {
		id := fastPathItemIdent{string(x.key), x.ptr}
		if postSet[id] > 0 {
			postSet[id]--
			continue
		}
		missing = append(missing, InsertLogRecord{Block: blk, Key: append([]byte(nil), x.key...), Ptr: x.ptr})
	}
	if len(missing) == 0 {
		return
	}
	bt.insertLogMu.Lock()
	defer bt.insertLogMu.Unlock()
	seq := bt.logSeqNext
	bt.logSeqNext++
	bt.fastPathViolations = append(bt.fastPathViolations, FastPathViolation{
		Seq:       seq,
		Block:     blk,
		Site:      site,
		Inserted:  InsertLogRecord{Seq: seq, Block: blk, Key: append([]byte(nil), it.key...), Ptr: it.ptr},
		PreCount:  len(pre),
		PostCount: len(post),
		Missing:   missing,
	})
}

// FastPathViolations returns every recorded FastPathViolation, in recorded
// order. Test/investigation helper for DebugVerifyFastPathInserts.
func (bt *BTree) FastPathViolations() []FastPathViolation {
	bt.insertLogMu.Lock()
	defer bt.insertLogMu.Unlock()
	return append([]FastPathViolation(nil), bt.fastPathViolations...)
}

// InsertLogRecord is one recorded insertItemSorted call, exported for
// investigation tooling outside this package. See BTree.DebugTraceInserts.
type InsertLogRecord struct {
	Seq     uint64
	Block   storage.BlockNumber
	LineIdx int
	Key     []byte
	Ptr     storage.ItemPointer
}

func (r InsertLogRecord) String() string {
	return fmt.Sprintf("seq=%d blk=%d lineIdx=%d key=%x ptr=%v", r.Seq, r.Block, r.LineIdx, r.Key, r.Ptr)
}

// InsertLogRecordsForTID returns every recorded insertItemSorted call whose
// item pointer matches tid, in recorded order. Test/investigation helper for
// DebugTraceInserts.
func (bt *BTree) InsertLogRecordsForTID(tid storage.ItemPointer) []InsertLogRecord {
	bt.insertLogMu.Lock()
	defer bt.insertLogMu.Unlock()
	var out []InsertLogRecord
	for _, e := range bt.insertLog {
		if e.Ptr == tid {
			out = append(out, InsertLogRecord{e.Seq, e.Block, e.LineIdx, e.Key, e.Ptr})
		}
	}
	return out
}

// InsertLogRecordsForBlockAfter returns every recorded insertItemSorted call
// that landed on blk with Seq >= afterSeq, in recorded order. Test/
// investigation helper for DebugTraceInserts: used to see whether a target
// block was later rewritten wholesale (a resetPageItems + reinsert-everything
// burst, the signature of a split/dedup-recovery redistribution) after a
// specific insert, and whether a specific TID's item survived that rewrite.
func (bt *BTree) InsertLogRecordsForBlockAfter(blk storage.BlockNumber, afterSeq uint64) []InsertLogRecord {
	bt.insertLogMu.Lock()
	defer bt.insertLogMu.Unlock()
	var out []InsertLogRecord
	for _, e := range bt.insertLog {
		if e.Block == blk && e.Seq >= afterSeq {
			out = append(out, InsertLogRecord{e.Seq, e.Block, e.LineIdx, e.Key, e.Ptr})
		}
	}
	return out
}

// RewriteLogEvent is one recorded insertIntoBlock page-rewrite event (the
// dedup-recovery no-split rebuild, or a genuine split): the full survivor set
// captured at the two checkpoints that matter for the M-NIGHTLY
// AI-20260708-064334-001 investigation — immediately after `pageItems()`
// (what the rewrite believes is currently on the page, plus the incoming
// item) and immediately after `dedupConsolidate()` (what it's about to write
// back). Comparing a specific (key,TID)'s presence across these two
// snapshots localizes a lost entry to either "pageItems undercounted a page
// that genuinely held it" or "dedupConsolidate dropped a non-duplicate".
// Gated by DebugTraceInserts, same zero-cost-when-off contract as insertLog.
type RewriteLogEvent struct {
	Seq             uint64
	Block           storage.BlockNumber
	Phase           string // "dedup-recovery" or "split"
	PreLineCount    int    // storage.PageLinePointerCount(slot.Page()) just before pageItems()
	PostPageItems   []InsertLogRecord
	PostDedup       []InsertLogRecord
}

func (bt *BTree) traceRewrite(blk storage.BlockNumber, phase string, preLineCount int, postPageItems, postDedup []item) {
	if !bt.DebugTraceInserts {
		return
	}
	toRecords := func(items []item) []InsertLogRecord {
		out := make([]InsertLogRecord, len(items))
		for i, it := range items {
			out[i] = InsertLogRecord{Block: blk, LineIdx: i, Key: append([]byte(nil), it.key...), Ptr: it.ptr}
		}
		return out
	}
	bt.insertLogMu.Lock()
	defer bt.insertLogMu.Unlock()
	seq := bt.logSeqNext
	bt.logSeqNext++
	bt.rewriteLog = append(bt.rewriteLog, RewriteLogEvent{
		Seq:           seq,
		Block:         blk,
		Phase:         phase,
		PreLineCount:  preLineCount,
		PostPageItems: toRecords(postPageItems),
		PostDedup:     toRecords(postDedup),
	})
}

// RewriteLogRecordsForBlock returns every recorded insertIntoBlock rewrite
// event (see RewriteLogEvent) for blk, in recorded order. Test/investigation
// helper for DebugTraceInserts.
func (bt *BTree) RewriteLogRecordsForBlock(blk storage.BlockNumber) []RewriteLogEvent {
	bt.insertLogMu.Lock()
	defer bt.insertLogMu.Unlock()
	var out []RewriteLogEvent
	for _, e := range bt.rewriteLog {
		if e.Block == blk {
			out = append(out, e)
		}
	}
	return out
}

// PresentIn reports whether tid appears among a RewriteLogEvent snapshot's
// recorded (key,TID) pairs. Investigation helper — avoids callers
// hand-rolling the same linear scan at every call site.
func RewriteSnapshotHasTID(recs []InsertLogRecord, tid storage.ItemPointer) bool {
	for _, r := range recs {
		if r.Ptr == tid {
			return true
		}
	}
	return false
}

// FlushSnapshotEvent records one dirty-page flush to disk: the leaf
// entries pageItems() decodes from the exact bytes flushSlot is about to
// write, keyed to bt's shared Seq counter (see DebugTraceFlushes) so a
// caller can compare temporal order against insertLog/rewriteLog/
// fastPathViolations for the same block.
type FlushSnapshotEvent struct {
	Seq   uint64
	Block storage.BlockNumber
	Items []InsertLogRecord
}

// RecordFlushSnapshot is the storage.Pool.OnFlushSnapshot hook this BTree
// wires when DebugTraceFlushes is set (e.g. `pool.OnFlushSnapshot =
// bt.RecordFlushSnapshot`): given the exact tag and bytes a dirty-page
// flush is about to write to disk, it decodes pageItems() and appends a
// FlushSnapshotEvent. A no-op when DebugTraceFlushes is false, tag
// belongs to a different relation, or pageItems() can't decode the page
// (e.g. the meta page) — matches the signature
// storage.Pool.OnFlushSnapshot expects.
func (bt *BTree) RecordFlushSnapshot(tag storage.BufferTag, page storage.Page) {
	if !bt.DebugTraceFlushes || tag.Rel != bt.rel {
		return
	}
	items, err := pageItems(page)
	if err != nil {
		return
	}
	recs := make([]InsertLogRecord, len(items))
	for i, it := range items {
		recs[i] = InsertLogRecord{Block: tag.Block, LineIdx: i, Key: append([]byte(nil), it.key...), Ptr: it.ptr}
	}
	bt.insertLogMu.Lock()
	seq := bt.logSeqNext
	bt.logSeqNext++
	bt.flushLog = append(bt.flushLog, FlushSnapshotEvent{Seq: seq, Block: tag.Block, Items: recs})
	bt.insertLogMu.Unlock()
}

// FlushSnapshotRecordsForBlock returns every recorded FlushSnapshotEvent
// (see DebugTraceFlushes) for blk, in recorded order. Test/investigation
// helper.
func (bt *BTree) FlushSnapshotRecordsForBlock(blk storage.BlockNumber) []FlushSnapshotEvent {
	bt.insertLogMu.Lock()
	defer bt.insertLogMu.Unlock()
	var out []FlushSnapshotEvent
	for _, e := range bt.flushLog {
		if e.Block == blk {
			out = append(out, e)
		}
	}
	return out
}

// ReloadSnapshotEvent records one disk reload of a block: the leaf
// entries pageItems() decodes from the exact bytes Manager.ReadBlock just
// served, keyed to bt's shared Seq counter (see DebugTraceReloads) so a
// caller can compare temporal order against flushLog for the same block.
type ReloadSnapshotEvent struct {
	Seq   uint64
	Block storage.BlockNumber
	Items []InsertLogRecord
}

// RecordReloadSnapshot is the storage.Pool.OnBlockReload hook this BTree
// wires when DebugTraceReloads is set (e.g. `pool.OnBlockReload =
// bt.RecordReloadSnapshot`): given the exact tag and bytes a disk reload
// just read, it decodes pageItems() and appends a ReloadSnapshotEvent. A
// no-op when DebugTraceReloads is false, tag belongs to a different
// relation, or pageItems() can't decode the page (e.g. the meta page) —
// matches the signature storage.Pool.OnBlockReload expects.
func (bt *BTree) RecordReloadSnapshot(tag storage.BufferTag, page storage.Page) {
	if !bt.DebugTraceReloads || tag.Rel != bt.rel {
		return
	}
	items, err := pageItems(page)
	if err != nil {
		return
	}
	recs := make([]InsertLogRecord, len(items))
	for i, it := range items {
		recs[i] = InsertLogRecord{Block: tag.Block, LineIdx: i, Key: append([]byte(nil), it.key...), Ptr: it.ptr}
	}
	bt.insertLogMu.Lock()
	seq := bt.logSeqNext
	bt.logSeqNext++
	bt.reloadLog = append(bt.reloadLog, ReloadSnapshotEvent{Seq: seq, Block: tag.Block, Items: recs})
	bt.insertLogMu.Unlock()
}

// ReloadSnapshotRecordsForBlock returns every recorded ReloadSnapshotEvent
// (see DebugTraceReloads) for blk, in recorded order. Test/investigation
// helper.
func (bt *BTree) ReloadSnapshotRecordsForBlock(blk storage.BlockNumber) []ReloadSnapshotEvent {
	bt.insertLogMu.Lock()
	defer bt.insertLogMu.Unlock()
	var out []ReloadSnapshotEvent
	for _, e := range bt.reloadLog {
		if e.Block == blk {
			out = append(out, e)
		}
	}
	return out
}

// BufmapEvent records one bufmap mutation for a block belonging to this
// BTree's relation: an Insert attempt (Ok reports whether it succeeded —
// false means some other slot already owned the tag) or a Delete, keyed to
// bt's shared Seq counter (see DebugTraceBufmap). Op is "insert" or
// "delete".
type BufmapEvent struct {
	Seq     uint64
	Block   storage.BlockNumber
	Op      string
	SlotIdx int32
	Gen     uint32
	Ok      bool
}

// RecordBufmapInsert is the storage.Pool.OnBufmapInsert hook this BTree
// wires when DebugTraceBufmap is set (e.g. `pool.OnBufmapInsert =
// bt.RecordBufmapInsert`). Fires synchronously inside storage.Pool.bmInsert
// while bufmap's own internal mu is still held, so bufmapLog's recorded
// order is bufmap's TRUE mutation order for the tag (see DebugTraceBufmap's
// doc comment for why this differs from the flush/reload hooks). A no-op
// when DebugTraceBufmap is false or tag belongs to a different relation.
func (bt *BTree) RecordBufmapInsert(tag storage.BufferTag, slotIdx int32, gen uint32, ok bool) {
	if !bt.DebugTraceBufmap || tag.Rel != bt.rel {
		return
	}
	bt.insertLogMu.Lock()
	seq := bt.logSeqNext
	bt.logSeqNext++
	bt.bufmapLog = append(bt.bufmapLog, BufmapEvent{Seq: seq, Block: tag.Block, Op: "insert", SlotIdx: slotIdx, Gen: gen, Ok: ok})
	bt.insertLogMu.Unlock()
}

// RecordBufmapDelete is the storage.Pool.OnBufmapDelete hook this BTree
// wires when DebugTraceBufmap is set (e.g. `pool.OnBufmapDelete =
// bt.RecordBufmapDelete`). See RecordBufmapInsert's doc comment. A no-op
// when DebugTraceBufmap is false or tag belongs to a different relation.
func (bt *BTree) RecordBufmapDelete(tag storage.BufferTag, slotIdx int32) {
	if !bt.DebugTraceBufmap || tag.Rel != bt.rel {
		return
	}
	bt.insertLogMu.Lock()
	seq := bt.logSeqNext
	bt.logSeqNext++
	bt.bufmapLog = append(bt.bufmapLog, BufmapEvent{Seq: seq, Block: tag.Block, Op: "delete", SlotIdx: slotIdx, Ok: true})
	bt.insertLogMu.Unlock()
}

// BufmapEventsForBlock returns every recorded BufmapEvent (see
// DebugTraceBufmap) for blk, in recorded (true bufmap-lock) order.
// Test/investigation helper.
func (bt *BTree) BufmapEventsForBlock(blk storage.BlockNumber) []BufmapEvent {
	bt.insertLogMu.Lock()
	defer bt.insertLogMu.Unlock()
	var out []BufmapEvent
	for _, e := range bt.bufmapLog {
		if e.Block == blk {
			out = append(out, e)
		}
	}
	return out
}

// CheckBufmapExclusivity scans every recorded BufmapEvent (see
// DebugTraceBufmap) for blk and reports the first point at which two
// DIFFERENT slot indices both hold a live (successful-Insert,
// no-matching-Delete-yet) ownership of the same block's tag at once — a
// direct, ground-truth violation of bufmap's single-owner invariant. Returns
// ok=true (no violation found) or ok=false plus the two conflicting events.
// Test/investigation helper.
func (bt *BTree) CheckBufmapExclusivity(blk storage.BlockNumber) (ok bool, first, second BufmapEvent) {
	events := bt.BufmapEventsForBlock(blk)
	live := map[int32]bool{}
	var liveEvent map[int32]BufmapEvent = map[int32]BufmapEvent{}
	for _, e := range events {
		switch e.Op {
		case "insert":
			if !e.Ok {
				continue
			}
			for slot, isLive := range live {
				if isLive && slot != e.SlotIdx {
					return false, liveEvent[slot], e
				}
			}
			live[e.SlotIdx] = true
			liveEvent[e.SlotIdx] = e
		case "delete":
			live[e.SlotIdx] = false
		}
	}
	return true, BufmapEvent{}, BufmapEvent{}
}

// ContentMuEvent records one full pinW..unpinW hold on a block: the
// pageItems() decoded right after the exclusive content latch was
// acquired (Before) and right before it was released (After), keyed to
// bt's shared Seq counter (see DebugTraceContentMu) so a caller can find
// the exact hold where a previously-present (key,TID) pair is in Before
// but missing from After.
type ContentMuEvent struct {
	Seq    uint64
	Block  storage.BlockNumber
	Before []InsertLogRecord
	After  []InsertLogRecord
}

// recordContentMuLock is called from pinW immediately after s.Lock(),
// while DebugTraceContentMu is armed — it snapshots pageItems() for the
// page about to be mutated and stashes it in contentMuBefore, keyed by
// block, for recordContentMuUnlock to pair with the post-mutation
// snapshot. A no-op when DebugTraceContentMu is false.
func (bt *BTree) recordContentMuLock(blk storage.BlockNumber, page storage.Page) {
	if !bt.DebugTraceContentMu {
		return
	}
	recs := snapshotPageItemsAsLog(blk, page)
	if recs == nil {
		return
	}
	bt.insertLogMu.Lock()
	if bt.contentMuBefore == nil {
		bt.contentMuBefore = make(map[storage.BlockNumber][]InsertLogRecord)
	}
	bt.contentMuBefore[blk] = recs
	bt.insertLogMu.Unlock()
}

// recordContentMuUnlock is called from unpinW immediately before
// s.Unlock(), while DebugTraceContentMu is armed — it snapshots
// pageItems() again, pairs it with the matching recordContentMuLock
// snapshot for the same block, and appends a ContentMuEvent. A no-op
// when DebugTraceContentMu is false or no matching "before" snapshot was
// recorded (e.g. pageItems() failed to decode at lock time).
func (bt *BTree) recordContentMuUnlock(blk storage.BlockNumber, page storage.Page) {
	if !bt.DebugTraceContentMu {
		return
	}
	recs := snapshotPageItemsAsLog(blk, page)
	bt.insertLogMu.Lock()
	before, ok := bt.contentMuBefore[blk]
	if ok {
		delete(bt.contentMuBefore, blk)
		seq := bt.logSeqNext
		bt.logSeqNext++
		bt.contentMuLog = append(bt.contentMuLog, ContentMuEvent{Seq: seq, Block: blk, Before: before, After: recs})
	}
	bt.insertLogMu.Unlock()
}

// snapshotPageItemsAsLog decodes pageItems() and converts it to the
// []InsertLogRecord shape shared by insertLog/rewriteLog/flushLog/
// reloadLog/contentMuLog so RewriteSnapshotHasTID and the other existing
// diagnostic helpers work unchanged against a ContentMuEvent. Returns nil
// (not an error) on decode failure, matching RecordFlushSnapshot/
// RecordReloadSnapshot's best-effort behavior — this is debug
// instrumentation, not a correctness path.
func snapshotPageItemsAsLog(blk storage.BlockNumber, page storage.Page) []InsertLogRecord {
	items, err := pageItems(page)
	if err != nil {
		return nil
	}
	recs := make([]InsertLogRecord, len(items))
	for i, it := range items {
		recs[i] = InsertLogRecord{Block: blk, LineIdx: i, Key: append([]byte(nil), it.key...), Ptr: it.ptr}
	}
	return recs
}

// ContentMuRecordsForBlock returns every recorded ContentMuEvent (see
// DebugTraceContentMu) for blk, in recorded order. Test/investigation
// helper.
func (bt *BTree) ContentMuRecordsForBlock(blk storage.BlockNumber) []ContentMuEvent {
	bt.insertLogMu.Lock()
	defer bt.insertLogMu.Unlock()
	var out []ContentMuEvent
	for _, e := range bt.contentMuLog {
		if e.Block == blk {
			out = append(out, e)
		}
	}
	return out
}

// pinNewOrRecycled (M0055-0005 Phase D) returns a writable slot
// for a fresh page allocation, preferring a recycled block from
// the free list before extending the file. The page bytes are
// re-initialised so the caller sees a clean page (matching the
// post-PinNew contract).
//
// The returned slot is ALWAYS still content-locked (Lock held, not
// Unlock'd) — the caller must populate the real opaque/header and
// MarkDirty, then Unlock itself. This closes a gap found while
// investigating the recurring nightly "btree: item length mismatch
// keyLen=9 total=37" corruption: the recycled-block branch used to
// zero the page and then Unlock before the caller re-Locked to
// stamp real content, leaving a window where any other writer
// racing to pinW this same (already-tagged, already-reachable-once-
// recycled-block-numbers-get-reused) block could observe and insert
// into the transitional all-zero page, which the split path then
// unconditionally overwrites via initPage — silently discarding that
// writer's insert and leaving the tree structurally inconsistent.
// Keeping the lock held end-to-end across both branches (recycled
// and fresh-PinNew) removes the window entirely.
func (bt *BTree) pinNewOrRecycled() (*storage.Slot, storage.BlockNumber, error) {
	if blk, ok := bt.popRecycledBlock(); ok {
		slot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: blk})
		if err != nil {
			// Could not re-pin; fall back to fresh allocation.
			return bt.pinNewLocked()
		}
		// Re-initialise the page bytes so the recycled slot looks
		// like a fresh PinNew result. The page must be zeroed under
		// the content lock so a concurrent reader never observes a
		// partially-zeroed page (M0118-0130: "btree: item length
		// mismatch keyLen=9 total=37"). The lock is intentionally
		// NOT released here — see the function doc.
		slot.Lock()
		page := slot.Page()
		for i := range page {
			page[i] = 0
		}
		// Caller will write opaque/header before MarkDirty + Unlock.
		return slot, blk, nil
	}
	return bt.pinNewLocked()
}

// pinNewLocked wraps storage.Pool.PinNew so its result matches
// pinNewOrRecycled's locked-return contract: Pool.PinNew itself
// returns an already-unlocked, already-publicly-pinnable slot (its
// tag is inserted into the buffer map before the caller ever sees
// it), so without this, a fresh (non-recycled) allocation would
// reopen the exact same populate-before-lock gap the recycled branch
// closes.
func (bt *BTree) pinNewLocked() (*storage.Slot, storage.BlockNumber, error) {
	slot, blk, err := bt.pool.PinNew(bt.rel)
	if err != nil {
		return nil, storage.InvalidBlockNumber, err
	}
	slot.Lock()
	return slot, blk, nil
}

// recycleBlock (M0055-0005 Phase D) marks a block as available
// for reuse by future page allocations on this tree. The block
// must have been unlinked from the tree's logical structure
// before it lands here. Safe for concurrent callers.
func (bt *BTree) recycleBlock(blk storage.BlockNumber) {
	bt.freeListMu.Lock()
	bt.freeList = append(bt.freeList, blk)
	bt.freeListMu.Unlock()
}

// popRecycledBlock (M0055-0005 Phase D) returns a recycled
// block number if one is available, or (0, false) when the free
// list is empty. The caller is responsible for re-initialising
// the block's page bytes before reuse.
func (bt *BTree) popRecycledBlock() (storage.BlockNumber, bool) {
	bt.freeListMu.Lock()
	defer bt.freeListMu.Unlock()
	if len(bt.freeList) == 0 {
		return 0, false
	}
	n := len(bt.freeList) - 1
	blk := bt.freeList[n]
	bt.freeList = bt.freeList[:n]
	return blk, true
}

// RecycledPageCount returns the number of pages currently in the
// recycle free list. Used by tests and the M0055-0007 report.
func (bt *BTree) RecycledPageCount() int {
	bt.freeListMu.Lock()
	defer bt.freeListMu.Unlock()
	return len(bt.freeList)
}

// BTreeStats are the per-tree counters surfaced by `(*BTree).Stats`
// for benchmarks and regression tests. The counters are best-effort
// (incremented from the steady-state insert/split path) and are
// cleared by `(*BTree).ResetStats`. (M0055-0001.)
type BTreeStats struct {
	Inserts uint64 // total `Insert` calls
	Splits  uint64 // total leaf+internal page splits
}

// btreeStatsCounters is the on-tree storage backing BTreeStats.
// Each counter is a per-P sharded stats.Counter so the Insert /
// split hot paths bump local shards without cross-core cache-line
// invalidation. (M0107-0008 loop 7.)
type btreeStatsCounters struct {
	// Backing storage for BTreeStats using plain atomics.
	// Per-P sharding via stats.Counter (M0107-0008 loop 7) was reverted
	// because btree.Open is called per-statement — each call allocates a
	// fresh BTree struct, so sharding provides zero cross-goroutine
	// contention benefit while growing sizeof(BTree) by 32 KiB. That
	// 32 KiB per open × thousands of opens per second exhausted WSL2's
	// virtual address space under pgbench SU workloads.
	inserts atomic.Uint64
	splits  atomic.Uint64
}

// Stats returns a snapshot of the BTree's write-path counters.
// Snapshot is best-effort — concurrent inserts may make the
// returned numbers stale by the time the caller reads them.
func (bt *BTree) Stats() BTreeStats {
	return BTreeStats{
		Inserts: bt.stats.inserts.Load(),
		Splits:  bt.stats.splits.Load(),
	}
}

// ResetStats clears the BTree's write-path counters.
func (bt *BTree) ResetStats() {
	bt.stats.inserts.Store(0)
	bt.stats.splits.Store(0)
}

// LogSplitFunc emits the atomic page-split WAL record described in
// docs/design/0002-0002-btree-concurrency.md Landing 3a and
// returns the record's end LSN. nil means "no WAL writer wired";
// the btree falls back to the per-page FPI emitted by
// Pool.MarkDirty, which is correct under the limited
// crash-consistency contract (split atomicity is best-effort
// without this hook).
//
// On a non-rightmost split the page that used to be left's right
// sibling is relinked (btpo_prev → rightBlk) under the same record;
// callers pass it as sibBlk with its post-relink image as sibPage.
// On a rightmost split sibBlk is storage.InvalidBlockNumber and
// sibPage is nil.
type LogSplitFunc func(rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page, sibBlk storage.BlockNumber, sibPage storage.Page) (storage.LSN, error)

// Options carries optional dependencies for Open/Create. The zero
// value works for tests and callers that don't need WAL-backed
// split atomicity.
type Options struct {
	// LogSplit, when non-nil, is invoked on every page split to
	// emit one atomic BtreeSplit WAL record covering both pages.
	LogSplit LogSplitFunc
	// CreateXID is the creating transaction's xid, stamped onto the
	// block-0 smgr-create WAL record when the index relfile is created
	// (A9). Zero for non-transactional/bootstrap builds.
	CreateXID storage.TransactionID
}

// Open returns a handle to an existing B-tree on rel. Validates the
// metapage; returns ErrNotABTree if the magic doesn't match.
//
// The split-WAL hook (Landing 3a) is pulled from
// `pool.LogBtreeSplit()`. Callers that need to override (tests,
// future independent WAL streams) can use OpenWithOptions.
func Open(pool *storage.Pool, rel storage.RelFileNode) (*BTree, error) {
	return OpenWithOptions(pool, rel, Options{LogSplit: adaptPoolLogSplit(pool)})
}

// OpenWithOptions is the wired-up Open variant.
func OpenWithOptions(pool *storage.Pool, rel storage.RelFileNode, opts Options) (*BTree, error) {
	bt := &BTree{pool: pool, rel: rel, logSplit: opts.LogSplit}
	meta, err := bt.readMeta()
	if err != nil {
		return nil, err
	}
	if meta.Magic != btreeMagic || meta.Version != btreeVersion {
		return nil, ErrNotABTree
	}
	return bt, nil
}

// Create initializes a new B-tree on rel. The relation must be empty.
// Allocates the metapage and the initial leaf-as-root page.
func Create(pool *storage.Pool, rel storage.RelFileNode) (*BTree, error) {
	return CreateWithOptions(pool, rel, Options{LogSplit: adaptPoolLogSplit(pool)})
}

// CreateWithXID is Create with the creating transaction's xid stamped onto the
// index relfile's smgr-create WAL record (A9). Callers in a DDL transaction
// (CREATE INDEX) pass ctx.Tx.XID.
func CreateWithXID(pool *storage.Pool, rel storage.RelFileNode, xid storage.TransactionID) (*BTree, error) {
	return CreateWithOptions(pool, rel, Options{LogSplit: adaptPoolLogSplit(pool), CreateXID: xid})
}

// adaptPoolLogSplit returns the pool's split-WAL hook in btree's
// LogSplitFunc shape, or nil when no hook is wired (tests etc.).
func adaptPoolLogSplit(pool *storage.Pool) LogSplitFunc {
	hook := pool.LogBtreeSplit()
	if hook == nil {
		return nil
	}
	return func(rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page, sibBlk storage.BlockNumber, sibPage storage.Page) (storage.LSN, error) {
		return hook(rel, leftBlk, rightBlk, leftPage, rightPage, sibBlk, sibPage)
	}
}

// CreateWithOptions is the wired-up Create variant.
func CreateWithOptions(pool *storage.Pool, rel storage.RelFileNode, opts Options) (*BTree, error) {
	bt := &BTree{pool: pool, rel: rel, logSplit: opts.LogSplit}

	// Ensure the relation file starts at block 0 (see
	// BulkCreateWithOptions for rationale).
	mgr := pool.Manager()
	if err := mgr.TruncateRelation(rel); err != nil {
		return nil, fmt.Errorf("btree: truncate relation: %w", err)
	}
	pool.InvalidateRel(rel)

	// Block 0: metapage. A9: this creates the index relfile — pass the
	// creating xid so its smgr-create WAL record is PG-faithful.
	metaSlot, metaBlk, err := pool.PinNewWithXID(rel, opts.CreateXID)
	if err != nil {
		return nil, err
	}
	if metaBlk != MetaBlock {
		pool.Unpin(metaSlot)
		return nil, fmt.Errorf("btree: expected meta at block 0, got %d", metaBlk)
	}

	// Block 1: root, also the only leaf.
	rootSlot, rootBlk, err := pool.PinNew(rel)
	if err != nil {
		pool.Unpin(metaSlot)
		return nil, err
	}
	if rootBlk != rootStart {
		pool.Unpin(metaSlot)
		pool.Unpin(rootSlot)
		return nil, fmt.Errorf("btree: expected root at block 1, got %d", rootBlk)
	}

	// Initialise the root page as a leaf.
	rootSlot.Lock()
	initPage(rootSlot.Page(), BTPageOpaque{
		Prev:  storage.InvalidBlockNumber,
		Next:  storage.InvalidBlockNumber,
		Level: 0,
		Flags: BTLeaf | BTRoot,
	})
	if err := bt.markDirtyWithPageRecord(rootSlot, rootBlk); err != nil {
		rootSlot.Unlock()
		pool.Unpin(rootSlot)
		pool.Unpin(metaSlot)
		return nil, err
	}
	rootSlot.Unlock()

	// Initialise the metapage.
	metaSlot.Lock()
	storage.InitPage(metaSlot.Page())
	writeMeta(metaSlot.Page(), BTreeMeta{
		Magic:     btreeMagic,
		Version:   btreeVersion,
		Root:      rootBlk,
		Level:     0,
		FastRoot:  rootBlk,
		FastLevel: 0,
	})
	if err := bt.markDirtyWithPageRecord(metaSlot, MetaBlock); err != nil {
		metaSlot.Unlock()
		pool.Unpin(rootSlot)
		pool.Unpin(metaSlot)
		return nil, err
	}
	metaSlot.Unlock()

	pool.Unpin(rootSlot)
	pool.Unpin(metaSlot)

	return bt, nil
}

// Errors.
var (
	ErrNotABTree = errors.New("btree: not a B-tree relation")
)

// readMeta loads the metapage under a shared content latch so the
// read sees a torn-byte-free snapshot.
func (bt *BTree) readMeta() (BTreeMeta, error) {
	slot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: MetaBlock})
	if err != nil {
		return BTreeMeta{}, err
	}
	slot.RLock()
	m := parseMeta(slot.Page())
	slot.RUnlock()
	bt.pool.Unpin(slot)
	return m, nil
}

// pinR pins a buffer and acquires its shared content latch. Callers
// release it with unpinR. Used on every read of internal/leaf pages
// during descent and scans.
func (bt *BTree) pinR(blk storage.BlockNumber) (*storage.Slot, error) {
	s, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: blk})
	if err != nil {
		return nil, err
	}
	s.RLock()
	return s, nil
}

func (bt *BTree) unpinR(s *storage.Slot) {
	s.RUnlock()
	bt.pool.Unpin(s)
}

// pinW pins a buffer and acquires its exclusive content latch.
// Used at every mutation site so concurrent readers (which take
// the shared latch) see a coherent page image. NOTE: this is every
// CALLER-side choke point for this latch, but storage.Pool.pinLoad also
// independently Lock()s/Unlock()s the same per-slot contentMu around its
// own ReadBlock call during a cache-miss reload (bufpool.go
// ~1561-1572) -- see DebugTraceContentMu's doc comment for why that
// distinction mattered to the M-NIGHTLY AI-20260708-064334-001
// investigation.
func (bt *BTree) pinW(blk storage.BlockNumber) (*storage.Slot, error) {
	s, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: blk})
	if err != nil {
		return nil, err
	}
	s.Lock()
	bt.recordContentMuLock(blk, s.Page())
	return s, nil
}

func (bt *BTree) unpinW(s *storage.Slot) {
	bt.recordContentMuUnlock(s.Tag().Block, s.Page())
	s.Unlock()
	bt.pool.Unpin(s)
}

// ParseMeta exposes the metapage decode for out-of-package readers (the
// amcheck verify engine) that validate a metapage's magic/version without
// opening the tree. See ParseOpaque for the single-source-of-truth rationale.
func ParseMeta(p storage.Page) BTreeMeta { return parseMeta(p) }

func parseMeta(p storage.Page) BTreeMeta {
	off := storage.SizeOfPageHeaderData
	return BTreeMeta{
		Magic:     binary.LittleEndian.Uint32(p[off : off+4]),
		Version:   binary.LittleEndian.Uint32(p[off+4 : off+8]),
		Root:      storage.BlockNumber(binary.LittleEndian.Uint32(p[off+8 : off+12])),
		Level:     binary.LittleEndian.Uint32(p[off+12 : off+16]),
		FastRoot:  storage.BlockNumber(binary.LittleEndian.Uint32(p[off+16 : off+20])),
		FastLevel: binary.LittleEndian.Uint32(p[off+20 : off+24]),
	}
}

func writeMeta(p storage.Page, m BTreeMeta) {
	off := storage.SizeOfPageHeaderData
	binary.LittleEndian.PutUint32(p[off:off+4], m.Magic)
	binary.LittleEndian.PutUint32(p[off+4:off+8], m.Version)
	binary.LittleEndian.PutUint32(p[off+8:off+12], uint32(m.Root))
	binary.LittleEndian.PutUint32(p[off+12:off+16], m.Level)
	binary.LittleEndian.PutUint32(p[off+16:off+20], uint32(m.FastRoot))
	binary.LittleEndian.PutUint32(p[off+20:off+24], m.FastLevel)
	// Bump pd_lower so PageLinePointerCount on this page (if anyone
	// ever asks) reports zero rather than walking into our payload.
	h := storage.MustHeader(p)
	h.SetLower(uint16(off + 24))
}

// markDirtyWithPageRecord routes metadata-page mutations through
// MarkDirtyChangeRecord when a page-image WAL hook is available.
// This keeps metapage/root-flag/root-create updates crash-safe once
// MarkDirty returns to once-per-checkpoint FPI behaviour.
func (bt *BTree) markDirtyWithPageRecord(slot *storage.Slot, blk storage.BlockNumber) error {
	if logPage := bt.pool.LogPageImage(); logPage != nil {
		return bt.pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
			pageCopy := make(storage.Page, storage.BlockSize)
			copy(pageCopy, slot.Page())
			return logPage(bt.rel, blk, pageCopy)
		})
	}
	bt.pool.MarkDirty(slot)
	return nil
}

// updateRootMeta rewrites the metapage root pointer + level. Caller
// must hold bt.splitMu (structural writer serialisation).
func (bt *BTree) updateRootMeta(root storage.BlockNumber, level uint32) error {
	slot, err := bt.pinW(MetaBlock)
	if err != nil {
		return err
	}
	m := parseMeta(slot.Page())
	m.Root = root
	m.Level = level
	m.FastRoot = root
	m.FastLevel = level
	writeMeta(slot.Page(), m)
	err = bt.markDirtyWithPageRecord(slot, MetaBlock)
	bt.unpinW(slot)
	if err != nil {
		return err
	}
	return nil
}

// pageItems lists every item on a page in slot order.
// pageItemsWithDead is pageItems WITHOUT the ItemIDDead skip: it returns
// every item (posting lists expanded) plus a parallel dead flag per
// returned element. VACUUM is the one consumer that must SEE dead-marked
// entries — skipping them there would leave a marked entry out of the
// kept-items WAL rewrite while the heap side reclaims its TID; a crash
// then replays the entry back as Normal pointing at a recycled heap slot
// (C3-S1 review MUST-FIX 1). PG's btbulkdelete likewise deletes by TID
// regardless of LP_DEAD.
func pageItemsWithDead(p storage.Page) ([]item, []bool, error) {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return nil, nil, err
	}
	out := make([]item, 0, count)
	dead := make([]bool, 0, count)
	for slot := uint16(1); slot <= uint16(count); slot++ {
		isDead, derr := storage.PageItemIsDead(p, slot)
		if derr != nil {
			return nil, nil, derr
		}
		raw, err := storage.PageGetItemRawAllowDead(p, slot)
		if err != nil {
			return nil, nil, err
		}
		if isPostingRaw(raw) {
			key, tids, perr := parsePostingRaw(raw)
			if perr != nil {
				maybeDumpPageOnParseErr(p, "pageItemsWithDead: parsePostingRaw")
				return nil, nil, perr
			}
			for _, tid := range tids {
				out = append(out, item{keyLen: uint16(len(key)), ptr: tid, key: key})
				dead = append(dead, isDead)
			}
		} else {
			it, perr := parseItem(raw)
			if perr != nil {
				maybeDumpPageOnParseErr(p, "pageItemsWithDead: parseItem")
				return nil, nil, perr
			}
			out = append(out, it)
			dead = append(dead, isDead)
		}
	}
	return out, dead, nil
}

func pageItems(p storage.Page) ([]item, error) {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return nil, err
	}
	out := make([]item, 0, count)
	for slot := uint16(1); slot <= uint16(count); slot++ {
		if dead, derr := storage.PageItemIsDead(p, slot); derr == nil && dead {
			// C3-S1: ItemIDDead entries are invisible to every reader —
			// the referenced heap tuple is dead to all snapshots and the
			// entry awaits the pre-split purge / VACUUM.
			continue
		}
		raw, err := storage.PageGetItemRaw(p, slot)
		if err != nil {
			return nil, err
		}
		// Posting-list items (M0047-0003) are expanded to individual
		// (key, TID) pairs so callers like insertItemSorted work correctly.
		if isPostingRaw(raw) {
			key, tids, perr := parsePostingRaw(raw)
			if perr != nil {
				maybeDumpPageOnParseErr(p, "pageItems: parsePostingRaw")
				return nil, perr
			}
			for _, tid := range tids {
				out = append(out, item{keyLen: uint16(len(key)), ptr: tid, key: key})
			}
		} else {
			it, perr := parseItem(raw)
			if perr != nil {
				maybeDumpPageOnParseErr(p, "pageItems: parseItem")
				return nil, perr
			}
			out = append(out, it)
		}
	}
	return out, nil
}

// PageItemKeys returns the separator/index key of every line pointer on a
// B-tree page, in physical slot order (slot 1..N). Posting-list items
// (M0047-0003), which pack many heap TIDs under a single shared key, contribute
// that one key exactly once: callers that verify on-disk key ordering (amcheck's
// item-order / high-key invariants) compare the stored separator keys, not the
// expanded (key, TID) pairs that pageItems materialises. It returns an error if
// any line pointer cannot be decoded, so a structurally damaged page surfaces
// to the caller rather than producing a misleading key sequence.
//
// This is exported so the amcheck verification engine (internal/amcheck) decodes
// keys through the canonical on-disk reader here instead of re-implementing the
// item layout — the same single-source-of-truth discipline as ParseMeta /
// ParseOpaque (the inline 2-byte-length key layout is a v3->v4 drift hazard).
func PageItemKeys(p storage.Page) ([][]byte, error) {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, count)
	for slot := uint16(1); slot <= uint16(count); slot++ {
		if dead, derr := storage.PageItemIsDead(p, slot); derr == nil && dead {
			// C3-S1: ItemIDDead entries are invisible to every reader —
			// the referenced heap tuple is dead to all snapshots and the
			// entry awaits the pre-split purge / VACUUM.
			continue
		}
		raw, err := storage.PageGetItemRaw(p, slot)
		if err != nil {
			return nil, err
		}
		if isPostingRaw(raw) {
			key, _, perr := parsePostingRaw(raw)
			if perr != nil {
				maybeDumpPageOnParseErr(p, "PageItemKeys: parsePostingRaw")
				return nil, perr
			}
			out = append(out, key)
		} else {
			it, perr := parseItem(raw)
			if perr != nil {
				maybeDumpPageOnParseErr(p, "PageItemKeys: parseItem")
				return nil, perr
			}
			out = append(out, it.key)
		}
	}
	return out, nil
}

// LeafEntry is one (index key, heap TID) pair on a B-tree leaf page: the key
// and the heap location it points to. Posting-list items (M0047-0003) — which
// pack many heap TIDs under one shared key — are expanded to one LeafEntry per
// TID, so the entries returned here are the "plain" tuples upstream amcheck
// fingerprints for heapallindexed verification (verify_nbtree.c's
// bt_posting_plain_tuple expansion).
type LeafEntry struct {
	Key []byte
	TID storage.ItemPointer
}

// PageLeafEntries returns every (key, heap TID) entry on a B-tree leaf page, in
// physical slot order (slot 1..N), expanding each posting-list item to one
// entry per TID. It returns an error if any line pointer cannot be decoded, so a
// structurally damaged leaf page surfaces to the caller rather than yielding a
// misleading entry set.
//
// Exported, like PageItemKeys / PageDownlinks, so amcheck's heapallindexed
// checker (internal/amcheck.VerifyBtreeHeapAllIndexed) fingerprints the leaf
// entries through the canonical on-disk reader here instead of re-deriving the
// inline (keyLen, TID) item layout — the same single-source-of-truth discipline
// that guards against the v3->v4 layout drift. Unlike PageItemKeys (which
// collapses a posting item to its one separator key for the item-order tier),
// this expands posting items because heapallindexed fingerprints every heap TID
// the index references.
func PageLeafEntries(p storage.Page) ([]LeafEntry, error) {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return nil, err
	}
	out := make([]LeafEntry, 0, count)
	for slot := uint16(1); slot <= uint16(count); slot++ {
		if dead, derr := storage.PageItemIsDead(p, slot); derr == nil && dead {
			// C3-S1: ItemIDDead entries are invisible to every reader —
			// the referenced heap tuple is dead to all snapshots and the
			// entry awaits the pre-split purge / VACUUM.
			continue
		}
		raw, err := storage.PageGetItemRaw(p, slot)
		if err != nil {
			return nil, err
		}
		if isPostingRaw(raw) {
			key, tids, perr := parsePostingRaw(raw)
			if perr != nil {
				maybeDumpPageOnParseErr(p, "PageLeafEntries: parsePostingRaw")
				return nil, perr
			}
			for _, tid := range tids {
				out = append(out, LeafEntry{Key: key, TID: tid})
			}
		} else {
			it, perr := parseItem(raw)
			if perr != nil {
				maybeDumpPageOnParseErr(p, "PageLeafEntries: parseItem")
				return nil, perr
			}
			out = append(out, LeafEntry{Key: it.key, TID: it.ptr})
		}
	}
	return out, nil
}

// Downlink is one (separator key, child block) entry on an internal B-tree
// page: the key routes a search to the child block it precedes. By v0
// convention the leftmost item's key is empty (negative infinity), so its
// child is the subtree that holds everything below the next separator.
type Downlink struct {
	Key   []byte
	Child storage.BlockNumber
}

// PageDownlinks returns the (separator key, child block) downlink entries of an
// internal B-tree page, in physical slot order (slot 1..N). Internal pages never
// carry posting-list items (those are leaf-only, M0047-0003), so each line
// pointer decodes to exactly one downlink whose pointer Block is the child page.
// It returns an error if any line pointer cannot be decoded, so a structurally
// damaged internal page surfaces to the caller rather than yielding misleading
// downlinks.
//
// Exported, like PageItemKeys, so amcheck's cross-level checker
// (internal/amcheck.VerifyBtreeParentDownlinks) follows each parent downlink to
// its child through the canonical on-disk reader here instead of re-deriving the
// inline (keyLen, child-block) item layout — the same single-source-of-truth
// discipline that guards against the v3->v4 layout drift.
func PageDownlinks(p storage.Page) ([]Downlink, error) {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return nil, err
	}
	out := make([]Downlink, 0, count)
	for slot := uint16(1); slot <= uint16(count); slot++ {
		if dead, derr := storage.PageItemIsDead(p, slot); derr == nil && dead {
			// C3-S1: ItemIDDead entries are invisible to every reader —
			// the referenced heap tuple is dead to all snapshots and the
			// entry awaits the pre-split purge / VACUUM.
			continue
		}
		raw, err := storage.PageGetItemRaw(p, slot)
		if err != nil {
			return nil, err
		}
		it, perr := parseItem(raw)
		if perr != nil {
			maybeDumpPageOnParseErr(p, "PageDownlinks: parseItem")
			return nil, perr
		}
		out = append(out, Downlink{Key: it.key, Child: it.ptr.Block})
	}
	return out, nil
}

// findChildBlock returns the block number of the child to descend into
// for `key` from the items on an internal page. Items are sorted by key;
// the entry to descend into is the rightmost item whose key ≤ search
// key. By v0 convention the leftmost internal item carries the empty
// key (lowest possible), so the search is well-defined for the first
// child too.
func findChildBlock(items []item, key []byte) storage.BlockNumber {
	idx := sort.Search(len(items), func(i int) bool {
		return CompareKeys(items[i].key, key) > 0
	})
	if idx == 0 {
		// All items are > key; descend through the first item anyway
		// (for v0 the leftmost item has empty key, making this a
		// theoretical-only branch; we still pick items[0] so an
		// off-by-one doesn't crash).
		return items[0].ptr.Block
	}
	return items[idx-1].ptr.Block
}

// findChildBlockDirect binary-searches an internal B-tree page for the
// child block to descend into for `key`, without decoding all items
// first.  Replaces the pageItems + findChildBlock pair (which allocated
// a slice of all items on every descent).  M0027-0001.
func findChildBlockDirect(p storage.Page, key []byte) (storage.BlockNumber, error) {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, fmt.Errorf("btree: empty internal page")
	}
	// Binary search across line pointers.
	n := count
	idx := sort.Search(n, func(i int) bool {
		raw, err := storage.PageGetItemRawAllowDead(p, uint16(i+1)) // C3-S1: dead items keep ordering bytes
		if err != nil {
			return true // will surface at the final error check
		}
		it, err := parseItem(raw)
		if err != nil {
			return true
		}
		return CompareKeys(it.key, key) > 0
	})
	// sort.Search returns [0, n]; idx==n means all items ≤ key.
	if idx >= n {
		idx = n - 1
	} else if idx > 0 {
		// idx is the first item with key > search_key;
		// child is the preceding item (last one ≤ key).
		idx--
	}
	// idx==0 stays 0: first child.
	raw, err := storage.PageGetItemRawAllowDead(p, uint16(idx+1)) // C3-S1
	if err != nil {
		return 0, err
	}
	it, err := parseItem(raw)
	if err != nil {
		maybeDumpPageOnParseErr(p, "findChildBlockDirect: parseItem")
		return 0, err
	}
	return it.ptr.Block, nil
}

// descendToLeaf walks from the current root to the leaf containing
// `key`. Each page is read under a shared content latch, and the
// classic Lehman-Yao right-link recovery handles the case where a
// concurrent writer split the page after we picked it: if the
// search key exceeds the page's high key, we follow op.Next and
// retry on the right sibling.
//
// The returned `path` is the chain of internal pages walked
// (root..parent-of-leaf), used by writers to propagate separator
// items up after a split. Readers ignore it.
func (bt *BTree) descendToLeaf(key []byte) (leafBlk storage.BlockNumber, path []storage.BlockNumber, err error) {
	meta, err := bt.readMeta()
	if err != nil {
		return 0, nil, err
	}
	cur := meta.Root
	for {
		slot, err := bt.pinR(cur)
		if err != nil {
			return 0, nil, err
		}
		op := readOpaque(slot.Page())

		// Right-link recovery: a concurrent split may have moved
		// our target keys to the right sibling. Follow op.Next
		// until we land on a page that covers the search key.
		if keyExceedsHighKey(op, key) {
			next := op.Next
			bt.unpinR(slot)
			cur = next
			continue
		}

		if op.IsLeaf() {
			// M0055-0002-followup-rightmost-cache: if this leaf
			// has no Next pointer it IS the rightmost; refresh
			// the cache. Cheap atomic write, no allocation.
			if op.Next == 0 {
				bt.rightmostLeafBlk.Store(uint64(cur))
			}
			// (M0055-0004-followup-stage2-splitmu-removal:
			// eager finishSplit on descend was attempted but
			// races with the fast-path concurrent descend
			// produced unpin-underflow under -race stress.
			// The flag remains observable; crash-replay
			// completion is handled by an explicit
			// `CompleteDeferredSplits` maintenance pass —
			// implemented analogous to
			// `CompleteDeferredDeletions` — rather than
			// inline-on-descend. In steady-state operation
			// the flag is set + cleared by the SAME slow-path
			// goroutine while splitMu is held, so descenders
			// never observe it.)
			bt.unpinR(slot)
			return cur, path, nil
		}

		// Binary-search the internal page directly, without decoding
		// all items (avoids allocation & linear decode — M0027-0001).
		child, err := findChildBlockDirect(slot.Page(), key)
		bt.unpinR(slot)
		if err != nil {
			return 0, nil, fmt.Errorf("%w (blk=%d rel=%+v)", err, cur, bt.rel)
		}
		path = append(path, cur)
		cur = child
	}
}

// Insert places (key, ptr) into the leaf where it belongs, splitting
// pages on the way up if needed.
func (bt *BTree) Insert(key []byte, ptr storage.ItemPointer) error {
	bt.stats.inserts.Add(1) // M0055-0001
	it := item{
		keyLen: uint16(len(key)),
		ptr:    ptr,
		key:    append([]byte(nil), key...),
	}

	// Fast path: no split required. Writers touching different leaves
	// only contend on those page latches, not on a tree-wide mutex.
	if err := bt.tryInsertNoSplit(it); err == nil {
		return nil
	} else if !errors.Is(err, errNeedsSplit) {
		return err
	}

	// Split path: serialise structure changes (split propagation,
	// root lift, metapage rewrite), then retry from a fresh
	// descent. (M0055-0004-followup-stage2-splitmu-removal: full
	// removal needs additional buffer-pool concurrency-safety
	// work beyond M0055's scope. The race-safe createNewRoot
	// in this commit handles concurrent root-lifts; the rest
	// of the structural-update path remains under splitMu.)
	bt.stats.splits.Add(1) // M0055-0001 — counts insert calls that take the split-path retry.
	bt.splitMu.Lock()
	defer bt.splitMu.Unlock()

	leafBlk, path, err := bt.descendToLeaf(key)
	if err != nil {
		return err
	}
	return bt.insertIntoBlock(leafBlk, path, it)
}

var errNeedsSplit = errors.New("btree: insert needs split")

func (bt *BTree) tryInsertNoSplit(it item) error {
	// M0055-0002-followup-rightmost-cache: try the cached
	// rightmost leaf first. When the new key is ≥ the leaf's
	// highest key (which is true for monotonic / append-shaped
	// inserts), we skip the descent entirely. On a cache miss
	// (key falls before the cached leaf's max, or the leaf was
	// invalidated by a split that bumped Next), fall through to
	// the slow descent path.
	if cached := bt.rightmostLeafBlk.Load(); cached != 0 {
		if ok, err := bt.tryInsertOnCachedRightmost(storage.BlockNumber(cached), it); ok || err != nil {
			return err
		}
	}
	leafBlk, _, err := bt.descendToLeaf(it.key)
	if err != nil {
		return err
	}
	slot, err := bt.pinW(leafBlk)
	if err != nil {
		return err
	}
	defer bt.unpinW(slot)

	op := readOpaque(slot.Page())
	if keyExceedsHighKey(op, it.key) {
		return errNeedsSplit
	}
	if !pageHasSpaceFor(slot.Page(), it) {
		return errNeedsSplit
	}

	lineIdx, err := bt.insertItemSortedVerified("tryInsertNoSplit", leafBlk, slot.Page(), it)
	if err != nil {
		return errNeedsSplit
	}
	bt.traceInsert(leafBlk, lineIdx, it)
	if logIns := bt.pool.LogBtreeInsert(); logIns != nil {
		itemBytes := it.marshal()
		return bt.pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
			return logIns(bt.rel, leafBlk, itemBytes)
		})
	}
	bt.pool.MarkDirty(slot)
	return nil
}

// insertIntoBlock inserts `it` into the page at blk. If the page
// lacks space, splits it; the resulting separator item is
// recursively inserted into the parent (or a new root is
// created).
//
// All page mutations happen under the per-page exclusive content
// latch so concurrent readers (Search/RangeScan) see either the
// pre-split or post-split state, never an in-between snapshot.
// The split sequence stamps a high key on the left page BEFORE
// dropping its latch — readers that descended to it under shared
// latch will follow the new right-link to find the moved keys.
func (bt *BTree) insertIntoBlock(blk storage.BlockNumber, path []storage.BlockNumber, it item) error {
	// Lehman-Yao move-right: `blk` was decided by the caller (a fresh
	// descendToLeaf under splitMu, or a path[] ancestor recorded before
	// or during this connection's own split). `bt.splitMu` only
	// serializes structural changes within THIS *BTree Go instance —
	// each backend opens its own instance per statement — so a
	// concurrent split on a DIFFERENT connection's instance for the
	// SAME relation can move the key range `it` belongs in to blk's
	// right sibling between that decision and this pin (M-NIGHTLY
	// AI-20260709-010336-082: reproduced as a "high key invariant
	// violated" internal-page finding). Detect via the same
	// leaf/internal high-key boundary amcheck enforces and step right
	// instead of inserting out of bounds.
	var slot *storage.Slot
	var op BTPageOpaque
	// held owns `slot`'s exclusive latch for this frame. Every normal exit
	// below still releases through its own held.release() call; this holder
	// exists for the path those calls cannot cover — a PANIC unwinding out of
	// the page-mutation window, which would otherwise strand the latch and
	// wedge the whole cluster (see latch_release.go). release() is idempotent,
	// so the deferred call is a no-op on every normal return.
	held := wlatch{bt: bt}
	defer held.release()
	for {
		var err error
		slot, err = bt.pinW(blk)
		if err != nil {
			return err
		}
		held.hold(slot)
		op = readOpaque(slot.Page())
		if itemOvershootsHighKey(op, it.key) {
			next := op.Next
			held.release()
			blk = next
			continue
		}
		break
	}

	// Test-only fault injection: fires exactly where insertItemSorted
	// panicked in the wedge (regress suite, 2026-08-06), i.e. with the leaf
	// latch held and the frame mid-mutation.
	if bt.panicBeforeLeafWrite != nil {
		bt.panicBeforeLeafWrite(blk)
	}

	if pageHasSpaceFor(slot.Page(), it) {
		lineIdx, err := bt.insertItemSortedVerified("insertIntoBlock-nosplit", blk, slot.Page(), it)
		if err != nil {
			// pageHasSpaceFor and PageInsertItemRawAt disagreed
			// (root-0040). Fall through to the split path instead
			// of panicking — the space budget is restated from a
			// single source (itemEncodedSize), but a residual
			// disagreement must not strand the latch.
		} else {
			bt.traceInsert(blk, lineIdx, it)
			// Logical-record path (M0002 redo-records): if the pool
			// has a btree-insert hook configured, emit a small
			// change record on subsequent dirties of this page in
			// the same checkpoint epoch instead of a full FPI. The
			// first dirty in an epoch still emits the FPI baseline.
			// Falls back to plain MarkDirty when the hook isn't
			// wired (test helpers, pre-runtime callers).
			var derr error
			if logIns := bt.pool.LogBtreeInsert(); logIns != nil {
				itemBytes := it.marshal()
				derr = bt.pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
					return logIns(bt.rel, blk, itemBytes)
				})
			} else {
				bt.pool.MarkDirty(slot)
			}
			held.release()
			return derr
		}
	}

	// Split. Pin a freshly-extended right page exclusively,
	// redistribute items, stamp the high key, then drop both
	// latches before walking up. `op` already holds this page's
	// pre-split opaque header from the move-right loop above.
	// The block that is currently left's right sibling (if any).
	// After the split the new right page is spliced between them, so
	// this sibling's btpo_prev must be relinked from blk to rightBlk
	// (done below, atomically with the split). InvalidBlockNumber
	// here means a rightmost split with no sibling to relink.
	oldNext := op.Next
	// M0055-0005 Phase D: prefer recycled blocks before
	// extending the file.
	rightSlot, rightBlk, err := bt.pinNewOrRecycled()
	if err != nil {
		held.release()
		return err
	}
	// rightSlot is already content-locked by pinNewOrRecycled (its
	// locked-return contract) — no separate Lock() here.

	// The right sibling inherits the original page's high key
	// (or has none, if this was the rightmost page on its level).
	rightOpaque := BTPageOpaque{
		Prev:    blk,
		Next:    op.Next,
		Level:   op.Level,
		Flags:   op.Flags & ^BTRoot, // right sibling is never the root (root stays the original blk during a split, until we lift)
		HighKey: op.HighKey,
	}
	if !op.HasHighKey() {
		rightOpaque.Flags &^= BTHasHighKey
	}
	initPage(rightSlot.Page(), rightOpaque)

	// M-NIGHTLY investigation aid (AI-20260708-064334-001, 6th loop):
	// snapshot the two checkpoints that matter for localizing a lost
	// entry — right after pageItems()+appendSorted() (what the rewrite
	// believes is on the page, plus the incoming item) and right after
	// dedupConsolidate() (what it's about to write back). Both are DEEP
	// COPIES of the item-struct slice (not the post-dedup slice itself,
	// which reuses pageItems's backing array via `items[:0]` in-place
	// compaction — holding a live reference into it would read corrupted
	// data once dedupConsolidate runs). Zero cost when DebugTraceInserts
	// is off (traceRewrite no-ops immediately).
	preLineCount, _ := storage.PageLinePointerCount(slot.Page())

	allItems, err := pageItems(slot.Page())
	if err != nil {
		rightSlot.Unlock()
		bt.pool.Unpin(rightSlot)
		held.release()
		return err
	}
	allItems = appendSorted(allItems, it)
	var postPageItemsSnap []item
	if bt.DebugTraceInserts {
		postPageItemsSnap = append([]item(nil), allItems...)
	}

	// M0055-0003 Phase B (pre-split dedup compaction): consolidate
	// adjacent same-key items into postings. For duplicate-heavy
	// workloads this reduces split frequency dramatically — the
	// page may fit comfortably in a single leaf after dedup,
	// avoiding the split entirely. We bail back to the no-split
	// path if dedup recovers enough space.
	allItems = dedupConsolidate(allItems)
	var postDedupSnap []item
	if bt.DebugTraceInserts {
		postDedupSnap = append([]item(nil), allItems...)
	}
	if compactRawSize(allItems) < pageFreeBudget(slot.Page())+pageOccupied(slot.Page()) {
		bt.traceRewrite(blk, "dedup-recovery", preLineCount, postPageItemsSnap, postDedupSnap)
		// Re-attempt no-split insert with the dedup'd content.
		// Reset the page and write the dedup'd items back, no
		// split needed. The right-side allocation is rolled back.
		resetPageItems(slot.Page())
		for _, x := range allItems {
			lineIdx := mustInsertItemSorted(slot.Page(), x)
			bt.traceInsert(blk, lineIdx, x)
		}
		// Drop the freshly-allocated right slot — split avoided.
		rightSlot.Unlock()
		bt.pool.Unpin(rightSlot)
		// C3-S3 (S2-review blocker fix A): this rewrite SHIFTS SLOT
		// NUMBERS, so it must bump pd_lsn — the deferred kill pass
		// re-verifies leaf identity by LSN equality (D7) and a plain
		// MarkDirty leaves pd_lsn unchanged when an FPI already exists
		// this epoch, letting a stale kill mark the WRONG slot. Route
		// through the logical kept-items record (same emitter VACUUM
		// uses; also fixes the pre-existing unlogged-rewrite crash gap
		// the S1 review noted). Falls back to a page-image record for
		// harnesses without the vacuum hook.
		if opAfter := readOpaque(slot.Page()); opAfter.HasGarbage() {
			// The rewrite dropped every dead-marked item (pageItems skips
			// them) — clear the hint like VacuumIndexPages does (O-C3-5).
			opAfter.Flags &^= BTHasGarbage
			writeOpaque(slot.Page(), opAfter)
		}
		if logVac := bt.pool.LogBtreeVacuum(); logVac != nil {
			// A8: the record carries the post-vacuum page as a full-page image,
			// so pass the mutated page rather than the kept-items projection.
			if err := bt.pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
				return logVac(bt.rel, blk, slot.Page())
			}); err != nil {
				held.release()
				return err
			}
		} else if err := bt.markDirtyWithPageRecord(slot, blk); err != nil {
			held.release()
			return err
		}
		held.release()
		return nil
	}
	bt.traceRewrite(blk, "split", preLineCount, postPageItemsSnap, postDedupSnap)

	// M0055-0002-followup-byte-split: byte-aware split-loc.
	// Pick the entry whose cumulative encoded byte size lands
	// closest to half the total. For fixed-width keys this is
	// equivalent to count-midpoint; for variable-width keys it
	// produces balanced halves in bytes (the on-disk metric the
	// page fill threshold actually cares about).
	mid := byteAwareSplitLoc(allItems)
	leftItems := allItems[:mid]
	rightItems := allItems[mid:]

	// Reset left page to empty, refill.
	resetPageItems(slot.Page())
	for _, x := range leftItems {
		lineIdx := mustInsertItemSorted(slot.Page(), x)
		bt.traceInsert(blk, lineIdx, x)
	}
	for _, x := range rightItems {
		lineIdx := mustInsertItemSorted(rightSlot.Page(), x)
		bt.traceInsert(rightBlk, lineIdx, x)
	}

	// Stamp the new high key onto the left page: left now covers
	// keys ≤ HighKey, the rest live on rightBlk via the
	// right-link. This is the Lehman-Yao invariant readers rely on.
	op.Next = rightBlk
	op.Flags |= BTHasHighKey
	sepKey := rightItems[0].key
	if len(sepKey) > MaxHighKeyLen {
		rightSlot.Unlock()
		bt.pool.Unpin(rightSlot)
		held.release()
		return fmt.Errorf("btree: separator key length %d exceeds MaxHighKeyLen %d",
			len(sepKey), MaxHighKeyLen)
	}
	op.HighKey = append([]byte(nil), sepKey...)
	// M0055-0004-followup-finish-split: mark the LEFT page as
	// incomplete-split before releasing latches. Cleared after
	// the parent downlink insert succeeds. Crash-replay leaves
	// the flag set so a subsequent writer descending to this
	// page runs finishSplit before inserting (the protocol's
	// resume guarantee).
	op.Flags |= BTIncompleteSplit
	writeOpaque(slot.Page(), op)

	// Non-rightmost split: the page that was left's right sibling
	// still has btpo_prev pointing at the left block. Relink it to
	// the new right page so the doubly-linked sibling chain stays
	// consistent — btpo_prev is load-bearing (btree_vacuum.go reads
	// op.Prev and WAL-logs RightSibNewPrev to relink siblings on
	// page deletion; a stale left-link there would relink the wrong
	// page). PostgreSQL does exactly this in _bt_split, stamping the
	// original right sibling under the same atomic split WAL record.
	// Lock order is strictly left→right (blk → rightBlk → oldNext),
	// matching _bt_split, so it cannot deadlock against a concurrent
	// split descending from the left.
	sibBlk := oldNext
	var sibSlot *storage.Slot
	if sibBlk != storage.InvalidBlockNumber {
		sibSlot, err = bt.pinW(sibBlk)
		if err != nil {
			rightSlot.Unlock()
			bt.pool.Unpin(rightSlot)
			held.release()
			return fmt.Errorf("btree: pin old right sibling %d: %w", sibBlk, err)
		}
		sibOp := readOpaque(sibSlot.Page())
		sibOp.Prev = rightBlk
		writeOpaque(sibSlot.Page(), sibOp)
	}

	// Atomic split WAL record (Landing 3a). When a writer is
	// available, emit ONE record covering both pages (plus the
	// relinked old right sibling on a non-rightmost split) and stamp
	// the resulting LSN onto every page header; this guarantees
	// crash recovery never observes the half-split state where
	// left's right-link points at a right block whose disk image
	// is the bare smgr.Extend init page, nor the half-relinked state
	// where the old sibling's btpo_prev still points at the left
	// block. When no writer is wired (test helpers, pre-runtime
	// callers), fall back to the per-page FPI path via MarkDirty —
	// losing split atomicity but keeping the in-memory tree correct.
	if bt.logSplit != nil {
		var sibPage storage.Page
		if sibSlot != nil {
			sibPage = sibSlot.Page()
		}
		lsn, lerr := bt.logSplit(bt.rel, blk, rightBlk, slot.Page(), rightSlot.Page(), sibBlk, sibPage)
		if lerr != nil {
			if sibSlot != nil {
				bt.unpinW(sibSlot)
			}
			rightSlot.Unlock()
			bt.pool.Unpin(rightSlot)
			held.release()
			return fmt.Errorf("btree: log split: %w", lerr)
		}
		bt.pool.MarkDirtyWithLSNLocked(slot, lsn)
		bt.pool.MarkDirtyWithLSNLocked(rightSlot, lsn)
		if sibSlot != nil {
			bt.pool.MarkDirtyWithLSNLocked(sibSlot, lsn)
		}
	} else {
		bt.pool.MarkDirty(slot)
		bt.pool.MarkDirty(rightSlot)
		if sibSlot != nil {
			bt.pool.MarkDirty(sibSlot)
		}
	}
	if sibSlot != nil {
		bt.unpinW(sibSlot)
	}

	// The separator key going up is the smallest key in the right page.
	sepItem := item{
		keyLen: rightItems[0].keyLen,
		ptr:    storage.ItemPointer{Block: rightBlk, Offset: 0},
		key:    append([]byte(nil), rightItems[0].key...),
	}

	rightSlot.Unlock()
	bt.pool.Unpin(rightSlot)
	held.release()

	// If we just split the root, lift a new root.
	if op.IsRoot() {
		// Strip BTRoot off the now-non-root left page.
		if err := bt.clearRootFlag(blk); err != nil {
			return err
		}
		if err := bt.createNewRoot(blk, rightBlk, sepItem.key, op.Level+1); err != nil {
			return err
		}
		// Parent (the new root) now references the left half;
		// clear the INCOMPLETE_SPLIT marker. (M0055-0004-followup.)
		return bt.clearIncompleteSplit(blk)
	}

	// Otherwise, insert the separator into the parent.
	parentBlk := path[len(path)-1]
	parentPath := path[:len(path)-1]
	if err := bt.insertIntoBlock(parentBlk, parentPath, sepItem); err != nil {
		return err
	}
	// Parent insert succeeded — clear the INCOMPLETE_SPLIT flag
	// on the left page. (M0055-0004-followup.)
	return bt.clearIncompleteSplit(blk)
}

// clearIncompleteSplit (M0055-0004-followup-finish-split) unsets
// the BTIncompleteSplit flag on the page. Invoked at the end of
// a successful split sequence (parent downlink inserted), and
// by `finishSplit` when a writer / vacuum encounters a stale
// incomplete-split marker from a crashed prior run.
func (bt *BTree) clearIncompleteSplit(blk storage.BlockNumber) error {
	slot, err := bt.pinW(blk)
	if err != nil {
		return err
	}
	op := readOpaque(slot.Page())
	op.Flags &^= BTIncompleteSplit
	writeOpaque(slot.Page(), op)
	err = bt.markDirtyWithPageRecord(slot, blk)
	bt.unpinW(slot)
	return err
}

// CompleteDeferredSplits (M0055-0004-followup-stage2-splitmu-
// removal) scans the tree for any pages still flagged
// BTIncompleteSplit (Phase 1 — leaf split — committed but the
// parent-downlink insertion did not). Used by maintenance
// passes (vacuum, post-recovery startup) to complete in-flight
// splits that were interrupted by a crash. Steady-state
// operation never produces such pages (the slow-path sets +
// clears the flag under splitMu in the same critical
// section).
func (bt *BTree) CompleteDeferredSplits() (int, error) {
	rel := bt.rel
	nBlocks, err := bt.pool.NBlocks(rel)
	if err != nil {
		return 0, err
	}
	completed := 0
	for blk := storage.BlockNumber(1); blk < nBlocks; blk++ {
		slot, err := bt.pinR(blk)
		if err != nil {
			continue
		}
		op := readOpaque(slot.Page())
		incomplete := op.HasIncompleteSplit()
		bt.unpinR(slot)
		if !incomplete {
			continue
		}
		if err := bt.finishSplit(blk); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

// finishSplit (M0055-0004-followup-finish-split) re-runs the
// parent-downlink insertion for a page that is still flagged
// BTIncompleteSplit (e.g., after crash-replay where the writer's
// split phase landed but the parent insert did not). Idempotent
// — if the parent already references the page, the redundant
// insert is detected by the parent's binary-search "already
// present" check and the flag is simply cleared.
//
// Callers: writers descending the tree (before continuing
// writes on this page); vacuum maintenance pass.
func (bt *BTree) finishSplit(blk storage.BlockNumber) error {
	slot, err := bt.pinW(blk)
	if err != nil {
		return err
	}
	op := readOpaque(slot.Page())
	if !op.HasIncompleteSplit() {
		bt.unpinW(slot)
		return nil
	}
	// Read the high key + Next to reconstruct the separator
	// item that the original split was about to push up.
	if !op.HasHighKey() || op.Next == storage.InvalidBlockNumber {
		// Defensive: malformed half-state. Clear the flag and
		// hope the parent caught up via some other path.
		op.Flags &^= BTIncompleteSplit
		writeOpaque(slot.Page(), op)
		bt.markDirtyWithPageRecord(slot, blk)
		bt.unpinW(slot)
		return nil
	}
	rightBlk := op.Next
	sepKey := append([]byte(nil), op.HighKey...)
	bt.unpinW(slot)

	// Walk to the parent and insert the separator. We descend by
	// the separator key so the parent path is reconstructed.
	bt.splitMu.Lock()
	defer bt.splitMu.Unlock()
	_, path, err := bt.descendToLeaf(sepKey)
	if err != nil {
		return err
	}
	if len(path) == 0 {
		// blk is the root — parent insert means a new root lift.
		// Should have happened atomically; if not, redo it.
		return bt.createNewRoot(blk, rightBlk, sepKey, op.Level+1)
	}
	parentBlk := path[len(path)-1]
	parentPath := path[:len(path)-1]
	sepItem := item{
		keyLen: uint16(len(sepKey)),
		ptr:    storage.ItemPointer{Block: rightBlk, Offset: 0},
		key:    sepKey,
	}
	if err := bt.insertIntoBlock(parentBlk, parentPath, sepItem); err != nil {
		return err
	}
	return bt.clearIncompleteSplit(blk)
}

func (bt *BTree) clearRootFlag(blk storage.BlockNumber) error {
	slot, err := bt.pinW(blk)
	if err != nil {
		return err
	}
	op := readOpaque(slot.Page())
	op.Flags &^= BTRoot
	writeOpaque(slot.Page(), op)
	err = bt.markDirtyWithPageRecord(slot, blk)
	bt.unpinW(slot)
	if err != nil {
		return err
	}
	return nil
}

// ApplyInsertRecord re-runs one B-tree non-split insert against
// the given page bytes during WAL replay (see
// docs/design/0002-0003-redo-records.md). The raw item is the
// same payload the writer emitted (item.marshal output: keyLen +
// ptr.block + ptr.offset + key). The page must already be a
// valid initialised B-tree page; replay never creates a fresh
// btree page from a logical insert (a split record handles that
// case).
//
// Idempotency is the caller's responsibility: WAL recovery
// compares page pd_lsn against the record's end-LSN before
// invoking this. The function is "apply unconditionally".
func ApplyInsertRecord(page storage.Page, raw []byte) error {
	it, err := parseItem(raw)
	if err != nil {
		return err
	}
	if !pageHasSpaceFor(page, it) {
		return fmt.Errorf("btree: replay of insert: page has no space for keyLen=%d", it.keyLen)
	}
	mustInsertItemSorted(page, it)
	return nil
}

func (bt *BTree) createNewRoot(leftBlk, rightBlk storage.BlockNumber, rightKey []byte, level uint32) error {
	// M0055-0004-followup-stage2-splitmu-removal (race-safe
	// new-root publication): the caller is expected to hold
	// `splitMu`, but a future Stage-2 design that lifts splitMu
	// must still serialise root publication. We RE-READ the
	// meta here so even under a future relaxation, two
	// concurrent splits that both target the OLD root cannot
	// both create a new root; the loser falls back to inserting
	// its separator into the CURRENT root via the regular
	// path. Today, with splitMu held, the re-read is a defensive
	// invariant check (and a no-op fast path).
	meta, err := bt.readMeta()
	if err != nil {
		return err
	}
	if meta.Root != leftBlk {
		// Some other writer (under a future lock-free protocol)
		// already lifted a new root above `leftBlk`. Insert our
		// separator into the current root through the regular
		// path.
		sepItem := item{
			keyLen: uint16(len(rightKey)),
			ptr:    storage.ItemPointer{Block: rightBlk, Offset: 0},
			key:    append([]byte(nil), rightKey...),
		}
		_, parentPath, err := bt.descendToLeaf(rightKey)
		if err != nil {
			return err
		}
		if len(parentPath) == 0 {
			// Defensive: meta.Root != leftBlk but parentPath is
			// empty — meta moved again. Recurse to pick up the
			// latest state.
			return bt.createNewRoot(leftBlk, rightBlk, rightKey, level)
		}
		return bt.insertIntoBlock(parentPath[0], parentPath[:0], sepItem)
	}

	rootSlot, rootBlk, err := bt.pool.PinNew(bt.rel)
	if err != nil {
		return err
	}
	rootSlot.Lock()
	initPage(rootSlot.Page(), BTPageOpaque{
		Prev:  storage.InvalidBlockNumber,
		Next:  storage.InvalidBlockNumber,
		Level: level,
		Flags: BTRoot,
	})

	// Leftmost internal item: empty key, pointer to leftBlk.
	leftItem := item{
		keyLen: 0,
		ptr:    storage.ItemPointer{Block: leftBlk, Offset: 0},
		key:    nil,
	}
	rightItem := item{
		keyLen: uint16(len(rightKey)),
		ptr:    storage.ItemPointer{Block: rightBlk, Offset: 0},
		key:    append([]byte(nil), rightKey...),
	}
	mustInsertItemSorted(rootSlot.Page(), leftItem)
	mustInsertItemSorted(rootSlot.Page(), rightItem)

	// M0079-0004 / A8: emit a single PG RM_BTREE new-root record covering
	// both the new root page (backup block 0) and the updated metapage
	// (backup block 2) as full-page images. The metapage is mutated in
	// memory HERE — before the emit — so its post-op bytes ride the same
	// record; both pages' pd_lsn is then stamped to the record LSN (per-page
	// replay idempotency). Both slots are held under splitMu (this path's
	// structural-writer serialisation), so co-locking the metapage with the
	// new root is race-free. Falls back to the legacy per-page FPI path when
	// no hook is wired (test harnesses).
	if emitter := bt.pool.LogBtreeNewRoot(); emitter != nil {
		metaSlot, err := bt.pinW(MetaBlock)
		if err != nil {
			rootSlot.Unlock()
			bt.pool.Unpin(rootSlot)
			return err
		}
		m := parseMeta(metaSlot.Page())
		m.Root = rootBlk
		m.Level = level
		m.FastRoot = rootBlk
		m.FastLevel = level
		writeMeta(metaSlot.Page(), m)
		lsn, err := emitter(bt.rel, rootBlk, rootSlot.Page(), MetaBlock, metaSlot.Page())
		if err != nil {
			bt.unpinW(metaSlot)
			rootSlot.Unlock()
			bt.pool.Unpin(rootSlot)
			return err
		}
		bt.pool.MarkDirtyWithLSNLocked(rootSlot, lsn)
		bt.pool.MarkDirtyWithLSNLocked(metaSlot, lsn)
		bt.unpinW(metaSlot)
		rootSlot.Unlock()
		bt.pool.Unpin(rootSlot)
		return nil
	}
	if err := bt.markDirtyWithPageRecord(rootSlot, rootBlk); err != nil {
		rootSlot.Unlock()
		bt.pool.Unpin(rootSlot)
		return err
	}
	rootSlot.Unlock()
	bt.pool.Unpin(rootSlot)

	return bt.updateRootMeta(rootBlk, level)
}

// itemEncodedSize returns the number of bytes the raw item occupies on a page,
// including the line-pointer ItemID (4 bytes) and the prefix+key body.
// This is the single source of truth for the budget both pageHasSpaceFor and
// PageInsertItemRawAt depend on — they were once computed from different
// expressions and a mismatch at a nearly-full leaf triggered the
// "not enough free space in page" panic (root-0040).
func itemEncodedSize(it item) int {
	const itemIDSize = 4 // matches storage.itemIDSize
	return itemIDSize + itemPrefixSize + len(it.key)
}

// pageHasSpaceFor reports whether `it` would fit on `p` if appended.
func pageHasSpaceFor(p storage.Page, it item) bool {
	h := storage.MustHeader(p)
	free := int(h.Upper()) - int(h.Lower())
	return free >= itemEncodedSize(it)
}

// insertItemSorted (M0055-0002 Phase A) writes `it` into its
// sorted position on the page WITHOUT decoding and re-encoding
// every existing item. Binary-searches the existing line-pointer
// array reading only each candidate's key; calls
// `PageInsertItemRawAt` to memmove the line-pointer suffix and
// drop the new tuple bytes at pd_upper.
//
// Replaces the prior whole-page rewrite path that decoded every
// item, inserted into a Go slice, reset the page, and re-
// encoded every item. The pre-Phase-A path was the chief CPU
// hotspot per the M0055 baseline pprof (analysis/btree-baseline-
// 2026-05-06.md) — this rewrite eliminates the O(n) re-encode.
//
// Returns the 0-based line-pointer index (used by BTree.traceInsert) and
// any error from PageInsertItemRawAt. Callers that have pre-verified space
// via pageHasSpaceFor should use mustInsertItemSorted; callers on the
// no-split fast path should route storage.ErrNoSpaceInPage to the split
// path instead of panicking (root-0040).
func insertItemSorted(p storage.Page, it item) (int, error) {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		panic(err)
	}
	// Binary-search for the insertion slot. The line-pointer
	// accessors decode just one key per probe, not every item
	// on the page.
	lo, hi := 0, count
	for lo < hi {
		mid := (lo + hi) >> 1
		midItem, err := readPageItem(p, mid)
		if err != nil {
			panic(err)
		}
		if CompareKeys(midItem.key, it.key) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	// (M0055-0003 Phase B's in-insertItemSorted dedup-grow path
	// was tried but produces page-fragment garbage that
	// accumulates over many duplicate inserts. The pre-split
	// dedup compaction variant — see `dedupPageBeforeSplit` —
	// is the safer landing for steady-state dedup retention.)
	raw := it.marshal()
	// PageInsertItemRawAt is 1-based, line-pointer index lo is
	// 0-based; convert.
	if _, err := storage.PageInsertItemRawAt(p, uint16(lo+1), raw); err != nil {
		return 0, err
	}
	return lo, nil
}

// mustInsertItemSorted is the panicking variant for callers that have
// pre-verified space (dedup-recovery refill, split left/right refill,
// createNewRoot, WAL replay). A panic here means the space estimate and
// the real insert logic have diverged — a logic bug, not a runtime
// condition.
func mustInsertItemSorted(p storage.Page, it item) int {
	idx, err := insertItemSorted(p, it)
	if err != nil {
		panic(err)
	}
	return idx
}

// tryInsertOnCachedRightmost (M0055-0002-followup-rightmost-cache)
// attempts to insert `it` on the cached rightmost leaf. Returns
// (true, nil) when the insert succeeded on the cached leaf; (false,
// nil) when the cache is stale or the key doesn't belong on this
// leaf (so the caller falls back to a full descent); (any, err)
// when an underlying I/O error fires.
//
// Staleness conditions:
//   - The leaf has a Next pointer set (a later split moved the
//     rightmost forward). The cache is stale; clear and miss.
//   - The leaf is full (no space for `it`). Caller takes the
//     normal split path.
//   - `it.key < leaf.HighKey` (the key belongs to a left sibling,
//     not this leaf — the cache is wrong for this insert).
//
// The cache is updated by the slow-path descent every time it
// reaches a leaf that's truly the rightmost (Next == 0).
func (bt *BTree) tryInsertOnCachedRightmost(blk storage.BlockNumber, it item) (bool, error) {
	slot, err := bt.pinW(blk)
	if err != nil {
		// Cache may reference a deleted page — clear and miss.
		bt.rightmostLeafBlk.Store(0)
		return false, nil
	}
	op := readOpaque(slot.Page())
	if op.Level != 0 || op.Next != 0 {
		// Not a leaf, or no longer rightmost — cache is stale.
		bt.unpinW(slot)
		bt.rightmostLeafBlk.Store(0)
		return false, nil
	}
	// M0056-0001: verify the key actually belongs on this leaf
	// — the rightmost leaf has no high key, but it DOES have a
	// SMALLEST key. Inserting a key smaller than the leaf's
	// smallest entry would be a logical error (the key belongs
	// on a left sibling). For an empty leaf any key is in
	// range. With concurrent writers inserting disjoint key
	// ranges, the cache may point at a leaf that's rightmost
	// for one writer's range but irrelevant for another's;
	// fall back to the descent path in that case.
	count, perr := storage.PageLinePointerCount(slot.Page())
	if perr == nil && count > 0 {
		first, ferr := readPageItem(slot.Page(), 0)
		if ferr == nil && CompareKeys(it.key, first.key) < 0 {
			bt.unpinW(slot)
			return false, nil
		}
	}
	if !pageHasSpaceFor(slot.Page(), it) {
		bt.unpinW(slot)
		return false, nil
	}
	lineIdx, err := bt.insertItemSortedVerified("tryInsertOnCachedRightmost", blk, slot.Page(), it)
	if err != nil {
		bt.unpinW(slot)
		return false, nil
	}
	bt.traceInsert(blk, lineIdx, it)
	if logIns := bt.pool.LogBtreeInsert(); logIns != nil {
		itemBytes := it.marshal()
		err := bt.pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
			return logIns(bt.rel, blk, itemBytes)
		})
		bt.unpinW(slot)
		return true, err
	}
	bt.pool.MarkDirty(slot)
	bt.unpinW(slot)
	return true, nil
}

// dedupConsolidate (M0055-0003 Phase B) walks a sorted item list
// and merges runs of same-key items into a single conceptual
// posting (represented here as the FIRST item; subsequent
// duplicates are suppressed and their TIDs are folded into the
// first via a synthetic posting marker). The result is suitable
// for a fresh insertItemSorted re-build of the page.
//
// Implementation note: items[] is the EXPANDED form (one item
// per (key, tid)) produced by `pageItems`. Consolidation
// re-builds postings by replacing the first item of each run
// with a posting-flagged item whose `key` is the actual key and
// whose `ptr` is the first TID; subsequent duplicates contribute
// their TIDs through a side-channel `extraTIDs` slice on the
// item.
//
// To keep things simple, we DON'T yet rebuild posting bytes here
// — instead we de-duplicate the expanded list by collapsing
// runs of identical (key, ptr) pairs that may exist after
// pageItems' expansion. For real consolidation into postings,
// we use `marshalPosting` directly when re-writing the page
// (Phase B's full landing — out of scope for this commit).
//
// What this function does TODAY: drop exact (key, ptr) duplicates
// from the expanded list. That alone bounds duplicate-heavy
// workloads where the same heap tuple shows up multiple times
// in the bulk-build's input.
func dedupConsolidate(items []item) []item {
	if len(items) <= 1 {
		return items
	}
	out := items[:0]
	for i, it := range items {
		if i == 0 {
			out = append(out, it)
			continue
		}
		prev := out[len(out)-1]
		if CompareKeys(prev.key, it.key) == 0 && prev.ptr == it.ptr {
			// Exact duplicate — drop.
			continue
		}
		out = append(out, it)
	}
	return out
}

// compactRawSize (M0055-0003 Phase B) sums the marshalled byte
// size of every item in `items` plus the per-item line-pointer
// overhead. Used to decide whether dedup recovered enough space
// to skip the split.
func compactRawSize(items []item) int {
	total := 0
	for _, it := range items {
		total += itemEncodedSize(it)
	}
	return total
}

// pageFreeBudget returns the remaining free byte budget on a
// page (pd_upper - pd_lower).
func pageFreeBudget(p storage.Page) int {
	h, err := storage.Header(p)
	if err != nil {
		return 0
	}
	return int(h.Upper()) - int(h.Lower())
}

// pageOccupied returns the bytes already used by line pointers
// + tuple data on a page.
func pageOccupied(p storage.Page) int {
	h, err := storage.Header(p)
	if err != nil {
		return 0
	}
	return int(h.Lower()) - int(storage.SizeOfPageHeaderData) +
		(int(btSpecialOffset) - int(h.Upper()))
}

// byteAwareSplitLoc (M0055-0002-followup-byte-split) returns the
// 0-based slot index where the split should land so the left
// half holds approximately half the total encoded byte size.
// For fixed-width keys this collapses to len(items)/2 (within a
// rounding tick); for variable-width keys the split point lands
// where the byte cursor crosses half-total, producing balanced
// halves in bytes — the metric the page-fill threshold actually
// cares about.
//
// The minimum split point is 1 (left always retains at least one
// item) and the maximum is len(items)-1 so the right side is
// non-empty. A degenerate single-item input returns 1 (caller
// guarantees ≥ 2 items because we're splitting a full page).
func byteAwareSplitLoc(items []item) int {
	if len(items) <= 2 {
		return 1
	}
	total := 0
	sizes := make([]int, len(items))
	for i, it := range items {
		// 8-byte fixed prefix + variable-length key (per
		// `(item).marshal()` layout). We use the encoded size
		// rather than just key length so the metric matches the
		// on-page footprint.
		sizes[i] = 8 + len(it.key)
		total += sizes[i]
	}
	half := total / 2
	cum := 0
	for i := 0; i < len(items)-1; i++ {
		cum += sizes[i]
		if cum >= half {
			// Place the split AFTER item i — left holds items[0..i],
			// right holds items[i+1..]. Return i+1 because callers
			// use `items[:mid]` / `items[mid:]`.
			split := i + 1
			if split < 1 {
				split = 1
			}
			if split > len(items)-1 {
				split = len(items) - 1
			}
			return split
		}
	}
	// Fall-through: total too small to cross half-mark before the
	// last entry — return n-1 so right has exactly one item.
	return len(items) - 1
}

// readPageItem (M0055-0002 Phase A) decodes a single item at the
// given 0-based line-pointer index. Used by binary-search probes
// in `insertItemSorted` so the insert path no longer decodes
// every item on the page. For posting-list line pointers
// (M0047-0003) it returns the FIRST tid bundled in the posting
// — that's enough for the binary search since all posting tids
// share the same key.
func readPageItem(p storage.Page, idx int) (item, error) {
	// C3-S1: AllowDead — this is a binary-search ordering probe; Dead
	// items retain valid key bytes until purged, and result filtering
	// happens at the caller's visibility layer.
	raw, err := storage.PageGetItemRawAllowDead(p, uint16(idx+1))
	if err != nil {
		return item{}, err
	}
	if isPostingRaw(raw) {
		key, tids, perr := parsePostingRaw(raw)
		if perr != nil {
			maybeDumpPageOnParseErr(p, "readPageItem: parsePostingRaw")
			return item{}, perr
		}
		var ptr storage.ItemPointer
		if len(tids) > 0 {
			ptr = tids[0]
		}
		return item{keyLen: uint16(len(key)), ptr: ptr, key: key}, nil
	}
	it, perr := parseItem(raw)
	if perr != nil {
		maybeDumpPageOnParseErr(p, "readPageItem: parseItem")
	}
	return it, perr
}

func appendSorted(items []item, it item) []item {
	idx := sort.Search(len(items), func(i int) bool {
		return CompareKeys(items[i].key, it.key) >= 0
	})
	out := make([]item, len(items)+1)
	copy(out[:idx], items[:idx])
	out[idx] = it
	copy(out[idx+1:], items[idx:])
	return out
}

// resetPageItems clears the page's tuple region and line-pointer array
// while keeping the opaque area intact.
func resetPageItems(p storage.Page) {
	h := storage.MustHeader(p)
	h.SetLower(uint16(storage.SizeOfPageHeaderData))
	h.SetUpper(uint16(btSpecialOffset))
	// Zero the in-between bytes for cleanliness; not strictly required.
	for i := storage.SizeOfPageHeaderData; i < btSpecialOffset; i++ {
		p[i] = 0
	}
}

// Search returns the heap pointer associated with key, if any.
//
// Search takes no tree-wide lock — concurrent Searches and the
// single in-flight Insert run truly in parallel except where they
// touch the same page (and there only transiently, under the
// buffer pool's per-slot content latch). Right-link recovery at
// the leaf catches the case where a split moved our key to the
// right sibling between descent and lookup.
func (bt *BTree) Search(key []byte) (storage.ItemPointer, bool, error) {
	leafBlk, _, err := bt.descendToLeaf(key)
	if err != nil {
		return storage.ItemPointer{}, false, err
	}
	cur := leafBlk
	for {
		slot, err := bt.pinR(cur)
		if err != nil {
			return storage.ItemPointer{}, false, err
		}
		op := readOpaque(slot.Page())
		if keyExceedsHighKey(op, key) {
			next := op.Next
			bt.unpinR(slot)
			cur = next
			continue
		}
		items, err := pageItems(slot.Page())
		bt.unpinR(slot)
		if err != nil {
			return storage.ItemPointer{}, false, err
		}
		idx := sort.Search(len(items), func(i int) bool {
			return CompareKeys(items[i].key, key) >= 0
		})
		if idx >= len(items) || CompareKeys(items[idx].key, key) != 0 {
			return storage.ItemPointer{}, false, nil
		}
		return items[idx].ptr, true, nil
	}
}

// ScanPos identifies where a RangeScan callback's entry physically lives:
// the leaf block, the 1-based line-pointer slot, and the leaf's pd_lsn AS
// CAPTURED AT SCAN TIME. C3-S2 scan plumbing: the executor records these
// alongside TIDs so the deferred kill pass (S3) can re-latch the leaf and
// re-verify identity keyed on PageLSN (design D7 — a changed LSN means the
// page split/vacuumed/recycled and the pending kill is dropped).
type ScanPos struct {
	Blk     storage.BlockNumber
	Slot    uint16
	PageLSN storage.LSN
}

// RangeScanWithPos is RangeScan carrying a ScanPos per callback (C3-S2,
// additive — the plain RangeScan signature and its callers are untouched;
// both share one implementation).
func (bt *BTree) RangeScanWithPos(lo, hi []byte, fn func(key []byte, ptr storage.ItemPointer, pos ScanPos) (bool, error)) error {
	return bt.rangeScanPos(lo, hi, fn)
}

func (bt *BTree) RangeScan(lo, hi []byte, fn func(key []byte, ptr storage.ItemPointer) (bool, error)) error {
	return bt.rangeScanPos(lo, hi, func(key []byte, ptr storage.ItemPointer, _ ScanPos) (bool, error) {
		return fn(key, ptr)
	})
}

// RangeScan invokes fn for every (key, ptr) pair where lo ≤ key ≤ hi.
// Either bound may be nil to indicate an open-ended range:
//   - nil lo means no lower bound (scan from the leftmost key).
//   - nil hi means no upper bound (scan through the rightmost key).
//
// fn returning false stops the scan; the returned error from fn aborts
// with that error.
//
// **CONTRACT (M0091-0002):** the `key []byte` passed to `fn` ALIASES
// the still-pinned btree leaf page. `fn` MUST NOT:
//   - retain `key` beyond its return (the page may be unpinned and
//     reused for a different page after the next call);
//   - re-enter THIS btree (would deadlock against our held RLock
//     on the leaf page);
//   - perform long-running I/O (the RLock blocks btree writers on
//     this leaf page only; bounded writer-starvation is acceptable
//     for point lookups / heap-fetch but not for arbitrary I/O).
//
// Callers that need to retain `key` clone it explicitly:
//
//	keyCopy := append([]byte(nil), key...)
//
// All 4 production callers (indexScanOp.Rescan,
// indexOnlyScanOp.Rescan, upsertOp.probeArbiter, and the non-HOT
// UPDATE index-probe in operators_storage.go) are CAT-1 per
// docs/design/0091-0002 — audited 2026-05-11.
//
// RangeScan takes no tree-wide lock; each page is read under the
// buffer pool's per-slot shared content latch. The first leaf is
// reached via descendToLeaf (which already handles right-link
// recovery); subsequent leaves are walked rightward via op.Next.
func (bt *BTree) rangeScanPos(lo, hi []byte, fn func(key []byte, ptr storage.ItemPointer, pos ScanPos) (bool, error)) error {
	cur, _, err := bt.descendToLeaf(lo)
	if err != nil {
		return err
	}
	for cur != storage.InvalidBlockNumber {
		slot, err := bt.pinR(cur)
		if err != nil {
			return err
		}
		op := readOpaque(slot.Page())
		// Recovery on the first iteration: descendToLeaf may
		// have returned a leaf that has since been split. Skip
		// rightward until we land on a page whose key range
		// covers `lo` (or we run out of pages).
		// When lo is nil, keyExceedsHighKey(op, nil) is always false
		// (nil compares less than any real key), so we never skip — correct.
		if lo != nil && keyExceedsHighKey(op, lo) {
			next := op.Next
			bt.unpinR(slot)
			cur = next
			continue
		}
		// M0091-0002: parse + invoke fn WHILE the pin is held.
		// Pre-fix this loop copied every slot's raw bytes to a
		// per-slot []byte ("append([]byte(nil), r...)") so the
		// pin could be released before fn ran — ~400 allocations
		// per point-lookup leaf-page visit, driving 13 % of
		// allocations in the select-only pprof. All current
		// callers are CAT-1 (see contract above); none retain
		// key beyond fn, none re-enter the btree.
		count, countErr := storage.PageLinePointerCount(slot.Page())
		pageLSN := storage.MustHeader(slot.Page()).LSN()
		nextBlk := op.Next
		stop := false
		var fnErr error
		if countErr == nil {
		slotLoop:
			for s := uint16(1); s <= uint16(count); s++ {
				// M0091-0002: NoCopy aliases the still-pinned
				// page; we never retain it past `fn`'s return,
				// and the pin is held across this whole loop.
				r, rawErr := storage.PageGetItemRawNoCopy(slot.Page(), s)
				if rawErr != nil {
					// Includes ItemIDDead slots (ErrUnsupportedItem):
					// C3-S1 — dead entries are invisible to scans.
					continue
				}
				if isPostingRaw(r) {
					// Posting items still allocate inside
					// parsePostingRaw (TID slice + key copy).
					// Out-of-scope for M0091; pgbench pkey is
					// non-posting so this branch doesn't fire
					// in the target workload.
					key, tids, perr := parsePostingRaw(r)
					if perr != nil {
						continue
					}
					if lo != nil && CompareKeys(key, lo) < 0 {
						continue
					}
					if hi != nil && CompareKeys(key, hi) > 0 {
						stop = true
						break slotLoop
					}
					for _, tid := range tids {
						ok, ferr := fn(key, tid, ScanPos{Blk: cur, Slot: s, PageLSN: pageLSN})
						if ferr != nil {
							fnErr = ferr
							stop = true
							break slotLoop
						}
						if !ok {
							stop = true
							break slotLoop
						}
					}
				} else {
					// M0091-0002: parseItemNoCopy aliases the
					// page; key MUST NOT be retained by fn.
					it, perr := parseItemNoCopy(r)
					if perr != nil {
						continue
					}
					if lo != nil && CompareKeys(it.key, lo) < 0 {
						continue
					}
					if hi != nil && CompareKeys(it.key, hi) > 0 {
						stop = true
						break slotLoop
					}
					ok, ferr := fn(it.key, it.ptr, ScanPos{Blk: cur, Slot: s, PageLSN: pageLSN})
					if ferr != nil {
						fnErr = ferr
						stop = true
						break slotLoop
					}
					if !ok {
						stop = true
						break slotLoop
					}
				}
			}
		}
		bt.unpinR(slot)
		if fnErr != nil {
			return fnErr
		}
		if stop {
			return nil
		}
		cur = nextBlk
	}
	return nil
}
