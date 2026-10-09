package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestTempRelationsDoNotSurviveRestart pins M0146-0039: a TEMP table (with
// its serial sequence and index) and an explicit TEMP SEQUENCE belong to the
// backend that created them. PG drops them when that backend exits and never
// shows a leftover to another session; goopg wrote their pg_class rows with
// relpersistence 'p' in public, so a restart brought them back as permanent
// tables with the old rows. The rows now say 't', and startup skips them.
// The permanent table next to them must survive.
func TestTempRelationsDoNotSurviveRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE keep (k int PRIMARY KEY)")
	runDDL(t, rt1, "CREATE TEMP TABLE tt (id serial PRIMARY KEY, w text)")
	runDDL(t, rt1, "CREATE INDEX tt_w ON tt (w)")
	runDDL(t, rt1, "CREATE TEMP SEQUENCE ts")
	cat1 := rt1.Catalog.(*catalog.InMemory)
	for _, name := range []string{"keep", "tt", "tt_id_seq", "ts"} {
		if _, ok := cat1.LookupTable(parser.ObjectName{Name: name}); !ok {
			rt1.Close()
			t.Fatalf("before restart: %s not registered", name)
		}
	}
	if err := rt1.SaveCatalog(); err != nil {
		rt1.Close()
		t.Fatal(err)
	}
	rt1.Close()

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()
	cat2 := rt2.Catalog.(*catalog.InMemory)
	if _, ok := cat2.LookupTable(parser.ObjectName{Name: "keep"}); !ok {
		t.Error("permanent table keep lost across the restart")
	}
	for _, name := range []string{"tt", "tt_id_seq", "ts"} {
		if tbl, ok := cat2.LookupTable(parser.ObjectName{Name: name}); ok {
			t.Errorf("temp relation %s came back after a restart (Temp=%v)", name, tbl.Temp)
		}
	}
	for _, idx := range cat2.AllIndexes() {
		if idx.Name == "tt_w" || idx.Name == "tt_pkey" {
			t.Errorf("index %s on a temp table came back after a restart", idx.Name)
		}
	}
}
