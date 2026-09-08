package executor

import "github.com/goopg/goopg/internal/catalog"

// resolveEnumColumns answers, ONCE PER SCAN, which columns are user-defined
// enum types. Scan operators inject KindEnum datums for those columns so
// ORDER BY follows declaration order rather than label text (M0097-0022).
//
// WHY THIS IS A SHARED HELPER AND NOT AN INLINE LOOP. The sequential scan
// already resolved this in Open; the index scan did not, and called
// catalog.InMemory.LookupEnum once per column PER ROW inside Next() instead.
// On TPC-H SF=1 — a schema with zero enum types — that measured **21.09 s of
// 117.56 s, 17.94 % of all CPU**, of which 13.53 s was nothing but
// sync.RWMutex acquire/release (RLock+RUnlock are 11.68 % of total TPC-H CPU
// and essentially all of that traffic originated here). The remainder was a
// strings.ToLower of the type name and a map miss.
//
// The two scan paths are twins and one of them had the fix. Both now call
// this, so the next change cannot repair one and miss the other — that
// divergence IS the defect this function exists to close.
//
// STALENESS: this memoises a catalog fact, so it must be resolved wherever the
// column list itself is resolved — an operator's Open, i.e. per execution —
// and never cached against a table across DDL. Same rule colTypeInfo states.
//
// The returned `hasEnum` is the load-bearing half for performance: on every
// table in TPC-H and TPC-DS it is false, and callers skip the per-row loop
// entirely rather than walking columns to find all-nils.
func resolveEnumColumns(cat catalog.Catalog, cols []catalog.Column) (types []*catalog.EnumType, hasEnum bool) {
	im, ok := cat.(*catalog.InMemory)
	if !ok || len(cols) == 0 {
		return nil, false
	}
	types = make([]*catalog.EnumType, len(cols))
	for i, col := range cols {
		if et, isEnum := im.LookupEnum(col.Type.Name); isEnum {
			types[i] = et
			hasEnum = true
		}
	}
	if !hasEnum {
		// Nothing to inject. Returning nil lets callers disarm on a single
		// nil check and lets the slice be collected immediately.
		return nil, false
	}
	return types, true
}

// applyEnumColumns rewrites KindString values into KindEnum for the columns
// resolved by resolveEnumColumns. It is the per-row half, and is a no-op
// unless the scan actually has an enum column.
//
// The label -> SortOrder search is linear over the enum's values, matching the
// behaviour it replaces exactly; enum types are small and this preserves the
// existing answer rather than introducing a map whose iteration order or
// construction cost would be a separate change.
//
// FOUND DIVERGENCE, DELIBERATELY PRESERVED, NOT FIXED HERE: this handles
// KindString only, because that is exactly what the index scan did before.
// The sequential scan's own inline conversion (operators_storage.go, the
// `len(o.enumTypes) > 0` arm) additionally accepts KindBytes. The two paths
// have therefore always disagreed about a KindBytes enum value, and unifying
// them would CHANGE RESULTS on one of them — a values question that needs its
// own reproducer and its own gate, not a silent ride-along on a performance
// change. Recorded so the next reader does not "tidy" it by accident.
// The resolution half above is shared, which is the divergence that mattered.
func applyEnumColumns(row Row, types []*catalog.EnumType) {
	if types == nil {
		return
	}
	for i, et := range types {
		if et == nil || i >= len(row) || row[i].Kind != KindString {
			continue
		}
		label := row[i].StringValue()
		for _, ev := range et.Values {
			if ev.Label == label {
				row[i] = NewEnumDatum(ev.SortOrder, label)
				break
			}
		}
	}
}
