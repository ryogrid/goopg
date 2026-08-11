package executor

import (
	"fmt"
	"math/bits"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/parser"
)

// The `interval` B-tree key (M0119-0006). Before this slice `CREATE INDEX ON
// t(interval_col)` raised `0A000 btree v0 only supports int4 / numeric keys`,
// so an interval column could not be indexed at all — not even as a PRIMARY
// KEY.
//
// interval is the one type in this cluster whose key is deliberately LOSSY, and
// that is a faithfulness property rather than a shortcut. Upstream's
// `interval_cmp_value` (postgres/src/backend/utils/adt/timestamp.c) collapses
// the three fields of an Interval into a single signed 128-bit span
//
//	span = (month * 30 + day) * USECS_PER_DAY + time
//
// and `interval_cmp` compares nothing else. Two intervals with the same span
// are EQUAL to PostgreSQL even when their fields differ — `'1 mon' = '30 days'`
// is true, captured from the PG 18.3 reference cluster — so a key that
// preserved the fields (e.g. span followed by month/day/time tiebreak bytes)
// would be the WRONG key: it would order equal values, let a UNIQUE index
// accept a duplicate PG rejects, and make `WHERE i = '30 days'` miss a stored
// `'1 mon'`. The key is therefore the span alone, and reconstructing an
// interval from it is impossible by construction — see intervalKeyNotDecodable.
//
// The span needs 128 bits for the same reason upstream uses INT128: the day
// total reaches 2^31*30 + 2^31 ≈ 6.4e10 and scaling it by 86_400_000_000
// microseconds overflows int64. btree.EncodeInt128 gives it an order-preserving
// 16-byte form, fixed width, so an interval is safe in any position of a
// composite key.
//
// The infinity sentinels need no special case, unlike the timestamp ones: PG
// spells them as the field extremes (INTERVAL_NOEND = all three fields at their
// signed maximum, postgres/src/include/datatype/timestamp.h), so their spans
// are already the largest and smallest any interval can produce and the plain
// ordering puts them at the ends — which is where the reference cluster orders
// them.

// usecsPerDayForKey mirrors upstream's USECS_PER_DAY
// (postgres/src/include/datatype/timestamp.h).
const usecsPerDayForKey = 86_400_000_000

// intervalKeyParts extracts the three Interval fields from either runtime shape
// an interval value arrives in.
//
// KindString is the shape that actually reaches both stored-key writers: goopg
// holds an interval COLUMN as text (the codec has no interval branch, so the
// value round-trips through its text form and a stored row decodes to
// KindString), and an unknown-literal probe (`WHERE i = '30 days'`) is a string
// too. KindInterval arrives from expression evaluation — `interval '1 day'`,
// timestamp subtraction. Both must produce identical bytes or a probe would not
// find the row it stored.
func intervalKeyParts(v Datum) (months, days int32, micros int64, ok bool) {
	switch v.Kind {
	case KindInterval:
		return v.IntervalMonthsValue(), v.IntervalDaysValue(), v.IntervalMicrosValue(), true
	case KindString:
		// parser.ParseIntervalBody is the same entry point `'…'::interval`
		// uses (evalCast → parseIntervalCastString), including the
		// 'infinity'/'-infinity' sentinels — so a value goopg can store as an
		// interval is a value this key can encode. Hard-won Rule #2: sharing
		// the parser is what keeps the key's notion of a valid interval from
		// drifting from the cast's.
		return parser.ParseIntervalBody(v.StringValue())
	default:
		return 0, 0, 0, false
	}
}

// intervalSpan128 reproduces interval_cmp_value: the span in microseconds as a
// signed 128-bit value, returned as its two's-complement (high, low) halves.
//
// The month/day combination is done in int64 exactly as upstream does ("Because
// the inputs are int32, int64 arithmetic suffices here"); only the scaling to
// microseconds needs the wider type.
func intervalSpan128(months, days int32, micros int64) (hi int64, lo uint64) {
	total := int64(months)*30 + int64(days)

	// 128-bit product |total| * USECS_PER_DAY, negated afterwards when the day
	// total is negative (bits.Mul64 is unsigned). total == math.MinInt64 cannot
	// occur — |total| <= 2^31*30 + 2^31 — so the negation is safe.
	neg := total < 0
	mag := total
	if neg {
		mag = -mag
	}
	uhi, ulo := bits.Mul64(uint64(mag), usecsPerDayForKey)
	if neg {
		// Two's-complement negation of the 128-bit magnitude.
		ulo, uhi = ^ulo+1, ^uhi
		if ulo == 0 {
			uhi++
		}
	}

	// Add the sign-extended time field.
	sumLo, carry := bits.Add64(ulo, uint64(micros), 0)
	sumHi, _ := bits.Add64(uhi, uint64(micros>>63), carry)
	return int64(sumHi), sumLo
}

// encodeIntervalBTreeKey builds the 16-byte interval key. A value the interval
// parser rejects raises PG's 22007 rather than silently indexing something
// else — the same error `'…'::interval` gives it.
func encodeIntervalBTreeKey(v Datum, colName string, pos int) ([]byte, *ExecError) {
	months, days, micros, ok := intervalKeyParts(v)
	if !ok {
		if v.Kind == KindString {
			return nil, &ExecError{Code: "22007", Pos: pos,
				Message: fmt.Sprintf("invalid input syntax for type interval: %q", v.StringValue())}
		}
		return nil, &ExecError{Code: "42804", Pos: pos,
			Message: fmt.Sprintf("column %q is not interval at runtime", colName)}
	}
	hi, lo := intervalSpan128(months, days, micros)
	return btree.EncodeInt128(hi, lo), nil
}

// intervalKeyNotDecodable is the refusal both key-decode siblings return for an
// interval key. It is not a gap to be filled later: the stored key is the
// comparison span, which is a many-to-one image of the three interval fields
// (`'1 mon'` and `'30 days'` produce the same 16 bytes), so no decoder can
// exist. Returning an error rather than falling through matters for the reason
// the array-decode slice found at HEAD — the two siblings' shared `default:`
// arm reads any 8 leading bytes as an enum float8 and never errors, so an
// unrouted interval key would decode as a bogus enum AND desynchronize the
// composite walk by consuming 8 of its 16 bytes.
//
// Consumers must therefore not depend on decoding an interval key:
//   - the amcheck operator-class comparator falls back to byte order for the
//     column, which IS the interval ordering (missed detection only, never a
//     false positive);
//   - the index-only scan declines its decode-from-key fast path and reads the
//     heap instead (indexOnlyKeyIsDecodable).
func intervalKeyNotDecodable() error {
	return fmt.Errorf("btree: interval key is the comparison span (interval_cmp_value) and cannot be decoded back to month/day/time")
}
