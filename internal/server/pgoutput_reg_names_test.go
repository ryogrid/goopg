package server

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/wal"
)

// TestPgoutputSnapshotRegOutRendererWired pins acceptance criterion #3: the
// publisher walsender binds a REAL executor.RegOutRenderer into the catalog
// snapshot, so TEXT-mode pgoutput emits a reg* column's NAME (regclassout via
// OidOutputFunctionCall, postgres/src/backend/replication/logical/proto.c:848)
// rather than the numeric OID that belongs to BINARY mode's typsend. regclass
// 1259 is pg_class itself, resolved against the in-memory catalog's system
// tables; a dangling OID keeps the numeric fallback. M0119-0006 (deferral row
// 1353).
func TestPgoutputSnapshotRegOutRendererWired(t *testing.T) {
	im := catalog.NewInMemory()
	snap := wal.BuildCatalogSnapshot(im, executor.RegOutRenderer(im, false))
	if snap.RegOut == nil {
		t.Fatal("snap.RegOut is nil — the walsender wiring did not bind the renderer")
	}
	if got := snap.RegOut("regclass", 1259); got != "pg_class" {
		t.Errorf("regclass 1259 rendered %q, want %q", got, "pg_class")
	}
	// A relation in pg_catalog never schema-qualifies (the search path always
	// searches it implicitly), so qualify=false yields the bare name.
	if got := snap.RegOut("regclass", 125999999); got != "125999999" {
		t.Errorf("dangling regclass OID rendered %q, want numeric fallback %q", got, "125999999")
	}
}
