package analyzer

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// NumericCoercePrecedence returns the position of typeName on goopg's numeric
// coercion lattice:
//
//	int2 (0) → int4 (1) → int8 (2) → numeric (3) → float4 (4) → float8 (5)
//
// Returns -1 when typeName is not a numeric type.
// Upstream reference: postgres/src/include/catalog/pg_cast.h implicit casts.
func NumericCoercePrecedence(typeName string) int {
	switch strings.ToLower(typeName) {
	case "int2", "smallint":
		return 0
	case "int4", "int", "integer":
		return 1
	case "int8", "bigint":
		return 2
	case "numeric", "decimal":
		return 3
	case "float4", "real":
		return 4
	case "float8", "double precision", "double":
		return 5
	}
	return -1
}

// PromoteNumericType returns the wider of two numeric catalog types.
// "Wider" means the type higher on the coercion lattice. When both types are
// the same, l is returned unchanged. When one side is unknown (literal not yet
// typed) the other side wins. Returns an empty Type when neither side is
// numeric.
//
// Examples:
//
//	int4 + int8  → int8
//	int8 + numeric → numeric
//	numeric * float4 → float4
func PromoteNumericType(l, r catalog.Type) catalog.Type {
	// unknown/untyped literals yield to whatever the other side says.
	if isUnknownType(l) {
		return r
	}
	if isUnknownType(r) {
		return l
	}
	lp := NumericCoercePrecedence(l.Name)
	rp := NumericCoercePrecedence(r.Name)
	if lp < 0 || rp < 0 {
		// At least one side is not a numeric type; caller handles the error.
		return catalog.Type{}
	}
	if rp > lp {
		return r
	}
	return l
}

// PromoteStringType returns the canonical string type for mixed string/char
// operations. All string-family types (`text`, `varchar`, `character varying`,
// `char`, `bpchar`) coerce to `text` under goopg's lattice, matching
// PostgreSQL's implicit-cast rules for text category operators.
func PromoteStringType(l, r catalog.Type) catalog.Type {
	if isUnknownType(l) {
		return r
	}
	if isUnknownType(r) {
		return l
	}
	// Both are string-like: return text as the common type.
	return catalog.Type{Name: "text"}
}

// PromoteTimestampType returns the wider of date and timestamp. A `date`
// coerces implicitly to `timestamp` (they share the same KindTime runtime
// representation). Returns l unchanged for same-family pairs.
func PromoteTimestampType(l, r catalog.Type) catalog.Type {
	if isUnknownType(l) {
		return r
	}
	if isUnknownType(r) {
		return l
	}
	// date < timestamp < timestamptz in the coercion order.
	lp := timestampPrecedence(l.Name)
	rp := timestampPrecedence(r.Name)
	if rp > lp {
		return r
	}
	return l
}

func timestampPrecedence(name string) int {
	switch strings.ToLower(name) {
	case "date":
		return 0
	case "timestamp", "timestamp without time zone":
		return 1
	case "timestamptz", "timestamp with time zone":
		return 2
	}
	return -1
}
