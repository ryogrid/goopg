package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestDropSchemaCascadeDropsForeignSchemaPartitions pins PG 18.3: a
// partition is an AUTO dependency of its partitioned parent, so DROP SCHEMA
// s1 CASCADE drops public.t_1 when it is a partition of s1.t — and a new
// public.t_1 can then be created (031_column_list.pl does exactly that).
// M0149-0060.
func TestDropSchemaCascadeDropsForeignSchemaPartitions(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE SCHEMA s1")
	runSQL(t, ctx, "CREATE TABLE s1.t (a int, b int) PARTITION BY RANGE (a)")
	runSQL(t, ctx, "CREATE TABLE t_1 PARTITION OF s1.t FOR VALUES FROM (1) TO (10)")
	runSQL(t, ctx, "DROP SCHEMA s1 CASCADE")
	ps := optimizer.DefaultPlannerSettings()
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "SELECT relname FROM pg_class WHERE relname IN ('t', 't_1')", ps)), ","); got != "" {
		t.Errorf("DROP SCHEMA s1 CASCADE left %q", got)
	}
	if err := runDDL(t, ctx, "CREATE TABLE t_1 (a int)"); err != nil {
		t.Errorf("re-creating t_1 after the cascade: %v", err)
	}
}

// TestCopyTextEnumAndIntSend pins two output functions 031_column_list.pl
// relies on: enum_out in COPY TO (a user-defined enum column failed "kind 9
// cannot encode") and int2send / int4send / int8send (network byte order).
// M0149-0060.
func TestCopyTextEnumAndIntSend(t *testing.T) {
	got, err := datumToCopyText(catalog.Type{Name: "test_typ"}, NewEnumDatum(2, "red"), "ISO", "MDY", "UTC", "hex", nil, false)
	if err != nil || got != "red" {
		t.Errorf("COPY TEXT of enum = %q, %v; want red", got, err)
	}
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	rows := runSQL(t, ctx, "SELECT int2send(2::smallint), int4send(258), int8send(-1::bigint)")
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	for i, want := range [][]byte{{0, 2}, {0, 0, 1, 2}, {255, 255, 255, 255, 255, 255, 255, 255}} {
		if g := rows[0][i].BytesValue(); string(g) != string(want) {
			t.Errorf("column %d = %v, want %v", i, g, want)
		}
	}
}

// TestAlterPublicationColumnLists pins AlterPublicationTables' column lists:
// ADD gives the table its list, SET replaces it (a table listed without one
// publishes every column), and DROP refuses a list. M0149-0060.
func TestAlterPublicationColumnLists(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	ctx.PubSub = catalog.NewPubSub()
	for _, q := range []string{
		"CREATE TABLE cl_t (a int, b int, c int)",
		"CREATE TABLE cl_u (a int, b int)",
		"CREATE PUBLICATION cl_p FOR TABLE cl_u",
		"ALTER PUBLICATION cl_p ADD TABLE cl_t (c, a)",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	pub, _ := ctx.PubSub.LookupPublication("cl_p")
	if got := strings.Join(pub.Columns[pub.Tables[1]], ","); got != "a,c" {
		t.Errorf("after ADD: list %q, want a,c (attnum order)", got)
	}
	if err := runDDL(t, ctx, "ALTER PUBLICATION cl_p SET TABLE cl_t, cl_u (b)"); err != nil {
		t.Fatal(err)
	}
	pub, _ = ctx.PubSub.LookupPublication("cl_p")
	for _, q := range pub.Tables {
		cols, has := pub.Columns[q]
		switch {
		case strings.HasSuffix(q, "cl_t") && has:
			t.Errorf("SET without a list kept %v on cl_t", cols)
		case strings.HasSuffix(q, "cl_u") && strings.Join(cols, ",") != "b":
			t.Errorf("SET cl_u (b): list %v", cols)
		}
	}
	err := runDDL(t, ctx, "ALTER PUBLICATION cl_p DROP TABLE cl_u (b)")
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42601" {
		t.Errorf("DROP with a column list: err = %v, want 42601", err)
	}
}
