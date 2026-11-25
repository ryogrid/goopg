package misc

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// compiledLocaleFile builds a glibc locale category holding strs in order.
func compiledLocaleFile(category int, strs ...string) []byte {
	le := binary.LittleEndian
	head := 8 + 4*len(strs)
	out := make([]byte, head)
	le.PutUint32(out, localeFileMagic(category))
	le.PutUint32(out[4:], uint32(len(strs)))
	for i, s := range strs {
		le.PutUint32(out[8+i*4:], uint32(len(out)))
		out = append(append(out, s...), 0)
	}
	return out
}

func monetaryFile() []byte {
	return compiledLocaleFile(lcMonetary, "EUR ", "€", ",", ".", "", "", "-")
}

func useLocaleDir(t *testing.T, dir string) {
	t.Helper()
	old := localeDir
	localeDir = dir
	t.Setenv("LOCPATH", "")
	localeStringCache.Range(func(k, _ any) bool { localeStringCache.Delete(k); return true })
	t.Cleanup(func() {
		localeDir = old
		localeStringCache.Range(func(k, _ any) bool { localeStringCache.Delete(k); return true })
	})
}

func TestLocaleConvFromDirectory(t *testing.T) {
	dir := t.TempDir()
	useLocaleDir(t, dir)
	loc := filepath.Join(dir, "de_DE.utf8")
	if err := os.Mkdir(loc, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, data := range map[string][]byte{
		"LC_NUMERIC":  compiledLocaleFile(lcNumeric, ",", "."),
		"LC_MONETARY": monetaryFile(),
	} {
		if err := os.WriteFile(filepath.Join(loc, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := LocaleConvFor("de_DE.UTF-8", "de_DE.UTF-8")
	want := LocaleConv{DecimalPoint: ",", ThousandsSep: ".", NegativeSign: "-", CurrencySymbol: "€"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := LocaleConvFor("C", "POSIX"); got != (LocaleConv{}) {
		t.Errorf("C locale: %+v", got)
	}
}

func TestLocaleConvFromArchive(t *testing.T) {
	dir := t.TempDir()
	useLocaleDir(t, dir)
	le := binary.LittleEndian
	name := append([]byte("fr_FR.utf8"), 0)
	mon := monetaryFile()
	const hashOff, nameOff, recOff, dataOff = 32, 44, 60, 200
	archive := make([]byte, dataOff+len(mon))
	le.PutUint32(archive[8:], hashOff)
	le.PutUint32(archive[16:], 1)
	le.PutUint32(archive[hashOff+4:], nameOff)
	le.PutUint32(archive[hashOff+8:], recOff)
	copy(archive[nameOff:], name)
	le.PutUint32(archive[recOff+4+lcMonetary*8:], dataOff)
	le.PutUint32(archive[recOff+8+lcMonetary*8:], uint32(len(mon)))
	copy(archive[dataOff:], mon)
	if err := os.WriteFile(filepath.Join(dir, "locale-archive"), archive, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LocaleConvFor("C", "fr_FR.UTF-8"); got.CurrencySymbol != "€" || got.NegativeSign != "-" {
		t.Errorf("got %+v", got)
	}
}
