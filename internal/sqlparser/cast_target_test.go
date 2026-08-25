package sqlparser

import "testing"

func TestCastTargetTypes(t *testing.T) {
	for _, q := range []string{
		"SELECT '3'::float",
		"SELECT '3'::float(24)",
		"SELECT 'x'::char",
		"SELECT 'x'::char(3)",
		"SELECT x::double precision FROM t",
		"SELECT x::character varying(10) FROM t",
		"SELECT x::bit varying FROM t",
		"SELECT ts::timestamp with time zone FROM t",
		"SELECT t::time without time zone FROM t",
		"SELECT n::national character varying(8) FROM t",
		"SELECT x::numeric(10,2) FROM t",
	} {
		l, n, err := diffParse(q)
		if err != nil {
			t.Errorf("%q -> %v", q, err)
		} else if l != n {
			t.Errorf("DIFF %q\n L=%s\n N=%s", q, l, n)
		}
	}
}
