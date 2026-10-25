package executor

import (
	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/catalog"
)

// indexProbeForm is what an index scan needs to form its equality probe key
// on every rescan, resolved once (M0122-0012 slice 80). PG builds a scan's
// ScanKey array once (ExecIndexBuildScanKeys at ExecInitIndexScan) and per
// rescan only stores the runtime datums into it (ExecIndexEvalRuntimeKeys).
// goopg's probe is a formed index tuple, and every rescan of a
// parameterised index scan re-resolved the key columns by name, re-derived
// the index's key columns and formed the tuple through the general builder:
// TPC-DS Q14's store/catalog/web_sales_pkey probes (16 173 rescans each)
// spent about a third of their rescan there.
//
// For a probe whose attributes are all fixed-width the tuple's layout does
// not depend on the values (no null bitmap — a NULL key ends the scan
// before a probe is formed — no varlena header, every attribute at its fixed
// offset), so the first probe formed by ctx.indexProbeKey is kept as a
// template and later ones copy it and write each value's image at its
// offset: the same bytes (pinned against indexProbeKey). Anything else takes
// indexProbeKey on every call.
type indexProbeForm struct {
	// cols[i] is the table column of key position i, resolved on first use.
	cols []*catalog.Column
	// tmpl is the template probe for tmplN attributes, offs/lens each
	// attribute's offset and width in it; nil when the probe is not
	// all-fixed-width (or no probe has been formed yet).
	tmpl  []byte
	tmplN int
	offs  []int
	lens  []int
	tried bool
}

// colAt is ctx.Catalog.LookupColumn(table, name) for key position i, cached.
func (f *indexProbeForm) colAt(ctx *Context, table *catalog.Table, name string, i int) (*catalog.Column, bool) {
	if i < len(f.cols) && f.cols[i] != nil && f.cols[i].Name == name {
		return f.cols[i], true
	}
	col, ok := ctx.Catalog.LookupColumn(table, name)
	if !ok {
		return nil, false
	}
	for len(f.cols) <= i {
		f.cols = append(f.cols, nil)
	}
	f.cols[i] = col
	return col, true
}

// form is ctx.indexProbeKey(idx, parts), through the template when it
// applies.
func (f *indexProbeForm) form(ctx *Context, idx *catalog.Index, parts []indexProbeKeyPart) ([]byte, *ExecError) {
	if f.tmpl != nil && len(parts) == f.tmplN {
		if key, ok := f.fill(parts); ok {
			return key, nil
		}
	}
	key, err := ctx.indexProbeKey(idx, parts)
	if err != nil {
		return nil, err
	}
	if !f.tried {
		f.tried = true
		f.keepTemplate(ctx, idx, len(parts), key)
	}
	return key, nil
}

// keepTemplate keeps key as the template when its n attributes are all
// fixed-width and it carries no null bitmap.
func (f *indexProbeForm) keepTemplate(ctx *Context, idx *catalog.Index, n int, key []byte) {
	desc := ctx.pgIndexKeyDesc(idx)
	if desc == nil || n == 0 || n > desc.NKeyAtts() || nbtree.PGIndexTupleHasNulls(key) {
		return
	}
	phys := desc.Physical()[:n]
	offs, fixed := nbtree.PGIndexFixedAttrOffsets(phys)
	if !fixed {
		return
	}
	size := nbtree.PGIndexTupleSize(key)
	lens := make([]int, n)
	for i := range phys {
		lens[i] = int(phys[i].Len)
	}
	if size > len(key) || offs[n-1]+lens[n-1] > size {
		return
	}
	f.tmpl, f.tmplN, f.offs, f.lens = append([]byte(nil), key...), n, offs, lens
}

// fill forms the probe from the template, or ok=false when a value's image
// is not the template's width (or cannot be encoded), leaving the caller to
// the general path — and its error.
func (f *indexProbeForm) fill(parts []indexProbeKeyPart) ([]byte, bool) {
	key := append([]byte(nil), f.tmpl...)
	for i, p := range parts {
		if p.col == nil || p.val.IsNull() || p.val.Kind == KindToastPointer {
			return nil, false
		}
		img, err := encodeValuePG(p.col.Type, p.val)
		if err != nil || len(img) != f.lens[i] {
			return nil, false
		}
		copy(key[f.offs[i]:], img)
	}
	return key, true
}
