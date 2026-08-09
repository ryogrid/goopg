package mb

import "fmt"

// ConvProc is a character-set conversion function with the PostgreSQL
// conversion-proc signature:
//
//	conv_proc(
//	    INTEGER,   -- source encoding id (ignored; the proc is pair-specific)
//	    INTEGER,   -- destination encoding id (ignored)
//	    CSTRING,   -- source bytes
//	    CSTRING,   -- destination buffer (PG model)
//	    INTEGER,   -- source length
//	    BOOL       -- if true, don't error on conversion failure
//	) returns INTEGER;  -- bytes consumed
//
// Go translation: ConvProc receives the source bytes and noError flag;
// it returns bytes consumed, the converted output, and any error.
// The encoding IDs are omitted because each proc is registered for a
// specific pair and the dispatch already resolved them.
type ConvProc func(src []byte, noError bool) (consumed int, dest []byte, err error)

// BuiltinConversions maps proc OID → conversion function for the
// built-in pg_conversion entries (the 128 bootstrap rows).
// Populated by init(); each conversion proc registers itself here.
var BuiltinConversions = map[uint32]ConvProc{
	4374: iso8859_1_to_utf8,
	4375: utf8_to_iso8859_1,
}

// DoEncodingConversion converts src from srcEnc to destEnc.
// Mirrors pg_do_encoding_conversion (postgres/src/backend/utils/mb/mbutils.c:365).
//
// Fast paths:
//  1. len(src) == 0 → return src unchanged (empty string always valid).
//  2. srcEnc == destEnc → return src unchanged (no conversion needed).
//  3. destEnc == PG_SQL_ASCII → return src unchanged (any string valid in SQL_ASCII).
//  4. srcEnc == PG_SQL_ASCII → verify src is valid destEnc, return src unchanged.
//
// Otherwise looks up the conversion proc from the catalog (or builtins)
// and dispatches.
func DoEncodingConversion(src []byte, srcEnc, destEnc int32, lookup ConvProcLookup) ([]byte, error) {
	// Fast path 1: empty string.
	if len(src) == 0 {
		return src, nil
	}

	// Fast path 2: same encoding.
	if srcEnc == destEnc {
		return src, nil
	}

	// Fast path 3: any string is valid in SQL_ASCII.
	if destEnc == PG_SQL_ASCII {
		return src, nil
	}

	// Fast path 4: SQL_ASCII source — verify and pass through.
	if srcEnc == PG_SQL_ASCII {
		// TODO: pg_verify_mbstr(destEnc, src, false) for real validation.
		return src, nil
	}

	// Look up the conversion proc.
	proc, err := lookup(srcEnc, destEnc)
	if err != nil {
		return nil, err
	}

	// Allocate destination buffer with worst-case expansion.
	destBuf := make([]byte, len(src)*MAX_CONVERSION_GROWTH+1)
	// We pass a sub-slice that the proc can write into.
	// The proc is responsible for returning how many source bytes were consumed
	// and the dest bytes produced.
	consumed, converted, procErr := proc(src, false)
	if procErr != nil {
		return nil, procErr
	}

	// Use the proc's output directly (it allocated its own dest).
	_ = consumed
	_ = destBuf
	return converted, nil
}

// ConvProcLookup resolves (srcEnc, destEnc) to a ConvProc.
// Implementations: catalog.FindDefaultConversionProc (server),
// or a test-only builtin lookup.
type ConvProcLookup func(srcEnc, destEnc int32) (ConvProc, error)

// ErrConversionNotFound is returned when no conversion proc is registered
// for the given encoding pair.
type ErrConversionNotFound struct {
	SrcEnc  int32
	DestEnc int32
}

func (e *ErrConversionNotFound) Error() string {
	return fmt.Sprintf("default conversion function for encoding %d to %d does not exist", e.SrcEnc, e.DestEnc)
}

// BuiltinLookup is a ConvProcLookup that only checks BuiltinConversions.
// Useful for testing and as a fallback.
func BuiltinLookup(srcEnc, destEnc int32) (ConvProc, error) {
	// The built-in pg_conversion bootstrap rows map specific proc OIDs
	// to encoding pairs. For the first slice, we only have LATIN1↔UTF8.
	// The full catalog lookup (FindDefaultConversionProc) will be added
	// to internal/catalog/.
	if (srcEnc == PG_LATIN1 && destEnc == PG_UTF8) || (srcEnc == PG_UTF8 && destEnc == PG_LATIN1) {
		// LATIN1→UTF8 = proc OID 4374, UTF8→LATIN1 = proc OID 4375
		// Both are registered in BuiltinConversions above.
		if srcEnc == PG_LATIN1 {
			return BuiltinConversions[4374], nil
		}
		return BuiltinConversions[4375], nil
	}
	return nil, &ErrConversionNotFound{SrcEnc: srcEnc, DestEnc: destEnc}
}
