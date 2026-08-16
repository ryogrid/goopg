package datetime

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// PG-faithful renderers for the WIRE/on-disk integer forms of the date-time
// types, ported from postgres/src/backend/utils/adt/{datetime.c,date.c,
// timestamp.c}.
//
// They live in this leaf package for the same reason FormatInterval does: the
// on-disk image of a date/time/timestamp/timestamptz/timetz ARRAY ELEMENT is
// read by two decoders that cannot share executor code — internal/pgarray (used
// by the executor's heap codec) and internal/wal's pgoutput, which cannot import
// internal/executor. One table, two readers (Hard-won Rule #2). M0119-0006.
//
// Every input is the integer PG itself stores:
//
//	date        int32 days since 2000-01-01 (DATEVAL_NOEND/NOBEGIN = ±INT32)
//	time        int64 microseconds since midnight (0 .. 86400000000)
//	timestamp   int64 microseconds since 2000-01-01 00:00:00 (DT_NOEND/NOBEGIN = ±INT64)
//	timestamptz the same, always an absolute UTC instant
//	timetz      int64 local micros + int32 zone SECONDS WEST of UTC (PG's sign)

const (
	// postgresEpochJDate is POSTGRES_EPOCH_JDATE (2000-01-01) as a Julian day,
	// from postgres/src/include/datatype/timestamp.h.
	postgresEpochJDate = 2451545
	usecsPerDay        = int64(86400) * 1000000
	usecsPerHour       = int64(3600) * 1000000
	usecsPerMinute     = int64(60) * 1000000
	usecsPerSec        = int64(1000000)
)

// FormatDate renders a DATE the way date_out does under the default ISO
// DateStyle: "2020-01-01", "0001-01-01 BC", and the ±infinity sentinels.
func FormatDate(days int32) string {
	switch days {
	case math.MaxInt32:
		return "infinity"
	case math.MinInt32:
		return "-infinity"
	}
	y, m, d := j2date(int64(days) + postgresEpochJDate)
	return formatISODate(y, m, d)
}

// FormatTime renders a TIME the way time_out does: "01:02:03", with the
// fractional seconds trimmed of trailing zeros ("12:34:56.1") and omitted
// entirely when zero. 86400000000 renders as PG's "24:00:00".
func FormatTime(micros int64) string {
	return formatTimeOfDay(micros)
}

// FormatTimestamp renders a TIMESTAMP the way timestamp_out does under ISO
// DateStyle: "2020-01-01 10:00:00", "0001-01-01 00:00:00 BC", ±infinity.
func FormatTimestamp(micros int64) string {
	switch micros {
	case math.MaxInt64:
		return "infinity"
	case math.MinInt64:
		return "-infinity"
	}
	dateDays := micros / usecsPerDay
	timeOfDay := micros % usecsPerDay
	if timeOfDay < 0 {
		timeOfDay += usecsPerDay
		dateDays--
	}
	y, m, d := j2date(dateDays + postgresEpochJDate)
	// The BC marker trails the whole value ("0001-01-01 00:00:00 BC"), so the
	// date part is rendered without it and the suffix appended last.
	s := fmt.Sprintf("%s %s", formatISODateNoEra(y, m, d), formatTimeOfDay(timeOfDay))
	if y <= 0 {
		s += " BC"
	}
	return s
}

// FormatTimestampTZUTC renders a TIMESTAMPTZ as timestamptz_out does with the
// session TimeZone set to UTC — the zone goopg's own clusters run in (SHOW
// TimeZone = UTC), and the only zone this package can honour without a tzdata
// dependency in a leaf package. A stored timestamptz is an absolute instant, so
// the value is unambiguous; only the displayed zone is a session property.
func FormatTimestampTZUTC(micros int64) string {
	switch micros {
	case math.MaxInt64:
		return "infinity"
	case math.MinInt64:
		return "-infinity"
	}
	return FormatTimestamp(micros) + "+00"
}

// FormatTimeTZ renders a TIMETZ as timetz_out does: the LOCAL time of day
// followed by the UTC offset, e.g. "01:02:03+05", "12:00:00-03:30". zoneSecs is
// PG's own TimeTzADT.zone convention — seconds WEST of UTC — so the printed
// offset is its negation.
func FormatTimeTZ(micros int64, zoneSecs int32) string {
	var sb strings.Builder
	sb.WriteString(formatTimeOfDay(micros))
	east := -int64(zoneSecs)
	if east < 0 {
		sb.WriteByte('-')
		east = -east
	} else {
		sb.WriteByte('+')
	}
	hh := east / 3600
	mm := (east % 3600) / 60
	ss := east % 60
	fmt.Fprintf(&sb, "%02d", hh)
	if mm != 0 || ss != 0 {
		fmt.Fprintf(&sb, ":%02d", mm)
	}
	if ss != 0 {
		fmt.Fprintf(&sb, ":%02d", ss)
	}
	return sb.String()
}

func formatISODate(y, m, d int) string {
	s := formatISODateNoEra(y, m, d)
	if y <= 0 {
		s += " BC"
	}
	return s
}

// formatISODateNoEra prints the y-m-d fields with PG's era convention applied to
// the YEAR digits (year 0 is 1 BC) but WITHOUT the trailing " BC" marker, which
// upstream appends after the time part of a timestamp.
func formatISODateNoEra(y, m, d int) string {
	year := y
	if year <= 0 {
		year = -(year - 1)
	}
	return fmt.Sprintf("%04d-%02d-%02d", year, m, d)
}

// formatTimeOfDay renders microseconds-since-midnight as HH:MM:SS[.ffffff],
// which is what upstream EncodeTimeOnly emits for every ISO-style output: the
// fraction is printed only when non-zero and carries no trailing zeros
// (12:34:56.100000 → "12:34:56.1", 01:02:03.000001 → "01:02:03.000001").
func formatTimeOfDay(micros int64) string {
	neg := ""
	if micros < 0 {
		neg = "-"
		micros = -micros
	}
	h := micros / usecsPerHour
	rem := micros % usecsPerHour
	m := rem / usecsPerMinute
	rem %= usecsPerMinute
	s := rem / usecsPerSec
	frac := rem % usecsPerSec
	out := neg + fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	if frac != 0 {
		f := strings.TrimRight(fmt.Sprintf("%06d", frac), "0")
		out += "." + f
	}
	return out
}

// j2date is the port of upstream j2date (postgres/src/backend/utils/adt/
// datetime.c): Julian day → (year, month, day), where year <= 0 means BC. The
// unsigned arithmetic is upstream's and is what makes the pre-Gregorian range
// (down to 4713 BC) come out right.
func j2date(jd int64) (year, month, day int) {
	julian := uint64(jd) + 32044
	quad := julian / 146097
	extra := (julian-quad*146097)*4 + 3
	julian += 60 + quad*3 + extra/146097
	quad = julian / 1461
	julian -= quad * 1461
	y := julian * 4 / 1461
	if y != 0 {
		julian = (julian + 305) % 365
	} else {
		julian = (julian + 306) % 366
	}
	julian += 123
	y += quad * 4
	year = int(int64(y) - 4800)
	quad = julian * 2141 / 65536
	day = int(julian - 7834*quad/256)
	month = int((quad+10)%12 + 1)
	return year, month, day
}

// ParseZoneSecsWest is the inverse helper the tests use to build a timetz zone
// field from a "+05:30"-style offset; it is not used on any storage path.
func ParseZoneSecsWest(offset string) (int32, bool) {
	if offset == "" {
		return 0, false
	}
	sign := int32(1)
	switch offset[0] {
	case '-':
		sign = -1
	case '+':
	default:
		return 0, false
	}
	parts := strings.Split(offset[1:], ":")
	if len(parts) > 3 {
		return 0, false
	}
	var secs int32
	mult := []int32{3600, 60, 1}
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		secs += int32(v) * mult[i]
	}
	// East offsets are negative in PG's zone field.
	return -sign * secs, true
}
