package datetime

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// FormatInterval renders (months, days, micros) the way upstream PG's
// EncodeInterval does under the default 'postgres' IntervalStyle
// (INTSTYLE_POSTGRES, postgres/src/backend/utils/adt/datetime.c): months
// split into years + remainder months, each nonzero year/mon/day
// component printed as "<n> <unit>" (plural unless n == 1 exactly),
// space-separated, with per-field sign carrying (a nonzero field forces a
// leading "+" on the *next* positive field — PG's `is_before` quirk). The
// sub-day component is rendered as "[-|+]HH:MM:SS[.ffffff]" (hours may
// exceed 24; fractional seconds trimmed of trailing zeros) and is emitted
// whenever any time field is nonzero OR the whole interval is zero (so the
// zero interval prints "00:00:00"). Verified byte-for-byte against real
// PostgreSQL 18.3 (see docs/design/0003-0006-date-interval-arithmetic.md).
//
// This lives in the leaf pgdatetime package — not in the executor, where it
// was born — because an interval COLUMN is now stored in PG's native 16-byte
// binary layout (M0119-0006 interval-column-storage slice). The two decoders
// of that layout live in different packages: the executor's
// decodePhysicalPGValueMctx and the logical-replication row decoder in
// internal/wal. Both must render the same text for the same bytes, and
// internal/wal cannot import the executor, so the renderer has to sit below
// both. Sibling-agreement rule, .ralph/PROMPT.md hard-won rule #2.
func FormatInterval(months, days int32, micros int64) string {
	// interval ±infinity: PG's INTERVAL_NOEND / INTERVAL_NOBEGIN sentinel (all
	// fields at their signed extreme) prints as the bare word, no field
	// decomposition. unimplemented_feat #5(d-iv).
	if months == math.MaxInt32 && days == math.MaxInt32 && micros == math.MaxInt64 {
		return "infinity"
	}
	if months == math.MinInt32 && days == math.MinInt32 && micros == math.MinInt64 {
		return "-infinity"
	}
	const usecsPerHour = 3600 * 1_000_000
	const usecsPerMin = 60 * 1_000_000
	const usecsPerSec = 1_000_000

	year := months / 12
	remMonths := months % 12

	// Decompose the signed microsecond total; Go's truncated integer
	// division gives every field the same sign as micros, matching
	// PG's struct pg_itm layout.
	hour := micros / usecsPerHour
	rem := micros % usecsPerHour
	minute := rem / usecsPerMin
	rem = rem % usecsPerMin
	sec := rem / usecsPerSec
	fsec := rem % usecsPerSec

	var sb strings.Builder
	isZero := true
	isBefore := false
	addPart := func(val int64, unit string) {
		if val == 0 {
			return
		}
		if !isZero {
			sb.WriteByte(' ')
		}
		if isBefore && val > 0 {
			sb.WriteByte('+')
		}
		sb.WriteString(strconv.FormatInt(val, 10))
		sb.WriteByte(' ')
		sb.WriteString(unit)
		if val != 1 {
			sb.WriteByte('s')
		}
		isBefore = val < 0
		isZero = false
	}
	addPart(int64(year), "year")
	// PG spells the month unit "mon" (not "month") for backward compat.
	addPart(int64(remMonths), "mon")
	addPart(int64(days), "day")

	if isZero || hour != 0 || minute != 0 || sec != 0 || fsec != 0 {
		minus := hour < 0 || minute < 0 || sec < 0 || fsec < 0
		if !isZero {
			sb.WriteByte(' ')
		}
		if minus {
			sb.WriteByte('-')
		} else if isBefore {
			sb.WriteByte('+')
		}
		fmt.Fprintf(&sb, "%02d:%02d:%02d", absInt64(hour), absInt64(minute), absInt64(sec))
		if fsec != 0 {
			frac := strings.TrimRight(fmt.Sprintf("%06d", absInt64(fsec)), "0")
			sb.WriteByte('.')
			sb.WriteString(frac)
		}
	}
	return sb.String()
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
