package misc

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// zoneInfoDirs are the tzdata roots Go's time.LoadLocation reads on Unix
// (zoneinfo_unix.go platformZoneSources), $ZONEINFO first.
func zoneInfoDirs() []string {
	dirs := []string{"/usr/share/zoneinfo/", "/usr/share/lib/zoneinfo/", "/usr/lib/locale/TZ/", "/etc/zoneinfo/"}
	if z := os.Getenv("ZONEINFO"); z != "" {
		dirs = append([]string{z}, dirs...)
	}
	return dirs
}

type zoneFileHit struct {
	name string
	ok   bool
}

var canonicalZoneCache sync.Map // lower-cased name -> zoneFileHit

// lookupZoneFile is pg_tzset's file lookup (pgtz.c pg_open_tzfile + tzload):
// the name is matched against the tz database case-insensitively, one path
// component at a time, and must load as a zone; the result is the file's own
// spelling — `SET timezone = utc` (an identifier, so downcased) shows UTC.
// ok is false when no loadable file matches (POSIX specs such as '<+02>-02',
// unknown names, tzdata's non-zone files like zone.tab).
func lookupZoneFile(name string) (string, bool) {
	if name == "" || strings.ContainsAny(name, "\x00") || strings.Contains(name, "..") ||
		strings.HasPrefix(name, "/") {
		return "", false
	}
	key := strings.ToLower(name)
	if c, ok := canonicalZoneCache.Load(key); ok {
		h := c.(zoneFileHit)
		return h.name, h.ok
	}
	hit := zoneFileHit{}
	for _, root := range zoneInfoDirs() {
		if found, ok := matchZonePath(root, strings.Split(name, "/")); ok {
			if _, err := time.LoadLocation(found); err == nil {
				hit = zoneFileHit{name: found, ok: true}
			}
			break
		}
	}
	canonicalZoneCache.Store(key, hit)
	return hit.name, hit.ok
}

// matchZonePath resolves parts under root, each component compared without
// regard to case, and returns the matched relative path as spelled on disk.
func matchZonePath(root string, parts []string) (string, bool) {
	dir := root
	matched := make([]string, 0, len(parts))
	for i, part := range parts {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", false
		}
		hit := ""
		for _, e := range entries {
			if strings.EqualFold(e.Name(), part) && e.IsDir() == (i < len(parts)-1) {
				hit = e.Name()
				break
			}
		}
		if hit == "" {
			return "", false
		}
		matched = append(matched, hit)
		dir = filepath.Join(dir, hit)
	}
	return strings.Join(matched, "/"), true
}

// TimezoneNameRow is one row of pg_timezone_names.
type TimezoneNameRow struct {
	Name, Abbrev string
	Offset       int // seconds east of UTC
	IsDST        bool
}

// TimezoneNames is pg_timezone_names: every zone of the tz database
// (pg_tzenumerate_next) with its abbreviation, offset and DST flag at now.
// Like pg_tzenumerate_next it skips hidden entries, files that are not zones
// and leap-second zones (pg_tz_acceptable); like pg_timezone_names it skips
// an abbreviation longer than 31 bytes.
func TimezoneNames(now time.Time) []TimezoneNameRow {
	root := ""
	for _, d := range zoneInfoDirs() {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			root = d
			break
		}
	}
	if root == "" {
		return nil
	}
	var rows []TimezoneNameRow
	var walk func(dir, prefix string)
	walk = func(dir, prefix string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			// posixrules and localtime are host links, not zones of the
			// database PG enumerates.
			if strings.HasPrefix(name, ".") || (prefix == "" && (name == "posixrules" || name == "localtime")) {
				continue
			}
			full := filepath.Join(dir, name)
			rel := prefix + name
			if e.IsDir() {
				walk(full, rel+"/")
				continue
			}
			if !isAcceptableZoneFile(full) {
				continue
			}
			loc, err := time.LoadLocation(rel)
			if err != nil {
				continue
			}
			t := now.In(loc)
			abbrev, off := t.Zone()
			if len(abbrev) > 31 {
				continue
			}
			rows = append(rows, TimezoneNameRow{Name: rel, Abbrev: abbrev, Offset: off, IsDST: t.IsDST()})
		}
	}
	walk(root, "")
	return rows
}

// isAcceptableZoneFile reports whether a file is a TZif zone without
// leap-second records (tzload succeeds and pg_tz_acceptable holds).
func isAcceptableZoneFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [44]byte
	if n, _ := f.Read(hdr[:]); n < len(hdr) || string(hdr[:4]) != "TZif" {
		return false
	}
	leapcnt := uint32(hdr[28])<<24 | uint32(hdr[29])<<16 | uint32(hdr[30])<<8 | uint32(hdr[31])
	return leapcnt == 0
}

