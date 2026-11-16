package executor

import (
	"encoding/binary"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// typedStatValueTypes are the column types whose pg_statistic stavalues goopg
// writes as an anyarray of the column's own type (M0149-0001j), as PG's
// update_attstats (analyze.c) does. Each is a type whose element text form is
// independent of session settings, so the values ANALYZE rendered round-trip
// exactly through the typed blob. Every other type keeps the legacy
// text-element blob.
var typedStatValueTypes = map[string]string{
	"int2": "int2", "smallint": "int2",
	"int4": "int4", "int": "int4", "integer": "int4",
	"int8": "int8", "bigint": "int8",
	"oid":     "oid",
	"numeric": "numeric", "decimal": "numeric",
	"float8": "float8", "double precision": "float8",
	"text":    "text",
	"varchar": "varchar", "character varying": "varchar",
	"bpchar": "bpchar", "character": "bpchar", "char": "bpchar",
	"date": "date",
}

// pgStatValuesBytes renders a pg_statistic stavalues slot for a column of type
// colType: a 1-D anyarray of the column's own element type for the
// typedStatValueTypes, built by goopg's array codec from the values' text
// form; otherwise (or if any value fails the type's input) the legacy
// text-element blob. The blob always carries a 4-byte varlena header, the
// form the pg_statistic row writer and catalog.DecodePGStatisticRow expect.
func pgStatValuesBytes(colType catalog.Type, vals []string) []byte {
	if !colType.IsArray {
		if name, ok := typedStatValueTypes[strings.ToLower(colType.Name)]; ok {
			if blob, ok := pgTypedArrayBytes(catalog.Type{Name: name, IsArray: true}, vals); ok {
				return blob
			}
		}
	}
	return pgTextArrayBytes(vals)
}

// pgTypedArrayBytes encodes vals as a PG ArrayType of arrType's element type
// with a 4-byte varlena header, or ok=false if the codec rejects a value.
func pgTypedArrayBytes(arrType catalog.Type, vals []string) ([]byte, bool) {
	enc, err := encodeValuePG(arrType, NewStringDatum(formatTextArray(vals)))
	if err != nil {
		return nil, false
	}
	payload, n, err := decodePhysicalPGVarlena(enc)
	if err != nil || n != len(enc) {
		return nil, false
	}
	full := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(full[0:4], uint32(len(full))<<2)
	copy(full[4:], payload)
	return full, true
}

// decodeTypedStatValues is catalog.StatValuesDecoder: it renders a typed
// stavalues blob (a 4-byte-header ArrayType) back to the per-element text
// ANALYZE produced. ok=false leaves the caller on its legacy text decoder.
func decodeTypedStatValues(blob []byte) ([]string, bool) {
	if len(blob) < 24 {
		return nil, false
	}
	elem := catalog.OIDToTypeName(binary.LittleEndian.Uint32(blob[12:16]))
	if _, ok := typedStatValueTypes[elem]; !ok {
		return nil, false
	}
	d, _, err := decodePhysicalPGValueMctx(catalog.Type{Name: elem, IsArray: true}, blob, nil)
	if err != nil || d.IsNull() {
		return nil, false
	}
	return parseTextArray(d.StringValue()), true
}

func init() {
	catalog.StatValuesDecoder = decodeTypedStatValues
}
