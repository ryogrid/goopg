package executor

// B-05a: functional-dependency build + serialization.
//
// Oracle: postgres/src/backend/statistics/dependencies.c (variation
// generator, dependency_degree, build, serialize, deserialize) and the
// on-disk layout in postgres/src/include/statistics/statistics.h:43-63
// (MVDependency, MVDependencies, STATS_DEPS_MAGIC, STATS_DEPS_TYPE_BASIC).
//
// Wire layout (little-endian, full varlena with 4-byte header):
//
//	magic uint32 = 0xB4549A2C (STATS_DEPS_MAGIC)
//	type  uint32 = 1          (STATS_DEPS_TYPE_BASIC)
//	ndeps uint32
//	per dependency:
//	  degree      float64
//	  nattributes int16           (NOTE: int16 here — ndistinct uses int32;
//	                               the asymmetry is oracle's, see SizeOfItem)
//	  attributes  int16[nattributes] (1-based attnums; the LAST element is
//	                               the implied (right-hand-side) column)

import (
	"encoding/binary"
	"fmt"
	"math"
)

const (
	// extDepsMagic mirrors STATS_DEPS_MAGIC (statistics.h:43).
	extDepsMagic uint32 = 0xB4549A2C
	// extDepsTypeBasic mirrors STATS_DEPS_TYPE_BASIC (statistics.h:44).
	extDepsTypeBasic uint32 = 1
)

// ExtDependency is one MVDependency: Attrs[0:len-1] functionally determine
// Attrs[len-1] with the given degree of validity in [0, 1].
type ExtDependency struct {
	Degree float64
	Attrs  []int16
}

// ExtDependencies is one MVDependencies, in build order: k=2 variations
// first through k=n, generator order within a k (see generateExtVariations).
// Only dependencies with degree > 0 are stored — a zero degree means every
// group is contradicted, i.e. no information at all.
type ExtDependencies struct {
	Deps []ExtDependency
}

// buildExtDependencies mirrors statext_dependencies_build
// (dependencies.c:347-437): enumerate every variation of k=2..n columns and
// keep those with a positive degree of validity.
//
// rows/colIdxs/attnums carry the same meaning as in buildExtNDistinct.
// Returns nil when there is nothing to build (fewer than 2 columns after
// the dimension cap, an empty sample), or when no variation carries any
// information (all degrees zero) — in all three cases the caller stores
// NULL for the kind, exactly as statext_store does when dependencies==NULL.
//
// Cost note: variations grow combinatorially (sum over k of C(n,k-1)*(n-k+1)
// sorts of the sample); with the 8-column cap that is worst-case ~11k
// candidate sorts. Real objects span 2-4 columns (9..~50 sorts). No
// oracle-divergent cap is imposed — truncating the enumeration would drop
// real dependencies PG keeps.
func buildExtDependencies(rows []Row, colIdxs []int, attnums []int16) *ExtDependencies {
	n := len(colIdxs)
	if n > extStatsMaxDimensions {
		n = extStatsMaxDimensions
		colIdxs = colIdxs[:n]
		attnums = attnums[:n]
	}
	if n < 2 || len(rows) == 0 {
		return nil
	}
	var out *ExtDependencies
	for k := 2; k <= n; k++ {
		for _, variation := range generateExtVariations(n, k) {
			degree := dependencyDegree(rows, colIdxs, variation)
			if degree == 0.0 {
				continue
			}
			attrs := make([]int16, k)
			for i, idx := range variation {
				attrs[i] = attnums[idx]
			}
			if out == nil {
				out = &ExtDependencies{}
			}
			out.Deps = append(out.Deps, ExtDependency{Degree: degree, Attrs: attrs})
		}
	}
	return out
}

// generateExtVariations mirrors generate_dependencies_recurse
// (dependencies.c:90-153): the first k-1 positions range over ascending
// index combinations (order among them is irrelevant: (a,b=>c) == (b,a=>c))
// while the last position — the implied column — ranges freely over every
// index not already taken. Variations are indexes into the object's column
// list (the oracle stores data->attnums[dependency[i]] at build time, which
// the caller above translates the same way).
func generateExtVariations(n, k int) [][]int {
	var out [][]int
	current := make([]int, k)
	var rec func(index, start int)
	rec = func(index, start int) {
		if index < k-1 {
			for i := start; i < n; i++ {
				current[index] = i
				rec(index+1, i+1)
			}
			return
		}
		for i := 0; i < n; i++ {
			dup := false
			for j := 0; j < index; j++ {
				if current[j] == i {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			current[index] = i
			cp := make([]int, k)
			copy(cp, current)
			out = append(out, cp)
		}
	}
	rec(0, 0)
	return out
}

// dependencyDegree mirrors dependency_degree (dependencies.c:220-329) for
// one variation: sort the sample by all k columns, split it into groups by
// the first k-1, and count a group as supporting when its last-column value
// is constant. The degree is supporting_rows / total_rows.
//
// NULLS LAST ordering (ssup_nulls_first=false) applies here too: a NULL
// driver groups with NULL drivers, a NULL dependent with NULL dependents —
// NULLs participate as ordinary values, exactly as in the oracle.
func dependencyDegree(rows []Row, colIdxs []int, variation []int) float64 {
	k := len(variation)
	cols := make([]int, k)
	for i, v := range variation {
		cols[i] = colIdxs[v]
	}
	order := sortRowIndexesByColumns(rows, cols)
	drivers := cols[:k-1]
	last := cols[k-1:]

	supporting := 0
	groupSize := 1
	violations := 0
	flush := func() {
		if violations == 0 {
			supporting += groupSize
		}
		groupSize = 1
		violations = 0
	}
	for i := 1; i <= len(order); i++ {
		if i == len(order) ||
			rowsCompare(order[i-1], order[i], rows, drivers) != 0 {
			flush()
			continue
		}
		if rowsCompare(order[i-1], order[i], rows, last) != 0 {
			violations++
		}
		groupSize++
	}
	return float64(supporting) / float64(len(rows))
}

// serializeExtDependencies mirrors statext_dependencies_serialize
// (dependencies.c:443-493): 4-byte SET_VARSIZE header + magic/type/ndeps +
// per-dependency double/int16/int16s. Complete varlena, KindBytes-ready.
func serializeExtDependencies(deps *ExtDependencies) []byte {
	length := 4 + 12 // varlena header + SizeOfHeader (magic, type, ndeps)
	for _, d := range deps.Deps {
		length += 8 + 2 + 2*len(d.Attrs) // SizeOfItem(natts)
	}
	buf := make([]byte, length)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(length)<<2) // SET_VARSIZE
	binary.LittleEndian.PutUint32(buf[4:8], extDepsMagic)
	binary.LittleEndian.PutUint32(buf[8:12], extDepsTypeBasic)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(len(deps.Deps)))
	off := 16
	for _, d := range deps.Deps {
		binary.LittleEndian.PutUint64(buf[off:off+8], math.Float64bits(d.Degree))
		off += 8
		// int16 member count — NOT int32 (see the file header).
		binary.LittleEndian.PutUint16(buf[off:off+2], uint16(len(d.Attrs)))
		off += 2
		for _, a := range d.Attrs {
			binary.LittleEndian.PutUint16(buf[off:off+2], uint16(a))
			off += 2
		}
	}
	return buf
}

// deserializeExtDependencies mirrors statext_dependencies_deserialize
// (dependencies.c:499-587): full-varlena input, magic/type/zero-count/size
// validation, exact-consumption check.
func deserializeExtDependencies(varlena []byte) (*ExtDependencies, error) {
	body, err := skipExtVarlenaHeader(varlena)
	if err != nil {
		return nil, fmt.Errorf("dependencies: %w", err)
	}
	if len(body) < 12 {
		return nil, fmt.Errorf("dependencies: %d body bytes, need at least 12 (magic/type/ndeps)", len(body))
	}
	if magic := binary.LittleEndian.Uint32(body[0:4]); magic != extDepsMagic {
		return nil, fmt.Errorf("dependencies: invalid magic %08x (expected %08x)", magic, extDepsMagic)
	}
	if typ := binary.LittleEndian.Uint32(body[4:8]); typ != extDepsTypeBasic {
		return nil, fmt.Errorf("dependencies: invalid type %d (expected %d)", typ, extDepsTypeBasic)
	}
	ndeps := int(binary.LittleEndian.Uint32(body[8:12]))
	if ndeps == 0 {
		return nil, fmt.Errorf("dependencies: invalid zero-length dependency array")
	}
	// Minimum: every dependency spans >= 2 attributes (MinSizeOfItem).
	if len(body) < 12+ndeps*(8+2+2*2) {
		return nil, fmt.Errorf("dependencies: %d body bytes too small for %d dependencies", len(body), ndeps)
	}
	out := &ExtDependencies{Deps: make([]ExtDependency, 0, ndeps)}
	off := 12
	for i := 0; i < ndeps; i++ {
		if off+10 > len(body) {
			return nil, fmt.Errorf("dependencies: item %d truncated", i)
		}
		degree := math.Float64frombits(binary.LittleEndian.Uint64(body[off : off+8]))
		nattrs := int(binary.LittleEndian.Uint16(body[off+8 : off+10]))
		off += 10
		if nattrs < 2 || nattrs > extStatsMaxDimensions {
			return nil, fmt.Errorf("dependencies: item %d has %d attributes, want 2..%d", i, nattrs, extStatsMaxDimensions)
		}
		if off+2*nattrs > len(body) {
			return nil, fmt.Errorf("dependencies: item %d attributes truncated", i)
		}
		attrs := make([]int16, nattrs)
		for j := range attrs {
			attrs[j] = int16(binary.LittleEndian.Uint16(body[off : off+2]))
			off += 2
		}
		out.Deps = append(out.Deps, ExtDependency{Degree: degree, Attrs: attrs})
	}
	if off != len(body) {
		return nil, fmt.Errorf("dependencies: %d trailing bytes after %d items", len(body)-off, ndeps)
	}
	return out, nil
}
