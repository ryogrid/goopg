package catalog

import "testing"

// TestBuildIndexDefColOrder pins BuildIndexDef's per-column ASC/DESC + NULLS
// rendering (DU-002 slice 56), mirroring ruleutils.c pg_get_indexdef_worker:
// DESC defaults to NULLS FIRST (print NULLS LAST when overridden); ASC defaults
// to NULLS LAST (print NULLS FIRST when set). Defaults are suppressed so a plain
// index dumps byte-identically to upstream pg_dump.
func TestBuildIndexDefColOrder(t *testing.T) {
	tbl := &Table{Schema: "public", Name: "foo"}
	cases := []struct {
		name    string
		cols    []string
		desc    []bool
		nullsFC []bool
		want    string
	}{
		{
			name: "plain_default",
			cols: []string{"name"},
			want: "CREATE INDEX i ON public.foo USING btree (name)",
		},
		{
			name: "desc_default_nulls_first_suppressed",
			cols: []string{"name"}, desc: []bool{true}, nullsFC: []bool{true},
			want: "CREATE INDEX i ON public.foo USING btree (name DESC)",
		},
		{
			name: "desc_nulls_last_override",
			cols: []string{"name"}, desc: []bool{true}, nullsFC: []bool{false},
			want: "CREATE INDEX i ON public.foo USING btree (name DESC NULLS LAST)",
		},
		{
			name: "asc_nulls_first_override",
			cols: []string{"qty"}, desc: []bool{false}, nullsFC: []bool{true},
			want: "CREATE INDEX i ON public.foo USING btree (qty NULLS FIRST)",
		},
		{
			name: "composite_mixed",
			cols: []string{"name", "qty"}, desc: []bool{true, false}, nullsFC: []bool{true, true},
			want: "CREATE INDEX i ON public.foo USING btree (name DESC, qty NULLS FIRST)",
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
				ColDescending: c.desc,
				ColNullsFirst: c.nullsFC,
			}
			if got := BuildIndexDef(idx); got != c.want {
				t.Errorf("BuildIndexDef = %q, want %q", got, c.want)
			}
		})
	}
}
