// M0106-0010 batched-50: populate pg_proc_proname_args_nsp_index (OID 2691)
// with one IndexTuple per pg_proc heap row so PG18's
// SearchSysCacheList1(PROCNAMEARGSNSP, ...) — the backing syscache for
// FuncnameGetCandidates / ParseFuncOrColumn — finds the bootstrap-seeded
// pg_proc rows by proname. Without this, every PG-standby parse-analyse
// of `SELECT count(*) FROM <user table>` fails with
//   ERROR: 42883 function count() does not exist
// because the empty btree placeholder previously left at base/{1,5}/2691
// returns no candidates.
//
// The index is btree(proname name_ops, proargtypes oidvector_ops,
// pronamespace oid_ops); see postgres/src/include/catalog/pg_proc.h:177.
// proargtypes is a varlena (oidvector) so every IndexTuple is variable
// length: PG's index_form_tuple stamps INDEX_VAR_MASK on t_info and the
// per-row tuple size depends on the function's argument count.
//
// Sort order matches PG's btoidvectorcmp
// (postgres/src/backend/utils/adt/oid.c) — first by proname (NameData
// strncmp), then by oidvector length, then by oidvector elements, then
// by pronamespace.

package initdb

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/goopg/goopg/internal/storage"
)

const (
	// INDEX_VAR_MASK from postgres/src/include/access/itup.h:74. Set on
	// every IndexTuple whose data contains at least one varlena
	// attribute (we have proargtypes = oidvector).
	indexVarMask uint16 = 0x4000
)

// pgEncodeOidvectorForIndex returns the on-disk binary form of an
// oidvector value (pg_proc.proargtypes). The layout matches
// `oidVectorBytes` in initdb.go and PG18's buildoidvector:
//
//	[0..3]   vl_len_   = (24 + 4*n) << 2   (4-byte VARSIZE_4B encoding)
//	[4..7]   ndim      = 1
//	[8..11]  dataoffset = 0
//	[12..15] elemtype  = OIDOID (26)
//	[16..19] dim1      = n
//	[20..23] lbound1   = 0
//	[24..]   values    = n * uint32 little-endian
func pgEncodeOidvectorForIndex(oids []uint32) []byte {
	const headerSize = 24
	total := headerSize + 4*len(oids)
	buf := make([]byte, total)
	le := binary.LittleEndian
	le.PutUint32(buf[0:4], uint32(total)<<2)
	le.PutUint32(buf[4:8], 1)
	le.PutUint32(buf[8:12], 0)
	le.PutUint32(buf[12:16], 26)
	le.PutUint32(buf[16:20], uint32(len(oids)))
	le.PutUint32(buf[20:24], 0)
	for i, o := range oids {
		le.PutUint32(buf[24+i*4:28+i*4], o)
	}
	return buf
}

// pgBuildIndexTupleProcKey constructs the variable-length IndexTuple
// for one row of pg_proc_proname_args_nsp_index. Tuple layout (no
// nulls, hoff = MAXALIGN(sizeof(IndexTupleData)) = 8):
//
//	[0..1]    ip_blkid.bi_hi
//	[2..3]    ip_blkid.bi_lo
//	[4..5]    ip_posid
//	[6..7]    t_info (size in low 13 bits | INDEX_VAR_MASK because
//	          proargtypes is varlena; no INDEX_NULL_MASK)
//	[8..71]   proname   (NameData, typalign='c', 64 bytes NUL-padded)
//	[72..]    proargtypes (oidvector, typalign='i', 24+4n bytes)
//	[..]      pronamespace (oid, typalign='i', 4 bytes — already aligned
//	          because oidvector size is always a multiple of 4)
//	[..size]  MAXALIGN'd to 8-byte boundary (trailing zero pad)
func pgBuildIndexTupleProcKey(heapBlk uint32, heapOff uint16, proname string, proargtypes []uint32, pronamespace uint32) []byte {
	const (
		hoff        = 8
		nameDataLen = 64
	)
	oidVec := pgEncodeOidvectorForIndex(proargtypes)
	rawSize := hoff + nameDataLen + len(oidVec) + 4
	aligned := (rawSize + 7) &^ 7
	out := make([]byte, aligned)
	le := binary.LittleEndian

	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], (uint16(aligned)&indexSizeMask)|indexVarMask)

	n := len(proname)
	if n > nameDataLen {
		n = nameDataLen
	}
	copy(out[hoff:hoff+n], proname[:n])
	off := hoff + nameDataLen
	copy(out[off:off+len(oidVec)], oidVec)
	off += len(oidVec)
	le.PutUint32(out[off:off+4], pronamespace)
	return out
}

// pgBuildBtreeBulkLoadVariable packs sorted variable-size IndexTuples
// into a complete on-disk btree file (metapage + leaves + optional
// internal root) compatible with PG18's nbtsort bulk-load output. It
// generalises pgBuildBtreeBulkLoadSized to non-fixed tuple sizes — every
// non-rightmost leaf reserves space for a P_HIKEY of the worst-case
// (largest input) size so packing is monotonic and never has to be
// rewound after the fact.
//
// Limitations: the internal level is assumed to fit in a single root
// page. For our pg_proc scale (~3400 entries averaging ~104 B/tuple =
// ~75 entries per leaf = ~50 leaves), 50 downlinks of similar size fit
// easily in the 8152-byte root payload. If a future caller overflows
// this budget the function errors out so the regression is surfaced
// rather than silently producing a corrupt tree.
func pgBuildBtreeBulkLoadVariable(sortedTuples [][]byte, nkeyatts uint16) ([]byte, error) {
	if len(sortedTuples) == 0 {
		leaf, err := pgBuildBtreeLeafRootPage(nil)
		if err != nil {
			return nil, fmt.Errorf("bulk-load (variable) empty leaf-root: %w", err)
		}
		meta := pgBuildBtreeMetapageWithRoot(1, 0)
		out := make([]byte, 0, 2*storage.BlockSize)
		out = append(out, meta...)
		out = append(out, leaf...)
		return out, nil
	}

	totalNeeded := 0
	maxTupleSize := 0
	for _, t := range sortedTuples {
		if len(t)%8 != 0 {
			return nil, fmt.Errorf("pgBuildBtreeBulkLoadVariable: tuple not MAXALIGN'd: len=%d", len(t))
		}
		totalNeeded += len(t) + 4
		if len(t) > maxTupleSize {
			maxTupleSize = len(t)
		}
	}
	if totalNeeded <= leafPayloadBytes {
		leaf, err := pgBuildBtreeLeafRootPage(sortedTuples)
		if err != nil {
			return nil, fmt.Errorf("bulk-load (variable) leaf-root: %w", err)
		}
		meta := pgBuildBtreeMetapageWithRoot(1, 0)
		out := make([]byte, 0, 2*storage.BlockSize)
		out = append(out, meta...)
		out = append(out, leaf...)
		return out, nil
	}

	hikeyReservation := maxTupleSize + 4

	var leafGroups [][][]byte
	pos := 0
	for pos < len(sortedTuples) {
		remaining := 0
		for i := pos; i < len(sortedTuples); i++ {
			remaining += len(sortedTuples[i]) + 4
		}
		if remaining <= leafPayloadBytes {
			leafGroups = append(leafGroups, sortedTuples[pos:])
			break
		}
		budget := leafPayloadBytes - hikeyReservation
		used := 0
		end := pos
		for end < len(sortedTuples) {
			slot := len(sortedTuples[end]) + 4
			if used+slot > budget {
				break
			}
			used += slot
			end++
		}
		if end == pos {
			return nil, fmt.Errorf("pgBuildBtreeBulkLoadVariable: tuple %d (size=%d) does not fit on a leaf (budget=%d)", pos, len(sortedTuples[pos]), budget)
		}
		leafGroups = append(leafGroups, sortedTuples[pos:end])
		pos = end
	}
	nLeaves := len(leafGroups)

	leaves := make([][]byte, nLeaves)
	for li, group := range leafGroups {
		isRightmost := li == nLeaves-1
		prevBlock := uint32(pNone)
		nextBlock := uint32(pNone)
		if li > 0 {
			prevBlock = uint32(li)
		}
		if !isRightmost {
			nextBlock = uint32(li + 2)
		}
		var highKey []byte
		if !isRightmost {
			highKey = pgBuildBtreeLeafHighKey(leafGroups[li+1][0], nkeyatts)
		}
		page, err := pgBuildBtreeLeafPage(group, highKey, prevBlock, nextBlock)
		if err != nil {
			return nil, fmt.Errorf("leaf %d: %w", li, err)
		}
		leaves[li] = page
	}

	downlinks := make([][]byte, nLeaves)
	downlinks[0] = pgBuildBtreeMinusInfinityDownlink(1)
	rootNeeded := len(downlinks[0]) + 4
	for li := 1; li < nLeaves; li++ {
		downlinks[li] = pgBuildBtreeInternalDownlink(leafGroups[li][0], uint32(li+1), nkeyatts)
		rootNeeded += len(downlinks[li]) + 4
	}
	if rootNeeded > leafPayloadBytes {
		return nil, fmt.Errorf("pgBuildBtreeBulkLoadVariable: %d downlinks (%d bytes) exceed single internal root capacity (%d); multi-level internal tree not yet implemented",
			len(downlinks), rootNeeded, leafPayloadBytes)
	}
	rootPage, err := pgBuildBtreeInternalRootPage(downlinks)
	if err != nil {
		return nil, fmt.Errorf("root: %w", err)
	}

	rootBlock := uint32(nLeaves + 1)
	meta := pgBuildBtreeMetapageWithRoot(rootBlock, 1)
	out := make([]byte, 0, (nLeaves+2)*storage.BlockSize)
	out = append(out, meta...)
	for _, leaf := range leaves {
		out = append(out, leaf...)
	}
	out = append(out, rootPage...)
	return out, nil
}

// bootstrapPgProcPronameArgsNspIndex (M0106-0010 batched-50) overwrites
// the empty btree placeholder at base/{1,5}/2691 and global/2691 with a
// fully populated btree containing one IndexTuple per pg_proc heap row.
// Required so PG18's parse-analyse can resolve `count(*)`,
// `lower(text)`, and every other built-in function looked up by name on
// the PG standby.
//
// tids must align 1:1 with pgProcInitialEntries() (the call site mirrors
// bootstrapPgProcOidIndex).
func bootstrapPgProcPronameArgsNspIndex(dataDir string, tids []heapTID) error {
	entries := pgProcInitialEntries()
	if len(entries) != len(tids) {
		return fmt.Errorf("pg_proc_proname_args_nsp_index: entries=%d tids=%d", len(entries), len(tids))
	}
	type indexed struct {
		name string
		args []uint32
		nsp  uint32
		blk  uint32
		off  uint16
	}
	items := make([]indexed, len(entries))
	for i, e := range entries {
		// Mirror pgProcRow's nil-ArgTypes default: a nil slice means
		// "single internal argument" (OID 2281); only the explicit empty
		// []uint32{} represents a true zero-argument function.
		args := e.ArgTypes
		if args == nil {
			args = []uint32{2281}
		}
		// M0133-S2: pronamespace is pg_catalog (11) unless the entry opts in —
		// the information_schema helpers live in namespace 13273. Hardcoding 11
		// here made FuncnameGetCandidates on the hosted standby miss them the
		// same way the pg_type_typname_nsp_index gap (0133-0001 F1) did.
		nsp := e.Namespace
		if nsp == 0 {
			nsp = 11
		}
		items[i] = indexed{
			name: e.Name,
			args: args,
			nsp:  nsp,
			blk:  tids[i].Block,
			off:  tids[i].Offset,
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].name != items[j].name {
			return items[i].name < items[j].name
		}
		a, b := items[i].args, items[j].args
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		for k := 0; k < len(a); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return items[i].nsp < items[j].nsp
	})

	tuples := make([][]byte, len(items))
	for i, it := range items {
		tuples[i] = pgBuildIndexTupleProcKey(it.blk, it.off, it.name, it.args, it.nsp)
	}
	const nkeyatts uint16 = 3
	file, err := pgBuildBtreeBulkLoadVariable(tuples, nkeyatts)
	if err != nil {
		return fmt.Errorf("pg_proc_proname_args_nsp_index bulk-load: %w", err)
	}

	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
		filepath.Join(dataDir, "global"),
	} {
		if err := os.WriteFile(filepath.Join(dir, "2691"), file, 0o600); err != nil {
			return fmt.Errorf("write pg_proc_proname_args_nsp_index in %s: %w", dir, err)
		}
	}
	return nil
}
