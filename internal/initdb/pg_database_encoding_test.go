package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// readPgDatabaseEncoding decodes the `encoding` int4 column (attnum 4) from the
// first pg_database heap tuple written to <dir>/global/1262. The fixed-width
// Form_pg_database prefix is oid(4) + datname(NAMEDATALEN=64) + datdba(4) +
// encoding(4), so encoding sits at tuple-data-start + 72. tuple-data-start is
// the line pointer's offset plus t_hoff (page[off+22]).
func readPgDatabaseEncoding(t *testing.T, dir string) int32 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "global", "1262"))
	if err != nil {
		t.Fatalf("read global/1262: %v", err)
	}
	page := raw[:storage.BlockSize]
	lp := binary.LittleEndian.Uint32(page[24:28]) // first line pointer
	off := int(lp & 0x7FFF)
	hoff := int(page[off+22])
	const encodingOffset = 4 + 64 + 4 // oid + datname(NAMEDATALEN) + datdba
	start := off + hoff + encodingOffset
	return int32(binary.LittleEndian.Uint32(page[start : start+4]))
}

// TestBootstrapPostgresDatabaseWritesEncoding proves that the resolved encoding
// ID threads all the way into the pg_database.encoding column, for both the
// UTF8 default and a non-default server encoding. This is the wiring half of
// the -E/--encoding option (the name→ID logic is covered in encoding_test.go).
func TestBootstrapPostgresDatabaseWritesEncoding(t *testing.T) {
	cases := []struct {
		name string
		enc  int32
	}{
		{"default-utf8", pgEncUTF8},
		{"latin1", pgEncLATIN1},
		{"sql-ascii", pgEncSQLASCII},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, sub := range []string{"global", "base/1", "base/4", "base/5"} {
				if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
					t.Fatalf("mkdir %s: %v", sub, err)
				}
			}
			if err := bootstrapPostgresDatabase(dir, c.enc, localeSettings{provider: collProviderLibc, collate: "C", ctype: "C"}); err != nil {
				t.Fatalf("bootstrapPostgresDatabase: %v", err)
			}
			if got := readPgDatabaseEncoding(t, dir); got != c.enc {
				t.Errorf("pg_database.encoding = %d, want %d", got, c.enc)
			}
		})
	}
}
