package executor

import "testing"

// TestApplyViewColumnAliases covers the raw-text alias splicing used by
// pg_get_viewdef when a view was created with an explicit column list
// (`CREATE VIEW v (c1, c2, …) AS …`). DU-002 slice 58 (M0110-0001).
func TestApplyViewColumnAliases(t *testing.T) {
	cases := []struct {
		name    string
		rawDef  string
		aliases []string
		want    string
	}{
		{
			name:    "simple column refs",
			rawDef:  "SELECT id, name FROM public.foo",
			aliases: []string{"col_a", "col_b"},
			want:    "SELECT id AS col_a, name AS col_b FROM public.foo",
		},
		{
			name:    "with where clause preserved",
			rawDef:  "SELECT id, name FROM public.foo WHERE qty > 0",
			aliases: []string{"a", "b"},
			want:    "SELECT id AS a, name AS b FROM public.foo WHERE qty > 0",
		},
		{
			name:    "no from clause",
			rawDef:  "SELECT 1, 2",
			aliases: []string{"x", "y"},
			want:    "SELECT 1 AS x, 2 AS y",
		},
		{
			name:    "function call with internal comma not split",
			rawDef:  "SELECT coalesce(a, b), c FROM t",
			aliases: []string{"first", "second"},
			want:    "SELECT coalesce(a, b) AS first, c AS second FROM t",
		},
		{
			name:    "EXTRACT with internal FROM not treated as clause boundary",
			rawDef:  "SELECT extract(year FROM ts), id FROM t",
			aliases: []string{"yr", "ident"},
			want:    "SELECT extract(year FROM ts) AS yr, id AS ident FROM t",
		},
		{
			name:    "alias needing quotes",
			rawDef:  "SELECT id FROM t",
			aliases: []string{"Mixed Case"},
			want:    `SELECT id AS "Mixed Case" FROM t`,
		},
		{
			name:    "reserved-keyword alias gets quoted",
			rawDef:  "SELECT id FROM t",
			aliases: []string{"select"},
			want:    `SELECT id AS "select" FROM t`,
		},
		{
			name:    "count mismatch bails to raw",
			rawDef:  "SELECT id, name, qty FROM t",
			aliases: []string{"a", "b"},
			want:    "SELECT id, name, qty FROM t",
		},
		{
			name:    "star bails to raw",
			rawDef:  "SELECT * FROM t",
			aliases: []string{"a"},
			want:    "SELECT * FROM t",
		},
		{
			name:    "qualified star bails to raw",
			rawDef:  "SELECT t.* FROM t",
			aliases: []string{"a"},
			want:    "SELECT t.* FROM t",
		},
		{
			name:    "existing AS alias bails to raw",
			rawDef:  "SELECT id AS already FROM t",
			aliases: []string{"a"},
			want:    "SELECT id AS already FROM t",
		},
		{
			name:    "empty alias list returns raw unchanged",
			rawDef:  "SELECT id, name FROM t",
			aliases: nil,
			want:    "SELECT id, name FROM t",
		},
		{
			name:    "lowercase select keyword still matched, casing preserved",
			rawDef:  "select id from t",
			aliases: []string{"a"},
			want:    "select id AS a from t",
		},
		{
			name:    "comma inside string literal not split",
			rawDef:  "SELECT 'x,y', id FROM t",
			aliases: []string{"lit", "ident"},
			want:    "SELECT 'x,y' AS lit, id AS ident FROM t",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyViewColumnAliases(tc.rawDef, tc.aliases)
			if got != tc.want {
				t.Fatalf("applyViewColumnAliases(%q, %v)\n  got  %q\n  want %q",
					tc.rawDef, tc.aliases, got, tc.want)
			}
		})
	}
}
