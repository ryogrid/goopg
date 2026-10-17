// Package blkreftable is a port of PostgreSQL 18.3's block reference tables
// (src/common/blkreftable.c, src/include/common/blkreftable.h).
//
// A block reference table records which blocks of each relation fork were
// modified by the WAL records of some LSN range, plus a "limit block" per
// fork: the shortest length the fork is known to have had inside the range
// (0 when it was created or dropped, the post-truncation length when it was
// truncated). Setting the limit forgets every modified block at or above it;
// a block marked again later means the fork was re-extended.
//
// The WAL summarizer (docs/design/0100-0149/0122-0009-wal-summarizer.md)
// serializes one table per summary file under pg_wal/summaries. The on-disk
// bytes must be identical to PostgreSQL's so that PG's own pg_walsummary and
// pg_combinebackup read goopg's files, which is why the in-memory layout here
// follows blkreftable.c closely: each fork is split into chunks of 2^16
// blocks, and a chunk is either an array of 16-bit offsets kept in insertion
// order or, from its 4096th distinct offset on, a 4096-word bitmap. The array
// order is visible in the file, so it is part of the format.
package blkreftable

import "sort"

// BlockNumber is a block number within one relation fork.
type BlockNumber = uint32

// InvalidBlockNumber is the limit block of a fork that has no known limit
// (block.h InvalidBlockNumber).
const InvalidBlockNumber BlockNumber = 0xFFFFFFFF

// ForkNumber is PostgreSQL's ForkNumber enum (common/relpath.h): main 0,
// fsm 1, visibility map 2, init 3. It is serialized as a signed int32.
type ForkNumber = int32

// RelFileLocator identifies a relation's storage (storage/relfilelocator.h).
type RelFileLocator struct {
	SpcOid    uint32
	DbOid     uint32
	RelNumber uint32
}

const (
	// blocksPerChunk is BLOCKS_PER_CHUNK: the blocks one chunk covers.
	blocksPerChunk = 1 << 16
	// blocksPerEntry is BLOCKS_PER_ENTRY: the bits in one bitmap word.
	blocksPerEntry = 16
	// maxEntriesPerChunk is MAX_ENTRIES_PER_CHUNK: the bitmap's word count,
	// and the usage value that marks a chunk as a bitmap.
	maxEntriesPerChunk = blocksPerChunk / blocksPerEntry
)

// Key names one relation fork.
type Key struct {
	Locator RelFileLocator
	Fork    ForkNumber
}

// Entry is the state of one relation fork (BlockRefTableEntry).
//
// chunkUsage[i] is the number of offsets in array chunk i, or
// maxEntriesPerChunk when chunk i is a bitmap; chunkData[i] holds the offsets
// (array) or the bitmap words. A chunk whose usage is 0 contributes nothing,
// and SetLimitBlock drops chunks above the limit by zeroing their usage.
type Entry struct {
	key        Key
	limitBlock BlockNumber
	chunkUsage []uint16
	chunkData  [][]uint16
}

// NewEntry returns an entry with no modified blocks and no limit, for use
// with Writer, which takes entries one at a time
// (CreateBlockRefTableEntry).
func NewEntry(loc RelFileLocator, fork ForkNumber) *Entry {
	return &Entry{key: Key{Locator: loc, Fork: fork}, limitBlock: InvalidBlockNumber}
}

// Key returns the relation fork the entry describes.
func (e *Entry) Key() Key { return e.key }

// LimitBlock returns the entry's limit block, InvalidBlockNumber when none.
func (e *Entry) LimitBlock() BlockNumber { return e.limitBlock }

// SetLimitBlock lowers the entry's limit block and forgets every modified
// block at or above it (BlockRefTableEntrySetLimitBlock). A limit at or above
// the current one changes nothing: the limit is the shortest length seen.
func (e *Entry) SetLimitBlock(limit BlockNumber) {
	if limit >= e.limitBlock {
		return
	}
	e.limitBlock = limit
	limitChunk := int(limit / blocksPerChunk)
	limitOffset := uint32(limit % blocksPerChunk)
	if limitChunk >= len(e.chunkUsage) {
		return
	}
	for c := limitChunk + 1; c < len(e.chunkUsage); c++ {
		e.chunkUsage[c] = 0
	}
	data := e.chunkData[limitChunk]
	if e.chunkUsage[limitChunk] == maxEntriesPerChunk {
		// A bitmap stays a bitmap, even when every bit is now clear.
		for off := limitOffset; off < blocksPerChunk; off++ {
			data[off/blocksPerEntry] &^= 1 << (off % blocksPerEntry)
		}
		return
	}
	// Filter the array in place, keeping the insertion order.
	j := 0
	for i := 0; i < int(e.chunkUsage[limitChunk]); i++ {
		if uint32(data[i]) < limitOffset {
			data[j] = data[i]
			j++
		}
	}
	e.chunkUsage[limitChunk] = uint16(j)
}

// MarkBlockModified records blk as modified
// (BlockRefTableEntryMarkBlockModified). It does not consult the limit
// block: a block marked after the limit was set means the fork was
// re-extended.
func (e *Entry) MarkBlockModified(blk BlockNumber) {
	chunk := int(blk / blocksPerChunk)
	offset := uint16(blk % blocksPerChunk)
	if chunk >= len(e.chunkUsage) {
		n := chunk + 1
		e.chunkUsage = append(e.chunkUsage, make([]uint16, n-len(e.chunkUsage))...)
		e.chunkData = append(e.chunkData, make([][]uint16, n-len(e.chunkData))...)
	}
	usage := e.chunkUsage[chunk]
	if usage == maxEntriesPerChunk {
		e.chunkData[chunk][offset/blocksPerEntry] |= 1 << (offset % blocksPerEntry)
		return
	}
	data := e.chunkData[chunk][:usage]
	for _, o := range data {
		if o == offset {
			return
		}
	}
	if usage == maxEntriesPerChunk-1 {
		// The 4096th distinct offset converts the array to a bitmap.
		bitmap := make([]uint16, maxEntriesPerChunk)
		for _, o := range data {
			bitmap[o/blocksPerEntry] |= 1 << (o % blocksPerEntry)
		}
		bitmap[offset/blocksPerEntry] |= 1 << (offset % blocksPerEntry)
		e.chunkData[chunk] = bitmap
		e.chunkUsage[chunk] = maxEntriesPerChunk
		return
	}
	// A chunk dropped by SetLimitBlock (usage 0) restarts as an array here,
	// even if it used to be a bitmap — as in PG, where only usage decides.
	e.chunkData[chunk] = append(data, offset)
	e.chunkUsage[chunk] = usage + 1
}

// serializedChunks is the chunk count written to disk: trailing chunks with
// no usage are trimmed, chunks with no usage in the middle are kept.
func (e *Entry) serializedChunks() int {
	n := len(e.chunkUsage)
	for n > 0 && e.chunkUsage[n-1] == 0 {
		n--
	}
	return n
}

// Blocks appends to dst the modified blocks in [start, stop) and returns it
// (BlockRefTableEntryGetBlocks). Within an array chunk the order is the
// insertion order; across chunks it ascends.
func (e *Entry) Blocks(start, stop BlockNumber, dst []BlockNumber) []BlockNumber {
	if stop <= start {
		return dst
	}
	startChunk := int(start / blocksPerChunk)
	stopChunk := int(stop / blocksPerChunk)
	if stop%blocksPerChunk != 0 {
		stopChunk++
	}
	if stopChunk > len(e.chunkUsage) {
		stopChunk = len(e.chunkUsage)
	}
	for c := startChunk; c < stopChunk; c++ {
		startOff, stopOff := uint32(0), uint32(blocksPerChunk)
		if c == startChunk {
			startOff = start % blocksPerChunk
		}
		if c == int(stop/blocksPerChunk) {
			stopOff = stop % blocksPerChunk
		}
		base := BlockNumber(c) * blocksPerChunk
		usage := e.chunkUsage[c]
		data := e.chunkData[c]
		if usage == maxEntriesPerChunk {
			for off := startOff; off < stopOff; off++ {
				if data[off/blocksPerEntry]&(1<<(off%blocksPerEntry)) != 0 {
					dst = append(dst, base+off)
				}
			}
			continue
		}
		for _, o := range data[:usage] {
			if uint32(o) >= startOff && uint32(o) < stopOff {
				dst = append(dst, base+BlockNumber(o))
			}
		}
	}
	return dst
}

// Table is a block reference table: one Entry per relation fork.
type Table struct {
	entries map[Key]*Entry
}

// NewTable returns an empty table (CreateEmptyBlockRefTable).
func NewTable() *Table {
	return &Table{entries: make(map[Key]*Entry)}
}

// Len returns the number of relation forks in the table.
func (t *Table) Len() int { return len(t.entries) }

// SetLimitBlock lowers the limit block of a fork, creating its entry with
// that limit when the fork is not in the table yet
// (BlockRefTableSetLimitBlock).
func (t *Table) SetLimitBlock(loc RelFileLocator, fork ForkNumber, limit BlockNumber) {
	k := Key{Locator: loc, Fork: fork}
	if e, ok := t.entries[k]; ok {
		e.SetLimitBlock(limit)
		return
	}
	t.entries[k] = &Entry{key: k, limitBlock: limit}
}

// MarkBlockModified records a modified block, creating the fork's entry with
// no limit when needed (BlockRefTableMarkBlockModified).
func (t *Table) MarkBlockModified(loc RelFileLocator, fork ForkNumber, blk BlockNumber) {
	k := Key{Locator: loc, Fork: fork}
	e, ok := t.entries[k]
	if !ok {
		e = NewEntry(loc, fork)
		t.entries[k] = e
	}
	e.MarkBlockModified(blk)
}

// Entry returns the fork's entry, or nil when the table has none
// (BlockRefTableGetEntry).
func (t *Table) Entry(loc RelFileLocator, fork ForkNumber) *Entry {
	return t.entries[Key{Locator: loc, Fork: fork}]
}

// SortedEntries returns the entries in the order they are serialized:
// tablespace, then database, then relation, then fork
// (BlockRefTableComparator). blkreftable.h's comment on the incremental
// writer says "database, then tablespace"; the comparator and both writers
// sort by tablespace first, and so does this.
func (t *Table) SortedEntries() []*Entry {
	out := make([]*Entry, 0, len(t.entries))
	for _, e := range t.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return keyLess(out[i].key, out[j].key) })
	return out
}

func keyLess(a, b Key) bool {
	if a.Locator.SpcOid != b.Locator.SpcOid {
		return a.Locator.SpcOid < b.Locator.SpcOid
	}
	if a.Locator.DbOid != b.Locator.DbOid {
		return a.Locator.DbOid < b.Locator.DbOid
	}
	if a.Locator.RelNumber != b.Locator.RelNumber {
		return a.Locator.RelNumber < b.Locator.RelNumber
	}
	return a.Fork < b.Fork
}
