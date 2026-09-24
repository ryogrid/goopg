package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestGoopgFeaturesMarker: initdb writes the cluster capability marker
// (global/pg_goopg_features) naming null_keyed_index_entries and
// heap_lp_lifecycle (M0145-0008v), and a cluster without the file — one
// created before the capabilities existed — reads as having none, so open
// leaves both off.
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
	if got := readGoopgFeatures(dir); !got[catalog.NullKeyedIndexEntriesFeature] || !got[storage.HeapLinePointerLifecycleFeature] || len(got) != 2 {
		t.Fatalf("written marker: features %v, want exactly %s and %s", got,
			catalog.NullKeyedIndexEntriesFeature, storage.HeapLinePointerLifecycleFeature)
	}
}
