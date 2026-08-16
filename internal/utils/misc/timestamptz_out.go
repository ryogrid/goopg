package misc

import (
	"strconv"
	"sync"
	"time"
)

// timestamptz_out.go — the `timestamp with time zone` half of goopg's
// date/time output, i.e. upstream's EncodeDateTime with print_tz=true
// (postgres/src/backend/utils/adt/datetime.c, reached from timestamptz_out in
// timestamp.c).
//
// Until this file existed, goopg rendered a TIMESTAMPTZ exactly like a plain
// TIMESTAMP: it dropped the zone entirely and never converted out of UTC, so
// `'2020-01-01 10:00:00+05:30'::timestamptz` printed "2020-01-01 04:30:00"
// where PG 18.3 prints "2020-01-01 04:30:00+00". That is not a cosmetic
// difference: with `SET TimeZone='Asia/Kolkata'` the unlabelled text READS as a
// different instant than the one stored, so a client round-tripping the value
// through text got the wrong time. (Deferral-ledger row 2026-08-11.)
//
// Two behaviours land together here because upstream has them in one function
// and neither is correct alone: converting into the session zone without
// printing the offset would relabel the instant silently, and printing "+00"
// without converting would contradict the session's TimeZone GUC.

// zoneCache memoises time.LoadLocation, which reads and parses a tzdata file.
// Output formatting runs once per timestamptz cell per row, so the uncached
// cost would land in the middle of a result-set scan.
var zoneCache sync.Map // zone name (string) -> *time.Location

// sessionLocation resolves the session's TimeZone GUC to a *time.Location,
// falling back to UTC for the empty string and for any spelling Go's tzdata
// does not know. Upstream accepts POSIX-style zone specifications there too
// (`SET TimeZone='+05:30'`, note the inverted POSIX sign); Go's LoadLocation
// does not, and those fall back to UTC — see the deferral ledger.
func sessionLocation(zone string) *time.Location {
	if zone == "" {
		return time.UTC
	}
	if v, ok := zoneCache.Load(zone); ok {
		return v.(*time.Location)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	zoneCache.Store(zone, loc)
	return loc
}

// ZoneOffsetForLocalTime ports DetermineTimeZoneOffset (backend/utils/adt/
// datetime.c): given a LOCAL wall clock and a zone, it answers the zone's UTC
// offset at that local moment — the quantity upstream stores in a timetz's
// `zone` field (there as seconds WEST; the sign is flipped at the call site).
// `wall` is read for its calendar fields only, so callers may pass a UTC-
// anchored Time carrying local field values (goopg's zone-less convention).
//
// Go's time.Date performs the same local→absolute resolution upstream's
// DetermineTimeZoneOffset does, including the DST-ambiguity preference for the
// earlier of two candidate offsets; the spring-forward "nonexistent local time"
// case is not asserted to match, exactly as for TimestampToTimestampTZ above.
// M0119-0006.
func ZoneOffsetForLocalTime(zone string, wall time.Time) int {
	l := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(),
		wall.Second(), 0, sessionLocation(zone))
	_, off := l.Zone()
	return off
}

// TimeTZSessionOffset ports the "timezone not specified? then use session
// timezone" arm of DecodeTimeOnly (datetime.c, the `!(fmask & DTK_M(TZ))`
// block): a TIMETZ literal that carries no zone field does NOT take +00 — it
// takes the session zone's offset for TODAY's date at the literal's own time of
// day. Upstream builds that struct pg_tm from GetCurrentDateTime() plus the
// parsed tm_hour/tm_min/tm_sec and hands it to DetermineTimeZoneOffset, which
// is why the answer is DST-sensitive: on America/New_York `'10:00'::timetz` is
// 10:00:00-05 in January and 10:00:00-04 in July.
//
// The returned offset is seconds EAST of UTC (goopg's Datum convention).
// An empty zone — the boot default — yields 0, i.e. the previous behaviour.
func TimeTZSessionOffset(zone string, hour, min, sec int) int {
	if zone == "" {
		return 0
	}
	now := time.Now().In(sessionLocation(zone))
	return ZoneOffsetForLocalTime(zone,
		time.Date(now.Year(), now.Month(), now.Day(), hour, min, sec, 0, time.UTC))
}

// TimestampToTimestampTZ ports timestamp2timestamptz (backend/utils/adt/
// timestamp.c): the plain-`timestamp` wall clock is read AS A LOCAL TIME in the
// session zone, and the instant it denotes is what a timestamptz stores. `t`
// carries the wall-clock fields in UTC (goopg's KindTime convention for a
// zone-less timestamp); the result is the corresponding absolute instant, again
// expressed in UTC.
//
// Upstream calls DetermineTimeZoneOffset(tm, session_timezone) for the offset;
// Go's time.Date(..., loc) performs the same local→absolute resolution. The two
// agree on the ordinary case and on the DST-ambiguity preference (both take the
// earlier of two candidate offsets); the spring-forward "nonexistent local time"
// case is NOT asserted to match — see the deferral ledger. M0119-0006.
func TimestampToTimestampTZ(t time.Time, zone string) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), u.Minute(), u.Second(),
		u.Nanosecond(), sessionLocation(zone)).UTC()
}

// TimestampTZToTimestamp ports timestamptz2timestamp (same file): the stored
// instant is rendered into the session zone and the resulting wall clock — with
// the zone thrown away — is what a plain `timestamp` holds. Inverse of
// TimestampToTimestampTZ; returns the wall-clock fields in UTC, matching the
// KindTime convention for a zone-less timestamp. M0119-0006.
func TimestampTZToTimestamp(t time.Time, zone string) time.Time {
	l := t.In(sessionLocation(zone))
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(),
		l.Nanosecond(), time.UTC)
}

// encodeTimezone ports EncodeTimezone (datetime.c). offsetEast is the zone's
// offset in seconds EAST of UTC — the sign convention Go's Time.Zone uses.
// Upstream's `tz` is the negation of that (seconds west), which is why its
// sign test reads `tz <= 0 ? '+' : '-'`.
//
// The three widths are upstream's: HH when the offset is a whole number of
// hours ("+00", "-08"), HH:MM when it has a minute part ("+05:30"), and
// HH:MM:SS when it has a second part — which real zones do have before their
// first standardisation, e.g. Asia/Kolkata's LMT is "+05:53:28".
func encodeTimezone(offsetEast int) string {
	sign := byte('+')
	if offsetEast < 0 {
		sign = '-'
	}
	sec := offsetEast
	if sec < 0 {
		sec = -sec
	}
	min := sec / 60
	sec -= min * 60
	hour := min / 60
	min -= hour * 60

	out := make([]byte, 0, 9)
	out = append(out, sign)
	out = appendZeroPad2(out, hour)
	if sec != 0 {
		out = append(out, ':')
		out = appendZeroPad2(out, min)
		out = append(out, ':')
		out = appendZeroPad2(out, sec)
	} else if min != 0 {
		out = append(out, ':')
		out = appendZeroPad2(out, min)
	}
	return string(out)
}

// appendZeroPad2 is pg_ultostr_zeropad(str, v, 2) for the non-negative values
// encodeTimezone produces. An hour field wider than two digits (impossible for
// a real zone) is emitted unpadded rather than truncated.
func appendZeroPad2(dst []byte, v int) []byte {
	if v < 10 {
		return append(dst, '0', byte('0'+v))
	}
	if v < 100 {
		return append(dst, byte('0'+v/10), byte('0'+v%10))
	}
	return strconv.AppendInt(dst, int64(v), 10)
}

// FormatTimestampTZ renders a TIMESTAMPTZ value as PostgreSQL 18.3's
// timestamptz_out does: convert the stored instant into the session's TimeZone,
// format the local wall clock per the DateStyle (style, order) pair, then
// append the zone — and only then the era marker.
//
// The zone spelling follows EncodeDateTime's per-style split, which is NOT
// uniform:
//
//	ISO      — always the numeric offset, jammed onto the seconds field:
//	           "2020-06-15 03:00:00-07", "2020-06-15 15:45:00+05:45".
//	SQL,
//	Postgres,
//	German   — the zone ABBREVIATION after a space: "06/15/2020 03:00:00 PDT",
//	           "Mon Jun 15 15:30:00 2020 IST". Zones with no alphabetic
//	           abbreviation report a numeric one and it is printed verbatim
//	           ("... 2020 +0545"), matching upstream, which takes the string
//	           straight from the tzdata entry.
//
// t may be in any location; it is converted, so callers need not pre-normalise
// to UTC.
func FormatTimestampTZ(t time.Time, style, order, zone string) string {
	local := t.In(sessionLocation(zone))
	body, era := formatTimestampBody(local, style, order)
	tzn, offsetEast := local.Zone()

	switch style {
	case "SQL", "German":
		// datetime.c: `if (tzn) sprintf(" %s", tzn); else EncodeTimezone(...)`
		// — the numeric fallback carries no leading space in these two arms.
		if tzn != "" {
			return body + " " + tzn + era
		}
		return body + encodeTimezone(offsetEast) + era
	case "Postgres":
		// The Postgres arm is the same except that its numeric fallback DOES
		// prepend a space, "to avoid formatting something which would be
		// rejected by the date/time parser later" (datetime.c, 2001 comment).
		if tzn != "" {
			return body + " " + tzn + era
		}
		return body + " " + encodeTimezone(offsetEast) + era
	default:
		return body + encodeTimezone(offsetEast) + era
	}
}
