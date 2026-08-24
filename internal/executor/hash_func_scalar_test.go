package executor

import "testing"

// TestHashFuncScalarFamily covers the M0134-0128 slice: the SQL-callable
// hash*() / hash*extended() pair for the plain scalar types that reduce to
// PG's hash_uint32/hash_bytes primitives (hashint2/4/8, hashoid, hashchar,
// hashfloat4/8, hashname/text/bpchar). postgres/src/test/regress/sql/
// hash_func.sql asserts each pair is internally consistent — the extended
// variant with seed=0 must agree with the plain hash's low 32 bits, and with
// seed=1 must differ — rather than pinning fixed hash values, so that
// invariant is what this test checks too.
func TestHashFuncScalarFamily(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		std  string // hash*(...) expression
		ext  string // hash*extended(..., seed) expression, %d for seed
	}{
		{"hashint2", "hashint2(42::int2)", "hashint2extended(42::int2, %d)"},
		{"hashint4", "hashint4(42)", "hashint4extended(42, %d)"},
		{"hashint4_neg", "hashint4(-42)", "hashint4extended(-42, %d)"},
		{"hashint8", "hashint8(207112489::int8)", "hashint8extended(207112489::int8, %d)"},
		{"hashint8_neg", "hashint8(-207112489::int8)", "hashint8extended(-207112489::int8, %d)"},
		{"hashoid", "hashoid(42::oid)", "hashoidextended(42::oid, %d)"},
		{"hashchar", "hashchar('x'::\"char\")", "hashcharextended('x'::\"char\", %d)"},
		{"hashfloat4", "hashfloat4(1.5::float4)", "hashfloat4extended(1.5::float4, %d)"},
		{"hashfloat4_zero", "hashfloat4('-0'::float4)", "hashfloat4extended('-0'::float4, %d)"},
		{"hashfloat8", "hashfloat8(1.5::float8)", "hashfloat8extended(1.5::float8, %d)"},
		{"hashname", "hashname('PostgreSQL'::name)", "hashnameextended('PostgreSQL'::name, %d)"},
		{"hashtext", "hashtext('PostgreSQL')", "hashtextextended('PostgreSQL', %d)"},
		{"hashbpchar", "hashbpchar('PostgreSQL')", "hashbpcharextended('PostgreSQL', %d)"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			std := runQuery(t, ctx, "SELECT "+c.std+"::bit(32)")
			ext0 := runQuery(t, ctx, "SELECT "+sprintfSeed(c.ext, 0)+"::bit(32)")
			ext1 := runQuery(t, ctx, "SELECT "+sprintfSeed(c.ext, 1)+"::bit(32)")
			stdV, ext0V, ext1V := std[0][0].StringValue(), ext0[0][0].StringValue(), ext1[0][0].StringValue()
			if stdV != ext0V {
				t.Errorf("%s: standard hash %v != extended(seed=0) %v", c.name, stdV, ext0V)
			}
			if stdV == ext1V {
				t.Errorf("%s: standard hash %v == extended(seed=1) %v, want different", c.name, stdV, ext1V)
			}
		})
	}
}

func sprintfSeed(pattern string, seed int) string {
	out := make([]byte, 0, len(pattern)+2)
	for i := 0; i < len(pattern); i++ {
		if i+1 < len(pattern) && pattern[i] == '%' && pattern[i+1] == 'd' {
			if seed == 0 {
				out = append(out, '0')
			} else {
				out = append(out, '1')
			}
			i++
			continue
		}
		out = append(out, pattern[i])
	}
	return string(out)
}

// TestIntToBitTypmodCast covers the general integer::bit(n) cast fix
// (M0134-0128): PG's bitfromint4/bitfromint8 render the two's-complement bit
// pattern, not a decimal string — every hash*()::bit(32) probe in
// hash_func.sql depends on this.
func TestIntToBitTypmodCast(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT 5::bit(32)", "00000000000000000000000000000101"},
		{"SELECT 0::bit(32)", "00000000000000000000000000000000"},
		{"SELECT (-1)::bit(32)", "11111111111111111111111111111111"},
		{"SELECT 255::bit(8)", "11111111"},
		{"SELECT 5::int8::bit(32)", "00000000000000000000000000000101"},
	}
	for _, c := range cases {
		got := runQuery(t, ctx, c.sql)
		if gotV := got[0][0].StringValue(); gotV != c.want {
			t.Errorf("%s: got %q, want %q", c.sql, gotV, c.want)
		}
	}
}
