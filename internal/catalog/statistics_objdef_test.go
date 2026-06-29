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
		exprs   []string
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
			// Expression present but not captured (parse fallback) → decline so the
			// dump path never silently drops it. DU-002 slice 316.
			name:    "expr-target-uncaptured-declined",
			cols:    nil,
			hasExpr: true,
			want:    "",
		},
		{
			// Single expression target: ncolumns==1 so the kinds clause is
			// suppressed (it must be expression stats). DU-002 slice 316.
			name:    "single-expr-default-kinds",
			exprs:   []string{"(a + b)"},
			hasExpr: true,
			want:    "CREATE STATISTICS public.s ON (a + b) FROM public.t",
		},
		{
			// PG emits simple columns first, then expressions. DU-002 slice 316.
			name:    "col-plus-expr",
			cols:    []string{"a"},
			exprs:   []string{"(b + c)"},
			hasExpr: true,
			want:    "CREATE STATISTICS public.s ON a, (b + c) FROM public.t",
		},
		{
			// A disabled kind with ncolumns>1 (col + expr) re-emits the kinds
			// clause. DU-002 slice 316.
			name:    "single-kind-col-plus-expr",
			kinds:   []string{"ndistinct"},
			cols:    []string{"a"},
			exprs:   []string{"(b + c)"},
			hasExpr: true,
			want:    "CREATE STATISTICS public.s (ndistinct) ON a, (b + c) FROM public.t",
		},
		{
			// A disabled kind but a single expression target (ncolumns==1) still
			// suppresses the kinds clause. DU-002 slice 316.
			name:    "single-kind-single-expr-suppressed",
			kinds:   []string{"mcv"},
			exprs:   []string{"(a + b)"},
			hasExpr: true,
			want:    "CREATE STATISTICS public.s ON (a + b) FROM public.t",
		},
		{
			// A bare function-call expression stays unparenthesized (the executor's
			// deparser already rendered it that way). DU-002 slice 316.
			name:    "function-expr",
			exprs:   []string{"lower(a)"},
			hasExpr: true,
			want:    "CREATE STATISTICS public.s ON lower(a) FROM public.t",
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
				Exprs:    tc.exprs,
				HasExpr:  tc.hasExpr,
			}
			if got := c.BuildStatisticsObjDef(obj); got != tc.want {
				t.Errorf("BuildStatisticsObjDef = %q, want %q", got, tc.want)
			}
		})
	}
}
