package executor

import (
	"encoding/binary"
	"slices"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/adt/datetime"
)

// typedStatValueTypes are the column types whose pg_statistic stavalues goopg
// writes as an anyarray of the column's own type (M0149-0001j), as PG's
// update_attstats (analyze.c) does. Every other type keeps the legacy
// text-element blob.
//
// ANALYZE keeps its values as text, and for the date-time types that text
// follows the analysing session's DateStyle / TimeZone / IntervalStyle. The
// array encoder parses elements under the boot defaults (ISO, MDY, UTC), so
// a non-default rendering could parse to a different value (a DMY
// "05/11/2026" read as May 11). pgStatValuesBytes therefore keeps a typed
// blob only when it decodes back to exactly the strings it was built from,
// which is the case precisely when the session rendered in the canonical
// style. Any other session falls back to the legacy blob: a PG node reads
// that as no statistics, never as wrong ones.
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
	"time": "time", "time without time zone": "time",
	"timetz": "timetz", "time with time zone": "timetz",
	"timestamp": "timestamp", "timestamp without time zone": "timestamp",
	"timestamptz": "timestamptz", "timestamp with time zone": "timestamptz",
	"interval": "interval",
	"bool":     "bool", "boolean": "bool", // ANALYZE renders t / f, as boolout does
	"name":  "name",  // NameData element (64 bytes, typalign 'c')
	"money": "money", // cash_out text; the 8-byte Cash element under fixed_length_money
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
				if back, ok := decodeTypedStatValues(blob); ok && slices.Equal(back, vals) {
					return blob
				}
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

// statValueText renders one ANALYZE sample value (an MCV, a histogram bound,
// an imported statistic or an index endpoint) as the column type's output
// function does. A time / timetz value is a KindTime Datum like a timestamp,
// so formatDatumDateStyle alone rendered `23:00` as "1970-01-01 23:00:00" and
// dropped timetz's zone. Those two go through time_out / timetz_out, exactly
// as COPY TO's arms do (copy_text.go); every other type keeps
// formatDatumDateStyle. M0149-0001j.
func statValueText(d Datum, colType catalog.Type, ctx *Context) string {
	if isMoneyTypeName(colType.Name) && !colType.IsArray && !d.IsNull() {
		// cash_out: money statistics read like PG's pg_stats ("$12.34"),
		// and the optimizer compares them through cash_in. M0149-0001k5.
		return MoneyText(d)
	}
	if d.Kind == KindTime && !colType.IsArray {
		switch strings.ToLower(colType.Name) {
		case "time", "time without time zone":
			return datetime.FormatTime(pgTimeMicros(d.TimeValue()))
		case "timetz", "time with time zone":
			return datetime.FormatTimeTZ(pgTimeMicros(d.TimeValue()), int32(-d.TimeTZOffsetSecs()))
		}
	}
	return formatDatumDateStyle(d, ctx)
}
