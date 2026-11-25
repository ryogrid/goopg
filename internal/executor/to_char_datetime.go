package executor

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/utils/misc"
)

// This file ports the date/time half of PostgreSQL's to_char: the format parser
// (parse_format), its keyword and suffix tables (DCH_keywords, DCH_suff) and
// DCH_to_char, all in postgres/src/backend/utils/adt/formatting.c.

// dchKeywords lists the template patterns in DCH_keywords' order; a pattern is
// found by its first match, so longer spellings come first. FX only affects
// parsing and prints nothing.
var dchKeywords = []struct{ text, id string }{
	{"A.D.", "A_D"}, {"A.M.", "A_M"}, {"AD", "AD"}, {"AM", "AM"}, {"B.C.", "B_C"}, {"BC", "BC"},
	{"CC", "CC"}, {"DAY", "DAY"}, {"DDD", "DDD"}, {"DD", "DD"}, {"DY", "DY"}, {"Day", "Day"},
	{"Dy", "Dy"}, {"D", "D"}, {"FF1", "FF1"}, {"FF2", "FF2"}, {"FF3", "FF3"}, {"FF4", "FF4"},
	{"FF5", "FF5"}, {"FF6", "FF6"}, {"FX", "FX"}, {"HH24", "HH24"}, {"HH12", "HH12"}, {"HH", "HH"},
	{"IDDD", "IDDD"}, {"ID", "ID"}, {"IW", "IW"}, {"IYYY", "IYYY"}, {"IYY", "IYY"}, {"IY", "IY"},
	{"I", "I"}, {"J", "J"}, {"MI", "MI"}, {"MM", "MM"}, {"MONTH", "MONTH"}, {"MON", "MON"},
	{"MS", "MS"}, {"Month", "Month"}, {"Mon", "Mon"}, {"OF", "OF"}, {"P.M.", "P_M"}, {"PM", "PM"},
	{"Q", "Q"}, {"RM", "RM"}, {"SSSSS", "SSSS"}, {"SSSS", "SSSS"}, {"SS", "SS"}, {"TZH", "TZH"},
	{"TZM", "TZM"}, {"TZ", "TZ"}, {"US", "US"}, {"WW", "WW"}, {"W", "W"}, {"Y,YYY", "Y_YYY"},
	{"YYYY", "YYYY"}, {"YYY", "YYY"}, {"YY", "YY"}, {"Y", "Y"},
	{"a.d.", "a_d"}, {"a.m.", "a_m"}, {"ad", "ad"}, {"am", "am"}, {"b.c.", "b_c"}, {"bc", "bc"},
	{"cc", "CC"}, {"day", "day"}, {"ddd", "DDD"}, {"dd", "DD"}, {"dy", "dy"}, {"d", "D"},
	{"ff1", "FF1"}, {"ff2", "FF2"}, {"ff3", "FF3"}, {"ff4", "FF4"}, {"ff5", "FF5"}, {"ff6", "FF6"},
	{"fx", "FX"}, {"hh24", "HH24"}, {"hh12", "HH12"}, {"hh", "HH"}, {"iddd", "IDDD"}, {"id", "ID"},
	{"iw", "IW"}, {"iyyy", "IYYY"}, {"iyy", "IYY"}, {"iy", "IY"}, {"i", "I"}, {"j", "J"},
	{"mi", "MI"}, {"mm", "MM"}, {"month", "month"}, {"mon", "mon"}, {"ms", "MS"}, {"of", "OF"},
	{"p.m.", "p_m"}, {"pm", "pm"}, {"q", "Q"}, {"rm", "rm"}, {"sssss", "SSSS"}, {"ssss", "SSSS"},
	{"ss", "SS"}, {"tzh", "TZH"}, {"tzm", "TZM"}, {"tz", "tz"}, {"us", "US"}, {"ww", "WW"},
	{"w", "W"}, {"y,yyy", "Y_YYY"}, {"yyyy", "YYYY"}, {"yyy", "YYY"}, {"yy", "YY"}, {"y", "Y"},
}

var (
	dchMonths     = [...]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	dchDays       = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	dchRomanUpper = [...]string{"I", "II", "III", "IV", "V", "VI", "VII", "VIII", "IX", "X", "XI", "XII"}
)

// dchSuffix is the set of modifiers on one template pattern.
type dchSuffix struct {
	fm, tm, thUpper, thLower bool
}

// dchValue is the broken-down time DCH_to_char formats. year is astronomical, so
// 1 BC is year 0, as in PG's tm_year.
type dchValue struct {
	year, month, day, hour, minute, second, micros int
	weekday, yearDay                               int
	julian                                         int
	isoYear, isoWeek, isoYearDay                   int
	zoneName                                       string
	zoneOffset                                     int
	// interval marks a value from interval_to_char: fields are as stored, may be
	// negative, and the year is not adjusted for BC.
	interval bool
}

func newDCHValue(t time.Time, zoneName string, zoneOffset int) dchValue {
	y, mo, d := t.Date()
	hh, mi, ss := t.Clock()
	// The calendar date, independent of the zone t is in.
	date := time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
	days := date.Unix() / 86400
	if date.Unix()%86400 < 0 {
		days--
	}
	isoYear, isoWeek := date.ISOWeek()
	isoWeekday := int(date.Weekday())
	if isoWeekday == 0 {
		isoWeekday = 7
	}
	return dchValue{
		year: y, month: int(mo), day: d, hour: hh, minute: mi, second: ss,
		micros: t.Nanosecond() / 1000, weekday: int(date.Weekday()), yearDay: date.YearDay(),
		julian: int(days) + 2440588, isoYear: isoYear, isoWeek: isoWeek,
		isoYearDay: (isoWeek-1)*7 + isoWeekday, zoneName: zoneName, zoneOffset: zoneOffset,
	}
}

// dchJ2Day is j2day.
func dchJ2Day(date int) int {
	date = (date + 1) % 7
	if date < 0 {
		date += 7
	}
	return date
}

// dchISOWeekStart is the Julian day of the Monday of ISO week 1 of year.
func dchISOWeekStart(year int) int {
	day4 := date2j(year, 1, 4)
	return day4 - dchJ2Day(day4-1)
}

// dchISOYearWeek is date2isoyear and date2isoweek. It takes raw fields, because
// an interval's month and day need not form a calendar date.
func dchISOYearWeek(year, month, day int) (int, int) {
	dayn := date2j(year, month, day)
	start := dchISOWeekStart(year)
	if dayn < start {
		year--
		start = dchISOWeekStart(year)
	}
	week := (dayn-start)/7 + 1
	if week >= 52 {
		if next := dchISOWeekStart(year + 1); dayn >= next {
			year++
			week = (dayn-next)/7 + 1
		}
	}
	return year, week
}

// newIntervalDCHValue is the field setup of interval_to_char (interval2itm).
func newIntervalDCHValue(d Datum) dchValue {
	const usecsPerSecond = int64(1000000)
	months := int(d.IntervalMonthsValue())
	micros := d.IntervalMicrosValue()
	v := dchValue{
		interval: true,
		year:     months / 12, month: months % 12, day: int(d.IntervalDaysValue()),
		hour:   int(micros / (3600 * usecsPerSecond)),
		minute: int(micros % (3600 * usecsPerSecond) / (60 * usecsPerSecond)),
		second: int(micros % (60 * usecsPerSecond) / usecsPerSecond),
		micros: int(micros % usecsPerSecond),
	}
	// PG's comment: yday approximates the total span in days.
	v.yearDay = (v.year*12+v.month)*30 + v.day
	v.julian = date2j(v.year, v.month, v.day)
	v.isoYear, v.isoWeek = dchISOYearWeek(v.year, v.month, v.day)
	v.isoYearDay = v.julian - dchISOWeekStart(v.isoYear) + 1
	return v
}

// timeOfDayAsInterval is time_interval.
func timeOfDayAsInterval(d Datum) Datum {
	t := d.TimeValue().UTC()
	hh, mi, ss := t.Clock()
	micros := (int64(hh)*3600+int64(mi)*60+int64(ss))*1000000 + int64(t.Nanosecond()/1000)
	return NewIntervalDatumFull(0, 0, micros)
}

// dchInvalidForInterval lists the patterns under INVALID_FOR_INTERVAL.
func dchInvalidForInterval(id string) bool {
	switch id {
	case "tz", "TZ", "TZH", "TZM", "OF", "A_D", "B_C", "AD", "BC", "a_d", "b_c", "ad", "bc",
		"MONTH", "Month", "month", "MON", "Mon", "mon",
		"DAY", "Day", "day", "DY", "Dy", "dy", "D", "ID":
		return true
	}
	return false
}

// dchDisplayYear is ADJUST_YEAR for a non-interval: BC years count from 1.
func dchDisplayYear(year int) int {
	if year <= 0 {
		return -(year - 1)
	}
	return year
}

// dchNumberSuffix is str_numth's ordinal for the digits already written.
func dchNumberSuffix(digits string, upper bool) string {
	last := digits[len(digits)-1]
	if len(digits) > 1 && digits[len(digits)-2] == '1' {
		last = 0
	}
	s := "th"
	switch last {
	case '1':
		s = "st"
	case '2':
		s = "nd"
	case '3':
		s = "rd"
	}
	if upper {
		return strings.ToUpper(s)
	}
	return s
}

func dchName(name string, suffix dchSuffix, upper, lower bool, pad bool) string {
	switch {
	case upper:
		name = strings.ToUpper(name)
	case lower:
		name = strings.ToLower(name)
	}
	if pad && !suffix.fm && !suffix.tm {
		return fmt.Sprintf("%-9s", name)
	}
	return name
}

// dchField renders one template pattern (DCH_to_char's switch).
func dchField(id string, s dchSuffix, v dchValue) string {
	zero := func(width, n int) string {
		if s.fm {
			width = 0
		}
		return fmt.Sprintf("%0*d", width, n)
	}
	// signed widens a field by one for its minus sign, as the "%0*d" calls do.
	signed := func(width, n int) string {
		if n < 0 {
			width++
		}
		return zero(width, n)
	}
	pm := v.hour%24 >= 12
	pick := func(cond bool, yes, no string) string {
		if cond {
			return yes
		}
		return no
	}
	year, isoYear := v.year, v.isoYear
	if !v.interval {
		year, isoYear = dchDisplayYear(v.year), dchDisplayYear(v.isoYear)
	}
	// The year patterns all take their width from the year itself.
	yearField := func(width, n int) string {
		if year < 0 {
			width++
		}
		return zero(width, n)
	}
	offset := v.zoneOffset
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	hour12 := v.hour % 12
	if hour12 == 0 {
		hour12 = 12
	}
	hourWidth := 2
	if v.hour < 0 {
		hourWidth = 3
	}
	switch id {
	case "A_M", "P_M":
		return pick(pm, "P.M.", "A.M.")
	case "AM", "PM":
		return pick(pm, "PM", "AM")
	case "a_m", "p_m":
		return pick(pm, "p.m.", "a.m.")
	case "am", "pm":
		return pick(pm, "pm", "am")
	case "HH", "HH12":
		return zero(hourWidth, hour12)
	case "HH24":
		return zero(hourWidth, v.hour)
	case "MI":
		return signed(2, v.minute)
	case "SS":
		return signed(2, v.second)
	case "FF1":
		return fmt.Sprintf("%01d", v.micros/100000)
	case "FF2":
		return fmt.Sprintf("%02d", v.micros/10000)
	case "FF3", "MS":
		return fmt.Sprintf("%03d", v.micros/1000)
	case "FF4":
		return fmt.Sprintf("%04d", v.micros/100)
	case "FF5":
		return fmt.Sprintf("%05d", v.micros/10)
	case "FF6", "US":
		return fmt.Sprintf("%06d", v.micros)
	case "SSSS":
		return strconv.Itoa(v.hour*3600 + v.minute*60 + v.second)
	case "tz":
		return strings.ToLower(v.zoneName)
	case "TZ":
		return v.zoneName
	case "TZH":
		return fmt.Sprintf("%s%02d", sign, offset/3600)
	case "TZM":
		return fmt.Sprintf("%02d", offset%3600/60)
	case "OF":
		out := sign + zero(2, offset/3600)
		if offset%3600 != 0 {
			out += fmt.Sprintf(":%02d", offset%3600/60)
		}
		return out
	case "A_D", "B_C":
		return pick(v.year <= 0, "B.C.", "A.D.")
	case "AD", "BC":
		return pick(v.year <= 0, "BC", "AD")
	case "a_d", "b_c":
		return pick(v.year <= 0, "b.c.", "a.d.")
	case "ad", "bc":
		return pick(v.year <= 0, "bc", "ad")
	case "MONTH":
		return dchName(dchMonths[v.month-1], s, true, false, true)
	case "Month":
		return dchName(dchMonths[v.month-1], s, false, false, true)
	case "month":
		return dchName(dchMonths[v.month-1], s, false, true, true)
	case "MON":
		return dchName(dchMonths[v.month-1][:3], s, true, false, false)
	case "Mon":
		return dchName(dchMonths[v.month-1][:3], s, false, false, false)
	case "mon":
		return dchName(dchMonths[v.month-1][:3], s, false, true, false)
	case "MM":
		return signed(2, v.month)
	case "DAY":
		return dchName(dchDays[v.weekday], s, true, false, true)
	case "Day":
		return dchName(dchDays[v.weekday], s, false, false, true)
	case "day":
		return dchName(dchDays[v.weekday], s, false, true, true)
	case "DY":
		return dchName(dchDays[v.weekday][:3], s, true, false, false)
	case "Dy":
		return dchName(dchDays[v.weekday][:3], s, false, false, false)
	case "dy":
		return dchName(dchDays[v.weekday][:3], s, false, true, false)
	case "DDD":
		return zero(3, v.yearDay)
	case "IDDD":
		return zero(3, v.isoYearDay)
	case "DD":
		return zero(2, v.day)
	case "D":
		return strconv.Itoa(v.weekday + 1)
	case "ID":
		if v.weekday == 0 {
			return "7"
		}
		return strconv.Itoa(v.weekday)
	case "WW":
		return zero(2, (v.yearDay-1)/7+1)
	case "IW":
		return zero(2, v.isoWeek)
	case "Q":
		if v.month == 0 {
			return ""
		}
		return strconv.Itoa((v.month-1)/3 + 1)
	case "CC":
		var century int
		if v.interval {
			century = v.year / 100
		} else if v.year > 0 {
			century = (v.year-1)/100 + 1
		} else {
			century = v.year/100 - 1
		}
		if century <= 99 && century >= -99 {
			width := 2
			if century < 0 {
				width = 3
			}
			return zero(width, century)
		}
		return strconv.Itoa(century)
	case "Y_YYY":
		return fmt.Sprintf("%d,%03d", year/1000, year-year/1000*1000)
	case "YYYY":
		return yearField(4, year)
	case "IYYY":
		return yearField(4, isoYear)
	case "YYY":
		return yearField(3, year%1000)
	case "IYY":
		return yearField(3, isoYear%1000)
	case "YY":
		return yearField(2, year%100)
	case "IY":
		return yearField(2, isoYear%100)
	case "Y":
		return strconv.Itoa(year % 10)
	case "I":
		return strconv.Itoa(isoYear % 10)
	case "RM", "rm":
		if v.month == 0 && v.year == 0 {
			return ""
		}
		// A whole number of interval years, or a negative month, still names one.
		index := v.month - 1
		switch {
		case v.month == 0 && v.year >= 0:
			index = 11
		case v.month == 0:
			index = 0
		case v.month < 0:
			index = 12 + v.month
		}
		roman := dchRomanUpper[index]
		if id == "rm" {
			roman = strings.ToLower(roman)
		}
		if s.fm {
			return roman
		}
		return fmt.Sprintf("%-4s", roman)
	case "W":
		return strconv.Itoa((v.day-1)/7 + 1)
	case "J":
		return strconv.Itoa(v.julian)
	}
	return "" // FX
}

// dchNumericField reports the patterns DCH_to_char follows with str_numth.
func dchNumericField(id string) bool {
	switch id {
	case "HH", "HH12", "HH24", "MI", "SS", "FF1", "FF2", "FF3", "FF4", "FF5", "FF6", "MS", "US",
		"SSSS", "MM", "DDD", "IDDD", "DD", "D", "ID", "WW", "IW", "Q", "CC", "Y_YYY", "YYYY", "IYYY",
		"YYY", "IYY", "YY", "IY", "Y", "I", "W", "J":
		return true
	}
	return false
}

// formatDCH is to_char's formatter: it walks the template as parse_format does.
func formatDCH(format string, v dchValue) (string, error) {
	var out strings.Builder
	for i := 0; i < len(format); {
		var s dchSuffix
		rest := format[i:]
		switch {
		case strings.HasPrefix(rest, "FM"), strings.HasPrefix(rest, "fm"):
			s.fm = true
			i += 2
		case strings.HasPrefix(rest, "TM"), strings.HasPrefix(rest, "tm"):
			s.tm = true
			i += 2
		}
		if i >= len(format) {
			break
		}
		rest = format[i:]
		id := ""
		for _, k := range dchKeywords {
			if strings.HasPrefix(rest, k.text) {
				id = k.id
				i += len(k.text)
				break
			}
		}
		if id != "" {
			rest = format[i:]
			switch {
			case strings.HasPrefix(rest, "TH"):
				s.thUpper = true
				i += 2
			case strings.HasPrefix(rest, "th"):
				s.thLower = true
				i += 2
			case strings.HasPrefix(rest, "SP"):
				i += 2
			}
			if v.interval && dchInvalidForInterval(id) {
				return "", &ExecError{Code: "22007",
					Message: "invalid format specification for an interval value",
					Hint:    "Intervals are not tied to specific calendar dates."}
			}
			field := dchField(id, s, v)
			if (s.thUpper || s.thLower) && dchNumericField(id) && field != "" {
				field += dchNumberSuffix(field, s.thUpper)
			}
			out.WriteString(field)
			continue
		}
		if rest[0] == '"' {
			// A quoted literal; a backslash quotes the next character.
			i++
			for i < len(format) {
				c := format[i]
				if c == '"' {
					i++
					break
				}
				if c == '\\' && i+1 < len(format) {
					i++
					c = format[i]
				}
				out.WriteByte(c)
				i++
			}
			continue
		}
		// Outside quotes a backslash is special only before a double quote.
		if rest[0] == '\\' && len(rest) > 1 && rest[1] == '"' {
			i++
		}
		out.WriteByte(format[i])
		i++
	}
	return out.String(), nil
}

// toCharInterval is interval_to_char. The result is NULL for an infinite value or
// an empty template.
func toCharInterval(d Datum, format string) (Datum, error) {
	if format == "" || d.IsIntervalNoEnd() || d.IsIntervalNoBegin() {
		return NullDatum, nil
	}
	text, err := formatDCH(format, newIntervalDCHValue(d))
	if err != nil {
		return Datum{}, err
	}
	return NewStringDatum(text), nil
}

// toCharDatetime is timestamp_to_char / timestamptz_to_char. The result is NULL
// for an infinite value or an empty template. A timestamptz is broken down in the
// session time zone. A date resolves to to_char(timestamptz, text) in PG, so it
// is midnight in that zone. A timestamp has no zone: TZ prints nothing and the
// offset is zero.
func toCharDatetime(d Datum, format string, ctx *Context) (Datum, error) {
	if format == "" || d.IsTimestampNotFinite() {
		return NullDatum, nil
	}
	if d.IsDate() && isFarDateCarrier(d.Int) {
		// to_char(date) is to_char(timestamptz) after date2timestamptz.
		return Datum{}, dateOutOfRangeForTimestamp()
	}
	t := d.TimeValue()
	zoneName, zoneOffset := "", 0
	switch {
	case d.IsTimestampTZ():
		t = t.In(misc.SessionLocation(timeZoneFromCtx(ctx)))
		zoneName, zoneOffset = t.Zone()
	case d.IsDate():
		y, mo, day := t.Date()
		t = time.Date(y, mo, day, 0, 0, 0, 0, misc.SessionLocation(timeZoneFromCtx(ctx)))
		zoneName, zoneOffset = t.Zone()
	}
	text, err := formatDCH(format, newDCHValue(t, zoneName, zoneOffset))
	if err != nil {
		return Datum{}, err
	}
	return NewStringDatum(text), nil
}
