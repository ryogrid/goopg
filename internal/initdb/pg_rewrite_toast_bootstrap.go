// pg_rewrite TOAST relation pair — DECLARE_TOAST(pg_rewrite, 2838, 2839).
//
// M0131-S20.1 (2026-08-12), design docs/design/0131-0035-pg-rewrite-toast.md.
//
// Every `ev_action` blob goopg seeds today is stored INLINE (pglz-compressed
// varlena, see pglz.go). That works only while the compressed value still fits
// a heap tuple — MaxHeapTupleSize ≈ 8160 B at BLCKSZ=8192
// (postgres/src/include/access/htup_details.h:615). Eight upstream system views
// breach it (`pg_indexes` 9002 B … `pg_seclabels` 35379 B, sizes measured on a
// throwaway PG 18.3 oracle), which is why the on-disk system-view corpus stops
// at 71 of upstream's 80. Upstream stores those eight out of line, in
// pg_rewrite's TOAST relation:
//
//	postgres/src/include/catalog/pg_rewrite.h:54
//	  DECLARE_TOAST(pg_rewrite, 2838, 2839);
//
// goopg bootstrapped NEITHER relation, so a `varatt_external` pointer written
// into `ev_action` would have had no relation to point at. This file lands the
// relation pair itself — pg_class + pg_attribute + pg_index rows, the physical
// files, and `pg_class(2618).reltoastrelid = 2838`. The toast heap is EMPTY at
// this stage: writing chunks (and adopting the eight views) is S20.2, and this
// slice is deliberately separated from it because an empty, well-formed toast
// relation is exactly what upstream's own initdb produces before the first
// oversize datum is inserted, and it is independently verifiable.
//
// Oracle facts this file encodes (PG 18.3, freshly initdb'd, probed directly):
//
//	pg_class 2838: relname pg_toast_2618, relnamespace 99 (pg_toast),
//	               reltype 0, relam 2 (heap), relfilenode 2838,
//	               reltoastrelid 0, relhasindex t, relkind 't', relnatts 3
//	pg_class 2839: relname pg_toast_2618_index, relnamespace 99, reltype 0,
//	               relam 403 (btree), relkind 'i', relnatts 2, relhasindex f
//	pg_class 2618: reltoastrelid 2838
//	pg_attribute:  2838 → chunk_id oid(26), chunk_seq int4(23),
//	               chunk_data bytea(17); all attnotnull=f, attstorage='p'
//	               (a TOAST chunk is never itself toasted)
//	               2839 → chunk_id, chunk_seq
//	pg_index 2839: indrelid 2838, indnatts/indnkeyatts 2, unique + primary,
//	               indkey "1 2", indclass "1981 1978" (oid_ops, int4_ops),
//	               indcollation "0 0", indoption "0 0"
//	NO pg_type row (reltype 0) and NO pg_depend rows — both verified absent
//	upstream, so the pair must stay OUT of the nailedLocalRels list that
//	drives bootstrapPgTypeTuples.
//
// Divergence carried forward (same as every other goopg catalog, ledgered):
// upstream emits six negative-attnum system-attribute rows per table into
// pg_attribute; goopg emits user columns only. The toast pair follows goopg's
// existing convention rather than introducing a second one here.

package initdb

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/goopg/goopg/internal/storage"
)

const (
	// pgToastNamespaceOID is PG_TOAST_NAMESPACE (postgres/src/include/catalog/
	// pg_namespace.dat) — the schema every TOAST relation lives in. goopg's
	// initdb already seeds the pg_namespace row (initdb.go:2465).
	pgToastNamespaceOID uint32 = 99

	// toastMaxChunkSize is TOAST_MAX_CHUNK_SIZE for BLCKSZ=8192
	// (postgres/src/include/access/heaptoast.h). Confirmed against the oracle:
	// every full chunk of pg_toast.pg_toast_2618 is exactly 1996 bytes, and
	// pg_control reports toast_max_chunk_size = 1996 (pg_control_test.go:192).
	// Unused until S20.2 writes chunks; declared here so the two halves of the
	// slice cannot disagree about the constant.
	toastMaxChunkSize = 1996
)

// toastRelPair is one DECLARE_TOAST(parent, toastRel, toastIdx) declaration.
// The list is a closed set — goopg bootstraps a TOAST relation only where
// upstream declares one, because the OIDs are pinned upstream constants and a
// hosted PG resolves them by OID, not by name.
type toastRelPair struct {
	Parent   uint32 // heap whose oversize columns are stored out of line
	ToastRel uint32 // pg_class OID of the TOAST heap (relkind 't')
	ToastIdx uint32 // pg_class OID of its UNIQUE btree (chunk_id, chunk_seq)
	RelName  string // pg_toast_<Parent>; the index is <RelName>_index
}

// nailedToastPairs returns every TOAST relation goopg bootstraps at initdb.
// Exactly one today: pg_rewrite's, whose ev_action column is the only
// bootstrap-time datum that can exceed an inline heap tuple.
func nailedToastPairs() []toastRelPair {
	return []toastRelPair{
		{Parent: 2618, ToastRel: 2838, ToastIdx: 2839, RelName: "pg_toast_2618"},
	}
}

// nailedToastRels renders the TOAST pairs as nailedRel rows so they flow into
// the pg_class and pg_attribute heaps (and the pg_class btrees built over
// them). They are deliberately NOT members of nailedLocalRels: that list also
// drives bootstrapPgTypeTuples and writeRelcacheInitFile, and a TOAST relation
// has neither a pg_type row (reltype 0, verified upstream) nor a place in
// pg_internal.init (PG never opens a TOAST relation during the critical
// relcache phase — it reaches one only while detoasting a datum, long after
// the init file has been consumed).
func nailedToastRels() []nailedRel {
	var rels []nailedRel
	for _, p := range nailedToastPairs() {
		rels = append(rels,
			nailedRel{
				OID: p.ToastRel, RelName: p.RelName, RelType: 0,
				RelKind: 't', RelNatts: 3, IsShared: false,
				Attrs: []nailedAttr{
					{Name: "chunk_id", TypeOID: 26, Num: 1, Len: 4},
					{Name: "chunk_seq", TypeOID: 23, Num: 2, Len: 4},
					{Name: "chunk_data", TypeOID: 17, Num: 3, Len: -1},
				},
			},
			nailedRel{
				OID: p.ToastIdx, RelName: p.RelName + "_index", RelType: 0,
				RelKind: 'i', RelNatts: 2, IsShared: false,
				Attrs: []nailedAttr{
					{Name: "chunk_id", TypeOID: 26, Num: 1, Len: 4},
					{Name: "chunk_seq", TypeOID: 23, Num: 2, Len: 4},
				},
			},
		)
	}
	return rels
}

// isNailedToastRelOID reports whether the OID names a bootstrapped TOAST heap
// (not its index). Used for the pg_class flags that differ from a plain
// catalog heap: relhasindex, and the 'p' attstorage its columns carry.
func isNailedToastRelOID(oid uint32) bool {
	for _, p := range nailedToastPairs() {
		if p.ToastRel == oid {
			return true
		}
	}
	return false
}

// isNailedToastRel reports whether a nailed relation is a bootstrapped TOAST
// heap OR its index. Both keep pg_class.reltype = 0 upstream, so they must be
// exempt from pgClassRow's "reltype defaults to the relation OID" fallback —
// that fallback would name a pg_type row bootstrapPgTypeTuples never writes,
// and a hosted PG's RelationBuildDesc asserts tdtypeid against it.
func isNailedToastRel(rel nailedRel) bool {
	for _, p := range nailedToastPairs() {
		if p.ToastRel == rel.OID || p.ToastIdx == rel.OID {
			return true
		}
	}
	return false
}

// pgClassRelnamespaceFor returns the pg_class.relnamespace for a nailed
// relation: PG_TOAST_NAMESPACE (99) for a bootstrapped TOAST relation or its
// index, PG_CATALOG_NAMESPACE (11) for everything else. A wrong namespace here
// is not cosmetic — `pg_class_relname_nsp_index` is keyed on it, so a hosted
// PG's RELNAMENSP syscache probe would miss.
func pgClassRelnamespaceFor(oid uint32) uint32 {
	for _, p := range nailedToastPairs() {
		if p.ToastRel == oid || p.ToastIdx == oid {
			return pgToastNamespaceOID
		}
	}
	return 11
}

// pgClassReltoastrelidFor returns the pg_class.reltoastrelid a nailed relation
// must carry: the TOAST heap OID for a parent that declares one, 0 otherwise.
// PG reads this field to open the TOAST relation while detoasting
// (heap_fetch_toast_slice → table_open(toastrelid)); a 0 here turns every
// external pointer in the parent into "missing chunk number 0 for toast value".
func pgClassReltoastrelidFor(oid uint32) uint32 {
	for _, p := range nailedToastPairs() {
		if p.Parent == oid {
			return p.ToastRel
		}
	}
	return 0
}

// pgAttrStorageChar returns the attstorage char for one nailed column.
// TOAST-relation columns are 'p' (plain) upstream — a chunk is never itself
// toasted, and heap_toast_insert_or_update relies on that to avoid recursion —
// whereas the type-derived default for bytea would be 'x' (extended).
func pgAttrStorageChar(relOID, typeOID uint32) string {
	if isNailedToastRelOID(relOID) {
		return "p"
	}
	return pgTypeStorageChar(typeOID)
}

// bootstrapToastRelationFiles writes the physical files for every bootstrapped
// TOAST pair into base/1 and base/5 (the same two databases every other
// per-database catalog bootstrapper targets; base/4 is re-materialised from
// base/5 afterwards by M0131-S15).
//
// The heap gets one initialised, empty page rather than a zero-length file:
// goopg's own smgr recreates a removed fork as empty (see the
// goopg_smgr_ocreate_recreates_removed_files note) and PG's
// ReadBufferExtended on block 0 of a zero-length relation is a different code
// path than on an initialised page. The index gets the standard metapage-only
// image (btm_root = P_NONE = "index has never had a tuple inserted"), which is
// exactly right while the heap is empty.
func bootstrapToastRelationFiles(dataDir string) error {
	pairs := nailedToastPairs()
	if len(pairs) == 0 {
		return nil
	}
	heapPage := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(heapPage); err != nil {
		return fmt.Errorf("toast heap page: %w", err)
	}
	btreePage := makeBtreeRootPage()
	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
	} {
		for _, p := range pairs {
			heapPath := filepath.Join(dir, strconv.FormatUint(uint64(p.ToastRel), 10))
			if err := os.WriteFile(heapPath, heapPage, 0o600); err != nil {
				return fmt.Errorf("write toast heap %d: %w", p.ToastRel, err)
			}
			idxPath := filepath.Join(dir, strconv.FormatUint(uint64(p.ToastIdx), 10))
			if err := os.WriteFile(idxPath, btreePage, 0o600); err != nil {
				return fmt.Errorf("write toast index %d: %w", p.ToastIdx, err)
			}
		}
	}
	return nil
}
