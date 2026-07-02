package catalog

import "testing"

// TestBuildIndexDefColOpClass pins BuildIndexDef's per-column operator-class
// rendering (DU-002 slice 312), mirroring ruleutils.c pg_get_indexdef_worker:
// get_opclass_name emits a non-default operator class after the column/expression
// (and after any COLLATE) and before the ASC/DESC ordering. goopg records only an
// explicitly-written opclass, so a non-empty ColOpClasses entry is by construction
// non-default and always emitted. An empty entry (or empty slice) leaves the
// default opclass implicit so a plain index dumps byte-identically.
func TestBuildIndexDefColOpClass(t *testing.T) {
	tbl := &Table{Schema: "public", Name: "foo"}
	cases := []struct {
		name    string
		cols    []string
		opc     []string
		desc    []bool
		nullsFC []bool
		want    string
	}{
		{
			name: "single_opclass",
			cols: []string{"name"}, opc: []string{"text_pattern_ops"},
			want: "CREATE INDEX i ON public.foo USING btree (name text_pattern_ops)",
		},
		{
			name: "opclass_then_desc",
			cols: []string{"name"}, opc: []string{"text_pattern_ops"},
			desc: []bool{true}, nullsFC: []bool{false},
			want: "CREATE INDEX i ON public.foo USING btree (name text_pattern_ops DESC NULLS LAST)",
		},
		{
			name: "mixed_one_default_one_opclass",
			cols: []string{"a", "b"}, opc: []string{"", "varchar_pattern_ops"},
			want: "CREATE INDEX i ON public.foo USING btree (a, b varchar_pattern_ops)",
		},
		{
			name: "empty_slice_no_opclass",
			cols: []string{"a"},
			want: "CREATE INDEX i ON public.foo USING btree (a)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx := &Index{
				Name:          "i",
				Schema:        "public",
				Table:         tbl,
				Columns:       c.cols,
				Method:        "btree",
				ColOpClasses:  c.opc,
				ColDescending: c.desc,
				ColNullsFirst: c.nullsFC,
			}
			if got := BuildIndexDef(idx); got != c.want {
				t.Errorf("BuildIndexDef = %q, want %q", got, c.want)
			}
		})
	}
}
