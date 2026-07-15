package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCatalogSurvivesCheckpointCrossingNativeOnly is the catalog variant of
// the perf-optimize3-dash/03 window regression (doc 03 §5): catalog pages
// dirtied in one redo epoch, a checkpoint publishing a new redo pointer, and
// further catalog inserts AFTER publication — the post-publication inserts
// must carry fresh images so replay from the new redo reconstructs them.
// Runs native-only (GOOPG_WAL_CANONICAL=off): the canonical family's
// unconditional images are absent, so the native pd_lsn<=redo machinery is
// the only torn-page cover — exactly the load-bearing configuration
// (README R1/R2 of the dash bundle).
//
// NOTE (review SHOULD-3): this is an end-to-end SMOKE across a checkpoint
// crossing, not the old-vs-new discriminator — rt1.Close() flushes cleanly,
// so no torn base exists. The behavioral discriminator is
// storage.TestFPIRedoPublicationClosesWindow (image-count assertions that
// FAIL under the old fpiSinceCheckpoint design).
func TestCatalogSurvivesCheckpointCrossingNativeOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}

	// Epoch 1: dirty the catalog heap pages (pg_attribute etc.).
	runDDLRealDataDir(t, rt1, "CREATE TABLE fpi_epoch1 (a int4, b text)")
	runDDLRealDataDir(t, rt1, "INSERT INTO fpi_epoch1 VALUES (1, 'x'), (2, 'y')")

	// Checkpoint: publishes a new redo pointer at checkpoint START
	// (redoPublisher). Under the OLD post-record epoch-reset design this is
	// where the image-less window opened for pages already imaged in epoch 1.
	if err := rt1.Checkpointer.CheckpointNow(); err != nil {
		t.Fatalf("CheckpointNow: %v", err)
	}

	// Epoch 2 (post-publication): more catalog-heap inserts, very likely
	// landing on the same pg_attribute/pg_class heap pages epoch 1 touched.
	// These must re-image before their incremental records.
	runDDLRealDataDir(t, rt1, "CREATE TABLE fpi_epoch2 (c int4, d text)")
	runDDLRealDataDir(t, rt1, "INSERT INTO fpi_epoch2 VALUES (3, 'z'), (4, 'w')")
	rt1.Close() // no SaveCatalog — catalog recovery rides WAL replay

	// Reopen native-only: replay from the NEW redo must reconstruct both
	// epochs' catalog rows and table data.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer rt2.Close()
	for _, name := range []string{"fpi_epoch1", "fpi_epoch2"} {
		if _, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: name}); !ok {
			t.Fatalf("%s missing after checkpoint-crossing native-only restart", name)
		}
	}
	if rows := runSelectRealDataDir(t, rt2, "SELECT a FROM fpi_epoch1"); len(rows) != 2 {
		t.Fatalf("fpi_epoch1: want 2 rows, got %d", len(rows))
	}
	if rows := runSelectRealDataDir(t, rt2, "SELECT c FROM fpi_epoch2"); len(rows) != 2 {
		t.Fatalf("fpi_epoch2: want 2 rows, got %d", len(rows))
	}
}
