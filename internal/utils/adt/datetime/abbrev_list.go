package datetime

import (
	"sort"
	"strings"
	"time"
)

// AbbrevRow is one row of pg_timezone_abbrevs: an abbreviation, its offset
// east of UTC in seconds and whether it denotes daylight time.
type AbbrevRow struct {
	Abbrev string
	Offset int
	IsDST  bool
}

// DefaultAbbrevRows is pg_timezone_abbrevs_abbrevs over the Default
// abbreviation set: a fixed abbreviation as written, and a dynamic one with the
// meaning it has in its zone at now (DetermineTimeZoneAbbrevOffsetTS).
func DefaultAbbrevRows(now time.Time, lookup func(name string) (*time.Location, bool)) []AbbrevRow {
	return AbbrevRows("", now, lookup)
}

// AbbrevRows is DefaultAbbrevRows over the named timezone_abbreviations set
// ("" = Default; an unloadable name falls back to Default). M0149-0001h1a82.
func AbbrevRows(setName string, now time.Time, lookup func(name string) (*time.Location, bool)) []AbbrevRow {
	set, err := abbrevSet(setName)
	if err != nil {
		set = defaultZoneAbbrevs
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([]AbbrevRow, 0, len(keys))
	for _, k := range keys {
		ab := set[k]
		up := strings.ToUpper(k)
		switch ab.kind {
		case tokTZ:
			rows = append(rows, AbbrevRow{up, ab.offset, false})
		case tokDTZ:
			rows = append(rows, AbbrevRow{up, ab.offset, true})
		case tokDynTZ:
			loc, ok := lookup(ab.zone)
			if !ok {
				continue
			}
			off, found := abbrevOffsetAt(up, loc, now.Unix())
			dst := false
			if found {
				for _, p := range zonePeriods(loc) {
					if p.abbrev == up && p.offset == off {
						dst = p.isDST
					}
				}
			} else {
				// An abbreviation the zone's data no longer uses means the
				// zone's own offset (DetermineTimeZoneAbbrevOffsetInternal).
				t := now.In(loc)
				_, off = t.Zone()
				dst = t.IsDST()
			}
			rows = append(rows, AbbrevRow{up, off, dst})
		}
	}
	return rows
}

// ZoneAbbrevRows is pg_timezone_abbrevs_zone: the all-letter abbreviations a
// zone has used, each with its meaning at now (pg_interpret_timezone_abbrev),
// in the order the zone first used them.
func ZoneAbbrevRows(loc *time.Location, now time.Time) []AbbrevRow {
	seen := map[string]bool{}
	var rows []AbbrevRow
	for _, p := range zonePeriods(loc) {
		up := p.abbrev
		if seen[up] || up == "" || strings.Trim(up, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			continue
		}
		seen[up] = true
		off, found := abbrevOffsetAt(up, loc, now.Unix())
		if !found {
			continue
		}
		dst := false
		for _, q := range zonePeriods(loc) {
			if q.abbrev == up && q.offset == off {
				dst = q.isDST
			}
		}
		rows = append(rows, AbbrevRow{up, off, dst})
	}
	return rows
}
