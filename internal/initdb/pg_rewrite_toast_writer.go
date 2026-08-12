// pg_rewrite TOAST chunk writer — the out-of-line half of
// DECLARE_TOAST(pg_rewrite, 2838, 2839).
//
// M0131-S20.2 (2026-08-12), design docs/design/0131-0035-pg-rewrite-toast.md.
//
// S20.1 landed the relation pair itself (pg_class / pg_attribute / pg_index
// rows plus two physical files) and left the TOAST heap EMPTY. This file is
// what fills it: a seeded `pg_rewrite.ev_action` whose inline representation
// would not fit a heap tuple is compressed, split into TOAST_MAX_CHUNK_SIZE
// chunks, written into base/{1,5}/2838, indexed by 2839, and replaced in the
// pg_rewrite row by an 18-byte `varatt_external` on-disk pointer.
//
// Everything here is measured against a throwaway PG 18.3 oracle, not inferred
// from headers (the numbers below are from a freshly `initdb`'d cluster):
//
//	view                  rule oid  extsize  raw text  chunks
//	pg_indexes               12046     9002     70408       5
//	pg_stats                 12056     9316     92367       5
//	pg_statio_all_tables     12177    10475     72286       6
//	pg_stats_ext_exprs       12066    11481     92109       6
//	pg_stats_ext             12061    12196     73811       7
//	pg_seclabels             12102    35379    203378      18
//
// Three facts that shaped the code, each of which would have been guessed
// wrong from the headers alone:
//
//  1. **The chunked bytes are the compressed varlena MINUS its 4-byte
//     header** — i.e. they begin with the 4-byte `va_tcinfo` word, not with
//     the pglz stream. `toast_save_datum` takes `data_p = VARDATA(dval)` for a
//     compressed datum (toast_internals.c), and VARDATA of a 4B varlena skips
//     only the header. Confirmed on the oracle: chunk 0 of pg_indexes' value
//     starts `08 13 01 00 | 00 28 7b 51` = tcinfo 70408, then the pglz control
//     byte and the literals `(`, `{`, `Q` of "({QUERY". Consequently
//     `sum(length(chunk_data)) == pg_column_size(ev_action)` exactly (9002),
//     which is also what `toast_datum_size` returns for an external datum.
//  2. **`va_rawsize` is the UNCOMPRESSED size + VARHDRSZ in both branches** —
//     for a compressed datum it comes from the tcinfo's extsize field, for a
//     plain one from VARSIZE. So the single expression `len(raw)+4` is right
//     either way, and `VARATT_EXTERNAL_IS_COMPRESSED` (extsize < rawsize-4)
//     then discriminates the two for free, with no flag of our own.
//  3. **`chunk_id == rule OID + 1`, universally.** Upstream assigns the value
//     id with `GetNewOidWithIndex(toastrel)` during the very insert that just
//     consumed the rule's own OID, so the two are adjacent. Verified as an
//     invariant, not a coincidence: on the oracle NO distinct chunk_id in
//     pg_toast_2618 fails to match some `pg_rewrite.oid + 1` (0 exceptions
//     over all 280 chunk rows). goopg pins its rule OIDs to upstream's
//     (system_view_oid_pins.go), so pinning chunk_id the same way makes
//     goopg's TOAST heap OID-identical to upstream's rather than merely
//     well-formed.
//
// goopg's own reload does NOT read these values: loadViewsFromHeapForDB skips
// every rule whose ev_class is below FirstUserOID, and a user view's rule is
// SQL text written by writeViewRewriteRow (never toasted — "pg_node_tree" is
// not in executor.isToastableType). What the reload path DOES do is decode
// every pg_rewrite row it scans, including these, so the codec must consume an
// 18-byte on-disk pointer without erroring; see the "pg_node_tree" arm of
// decodePhysicalPGValueMctx, which is this file's sibling path.

package initdb

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/pglz"
)

const (
	// varattExternalPointerSize is 2 + sizeof(varatt_external) = 18: the 1-byte
	// VARATT_IS_1B_E header, the 1-byte va_tag, then the four 4-byte fields
	// (postgres/src/include/varatt.h:38-48, :89).
	varattExternalPointerSize = 18

	// varTagOnDisk is VARTAG_ONDISK (varatt.h:89). Its value is 18 for on-disk
	// compatibility reasons unrelated to the struct's size; the coincidence
	// with varattExternalPointerSize is exactly that.
	varTagOnDisk = 18

	// varlenaExtSizeBits / varlenaExtSizeMask split va_extinfo into the stored
	// payload size (low 30 bits) and the compression method (high 2 bits) —
	// varatt.h:45-46.
	varlenaExtSizeBits = 30
	varlenaExtSizeMask = uint32(1)<<varlenaExtSizeBits - 1

	// toastPglzCompressionID is TOAST_PGLZ_COMPRESSION_ID (varatt.h). goopg
	// only ever produces pglz; LZ4 (id 1) is a build option upstream and its
	// absence is not observable in the pointer, which carries the method.
	toastPglzCompressionID = 0

	// maxInlineEvActionStored is the largest STORED (post-compression) size a
	// seeded ev_action may have and still live inline in its pg_rewrite tuple.
	// MaxHeapTupleSize is 8160 at BLCKSZ=8192
	// (postgres/src/include/access/htup_details.h); the other seven columns,
	// the "<>" ev_qual varlena and the tuple header take a flat 160 B reserve.
	// Deliberately the same number as scripts/capture-ev-action.sh's guard #5,
	// which is what decides whether a capture may enter the corpus at all —
	// the two must name the same boundary or a view could pass capture and
	// then produce an unreadable heap page.
	maxInlineEvActionStored = 8000
)

// toastChunk is one row of a TOAST relation: (chunk_id oid, chunk_seq int4,
// chunk_data bytea). Data is at most toastMaxChunkSize bytes and every chunk
// but the last of a value is exactly that long.
type toastChunk struct {
	ToastRel uint32 // which TOAST heap the chunk belongs in
	ChunkID  uint32
	Seq      int32
	Data     []byte
}

// toastChunkCols is the fixed 3-column layout of every TOAST relation
// (postgres/src/backend/catalog/toasting.c:222-243). It mirrors the
// pg_attribute rows nailedToastRels() seeds, and the two must agree — a
// hosted PG deforms the chunk with the descriptor built from those rows.
func toastChunkCols() []catalog.Column {
	return []catalog.Column{
		{Name: "chunk_id", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
		{Name: "chunk_seq", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
		{Name: "chunk_data", Type: catalog.Type{Name: "bytea"}, Ordinal: 2},
	}
}

// buildVarattExternalOnDisk renders the 18-byte VARTAG_ONDISK pointer PG
// stores in place of an externalised value. Field order and widths are
// varatt_external's (varatt.h:38-48); every field is native-endian, which on
// every platform goopg targets is little-endian — the same assumption the rest
// of the physical-tuple codec makes.
func buildVarattExternalOnDisk(rawSize int32, extSize uint32, cmethod uint8, valueID, toastRelOID uint32) []byte {
	out := make([]byte, varattExternalPointerSize)
	out[0] = 0x01 // VARATT_IS_1B_E
	out[1] = varTagOnDisk
	le := binary.LittleEndian
	le.PutUint32(out[2:6], uint32(rawSize))
	le.PutUint32(out[6:10], (extSize&varlenaExtSizeMask)|uint32(cmethod)<<varlenaExtSizeBits)
	le.PutUint32(out[10:14], valueID)
	le.PutUint32(out[14:18], toastRelOID)
	return out
}

// externalizeVarlenaPayload splits raw into TOAST chunks and returns the
// pointer that replaces it in the owning tuple. It reproduces
// toast_save_datum's two live branches:
//
//   - compressible: the value is pglz-compressed into a 4B_C varlena and the
//     bytes AFTER its 4-byte header (i.e. va_tcinfo + the pglz stream) are what
//     get chunked; va_rawsize is the uncompressed length + VARHDRSZ.
//   - incompressible: the raw bytes are chunked as-is and va_extsize equals
//     va_rawsize - VARHDRSZ, which is precisely how PG's reader tells the two
//     apart (VARATT_EXTERNAL_IS_COMPRESSED).
//
// The compression acceptance test mirrors pglzVarlenaDatum's, so a value's
// representation does not change merely because it crossed the inline budget.
func externalizeVarlenaPayload(raw []byte, valueID, toastRelOID uint32) ([]byte, []toastChunk) {
	stored := raw
	cmethod := uint8(toastPglzCompressionID)
	compressed := false
	if comp := pglz.Compress(raw); comp != nil && 8+len(comp) < len(raw)+4 {
		blob := pglz.BuildCompressedVarlena(comp, len(raw))
		stored = blob[4:] // drop va_header; keep va_tcinfo + the pglz stream
		compressed = true
	}
	if !compressed {
		// An incompressible value carries no compression method: PG leaves the
		// two high bits of va_extinfo clear and detects "not compressed" by
		// size equality, so cmethod must stay 0 here too.
		cmethod = 0
	}
	ptr := buildVarattExternalOnDisk(int32(len(raw)+4), uint32(len(stored)), cmethod, valueID, toastRelOID)

	var chunks []toastChunk
	for off, seq := 0, int32(0); off < len(stored); off, seq = off+toastMaxChunkSize, seq+1 {
		end := off + toastMaxChunkSize
		if end > len(stored) {
			end = len(stored)
		}
		chunks = append(chunks, toastChunk{
			ToastRel: toastRelOID,
			ChunkID:  valueID,
			Seq:      seq,
			Data:     append([]byte(nil), stored[off:end]...),
		})
	}
	return ptr, chunks
}

// pgNodeTreeInlineStoredSize returns the number of heap-tuple bytes the datum
// pglzVarlenaDatum(s) would occupy — the compressed varlena when compression
// pays, otherwise the plain 1- or 4-byte-header varlena. This is the quantity
// the inline budget is measured against; the raw string length is not, because
// every oversize ev_action compresses roughly 8:1.
func pgNodeTreeInlineStoredSize(s string) int {
	d := pglzVarlenaDatum(s)
	if d.Kind == executor.KindBytes {
		return len(d.BytesValue())
	}
	if len(s)+1 <= 127 {
		return len(s) + 1
	}
	return len(s) + 4
}

// pgRewriteToastValueID returns the chunk_id a rule's externalised ev_action
// is stored under: the rule OID + 1, pinned to what upstream's own initdb
// assigns (see this file's header comment for the measurement). Keeping it
// pinned rather than counter-allocated means goopg's pg_toast_2618 is
// OID-identical to upstream's, so a diff of the two clusters' TOAST heaps
// stays a diff about bytes rather than about numbering.
func pgRewriteToastValueID(ruleOID uint32) uint32 { return ruleOID + 1 }

// pgRewriteEvActionDatum decides how one seeded rule's ev_action is stored and
// returns the datum for column 8 plus any chunks that must reach the TOAST
// heap. The inline case is untouched by S20.2 — it is still whatever
// pglzVarlenaDatum produces, which is what all 71 corpus views use today.
func pgRewriteEvActionDatum(e pgRewriteEntry) (executor.Datum, []toastChunk) {
	if pgNodeTreeInlineStoredSize(e.EvAction) <= maxInlineEvActionStored {
		return pglzVarlenaDatum(e.EvAction), nil
	}
	pairs := nailedToastPairs()
	if len(pairs) == 0 {
		// Unreachable while DECLARE_TOAST(pg_rewrite, …) exists; storing the
		// value inline anyway would produce a heap page no reader can parse,
		// so fall back to the inline datum only to keep the failure local to
		// the (impossible) configuration that removed the toast relation.
		return pglzVarlenaDatum(e.EvAction), nil
	}
	ptr, chunks := externalizeVarlenaPayload([]byte(e.EvAction),
		pgRewriteToastValueID(e.OID), pairs[0].ToastRel)
	// The 18 bytes are emitted verbatim by the codec's pg_node_tree KindBytes
	// passthrough — the same door the inline compressed varlena walks through.
	return executor.NewBytesDatum(ptr), chunks
}

// toastChunkRows renders chunks as heap rows in the TOAST relation's column
// order. Chunks are emitted in (chunk_id, chunk_seq) order by construction of
// the caller, which is also the order the 2839 index is built in.
func toastChunkRows(chunks []toastChunk) []executor.Row {
	rows := make([]executor.Row, 0, len(chunks))
	for _, c := range chunks {
		rows = append(rows, executor.Row{
			executor.NewIntDatum(int64(c.ChunkID)),
			executor.NewIntDatum(int64(c.Seq)),
			executor.NewBytesDatum(c.Data),
		})
	}
	return rows
}

// writeToastChunkHeap writes the chunk rows into base/{1,5}/<toastRel> and
// returns their heap TIDs in input order so the (chunk_id, chunk_seq) btree
// can point at them. Multi-page from the start: a 1996-byte chunk tuple is
// ~2032 bytes, so only four fit a page and pg_seclabels alone needs 18.
func writeToastChunkHeap(dataDir string, toastRel uint32, chunks []toastChunk) ([]heapTID, error) {
	tids, err := writeMultiPageHeapRows(dataDir, fmt.Sprintf("%d", toastRel),
		toastChunkCols(), toastChunkRows(chunks))
	if err != nil {
		return nil, fmt.Errorf("toast heap %d: %w", toastRel, err)
	}
	return tids, nil
}
