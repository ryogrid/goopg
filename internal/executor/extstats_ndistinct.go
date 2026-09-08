package executor

// B-05a: multivariate ndistinct build + serialization.
//
// Oracle: postgres/src/backend/statistics/mvdistinct.c (build, serialize,
// deserialize, Duj1 estimator) and the on-disk layout declared in
// postgres/src/include/statistics/statistics.h:25-40 (MVNDistinctItem,
// MVNDistinct, STATS_NDISTINCT_MAGIC, STATS_NDISTINCT_TYPE_BASIC).
//
// Wire layout (all scalars little-endian — the oracle memcpy's native order
// on LE platforms; goopg targets LE only):
//
//	full varlena (4-byte length header, SET_VARSIZE form)
//	magic uint32  = 0xA352BFA4 (STATS_NDISTINCT_MAGIC)
//	type  uint32  = 1          (STATS_NDISTINCT_TYPE_BASIC)
//	nitems uint32
//	per item:
//	  ndistinct  float64
//	  nattributes int32            (NOTE: int, not AttrNumber — dependencies
//	                               use int16 here; the asymmetry is oracle's)
//	  attributes int16[nattributes] (1-based attnums, ascending within an item)

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

const (
	// extStatsMaxDimensions mirrors STATS_MAX_DIMENSIONS (statistics.h:19):
	// no statistics object spans more than 8 attributes. Wider objects are
	// truncated to their first 8 columns by the builder.
	extStatsMaxDimensions = 8

	// extNDistinctMagic mirrors STATS_NDISTINCT_MAGIC (statistics.h:22).
	extNDistinctMagic uint32 = 0xA352BFA4
	// extNDistinctTypeBasic mirrors STATS_NDISTINCT_TYPE_BASIC (statistics.h:23).
	extNDistinctTypeBasic uint32 = 1
)

// ExtNDistinctItem is one MVNDistinctItem: the estimated distinct count for
// one combination of columns. Attrs holds 1-based attnums in ascending order
// (the oracle stores data->attnums[combination[j]], i.e. real attnums, for a
// combinations of column INDEXES walked in lexicographic order).
type ExtNDistinctItem struct {
	NDistinct float64
	Attrs     []int16
}

// ExtNDistinct is one MVNDistinct: every k-combination (k = 2..n) of the
// object's columns. Items are ordered k=2 first, lexicographic within a k —
// exactly statext_ndistinct_build's emission order.
type ExtNDistinct struct {
	Items []ExtNDistinctItem
}

// extNDistinctNumCombinations mirrors num_combinations() (mvdistinct.c:575):
// all subsets of size >= 2, i.e. 2^n - (n+1). At the 8-column cap that is
// 247 items, so a built ndistinct blob always fits the catalog heap without
// TOAST (the reason MCV — unbounded by target — is deferred instead).
func extNDistinctNumCombinations(n int) int {
	return (1 << n) - (n + 1)
}

// buildExtNDistinct mirrors statext_ndistinct_build (mvdistinct.c:87-141).
//
// rows is the ANALYZE sample (row-major), colIdxs the object's member columns
// as indexes into each row, attnums their 1-based attnums (same order),
// totalRows the exact relation row count (reltuples). Returns nil when there
// is nothing to build (fewer than 2 columns after the dimension cap, or an
// empty sample — the Duj1 estimator divides by the sample size, so an empty
// sample has no defined estimate; the caller stores NULL for the kind).
func buildExtNDistinct(rows []Row, colIdxs []int, attnums []int16, totalRows float64) *ExtNDistinct {
	n := len(colIdxs)
	if n > extStatsMaxDimensions {
		n = extStatsMaxDimensions
		colIdxs = colIdxs[:n]
		attnums = attnums[:n]
	}
	if n < 2 || len(rows) == 0 {
		return nil
	}
	out := &ExtNDistinct{
		Items: make([]ExtNDistinctItem, 0, extNDistinctNumCombinations(n)),
	}
	for k := 2; k <= n; k++ {
		comb := make([]int, k)
		var rec func(start, depth int)
		rec = func(start, depth int) {
			if depth == k {
				attrs := make([]int16, k)
				for j, idx := range comb {
					attrs[j] = attnums[idx]
				}
				out.Items = append(out.Items, ExtNDistinctItem{
					NDistinct: ndistinctForCombination(totalRows, rows, colIdxs, comb),
					Attrs:     attrs,
				})
				return
			}
			// Lexicographic k-combinations of n indexes — the oracle's
			// generate_combinations_recurse (mvdistinct.c:657), which the
			// build loop consumes via generator_init/generator_next.
			for i := start; i < n; i++ {
				comb[depth] = i
				rec(i+1, depth+1)
			}
		}
		rec(0, 0)
	}
	return out
}

// ndistinctForCombination mirrors ndistinct_for_combination + its sort/count
// prologue (mvdistinct.c:424-517): sort the sample by the k columns, count
// the distinct full-width combinations (d) and how many of them appear exactly
// once (f1), then scale with the Duj1 estimator.
//
// The oracle sorts with each column type's default sort operator and
// collation (multi_sort_compare); goopg sorts with its engine comparator
// (compareDatum, NULLS LAST like the oracle's ssup_nulls_first=false).
// Equal-by-either-relation rows group identically — only exotic
// cross-type edges (e.g. numeric 1.0 vs 1.00, equal by numeric_cmp but
// distinct carriers here) can split a group the oracle merges. Documented
// deviation; byte-identical grouping holds for all fixed-scale data.
func ndistinctForCombination(totalRows float64, rows []Row, colIdxs []int, combination []int) float64 {
	k := len(combination)
	cols := make([]int, k)
	for i, c := range combination {
		cols[i] = colIdxs[c]
	}
	order := sortRowIndexesByColumns(rows, cols)
	d, f1 := 1, 0
	run := 1
	for i := 1; i < len(order); i++ {
		if rowsCompare(order[i], order[i-1], rows, cols) != 0 {
			if run == 1 {
				f1++
			}
			d++
			run = 0
		}
		run++
	}
	if run == 1 {
		f1++
	}
	return estimateExtNDistinct(totalRows, len(rows), d, f1)
}

// estimateExtNDistinct mirrors estimate_ndistinct, the Haas-Stokes Duj1
// estimator shared with compute_scalar_stats (mvdistinct.c:519-542):
// n*d / ((n - f1) + f1*n/N), clamped to [d, N] and rounded to an integer.
func estimateExtNDistinct(totalRows float64, numRows, d, f1 int) float64 {
	numer := float64(numRows) * float64(d)
	denom := float64(numRows-f1) + float64(f1)*float64(numRows)/totalRows
	ndistinct := numer / denom
	if ndistinct < float64(d) {
		ndistinct = float64(d)
	}
	if ndistinct > totalRows {
		ndistinct = totalRows
	}
	return math.Floor(ndistinct + 0.5)
}

// serializeExtNDistinct mirrors statext_ndistinct_serialize
// (mvdistinct.c:178-243): 4-byte SET_VARSIZE header + magic/type/nitems +
// per-item double/int32/int16s. The returned blob is the complete varlena,
// ready to splice into the heap tuple as a KindBytes datum.
func serializeExtNDistinct(nd *ExtNDistinct) []byte {
	length := 4 + 12 // varlena header + SizeOfHeader (magic, type, nitems)
	for _, it := range nd.Items {
		length += 8 + 4 + 2*len(it.Attrs) // SizeOfItem(natts)
	}
	buf := make([]byte, length)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(length)<<2) // SET_VARSIZE
	binary.LittleEndian.PutUint32(buf[4:8], extNDistinctMagic)
	binary.LittleEndian.PutUint32(buf[8:12], extNDistinctTypeBasic)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(len(nd.Items)))
	off := 16
	for _, it := range nd.Items {
		binary.LittleEndian.PutUint64(buf[off:off+8], math.Float64bits(it.NDistinct))
		off += 8
		// int32 member count — NOT int16 (see the file header).
		binary.LittleEndian.PutUint32(buf[off:off+4], uint32(len(it.Attrs)))
		off += 4
		for _, a := range it.Attrs {
			binary.LittleEndian.PutUint16(buf[off:off+2], uint16(a))
			off += 2
		}
	}
	return buf
}

// deserializeExtNDistinct mirrors statext_ndistinct_deserialize
// (mvdistinct.c:249-329). It accepts the full varlena (header included,
// either 4-byte or short 1-byte form) and validates magic, type, a non-zero
// item count, and the minimum size before reading — the same checks that make
// a corrupt pg_ndistinct blob an ERROR upstream rather than silent garbage.
func deserializeExtNDistinct(varlena []byte) (*ExtNDistinct, error) {
	body, err := skipExtVarlenaHeader(varlena)
	if err != nil {
		return nil, fmt.Errorf("ndistinct: %w", err)
	}
	if len(body) < 12 {
		return nil, fmt.Errorf("ndistinct: %d body bytes, need at least 12 (magic/type/nitems)", len(body))
	}
	if magic := binary.LittleEndian.Uint32(body[0:4]); magic != extNDistinctMagic {
		return nil, fmt.Errorf("ndistinct: invalid magic %08x (expected %08x)", magic, extNDistinctMagic)
	}
	if typ := binary.LittleEndian.Uint32(body[4:8]); typ != extNDistinctTypeBasic {
		return nil, fmt.Errorf("ndistinct: invalid type %d (expected %d)", typ, extNDistinctTypeBasic)
	}
	nitems := int(binary.LittleEndian.Uint32(body[8:12]))
	if nitems == 0 {
		return nil, fmt.Errorf("ndistinct: invalid zero-length item array")
	}
	// Minimum: every item spans >= 2 attributes (MinSizeOfItems).
	if len(body) < 12+nitems*(8+4+2*2) {
		return nil, fmt.Errorf("ndistinct: %d body bytes too small for %d items", len(body), nitems)
	}
	out := &ExtNDistinct{Items: make([]ExtNDistinctItem, 0, nitems)}
	off := 12
	for i := 0; i < nitems; i++ {
		if off+12 > len(body) {
			return nil, fmt.Errorf("ndistinct: item %d truncated", i)
		}
		nd := math.Float64frombits(binary.LittleEndian.Uint64(body[off : off+8]))
		nattrs := int(binary.LittleEndian.Uint32(body[off+8 : off+12]))
		off += 12
		if nattrs < 2 || nattrs > extStatsMaxDimensions {
			return nil, fmt.Errorf("ndistinct: item %d has %d attributes, want 2..%d", i, nattrs, extStatsMaxDimensions)
		}
		if off+2*nattrs > len(body) {
			return nil, fmt.Errorf("ndistinct: item %d attributes truncated", i)
		}
		attrs := make([]int16, nattrs)
		for j := range attrs {
			attrs[j] = int16(binary.LittleEndian.Uint16(body[off : off+2]))
			off += 2
		}
		out.Items = append(out.Items, ExtNDistinctItem{NDistinct: nd, Attrs: attrs})
	}
	if off != len(body) {
		return nil, fmt.Errorf("ndistinct: %d trailing bytes after %d items", len(body)-off, nitems)
	}
	return out, nil
}

// skipExtVarlenaHeader strips one varlena length header (4-byte
// uncompressed, 1-byte short, or inline-PGLZ-compressed form) and returns
// the payload. The B-05a serializers always emit the 4-byte SET_VARSIZE
// form; the short/compressed arms exist so the deserializers also accept
// blobs written by real PG (e.g. a heap copied from a PG-built cluster).
func skipExtVarlenaHeader(varlena []byte) ([]byte, error) {
	if len(varlena) == 0 {
		return nil, fmt.Errorf("truncated varlena")
	}
	if varlena[0]&0x01 == 0x01 {
		if varlena[0] == 0x01 {
			return nil, fmt.Errorf("external (TOAST pointer) varlena not supported — catalog heap does not TOAST")
		}
		total := int(varlena[0] >> 1)
		if total < 1 || total > len(varlena) {
			return nil, fmt.Errorf("truncated short varlena")
		}
		return varlena[1:total], nil
	}
	if len(varlena) < 4 {
		return nil, fmt.Errorf("truncated 4-byte varlena header")
	}
	if varlena[0]&0x03 == 0x02 {
		return nil, fmt.Errorf("compressed varlena not supported")
	}
	total := int(binary.LittleEndian.Uint32(varlena[:4]) >> 2)
	if total < 4 || total > len(varlena) {
		return nil, fmt.Errorf("truncated 4-byte varlena")
	}
	return varlena[4:total], nil
}

// sortRowIndexesByColumns returns the sample row indexes ordered by the
// given columns (NULLS LAST), via a stable sort so equal keys keep sample
// order — determinism the degree/group counting below relies on only for
// tie-breaking, never for correctness.
func sortRowIndexesByColumns(rows []Row, cols []int) []int {
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return rowsCompare(order[a], order[b], rows, cols) < 0
	})
	return order
}

// rowsCompare orders two sample rows by cols, mirroring multi_sort_compare
// (extended_stats.c:862) with ssup_nulls_first=false: NULL sorts after every
// non-NULL value. A comparator error (exotic type pair) falls back to the
// rendered text form so a build never fails on unorderable data.
func rowsCompare(a, b int, rows []Row, cols []int) int {
	ra, rb := rows[a], rows[b]
	for _, c := range cols {
		var da, db Datum
		if c < len(ra) {
			da = ra[c]
		}
		if c < len(rb) {
			db = rb[c]
		}
		an, bn := da.IsNull(), db.IsNull()
		if an && bn {
			continue
		}
		if an {
			return 1 // NULLS LAST
		}
		if bn {
			return -1
		}
		cmp, err := compareDatum(da, db, 0)
		if err != nil {
			cmp = strCompareFallback(da.Format(), db.Format())
		}
		if cmp != 0 {
			return cmp
		}
	}
	return 0
}

func strCompareFallback(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
