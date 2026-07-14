package config

import (
	"fmt"
	"strings"
	"time"
)

// mergeDateStyle implements PostgreSQL's check_datestyle GUC check hook
// (postgres/src/backend/commands/variable.c): DateStyle is a two-component
// value (a display STYLE — ISO/SQL/Postgres/German — and a field ORDER —
// YMD/DMY/MDY) packed into one comma-separated string, but a SET only
// specifies the token(s) it wants to change. `SET datestyle = 'SQL'` must
// keep the session's current order component rather than resetting it, so
// the merge starts from `current` (the effective value before this SET) and
// only overwrites the component(s) actually named in newValue. GERMAN also
// implies DMY order unless the same SET explicitly names an order. DEFAULT
// resolves recursively against bootVal. Conflicting tokens for the same
// component (e.g. "ISO, SQL") or an unrecognized keyword are rejected,
// matching upstream's ok=false path. Returns the canonical "<Style>, <Order>"
// form guc_tables.c's assign_datestyle stores.
func mergeDateStyle(current, bootVal, newValue string) (string, error) {
	style, order := ParseDateStyleValue(current)
	haveStyle, haveOrder := false, false

	for raw := range strings.SplitSeq(newValue, ",") {
		tok := strings.TrimSpace(raw)
		if tok == "" {
			return "", fmt.Errorf("invalid value for parameter \"datestyle\": %q", newValue)
		}
		switch {
		case strings.EqualFold(tok, "ISO"):
			if haveStyle && style != "ISO" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			style, haveStyle = "ISO", true
		case strings.EqualFold(tok, "SQL"):
			if haveStyle && style != "SQL" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			style, haveStyle = "SQL", true
		case len(tok) >= 8 && strings.EqualFold(tok[:8], "POSTGRES"):
			if haveStyle && style != "Postgres" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			style, haveStyle = "Postgres", true
		case strings.EqualFold(tok, "GERMAN"):
			if haveStyle && style != "German" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			style, haveStyle = "German", true
			if !haveOrder {
				order = "DMY"
			}
		case strings.EqualFold(tok, "YMD"):
			if haveOrder && order != "YMD" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			order, haveOrder = "YMD", true
		case strings.EqualFold(tok, "DMY") || (len(tok) >= 4 && strings.EqualFold(tok[:4], "EURO")):
			if haveOrder && order != "DMY" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			order, haveOrder = "DMY", true
		case strings.EqualFold(tok, "MDY") || strings.EqualFold(tok, "US") || (len(tok) >= 7 && strings.EqualFold(tok[:7], "NONEURO")):
			if haveOrder && order != "MDY" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			order, haveOrder = "MDY", true
		case strings.EqualFold(tok, "DEFAULT"):
			defStyle, defOrder := ParseDateStyleValue(bootVal)
			if !haveStyle {
				style = defStyle
			}
			if !haveOrder {
				order = defOrder
			}
		default:
			return "", fmt.Errorf("unrecognized key word: %q", tok)
		}
	}
	return style + ", " + order, nil
}

// ParseDateStyleValue extracts the (style, order) pair from an already
// well-formed "<Style>, <Order>" DateStyle string (the only shape
// mergeDateStyle ever writes). Falls back to ISO/MDY for a component the
// string doesn't mention, so a malformed or partial `current` never panics.
// Exported for output formatters (executor/server/COPY) that need to render
// a value according to the session's effective DateStyle.
func ParseDateStyleValue(s string) (style, order string) {
	style, order = "ISO", "MDY"
	for raw := range strings.SplitSeq(s, ",") {
		tok := strings.TrimSpace(raw)
		switch {
		case strings.EqualFold(tok, "ISO"):
			style = "ISO"
		case strings.EqualFold(tok, "SQL"):
			style = "SQL"
		case len(tok) >= 8 && strings.EqualFold(tok[:8], "POSTGRES"):
			style = "Postgres"
		case strings.EqualFold(tok, "GERMAN"):
			style = "German"
		case strings.EqualFold(tok, "YMD"):
			order = "YMD"
		case strings.EqualFold(tok, "DMY") || (len(tok) >= 4 && strings.EqualFold(tok[:4], "EURO")):
			order = "DMY"
		case strings.EqualFold(tok, "MDY") || strings.EqualFold(tok, "US") || (len(tok) >= 7 && strings.EqualFold(tok[:7], "NONEURO")):
			order = "MDY"
		}
	}
	return style, order
}

// FormatDate renders a DATE value's calendar part as text according to the
// given DateStyle (style, order) pair, matching PostgreSQL's EncodeDateOnly
// (postgres/src/backend/utils/adt/datetime.c). ISO output is order-
// independent; German is always DD.MM.YYYY regardless of order (mergeDateStyle
// already forces order=DMY when style=German is set, but FormatDate doesn't
// depend on that invariant). Unrecognized style falls back to ISO, matching
// ParseDateStyleValue's own default.
func FormatDate(t time.Time, style, order string) string {
	switch style {
	case "SQL":
		if order == "DMY" {
			return t.Format("02/01/2006")
		}
		return t.Format("01/02/2006")
	case "Postgres":
		if order == "DMY" {
			return t.Format("02-01-2006")
		}
		return t.Format("01-02-2006")
	case "German":
		return t.Format("02.01.2006")
	default:
		return t.Format("2006-01-02")
	}
}


// fracSecondsSuffix renders t's sub-second component as a leading-dot suffix
// (e.g. ".5", ".000123"), trimmed of trailing zeros, or "" when there is no
// fractional part — matching PostgreSQL's AppendSeconds
// (postgres/src/backend/utils/adt/datetime.c), which omits the decimal point
// entirely for a zero fsec and otherwise prints only the significant digits.
// goopg stores microsecond resolution, matching AppendTimestampSeconds's
// MAX_TIMESTAMP_PRECISION=6 ceiling.
func fracSecondsSuffix(t time.Time) string {
	ns := t.Nanosecond()
	if ns == 0 {
		return ""
	}
	micro := ns / 1000
	var frac [6]byte
	for i := 5; i >= 0; i-- {
		frac[i] = byte('0' + micro%10)
		micro /= 10
	}
	end := len(frac)
	for end > 0 && frac[end-1] == '0' {
		end--
	}
	if end == 0 {
		return ""
	}
	return "." + string(frac[:end])
}

// FormatTimestamp renders a TIMESTAMP/TIMESTAMPTZ value's text according to
// the given DateStyle (style, order) pair, matching PostgreSQL's
// EncodeDateTime (postgres/src/backend/utils/adt/datetime.c) with
// print_tz=false — goopg has no session-timezone-aware conversion or offset
// rendering for TIMESTAMPTZ yet (a separate, larger deferred gap; see the
// deferral ledger), so both types render their stored instant identically to
// plain TIMESTAMP, same as before this DateStyle fix. Fractional seconds are
// trimmed of trailing zeros (and omitted entirely when zero) via
// fracSecondsSuffix, matching PostgreSQL's AppendSeconds rather than always
// padding to 6 digits.
func FormatTimestamp(t time.Time, style, order string) string {
	frac := fracSecondsSuffix(t)
	switch style {
	case "SQL":
		if order == "DMY" {
			return t.Format("02/01/2006 15:04:05") + frac
		}
		return t.Format("01/02/2006 15:04:05") + frac
	case "Postgres":
		if order == "DMY" {
			return t.Format("Mon 02 Jan 15:04:05") + frac + t.Format(" 2006")
		}
		return t.Format("Mon Jan 02 15:04:05") + frac + t.Format(" 2006")
	case "German":
		return t.Format("02.01.2006 15:04:05") + frac
	default:
		return t.Format("2006-01-02 15:04:05") + frac
	}
}
