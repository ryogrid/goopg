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
