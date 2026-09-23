package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestGoopgFeaturesMarker: initdb writes the cluster capability marker
// (global/pg_goopg_features) naming null_keyed_index_entries, and a cluster
// without the file — one created before the capability existed — reads as
// having none, so open leaves catalog.NullKeyedIndexEntries off.
func TestGoopgFeaturesMarker(t *testing.T) {
	var spec *FileSpec
	for _, f := range SampleFiles() {
		if f.Path == goopgFeaturesFile {
			f := f
			spec = &f
		}
	}
	if spec == nil {
		t.Fatalf("SampleFiles has no %s", goopgFeaturesFile)
	}
	dir := t.TempDir()
	if got := readGoopgFeatures(dir); len(got) != 0 {
		t.Fatalf("absent marker: features %v, want none", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, spec.Path), spec.Build(), spec.Mode); err != nil {
		t.Fatal(err)
	}
	if got := readGoopgFeatures(dir); !got[catalog.NullKeyedIndexEntriesFeature] || len(got) != 1 {
		t.Fatalf("written marker: features %v, want exactly %s", got, catalog.NullKeyedIndexEntriesFeature)
	}
}
