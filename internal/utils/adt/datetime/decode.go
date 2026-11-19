package datetime

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file ports the date and timestamp input path of
// postgres/src/backend/utils/adt/datetime.c: ParseDateTime, DecodeDateTime,
// DecodeDate, ValidateDate, DecodeTime, DecodeNumber, DecodeNumberField,
// DecodeTimezone, DecodeTimezoneAbbrev and DecodeSpecial.

// Input errors, matching DateTimeParseError's cases.
var (
	// ErrBadFormat is DTERR_BAD_FORMAT.
	ErrBadFormat = errors.New("invalid date/time syntax")
	// ErrMDFieldOverflow is DTERR_MD_FIELD_OVERFLOW: a field overflow that may come
	// from the wrong DateStyle. It also matches ErrFieldOutOfRange.
	ErrMDFieldOverflow = fmt.Errorf("%w: month or day", ErrFieldOutOfRange)
	// ErrTZDispOverflow is DTERR_TZDISP_OVERFLOW.
	ErrTZDispOverflow = errors.New("time zone displacement out of range")
)

// BadTimezoneError is DTERR_BAD_TIMEZONE.
type BadTimezoneError struct{ Name string }

func (e *BadTimezoneError) Error() string {
	return fmt.Sprintf("time zone %q not recognized", e.Name)
}

// DateOrder is the field order of the DateStyle setting.
type DateOrder int

// The DateStyle orders.
const (
	DateOrderYMD DateOrder = iota
	DateOrderDMY
	DateOrderMDY
)

// DecodeEnv is the session state DecodeDateTime reads.
type DecodeEnv struct {
	Order DateOrder
	// Now is the statement time, for now, today, tomorrow and yesterday.
	Now time.Time
	// Session is the TimeZone setting's zone.
	Session *time.Location
	// LookupZone is pg_tzset: a zone by name, in any letter case.
	LookupZone func(name string) (*time.Location, bool)
	// WestOffset is DetermineTimeZoneOffset: seconds west of UTC of a wall clock,
	// given as UTC-tagged fields, in loc.
	WestOffset func(wall time.Time, loc *time.Location) int
}

// DecodeType is DecodeDateTime's dtype result.
type DecodeType int

// The kinds of decoded input.
const (
	DecodedDate DecodeType = iota
	DecodedEpoch
	DecodedLate
	DecodedEarly
)

// Decoded is the result of DecodeDateTime. Year is astronomical, so 1 BC is 0.
// UsedSession reports that the value depends on session settings (time zone or
// DateStyle), so it must not be cached with the statement.
type Decoded struct {
	Type                            DecodeType
	Year, Mon, Mday, Hour, Min, Sec int
	Usec                            int
	TZWest                          int
	UsedSession                     bool
}

// Wall is the fields as a UTC-tagged time. Hour 24 and second 60 carry over, as
// they do in tm2timestamp.
func (d Decoded) Wall() time.Time {
	return time.Date(d.Year, time.Month(d.Mon), d.Mday, d.Hour, d.Min, d.Sec, d.Usec*1000, time.UTC)
}

// Field types from ParseDateTime.
const (
	dtkNumber = iota
	dtkString
	dtkDate
	dtkTime
	dtkTZ
	dtkSpecial
)

// Token types (datetime.h); the values are bit positions for the field masks.
const (
	tokReserv = iota
	tokMonth
	tokYear
	tokDay
	tokTZ
	tokDTZ
	tokDynTZ
	tokIgnore
	tokAmPm
	tokHour
	tokMinute
	tokSecond
	tokDOY
	tokDOW
	tokUnits
	tokADBC
	tokDTZMod
	tokISOTime
	tokUnknown
)

func dtkM(t int) int { return 1 << t }

var (
	dtkDateM = dtkM(tokYear) | dtkM(tokMonth) | dtkM(tokDay)
	dtkTimeM = dtkM(tokHour) | dtkM(tokMinute) | dtkM(tokSecond)
)

// Token values.
const (
	valAM = iota
	valPM
	valHR24
	valAD
	valBC
)
const (
	valNow = iota
	valYesterday
	valToday
	valTomorrow
	valZulu
	valEpoch
	valLate
	valEarly
)
const (
	unitJulian = iota + 1
	unitTime
	unitOther
)

type dateToken struct{ kind, value int }

func monthTokens(m map[string]dateToken) {
	names := [...][]string{{"jan", "january"}, {"feb", "february"}, {"mar", "march"}, {"apr", "april"},
		{"may"}, {"jun", "june"}, {"jul", "july"}, {"aug", "august"}, {"sep", "sept", "september"},
		{"oct", "october"}, {"nov", "november"}, {"dec", "december"}}
	for i, list := range names {
		for _, n := range list {
			m[n] = dateToken{tokMonth, i + 1}
		}
	}
}

// dateTokens is datetktbl.
var dateTokens = func() map[string]dateToken {
	m := map[string]dateToken{
		"+infinity": {tokReserv, valLate}, "-infinity": {tokReserv, valEarly}, "infinity": {tokReserv, valLate},
		"ad": {tokADBC, valAD}, "bc": {tokADBC, valBC}, "allballs": {tokReserv, valZulu},
		"am": {tokAmPm, valAM}, "pm": {tokAmPm, valPM}, "at": {tokIgnore, 0}, "on": {tokIgnore, 0},
		"dst": {tokDTZMod, 3600}, "epoch": {tokReserv, valEpoch}, "now": {tokReserv, valNow},
		"today": {tokReserv, valToday}, "tomorrow": {tokReserv, valTomorrow},
		"yesterday": {tokReserv, valYesterday}, "t": {tokISOTime, unitTime},
		"j": {tokUnits, unitJulian}, "jd": {tokUnits, unitJulian}, "julian": {tokUnits, unitJulian},
	}
	for _, u := range [...]string{"d", "dow", "doy", "h", "isodow", "isoyear", "m", "mm", "s", "y"} {
		m[u] = dateToken{tokUnits, unitOther}
	}
	days := [...][]string{{"sun", "sunday"}, {"mon", "monday"}, {"tue", "tues", "tuesday"},
		{"wed", "weds", "wednesday"}, {"thu", "thur", "thurs", "thursday"}, {"fri", "friday"},
		{"sat", "saturday"}}
	for i, list := range days {
		for _, n := range list {
			m[n] = dateToken{tokDOW, i}
		}
	}
	monthTokens(m)
	return m
}()

// tokMaxLen is TOKMAXLEN: tokens compare on this many leading characters.
const tokMaxLen = 10

func lookupDateToken(s string) (dateToken, bool) {
	if len(s) > tokMaxLen {
		s = s[:tokMaxLen]
	}
	t, ok := dateTokens[s]
	return t, ok
}

type zoneAbbrev struct {
	kind, offset int
	zone         string
}

func isAlnum(c byte) bool { return isDigit(c) || isAlpha(c) }
func isSpace(c byte) bool { return c == ' ' || (c >= '\t' && c <= '\r') }
func isPunct(c byte) bool { return c > ' ' && c < 0x7f && !isAlnum(c) }
func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// Limits of timestamp_in's work buffer and field arrays.
const (
	maxDateLen    = 128
	maxDateFields = 25
)

type dateField struct {
	text string
	kind int
}

// ParseDateTime breaks input into typed fields, with text in lower case.
func parseDateTimeFields(s string) ([]dateField, error) {
	var fields []dateField
	i, used := 0, 0
	at := func() byte {
		if i < len(s) {
			return s[i]
		}
		return 0
	}
	for i < len(s) {
		if s[i] == 0 {
			break
		}
		if isSpace(s[i]) {
			i++
			continue
		}
		if len(fields) >= maxDateFields {
			return nil, ErrBadFormat
		}
		var b []byte
		take := func(c byte) { b = append(b, c); i++ }
		kind := dtkNumber
		c := s[i]
		switch {
		case isDigit(c):
			take(c)
			for isDigit(at()) {
				take(at())
			}
			switch d := at(); {
			case d == ':':
				kind = dtkTime
				take(d)
				for isDigit(at()) || at() == ':' || at() == '.' {
					take(at())
				}
			case d == '-' || d == '/' || d == '.':
				take(d)
				if isDigit(at()) {
					kind = dtkDate
					if d == '.' {
						kind = dtkNumber
					}
					for isDigit(at()) {
						take(at())
					}
					if at() == d {
						kind = dtkDate
						take(d)
						for isDigit(at()) || at() == d {
							take(at())
						}
					}
				} else {
					kind = dtkDate
					for isAlnum(at()) || at() == d {
						take(lower(at()))
					}
				}
			}
		case c == '.':
			take(c)
			for isDigit(at()) {
				take(at())
			}
		case isAlpha(c):
			kind = dtkString
			take(lower(c))
			for isAlpha(at()) {
				take(lower(at()))
			}
			isDate := false
			switch d := at(); {
			case d == '-' || d == '/' || d == '.':
				isDate = true
			case d == '+' || isDigit(d):
				_, known := lookupDateToken(string(b))
				isDate = !known
			}
			if isDate {
				kind = dtkDate
				for {
					take(lower(at()))
					d := at()
					if !(d == '+' || d == '-' || d == '/' || d == '_' || d == '.' || d == ':' || isAlnum(d)) {
						break
					}
				}
			}
		case c == '+' || c == '-':
			take(c)
			for isSpace(at()) {
				i++
			}
			switch d := at(); {
			case isDigit(d):
				kind = dtkTZ
				take(d)
				for isDigit(at()) || at() == ':' || at() == '.' || at() == '-' {
					take(at())
				}
			case isAlpha(d):
				kind = dtkSpecial
				take(lower(d))
				for isAlpha(at()) {
					take(lower(at()))
				}
			default:
				return nil, ErrBadFormat
			}
		case isPunct(c):
			i++
			continue
		default:
			return nil, ErrBadFormat
		}
		used += len(b) + 1
		if used >= maxDateLen+maxDateFields {
			return nil, ErrBadFormat
		}
		fields = append(fields, dateField{string(b), kind})
	}
	return fields, nil
}

// strtoint is strtoint(s, &rest, 10): optional sign and digits. noDigits reports
// that nothing was consumed.
func strtoint(s string) (val int, rest string, overflow, noDigits bool) {
	i := 0
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	var n int64
	for i < len(s) && isDigit(s[i]) {
		if n <= math.MaxInt32+1 {
			n = n*10 + int64(s[i]-'0')
		}
		i++
	}
	if i == start {
		return 0, s, false, true
	}
	if neg {
		n = -n
	}
	if n > math.MaxInt32 || n < math.MinInt32 {
		return 0, s[i:], true, false
	}
	return int(n), s[i:], false, false
}

func atoi(s string) int {
	v, _, _, _ := strtoint(s)
	return v
}

// parseFraction is ParseFraction: s starts at the decimal point.
func parseFraction(s string) (float64, error) {
	if len(s) == 1 {
		return 0, nil
	}
	for i := 1; i < len(s); i++ {
		if !isDigit(s[i]) {
			return 0, ErrBadFormat
		}
	}
	f, err := strconv.ParseFloat("0"+s, 64)
	if err != nil {
		return 0, ErrBadFormat
	}
	return f, nil
}

func decodeFractionalSecond(s string) (int, error) {
	f, err := parseFraction(s)
	if err != nil {
		return 0, err
	}
	return int(math.RoundToEven(f * 1000000)), nil
}

// date2j is the Julian day conversion of timestamp.c; j2date is its inverse.
func date2j(y, m, d int) int {
	if m > 2 {
		m++
		y += 4800
	} else {
		m += 13
		y += 4799
	}
	century := y / 100
	julian := y*365 - 32167
	julian += y/4 - century + century/4
	return julian + 7834*m/256 + d
}

var monthDays = [2][12]int{
	{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31},
	{31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31},
}

type decoder struct {
	env           *DecodeEnv
	d             Decoded
	yday          int
	haveTextMonth bool
	is2digits     bool
	isjulian      bool
}

func (x *decoder) location() *time.Location {
	if x.env.Session != nil {
		return x.env.Session
	}
	return time.UTC
}

// decodeNumberField is DecodeNumberField.
func (x *decoder) decodeNumberField(str string, fmask int) (tmask int, err error) {
	for i := 0; i < len(str); i++ {
		if !isDigit(str[i]) && str[i] != '.' {
			return 0, ErrBadFormat
		}
	}
	d := &x.d
	if cp := strings.IndexByte(str, '.'); cp >= 0 {
		if d.Usec, err = decodeFractionalSecond(str[cp:]); err != nil {
			return 0, err
		}
		str = str[:cp]
	} else if fmask&dtkDateM != dtkDateM && len(str) >= 6 {
		n := len(str)
		d.Mday = atoi(str[n-2:])
		d.Mon = atoi(str[n-4 : n-2])
		d.Year = atoi(str[:n-4])
		if n-4 == 2 {
			x.is2digits = true
		}
		return dtkDateM, nil
	}
	if fmask&dtkTimeM != dtkTimeM {
		switch len(str) {
		case 6:
			d.Sec = atoi(str[4:])
			d.Min = atoi(str[2:4])
			d.Hour = atoi(str[:2])
			return dtkTimeM, nil
		case 4:
			d.Sec = 0
			d.Min = atoi(str[2:])
			d.Hour = atoi(str[:2])
			return dtkTimeM, nil
		}
	}
	return 0, ErrBadFormat
}

// decodeNumber is DecodeNumber.
func (x *decoder) decodeNumber(str string, fmask int) (tmask int, err error) {
	flen := len(str)
	val, cp, overflow, noDigits := strtoint(str)
	if overflow {
		return 0, ErrFieldOutOfRange
	}
	if noDigits {
		return 0, ErrBadFormat
	}
	d := &x.d
	if strings.HasPrefix(cp, ".") {
		if len(str)-len(cp) > 2 {
			return x.decodeNumberField(str, fmask|dtkDateM)
		}
		if d.Usec, err = decodeFractionalSecond(cp); err != nil {
			return 0, err
		}
	} else if cp != "" {
		return 0, ErrBadFormat
	}
	if flen == 3 && fmask&dtkDateM == dtkM(tokYear) && val >= 1 && val <= 366 {
		x.yday = val
		return dtkM(tokDOY) | dtkM(tokMonth) | dtkM(tokDay), nil
	}
	order := x.env.Order
	switch fmask & dtkDateM {
	case 0:
		d.UsedSession = d.UsedSession || flen < 3 // the field order decides
		switch {
		case flen >= 3 || order == DateOrderYMD:
			tmask, d.Year = dtkM(tokYear), val
		case order == DateOrderDMY:
			tmask, d.Mday = dtkM(tokDay), val
		default:
			tmask, d.Mon = dtkM(tokMonth), val
		}
	case dtkM(tokYear), dtkM(tokDay):
		tmask, d.Mon = dtkM(tokMonth), val
	case dtkM(tokMonth):
		d.UsedSession = d.UsedSession || (x.haveTextMonth && flen < 3)
		if x.haveTextMonth && (flen >= 3 || order == DateOrderYMD) {
			tmask, d.Year = dtkM(tokYear), val
		} else {
			tmask, d.Mday = dtkM(tokDay), val
		}
	case dtkM(tokYear) | dtkM(tokMonth):
		tmask = dtkM(tokDay)
		if x.haveTextMonth && flen >= 3 && x.is2digits {
			d.Mday, d.Year = d.Year, val
			x.is2digits = false
		} else {
			d.Mday = val
		}
	case dtkM(tokMonth) | dtkM(tokDay):
		tmask, d.Year = dtkM(tokYear), val
	case dtkDateM:
		return x.decodeNumberField(str, fmask)
	default:
		return 0, ErrBadFormat
	}
	if tmask == dtkM(tokYear) {
		x.is2digits = flen <= 2
	}
	return tmask, nil
}

// decodeDate is DecodeDate.
func (x *decoder) decodeDate(str string, fmask int) (tmask int, err error) {
	var fields []string
	i := 0
	for i < len(str) && len(fields) < 25 {
		for i < len(str) && !isAlnum(str[i]) {
			i++
		}
		if i >= len(str) {
			return 0, ErrBadFormat
		}
		start := i
		if isDigit(str[i]) {
			for i < len(str) && isDigit(str[i]) {
				i++
			}
		} else {
			for i < len(str) && isAlpha(str[i]) {
				i++
			}
		}
		fields = append(fields, str[start:i])
		if i < len(str) {
			i++
		}
	}
	saveText := x.haveTextMonth
	x.haveTextMonth = false
	defer func() { x.haveTextMonth = saveText }()
	done := make([]bool, len(fields))
	for k, f := range fields {
		if !isAlpha(f[0]) {
			continue
		}
		tok, ok := lookupDateToken(f)
		if ok && tok.kind == tokIgnore {
			continue
		}
		if !ok || tok.kind != tokMonth {
			return 0, ErrBadFormat
		}
		x.d.Mon = tok.value
		x.haveTextMonth = true
		dmask := dtkM(tokMonth)
		if fmask&dmask != 0 {
			return 0, ErrBadFormat
		}
		fmask |= dmask
		tmask |= dmask
		done[k] = true
	}
	for k, f := range fields {
		if done[k] {
			continue
		}
		dmask, err := x.decodeNumber(f, fmask)
		if err != nil {
			return 0, err
		}
		if fmask&dmask != 0 {
			return 0, ErrBadFormat
		}
		fmask |= dmask
		tmask |= dmask
	}
	if fmask&^(dtkM(tokDOY)|dtkM(tokTZ)) != dtkDateM {
		return 0, ErrBadFormat
	}
	return tmask, nil
}

// decodeTime is DecodeTime for a timestamp.
func (x *decoder) decodeTime(str string) error {
	hour, cp, overflow, noDigits := strtoint(str)
	if overflow {
		return ErrFieldOutOfRange
	}
	if noDigits || !strings.HasPrefix(cp, ":") {
		return ErrBadFormat
	}
	minute, cp, overflow, _ := strtoint(cp[1:])
	if overflow {
		return ErrFieldOutOfRange
	}
	sec, usec := 0, 0
	var err error
	switch {
	case cp == "":
	case cp[0] == '.':
		// mm:ss.sss
		if usec, err = decodeFractionalSecond(cp); err != nil {
			return err
		}
		sec, minute, hour = minute, hour, 0
	case cp[0] == ':':
		sec, cp, overflow, _ = strtoint(cp[1:])
		if overflow {
			return ErrFieldOutOfRange
		}
		if strings.HasPrefix(cp, ".") {
			if usec, err = decodeFractionalSecond(cp); err != nil {
				return err
			}
		} else if cp != "" {
			return ErrBadFormat
		}
	default:
		return ErrBadFormat
	}
	if hour < 0 || minute < 0 || minute > 59 || sec < 0 || sec > 60 || usec < 0 || usec > 1000000 {
		return ErrFieldOutOfRange
	}
	d := &x.d
	d.Hour, d.Min, d.Sec, d.Usec = hour, minute, sec, usec
	return nil
}

// decodeTimezone is DecodeTimezone; the result is seconds west of UTC.
func decodeTimezone(str string) (int, error) {
	if str == "" || (str[0] != '+' && str[0] != '-') {
		return 0, ErrBadFormat
	}
	hr, cp, overflow, _ := strtoint(str[1:])
	if overflow {
		return 0, ErrTZDispOverflow
	}
	minute, sec := 0, 0
	if strings.HasPrefix(cp, ":") {
		minute, cp, overflow, _ = strtoint(cp[1:])
		if overflow {
			return 0, ErrTZDispOverflow
		}
		if strings.HasPrefix(cp, ":") {
			sec, cp, overflow, _ = strtoint(cp[1:])
			if overflow {
				return 0, ErrTZDispOverflow
			}
		}
	} else if cp == "" && len(str) > 3 {
		minute = hr % 100
		hr /= 100
	}
	if hr < 0 || hr > 15 || minute < 0 || minute >= 60 || sec < 0 || sec >= 60 {
		return 0, ErrTZDispOverflow
	}
	tz := (hr*60+minute)*60 + sec
	if str[0] == '-' {
		tz = -tz
	}
	if cp != "" {
		return 0, ErrBadFormat
	}
	return -tz, nil
}

// zonePeriod is one stretch of a zone's history.
type zonePeriod struct {
	start  int64
	abbrev string
	offset int
	isDST  bool
}

var zonePeriodCache sync.Map // *time.Location -> []zonePeriod

// zonePeriods lists the abbreviations loc has used, oldest first.
func zonePeriods(loc *time.Location) []zonePeriod {
	if v, ok := zonePeriodCache.Load(loc); ok {
		return v.([]zonePeriod)
	}
	var out []zonePeriod
	t := time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC).In(loc)
	limit := time.Date(2038, 1, 1, 0, 0, 0, 0, time.UTC)
	for {
		name, off := t.Zone()
		out = append(out, zonePeriod{t.Unix(), strings.ToUpper(name), off, t.IsDST()})
		_, end := t.ZoneBounds()
		// A zone built from a POSIX TZ string (misc.posixLocation) can
		// report a bound that does not move forward; stop there rather
		// than loop and grow out without end.
		if end.IsZero() || !end.After(t) || end.After(limit) {
			break
		}
		t = end
	}
	zonePeriodCache.Store(loc, out)
	return out
}

// abbrevInZone is TimeZoneAbbrevIsKnown: east offset of the abbreviation in loc,
// and whether it has only one meaning there.
func abbrevInZone(abbr string, loc *time.Location) (offset int, isDST, fixed, ok bool) {
	up := strings.ToUpper(abbr)
	for _, p := range zonePeriods(loc) {
		if p.abbrev != up {
			continue
		}
		if !ok {
			offset, isDST, fixed, ok = p.offset, p.isDST, true, true
		} else if p.offset != offset || p.isDST != isDST {
			fixed = false
		}
	}
	return offset, isDST, fixed, ok
}

// abbrevOffsetAt is DetermineTimeZoneAbbrevOffset's probe: the meaning in force at
// instant t, or else the next one.
func abbrevOffsetAt(abbr string, loc *time.Location, t int64) (int, bool) {
	up := strings.ToUpper(abbr)
	found, offset := false, 0
	for _, p := range zonePeriods(loc) {
		if p.abbrev != up {
			continue
		}
		if p.start > t && found {
			break
		}
		offset, found = p.offset, true
		if p.start > t {
			break
		}
	}
	return offset, found
}

// westOffsetOf computes DetermineTimeZoneOffset of the decoded fields.
func (x *decoder) westOffsetOf(loc *time.Location) int {
	d := x.d
	d.Usec = 0
	return x.env.WestOffset(d.Wall(), loc)
}

// validateDate is ValidateDate.
func (x *decoder) validateDate(fmask int, bc bool) error {
	d := &x.d
	if fmask&dtkM(tokYear) != 0 {
		switch {
		case x.isjulian:
		case bc:
			if d.Year <= 0 {
				return ErrFieldOutOfRange
			}
			d.Year = -(d.Year - 1)
		case x.is2digits:
			if d.Year < 0 {
				return ErrFieldOutOfRange
			}
			if d.Year < 70 {
				d.Year += 2000
			} else if d.Year < 100 {
				d.Year += 1900
			}
		case d.Year <= 0:
			return ErrFieldOutOfRange
		}
	}
	if fmask&dtkM(tokDOY) != 0 {
		d.Year, d.Mon, d.Mday = j2date(int64(date2j(d.Year, 1, 1) + x.yday - 1))
	}
	if fmask&dtkM(tokMonth) != 0 && (d.Mon < 1 || d.Mon > 12) {
		return ErrMDFieldOverflow
	}
	if fmask&dtkM(tokDay) != 0 && (d.Mday < 1 || d.Mday > 31) {
		return ErrMDFieldOverflow
	}
	if fmask&dtkDateM == dtkDateM {
		leap := 0
		if isLeapYear(d.Year) {
			leap = 1
		}
		if d.Mday > monthDays[leap][d.Mon-1] {
			return ErrFieldOutOfRange
		}
	}
	return nil
}

// timeOverflows is time_overflows.
func timeOverflows(h, m, s, usec int) bool {
	if h < 0 || h > 24 || m < 0 || m > 59 || s < 0 || s > 60 || usec < 0 || usec > 1000000 {
		return true
	}
	return int64(h)*3600000000+int64(m)*60000000+int64(s)*1000000+int64(usec) > 86400000000
}

// DecodeDateTimeInput is ParseDateTime followed by DecodeDateTime. Input with only
// a time of day is a format error, as all of DecodeDateTime's callers treat it.
func DecodeDateTimeInput(s string, env *DecodeEnv) (Decoded, error) {
	fields, err := parseDateTimeFields(s)
	if err != nil {
		return Decoded{}, err
	}
	x := &decoder{env: env}
	return x.run(fields)
}

func (x *decoder) run(fields []dateField) (Decoded, error) {
	var (
		fmask, tmask, ptype int
		mer                 = valHR24
		bc                  bool
		namedTz, abbrevTz   *time.Location
		abbrev              string
		err                 error
	)
	d := &x.d
	d.Type = DecodedDate
	for _, f := range fields {
		text := f.text
		switch f.kind {
		case dtkDate:
			switch {
			case ptype == unitJulian:
				jday, cp, overflow, _ := strtoint(text)
				if overflow || jday < 0 {
					return *d, ErrFieldOutOfRange
				}
				d.Year, d.Mon, d.Mday = j2date(int64(jday))
				x.isjulian = true
				if d.TZWest, err = decodeTimezone(cp); err != nil {
					return *d, err
				}
				tmask = dtkDateM | dtkTimeM | dtkM(tokTZ)
				ptype = 0
			case ptype != 0 || fmask&(dtkM(tokMonth)|dtkM(tokDay)) == dtkM(tokMonth)|dtkM(tokDay):
				if isDigit(text[0]) || ptype != 0 {
					if ptype != 0 {
						if ptype != unitTime {
							return *d, ErrBadFormat
						}
						ptype = 0
					}
					if fmask&dtkTimeM == dtkTimeM {
						return *d, ErrBadFormat
					}
					cp := strings.IndexByte(text, '-')
					if cp < 0 {
						return *d, ErrBadFormat
					}
					if d.TZWest, err = decodeTimezone(text[cp:]); err != nil {
						return *d, err
					}
					if tmask, err = x.decodeNumberField(text[:cp], fmask); err != nil {
						return *d, err
					}
					tmask |= dtkM(tokTZ)
				} else {
					loc, ok := x.env.LookupZone(text)
					if !ok {
						return *d, &BadTimezoneError{Name: text}
					}
					namedTz = loc
					tmask = dtkM(tokTZ)
				}
			default:
				if tmask, err = x.decodeDate(text, fmask); err != nil {
					return *d, err
				}
			}
		case dtkTime:
			if ptype != 0 {
				if ptype != unitTime {
					return *d, ErrBadFormat
				}
				ptype = 0
			}
			tmask = dtkTimeM
			if err = x.decodeTime(text); err != nil {
				return *d, err
			}
			if timeOverflows(d.Hour, d.Min, d.Sec, d.Usec) {
				return *d, ErrFieldOutOfRange
			}
		case dtkTZ:
			if d.TZWest, err = decodeTimezone(text); err != nil {
				return *d, err
			}
			tmask = dtkM(tokTZ)
		case dtkNumber:
			if ptype != 0 {
				value, cp, overflow, _ := strtoint(text)
				if overflow {
					return *d, ErrFieldOutOfRange
				}
				if cp != "" && cp[0] != '.' {
					return *d, ErrBadFormat
				}
				switch ptype {
				case unitJulian:
					if value < 0 {
						return *d, ErrFieldOutOfRange
					}
					tmask = dtkDateM
					d.Year, d.Mon, d.Mday = j2date(int64(value))
					x.isjulian = true
					if cp != "" {
						frac, err := parseFraction(cp)
						if err != nil {
							return *d, err
						}
						us := int64(frac * 86400000000)
						d.Hour = int(us / 3600000000)
						us -= int64(d.Hour) * 3600000000
						d.Min = int(us / 60000000)
						us -= int64(d.Min) * 60000000
						d.Sec = int(us / 1000000)
						d.Usec = int(us - int64(d.Sec)*1000000)
						tmask |= dtkTimeM
					}
				case unitTime:
					if tmask, err = x.decodeNumberField(text, fmask|dtkDateM); err != nil {
						return *d, err
					}
					if tmask != dtkTimeM {
						return *d, ErrBadFormat
					}
				default:
					return *d, ErrBadFormat
				}
				ptype = 0
				d.Type = DecodedDate
				break
			}
			flen := len(text)
			cp := strings.IndexByte(text, '.')
			switch {
			case cp >= 0 && fmask&dtkDateM == 0:
				tmask, err = x.decodeDate(text, fmask)
			case cp > 2:
				tmask, err = x.decodeNumberField(text, fmask)
			case flen >= 6 && (fmask&dtkDateM == 0 || fmask&dtkTimeM == 0):
				tmask, err = x.decodeNumberField(text, fmask)
			default:
				tmask, err = x.decodeNumber(text, fmask)
			}
			if err != nil {
				return *d, err
			}
		case dtkString, dtkSpecial:
			kind, val := tokUnknown, 0
			var dynZone *time.Location
			// Abbreviations come before keywords: the session zone's first.
			if off, isDST, fixed, ok := abbrevInZone(text, x.location()); ok {
				d.UsedSession = true
				switch {
				case !fixed:
					kind, dynZone = tokDynTZ, x.location()
				case isDST:
					kind, val = tokDTZ, off
				default:
					kind, val = tokTZ, off
				}
			} else if ab, ok := defaultZoneAbbrevs[text]; ok {
				kind, val = ab.kind, ab.offset
				if ab.kind == tokDynTZ {
					if dynZone, ok = x.env.LookupZone(ab.zone); !ok {
						return *d, &BadTimezoneError{Name: ab.zone}
					}
				}
			} else if tok, ok := lookupDateToken(text); ok {
				kind, val = tok.kind, tok.value
			}
			if kind == tokIgnore {
				continue
			}
			tmask = dtkM(kind)
			switch kind {
			case tokReserv:
				switch val {
				case valNow:
					tmask = dtkDateM | dtkTimeM | dtkM(tokTZ)
					d.UsedSession = true
					now := x.env.Now.In(x.location())
					d.Year, d.Mon, d.Mday = now.Year(), int(now.Month()), now.Day()
					d.Hour, d.Min, d.Sec = now.Clock()
					d.Usec = now.Nanosecond() / 1000
					_, east := now.Zone()
					d.TZWest = -east
				case valYesterday, valToday, valTomorrow:
					tmask = dtkDateM
					d.UsedSession = true
					now := x.env.Now.In(x.location())
					j := date2j(now.Year(), int(now.Month()), now.Day())
					if val == valYesterday {
						j--
					} else if val == valTomorrow {
						j++
					}
					d.Year, d.Mon, d.Mday = j2date(int64(j))
				case valZulu:
					tmask = dtkTimeM | dtkM(tokTZ)
					d.Hour, d.Min, d.Sec, d.TZWest = 0, 0, 0, 0
				case valEpoch:
					tmask = dtkDateM | dtkTimeM | dtkM(tokTZ)
					d.Type = DecodedEpoch
				case valLate:
					tmask = dtkDateM | dtkTimeM | dtkM(tokTZ)
					d.Type = DecodedLate
				case valEarly:
					tmask = dtkDateM | dtkTimeM | dtkM(tokTZ)
					d.Type = DecodedEarly
				}
			case tokMonth:
				if fmask&dtkM(tokMonth) != 0 && !x.haveTextMonth && fmask&dtkM(tokDay) == 0 &&
					d.Mon >= 1 && d.Mon <= 31 {
					d.Mday = d.Mon
					tmask = dtkM(tokDay)
				}
				x.haveTextMonth = true
				d.Mon = val
			case tokDTZMod:
				tmask |= dtkM(tokDTZ)
				d.TZWest -= val
			case tokDTZ:
				tmask |= dtkM(tokTZ)
				d.TZWest = -val
			case tokTZ:
				d.TZWest = -val
			case tokDynTZ:
				tmask |= dtkM(tokTZ)
				abbrevTz, abbrev = dynZone, text
			case tokAmPm:
				mer = val
			case tokADBC:
				bc = val == valBC
			case tokDOW:
			case tokUnits:
				tmask = 0
				if ptype != 0 {
					return *d, ErrBadFormat
				}
				ptype = val
			case tokISOTime:
				tmask = 0
				if fmask&dtkDateM != dtkDateM || ptype != 0 {
					return *d, ErrBadFormat
				}
				ptype = val
			case tokUnknown:
				loc, ok := x.env.LookupZone(text)
				if !ok {
					return *d, ErrBadFormat
				}
				namedTz = loc
				tmask = dtkM(tokTZ)
			default:
				return *d, ErrBadFormat
			}
		default:
			return *d, ErrBadFormat
		}
		if tmask&fmask != 0 {
			return *d, ErrBadFormat
		}
		fmask |= tmask
	}
	if ptype != 0 {
		return *d, ErrBadFormat
	}
	if d.Type != DecodedDate {
		return *d, nil
	}
	if err := x.validateDate(fmask, bc); err != nil {
		return *d, err
	}
	if mer != valHR24 && d.Hour > 12 {
		return *d, ErrFieldOutOfRange
	}
	if mer == valAM && d.Hour == 12 {
		d.Hour = 0
	} else if mer == valPM && d.Hour != 12 {
		d.Hour += 12
	}
	if fmask&dtkDateM != dtkDateM {
		return *d, ErrBadFormat // also the time-only case
	}
	hasMod := fmask&dtkM(tokDTZMod) != 0
	if namedTz != nil {
		if hasMod {
			return *d, ErrBadFormat
		}
		d.TZWest = x.westOffsetOf(namedTz)
	}
	if abbrevTz != nil {
		if hasMod {
			return *d, ErrBadFormat
		}
		d.TZWest = x.westOffsetOf(abbrevTz)
		probe := d.Wall().Unix() + int64(d.TZWest)
		if east, ok := abbrevOffsetAt(abbrev, abbrevTz, probe); ok {
			d.TZWest = -east
		}
	}
	if fmask&dtkM(tokTZ) == 0 {
		if fmask&dtkM(tokDTZMod) != 0 {
			return *d, ErrBadFormat
		}
		d.UsedSession = true
		d.TZWest = x.westOffsetOf(x.location())
	}
	return *d, nil
}
