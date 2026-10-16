package executor

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// statsImportFixture is a table of mixed types with repeated values and NULLs,
// ANALYZEd so every statistics slot is populated.
func statsImportFixture(t *testing.T) (*Context, *catalog.Table, func()) {
	t.Helper()
	ctx, cleanup := newVMFixture(t)
	if err := runDDL(t, ctx, "CREATE TABLE simp (i int, n numeric(7,2), s text, c char(5), d date, v varchar(10), f float8)"); err != nil {
		cleanup()
		t.Fatalf("CREATE TABLE: %v", err)
	}
	for k := 0; k < 400; k++ {
		i := strconv.Itoa(k % 37)
		if k%11 == 0 {
			i = "NULL"
		}
		row := fmt.Sprintf("(%s, %d.%02d, 's%d', 'c%d', '2026-01-%02d', 'v%d', %d.5)",
			i, k%23, k%7, k%13, k%5, 1+k%28, k%17, k%19)
		if err := runDDL(t, ctx, "INSERT INTO simp VALUES "+row); err != nil {
			cleanup()
			t.Fatalf("INSERT: %v", err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)
	if err := runDDL(t, ctx, "ANALYZE simp"); err != nil {
		cleanup()
		t.Fatalf("ANALYZE: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "simp"})
	if !ok {
		cleanup()
		t.Fatal("simp not found")
	}
	return ctx, tbl, cleanup
}

// reloadForm is what a server restart reconstructs from ANALYZE's
// pg_statistic row: float4 fields round-trip through float32, stawidth is
// int(AvgWidth+0.5), and the signed stadistinct splits by
// catalog.ColumnStatsDistinct.
func reloadForm(cs catalog.ColumnStats) catalog.ColumnStats {
	out := catalog.ColumnStats{
		NullFrac:    float64(float32(cs.NullFrac)),
		AvgWidth:    float64(int64(cs.AvgWidth + 0.5)),
		Correlation: float64(float32(cs.Correlation)),
		Histogram:   cs.Histogram,
	}
	out.NDistinct, out.NDistinctFrac = catalog.ColumnStatsDistinct(float64(float32(cs.StaDistinct())))
	for _, e := range cs.MCV {
		out.MCV = append(out.MCV, catalog.MCVEntry{Value: e.Value, Frequency: float64(float32(e.Frequency))})
	}
	return out
}

func sqlLit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// TestPgRestoreAttributeStatsRoundTrip pins pg_restore_attribute_stats
// (M0146-0215) against goopg's own statistics. The test re-imports every column
// of an ANALYZEd table from its pg_stats text, which is what pg_dump
// --statistics reads, into a table whose statistics were wiped. The result must
// equal what a server restart reconstructs from ANALYZE's pg_statistic rows.
// This checks the value input (text_to_stavalues: through the column type,
// typmod applied, rendered as ANALYZE renders), the float4 rounding and the
// n_distinct sign convention together, for int, numeric(p,s), text, char(n),
// date, varchar(n) and float8.
func TestPgRestoreAttributeStatsRoundTrip(t *testing.T) {
	ctx, tbl, cleanup := statsImportFixture(t)
	defer cleanup()
	im := ctx.Catalog.(*catalog.InMemory)
	rows := im.PGStatsRowsForDBOid(catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	want := make([]catalog.ColumnStats, len(tbl.Columns))
	for i, cs := range tbl.Stats.Columns {
		want[i] = reloadForm(cs)
	}
	ctx.Catalog.SetTableStats(tbl, nil)

	imported := 0
	for _, r := range rows {
		if r[1] != "simp" {
			continue
		}
		args := []string{"'schemaname'", sqlLit(r[0]), "'relname'", sqlLit(r[1]), "'attname'", sqlLit(r[2]),
			"'inherited'", "false"}
		add := func(name, val, cast string) {
			if val == catalog.VirtualNull {
				return
			}
			args = append(args, sqlLit(name), sqlLit(val)+"::"+cast)
		}
		add("null_frac", r[4], "real")
		add("avg_width", r[5], "integer")
		add("n_distinct", r[6], "real")
		add("most_common_vals", r[7], "text")
		add("most_common_freqs", r[8], "real[]")
		add("histogram_bounds", r[9], "text")
		add("correlation", r[10], "real")
		got, err := runQueryWithErr(ctx, "SELECT pg_restore_attribute_stats("+strings.Join(args, ", ")+")")
		if err != nil {
			t.Fatalf("%s: %v", r[2], err)
		}
		if len(got) != 1 || !got[0][0].BoolValue() {
			t.Fatalf("%s: returned %v, warnings %v", r[2], renderRows(got), ctx.TakeWarnings())
		}
		imported++
	}
	if imported != len(tbl.Columns) {
		t.Fatalf("imported %d columns, want %d", imported, len(tbl.Columns))
	}
	if w := ctx.TakeWarnings(); len(w) != 0 {
		t.Fatalf("unexpected warnings: %v", w)
	}
	for i := range tbl.Columns {
		if got := tbl.Stats.Columns[i]; !reflect.DeepEqual(got, want[i]) {
			t.Errorf("%s:\n got  %+v\n want %+v", tbl.Columns[i].Name, got, want[i])
		}
	}
	if len(want[0].MCV) == 0 || len(want[1].Histogram) == 0 {
		t.Fatalf("fixture produced no MCV/histogram to compare (i MCV %d, n hist %d)", len(want[0].MCV), len(want[1].Histogram))
	}
}

// TestPgRestoreAttributeStatsSemantics pins attribute_statistics_update's
// contract case by case: the merge into existing statistics, the WARNING-and-
// false paths, the ERRORs, value input through the column type, and the
// n_distinct sign convention.
func TestPgRestoreAttributeStatsSemantics(t *testing.T) {
	ctx, tbl, cleanup := statsImportFixture(t)
	defer cleanup()
	call := func(args string) (bool, []string, error) {
		rows, err := runQueryWithErr(ctx, "SELECT pg_restore_attribute_stats("+args+")")
		w := ctx.TakeWarnings()
		if err != nil {
			return false, w, err
		}
		return rows[0][0].BoolValue(), w, nil
	}
	base := "'schemaname','public','relname','simp','attname','i','inherited',false"
	before := tbl.Stats.Columns[0]

	t.Run("merge keeps unspecified fields", func(t *testing.T) {
		ok, w, err := call(base + ",'null_frac',0.25::real")
		if err != nil || !ok || len(w) != 0 {
			t.Fatalf("ok=%v w=%v err=%v", ok, w, err)
		}
		got := tbl.Stats.Columns[0]
		if got.NullFrac != 0.25 || !reflect.DeepEqual(got.MCV, before.MCV) || !reflect.DeepEqual(got.Histogram, before.Histogram) {
			t.Fatalf("got %+v; MCV/histogram must be kept from %+v", got, before)
		}
	})
	t.Run("n_distinct sign convention", func(t *testing.T) {
		if ok, _, err := call(base + ",'n_distinct',-0.5::real"); err != nil || !ok {
			t.Fatal(err)
		}
		if cs := tbl.Stats.Columns[0]; cs.NDistinct != 0 || cs.NDistinctFrac != 0.5 || cs.StaDistinct() != -0.5 {
			t.Fatalf("negative n_distinct: %+v", cs)
		}
		if ok, _, err := call(base + ",'n_distinct',42::real"); err != nil || !ok {
			t.Fatal(err)
		}
		if cs := tbl.Stats.Columns[0]; cs.NDistinct != 42 || cs.NDistinctFrac != 0 {
			t.Fatalf("positive n_distinct: %+v", cs)
		}
	})
	t.Run("char(n) values are padded by the column's input", func(t *testing.T) {
		ok, w, err := call("'schemaname','public','relname','simp','attname','c','inherited',false," +
			"'most_common_vals','{x,yy}'::text,'most_common_freqs','{0.5,0.25}'::real[]")
		if err != nil || !ok || len(w) != 0 {
			t.Fatalf("ok=%v w=%v err=%v", ok, w, err)
		}
		mcv := tbl.Stats.Columns[3].MCV
		if len(mcv) != 2 || mcv[0].Value != "x    " || mcv[1].Value != "yy   " || mcv[1].Frequency != 0.25 {
			t.Fatalf("MCV %+v", mcv)
		}
	})
	t.Run("invalid value input is a warning and leaves the slot", func(t *testing.T) {
		prev := tbl.Stats.Columns[0].MCV
		ok, w, err := call(base + ",'most_common_vals','{1,abc}'::text,'most_common_freqs','{0.5,0.25}'::real[]")
		if err != nil || ok || len(w) != 1 {
			t.Fatalf("ok=%v w=%v err=%v; want false with one warning", ok, w, err)
		}
		if !reflect.DeepEqual(tbl.Stats.Columns[0].MCV, prev) {
			t.Fatal("a rejected MCV must not replace the existing one")
		}
	})
	for _, tc := range []struct {
		name, args, warn string
	}{
		{"unknown argument", base + ",'frobnitz',1::integer", `unrecognized argument name: "frobnitz"`},
		{"wrong type", base + ",'null_frac',0.5::float8", `argument "null_frac" has type double precision, expected type real`},
		{"mcv without freqs", base + ",'most_common_vals','{1}'::text", `argument "most_common_freqs" must be specified when argument "most_common_vals" is specified`},
		{"null in freqs", base + ",'most_common_vals','{1}'::text,'most_common_freqs','{NULL}'::real[]", `argument "most_common_freqs" array must not contain null values`},
		{"range stats on a non-range column", base + ",'range_bounds_histogram','{x}'::text", `column "i" is not a range type`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, w, err := call(tc.args)
			if err != nil || ok || len(w) != 1 || w[0] != tc.warn {
				t.Fatalf("ok=%v w=%q err=%v; want false with %q", ok, w, err, tc.warn)
			}
		})
	}
	for _, tc := range []struct {
		name, args, msg string
	}{
		{"odd argument count", "'schemaname'", "variadic arguments must be name/value pairs"},
		{"missing inherited", "'schemaname','public','relname','simp','attname','i'", `argument "inherited" must not be null`},
		{"attname and attnum", base + ",'attnum',1::smallint", `cannot specify both "attname" and "attnum"`},
		{"neither attname nor attnum", "'schemaname','public','relname','simp','inherited',false", `must specify either "attname" or "attnum"`},
		{"unknown column", "'schemaname','public','relname','simp','attname','zz','inherited',false", `column "zz" of relation "simp" does not exist`},
		{"system column", "'schemaname','public','relname','simp','attname','ctid','inherited',false", `cannot modify statistics on system column "ctid"`},
		{"unknown relation", "'schemaname','public','relname','nosuch','attname','i','inherited',false", `relation "public.nosuch" does not exist`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := call(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("err=%v, want %q", err, tc.msg)
			}
		})
	}
	t.Run("attnum addresses the same column", func(t *testing.T) {
		if ok, _, err := call("'schemaname','public','relname','simp','attnum',1::smallint,'inherited',false,'null_frac',0.125::real"); err != nil || !ok {
			t.Fatal(err)
		}
		if tbl.Stats.Columns[0].NullFrac != 0.125 {
			t.Fatalf("null_frac %v", tbl.Stats.Columns[0].NullFrac)
		}
	})
}

// TestPgRestoreRelationStats pins relation_statistics_update: relpages and
// reltuples land in TableStats, the reltuples range check warns, an index
// name is accepted, and the FROM-position call pg_dump emits plans.
func TestPgRestoreRelationStats(t *testing.T) {
	ctx, tbl, cleanup := statsImportFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE INDEX simp_i ON simp (i)"); err != nil {
		t.Fatal(err)
	}
	cols := tbl.Stats.Columns
	rows, err := runQueryWithErr(ctx, "SELECT * FROM pg_catalog.pg_restore_relation_stats('version','180003'::integer,"+
		"'schemaname','public','relname','simp','relpages','77'::integer,'reltuples','12345'::real,'relallvisible','3'::integer)")
	if err != nil || len(rows) != 1 || !rows[0][0].BoolValue() {
		t.Fatalf("rows=%v err=%v", renderRows(rows), err)
	}
	if s := tbl.Stats; s.Pages != 77 || s.RowCount != 12345 || !s.Analyzed || !reflect.DeepEqual(s.Columns, cols) {
		t.Fatalf("stats %+v; column statistics must be kept", s)
	}
	rows, err = runQueryWithErr(ctx, "SELECT pg_restore_relation_stats('schemaname','public','relname','simp','reltuples',-2::real)")
	if err != nil || rows[0][0].BoolValue() {
		t.Fatalf("reltuples -2: rows=%v err=%v", renderRows(rows), err)
	}
	if w := ctx.TakeWarnings(); len(w) != 1 || w[0] != `argument "reltuples" must not be less than -1.0` {
		t.Fatalf("warnings %q", w)
	}
	rows, err = runQueryWithErr(ctx, "SELECT pg_restore_relation_stats('schemaname','public','relname','simp_i','relpages',5::integer)")
	if err != nil || !rows[0][0].BoolValue() {
		t.Fatalf("index: rows=%v err=%v", renderRows(rows), err)
	}
	if tbl.Stats.Pages != 77 {
		t.Fatalf("an index's relation stats must not touch its table: pages %d", tbl.Stats.Pages)
	}
}

// TestImportedNullFracReachesPlanner pins that the planner reads imported
// statistics: an IS NULL estimate follows the imported null_frac.
func TestImportedNullFracReachesPlanner(t *testing.T) {
	ctx, _, cleanup := statsImportFixture(t)
	defer cleanup()
	for _, q := range []string{
		"SELECT pg_restore_relation_stats('schemaname','public','relname','simp','reltuples',10000::real)",
		"SELECT pg_restore_attribute_stats('schemaname','public','relname','simp','attname','s','inherited',false,'null_frac',0.3::real)",
	} {
		if _, err := runQueryWithErr(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := runQueryWithErr(ctx, "EXPLAIN SELECT * FROM simp WHERE s IS NULL")
	if err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(renderRows(rows), "\n")
	if !strings.Contains(plan, "rows=3000 ") {
		t.Fatalf("want rows=3000 (10000 x null_frac 0.3):\n%s", plan)
	}
}
