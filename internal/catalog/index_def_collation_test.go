package catalog

import "testing"

// TestBuildIndexDefColCollation pins BuildIndexDef's per-column COLLATE rendering
// (DU-002 slice 313), mirroring ruleutils.c pg_get_indexdef_worker: a non-default
// collation is emitted after the column/expression and BEFORE the operator class
// (and before the ASC/DESC ordering), via generate_collation_name which quotes the
// collname as an identifier. goopg records only an explicitly-written collation, so
// a non-empty ColCollations entry is by construction non-default and always
// emitted. An empty entry (or empty slice) leaves the default collation implicit so
// a plain index dumps byte-identically.
func TestBuildIndexDefColCollation(t *testing.T) {
	tbl := &Table{Schema: "public", Name: "foo"}
	cases := []struct {
		name    string
		cols    []string
		coll    []string
		opc     []string
		desc    []bool
		nullsFC []bool
		want    string
	}{
		{
			name: "single_collation_quoted",
			cols: []string{"name"}, coll: []string{"C"},
			want: `CREATE INDEX i ON public.foo USING btree (name COLLATE "C")`,
		},
		{
			name: "collation_lowercase_bare",
			cols: []string{"name"}, coll: []string{"ucs_basic"},
			want: "CREATE INDEX i ON public.foo USING btree (name COLLATE ucs_basic)",
		},
		{
			name: "collation_before_opclass",
			cols: []string{"name"}, coll: []string{"C"}, opc: []string{"text_pattern_ops"},
			want: `CREATE INDEX i ON public.foo USING btree (name COLLATE "C" text_pattern_ops)`,
		},
		{
			name: "collation_then_desc",
			cols: []string{"name"}, coll: []string{"C"},
			desc: []bool{true}, nullsFC: []bool{false},
			want: `CREATE INDEX i ON public.foo USING btree (name COLLATE "C" DESC NULLS LAST)`,
		},
		{
			name: "mixed_one_default_one_collation",
			cols: []string{"a", "b"}, coll: []string{"", "C"},
			want: `CREATE INDEX i ON public.foo USING btree (a, b COLLATE "C")`,
		},
		{
			name: "empty_slice_no_collation",
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
				ColCollations: c.coll,
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
