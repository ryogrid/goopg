package initdb

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestColumnTypesSurviveRepeatedRestarts pins M0146-0151 (found by
// M0141-S2a-fix2r-c): the second restart used to register user tables from
// the M0114 JSON cache, which kept only a column's type NAME — so char(16)
// lost its length, numeric(7,2) its precision, and an int4[] column read back
// as a scalar int4 (wrong results). Every restart must rebuild the columns
// from the pg_attribute heap, and a cache file an older binary left must be
// ignored and removed.
func TestColumnTypesSurviveRepeatedRestarts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt, "CREATE TABLE typmods (c char(16), v varchar(20), n numeric(7,2), arr int4[])")
	mem, ok := rt.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is %T, want *catalog.InMemory", rt.Catalog)
	}
	dbOid := mem.DBOID()
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}

	// A stale cache in the retired format (bare type names), as an older
	// binary wrote it on its first restart.
	stale := `{"version":1,"tables":[{"oid":16384,"schema":"public","name":"typmods","columns":[` +
		`{"name":"c","type":"bpchar","ordinal":0},{"name":"v","type":"varchar","ordinal":1},` +
		`{"name":"n","type":"numeric","ordinal":2},{"name":"arr","type":"int4","ordinal":3}]}]}`
	cachePath := catalogCachePath(dir, dbOid)
	if err := os.WriteFile(cachePath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}

	for restart := 1; restart <= 2; restart++ {
		rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
		if err != nil {
			t.Fatal(err)
		}
		tbl, ok := rt.Catalog.LookupTable(parser.ObjectName{Name: "typmods"})
		if !ok {
			rt.Close()
			t.Fatalf("restart %d: typmods not found", restart)
		}
		want := []struct {
			name    string
			args    []int64
			isArray bool
		}{
			{"bpchar", []int64{16}, false},
			{"varchar", []int64{20}, false},
			{"numeric", []int64{7, 2}, false},
			{"int4", nil, true},
		}
		for i, w := range want {
			got := tbl.Columns[i].Type
			if got.Name != w.name || !reflect.DeepEqual(got.Args, w.args) || got.IsArray != w.isArray {
				t.Errorf("restart %d: column %s type = %+v, want name=%s args=%v array=%v",
					restart, tbl.Columns[i].Name, got, w.name, w.args, w.isArray)
			}
		}
		if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
			t.Errorf("restart %d: the retired catalog cache file is still present (stat err %v)", restart, err)
		}
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
