package executor

// B-tree keys on composite (row) types — btree record_ops, whose comparison
// is record_cmp (postgres/src/backend/utils/adt/rowtypes.c): the records are
// compared field by field with each field type's default btree comparison,
// and a NULL field sorts after every non-NULL value ("nulls are considered
// larger than non-nulls"). Before this slice (M0149-0046b) a composite key
// column was refused with 0A000 "btree v0 only supports int4 / numeric keys",
// so subscription/t/002_types.pl's `a tst_comp_basic_t PRIMARY KEY` tables
// could not be created on a goopg node.
//
// The key mirrors the array_ops key (btree_array_key.go): one tag per field —
// recordKeyFieldPresent followed by the field's own key bytes, or
// recordKeyFieldNull — then recordKeyEnd. Each field key comes from
// encodeBTreeKeyForColumn with the field's type, so a field is keyed exactly
// as a column of that type would be (nested composites and composite arrays
// recurse through it), and every field key is self-delimiting, which is what
// makes the concatenation compare like record_cmp. The key is value-based:
// ROW(1.0,'a',1) and ROW(1,'a',1) of a (float8, text, int) type encode the
// same bytes, as record_cmp calls them equal.
//
// Decoding a record key needs the composite's field types, which the key
// decoders (no catalog at hand) cannot resolve, so an index-only scan never
// covers a composite key column (optimizer.indexCoversColumns) — the heap
// supplies the value instead. Deferral ledger 2026-11-26.

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

const (
	recordKeyEnd          byte = 0x00 // end of the record segment
	recordKeyFieldPresent byte = 0x01 // a non-NULL field's key bytes follow
	recordKeyFieldNull    byte = 0xFF // a NULL field: sorts after every value (record_cmp)
)

// compositeKeyFields resolves the field columns of a composite type for key
// encoding, or ok=false when typeName is not a composite type in ctx's
// catalog.
func compositeKeyFields(ctx *Context, typeName string) ([]catalog.Column, bool) {
	if ctx == nil || ctx.Catalog == nil {
		return nil, false
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil, false
	}
	fields := im.LookupCompositeTypeFields(typeName)
	if len(fields) == 0 {
		return nil, false
	}
	cols := make([]catalog.Column, len(fields))
	for i, f := range fields {
		_, _, base, isArray := parseCompositeFieldType(f.ColType)
		cols[i] = catalog.Column{Name: f.Name, Type: catalog.Type{Name: compositeFieldBaseTypeName(base), IsArray: isArray}}
	}
	return cols, true
}

// compositeFieldBaseTypeName is a field's base type name as the key
// encoders' type predicates read it: lower case, schema qualification
// ("public . tst_enum_t") dropped.
func compositeFieldBaseTypeName(base string) string {
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		base = base[i+1:]
	}
	return strings.ToLower(strings.TrimSpace(base))
}

// encodeRecordBTreeKey builds the stored/probe key of a composite key column
// whose fields are fields. v is the record's text form ("(1,a,1)").
func encodeRecordBTreeKey(ctx *Context, v Datum, col *catalog.Column, fields []catalog.Column, pos int) ([]byte, *ExecError) {
	if v.Kind != KindString {
		return nil, &ExecError{Code: "42804", Pos: pos,
			Message: fmt.Sprintf("column %q is not a record value at runtime (kind %d)", col.Name, v.Kind)}
	}
	vals, nulls, err := parseRecordText(v.StringValue())
	if err != nil {
		return nil, &ExecError{Code: "22P02", Pos: pos,
			Message: fmt.Sprintf("malformed record literal: %q", v.StringValue()), Detail: err.Error()}
	}
	if len(vals) != len(fields) {
		return nil, &ExecError{Code: "22P02", Pos: pos,
			Message: fmt.Sprintf("malformed record literal: %q", v.StringValue()),
			Detail:  fmt.Sprintf("record has %d fields, type %s has %d", len(vals), col.Type.Name, len(fields))}
	}
	out := make([]byte, 0, 16)
	for i := range fields {
		if nulls[i] {
			out = append(out, recordKeyFieldNull)
			continue
		}
		fk, encErr := encodeBTreeKeyForColumn(ctx, NewStringDatum(vals[i]), &fields[i], pos)
		if encErr != nil {
			return nil, encErr
		}
		out = append(out, recordKeyFieldPresent)
		out = append(out, fk...)
	}
	return append(out, recordKeyEnd), nil
}
