package misc

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLocaleAvailable pins M0149-0001k5b: lc_* values are accepted iff
// setlocale could load them — the C / POSIX / C.UTF-8 built-ins, the empty
// string, a compiled locale directory (codeset spellings normalised as glibc
// does: "UTF-8" finds "utf8"), and LOCPATH replacing the default search. PG
// 18.3 on a C-only host rejects en_US.UTF-8 / de_DE.UTF-8 with 22023.
func TestLocaleAvailable(t *testing.T) {
	saved := localeDir
	defer func() { localeDir = saved }()
	dir := t.TempDir()
	localeDir = dir
	if err := os.Mkdir(filepath.Join(dir, "de_DE.utf8"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCPATH", "")
	for name, want := range map[string]bool{
		"": true, "C": true, "POSIX": true, "C.UTF-8": true, "C.utf8": true,
		"de_DE.UTF-8": true, "de_DE.utf8": true, "fr_FR.UTF-8": false, "en_US.UTF-8": false,
		"../etc": false,
	} {
		if got := LocaleAvailable(name); got != want {
			t.Errorf("LocaleAvailable(%q) = %v, want %v", name, got, want)
		}
	}
	lp := t.TempDir()
	if err := os.Mkdir(filepath.Join(lp, "fr_FR.utf8"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCPATH", lp)
	if !LocaleAvailable("fr_FR.UTF-8") || LocaleAvailable("de_DE.UTF-8") {
		t.Error("LOCPATH must replace the default locale directory")
	}
	if err := localeCheck("lc_monetary")("xx_YY.UTF-8"); err == nil ||
		err.Error() != `invalid value for parameter "lc_monetary": "xx_YY.UTF-8"` {
		t.Errorf("check message: %v", err)
	}
}
