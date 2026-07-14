package config

import (
	"fmt"
	"strings"
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
	style, order := parseDateStyleValue(current)
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
			defStyle, defOrder := parseDateStyleValue(bootVal)
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

// parseDateStyleValue extracts the (style, order) pair from an already
// well-formed "<Style>, <Order>" DateStyle string (the only shape
// mergeDateStyle ever writes). Falls back to ISO/MDY for a component the
// string doesn't mention, so a malformed or partial `current` never panics.
func parseDateStyleValue(s string) (style, order string) {
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
