package catalog

import "testing"

// TestBuildStatisticsObjDef pins the CREATE STATISTICS reconstruction used by
// pg_get_statisticsobjdef → pg_dump (DU-002 slice 314). It mirrors ruleutils.c
// pg_get_statisticsobj_worker: the kinds clause is suppressed when all three
// kinds are enabled (the default) or the object spans a single column; the FROM
// relation is schema-qualified.
func TestBuildStatisticsObjDef(t *testing.T) {
	c := NewInMemory()
	const relOID = uint32(16400)
	tbl := &Table{Schema: "public", Name: "t", OID: relOID}
	c.RegisterTable(tbl)

	cases := []struct {
		name    string
		kinds   []string
		cols    []string
		hasExpr bool
		want    string
	}{
		{
			name: "default-kinds-multicol",
			cols: []string{"a", "b"},
			want: "CREATE STATISTICS public.s ON a, b FROM public.t",
		},
		{
			name:  "single-kind-multicol",
			kinds: []string{"ndistinct"},
			cols:  []string{"a", "b"},
			want:  "CREATE STATISTICS public.s (ndistinct) ON a, b FROM public.t",
		},
		{
			name:  "two-kinds-multicol",
			kinds: []string{"ndistinct", "mcv"},
			cols:  []string{"a", "b"},
			want:  "CREATE STATISTICS public.s (ndistinct, mcv) ON a, b FROM public.t",
		},
		{
			name:  "all-kinds-explicit-suppressed",
			kinds: []string{"ndistinct", "dependencies", "mcv"},
			cols:  []string{"a", "b"},
			want:  "CREATE STATISTICS public.s ON a, b FROM public.t",
		},
		{
			name:    "expr-target-declined",
			cols:    nil,
			hasExpr: true,
			want:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := &StatisticsObject{
				Name:     "s",
				Schema:   "public",
				OID:      99999,
				TableOID: relOID,
				Kinds:    tc.kinds,
				Columns:  tc.cols,
				HasExpr:  tc.hasExpr,
			}
			if got := c.BuildStatisticsObjDef(obj); got != tc.want {
				t.Errorf("BuildStatisticsObjDef = %q, want %q", got, tc.want)
			}
		})
	}
}
