// heapallindexed.go implements the heapallindexed tier of upstream amcheck's
// bt_index_check() / bt_index_parent_check() — the optional, redundancy-based
// check that every live heap tuple has a matching entry in the index
// (postgres/contrib/amcheck/verify_nbtree.c). It is the last B-tree tier and the
// first one that crosses the index↔heap boundary, so it follows the same
// engine-first/wire-later pattern as the rest of the package: this file ports
// the Bloom-filter fingerprint+probe core as a pure function over two entry
// sets. The two relation-walk producers that fill those sets from a live
// relation's page bytes are also in the engine — the index leaf-level walk
// (CollectBtreeLeafEntries in heapallindexed_relation.go) and the heap
// line-pointer walk (CollectHeapIndexEntries in heapallindexed_heapscan.go) —
// so the SQL surface (bt_index_check(index, heapallindexed => true)) reduces to
// a thin adapter that supplies the two PageSources and the TupleDesc-coupled
// HeapEntryFormer, wired in a later loop once the tree is clean. See
// docs/design/0110-0007-amcheck-heapallindexed.md.
//
// Upstream algorithm (verify_nbtree.c):
//
//  1. Fingerprint phase. While the structural check walks the index, every
//     non-dead leaf IndexTuple is normalized (bt_normalize_tuple) and added to a
//     Bloom filter (bloom_add_element, verify_nbtree.c:1500/1511). Posting-list
//     tuples are exploded into one "plain" tuple per heap TID
//     (bt_posting_plain_tuple) and each is fingerprinted separately.
//
//  2. Probe phase. table_index_build_scan then drives bt_tuple_present_callback
//     (verify_nbtree.c:2781) once per live heap tuple: it forms the IndexTuple a
//     fresh CREATE INDEX would emit (index_form_tuple), normalizes it, and probes
//     the filter with bloom_lacks_element. A heap tuple whose formed index tuple
//     is absent means the index is missing an entry for it — corruption — and
//     upstream raises "heap tuple (b,o) from table \"T\" lacks matching index
//     tuple within index \"I\"".
//
// The soundness of the check rests entirely on the Bloom filter having no false
// negatives: an element that was added is NEVER reported absent, so a heap tuple
// that genuinely has a matching index entry is never falsely flagged. False
// positives can only go the other way — they let a small fraction (<2% at the
// 2-bytes-per-element sizing in bloomCreate) of genuinely-missing entries slip
// through undetected — so the check never invents corruption, it only
// occasionally misses some. Upstream randomizes the seed per run so a different
// subset is caught on a re-check; this engine takes the seed as a parameter so
// the wire-later caller can randomize while tests pin it for determinism.
//
// goopg / upstream PG divergences:
//
//   - Fingerprint unit. Upstream fingerprints the raw normalized IndexTuple
//     bytes (heap TID in t_tid + the key data). goopg's leaf items are
//     (key, heap TID) pairs (btree.LeafEntry) with no PG IndexTuple header, so
//     the fingerprint here is a deterministic encoding of (TID, key) — see
//     fingerprintLeafEntry. The only invariant that matters is that the
//     fingerprint phase and the probe phase encode an identical logical
//     (key, TID) to identical bytes, which they do because both consume the same
//     btree.LeafEntry shape.
//
//   - Normalization. bt_normalize_tuple exists to undo a TOAST-compression
//     representation mismatch between index_form_tuple and btinsert. goopg does
//     not TOAST index keys, so there is no analogous representation drift and no
//     normalization step is needed; the leaf key bytes and the heap-formed key
//     bytes are already in one canonical form (the same EncodeXxxKey output).
//
//   - Pure function vs scan callback. Upstream interleaves fingerprinting with
//     the structural walk and drives probing from a heap-scan callback. This
//     engine takes both entry sets as slices: the index leaf entries (collected
//     by the caller via btree.PageLeafEntries over the leaf level) and the
//     heap-formed entries (the index tuple each live heap tuple would produce).
//     The lazy scan, snapshot registration, and HOT-chain root-TID remapping
//     that table_index_build_scan performs are caller (wire-later) concerns; the
//     fingerprint+probe core is what this tier ports.
package amcheck

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/access/nbtree"
)

// heapAllIndexedWorkMemKB bounds the Bloom filter bitset, standing in for
// upstream's maintenance_work_mem argument to bloom_create. 64 MB matches a
// modest maintenance_work_mem and is far more than any realistic index needs at
// 2 bytes/element; the wire-later SQL surface will instead thread the live
// maintenance_work_mem GUC here.
const heapAllIndexedWorkMemKB = 64 * 1024

// fingerprintLeafEntry encodes a (key, heap TID) entry to the byte string the
// Bloom filter fingerprints. The encoding is the goopg analog of upstream's
// "normalized IndexTuple bytes": the heap TID (block:4 + offset:2, big-endian so
// the bytes are stable across architectures) followed by the raw key bytes. The
// fingerprint phase (index leaf entries) and the probe phase (heap-formed
// entries) MUST run identical (key, TID) pairs through this same function, or a
// present entry would hash differently on the two sides and produce a false
// "lacks matching index tuple" report — the sibling-path invariant the package
// guards (see pattern_sibling_paths_must_agree).
func fingerprintLeafEntry(e nbtree.LeafEntry) []byte {
	buf := make([]byte, 6+len(e.Key))
	binary.BigEndian.PutUint32(buf[0:4], uint32(e.TID.Block))
	binary.BigEndian.PutUint16(buf[4:6], e.TID.Offset)
	copy(buf[6:], e.Key)
	return buf
}

// VerifyBtreeHeapAllIndexed runs the heapallindexed tier: it fingerprints every
// index leaf entry into a Bloom filter, then probes the filter with each
// heap-formed index entry, returning one BtreeReport per heap tuple whose formed
// index tuple is absent from the index. indexName / tableName are woven into the
// upstream-verbatim message so the later SQL surface and the 002_cic / 003_check
// ports can reuse it; seed is the Bloom hash seed (callers randomize it across
// runs to vary which false-positive-masked entries are caught, tests pin it).
//
// indexLeafEntries are the (key, heap TID) pairs of every non-dead leaf item in
// the index (caller collects them with btree.PageLeafEntries across the leaf
// level). heapEntries are the index tuples that a fresh CREATE INDEX would emit
// for each live heap tuple — same btree.LeafEntry shape, formed by the caller's
// heap scan. The Bloom filter's no-false-negative guarantee means a heap entry
// whose identical (key, TID) is among indexLeafEntries is NEVER reported; only
// genuinely-absent entries are flagged (modulo the <2% false-positive rate that
// can mask some absences — never invent them).
//
// Like the other engines in this package it returns findings, never a Go error:
// an empty index with a non-empty heap correctly yields one report per heap row
// (every row lacks an index entry), and an empty heap yields none.
func VerifyBtreeHeapAllIndexed(indexLeafEntries, heapEntries []nbtree.LeafEntry, indexName, tableName string, seed uint64) []BtreeReport {
	filter := bloomCreate(int64(len(indexLeafEntries)), heapAllIndexedWorkMemKB, seed)
	for _, e := range indexLeafEntries {
		filter.bloomAddElement(fingerprintLeafEntry(e))
	}

	var reports []BtreeReport
	for _, h := range heapEntries {
		if filter.bloomLacksElement(fingerprintLeafEntry(h)) {
			reports = append(reports, BtreeReport{
				Block: h.TID.Block,
				Msg: fmt.Sprintf(
					"heap tuple (%d,%d) from table \"%s\" lacks matching index tuple within index \"%s\"",
					h.TID.Block, h.TID.Offset, tableName, indexName),
			})
		}
	}
	return reports
}
