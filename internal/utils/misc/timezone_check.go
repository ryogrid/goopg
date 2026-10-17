package misc

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// IntervalParser is interval_in for check_timezone's INTERVAL 'x' form. The
// interval tokenizer lives in the parser package, which imports this one, so
// the parser registers it at init. It returns months, days and the sub-day
// microseconds; ok is false for input interval_in rejects.
var IntervalParser func(s string) (months, days int32, micros int64, ok bool)

// maxPOSIXOffsetSecs is the largest |offset| tzparse accepts: getsecs takes
// hours up to HOURSPERDAY * DAYSPERWEEK - 1 and minutes / seconds below 60.
const maxPOSIXOffsetSecs = 167*3600 + 59*60 + 59

// checkTimeZone is check_timezone (commands/variable.c) for the TimeZone
// GUC. It returns the value to store — the name the zone shows as, which is
// what PostgreSQL's SHOW TimeZone reports (pg_get_timezone_name):
//
//   - INTERVAL 'x': interval_in(x); months or days are rejected; the zone is
//     the fixed offset pg_tzset_offset builds, e.g. INTERVAL '+02:00' ->
//     <+02>-02 (the SQL sign flipped to POSIX's);
//   - a number: that many hours east of UTC, likewise -8 -> <-08>+08;
//   - otherwise a zone name: a tz database file, matched case-insensitively
//     and stored with the file's spelling, or a POSIX TZ specification
//     (upper-cased, as pg_tzset canonicalises it).
func checkTimeZone(value string) (string, error) {
	invalid := func(detail string) error {
		return &ValidationError{Msg: fmt.Sprintf("invalid value for parameter %q: %q", "TimeZone", value), Detail: detail}
	}
	if len(value) >= 8 && strings.EqualFold(value[:8], "interval") {
		rest := strings.TrimLeft(value[8:], " \t\n\v\f\r")
		if !strings.HasPrefix(rest, "'") {
			return "", invalid("")
		}
		body := rest[1:]
		end := strings.IndexByte(body, '\'')
		if end < 0 || end != len(body)-1 {
			return "", invalid("")
		}
		body = body[:end]
		if IntervalParser == nil {
			return "", invalid("")
		}
		months, days, micros, ok := IntervalParser(body)
		if !ok {
			// interval_in raises its own error from inside the check hook.
			return "", &ValidationError{Code: "22007", Msg: fmt.Sprintf("invalid input syntax for type interval: %q", body)}
		}
		if months != 0 {
			return "", invalid("Cannot specify months in time zone interval.")
		}
		if days != 0 {
			return "", invalid("Cannot specify days in time zone interval.")
		}
		return offsetZoneName(-(micros / 1_000_000), invalid)
	}
	if hours, err := strconv.ParseFloat(strings.TrimLeft(value, " \t\n\v\f\r"), 64); err == nil && value != "" {
		if math.IsNaN(hours) || math.IsInf(hours, 0) {
			return "", invalid("UTC timezone offset is out of range.")
		}
		return offsetZoneName(int64(-hours*3600), invalid)
	}
	if name, ok := zoneName(value); ok {
		return name, nil
	}
	return "", invalid("")
}

// checkLogTimeZone is check_log_timezone: a zone name only.
func checkLogTimeZone(value string) (string, error) {
	if name, ok := zoneName(value); ok {
		return name, nil
	}
	return "", &ValidationError{Msg: fmt.Sprintf("invalid value for parameter %q: %q", "log_timezone", value)}
}

// zoneName is pg_tzset's name resolution: a tz database file first, then a
// POSIX TZ specification, canonicalised to upper case.
func zoneName(value string) (string, bool) {
	if name, ok := lookupZoneFile(value); ok {
		return name, true
	}
	upper := strings.ToUpper(value)
	if upper == "" || upper[0] == ':' {
		return "", false
	}
	if _, ok := parsePOSIXTZ(upper); ok {
		return upper, true
	}
	return "", false
}

// offsetZoneName is pg_tzset_offset: gmtoffset is seconds WEST of UTC (the
// Unix sign convention), and the zone is named <+hh[:mm[:ss]]>-hh[:mm[:ss]]
// (or the mirror image for a west offset).
func offsetZoneName(gmtoffset int64, invalid func(string) error) (string, error) {
	abs := gmtoffset
	if abs < 0 {
		abs = -abs
	}
	if abs > maxPOSIXOffsetSecs {
		return "", invalid("UTC timezone offset is out of range.")
	}
	off := fmt.Sprintf("%02d", abs/3600)
	if rem := abs % 3600; rem != 0 {
		off += fmt.Sprintf(":%02d", rem/60)
		if rem%60 != 0 {
			off += fmt.Sprintf(":%02d", rem%60)
		}
	}
	if gmtoffset > 0 {
		return "<-" + off + ">+" + off, nil
	}
	return "<+" + off + ">-" + off, nil
}

// posixTZ is the standard-time part of a parsed POSIX TZ specification.
type posixTZ struct {
	stdName    string
	stdOffWest int // seconds WEST of UTC, as written (POSIX sign)
}

// parsePOSIXTZ is tzparse (src/timezone/localtime.c) for its acceptance
// decision: std offset [dst [offset] [{,|;}rule,rule]]. Names are either
// <quoted> or a run of characters other than digits, ',', '-' and '+'; the
// standard name may be empty but its offset is required; offsets are
// [+-]hh[:mm[:ss]] with hours < 168; rules are Jn (1..365), n (0..365) or
// Mm.w.d, each with an optional /offset time. A DST name without rules gets
// TZDEFRULESTRING (,M3.2.0,M11.1.0).
func parsePOSIXTZ(s string) (posixTZ, bool) {
	var p posixTZ
	name, rest, ok := posixName(s)
	if !ok || rest == "" {
		return p, false
	}
	off, rest, ok := posixOffset(rest)
	if !ok {
		return p, false
	}
	p.stdName, p.stdOffWest = name, off
	if rest == "" {
		return p, true
	}
	dst, rest, ok := posixName(rest)
	if !ok || dst == "" {
		return p, false
	}
	if rest != "" && rest[0] != ',' && rest[0] != ';' {
		if _, rest, ok = posixOffset(rest); !ok {
			return p, false
		}
	}
	if rest == "" {
		return p, true
	}
	if rest[0] != ',' && rest[0] != ';' {
		return p, false
	}
	rest, ok = posixRule(rest[1:])
	if !ok || rest == "" || rest[0] != ',' {
		return p, false
	}
	rest, ok = posixRule(rest[1:])
	return p, ok && rest == ""
}

// posixName is getqzname / getzname.
func posixName(s string) (name, rest string, ok bool) {
	if strings.HasPrefix(s, "<") {
		end := strings.IndexByte(s, '>')
		if end < 0 {
			return "", "", false
		}
		return s[1:end], s[end+1:], true
	}
	i := 0
	for i < len(s) {
		c := s[i]
		if (c >= '0' && c <= '9') || c == ',' || c == '-' || c == '+' {
			break
		}
		i++
	}
	return s[:i], s[i:], true
}

// posixNum is getnum.
func posixNum(s string, min, max int) (int, string, bool) {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return 0, s, false
	}
	n, i := 0, 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int(s[i]-'0')
		if n > max {
			return 0, s, false
		}
		i++
	}
	if n < min {
		return 0, s, false
	}
	return n, s[i:], true
}

// posixOffset is getoffset / getsecs.
func posixOffset(s string) (int, string, bool) {
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	h, s, ok := posixNum(s, 0, 24*7-1)
	if !ok {
		return 0, s, false
	}
	secs := h * 3600
	if s != "" && s[0] == ':' {
		m, r, ok := posixNum(s[1:], 0, 59)
		if !ok {
			return 0, s, false
		}
		secs, s = secs+m*60, r
		if s != "" && s[0] == ':' {
			sec, r, ok := posixNum(s[1:], 0, 60)
			if !ok {
				return 0, s, false
			}
			secs, s = secs+sec, r
		}
	}
	if neg {
		secs = -secs
	}
	return secs, s, true
}

// posixRule is getrule.
func posixRule(s string) (string, bool) {
	var ok bool
	switch {
	case strings.HasPrefix(s, "J"):
		_, s, ok = posixNum(s[1:], 1, 365)
	case strings.HasPrefix(s, "M"):
		if _, s, ok = posixNum(s[1:], 1, 12); !ok || !strings.HasPrefix(s, ".") {
			return s, false
		}
		if _, s, ok = posixNum(s[1:], 1, 5); !ok || !strings.HasPrefix(s, ".") {
			return s, false
		}
		_, s, ok = posixNum(s[1:], 0, 6)
	default:
		_, s, ok = posixNum(s, 0, 365)
	}
	if !ok {
		return s, false
	}
	if strings.HasPrefix(s, "/") {
		_, s, ok = posixOffset(s[1:])
	}
	return s, ok
}

// ZoneLocation resolves a stored TimeZone value to a *time.Location: a tz
// database name, or a POSIX TZ specification such as <+02>-02, UTC+3 or
// EST5EDT,M3.2.0,M11.1.0 (what SET TIME ZONE INTERVAL / a numeric offset
// store). "" and anything unresolvable are UTC. Every reader of the session
// zone — timestamptz output, EXTRACT, local-time resolution — goes through
// here so they agree.
func ZoneLocation(zone string) *time.Location {
	return sessionLocation(zone)
}

// posixLocation builds the zone a POSIX TZ string describes. Go's time
// package already implements POSIX TZ rules for the footer of a TZif file,
// so the location is a TZif v2 image with a single standard-time type, no
// transitions and the specification as its footer.
func posixLocation(spec string) (*time.Location, bool) {
	p, ok := parsePOSIXTZ(spec)
	if !ok {
		return nil, false
	}
	// tzparse accepts ';' before the first rule; Go's footer reader wants ','.
	footer := spec
	if i := strings.IndexByte(footer, ';'); i >= 0 {
		footer = footer[:i] + "," + footer[i+1:]
	}
	if strings.ContainsAny(footer, "\n") {
		return nil, false
	}
	chars := append([]byte(p.stdName), 0)
	block := func(version byte) []byte {
		var b []byte
		b = append(b, "TZif"...)
		b = append(b, version)
		b = append(b, make([]byte, 15)...)
		for _, n := range []uint32{0, 0, 0, 0, 1, uint32(len(chars))} {
			b = binary.BigEndian.AppendUint32(b, n)
		}
		b = binary.BigEndian.AppendUint32(b, uint32(int32(-p.stdOffWest)))
		b = append(b, 0, 0) // isdst, abbreviation index
		return append(b, chars...)
	}
	data := append(block('2'), block('2')...)
	data = append(data, '\n')
	data = append(data, footer...)
	data = append(data, '\n')
	loc, err := time.LoadLocationFromTZData(spec, data)
	if err != nil {
		return nil, false
	}
	return loc, true
}
