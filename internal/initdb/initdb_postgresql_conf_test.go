package initdb

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/config"
)

// TestInitWritesEmbeddedSampleAsPostgresqlConf pins M0108-0002: a
// fresh `goopg init` must write `<datadir>/postgresql.conf` as the
// byte-identical contents of `config.SampleConfig()`. The template
// is the single source of truth for the freshly-initted config; if
// callers want a tuned config they can copy it in afterwards. The
// equality check would catch any future drift between the embedded
// template and the bytes initdb writes (e.g. an accidental
// re-introduction of an ad-hoc default string in this package).
func TestInitWritesEmbeddedSampleAsPostgresqlConf(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "postgresql.conf"))
	if err != nil {
		t.Fatalf("read postgresql.conf: %v", err)
	}
	want := config.SampleConfig()
	if !bytes.Equal(got, want) {
		t.Fatalf("postgresql.conf bytes differ from config.SampleConfig(): got %d bytes, want %d bytes",
			len(got), len(want))
	}
}
