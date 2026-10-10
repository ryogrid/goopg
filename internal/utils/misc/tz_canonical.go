package misc

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

var canonicalZoneCache sync.Map // lower-cased name -> canonical spelling

// canonicalZoneName is pg_tzset's name resolution (pgtz.c pg_open_tzfile):
// a zone name is matched against the tz database case-insensitively, one
// path component at a time, and the stored name takes the file's own
// spelling — `SET timezone = utc` (an identifier, so downcased) shows UTC.
// A name with no match (POSIX specs such as '<+02>-02' or '+05:30') is
// returned unchanged.
func canonicalZoneName(name string) string {
	if name == "" || strings.ContainsAny(name, "\x00") || strings.Contains(name, "..") ||
		strings.HasPrefix(name, "/") {
		return name
	}
	key := strings.ToLower(name)
	if c, ok := canonicalZoneCache.Load(key); ok {
		return c.(string)
	}
	canon := name
	for _, root := range zoneInfoDirs() {
		if found, ok := matchZonePath(root, strings.Split(name, "/")); ok {
			canon = found
			break
		}
	}
	canonicalZoneCache.Store(key, canon)
	return canon
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
