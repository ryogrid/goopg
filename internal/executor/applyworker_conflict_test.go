package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestApplyConflictErrorFormat pins ReportApplyConflict's text (conflict.c)
// as 035_conflicts.pl matches it: the errmsg, one "Key already exists"
// sentence plus one "Key …; existing local row …; remote row …." line per
// violated index, and the server-log rendering with tab-indented
// continuation lines. M0149-0056a.
func TestApplyConflictErrorFormat(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
		{Name: "c", Type: catalog.Type{Name: "int4"}, Ordinal: 2},
	}
	tbl := &catalog.Table{Name: "conf_tab", Columns: cols}
	local := &applyFoundTuple{row: Row{NewIntDatum(2), NewIntDatum(2), NullDatum}, xmin: 757}
	remote := Row{NewIntDatum(2), NewIntDatum(3), NewIntDatum(4)}
	one := []applyConflict{{idx: &catalog.Index{Name: "conf_tab_pkey", Columns: []string{"a"}}, local: local}}
	ctx := NewContext()
	ee := applyConflictError(ctx, tbl, cols, "insert_exists", one, remote)
	if ee.Code != "23505" || ee.Message != `conflict detected on relation "public.conf_tab": conflict=insert_exists` {
		t.Fatalf("single conflict: %s %q", ee.Code, ee.Message)
	}
	wantDetail := "Key already exists in unique index \"conf_tab_pkey\", modified in transaction 757.\n" +
		"Key (a)=(2); existing local row (2, 2, null); remote row (2, 3, 4)."
	if ee.Detail != wantDetail {
		t.Fatalf("detail = %q, want %q", ee.Detail, wantDetail)
	}
	two := append(one, applyConflict{idx: &catalog.Index{Name: "conf_tab_a_b_key", Columns: []string{"a", "b"}}, local: local})
	ee = applyConflictError(ctx, tbl, cols, "update_exists", two, remote)
	if ee.Message != `conflict detected on relation "public.conf_tab": conflict=multiple_unique_conflicts` {
		t.Fatalf("two conflicts: %q", ee.Message)
	}
	log := pgServerLogText(ee)
	wantLog := `conflict detected on relation "public.conf_tab": conflict=multiple_unique_conflicts` + "\n" +
		"DETAIL:  Key already exists in unique index \"conf_tab_pkey\", modified in transaction 757.\n" +
		"\tKey (a)=(2); existing local row (2, 2, null); remote row (2, 3, 4).\n" +
		"\tKey already exists in unique index \"conf_tab_a_b_key\", modified in transaction 757.\n" +
		"\tKey (a, b)=(2, 2); existing local row (2, 2, null); remote row (2, 3, 4)."
	if log != wantLog {
		t.Fatalf("server log =\n%s\nwant\n%s", log, wantLog)
	}
	if _, ok := isApplyConflictError(ee); !ok {
		t.Fatal("isApplyConflictError rejected a conflict error")
	}
}

// TestPartitionCloneIndexName: a partition's clone of a parent unique index
// is named by ChooseIndexName — every key column — not "<child>_key".
// M0149-0056c.
func TestPartitionCloneIndexName(t *testing.T) {
	for _, c := range []struct {
		idx  *catalog.Index
		want string
	}{
		{&catalog.Index{Primary: true, Columns: []string{"a"}}, "conf_tab_2_p1_pkey"},
		{&catalog.Index{Unique: true, Columns: []string{"a", "b"}}, "conf_tab_2_p1_a_b_key"},
		{&catalog.Index{Unique: true, Columns: []string{"b"}}, "conf_tab_2_p1_b_key"},
		{&catalog.Index{Unique: true, Columns: []string{""}}, "conf_tab_2_p1_expr_key"},
	} {
		if got := partitionCloneIndexName("conf_tab_2_p1", c.idx); got != c.want {
			t.Errorf("partitionCloneIndexName(%v) = %q, want %q", c.idx.Columns, got, c.want)
		}
	}
}
